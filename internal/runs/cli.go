package runs

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const usage = `The registry of delegated runs: who is running right now, on what, for how
long, and how the last few ended.

  runs [list]                    human table (default)
  runs line                      one compact line, for a status line
  runs json                      machine-readable, one object per run
  runs start  --pid N --provider P --harness H [...]   -> prints run id
  runs finish <run-id> --rc N [--session S] [--model-actual M]
  runs trail  <run-id> --path P   record where this round's live trail is
  runs prune [--keep-seconds N]  drop finished records older than N
  runs dismiss <run-id>          drop one record you have read (not running)

list/line/json take --owner <session-id> and --owner-claude-pid <pid>, which
restrict the output to rounds launched from one Claude Code session. The
registry is machine-wide on purpose — an orphan has to be findable from
wherever you happen to be — but a status line showing every session's rounds
reports another window's work as if it were yours. So the store is global and
the filter lives at the reading end.

Ownership is recorded as two keys because one is not enough.
CLAUDE_CODE_SESSION_ID is exact but changes for an in-process subagent, so a
round a teammate launched would vanish from the lead's own status line;
CLAUDE_PID is the Claude Code process, shared by the lead and its in-process
agents. Either matching counts as yours.

The four states a run can be in:

  running   record has no rc, and the pid is alive
  orphan    record has no rc, and the pid is gone — the round died without
            finishing. Nothing else on the machine still remembers it existed.
  done      rc=0
  failed    rc!=0 (the launcher's own exit codes carry the reason)

Exit codes: 0 ok · 64 usage error · 65 no such run id · 66 refused (running)
`

// UsageError and the other sentinel codes keep the shell contract: callers and
// tests branch on these numbers.
const (
	ExitUsage    = 64
	ExitNoSuchID = 65
	ExitRefused  = 66
)

type filter struct {
	owner    string
	ownerPid string
	set      bool
}

// mine is the ownership test, carried over exactly. An empty filter means "no
// filter, show everything" — which is why the status line skips the call
// entirely when it has no identity, rather than passing empty flags.
//
// A record predating the ownership fields, or launched outside Claude Code,
// has no owner at all and is therefore nobody's: it stays out of a scoped view
// rather than appearing in every one of them.
func (f filter) mine(r *Record) bool {
	if f.owner == "" && f.ownerPid == "" {
		return true
	}
	if f.owner != "" && r.OwnerSession == f.owner {
		return true
	}
	if f.ownerPid != "" && r.OwnerClaudePid != "" && r.OwnerClaudePid == f.ownerPid {
		return true
	}
	return false
}

// parseFilter consumes --owner / --owner-claude-pid and returns the rest, the
// same shape as the shell's parse_filter_flags.
func parseFilter(args []string) (filter, []string, error) {
	var f filter
	rest := []string{}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--owner":
			if i+1 >= len(args) {
				return f, nil, fmt.Errorf("--owner needs a value")
			}
			f.owner, f.set = args[i+1], true
			i++
		case "--owner-claude-pid":
			if i+1 >= len(args) {
				return f, nil, fmt.Errorf("--owner-claude-pid needs a value")
			}
			f.ownerPid, f.set = args[i+1], true
			i++
		default:
			rest = append(rest, args[i])
		}
	}
	return f, rest, nil
}

// Main is the runs entry point. It returns an exit code rather than calling
// os.Exit so it stays testable and so the multi-call binary owns exiting.
func Main(args []string, stdout, stderr io.Writer) int {
	// How the subcommand is chosen, carried over exactly — each arm was earned.
	//
	// A flag-shaped first argument selects the default verb WITHOUT being
	// consumed, so `runs --label x` is `runs list --label x`. The no-argument
	// form matters most: `runs` is the invocation the README leads with, and in
	// the shell version a bare `shift` under `set -e` once killed it silently
	// with rc=1 (measured 2026-08-18). Unrecognised filter flags are then
	// ignored rather than refused, which is what makes the bare form forgiving.
	sub := "list"
	if len(args) > 0 {
		switch {
		case args[0] == "-h" || args[0] == "--help":
			sub = args[0] // args stay; help does not read them
		case strings.HasPrefix(args[0], "-"):
			sub = "list" // flag-shaped: default verb, arguments pass through
		default:
			sub = args[0]
			args = args[1:]
		}
	}
	switch sub {
	case "-h", "--help":
		fmt.Fprint(stdout, usage)
		return 0
	case "start":
		return cmdStart(args, stdout, stderr)
	case "finish":
		return cmdFinish(args, stderr)
	case "trail":
		return cmdTrail(args, stderr)
	case "prune":
		return cmdPrune(args, stdout, stderr)
	case "dismiss":
		return cmdDismiss(args, stdout, stderr)
	case "list", "line", "json":
		f, rest, err := parseFilter(args)
		if err != nil {
			fmt.Fprintf(stderr, "runs %s: %v\n", sub, err)
			return ExitUsage
		}
		_ = rest
		switch sub {
		case "list":
			return cmdList(f, stdout)
		case "line":
			return cmdLine(f, stdout)
		default:
			return cmdJSON(f, stdout)
		}
	default:
		fmt.Fprintf(stderr, "runs: unknown subcommand: %s (list|line|json|start|finish|trail|prune|dismiss)\n", sub)
		return ExitUsage
	}
}

func cmdStart(args []string, stdout, stderr io.Writer) int {
	var pid, label, provider, harness, model, cwd, spec, log, progress, trail, trailFormat, owner, ownerPid string
	need := func(i int, name string) bool {
		if i+1 >= len(args) {
			fmt.Fprintf(stderr, "runs start: %s needs a value\n", name)
			return false
		}
		return true
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		var dst *string
		switch a {
		case "--pid":
			dst = &pid
		case "--label":
			dst = &label
		case "--provider":
			dst = &provider
		case "--harness":
			dst = &harness
		case "--model":
			dst = &model
		case "--cwd":
			dst = &cwd
		case "--spec":
			dst = &spec
		case "--log":
			dst = &log
		case "--progress-dir":
			dst = &progress
		case "--trail":
			dst = &trail
		case "--trail-format":
			dst = &trailFormat
		case "--owner":
			dst = &owner
		case "--owner-claude-pid":
			dst = &ownerPid
		default:
			fmt.Fprintf(stderr, "runs start: unknown flag: %s\n", a)
			return ExitUsage
		}
		if !need(i, a) {
			return ExitUsage
		}
		*dst = args[i+1]
		i++
	}
	if pid == "" {
		fmt.Fprintln(stderr, "runs start: --pid is required")
		return ExitUsage
	}
	if label == "" {
		label = "run"
	}

	dir := Dir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintf(stderr, "runs start: %v\n", err)
		return 1
	}
	started := nowUnix()

	// <epoch>-<pid> is unique among live launchers, since a pid is. It is not
	// unique against a record left by a dead process whose pid was recycled
	// inside the same second — rare, but the failure mode is silent overwrite
	// of someone else's round, so the id takes a suffix instead.
	id := fmt.Sprintf("%d-%s", started, pid)
	for n := 2; ; n++ {
		if _, err := os.Stat(filepath.Join(dir, id+".run")); os.IsNotExist(err) {
			break
		}
		id = fmt.Sprintf("%d-%s-%d", started, pid, n)
	}

	var b strings.Builder
	put := func(k, v string) { fmt.Fprintf(&b, "%s=%s\n", k, sanitize(v)) }
	put("id", id)
	put("pid", pid)
	put("label", label)
	put("provider", provider)
	put("harness", harness)
	put("model", model)
	put("cwd", cwd)
	put("spec", spec)
	put("log", log)
	put("progressDir", progress)
	put("trail", trail)
	put("trailFormat", trailFormat)
	put("ownerSession", owner)
	put("ownerClaudePid", ownerPid)
	put("startedAt", fmt.Sprintf("%d", started))

	// Write-then-rename: a status line reading the directory concurrently sees
	// either no record or a complete one, never half of one.
	tmp := filepath.Join(dir, "."+id+".tmp")
	if err := os.WriteFile(tmp, []byte(b.String()), 0o644); err != nil {
		fmt.Fprintf(stderr, "runs start: %v\n", err)
		return 1
	}
	if err := os.Rename(tmp, filepath.Join(dir, id+".run")); err != nil {
		_ = os.Remove(tmp)
		fmt.Fprintf(stderr, "runs start: %v\n", err)
		return 1
	}

	// Housekeeping rides the write path so nobody has to remember to run it.
	_ = pruneOlderThan(KeepSecondsDefault)
	fmt.Fprintln(stdout, id)
	return 0
}

// cmdTrail records where a running round's live trail turned out to be. It is
// the CLI face of SetTrail, so a hook or a human can reveal a path without
// linking against this package.
func cmdTrail(args []string, stderr io.Writer) int {
	if len(args) < 1 {
		fmt.Fprintln(stderr, "runs trail: needs a run id")
		return ExitUsage
	}
	id := args[0]
	args = args[1:]
	var path string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--path":
			if i+1 >= len(args) {
				fmt.Fprintln(stderr, "runs trail: --path needs a value")
				return ExitUsage
			}
			path = args[i+1]
			i++
		default:
			fmt.Fprintf(stderr, "runs trail: unknown flag: %s\n", args[i])
			return ExitUsage
		}
	}
	if path == "" {
		fmt.Fprintln(stderr, "runs trail: --path is required")
		return ExitUsage
	}
	if err := SetTrail(id, path); err != nil {
		fmt.Fprintf(stderr, "runs trail: %v\n", err)
		return ExitNoSuchID
	}
	return 0
}

func cmdFinish(args []string, stderr io.Writer) int {
	if len(args) < 1 {
		fmt.Fprintln(stderr, "runs finish: needs a run id")
		return ExitUsage
	}
	id := args[0]
	args = args[1:]
	var rc, session, modelActual string
	for i := 0; i < len(args); i++ {
		var dst *string
		switch args[i] {
		case "--rc":
			dst = &rc
		case "--session":
			dst = &session
		case "--model-actual":
			dst = &modelActual
		default:
			fmt.Fprintf(stderr, "runs finish: unknown flag: %s\n", args[i])
			return ExitUsage
		}
		if i+1 >= len(args) {
			fmt.Fprintf(stderr, "runs finish: %s needs a value\n", args[i])
			return ExitUsage
		}
		*dst = args[i+1]
		i++
	}
	path := filepath.Join(Dir(), id+".run")
	if fi, err := os.Stat(path); err != nil || fi.IsDir() {
		fmt.Fprintf(stderr, "runs finish: no such run: %s\n", id)
		return ExitNoSuchID
	}
	if rc == "" {
		rc = "1" // a finish with no rc is a failure, not a success
	}

	// Append rather than rewrite: the start fields are the launcher's record of
	// what it launched, and Read takes the last assignment of a key.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		fmt.Fprintf(stderr, "runs finish: %v\n", err)
		return 1
	}
	defer f.Close()
	fmt.Fprintf(f, "rc=%s\n", sanitize(rc))
	fmt.Fprintf(f, "finishedAt=%d\n", nowUnix())
	if session != "" {
		fmt.Fprintf(f, "session=%s\n", sanitize(session))
	}
	if modelActual != "" {
		fmt.Fprintf(f, "modelActual=%s\n", sanitize(modelActual))
	}
	return 0
}

// pruneOlderThan drops finished records past their keep window. Only finished
// records expire. An orphan is kept: it is the only trace left of a round that
// died, and deleting it on a timer would delete the evidence before anyone
// read it.
func pruneOlderThan(keep int64) int {
	dir := Dir()
	ents, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	now := nowUnix()
	n := 0
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), ".run") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		r, err := Read(path)
		if err != nil {
			// A record that cannot be parsed is garbage, not evidence, and
			// prune is the only thing that ever removes it — the readers all
			// skip it silently, so left alone it would sit in the directory
			// forever. Deliberately not counted: `pruned N` means N expired
			// rounds, and a corrupt file was never a round. (Ported from the
			// shell, where the parity gate caught its absence.)
			_ = os.Remove(path)
			continue
		}
		// Only finished records expire. An orphan is kept: it is the only trace
		// left of a round that died, and deleting it on a timer would delete
		// the evidence before anyone read it.
		if r.RC == "" {
			continue
		}
		if now-atoi(r.FinishedAt) > keep {
			if os.Remove(path) == nil {
				n++
			}
		}
	}
	return n
}

func cmdPrune(args []string, stdout, stderr io.Writer) int {
	keep := KeepSecondsDefault
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--keep-seconds":
			if i+1 >= len(args) {
				fmt.Fprintln(stderr, "runs prune: --keep-seconds needs a value")
				return ExitUsage
			}
			keep = atoi(args[i+1])
			i++
		default:
			fmt.Fprintf(stderr, "runs prune: unknown flag: %s\n", args[i])
			return ExitUsage
		}
	}
	fmt.Fprintf(stdout, "pruned %d\n", pruneOlderThan(keep))
	return 0
}

// cmdDismiss is the verb for after reading an orphan. Prune keeps orphans on
// principle (evidence until read), but "until read" needs a verb for after
// reading, or an acknowledged orphan haunts the status line forever. It never
// takes a running round: a live pid is work, not residue.
func cmdDismiss(args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		fmt.Fprintln(stderr, "runs dismiss: needs a run id")
		return ExitUsage
	}
	id := args[0]
	path := filepath.Join(Dir(), id+".run")
	if fi, err := os.Stat(path); err != nil || fi.IsDir() {
		fmt.Fprintf(stderr, "runs dismiss: no such run: %s\n", id)
		return ExitNoSuchID
	}
	r, err := Read(path)
	if err != nil {
		_ = os.Remove(path)
		fmt.Fprintf(stdout, "dismissed %s (unreadable record)\n", id)
		return 0
	}
	if r.State() == Running {
		fmt.Fprintf(stderr, "runs dismiss: %s is running (pid %s alive) — a live round is work, not residue\n", id, r.Pid)
		return ExitRefused
	}
	if err := os.Remove(path); err != nil {
		fmt.Fprintf(stderr, "runs dismiss: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "dismissed %s\n", id)
	return 0
}

// jsonRecord fixes the key order the shell version emitted, because a
// downstream consumer reads this shape.
type jsonRecord struct {
	ID             string `json:"id"`
	Pid            *int64 `json:"pid"`
	Label          string `json:"label"`
	Provider       string `json:"provider"`
	Harness        string `json:"harness"`
	Model          string `json:"model"`
	Cwd            string `json:"cwd"`
	Spec           string `json:"spec"`
	Log            string `json:"log"`
	ProgressDir    string `json:"progressDir"`
	Trail          string `json:"trail"`
	TrailFormat    string `json:"trailFormat"`
	OwnerSession   string `json:"ownerSession"`
	OwnerClaudePid string `json:"ownerClaudePid"`
	StartedAt      *int64 `json:"startedAt"`
	RC             *int64 `json:"rc"`
	FinishedAt     *int64 `json:"finishedAt"`
	Session        string `json:"session"`
	ModelActual    string `json:"modelActual"`
	State          string `json:"state"`
	ElapsedSeconds *int64 `json:"elapsedSeconds"`
	IdleSeconds    *int64 `json:"idleSeconds"`
	Stalled        bool   `json:"stalled"`
}

func numOrNull(s string) *int64 {
	if !isInt(s) {
		return nil
	}
	n := atoi(s)
	return &n
}

func cmdJSON(f filter, stdout io.Writer) int {
	recs, _ := List()
	now := nowUnix()
	out := []jsonRecord{}
	for _, r := range recs {
		if !f.mine(r) {
			continue
		}
		el := r.Elapsed(now)
		j := jsonRecord{
			ID: r.ID, Pid: numOrNull(r.Pid), Label: r.Label, Provider: r.Provider,
			Harness: r.Harness, Model: r.Model, Cwd: r.Cwd, Spec: r.Spec, Log: r.Log,
			ProgressDir: r.ProgressDir, Trail: r.Trail, TrailFormat: r.TrailFormat,
			OwnerSession:   r.OwnerSession,
			OwnerClaudePid: r.OwnerClaudePid, StartedAt: numOrNull(r.StartedAt),
			RC: numOrNull(r.RC), FinishedAt: numOrNull(r.FinishedAt),
			Session: r.Session, ModelActual: r.ModelActual,
			State: string(r.State()), ElapsedSeconds: &el,
			Stalled: r.Stalled(now),
		}
		if idle, ok := r.Idle(now); ok {
			v := idle
			j.IdleSeconds = &v
		}
		out = append(out, j)
	}
	enc := json.NewEncoder(stdout)
	enc.SetEscapeHTML(false)
	return encodeOrFail(enc, out)
}

func encodeOrFail(enc *json.Encoder, v any) int {
	if err := enc.Encode(v); err != nil {
		return 1
	}
	return 0
}
