package launch

import (
	"crypto/rand"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/midagedev/outsource/internal/report"
	"github.com/midagedev/outsource/internal/runs"
	"github.com/midagedev/outsource/internal/telemetry"
)

// Exit codes are the contract watchers branch on.
const (
	ExitUsage      = 64 // unknown flag / git profile, missing required flags, or a --done-marker not in the spec
	ExitBadPath    = 66 // no such cwd, or an unreadable spec
	ExitNoStart    = 69 // grok CLI missing, or the process exited without writing
	ExitNoSentinel = 71 // EXIT path: no sentinel was written (a bug in this code)
	ExitNoMarker   = 72 // --done-marker set, clean exit, marker absent from the report
)

// gitProfiles is the single owner of the deny-flag strings.
// references/grok.md keeps the rationale — why the worktree denies are
// per-subcommand, why glob denies are a net and not a proof — and points here.
var gitProfiles = map[string][]string{
	"strict": {
		"git commit*", "git push*", "git checkout*", "git switch*",
		"git stash*", "git restore*", "git add*", "git rebase*",
		"git reset*", "git merge*", "git cherry-pick*", "git tag*",
		"git worktree add*", "git worktree remove*", "git worktree prune*",
		"gh pr create*", "gh pr merge*", "gh repo *",
	},
	"readonly-plus": nil, // handled below: a blanket ban, not a pattern list
	"trusted":       nil, // no denies at all
}

// researchNotice is prepended to the spec for a --research round. A research
// round has no write tool, but a spec that asks for a report FILE reads as
// writable work — measured 2026-08-17: the model looped "writing the report" for
// 301 turns ($5.77) with nothing to write with. The runner owns the
// contradiction: tell the model up front, in the spec itself, that the final
// message is the only deliverable channel.
const researchNotice = "> [runner notice — research mode] 이 라운드에는 파일 쓰기 도구가 없다.\n" +
	"> 스펙이 산출물을 파일로 요구하더라도 파일은 만들 수 없으며, 모든 산출물은\n" +
	"> **최종 메시지 본문**으로 제출하라. 쓰기 시도를 반복하지 말 것.\n\n"

type grokOpts struct {
	cwd, spec, log, label, marker, profile, resume string
	model, effort, maxTurns                        string
	research, detach, foreground                   bool
	extra                                          []string
}

// detachedEnvKey is set only on the --detach re-exec child's cmd.Env, so a
// child whose stdin is nil (not a TTY) does not refuse itself. The name is
// shared with outsource-run; both launchers re-exec the same binary.
const detachedEnvKey = "OUTSOURCE_DETACHED"

// GrokMain launches a raw grok CLI round with the same observability contract as
// the zai launcher: a registry entry while it runs, a <log>.rc sentinel when it
// exits, and a done-marker verdict inside that sentinel.
//
// Foreground by default: the wrapper is the caller's to kill, which is why
// signals are held rather than obeyed. Pass --detach to have the launcher own
// its single background layer (a re-exec in its own session) instead of
// hand-assembling one with nohup/& — the foreground form dies with the
// caller's process group, e.g. an orchestrator's command timeout.
func GrokMain(args []string, stdout, stderr io.Writer) int {
	o := grokOpts{profile: "strict", model: "grok-4.6", effort: "xhigh", maxTurns: "1200"}
	for i := 0; i < len(args); i++ {
		need := func() (string, bool) {
			if i+1 >= len(args) {
				fmt.Fprintf(stderr, "grok-run: %s needs a value\n", args[i])
				return "", false
			}
			i++
			return args[i], true
		}
		var ok bool
		switch args[i] {
		case "--cwd":
			o.cwd, ok = need()
		case "--spec":
			o.spec, ok = need()
		case "--log":
			o.log, ok = need()
		case "--label":
			o.label, ok = need()
		case "--done-marker":
			o.marker, ok = need()
		case "--git-profile":
			o.profile, ok = need()
		case "--resume":
			o.resume, ok = need()
		case "--model":
			o.model, ok = need()
		case "--reasoning-effort":
			o.effort, ok = need()
		case "--max-turns":
			o.maxTurns, ok = need()
		case "--research":
			o.research, ok = true, true
		case "--detach":
			o.detach, ok = true, true
		case "--foreground":
			o.foreground, ok = true, true
		case "-h", "--help":
			fmt.Fprintln(stdout, "usage: grok-run --cwd <dir> --spec <file> --log <file.ndjson> [--label L] [--done-marker M] [--git-profile strict|readonly-plus|trusted] [--resume SID] [--model M] [--reasoning-effort E] [--max-turns N] [--research] [--detach] [--foreground] [-- <grok flags...>]")
			return 0
		case "--":
			o.extra = append(o.extra, args[i+1:]...)
			i = len(args)
			ok = true
		default:
			fmt.Fprintf(stderr, "grok-run: unknown flag: %s\n", args[i])
			return ExitUsage
		}
		if !ok {
			return ExitUsage
		}
	}
	if o.cwd == "" || o.spec == "" || o.log == "" {
		fmt.Fprintln(stderr, "usage: grok-run --cwd <dir> --spec <file> --log <file.ndjson> [...]")
		return ExitUsage
	}
	if fi, err := os.Stat(o.cwd); err != nil || !fi.IsDir() {
		fmt.Fprintf(stderr, "grok-run: no such cwd: %s\n", o.cwd)
		return ExitBadPath
	}
	specBody, err := os.ReadFile(o.spec)
	if err != nil {
		fmt.Fprintf(stderr, "grok-run: unreadable spec: %s\n", o.spec)
		return ExitBadPath
	}

	// --done-marker is a contract the spec must be able to satisfy. Nothing
	// injects the string into the prompt — the spec is the whole truth the
	// delegate reads, and spec-lint would not see a hidden append. A lead who
	// passes --done-marker X while the spec never contains X has stated something
	// the delegate cannot know about (measured 2026-08-18: three delivered
	// rounds, all reported absent). Refused here, before contacting the provider
	// and before registering a round.
	if o.marker != "" && !strings.Contains(string(specBody), o.marker) {
		fmt.Fprintf(stderr, "grok-run: --done-marker '%s' does not appear in the spec (%s). Add that exact string as the spec's last line (the completion marker), then relaunch.\n", o.marker, o.spec)
		telemetry.Note("why", "done-marker not present in the spec")
		return ExitUsage
	}
	// --json-schema and --done-marker cannot both be satisfied. The marker is
	// looked for in the final report, and under a schema the final report IS the
	// JSON object: a sentinel line beside it would violate the very schema the
	// flag imposes. Measured 2026-08-18: a vision round returned a complete,
	// schema-valid verdict and still exited 72 — the round was fine, the launch
	// was contradictory.
	if o.marker != "" {
		for _, a := range o.extra {
			if a == "--json-schema" || strings.HasPrefix(a, "--json-schema=") {
				fmt.Fprintln(stderr, "grok-run: --done-marker and --json-schema are mutually exclusive — under a schema the final report IS the JSON object, so the marker can never appear in it. Drop --done-marker (completion = rc 0 + schema-valid stdout), or add a marker field inside the schema.")
				telemetry.Note("why", "done-marker and --json-schema are mutually exclusive")
				return ExitUsage
			}
		}
	}
	if o.label == "" {
		o.label = strings.TrimSuffix(filepath.Base(o.spec), ".md")
	}

	gitFlags, ok := profileFlags(o.profile)
	if !ok {
		fmt.Fprintf(stderr, "grok-run: unknown --git-profile: %s (strict|readonly-plus|trusted)\n", o.profile)
		return ExitUsage
	}

	// Non-TTY foreground is the hazardous shape: a harness-tracked background
	// task whose stdin is a pipe, not a terminal. Measured 2026-08-22: 6
	// rounds (5 with wrapper_signal=TERM, rc=-1) died at wall-clock times
	// aligned to :08:26/:38:26 — an external 30-minute-period killer, not
	// this launcher. --detach is the way out; --foreground is the opt-out
	// for tests and deliberate blocking waits. The detached child is marked
	// via cmd.Env so it does not refuse itself (its stdin is nil).
	if !skipForegroundGuard(o.foreground, o.detach) {
		refuseNonTTYForeground("grok-run", stderr)
		return ExitUsage
	}

	if _, err := exec.LookPath("grok"); err != nil {
		fmt.Fprintln(stderr, "grok-run: grok CLI not on PATH")
		return ExitNoStart
	}

	// --detach: re-exec this same launch in its own session and return. This
	// point — after every usage check, before anything stateful (session id,
	// registry, log files) — is the only safe seam: usage errors stay
	// synchronous on the caller's terminal, while everything the round owns is
	// created by exactly one process. The child is what "one background layer"
	// meant all along; the flag exists because callers kept hand-assembling
	// that layer with nohup and losing rounds to their orchestrator's command
	// timeout delivering TERM to the foreground process group (measured
	// 2026-08-19: rc=143, wrapper_signal=TERM, ten minutes of work gone;
	// 2026-08-22: six more, the 30-min external killer).
	if o.detach {
		return reexecDetached("grok-run", args, o.label, o.log, stdout, stderr)
	}

	promptFile := o.spec
	if o.research {
		// Say it at launch, not only inside the spec. researchNotice tells the
		// model; nothing told the lead. Measured 2026-08-21: three rounds were
		// launched with --research under specs that named a report file, all
		// three correctly reported in-message, and the lead went looking for
		// files that could never exist. The flag combination is legitimate
		// (a research round with no file deliverable is the normal case), so
		// this is a notice rather than the usage error --json-schema gets.
		fmt.Fprintln(stderr, "grok-run: research mode — no write tools; the round's only deliverable channel is its final message. If your spec asks for a report FILE, drop --research or drop the file.")
		gitFlags = append(gitFlags, "--deny", "Write", "--deny", "Edit",
			"--disallowed-tools", "write,search_replace")
		f, err := os.CreateTemp(os.TempDir(), "grok-spec.")
		if err != nil {
			fmt.Fprintf(stderr, "grok-run: could not stage the research spec: %v\n", err)
			return ExitUsage
		}
		if _, err := f.WriteString(researchNotice + string(specBody)); err != nil {
			f.Close()
			fmt.Fprintf(stderr, "grok-run: could not stage the research spec: %v\n", err)
			return ExitUsage
		}
		f.Close()
		promptFile = f.Name()
	}

	// A session id is pinned once (-s) and only resumed afterwards (-r): grok
	// rejects a second -s on a used id. --resume is for stop-then-revise.
	sid, sessionFlag := o.resume, "-r"
	if sid == "" {
		sid, sessionFlag = newUUID(), "-s"
	}

	rcFile := o.log + ".rc"
	if err := os.MkdirAll(filepath.Dir(o.log), 0o755); err != nil {
		fmt.Fprintf(stderr, "grok-run: %v\n", err)
		return ExitUsage
	}
	base := strings.TrimSuffix(o.log, ".ndjson")
	_ = os.WriteFile(base+".sid", []byte(sid+"\n"), 0o644)

	hold := holdSignals()
	sentinelWritten := false
	writeSentinel := func(rc int, verdict string) {
		var b strings.Builder
		fmt.Fprintf(&b, "rc=%d\n", rc)
		fmt.Fprintf(&b, "finished=%s\n", time.Now().UTC().Format("2006-01-02T15:04:05Z"))
		b.WriteString("harness=grok-cli\nprovider=xai\n")
		fmt.Fprintf(&b, "model_requested=%s\nsession=%s\n", o.model, sid)
		if o.marker != "" {
			fmt.Fprintf(&b, "done_marker=%s\ndone_marker_scope=report\n", verdict)
		}
		if s := hold.name(); s != "" {
			fmt.Fprintf(&b, "wrapper_signal=%s\n", s)
		}
		_ = os.WriteFile(rcFile, []byte(b.String()), 0o644)
		sentinelWritten = true
	}
	// Last resort for exits that never reach a writeSentinel call: an exit
	// without a sentinel is the one outcome watchers cannot classify, so 71 names
	// it. Does not run on SIGKILL.
	defer func() {
		if !sentinelWritten {
			writeSentinel(ExitNoSentinel, "absent")
		}
	}()

	// The owner keys are passed here and were NOT in the shell version, which is a
	// fix rather than a port difference. Without them a raw grok round registers
	// as nobody's, and a session-scoped status line — the default — filters it
	// out. So the shell's grok rounds were invisible in exactly the place this
	// launcher's header says it was written to make them visible. Verified before
	// changing it: `runs line --owner X` returns nothing for an unowned record.
	runID := registerRun(o.label, "xai", "grok-cli", o.model, o.cwd, promptFile, o.log, "")

	logf, err := os.Create(o.log)
	if err != nil {
		fmt.Fprintf(stderr, "grok-run: cannot write the log: %v\n", err)
		return ExitUsage
	}
	errf, _ := os.Create(base + ".err")
	cmd := exec.Command("grok", append([]string{
		sessionFlag, sid, "--cwd", o.cwd,
		"--prompt-file", promptFile,
		"-m", o.model, "--no-memory",
		"--always-approve", "--permission-mode", "bypassPermissions",
		"--reasoning-effort", o.effort, "--max-turns", o.maxTurns,
		"--no-plan", "--no-subagents",
		"--output-format", "streaming-json",
	}, append(gitFlags, o.extra...)...)...)
	cmd.Dir = o.cwd
	cmd.Stdout, cmd.Stderr = logf, errf
	if err := cmd.Start(); err != nil {
		logf.Close()
		if errf != nil {
			errf.Close()
		}
		fmt.Fprintf(stderr, "grok-run: could not start grok: %v\n", err)
		writeSentinel(ExitNoStart, "absent")
		finishRun(runID, ExitNoStart, sid, "")
		return ExitNoStart
	}

	// Prove it started before trusting it: the ndjson must exist and be non-empty
	// within the grace window. "The launch command ran" is a lifecycle signal,
	// not evidence — that is the incident this launcher was written for.
	waitErr := make(chan error, 1)
	go func() { waitErr <- cmd.Wait() }()
	started, alive := proveStarted(o.log, cmd, waitErr)
	logf.Close()
	if errf != nil {
		errf.Close()
	}

	if !started && !alive {
		rc := exitCode(<-waitErr)
		if rc == 0 {
			rc = ExitNoStart // exited clean but wrote nothing: still not a round
		}
		fmt.Fprintf(stderr, "grok-run: grok never produced output (rc=%d) — stderr follows:\n", rc)
		fmt.Fprint(stderr, tailLines(base+".err", 5))
		telemetry.Note("why", "provider produced no output")
		writeSentinel(rc, "absent")
		finishRun(runID, rc, sid, "")
		return rc
	}

	rc := exitCode(<-waitErr)

	verdict := "absent"
	if o.marker != "" {
		if f, err := os.Open(o.log); err == nil {
			// The marker is looked for on the LAST LINE of the round's FINAL
			// REPORT, not anywhere in the stream or mid-sentence: a marker
			// quoted in planning or in a "will end with `DONE-X`" promise must
			// not count as completion (field false-positive 2026-08-22).
			if rep, ok := report.Extract(f); ok && report.EndsWithMarker(rep, o.marker) {
				verdict = "found"
			}
			f.Close()
		}
		// A zero exit without the marker in the report is the lie the sentinel
		// exists to catch: downgrade it so no watcher reads rc=0 as delivered. 72
		// is this case only — 70 is the zai launcher's model-identity failure.
		if rc == 0 && verdict == "absent" {
			fmt.Fprintf(stderr, "grok-run: the round finished but --done-marker '%s' is absent; not claiming a pass (exit 72). Judge by the tree, not this exit code.\n", o.marker)
			telemetry.Note("why", "round finished, completion marker absent")
			rc = ExitNoMarker
		}
	}
	writeSentinel(rc, verdict)
	finishRun(runID, rc, sid, "")
	return rc
}

func profileFlags(profile string) ([]string, bool) {
	switch profile {
	case "strict":
		out := make([]string, 0, len(gitProfiles["strict"])*2)
		for _, p := range gitProfiles["strict"] {
			out = append(out, "--deny", "Bash("+p+")")
		}
		return out, true
	case "readonly-plus":
		// The blanket ban. For parallel tracks with tight file boundaries where
		// even a git read prompt is unwanted, and for vision verdicts.
		return []string{"--deny", "Bash(git *)", "--deny", "Bash(git)"}, true
	case "trusted":
		return nil, true
	}
	return nil, false
}

// proveStarted polls for the log to become non-empty, giving up when the child
// dies first. Returns whether output appeared and whether the child is still
// running — the caller needs both, because "no output and still alive" is a slow
// start and "no output and gone" is a failed launch.
func proveStarted(log string, cmd *exec.Cmd, waitErr chan error) (started, alive bool) {
	grace := 30
	if v := os.Getenv("GROK_RUN_STARTUP_GRACE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			grace = n
		}
	}
	// "Did it produce output" is a question about the file, not about timing, so
	// the log is re-checked AFTER the child is seen to exit. Without that, a round
	// that wrote its whole stream and exited faster than the first poll is
	// reported as a failed launch. The shell had the same race and lost it less
	// often only because bash takes longer to reach the loop — measured here the
	// moment the Go version was gated: a complete round came back exit 69,
	// "grok never produced output", with the output sitting in the log.
	for i := 0; i < grace; i++ {
		if fi, err := os.Stat(log); err == nil && fi.Size() > 0 {
			return true, true
		}
		select {
		case err := <-waitErr:
			waitErr <- err // put it back so the caller reads the real status
			if fi, err := os.Stat(log); err == nil && fi.Size() > 0 {
				return true, false
			}
			return false, false
		case <-time.After(time.Second):
		}
	}
	// Grace exhausted with nothing written: still running, so not a failed
	// launch. The registry's idle column is what reports on it from here.
	select {
	case err := <-waitErr:
		waitErr <- err
		if fi, err := os.Stat(log); err == nil && fi.Size() > 0 {
			return true, false
		}
		return false, false
	default:
		return false, true
	}
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode()
	}
	return 1
}

func tailLines(path string, n int) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return ""
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n") + "\n"
}

// newUUID is a lowercase v4, matching `uuidgen | tr A-Z a-z`.
func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// A session id that cannot be random is still better than none; the id
		// only has to be unique against this machine's other rounds.
		return fmt.Sprintf("%d-fallback-session", time.Now().UnixNano())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// skipForegroundGuard is true when a non-TTY stdin must not refuse the launch:
// the caller opted into --detach or --foreground, the process is already the
// detached re-exec child, or stdin really is a terminal.
func skipForegroundGuard(foreground, detach bool) bool {
	if foreground || detach {
		return true
	}
	if os.Getenv(detachedEnvKey) == "1" {
		return true
	}
	return stdinIsTerminal()
}

func stdinIsTerminal() bool {
	fi, err := os.Stdin.Stat()
	if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	// A character device is not yet a terminal: /dev/null is one too, and it
	// is exactly what an agent harness wires as a background task's stdin.
	// Measured 2026-08-23: with the bare ModeCharDevice test this guard never
	// fired in the environment it was built for — a live foreground round
	// sailed through from a harness shell — while the test suite passed,
	// because tests feed stdin from pipes. A real isatty needs an ioctl; the
	// one non-terminal char device that actually reaches this guard is
	// /dev/null, so exclude it by file identity.
	if dn, err := os.Stat(os.DevNull); err == nil && os.SameFile(fi, dn) {
		return false
	}
	return true
}

func refuseNonTTYForeground(tool string, stderr io.Writer) {
	fmt.Fprintf(stderr, "%s: launched foreground without a terminal — as a harness-tracked background task this exact shape lost 6 rounds to an external 30-min killer (2026-08-22); relaunch with --detach (recommended) or pass --foreground if you really mean to block\n", tool)
	telemetry.Note("why", "foreground without a terminal")
}

// reexecDetached starts this binary again without --detach, in its own
// session, and returns. Usage errors must already have been checked: the
// child is not a place they can still print on the caller's terminal.
func reexecDetached(tool string, args []string, label, logPath string, stdout, stderr io.Writer) int {
	self, err := os.Executable()
	if err != nil {
		fmt.Fprintf(stderr, "%s: cannot re-exec for --detach: %v\n", tool, err)
		return ExitUsage
	}
	child := []string{tool}
	for _, a := range args {
		if a != "--detach" {
			child = append(child, a)
		}
	}
	cmd := exec.Command(self, child...)
	cmd.SysProcAttr = detachAttr()
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	cmd.Env = envWithDetached()
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(stderr, "%s: could not detach: %v\n", tool, err)
		return ExitNoStart
	}
	fmt.Fprintf(stdout, "%s: detached (pid=%d, label=%s, log=%s) — completion evidence is %s.rc, watch with: outsource runs\n",
		tool, cmd.Process.Pid, label, logPath, logPath)
	_ = cmd.Process.Release()
	return 0
}

// envWithDetached is the re-exec child's environment: the caller's env plus
// OUTSOURCE_DETACHED=1, with any inherited copy of that key stripped first so
// a stale "0" cannot win. Scoped to this Command.Env only — the parent does
// not os.Setenv, so a sibling the parent later starts does not see the marker.
func envWithDetached() []string {
	prefix := detachedEnvKey + "="
	out := make([]string, 0, len(os.Environ())+1)
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, prefix) {
			continue
		}
		out = append(out, e)
	}
	return append(out, prefix+"1")
}

// registerRun and finishRun call the registry in-process. Bookkeeping must never
// be able to fail a round, so every error here is swallowed.
func registerRun(label, provider, harness, model, cwd, spec, log, progressDir string) string {
	var buf strings.Builder
	args := []string{"start", "--pid", strconv.Itoa(os.Getpid()), "--label", label,
		"--provider", provider, "--harness", harness, "--model", model,
		"--cwd", cwd, "--spec", spec, "--log", log}
	if progressDir != "" {
		args = append(args, "--progress-dir", progressDir)
	}
	args = append(args,
		"--owner", os.Getenv("CLAUDE_CODE_SESSION_ID"),
		"--owner-claude-pid", os.Getenv("CLAUDE_PID"))
	if runs.Main(args, &buf, io.Discard) != 0 {
		return ""
	}
	return strings.TrimSpace(buf.String())
}

func finishRun(id string, rc int, session, modelActual string) {
	if id == "" {
		return
	}
	runs.Main([]string{"finish", id, "--rc", strconv.Itoa(rc),
		"--session", session, "--model-actual", modelActual}, io.Discard, io.Discard)
}
