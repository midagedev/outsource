package launch

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/midagedev/outsource/internal/cred"
	"github.com/midagedev/outsource/internal/quota"
	"github.com/midagedev/outsource/internal/report"
	"github.com/midagedev/outsource/internal/telemetry"
)

// Exit codes specific to the zai launcher. 64/72 are shared with grok-run and
// mean the same things there.
const (
	ExitVisionRefused  = 65  // the spec names an image and the provider cannot see pixels
	ExitQuotaFloor     = 66  // --require-quota floor missed, or not evaluable
	ExitHarnessMissing = 69  // the harness CLI is not on PATH
	ExitModelIdentity  = 70  // the model that answered is not the one requested, or cannot be verified
	ExitTimedOut       = 124 // --max-seconds ceiling hit; the harness was killed
	ExitNoCredential   = 1
)

// imageRef matches a spec that names an image file. Case-insensitive, and the
// extension must end the token so a word like "gifted" does not trip it.
var imageRef = regexp.MustCompile(`(?i)\.(png|jpe?g|webp|gif)([^[:alnum:]]|$)`)

// nestedEnvKey marks every harness child's environment, so a delegate that
// tries to launch a round of its own is refused at the door. Measured
// 2026-08-27 (GDK-1034 round): a GLM delegate read the lead-side launch
// procedure in its assembled spec, decided it WAS the lead, and launched a
// nested round into the same worktree — clean exit, zero implementation,
// and a round running that no lead had launched. The delegate is an
// executor; only the lead launches. OUTSOURCE_ALLOW_NESTED=1 overrides, for
// a lead who deliberately runs a launcher-inside-a-launcher experiment.
const nestedEnvKey = "OUTSOURCE_ROUND"

func nestedLaunchRefusal(tool string, stderr io.Writer) bool {
	if os.Getenv(nestedEnvKey) != "1" || os.Getenv("OUTSOURCE_ALLOW_NESTED") == "1" {
		return false
	}
	fmt.Fprintf(stderr, "%s: this process is already inside a delegated round (%s=1), and a delegate does not launch rounds — implement the spec directly; launching is the lead's job. If a lead is deliberately nesting launchers, set OUTSOURCE_ALLOW_NESTED=1.\n", tool, nestedEnvKey)
	telemetry.Note("why", "nested launch refused: a delegate tried to spawn a round")
	return true
}

// nestedEnv appends the marker to a harness child's environment.
func nestedEnv(env []string) []string {
	return append(env, nestedEnvKey+"=1")
}

type opts struct {
	cwd, spec, log, session, model, harness, providerName  string
	configDir, label, doneMarker, requireQuota, maxSeconds string
	allowAgent, noVisionCheck, detach, foreground          bool
}

// OutsourceMain launches a delegated run on a third-party provider, on one of
// the wired harnesses. The provider is a table entry, not a hardcoded
// constant, and the launcher asserts which model actually answered before
// calling the round a success.
//
// The model is the point; the harness is only how it is driven headlessly.
func OutsourceMain(args []string, stdout, stderr io.Writer) int {
	if nestedLaunchRefusal("outsource-run", stderr) {
		return ExitUsage
	}
	o := opts{
		harness:      os.Getenv("OUTSOURCE_HARNESS"),
		providerName: envOr("OUTSOURCE_PROVIDER", "zai"),
	}
	for i := 0; i < len(args); i++ {
		need := func() (string, bool) {
			if i+1 >= len(args) {
				fmt.Fprintf(stderr, "outsource: %s needs a value\n", args[i])
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
		case "--session":
			o.session, ok = need()
		case "--model":
			o.model, ok = need()
		case "--harness":
			o.harness, ok = need()
		case "--provider":
			o.providerName, ok = need()
		case "--config-dir":
			o.configDir, ok = need()
		case "--label":
			o.label, ok = need()
		case "--done-marker":
			o.doneMarker, ok = need()
		case "--require-quota":
			o.requireQuota, ok = need()
		case "--max-seconds":
			o.maxSeconds, ok = need()
		case "--allow-agent":
			o.allowAgent, ok = true, true
		case "--no-vision-check":
			o.noVisionCheck, ok = true, true
		case "--detach":
			o.detach, ok = true, true
		case "--foreground":
			o.foreground, ok = true, true
		case "--list-wiring":
			// "What can run where" as one command instead of a read of
			// wiring.go. Printed and exited before every other flag is
			// resolved, so it answers even when the rest of the line is wrong.
			fmt.Fprint(stdout, wiringMatrix())
			return 0
		case "-h", "--help":
			// The harness and provider lists are derived, so a new arm cannot
			// be routable and undocumented at the same time.
			fmt.Fprintf(stdout, "usage: outsource-run --cwd <dir> --spec <file> --log <file> [--session S] [--model M] [--harness %s] [--provider %s] [--config-dir D] [--label L] [--done-marker M] [--require-quota N] [--max-seconds N] [--allow-agent] [--no-vision-check] [--detach] [--foreground] [--list-wiring]\n",
				strings.Join(harnessNameList(), "|"), strings.Join(providerNameList(), "|"))
			return 0
		default:
			fmt.Fprintf(stderr, "unknown flag: %s\n", args[i])
			return ExitUsage
		}
		if !ok {
			return ExitUsage
		}
	}

	if o.cwd == "" {
		fmt.Fprintln(stderr, "--cwd is required")
		return ExitUsage
	}
	if fi, err := os.Stat(o.cwd); err != nil || !fi.IsDir() {
		fmt.Fprintf(stderr, "--cwd does not exist: %s\n", o.cwd)
		return ExitUsage
	}
	if o.spec == "" {
		fmt.Fprintln(stderr, "--spec is required")
		return ExitUsage
	}
	specBody, err := os.ReadFile(o.spec)
	if err != nil {
		fmt.Fprintf(stderr, "--spec does not exist: %s\n", o.spec)
		return ExitUsage
	}

	p, ok := findProvider(o.providerName)
	if !ok {
		fmt.Fprintf(stderr, "unknown provider: %s (known: %s)\n", o.providerName, providerNames())
		return ExitUsage
	}
	// The provider's own model env var (zai's GLM_DELEGATE_MODEL). Applying one
	// provider's pin to every provider leaked a glm-* id into opencode's -m,
	// which opencode then rejected (the remainder must be openrouter/…).
	// Scoped by the table, after the provider is known and after --model, so an
	// explicit --model still wins.
	o.model = seedModel(p.name, o.model)
	// Checked here — after the seed, before the registry and before the
	// --detach re-exec — for the same reason as the model-form check below:
	// past the re-exec there is no caller left to tell. Exit 70 because this
	// is the model-identity failure known before spending the round.
	if msg, ok := mappedModelError(p, o.model); !ok {
		fmt.Fprintln(stderr, msg)
		telemetry.Note("why", "mapped model: request would be answered by a different model")
		return ExitModelIdentity
	}
	o.harness = defaultHarness(p.name, o.harness)
	// Validated here, not only at the dispatch below, so a usage error is caught
	// before the run registry records a round that was never going to launch.
	h, ok := findHarness(o.harness)
	if !ok {
		fmt.Fprintf(stderr, "--harness must be one of: %s, got: %s\n", harnessNames(), o.harness)
		return ExitUsage
	}
	if msg := pairingRefusal(o.harness, p.name); msg != "" {
		fmt.Fprintln(stderr, msg)
		return ExitUsage
	}
	// A provider whose only routed model was withdrawn has no default to fall
	// back on, and the empty string would become a malformed id inside the
	// harness — under --detach, where nothing can print.
	if msg, ok := requiredModelError(p, h, o.model); !ok {
		fmt.Fprintln(stderr, msg)
		telemetry.Note("why", "provider has no default model and --model was absent")
		return ExitUsage
	}
	if o.configDir == "" {
		o.configDir = filepath.Join(tmpDir(), "outsource-glm-cfg")
	}
	if err := os.MkdirAll(o.configDir, 0o755); err != nil {
		fmt.Fprintf(stderr, "outsource: %v\n", err)
		return ExitUsage
	}

	// --done-marker is a contract the spec must be able to satisfy. Nothing
	// injects the string into the prompt, so a marker the spec never mentions is
	// something the delegate cannot know about (measured 2026-08-18: three
	// delivered rounds, all reported absent). Refused before contacting the
	// provider and before registering a round — same point in the sequence as the
	// grok launcher.
	if o.doneMarker != "" && !strings.Contains(string(specBody), o.doneMarker) {
		fmt.Fprintf(stderr, "outsource: --done-marker '%s' does not appear in the spec (%s). Add that exact string as the spec's last line (the completion marker), then relaunch.\n", o.doneMarker, o.spec)
		telemetry.Note("why", "done-marker not present in the spec")
		return ExitUsage
	}

	// Same non-TTY refusal as grok-run, before quota (which contacts a
	// provider) and before a round is registered. --detach skips it; the
	// re-exec child is marked via cmd.Env so a nil stdin does not refuse.
	if !skipForegroundGuard(o.foreground, o.detach) {
		refuseNonTTYForeground("outsource-run", stderr)
		return ExitUsage
	}

	// The vision capability comes from the table plus the per-model
	// refinement (modelVision) — never a provider-name test at the call site.
	if !o.noVisionCheck && !modelVision(p, o.model) && imageRef.Match(specBody) {
		fmt.Fprintf(stderr, "outsource: spec %s references an image file, but model '%s' on provider '%s' cannot see images. This guard refuses a pixel verdict — a spec that only names an image as an artifact (capture harness, pixel-decoding script; see references/glm.md) wants --no-vision-check; a spec that asks the model to look at pixels wants a vision-capable model (on zai: --model glm-5.3-flash; see references/glm.md).\n",
			o.spec, orDefault(o.model, p.defaultModel), o.providerName)
		telemetry.Note("why", "vision guard: spec names an image, model is blind")
		return ExitVisionRefused
	}

	// Plan quota, pre-flight only: refusing to start a round the plan cannot
	// finish. Deliberately NOT used to price a round — a plan quota is a
	// plan-wide counter that concurrent rounds and other sessions move too, so a
	// before/after delta around one round measures the machine, not the round.
	if o.requireQuota != "" {
		switch rc := quota.Main([]string{"--provider", o.providerName, "--quiet",
			"--require-window", o.requireQuota}, io.Discard, stderr); rc {
		case 0:
		case quota.ExitGated:
			fmt.Fprintf(stderr, "outsource: refusing to launch — provider '%s' is below the --require-quota %s%% floor (reason above). Wait for the reset or run this track on another provider.\n", o.providerName, o.requireQuota)
			telemetry.Note("why", "quota floor: plan too low to start")
			return ExitQuotaFloor
		case quota.ExitUsage:
			// quota knows a different provider set: it reads PLAN quotas, so it
			// covers the subscription backends and not the pay-per-token api-key
			// ones, which have no plan window to be below.
			fmt.Fprintf(stderr, "outsource: --require-quota is not available for provider '%s' — plan quotas are read for the subscription backends and this provider bills per token. Drop the flag for this track.\n", o.providerName)
			return ExitQuotaFloor
		default:
			// Fail closed: a gate that cannot be evaluated is not a gate that passed.
			fmt.Fprintf(stderr, "outsource: refusing to launch — --require-quota could not be evaluated (quota exit %d, reason above)\n", rc)
			return ExitQuotaFloor
		}
	}

	if o.maxSeconds != "" {
		if n, err := strconv.Atoi(o.maxSeconds); err != nil || n < 1 {
			fmt.Fprintf(stderr, "--max-seconds wants a positive whole number of seconds, got: %s\n", o.maxSeconds)
			return ExitUsage
		}
	}

	// Fail before registering only when we can positively see that openrouter
	// is missing from opencode's auth.json. A missing file is not proof —
	// newer opencode also keeps credentials in opencode.db, which this
	// binary does not open.
	if p.name == "openrouter" && openrouterCredsPositivelyAbsent() {
		fmt.Fprintln(stderr, "outsource: no OpenRouter credentials in opencode's auth store; run `opencode auth login` then retry")
		return ExitNoCredential
	}

	// The harness's own model-form rule, checked here and not only inside the
	// harness: past the re-exec below there is no caller left to tell. The
	// table owns which harnesses have such a rule.
	if h.modelForm != nil {
		if msg, ok := h.modelForm(o.model, p.name); !ok {
			fmt.Fprintln(stderr, msg)
			telemetry.Note("why", "--model is not in the "+h.name+" harness's form")
			return ExitUsage
		}
	}

	if o.detach {
		if _, err := exec.LookPath(h.bin); err != nil {
			fmt.Fprintf(stderr, "harness %s needs the '%s' CLI on PATH\n", h.name, h.bin)
			return ExitHarnessMissing
		}
		label := o.label
		if label == "" {
			label = defaultLabel(o.spec)
		}
		return reexecDetached("outsource-run", args, label, o.log, stdout, stderr)
	}

	r := &round{o: o, p: p, stdout: stdout, stderr: stderr, specBody: string(specBody)}
	return r.run()
}

// round carries the state the harness paths share, so the sentinel and the
// registry are written from one place regardless of which harness ran.
type round struct {
	o        opts
	p        provider
	stdout   io.Writer
	stderr   io.Writer
	specBody string
	sid      string
	// bailed marks an exit taken BEFORE the harness was dispatched: a missing CLI,
	// a rejected --model, an unresolvable credential. The registry entry is still
	// closed (a record left open would read as an orphan), but no sentinel is
	// written and no SESSION line is printed, because neither would be true. This
	// is the shell's behaviour: those paths call exit directly rather than going
	// through finish().
	bailed      bool
	modelActual string
	// modelVerdict/modelSource record WHY the identity assertion landed where
	// it did. They exist because the assertion runs at the END of a round, so
	// a --detach failure cannot be moved earlier the way a usage error can,
	// and the launcher's own stderr has nowhere to go there: <log>.err carries
	// the harness's stderr, not ours. Measured 2026-08-26 — an ox-alpha round
	// finished its work, exited 70, and left model_actual= empty with no
	// recoverable reason anywhere on disk.
	modelVerdict string
	modelSource  string
	hold         *signalHold
	runID        string
	timedOut     bool
}

func (r *round) run() int {
	r.hold = holdSignals()
	// A sentinel from an earlier round on this --log path would read as this
	// round's completion (see clearStaleSentinel). --detach already cleared
	// it in the parent; the foreground path starts here.
	clearStaleSentinel(r.o.log)

	// Where this round leaves a live trail, so the registry can tell a round
	// that is working from one that is stuck without ever interrupting either.
	// The location is per-harness and the table owns it (harness.progress);
	// what matters here is that it is NOT the --log file for every harness —
	// the claude-code harness writes that only once, at the end, so a perfectly
	// healthy round shows an empty log for its entire life.
	h, harnessKnown := findHarness(r.o.harness)
	progressDir := ""
	if harnessKnown && h.progress != nil {
		progressDir = h.progress(r.o)
	}

	// The model recorded in the REGISTRY is the bare table default when --model was
	// not given, even on crush — because the crush qualification to provider/id
	// happens inside that branch, after registration. The sentinel then carries the
	// qualified id. That is the shell's behaviour and the two fields disagree by
	// construction; the sentinel is the authority and the registry field is
	// informational, so this port keeps the difference rather than inventing a
	// third answer in the riskiest file of the set.
	regModel := r.o.model
	if regModel == "" {
		regModel = r.p.defaultModel
	}

	label := r.o.label
	if label == "" {
		label = defaultLabel(r.o.spec)
	}
	// Registered once the round is actually going to be attempted — after the
	// guards, before the harness is dispatched. A guard that refuses to launch has
	// not started a round, and recording one would make the registry lie.
	r.runID = registerRun(label, r.p.name, r.o.harness, regModel,
		r.o.cwd, r.o.spec, r.o.log, progressDir)

	var rc int
	if !harnessKnown || h.run == nil {
		fmt.Fprintf(r.stderr, "--harness must be one of: %s, got: %s\n", harnessNames(), r.o.harness)
		r.bailed = true
		rc = ExitUsage
	} else {
		rc = h.run(r)
	}
	return r.finish(rc)
}

// finish writes the sentinel, closes the registry entry, prints the SESSION line
// and returns the exit code. Both harnesses come through here.
func (r *round) finish(rc int) int {
	// rc is a LIFECYCLE signal: the harness exited cleanly. It says nothing about
	// whether the round did its job. Both halves of that gap were measured on one
	// day (2026-08-16): one round exited rc=0 having written no code at all, and
	// another exited rc=0 with no edits because the spec's own precondition check
	// told it to stop. The first is a failure, the second is correct, and rc cannot
	// tell them apart.
	markerLines := ""
	if !r.bailed && r.o.log != "" && r.o.doneMarker != "" {
		verdict, scope := r.markerVerdict()
		markerLines = fmt.Sprintf("done_marker=%s (%s)\ndone_marker_scope=%s\n",
			verdict, r.o.doneMarker, scope)
		if verdict == "absent" && rc == 0 {
			fmt.Fprintf(r.stderr, "outsource: the round finished but --done-marker '%s' is absent; not claiming a pass (exit 72). Judge by the tree, not this exit code.\n", r.o.doneMarker)
			telemetry.Note("why", "round finished, completion marker absent")
			rc = ExitNoMarker
		}
	}
	finishRun(r.runID, rc, r.sid, r.modelActual)
	if r.bailed {
		return rc
	}
	if r.o.log != "" {
		body := r.sentinelBody(rc, markerLines, time.Now().UTC())
		if err := os.WriteFile(r.o.log+".rc", []byte(body), 0o644); err != nil {
			fmt.Fprintf(r.stderr, "outsource: warning: could not write sentinel %s.rc\n", r.o.log)
		}
	}
	sid := r.sid
	if sid == "" {
		sid = "unknown"
	}
	fmt.Fprintf(r.stdout, "SESSION %s\n", sid)
	return rc
}

// markerVerdict prefers the FINAL REPORT, the same scope the grok launcher uses:
// a marker quoted in a plan, a tool result, or an echoed spec must not count as
// completion.
func (r *round) markerVerdict() (verdict, scope string) {
	f, err := os.Open(r.o.log)
	if err == nil {
		defer f.Close()
		if fi, err := f.Stat(); err == nil && fi.Size() > 0 {
			if rep, src, ok := report.ExtractSource(f); ok {
				// Last-line identity, not Contains: a report that merely QUOTES
				// the marker ("…will end with `DONE-X`") is a promise, not a
				// completion (field false-positive 2026-08-22).
				if report.EndsWithMarker(rep, r.o.doneMarker) {
					return "found", "report"
				}
				if r.o.harness != "crush" || src != report.SourcePlainTail {
					return "absent", "report"
				}
				// A plain-text crush log has no plan-vs-report boundary: the
				// "report" above is plainTail's heading guess (0.13.3), and
				// scoring its absence as the round's absence silently
				// strengthened the marker contract for the one harness whose
				// log offers no such boundary — 0.13.3 orphaned the whole-log
				// arm below by making Extract succeed on every non-JSON log.
				// A structured (JSON-evented) log keeps the strict verdict:
				// that is what keeps a marker quoted in a plan from counting.
			}
		}
	}
	if r.o.harness == "crush" {
		// `crush run -q` writes the model's stdout (this launcher merges stderr) as
		// plain text — not JSONL with a result event and not grok text-deltas. There
		// is no extractable final report and so no plan-vs-report boundary to
		// honour. Grep the whole log for this harness only, and record the scope so
		// a "found" here is not silently the same verdict as a "found" in a final
		// report.
		b, err := os.ReadFile(r.o.log)
		if err == nil && strings.Contains(string(b), r.o.doneMarker) {
			return "found", "log"
		}
		return "absent", "log"
	}
	return "absent", "report"
}

// ---- wall-clock watchdog ---------------------------------------------------
// `timeout(1)` is GNU coreutils and absent from a stock macOS, so the ceiling is
// built here. The child is put in its OWN process group so the signal can reach
// the whole tree: the harnesses spawn children, and a TERM to the top process
// alone leaves the model CLI running and the round only LOOKS stopped.
//
// The default posture is never to interrupt. Measured on ten delivered rounds,
// duration ran 13 minutes to 1h50m and tracked message count almost linearly —
// those rounds were long because there was a lot of work, and cutting one at an
// hour truncates a working delegate mid-edit. --max-seconds exists for rounds
// whose loss is acceptable up front, and has no default.
func (r *round) startWatchdog(cmd *exec.Cmd, done <-chan struct{}) {
	if r.o.maxSeconds == "" {
		return
	}
	n, err := strconv.Atoi(r.o.maxSeconds)
	if err != nil || n < 1 {
		return
	}
	go func() {
		select {
		case <-done:
			return
		case <-time.After(time.Duration(n) * time.Second):
		}
		if cmd.Process == nil {
			return
		}
		r.timedOut = true
		pgid := cmd.Process.Pid
		_ = syscall.Kill(-pgid, syscall.SIGTERM)
		select {
		case <-done:
			return
		case <-time.After(10 * time.Second):
		}
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
	}()
}

func (r *round) timedOutNote() {
	fmt.Fprintf(r.stderr, "outsource: --max-seconds %s reached; the %s harness was killed mid-round (exit 124). Whatever it had already written to %s is still there — review the tree, and treat the round as unfinished.\n",
		r.o.maxSeconds, r.o.harness, r.o.cwd)
}

// defaultLabel is what to call this track when --label was not given.
//
// The spec's basename is the obvious guess and the wrong one on its own: this
// skill's own documented invocation writes every track's spec to <scratch>/spec.md,
// one scratch dir per track, so three parallel rounds would all register as
// "spec" — the exact case the label is for. A generic basename therefore defers to
// the directory holding it, which is where the track name actually lives.
func defaultLabel(spec string) string {
	base := strings.TrimSuffix(filepath.Base(spec), filepath.Ext(spec))
	switch base {
	case "spec", "task", "prompt", "input", "round", "delegate":
		parent := filepath.Base(filepath.Dir(spec))
		switch parent {
		case "", ".", "/", "tmp", "temp", "scratch", "sp", "specs":
			// no more specific than the basename
		default:
			base = parent
		}
	}
	return base
}

func orDefault(v, def string) string {
	if v != "" {
		return v
	}
	return def
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func tmpDir() string { return envOr("TMPDIR", "/tmp") }

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

var _ = cred.Base // used by the harness files

// sentinelBody renders the completion evidence. Pulled out of finish so it can
// be read back in a test: the sentinel is the only thing a --detach caller has
// after the round ends, and every field here exists because something was once
// unrecoverable without it.
func (r *round) sentinelBody(rc int, markerLines string, now time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "rc=%d\n", rc)
	fmt.Fprintf(&b, "finished=%s\n", now.Format("2006-01-02T15:04:05Z"))
	fmt.Fprintf(&b, "harness=%s\nprovider=%s\n", r.o.harness, r.p.name)
	fmt.Fprintf(&b, "model_requested=%s\nmodel_actual=%s\nsession=%s\n", r.o.model, r.modelActual, r.sid)
	if r.modelVerdict != "" {
		fmt.Fprintf(&b, "model_verdict=%s\n", r.modelVerdict)
	}
	if r.modelSource != "" {
		fmt.Fprintf(&b, "model_source=%s\n", r.modelSource)
	}
	b.WriteString(markerLines)
	if s := r.hold.name(); s != "" {
		fmt.Fprintf(&b, "wrapper_signal=%s\n", s)
	}
	return b.String()
}
