package launch

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/midagedev/outsource/internal/gitshim"
	"github.com/midagedev/outsource/internal/telemetry"
)

// runMuse drives `muse exec` — the Muse Code CLI, which is provider and harness
// in one: the model is Meta's, and auth is an OAuth device flow the CLI owns at
// ~/.config/muse/auth.json. There is no cred row for the same reason agy has
// none, and the Anthropic-compatible endpoint is NOT the path — measured
// 2026-09-18, a direct call to api.meta.ai/v1/messages with an API key returned
// `billing_error`, while the CLI's own session worked in the same minute.
//
// Measured profile (CLI 1.3.0-R3401.1, 2026-09-18):
//   - `muse exec --json --prompt-file <spec>` is the headless form. JSONL
//     envelopes on stdout, one per line, each with stream/sequence/payload_type.
//   - Stdout flushes per event while the process runs (log grew at 4s, 8s, 16s,
//     20s, 24s of a 24s round), so the --log file IS the live trail, the same
//     as opencode and agy.
//   - Session id is the `stream.id` of any envelope whose stream.kind is
//     "session".
//   - The final report is `run.terminal.completed`'s `text`.
//   - Approval and the sandbox are on by default but do NOT stop a headless
//     round from committing: `git commit --allow-empty` inside a plain
//     `muse exec` moved HEAD. The guard is therefore a PATH-level git shim —
//     see internal/gitshim for why that layer and not a config one.
func (r *round) runMuse() int {
	if _, err := exec.LookPath("muse"); err != nil {
		fmt.Fprintln(r.stderr, "harness muse needs the 'muse' CLI on PATH (install: the muse launcher; sign in with `muse login`)")
		r.bailed = true
		return ExitHarnessMissing
	}
	if r.o.model == "" {
		r.o.model = r.p.defaultModel
	}

	// The round's own bin dir, holding the git shim, goes first on PATH.
	binDir := filepath.Join(r.o.configDir, "bin")
	self, err := os.Executable()
	if err != nil {
		fmt.Fprintf(r.stderr, "outsource: cannot locate this binary for the git shim: %v\n", err)
		r.bailed = true
		return ExitUsage
	}
	if _, err := gitshim.Write(binDir, self); err != nil {
		// The guard is not optional. A round that cannot be guarded is not
		// launched — the alternative is a delegate with unguarded git, which is
		// the one thing this skill promises never to hand out.
		fmt.Fprintf(r.stderr, "outsource: could not write the git guard shim: %v\n", err)
		r.bailed = true
		return ExitUsage
	}

	logPath := r.o.log
	if logPath == "" {
		logPath = os.DevNull
	}
	// Keep stderr out of the log: muse prints workspace/skills notices there,
	// and one such line ahead of the JSONL would make the whole log unparseable.
	logf, err := os.Create(logPath)
	if err != nil {
		fmt.Fprintf(r.stderr, "outsource: cannot write the log: %v\n", err)
		r.bailed = true
		return ExitUsage
	}
	var errf *os.File
	if r.o.log != "" {
		errf, _ = os.Create(r.o.log + ".err")
	}

	args := []string{"exec", "--json", "--prompt-file", r.o.spec, "--model", r.o.model}
	if r.o.effort != "" {
		// muse's own scale is none|minimal|low|medium|high|xhigh|max|ultra,
		// which is a superset of this launcher's; museEffort maps the shared
		// names and effortRefusal has already rejected anything else.
		args = append(args, "--reasoning-effort", museEffort(r.o.effort))
	}
	if r.o.session != "" {
		args = append(args, "--session-id", r.o.session)
	}
	// Headless means nobody can answer a prompt. --disable-approval turns the
	// approval prompts off; the git guard is the shim, not the approval mode,
	// so nothing here weakens what is refused. --user-input-auto-resolve stops
	// a round that asks a question from hanging until --max-seconds.
	args = append(args, "--disable-approval", "--user-input-auto-resolve")

	cmd := exec.Command("muse", args...)
	cmd.Dir = r.o.cwd
	cmd.Stdout = logf
	if errf != nil {
		cmd.Stderr = errf
	} else {
		cmd.Stderr = r.stderr
	}
	cmd.Env = gitshim.PrependPath(nestedEnv(os.Environ()), binDir)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	rc := r.runChild(cmd, logf, errf)
	r.sid = museSessionID(logPath)
	if r.timedOut {
		r.timedOutNote()
		return ExitTimedOut
	}
	if rc != 0 {
		if msg := museLogError(logPath); msg != "" {
			r.harnessError = msg
			fmt.Fprintf(r.stderr, "outsource: the muse round failed (rc=%d) and its log says: %s\n", rc, msg)
		}
	}

	assertCode := 0
	switch {
	case rc != 0:
		fmt.Fprintf(r.stderr, "outsource: run failed (rc=%d); model-identity assertion skipped\n", rc)
	case r.o.log == "":
		fmt.Fprintln(r.stderr, "outsource: no --log given; model-identity assertion skipped (nothing to verify against)")
	default:
		actual, src, verdict := assertMuseIdentity(r.sid, r.o.model, r.o.cwd)
		r.modelVerdict, r.modelSource = verdict, src
		switch verdict {
		case "ok":
			r.modelActual = actual
		case "mismatch":
			r.modelActual = actual
			fmt.Fprintf(r.stderr, "outsource: MODEL MISMATCH — requested '%s' but the run was answered by: %s (evidence: %s); failing the round (exit 70)\n",
				r.o.model, orUnknown(actual), orNone(src))
			telemetry.Note("why", "model mismatch: another model answered")
			assertCode = ExitModelIdentity
		default:
			fmt.Fprintf(r.stderr, "outsource: MODEL ASSERTION FAILED — %s; cannot verify that '%s' answered, so not claiming a pass (exit 70)\n",
				orNone(src), r.o.model)
			telemetry.Note("why", "model unverifiable: muse export")
			assertCode = ExitModelIdentity
		}
	}
	if rc != 0 {
		return rc
	}
	return assertCode
}

// museEffort maps this launcher's effort scale onto muse's. The names this
// launcher accepts (low|medium|high|xhigh|max) are all names muse accepts too,
// so the map is an identity today — it exists so the two scales can diverge
// without a call site learning about it, and so muse's extra levels (none,
// minimal, ultra) are reachable later from one place.
func museEffort(e string) string { return e }

// museSessionID is the stream id of the first session-scoped envelope. Every
// envelope in a run carries it, so the first one is enough.
func museSessionID(logPath string) string {
	b, err := os.ReadFile(logPath)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		if line == "" {
			continue
		}
		var env struct {
			Stream struct {
				Kind string `json:"kind"`
				ID   string `json:"id"`
			} `json:"stream"`
		}
		if json.Unmarshal([]byte(line), &env) != nil {
			continue
		}
		if env.Stream.Kind == "session" && env.Stream.ID != "" {
			return env.Stream.ID
		}
	}
	return ""
}

// museLogError returns the last failure the log states, or "". A failed round
// otherwise arrives as a bare rc, and a --detach round has no terminal left to
// ask — the same gap that was closed for opencode on 2026-09-17.
func museLogError(logPath string) string {
	b, err := os.ReadFile(logPath)
	if err != nil {
		return ""
	}
	out := ""
	for _, line := range strings.Split(string(b), "\n") {
		if line == "" {
			continue
		}
		var env struct {
			PayloadType string `json:"payload_type"`
			Payload     struct {
				Terminal string `json:"terminal"`
				Reason   string `json:"reason"`
				Text     string `json:"text"`
				Message  string `json:"message"`
			} `json:"payload"`
		}
		if json.Unmarshal([]byte(line), &env) != nil {
			continue
		}
		p := env.Payload
		switch {
		case strings.HasPrefix(env.PayloadType, "run.terminal.") && p.Terminal != "" && p.Terminal != "completed":
			msg := p.Terminal
			if p.Reason != "" {
				msg += ": " + p.Reason
			}
			out = msg
		case strings.Contains(env.PayloadType, "error") && p.Message != "":
			out = p.Message
		}
	}
	return out
}

// assertMuseIdentity decides which model actually answered.
//
// The live --json stream carries `run.model.configured`, and that is a REQUEST
// echo: its own payload says `source: "startup"`, so it reports what the run was
// configured with, never what replied. Treating it as proof would repeat the
// mistake claude-code's modelUsage field invites (measured 2026-08-16) and the
// one agy's stream-init event invites.
//
// The response-side evidence is in the durable session log, reachable with
// `muse export`: envelopes whose payload is `{"kind":"run", "event":{"kind":
// "model_completed", "model": "<id>"}}`, one per model completion, carrying that
// completion's own usage and duration. Measured 2026-09-18: a two-turn round
// produced two of them, both naming the requested model. Every completion must
// match, and a round with none is unverifiable rather than passed.
func assertMuseIdentity(session, requested, cwd string) (actual, source, verdict string) {
	if session == "" {
		return "", "no session id in the log", "absent"
	}
	dir, err := os.MkdirTemp("", "outsource-muse-export")
	if err != nil {
		return "", fmt.Sprintf("cannot stage the export: %v", err), "absent"
	}
	defer os.RemoveAll(dir)
	out := filepath.Join(dir, "export.json")

	cmd := exec.Command("muse", "export", "--session", session, "--out", out)
	cmd.Dir = cwd
	if err := cmd.Run(); err != nil {
		return "", fmt.Sprintf("muse export failed for session %s: %v", session, err), "absent"
	}
	b, err := os.ReadFile(out)
	if err != nil {
		return "", fmt.Sprintf("muse export wrote nothing readable: %v", err), "absent"
	}
	return parseMuseExport(b, requested)
}

// parseMuseExport is the pure half, so the verdict is testable without the CLI.
func parseMuseExport(b []byte, requested string) (actual, source, verdict string) {
	var doc struct {
		Events []struct {
			Envelope struct {
				Payload struct {
					Kind  string `json:"kind"`
					Event struct {
						Kind  string `json:"kind"`
						Model string `json:"model"`
					} `json:"event"`
				} `json:"payload"`
			} `json:"envelope"`
		} `json:"events"`
	}
	if json.Unmarshal(b, &doc) != nil {
		return "", "muse export is not parseable JSON", "absent"
	}
	var answered []string
	for _, e := range doc.Events {
		ev := e.Envelope.Payload.Event
		if ev.Kind == "model_completed" && ev.Model != "" {
			answered = append(answered, ev.Model)
		}
	}
	if len(answered) == 0 {
		return "", "no model_completed event in the muse export", "absent"
	}
	models := uniq(answered)
	actual = strings.Join(models, ",")
	source = fmt.Sprintf("muse export (%d model_completed event(s))", len(answered))
	for _, m := range models {
		if m != requested {
			return actual, source, "mismatch"
		}
	}
	return actual, source, "ok"
}

// museReport is the round's final answer, for bin/last-report.sh.
func museReport(logPath string) string {
	b, err := os.ReadFile(logPath)
	if err != nil {
		return ""
	}
	out := ""
	for _, line := range strings.Split(string(b), "\n") {
		if line == "" {
			continue
		}
		var env struct {
			PayloadType string `json:"payload_type"`
			Payload     struct {
				Text string `json:"text"`
			} `json:"payload"`
		}
		if json.Unmarshal([]byte(line), &env) != nil {
			continue
		}
		if env.PayloadType == "run.terminal.completed" && env.Payload.Text != "" {
			out = env.Payload.Text
		}
	}
	return out
}
