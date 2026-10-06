package slot

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// These tests run the real tool as a separate process — flock only means
// something between processes — by re-executing this test binary with
// helperKey set. The helper modes:
//
//	slot      the tool itself: Main(os.Args[1:]); the key is unset first so
//	          the wrapped command does not inherit it
//	stamp     <file> <tag>: append "<tag> <unix ns>" to <file> — the
//	          timestamps come from the commands themselves
//	trapterm  <dir>: write <dir>/ready, wait for TERM, then 300 ms later
//	          write <dir>/term and exit 9 — a command that takes its time
//	          to stop
//
// Every test uses its own temp XDG_CACHE_HOME; none touches the real cache.
const (
	helperKey = "OUTSOURCE_SLOT_TESTHELPER"
	// testmainKey bounds recursion: a re-exec of this binary WITHOUT a
	// helper mode would rerun the whole suite, so it refuses instead.
	testmainKey = "OUTSOURCE_SLOT_TESTMAIN"
)

func TestMain(m *testing.M) {
	switch os.Getenv(helperKey) {
	case "slot":
		os.Unsetenv(helperKey)
		os.Exit(Main(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
	case "stamp":
		f, err := os.OpenFile(os.Args[1], os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			os.Exit(2)
		}
		fmt.Fprintf(f, "%s %d\n", os.Args[2], time.Now().UnixNano())
		f.Close()
		os.Exit(0)
	case "trapterm":
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGTERM)
		os.WriteFile(filepath.Join(os.Args[1], "ready"), []byte("ready\n"), 0o644)
		<-ch
		time.Sleep(300 * time.Millisecond)
		os.WriteFile(filepath.Join(os.Args[1], "term"), []byte("term\n"), 0o644)
		os.Exit(9)
	case "":
	default:
		fmt.Fprintf(os.Stderr, "slot tests: unknown %s=%s\n", helperKey, os.Getenv(helperKey))
		os.Exit(3)
	}
	if os.Getenv(testmainKey) != "" {
		fmt.Fprintf(os.Stderr, "slot tests: re-executed without a helper mode (%q); refusing to rerun the suite\n", os.Args[1:])
		os.Exit(3)
	}
	os.Setenv(testmainKey, "1")
	os.Exit(m.Run())
}

// rig is one test's temp cache and scratch dir.
type rig struct {
	t     *testing.T
	cache string
	tmp   string
	self  string
	seq   int
}

func newRig(t *testing.T) *rig {
	t.Helper()
	t.Parallel()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return &rig{t: t, cache: t.TempDir(), tmp: t.TempDir(), self: self}
}

// cleanEnv is this process's environment without the names that would change
// the tool's answer: a delegated round exports OUTSOURCE_RUN_LABEL, and a
// lead's shell may carry OUTSOURCE_SLOTS or a real XDG_CACHE_HOME.
func cleanEnv() []string {
	drop := map[string]bool{"OUTSOURCE_SLOTS": true, "OUTSOURCE_RUN_LABEL": true, "XDG_CACHE_HOME": true,
		heldEnvKey: true, helperKey: true, "OUTSOURCE_SLOT": true}
	var out []string
	for _, e := range os.Environ() {
		if k, _, _ := strings.Cut(e, "="); !drop[k] {
			out = append(out, e)
		}
	}
	return out
}

// slot builds a run of the tool with extra env and args.
func (r *rig) slot(env []string, args ...string) *exec.Cmd {
	c := exec.Command(r.self, args...)
	c.Env = append(append(cleanEnv(), "XDG_CACHE_HOME="+r.cache, helperKey+"=slot"), env...)
	return c
}

type proc struct {
	cmd    *exec.Cmd
	errf   string
	done   chan struct{}
	status int
}

// start runs c in the background with stderr in a file; a process still
// running when the test ends is killed (it is this test's own child).
func (r *rig) start(c *exec.Cmd) *proc {
	r.t.Helper()
	r.seq++
	p := &proc{cmd: c, errf: filepath.Join(r.tmp, fmt.Sprintf("stderr.%d", r.seq)), done: make(chan struct{})}
	f, err := os.Create(p.errf)
	if err != nil {
		r.t.Fatal(err)
	}
	c.Stderr = f
	if err := c.Start(); err != nil {
		r.t.Fatal(err)
	}
	f.Close()
	go func() {
		_ = c.Wait()
		p.status = exitStatus(c.ProcessState)
		close(p.done)
	}()
	r.t.Cleanup(func() {
		select {
		case <-p.done:
		default:
			_ = c.Process.Kill()
			<-p.done
		}
	})
	return p
}

func (p *proc) pid() int { return p.cmd.Process.Pid }

func (p *proc) wait(t *testing.T, limit time.Duration) int {
	t.Helper()
	select {
	case <-p.done:
		return p.status
	case <-time.After(limit):
		t.Fatalf("%v did not exit within %v; stderr: %s", p.cmd.Args, limit, p.stderr())
		return -1
	}
}

func (p *proc) stderr() string {
	b, _ := os.ReadFile(p.errf)
	return string(b)
}

// run is start + wait for a short command.
func (r *rig) run(env []string, args ...string) (int, string) {
	r.t.Helper()
	p := r.start(r.slot(env, args...))
	return p.wait(r.t, 20*time.Second), p.stderr()
}

func waitFor(t *testing.T, what string, limit time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %v waiting for %s", limit, what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func exists(path string) func() bool {
	return func() bool { _, err := os.Stat(path); return err == nil }
}

// sleeper is a command that writes its own pid to pidfile, then becomes
// `sleep 30`. The pid is what lets cleanup reap a command orphaned by a
// SIGKILLed slot — and nothing else. Only builtins run before the exec, so a
// signal that lands on the shell before it becomes sleep still kills it
// (no foreground child for the shell to wait on first).
func (r *rig) sleeper(pidfile string) []string {
	r.t.Cleanup(func() {
		if pid := readPid(pidfile); pid > 1 {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	return []string{"sh", "-c", `echo $$ > "$0" && exec sleep 30`, pidfile}
}

// readPid is the pid in a sleeper's pidfile once the line is complete, else 0.
func readPid(pidfile string) int {
	b, err := os.ReadFile(pidfile)
	if err != nil || !strings.HasSuffix(string(b), "\n") {
		return 0
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	return pid
}

func started(pidfile string) func() bool { return func() bool { return readPid(pidfile) > 0 } }

// stamper is a command that stamps <tag> into file via the stamp helper.
func (r *rig) stamper(file, tag string) []string {
	return []string{"sh", "-c", helperKey + `=stamp "$0" "$1" "$2"`, r.self, file, tag}
}

func (r *rig) waiterFile(pool string, pid int) string {
	return filepath.Join(r.cache, "outsource", "slots", pool, "waiters", strconv.Itoa(pid))
}

func readStamps(t *testing.T, file string) map[string]int64 {
	t.Helper()
	f, err := os.Open(file)
	if err != nil {
		t.Fatalf("no stamps: %v", err)
	}
	defer f.Close()
	out := map[string]int64{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		tag, ns, ok := strings.Cut(sc.Text(), " ")
		if !ok {
			continue
		}
		n, err := strconv.ParseInt(ns, 10, 64)
		if err != nil {
			t.Fatalf("bad stamp line %q", sc.Text())
		}
		out[tag] = n
	}
	return out
}

func args(head []string, cmd ...string) []string { return append(append([]string{}, head...), cmd...) }

// Six holders, N=2: the commands' own timestamps never show more than two
// running at once — and do show two, or the bound was never exercised.
func TestConcurrencyNeverExceedsN(t *testing.T) {
	r := newRig(t)
	var procs []*proc
	var files []string
	for i := 0; i < 6; i++ {
		f := filepath.Join(r.tmp, fmt.Sprintf("stamps.%d", i))
		files = append(files, f)
		cmd := []string{"sh", "-c",
			helperKey + `=stamp "$0" "$1" start && sleep 0.4 && ` + helperKey + `=stamp "$0" "$1" end`, r.self, f}
		procs = append(procs, r.start(r.slot(nil, args([]string{"--max", "2", "--name", "conc", "--"}, cmd...)...)))
	}
	for i, p := range procs {
		if rc := p.wait(t, 30*time.Second); rc != 0 {
			t.Fatalf("holder %d exited %d; stderr: %s", i, rc, p.stderr())
		}
	}
	type event struct {
		at    int64
		delta int
	}
	var evs []event
	for _, f := range files {
		s := readStamps(t, f)
		if s["start"] == 0 || s["end"] == 0 {
			t.Fatalf("%s lacks a start or an end: %v", f, s)
		}
		evs = append(evs, event{s["start"], +1}, event{s["end"], -1})
	}
	// Ends sort before starts at the same instant: a handoff is not overlap.
	sort.Slice(evs, func(i, j int) bool {
		if evs[i].at != evs[j].at {
			return evs[i].at < evs[j].at
		}
		return evs[i].delta < evs[j].delta
	})
	running, peak := 0, 0
	for _, e := range evs {
		running += e.delta
		peak = max(peak, running)
	}
	if peak > 2 {
		t.Fatalf("%d commands ran at once under --max 2", peak)
	}
	if peak < 2 {
		t.Fatalf("at most %d command ran at once; six holders under --max 2 should have reached 2", peak)
	}
}

// SIGKILL gives the holder no chance to release anything; the kernel drops
// its flock, and a waiter takes the slot within 2 s of the kill.
//
// The bound is on the acquisition — the waiter's own record appearing in the
// slot file — and not on its command starting, which would add a shell and a
// process start to the window, the margin a loaded machine eats first.
func TestCrashReleasesTheSlot(t *testing.T) {
	r := newRig(t)
	head := []string{"--max", "1", "--name", "crash", "--"}
	pidfile := filepath.Join(r.tmp, "holder.pid")
	h := r.start(r.slot(nil, args(head, r.sleeper(pidfile)...)...))
	waitFor(t, "the holder's command to start", 10*time.Second, started(pidfile))

	// The waiter's command stamps, then holds the slot for 0.5 s so the
	// record stays readable long enough to be seen.
	stamps := filepath.Join(r.tmp, "waiter.stamps")
	w := r.start(r.slot(nil, args(head, "sh", "-c", helperKey+`=stamp "$0" "$1" got && sleep 0.5`, r.self, stamps)...))
	waitFor(t, "the waiter to register", 10*time.Second, exists(r.waiterFile("crash", w.pid())))

	lockFile := filepath.Join(r.cache, "outsource", "slots", "crash", "0.lock")
	killed := time.Now()
	if err := h.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the waiter to take the slot", 10*time.Second, func() bool {
		return readRecord(lockFile)["pid"] == strconv.Itoa(w.pid())
	})
	d := time.Since(killed)
	if d > 2*time.Second {
		t.Fatalf("the waiter took the slot %v after the holder was killed; want ≤ 2s", d)
	}
	t.Logf("the waiter took the slot %v after the kill", d)
	if rc := h.wait(t, 5*time.Second); rc != 128+int(syscall.SIGKILL) {
		t.Fatalf("killed holder: status %d, want %d", rc, 128+int(syscall.SIGKILL))
	}
	if rc := w.wait(t, 10*time.Second); rc != 0 {
		t.Fatalf("waiter exited %d; stderr: %s", rc, w.stderr())
	}
	// Looser, end to end: the waiter's command itself ran soon after.
	if d := time.Duration(readStamps(t, stamps)["got"] - killed.UnixNano()); d > 4*time.Second {
		t.Errorf("the waiter's command started %v after the holder was killed", d)
	}
	for _, want := range []string{"waiting for a crash slot — 1/1 busy: sh (pid " + strconv.Itoa(h.pid()) + ")", "got a crash slot after"} {
		if !strings.Contains(w.stderr(), want) {
			t.Errorf("waiter stderr lacks %q:\n%s", want, w.stderr())
		}
	}
	if _, err := os.Stat(r.waiterFile("crash", w.pid())); !os.IsNotExist(err) {
		t.Errorf("the waiter file outlived the wait: %v", err)
	}
}

// The command's exit code is the tool's; a signalled command gives 128+n.
func TestExitCodesPassThrough(t *testing.T) {
	r := newRig(t)
	head := []string{"--max", "1", "--name", "codes", "--"}
	for _, c := range []struct {
		script string
		want   int
	}{
		{"exit 0", 0},
		{"exit 7", 7},
		{"kill -TERM $$", 128 + int(syscall.SIGTERM)},
		{"kill -KILL $$", 128 + int(syscall.SIGKILL)},
	} {
		if rc, errText := r.run(nil, args(head, "sh", "-c", c.script)...); rc != c.want {
			t.Errorf("%q: exit %d, want %d; stderr: %s", c.script, rc, c.want, errText)
		}
	}
	if rc, errText := r.run(nil, args(head, "no-such-command-for-slot-test")...); rc != exitNotFound {
		t.Errorf("a missing command: exit %d, want %d; stderr: %s", rc, exitNotFound, errText)
	}
}

// TERM, INT and HUP to the tool reach the command; the tool exits only when
// the command has, with its status, and the slot is free afterwards.
func TestSignalsAreForwardedAndTheSlotReleased(t *testing.T) {
	r := newRig(t)
	head := []string{"--max", "1", "--name", "fwd", "--"}
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP} {
		pidfile := filepath.Join(r.tmp, "cmd.pid."+strconv.Itoa(int(sig)))
		p := r.start(r.slot(nil, args(head, r.sleeper(pidfile)...)...))
		waitFor(t, "the command to start", 10*time.Second, started(pidfile))
		child := readPid(pidfile)
		// Only the tool is signalled; the command can die of sig only if the
		// tool forwarded it.
		if err := syscall.Kill(p.pid(), sig); err != nil {
			t.Fatal(err)
		}
		if rc := p.wait(t, 5*time.Second); rc != 128+int(sig) {
			t.Fatalf("%v: exit %d, want %d (the command dying of the forwarded signal); stderr: %s", sig, rc, 128+int(sig), p.stderr())
		}
		if alive(child) {
			t.Fatalf("%v: the command (pid %d) outlived the tool", sig, child)
		}
		rc, errText := r.run(nil, args(head, "true")...)
		if rc != 0 || strings.Contains(errText, "waiting") {
			t.Fatalf("%v: the slot was not free afterwards: exit %d, stderr: %s", sig, rc, errText)
		}
	}

	// A command that takes 300 ms to stop: the tool waits for it, and its
	// exit code (9), not the signal, is the answer.
	dir := filepath.Join(r.tmp, "trap")
	os.MkdirAll(dir, 0o755)
	p := r.start(r.slot(nil, args(head, "sh", "-c", helperKey+`=trapterm exec "$0" "$1"`, r.self, dir)...))
	waitFor(t, "the trapping command to start", 10*time.Second, exists(filepath.Join(dir, "ready")))
	if err := syscall.Kill(p.pid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if rc := p.wait(t, 5*time.Second); rc != 9 {
		t.Fatalf("trapping command: exit %d, want 9; stderr: %s", rc, p.stderr())
	}
	if _, err := os.Stat(filepath.Join(dir, "term")); err != nil {
		t.Fatalf("the tool exited before its command finished handling TERM: %v", err)
	}
}

// --max beats OUTSOURCE_SLOTS, which beats max(1, cores/4); anything below 1
// is a usage error.
func TestNPrecedence(t *testing.T) {
	r := newRig(t)
	for _, c := range []struct {
		flag, env string
		cores     int
		want      int
	}{
		{"", "", 1, 1}, {"", "", 3, 1}, {"", "", 4, 1}, {"", "", 10, 2}, {"", "", 16, 4},
		{"", "5", 10, 5},
		{"3", "5", 10, 3},
		{"3", "", 10, 3},
		{"1", "0", 10, 1}, // the flag wins before the env var is even read
	} {
		if got, err := slots(c.flag, c.env, c.cores); err != nil || got != c.want {
			t.Errorf("slots(%q, %q, %d) = %d, %v; want %d", c.flag, c.env, c.cores, got, err, c.want)
		}
	}
	for _, c := range []struct{ flag, env string }{{"0", ""}, {"", "0"}, {"-1", ""}, {"", "two"}, {"x", "2"}} {
		if n, err := slots(c.flag, c.env, 10); err == nil {
			t.Errorf("slots(%q, %q) = %d, want an error", c.flag, c.env, n)
		}
	}

	// The same order through the tool, with file 0 held: under --max 2 a
	// second caller gets file 1 at once whatever OUTSOURCE_SLOTS says, and
	// under OUTSOURCE_SLOTS=1 alone it waits.
	pidfile := filepath.Join(r.tmp, "holder.pid")
	r.start(r.slot(nil, args([]string{"--max", "1", "--name", "n", "--"}, r.sleeper(pidfile)...)...))
	waitFor(t, "the holder to start", 10*time.Second, started(pidfile))
	if rc, errText := r.run([]string{"OUTSOURCE_SLOTS=1"}, "--max", "2", "--name", "n", "--", "true"); rc != 0 || strings.Contains(errText, "waiting") {
		t.Fatalf("--max 2 under OUTSOURCE_SLOTS=1 should not wait: exit %d, stderr: %s", rc, errText)
	}
	w := r.start(r.slot([]string{"OUTSOURCE_SLOTS=1"}, "--name", "n", "--", "true"))
	waitFor(t, "OUTSOURCE_SLOTS=1 to wait", 10*time.Second, exists(r.waiterFile("n", w.pid())))
	syscall.Kill(w.pid(), syscall.SIGTERM)
	if rc := w.wait(t, 5*time.Second); rc != 128+int(syscall.SIGTERM) {
		t.Fatalf("a waiter TERMed before its turn: exit %d, want %d", rc, 128+int(syscall.SIGTERM))
	}
	if _, err := os.Stat(r.waiterFile("n", w.pid())); !os.IsNotExist(err) {
		t.Errorf("a TERMed waiter left its waiter file: %v", err)
	}
	// And the default, when this machine's default is above 1.
	if def := max(1, runtime.NumCPU()/4); def >= 2 {
		if rc, errText := r.run(nil, "--name", "n", "--", "true"); rc != 0 || strings.Contains(errText, "waiting") {
			t.Fatalf("the default N=%d should not wait with one slot held: exit %d, stderr: %s", def, rc, errText)
		}
	}

	for _, c := range []struct {
		env  []string
		args []string
	}{
		{nil, []string{"--max", "0", "--", "true"}},
		{[]string{"OUTSOURCE_SLOTS=0"}, []string{"--", "true"}},
		{[]string{"OUTSOURCE_SLOTS=lots"}, []string{"--", "true"}},
	} {
		if rc, errText := r.run(c.env, c.args...); rc != ExitUsage {
			t.Errorf("%v %v: exit %d, want %d; stderr: %s", c.env, c.args, rc, ExitUsage, errText)
		}
	}
}

// --status names each holder (pid, label, age) and counts the live waiters;
// a waiter file whose pid is gone is not counted.
func TestStatusShowsHoldersAndWaiters(t *testing.T) {
	r := newRig(t)
	if rc, _ := r.run(nil, "--status"); rc != 0 {
		t.Fatalf("--status on an empty cache: exit %d", rc)
	}
	head := []string{"--max", "1", "--name", "st", "--"}
	pidfile := filepath.Join(r.tmp, "holder.pid")
	h := r.start(r.slot([]string{"OUTSOURCE_RUN_LABEL=status-holder"}, args(head, r.sleeper(pidfile)...)...))
	waitFor(t, "the holder to start", 10*time.Second, started(pidfile))
	w := r.start(r.slot([]string{"OUTSOURCE_RUN_LABEL=status-waiter"}, args(head, "true")...))
	waitFor(t, "the waiter to register", 10*time.Second, exists(r.waiterFile("st", w.pid())))

	// A dead waiter: a finished process's pid, with a file left behind.
	dead := exec.Command("true")
	if err := dead.Run(); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(r.waiterFile("st", dead.Process.Pid), []byte("label=ghost\n"), 0o644)

	out, err := r.slot(nil, "--status").Output()
	if err != nil {
		t.Fatalf("--status: %v", err)
	}
	text := string(out)
	for _, want := range []string{
		"st: 1 slot file(s), 1 held, 1 waiting",
		"slot 0  held  status-holder (pid " + strconv.Itoa(h.pid()) + ")  for ",
		"waiting   status-waiter (pid " + strconv.Itoa(w.pid()) + ")",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("--status lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "ghost") {
		t.Errorf("--status counted a dead waiter:\n%s", text)
	}

	// --name narrows it to one pool; an unused pool reads as no files.
	out, _ = r.slot(nil, "--status", "--name", "other").Output()
	if !strings.HasPrefix(string(out), "other: 0 slot file(s), 0 held, 0 waiting") {
		t.Errorf("--status --name other:\n%s", out)
	}
}

// A caller with N=1 never takes file 1, even while it exists, is free and
// file 0 is held — it waits. A caller with N=2 takes file 1 at once.
func TestSmallerNNeverTakesFileOne(t *testing.T) {
	r := newRig(t)
	pidfile := filepath.Join(r.tmp, "holder.pid")
	r.start(r.slot(nil, args([]string{"--max", "2", "--name", "small", "--"}, r.sleeper(pidfile)...)...))
	waitFor(t, "the holder to start", 10*time.Second, started(pidfile))
	// First an N=2 caller, which takes file 1 at once — and so creates it:
	// a pool's files exist only up to the largest N that has used it, and the
	// claim below is about a file that is there to be taken.
	if rc, errText := r.run(nil, "--max", "2", "--name", "small", "--", "true"); rc != 0 || strings.Contains(errText, "waiting") {
		t.Fatalf("an N=2 caller should take the free file 1 at once: exit %d, stderr: %s", rc, errText)
	}
	if _, err := os.Stat(filepath.Join(r.cache, "outsource", "slots", "small", "1.lock")); err != nil {
		t.Fatalf("file 1 should exist after an N=2 caller used it: %v", err)
	}

	small := r.start(r.slot(nil, "--max", "1", "--name", "small", "--", "true"))
	waitFor(t, "the N=1 caller to wait", 10*time.Second, func() bool {
		select {
		case <-small.done:
			t.Fatalf("the N=1 caller ran while file 0 was held (exit %d) — it took file 1; stderr: %s", small.status, small.stderr())
		default:
		}
		return exists(r.waiterFile("small", small.pid()))()
	})
	// Give it a few polls in which file 1 was free to be (wrongly) taken.
	time.Sleep(2500 * time.Millisecond)
	select {
	case <-small.done:
		t.Fatalf("the N=1 caller ran while file 0 was held (exit %d); stderr: %s", small.status, small.stderr())
	default:
	}
	if !strings.Contains(small.stderr(), "1/1 busy") {
		t.Errorf("the N=1 caller's wait line should count its own budget:\n%s", small.stderr())
	}
	syscall.Kill(small.pid(), syscall.SIGTERM)
	small.wait(t, 5*time.Second)
}

// A command that wraps its own steps in the same pool runs them straight
// through instead of waiting for the slot its own ancestor holds. At N=1 the
// inner call would otherwise wait forever.
func TestNestedCallOnTheSamePoolPassesThrough(t *testing.T) {
	r := newRig(t)
	inner := helperKey + `=slot "$0" --max 1 --name nest -- sh -c 'echo "$OUTSOURCE_SLOT_HELD"'`
	p := r.start(r.slot(nil, "--max", "1", "--name", "nest", "--", "sh", "-c", inner, r.self))
	if rc := p.wait(t, 10*time.Second); rc != 0 {
		t.Fatalf("nested call: exit %d; stderr: %s", rc, p.stderr())
	}
	if strings.Contains(p.stderr(), "waiting") {
		t.Fatalf("the nested call waited for its own ancestor's slot:\n%s", p.stderr())
	}
	// Another pool inside is still a real acquisition, and the held list grows.
	out, err := r.slot(nil, "--max", "1", "--name", "nest", "--", "sh", "-c",
		helperKey+`=slot "$0" --max 1 --name other -- sh -c 'echo "$OUTSOURCE_SLOT_HELD"'`, r.self).Output()
	if err != nil || strings.TrimSpace(string(out)) != "nest,other" {
		t.Fatalf("nested other pool: %q, %v; want nest,other", out, err)
	}
}

func TestUsageErrors(t *testing.T) {
	r := newRig(t)
	for _, a := range [][]string{
		{},
		{"--"},
		{"--max"},
		{"--max="},
		{"--name", "../escape", "--", "true"},
		{"--name", "", "--", "true"},
		{"--bogus", "--", "true"},
		{"--status", "--", "true"},
	} {
		if rc, errText := r.run(nil, a...); rc != ExitUsage {
			t.Errorf("%q: exit %d, want %d; stderr: %s", a, rc, ExitUsage, errText)
		}
	}
	// The command may start without `--`.
	if rc, errText := r.run(nil, "--max", "1", "--name", "u", "true"); rc != 0 {
		t.Errorf("a command without --: exit %d; stderr: %s", rc, errText)
	}
}

// git is refused (64) before anything runs, bare or behind env/nice/command,
// and inside a held pool too: the preamble teaches this wrapper form, and
// the git guard does not see through it. git here is a fake that leaves a
// marker, first on PATH, and GIT_DIR points nowhere, so even a broken check
// could not touch a repository.
func TestGitIsRefused(t *testing.T) {
	r := newRig(t)
	fake := filepath.Join(r.tmp, "fakebin")
	os.MkdirAll(fake, 0o755)
	marker := filepath.Join(r.tmp, "git-ran")
	os.WriteFile(filepath.Join(fake, "git"), []byte("#!/bin/sh\n: > \""+marker+"\"\n"), 0o755)
	env := []string{"PATH=" + fake + string(os.PathListSeparator) + os.Getenv("PATH"),
		"GIT_DIR=" + filepath.Join(r.tmp, "no-such-git-dir")}
	run := func(extra []string, cmd ...string) (int, string) {
		t.Helper()
		c := r.slot(append(env, extra...), args([]string{"--max", "1", "--name", "g", "--"}, cmd...)...)
		c.Dir = r.tmp
		p := r.start(c)
		return p.wait(t, 20*time.Second), p.stderr()
	}
	const msg = "git is not a heavy step; run it directly (and a round may not change git state)"
	for _, cmd := range [][]string{
		{"git", "status"},
		{"/nonexistent/bin/git", "commit"},
		{"GIT", "push"},
		{"env", "git", "commit"},
		{"env", "-i", "FOO=1", "git", "push"},
		{"env", "-u", "HOME", "--", "BAR=2", "git", "add", "."},
		{"/usr/bin/env", "--unset=HOME", "git"},
		{"env", "-S", "git commit -am x"},
		{"nice", "git", "gc"},
		{"nice", "-n", "5", "git", "gc"},
		{"nice", "-n5", "git", "gc"},
		{"command", "git", "stash"},
		{"command", "-p", "git", "reset"},
		{"env", "nice", "-n", "1", "command", "git", "rebase"},
	} {
		rc, errText := run(nil, cmd...)
		if rc != ExitUsage || !strings.Contains(errText, msg) {
			t.Errorf("%q: exit %d, want %d with the git refusal; stderr: %s", cmd, rc, ExitUsage, errText)
		}
	}
	if rc, errText := run([]string{heldEnvKey + "=g"}, "git", "status"); rc != ExitUsage {
		t.Errorf("git inside a held pool: exit %d, want %d; stderr: %s", rc, ExitUsage, errText)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatalf("a refused git ran")
	}
	// git as an argument, not the command, is not refused.
	for _, cmd := range [][]string{{"true", "git"}, {"echo", "git"}, {"env", "FOO=git", "true"}, {"nice", "-n", "5", "true"}} {
		if rc, errText := run(nil, cmd...); rc != 0 {
			t.Errorf("%q: exit %d, want 0; stderr: %s", cmd, rc, errText)
		}
	}
}
