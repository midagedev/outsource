// Package slot is a machine-wide counting semaphore for heavy steps.
//
//	outsource slot [--max N] [--name <pool>] -- <cmd> [args...]
//	outsource slot --status [--name <pool>]
//
// Why it exists (field report from another lead session, 2026-10-06): about
// six live rounds, one per worktree, each ran its project's full
// check/lint/test on the same 10-core Mac. Load stayed at 23–28 and every
// round's proof steps crawled. Nothing in outsource knew the machine was
// shared. A round now wraps its heavy steps as `"$OUTSOURCE_SLOT" -- <cmd>`
// (the launcher exports that path to every harness child), at most N of them
// run at once across the machine, and the rest wait their turn.
//
// A pool is a directory of lock files,
// ${XDG_CACHE_HOME:-$HOME/.cache}/outsource/slots/<pool>/<i>.lock, and a
// holder is whoever has flock(LOCK_EX) on one of them. The kernel drops a
// flock when the last descriptor on it closes, a SIGKILLed holder included,
// so there is no stale-lock logic here to get wrong: no heartbeat, no age
// threshold, no pid check deciding that a slot is free. The record a holder
// writes inside its file (pid, label, start) is for people; whether a slot is
// held is decided by the lock, never by the record, because a killed holder
// leaves its record behind.
//
// N belongs to the caller, not to the pool. A caller with N tries files
// 0..N-1 in order and never looks above them, so callers that disagree about
// N share the low files and none of them exceeds its own budget: a caller with
// N=1 waits for file 0 even while file 1 is free.
//
// The lock descriptor is close-on-exec (Go opens every file that way), so the
// wrapped command does not inherit it. That is deliberate: a build that leaves
// a daemon behind (a compiler server, a test watcher) would otherwise hold the
// slot for as long as the daemon lives. The price is that SIGKILL of this
// process frees the slot while its command runs on, unthrottled. TERM, INT and
// HUP are forwarded to the command and this process exits only when the
// command does, so a tool timeout that signals before it kills leaves no
// orphan holding — or skipping — a slot.
package slot

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/midagedev/outsource/internal/human"
	"github.com/midagedev/outsource/internal/telemetry"
)

const (
	// ExitUsage: a bad flag, no command, an unusable pool name, or N < 1.
	ExitUsage = 64
	// Exit codes for a command that never started, the shell's convention.
	exitCannotRun = 126
	exitNotFound  = 127

	// DefaultPool is the pool a caller gets without --name.
	DefaultPool = "heavy"

	// pollInterval is how often a waiter retries. A handoff costs at most this
	// much latency, which is noise beside a heavy step's minutes.
	pollInterval = time.Second

	// heldEnvKey names the pools the wrapped command's ancestors already hold,
	// comma-separated. A call on a pool that is listed runs its command
	// straight through: the ancestor's slot already pays for it. Without this
	// a wrapped check script that wraps its own steps deadlocks — at N=1
	// (the default below eight cores) the inner call waits forever for the
	// slot its own parent holds, and at any N, N such outer holders do.
	heldEnvKey = "OUTSOURCE_SLOT_HELD"
)

const usage = `usage: outsource slot [--max N] [--name <pool>] -- <cmd> [args...]
       outsource slot --status [--name <pool>]

Runs <cmd> once one of N machine-wide slots in <pool> (default "heavy") is
free, and waits its turn otherwise. N is --max, else OUTSOURCE_SLOTS, else
max(1, cores/4). The command's exit code is this tool's exit code (128+n when
a signal killed it). --status lists every pool's holders and waiters.
`

// poolName keeps --name a single path component: it becomes a directory name.
var poolName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

type opts struct {
	max    string // --max as given; empty when absent
	pool   string
	named  bool // --name was given
	status bool
	cmd    []string
}

// Main is the slot tool.
func Main(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	o, rc, ok := parse(args, stdout, stderr)
	if !ok {
		return rc
	}
	if o.status {
		only := ""
		if o.named {
			only = o.pool
		}
		return status(Dir(), only, stdout, stderr)
	}
	if wrapsGit(o.cmd) {
		fmt.Fprintln(stderr, "outsource slot: git is not a heavy step; run it directly (and a round may not change git state)")
		return ExitUsage
	}

	n, err := slots(o.max, os.Getenv("OUTSOURCE_SLOTS"), runtime.NumCPU())
	if err != nil {
		fmt.Fprintf(stderr, "outsource slot: %v\n", err)
		return ExitUsage
	}

	// Resolved before taking a slot: a typo must not wait its turn to fail.
	path, err := exec.LookPath(o.cmd[0])
	if err != nil {
		fmt.Fprintf(stderr, "outsource slot: %v\n", err)
		if errors.Is(err, fs.ErrPermission) {
			return exitCannotRun
		}
		return exitNotFound
	}

	label := os.Getenv("OUTSOURCE_RUN_LABEL")
	if label == "" {
		label = filepath.Base(o.cmd[0])
	}

	// Registered before the wait, not after it: a timeout that TERMs a waiter
	// must still find its waiter file removed.
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	defer signal.Stop(sigs)

	env := os.Environ()
	held := os.Getenv(heldEnvKey)
	if !holds(held, o.pool) {
		lock, rc, ok := acquire(filepath.Join(Dir(), o.pool), o.pool, n, label, sigs, stderr)
		if !ok {
			return rc
		}
		if lock != nil {
			defer release(lock)
			if held != "" {
				held += ","
			}
			env = append(env, heldEnvKey+"="+held+o.pool)
		}
	}
	return runCommand(path, o.cmd, env, stdin, stdout, stderr, sigs)
}

func parse(args []string, stdout, stderr io.Writer) (opts, int, bool) {
	o := opts{pool: DefaultPool}
	bad := func(format string, a ...any) (opts, int, bool) {
		fmt.Fprintf(stderr, "outsource slot: "+format+"\n", a...)
		fmt.Fprint(stderr, usage)
		return o, ExitUsage, false
	}
	i := 0
flags:
	for ; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			i++
			break flags
		case a == "--max" || a == "--name":
			if i+1 >= len(args) || args[i+1] == "" {
				return bad("%s needs a value", a)
			}
			i++
			if a == "--max" {
				o.max = args[i]
			} else {
				o.pool, o.named = args[i], true
			}
		case strings.HasPrefix(a, "--max="):
			if o.max = strings.TrimPrefix(a, "--max="); o.max == "" {
				return bad("--max needs a value")
			}
		case strings.HasPrefix(a, "--name="):
			o.pool, o.named = strings.TrimPrefix(a, "--name="), true
		case a == "--status":
			o.status = true
		case a == "-h" || a == "--help":
			fmt.Fprint(stdout, usage)
			return o, 0, false
		case strings.HasPrefix(a, "-"):
			return bad("unknown flag: %s", a)
		default:
			// The command may start without `--`, the way nice and flock take
			// one; `--` is still the documented form, and the only one for a
			// command whose first word starts with '-'.
			break flags
		}
	}
	o.cmd = args[i:]
	if !poolName.MatchString(o.pool) {
		return bad("--name wants one path component of letters, digits, '.', '_' or '-', got %q", o.pool)
	}
	if o.status {
		if len(o.cmd) > 0 {
			return bad("--status takes no command")
		}
		return o, 0, true
	}
	if len(o.cmd) == 0 {
		return bad("no command to run")
	}
	return o, 0, true
}

// slots resolves N: --max wins, then OUTSOURCE_SLOTS, then max(1, cores/4).
//
// Why cores/4: the field case put about six concurrent full checks at load
// 23–28 on 10 cores, so one heavy step keeps roughly four cores runnable.
// cores/4 such steps hold the load near the core count — 2 on that machine.
// A value that is not a whole number ≥ 1 is refused rather than replaced by
// the default: a typo'd budget that silently became another number is worse
// than a step that refuses loudly.
func slots(flag, env string, cores int) (int, error) {
	src, v := "--max", flag
	if v == "" {
		src, v = "OUTSOURCE_SLOTS", env
	}
	if v == "" {
		return max(1, cores/4), nil
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n < 1 {
		return 0, fmt.Errorf("%s wants a whole number of slots, at least 1; got %q", src, v)
	}
	return n, nil
}

// Dir is the root of every pool: ${XDG_CACHE_HOME:-$HOME/.cache}/outsource/slots.
// It shares the dispatcher's cache base on purpose; the dispatcher's version
// pruning (bin/outsource prune_cache) skips every name that is not a version,
// so slots/ survives an update.
func Dir() string {
	base := os.Getenv("XDG_CACHE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			home = os.Getenv("HOME")
		}
		base = filepath.Join(home, ".cache")
	}
	return filepath.Join(base, "outsource", "slots")
}

// wrapsGit reports whether the wrapped command runs git, looking through the
// prefixes that are trivial to read: env (its options, NAME=value assignments
// and -S string), nice (its adjustment) and command (its -p/-v/-V), in any
// chain. Case-folded, because a case-insensitive filesystem runs GIT as git.
//
// Why the slot owns this: the preamble teaches rounds the form
// `"$OUTSOURCE_SLOT" -- <cmd>`, and the git guard (internal/guard) matches git
// only at a command boundary or after sudo/env, so `"$OUTSOURCE_SLOT" -- git
// commit` passed it. git is never a heavy step, so refusing it here costs a
// legitimate round nothing. Not read through, and so not refused: sh -c,
// xargs, timeout, nohup, sudo, a git under another name, and a script or
// build that runs git inside — the guard and the round's rules own those.
func wrapsGit(argv []string) bool {
	for len(argv) > 0 {
		switch strings.ToLower(filepath.Base(argv[0])) {
		case "git":
			return true
		case "env":
			argv = afterEnv(argv[1:])
		case "nice":
			argv = afterNice(argv[1:])
		case "command":
			argv = afterFlags(argv[1:])
		default:
			return false
		}
	}
	return false
}

// afterEnv is env's arguments with its options and assignments read past.
// -S's string is split on blanks and read on, which is all the quoting a
// trivial reading follows.
func afterEnv(args []string) []string {
	for len(args) > 0 {
		a := args[0]
		switch {
		case a == "--":
			return afterAssignments(args[1:])
		case a == "-u" || a == "-C" || a == "-P" || a == "--unset" || a == "--chdir":
			if len(args) < 2 {
				return nil
			}
			args = args[2:]
		case a == "-S" || a == "--split-string":
			if len(args) < 2 {
				return nil
			}
			args = append(strings.Fields(args[1]), args[2:]...)
		case strings.HasPrefix(a, "--split-string="):
			args = append(strings.Fields(strings.TrimPrefix(a, "--split-string=")), args[1:]...)
		case strings.HasPrefix(a, "-S"):
			args = append(strings.Fields(a[2:]), args[1:]...)
		case strings.HasPrefix(a, "-"):
			args = args[1:] // -i, -, -0, -v, -uNAME, --unset=NAME, --ignore-environment, …
		default:
			return afterAssignments(args)
		}
	}
	return nil
}

func afterAssignments(args []string) []string {
	for len(args) > 0 && strings.Contains(args[0], "=") {
		args = args[1:]
	}
	return args
}

// afterNice reads past -n N, --adjustment N and the joined forms (-n5, -5,
// --adjustment=5).
func afterNice(args []string) []string {
	for len(args) > 0 {
		a := args[0]
		switch {
		case a == "--":
			return args[1:]
		case a == "-n" || a == "--adjustment":
			if len(args) < 2 {
				return nil
			}
			args = args[2:]
		case strings.HasPrefix(a, "-") && len(a) > 1:
			args = args[1:]
		default:
			return args
		}
	}
	return nil
}

// afterFlags reads past flags that take no value, up to and including `--`.
func afterFlags(args []string) []string {
	for len(args) > 0 && strings.HasPrefix(args[0], "-") && len(args[0]) > 1 {
		if args[0] == "--" {
			return args[1:]
		}
		args = args[1:]
	}
	return args
}

func holds(held, pool string) bool {
	for _, p := range strings.Split(held, ",") {
		if p == pool {
			return true
		}
	}
	return false
}

func lockPath(dir string, i int) string { return filepath.Join(dir, strconv.Itoa(i)+".lock") }

// acquire takes one of files 0..n-1 in dir, waiting while all are held. It
// returns the locked file; a nil file with ok means the pool could not be used
// at all and the command runs without a slot (said on stderr). A signal while
// waiting returns 128+n with ok false.
//
// Failing open is the choice here because the slot is a courtesy to the other
// rounds, not a correctness boundary: an unwritable cache dir must not stop a
// round's proof step from running, only from being throttled.
func acquire(dir, pool string, n int, label string, sigs <-chan os.Signal, stderr io.Writer) (*os.File, int, bool) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintf(stderr, "outsource slot: cannot use %s (%v); running without a slot\n", dir, err)
		return nil, 0, true
	}
	var since time.Time
	unregister := func() {}
	defer func() { unregister() }()
	for {
		f, i, err := tryAcquire(dir, n)
		if err != nil {
			fmt.Fprintf(stderr, "outsource slot: cannot use %s (%v); running without a slot\n", dir, err)
			return nil, 0, true
		}
		if f != nil {
			writeRecord(f, label)
			if !since.IsZero() {
				waited := time.Since(since)
				fmt.Fprintf(stderr, "outsource slot: got a %s slot after %s (slot %d of %d)\n", pool, waitText(waited), i, n)
				// The wait is the number that says whether N fits the
				// machine, so telemetry carries it.
				telemetry.Note("waited_s", strconv.Itoa(int(waited.Seconds())))
			}
			return f, 0, true
		}
		if since.IsZero() {
			since = time.Now()
			unregister = registerWaiter(dir, label)
			fmt.Fprintf(stderr, "outsource slot: waiting for a %s slot — %d/%d busy: %s\n", pool, n, n, holders(dir, n))
		}
		select {
		case s := <-sigs:
			return nil, 128 + signum(s), false
		case <-time.After(pollInterval):
		}
	}
}

// tryAcquire makes one pass over files 0..n-1, in order. The order is what
// keeps a small-N caller inside its budget, and what makes callers with
// different N share the low files.
func tryAcquire(dir string, n int) (*os.File, int, error) {
	for i := 0; i < n; i++ {
		f, err := os.OpenFile(lockPath(dir, i), os.O_RDWR|os.O_CREATE, 0o644)
		if err != nil {
			return nil, 0, err
		}
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return f, i, nil
		}
		f.Close()
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, 0, err
		}
	}
	return nil, 0, nil
}

// writeRecord puts the holder record in the locked file. Best effort: the
// record is for --status and the wait message, and the lock alone is the
// slot.
func writeRecord(f *os.File, label string) {
	rec := fmt.Sprintf("pid=%d\nlabel=%s\nstarted=%d\n", os.Getpid(), oneLine(label), time.Now().Unix())
	_ = f.Truncate(0)
	_, _ = f.WriteAt([]byte(rec), 0)
}

// release empties the record while the lock is still held, so a released
// slot never shows the last holder as if it were current, then drops the
// lock by closing the file.
func release(f *os.File) {
	_ = f.Truncate(0)
	_ = f.Close()
}

// registerWaiter writes waiters/<pid> for --status and returns its removal.
// Files whose pid is gone (a SIGKILLed waiter) are pruned on the way in, so
// they cannot pile up or outlive a recycled pid for long.
func registerWaiter(dir, label string) func() {
	wdir := filepath.Join(dir, "waiters")
	if os.MkdirAll(wdir, 0o755) != nil {
		return func() {}
	}
	if ents, err := os.ReadDir(wdir); err == nil {
		for _, e := range ents {
			if pid, err := strconv.Atoi(e.Name()); err == nil && !alive(pid) {
				os.Remove(filepath.Join(wdir, e.Name()))
			}
		}
	}
	p := filepath.Join(wdir, strconv.Itoa(os.Getpid()))
	rec := fmt.Sprintf("pid=%d\nlabel=%s\nsince=%d\n", os.Getpid(), oneLine(label), time.Now().Unix())
	if os.WriteFile(p, []byte(rec), 0o644) != nil {
		return func() {}
	}
	return func() { os.Remove(p) }
}

// holders names the holders of files 0..n-1 for the wait message.
func holders(dir string, n int) string {
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, describe(readRecord(lockPath(dir, i))))
	}
	return strings.Join(out, ", ")
}

func describe(r map[string]string) string {
	pid := r["pid"]
	if pid == "" {
		pid = "?"
	}
	if r["label"] == "" {
		return "pid " + pid
	}
	return r["label"] + " (pid " + pid + ")"
}

// readRecord parses key=value lines; an unreadable or empty file is an empty
// record.
func readRecord(path string) map[string]string {
	rec := map[string]string{}
	b, err := os.ReadFile(path)
	if err != nil {
		return rec
	}
	for _, line := range strings.Split(string(b), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			rec[k] = v
		}
	}
	return rec
}

// runCommand runs the command with this process's stdio, forwards
// TERM/INT/HUP to it, and returns once it has exited — never before, so the
// slot is held for exactly the command's life.
//
// The command stays in this process's group. A terminal's Ctrl-C or a harness
// that kills the whole group then reaches both processes; a group of its own
// would let a group kill take this process and leave the command running.
func runCommand(path string, argv, env []string, stdin io.Reader, stdout, stderr io.Writer, sigs <-chan os.Signal) int {
	c := &exec.Cmd{Path: path, Args: argv, Env: env, Stdin: stdin, Stdout: stdout, Stderr: stderr}
	if err := c.Start(); err != nil {
		fmt.Fprintf(stderr, "outsource slot: cannot run %s: %v\n", argv[0], err)
		return exitCannotRun
	}
	done := make(chan struct{})
	go func() {
		_ = c.Wait() // the exit status is read from ProcessState below
		close(done)
	}()
	for {
		select {
		case s := <-sigs:
			_ = c.Process.Signal(s)
		case <-done:
			return exitStatus(c.ProcessState)
		}
	}
}

// exitStatus is the command's exit code, or 128+n when signal n killed it —
// ProcessState.ExitCode alone says -1 there.
func exitStatus(ps *os.ProcessState) int {
	if ps == nil {
		return exitCannotRun
	}
	if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	return ps.ExitCode()
}

func signum(s os.Signal) int {
	if ss, ok := s.(syscall.Signal); ok {
		return int(ss)
	}
	return 0
}

// alive is kill(pid, 0): EPERM still means the process exists.
func alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func oneLine(s string) string { return strings.NewReplacer("\n", " ", "\r", " ").Replace(s) }

// waitText renders a wait: tenths below a minute, where a handoff lives, and the
// shared duration format above it.
func waitText(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return human.Secs(int64(d.Seconds()))
}

// status prints every pool under root (or only the named one): each slot file
// held or free, the holder's pid, label and age, and the live waiters. Held
// is decided by a lock probe, not by the record, for the reason in the
// package comment.
func status(root, only string, stdout, stderr io.Writer) int {
	var pools []string
	if only != "" {
		pools = []string{only}
	} else {
		ents, err := os.ReadDir(root)
		if err != nil && !os.IsNotExist(err) {
			fmt.Fprintf(stderr, "outsource slot: %v\n", err)
			return 1
		}
		for _, e := range ents {
			if e.IsDir() {
				pools = append(pools, e.Name())
			}
		}
	}
	if len(pools) == 0 {
		fmt.Fprintf(stdout, "no slot pools yet under %s\n", root)
		return 0
	}
	now := time.Now().Unix()
	for _, pool := range pools {
		dir := filepath.Join(root, pool)
		files := lockFiles(dir)
		waiters := liveWaiters(dir)
		var lines []string
		nheld := 0
		for _, i := range files {
			p := lockPath(dir, i)
			if !heldNow(p) {
				lines = append(lines, fmt.Sprintf("  slot %d  free", i))
				continue
			}
			nheld++
			r := readRecord(p)
			lines = append(lines, fmt.Sprintf("  slot %d  held  %s  %s", i, describe(r), age(r["started"], now)))
		}
		for _, w := range waiters {
			lines = append(lines, fmt.Sprintf("  waiting   %s  %s", describe(w), age(w["since"], now)))
		}
		fmt.Fprintf(stdout, "%s: %d slot file(s), %d held, %d waiting\n", pool, len(files), nheld, len(waiters))
		for _, l := range lines {
			fmt.Fprintln(stdout, l)
		}
	}
	return 0
}

func age(unix string, now int64) string {
	t, err := strconv.ParseInt(unix, 10, 64)
	if err != nil {
		return "age ?"
	}
	return "for " + human.Secs(now-t)
}

// lockFiles lists the slot indices present in dir, in numeric order. The
// files are whatever the callers' N have created, so the list is as long as
// the largest N that ever used the pool.
func lockFiles(dir string) []int {
	ents, _ := os.ReadDir(dir)
	var out []int
	for _, e := range ents {
		if n, ok := strings.CutSuffix(e.Name(), ".lock"); ok {
			if i, err := strconv.Atoi(n); err == nil && i >= 0 {
				out = append(out, i)
			}
		}
	}
	sort.Ints(out)
	return out
}

// heldNow probes with a shared non-blocking lock: it fails only while some
// process holds the exclusive one. The probe itself holds for microseconds.
func heldNow(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	return errors.Is(syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB), syscall.EWOULDBLOCK)
}

// liveWaiters reads waiters/<pid>, skipping pids that are gone.
func liveWaiters(dir string) []map[string]string {
	wdir := filepath.Join(dir, "waiters")
	ents, _ := os.ReadDir(wdir)
	var out []map[string]string
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || !alive(pid) {
			continue
		}
		r := readRecord(filepath.Join(wdir, e.Name()))
		r["pid"] = strconv.Itoa(pid)
		out = append(out, r)
	}
	return out
}
