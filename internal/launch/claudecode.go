package launch

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/midagedev/outsource/internal/cred"
	"github.com/midagedev/outsource/internal/runs"
	"github.com/midagedev/outsource/internal/telemetry"
)

// runClaudeCode drives `claude -p` against the provider's Anthropic-compatible
// endpoint.
//
// Field-measured: ANTHROPIC_BASE_URL/AUTH_TOKEN are honoured (an invalid token
// 401s, so a green run really did go to the provider and not to the user's own
// subscription), CLAUDE_CONFIG_DIR isolates the run from the user's own Claude
// Code, and the git guard attaches as a PreToolUse hook. ANTHROPIC_MODEL must be
// set — z.ai maps an unqualified `claude-*` request onto the plan default
// (measured: glm-4.7) — so after the run the launcher asserts model identity and
// fails the round when the model that answered is not the one requested.
func (r *round) runClaudeCode() int {
	if _, err := exec.LookPath("claude"); err != nil {
		fmt.Fprintln(r.stderr, "harness claude-code needs the 'claude' CLI on PATH")
		r.bailed = true
		return ExitHarnessMissing
	}
	if r.o.model == "" {
		r.o.model = r.p.defaultModel
	}
	key, ok := cred.KeyOrExplain(r.p.name, r.stderr)
	if !ok {
		r.bailed = true
		return ExitNoCredential
	}

	// An isolated CLAUDE_CONFIG_DIR keeps the user's own Claude Code untouched.
	// It is NOT per-track by default: without --config-dir every zai round on the
	// machine lands in the same tmpDir()/outsource-glm-cfg, and its settings.json
	// is one file that concurrent rounds overwrite in turn. So the settings are
	// split by who they are true for -- the guard, identical for every round, in
	// the shared file; anything naming THIS run in a per-round file handed to the
	// CLI with --settings (which loads additional settings on top).
	ccHome := filepath.Join(r.o.configDir, "claude")
	if err := os.MkdirAll(ccHome, 0o755); err != nil {
		fmt.Fprintf(r.stderr, "outsource: %v\n", err)
		r.bailed = true
		return ExitUsage
	}
	if err := writeSharedSettings(filepath.Join(ccHome, "settings.json")); err != nil {
		fmt.Fprintf(r.stderr, "outsource: could not write the guard hook: %v\n", err)
		r.bailed = true
		return ExitUsage
	}
	roundSettings := filepath.Join(ccHome, "settings-"+roundKey(r.runID)+".json")
	if err := writeHookSettings(roundSettings, r.runID, runs.Dir()); err != nil {
		fmt.Fprintf(r.stderr, "outsource: could not write this round's settings: %v\n", err)
		r.bailed = true
		return ExitUsage
	}

	base := cred.Base(r.p.name, r.p.url)
	if r.p.name == "zai" {
		if v := os.Getenv("ZAI_ANTHROPIC_BASE"); v != "" {
			base = v // back-compat env override, zai only
		}
	}

	logPath := r.o.log
	if logPath == "" {
		logPath = os.DevNull
	}
	// Keep stderr out of the log: this harness prints diagnostics there (e.g.
	// `[claude-code:unrecognized_model]` for a non-Anthropic model id), and one
	// such line ahead of the JSON makes the whole log unparseable.
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
	specf, err := os.Open(r.o.spec)
	if err != nil {
		logf.Close()
		fmt.Fprintf(r.stderr, "outsource: cannot read the spec: %v\n", err)
		r.bailed = true
		return ExitUsage
	}
	defer specf.Close()

	cmdArgs := []string{"-p"}
	if r.o.session != "" {
		cmdArgs = append(cmdArgs, "--resume", r.o.session)
	}
	cmdArgs = append(cmdArgs, "--permission-mode", "bypassPermissions", "--output-format", "json",
		"--settings", roundSettings)
	if r.o.effort != "" {
		// Validated by effortRefusal before the run was registered.
		cmdArgs = append(cmdArgs, "--effort", r.o.effort)
	}
	cmd := exec.Command("claude", cmdArgs...)
	cmd.Dir = r.o.cwd
	cmd.Stdin = specf
	cmd.Stdout = logf
	if errf != nil {
		cmd.Stderr = errf
	} else {
		cmd.Stderr = r.stderr
	}
	env := append(os.Environ(),
		"ANTHROPIC_BASE_URL="+base,
		"ANTHROPIC_AUTH_TOKEN="+key,
		"ANTHROPIC_MODEL="+r.o.model,
		"CLAUDE_CONFIG_DIR="+ccHome,
	)
	// The CLI caps one assistant turn at 32k output tokens by default, and GLM
	// likes to write a whole file plus its tests in one turn: two implementation
	// rounds died on "response exceeded the 32000 output token maximum" with zero
	// files on disk (2026-09-05, even after the spec asked for split writes).
	// Raise the cap unless the caller pinned one.
	if os.Getenv("CLAUDE_CODE_MAX_OUTPUT_TOKENS") == "" {
		env = append(env, "CLAUDE_CODE_MAX_OUTPUT_TOKENS=64000")
	}
	// The same shape of defect on the input side, and a harder failure. The CLI
	// applies an unknown-model context ceiling and ENFORCES it: a round whose
	// prompt crossed it died with "Prompt is too long" before a request was
	// made. The provider table owns the real number; zero means unmeasured, and
	// then nothing is set and the CLI keeps its own behaviour.
	if r.p.contextWindow > 0 && os.Getenv("CLAUDE_CODE_MAX_CONTEXT_TOKENS") == "" {
		env = append(env, fmt.Sprintf("CLAUDE_CODE_MAX_CONTEXT_TOKENS=%d", r.p.contextWindow))
	}
	cmd.Env = nestedEnv(env)
	// Its own process group, so the watchdog can signal the whole tree.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	rc := r.runChild(cmd, logf, errf)
	if r.timedOut {
		// A killed round has a truncated log, so the model-identity assertion would
		// fail on it and report a mismatch that never happened. The ceiling is the
		// finding here; say so and stop.
		r.timedOutNote()
		return ExitTimedOut
	}

	a := analyzeRun(logPath, r.o.model, ccHome)
	r.sid = a.session
	r.modelActual = a.actual
	// analyzeRun had to locate the transcript anyway to assert identity, so the
	// sentinel gets the path for free — and a round whose SessionStart hook
	// never fired still ends up with a recorded trail.
	if p, ok := strings.CutPrefix(a.source, "transcript "); ok {
		r.trail = p
	}

	// Cost honesty. The token counts in `usage` are this round's and are the only
	// per-round figure worth quoting; total_cost_usd is Claude Code's
	// Anthropic-priced estimate, not what the provider charges. Plan credits are
	// deliberately absent — they are plan-wide, not per-round.
	if a.verdict != "nolog" && a.verdict != "unreadable" {
		fmt.Fprintf(r.stderr, "outsource: usage %s; total_cost_usd=%s is Claude Code's Anthropic-priced estimate, not what provider '%s' charges\n",
			orAbsent(a.usage), orAbsent(a.cost), r.p.name)
	}

	// A round that silently ran the wrong model is a failed round — exit 70 even
	// when the run itself succeeded.
	assertCode := 0
	switch {
	case rc != 0:
		fmt.Fprintf(r.stderr, "outsource: run failed (rc=%d); model-identity assertion skipped\n", rc)
	case r.o.log == "":
		fmt.Fprintln(r.stderr, "outsource: no --log given; model-identity assertion skipped (nothing to verify against)")
	default:
		switch a.verdict {
		case "ok":
		case "mismatch":
			fmt.Fprintf(r.stderr, "outsource: MODEL MISMATCH — requested '%s' but the run was answered by: %s (evidence: %s); failing the round (exit 70)\n",
				r.o.model, orUnknown(a.actual), orNone(a.source))
			telemetry.Note("why", "model mismatch: another model answered")
			assertCode = ExitModelIdentity
		case "unverifiable":
			fmt.Fprintf(r.stderr, "outsource: MODEL ASSERTION FAILED — no session transcript for session '%s', and modelUsage only echoes the requested id, so it cannot prove '%s' actually answered; not claiming a pass (exit 70)\n",
				orUnknown(a.session), r.o.model)
			telemetry.Note("why", "model unverifiable: no session transcript")
			assertCode = ExitModelIdentity
		default:
			fmt.Fprintf(r.stderr, "outsource: MODEL ASSERTION FAILED — no model-identity evidence in %s (modelUsage absent/unparseable and no session transcript); cannot verify that '%s' answered, so not claiming a pass (exit 70)\n",
				r.o.log, r.o.model)
			assertCode = ExitModelIdentity
		}
		r.modelActual = a.actual
	}
	if rc != 0 {
		return rc
	}
	return assertCode
}

// writeHookSettings holds the half that is true of THIS round only, and so must
// never go in the shared settings.json: the trail recorder, which carries the run
// id. Measured 2026-09-19: five rounds launched in the same second each wrote its
// own id into the one shared file, the last writer won, and every round's
// SessionStart hook then recorded into that one run's record — one .run file with
// 11 `trail=` lines while four rounds read `trail=pending` forever. The file is
// handed to the CLI with --settings, which loads additional settings on top of
// the config dir's.
//
// writeSharedSettings holds the half of the hook configuration that is the same
// for every round: the git guard. It is written to the config dir's settings.json,
// which concurrent rounds share by default -- and sharing is harmless precisely
// because nothing here names a particular round.
//
// PreToolUse is the git guard. It points at the BINARY rather than the
// git-guard.sh shim, which is the one place in this port where the
// compatibility name is deliberately bypassed. The guard fires on every Bash
// tool call of every round, and the shim costs an extra fork each time --
// measured 15ms through the shim against 10ms direct. The decision the guard
// makes is identical either way; the shim execs this same binary.
func writeSharedSettings(path string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	settings := map[string]any{"hooks": map[string]any{
		"PreToolUse": []any{
			map[string]any{
				"matcher": "Bash",
				"hooks": []any{
					map[string]any{"type": "command", "command": self + " guard", "timeout": 10},
				},
			},
		},
	}}
	b, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// roundKey names this round's own settings file. The run id already is
// <startedAt>-<pid>; without one (the registry refused the round) the pid alone
// still separates it from every other live round.
func roundKey(runID string) string {
	if runID != "" {
		return runID
	}
	return fmt.Sprint(os.Getpid())
}

// SessionStart is how the round tells the registry where its live trail is.
// Measured 2026-09-15 (CLI 2.1.272): the event fires under `claude -p` and its
// payload carries transcript_path and session_id. Before it, the only way to
// find a running round's transcript was to guess the newest .jsonl under
// projects/<cwd-slug>/ — which is not the round's own file as soon as two
// rounds share a cwd, and cost a session ten tool calls to work around. The
// registry directory is passed EXPLICITLY: the hook runs inside the harness,
// which makes no promise about passing OUTSOURCE_RUNS_DIR through, and a
// recorder writing to the default directory while the launcher used another
// one would silently record nothing. The recorder is silent by contract (its
// stdout would land in the model's first turn) and its timeout is short
// because it sits in front of turn one.
func writeHookSettings(path, runID, runsDir string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	hooks := map[string]any{}
	// No run id means the registry never took this round (it returns an empty
	// id on failure), so there is nothing to record the trail into.
	if runID != "" {
		hooks["SessionStart"] = []any{
			map[string]any{
				"hooks": []any{
					map[string]any{
						"type": "command",
						"command": fmt.Sprintf("%s tail --record-from-hook %s --runs-dir %s",
							shellQuote(self), shellQuote(runID), shellQuote(runsDir)),
						"timeout": 5,
					},
				},
			},
		}
	}
	settings := map[string]any{"hooks": hooks}
	b, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// analysis is one read-only pass over the JSON log and the session transcript:
// session id, real token counts, Anthropic-priced cost, and the model-identity
// verdict. Never writes back.
type analysis struct {
	session, usage, cost, actual, verdict, source string
}

// analyzeRun decides model identity.
//
// Measured 2026-08-16: `modelUsage` in the log echoes the REQUESTED id — a run
// that requested claude-opus-5 and was answered by glm-4.7 still logged
// modelUsage {"claude-opus-5": …}. The model that actually answered is the
// per-turn `message.model` in the session transcript (isolated config dir,
// located by session id), so that is the primary evidence; modelUsage keys are
// only a labelled fallback when no transcript can be found.
func analyzeRun(logPath, requested, ccHome string) analysis {
	a := analysis{verdict: "nolog"}
	b, err := os.ReadFile(logPath)
	if err != nil {
		return a
	}
	var d map[string]any
	if json.Unmarshal(b, &d) != nil {
		return analysis{verdict: "unreadable"}
	}
	if s, ok := d["session_id"].(string); ok {
		a.session = s
	}
	if u, ok := d["usage"].(map[string]any); ok {
		keys := make([]string, 0, len(u))
		for k := range u {
			keys = append(keys, k)
		}
		sort.Strings(keys) // sorted, so the line is stable across runs
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, fmt.Sprintf("%s=%s", k, plainNum(u[k])))
		}
		a.usage = strings.Join(parts, " ")
	}
	if c, ok := d["total_cost_usd"]; ok && c != nil {
		a.cost = plainNum(c)
	}

	var answered []string
	fromTranscript := false
	if a.session != "" {
		if path := findTranscript(filepath.Join(ccHome, "projects"), a.session+".jsonl"); path != "" {
			for _, line := range strings.Split(string(mustRead(path)), "\n") {
				var o map[string]any
				if line == "" || json.Unmarshal([]byte(line), &o) != nil {
					continue
				}
				if t, _ := o["type"].(string); t != "assistant" {
					continue
				}
				if m, ok := o["message"].(map[string]any); ok {
					if s, ok := m["model"].(string); ok && s != "" {
						answered = append(answered, s)
					}
				}
			}
			a.source = "transcript " + path
			fromTranscript = len(answered) > 0
		}
	}
	if len(answered) == 0 {
		if mu, ok := d["modelUsage"].(map[string]any); ok && len(mu) > 0 {
			for k := range mu {
				answered = append(answered, k)
			}
			sort.Strings(answered)
			a.source = "modelUsage (request-side; no transcript found)"
		}
	}
	if len(answered) == 0 {
		a.verdict = "absent"
		return a
	}
	models := uniq(answered)
	a.actual = strings.Join(models, ",")
	// Every answering model must be the requested id or an annotated variant of it
	// (e.g. claude-opus-5[1m]) — a partial match is still a remap.
	matched := true
	for _, m := range models {
		if m != requested && !strings.HasPrefix(m, requested) {
			matched = false
			break
		}
	}
	switch {
	case matched && !fromTranscript:
		// modelUsage echoes the REQUESTED id, so a match there proves nothing — it
		// is exactly what a silently remapped run also produces. Only the transcript
		// can clear the round. (A MISMATCH there is still real evidence.)
		a.verdict = "unverifiable"
	case matched:
		a.verdict = "ok"
	default:
		a.verdict = "mismatch"
	}
	return a
}

func findTranscript(root, name string) string {
	found := ""
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || found != "" {
			return nil
		}
		if d.Name() == name {
			found = p
		}
		return nil
	})
	return found
}

func mustRead(p string) []byte { b, _ := os.ReadFile(p); return b }

func uniq(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// plainNum renders a decoded JSON value for the human-readable cost line: a
// number without a float tail, so 1 prints as 1 and not 1.0.
//
// Composites are re-encoded as compact JSON rather than printed with %v. The
// harness's `usage` object nests — cache_creation and server_tool_use are
// sub-objects — and %v spells those in Go's own map syntax
// (`map[ephemeral_5m_input_tokens:0]`), which leaks the implementation language
// into a line a person reads. Caught on a live round, not by the gate: the fake
// harness emitted only flat integers, so the gate had nothing nested to compare.
// It does now.
func plainNum(v any) string {
	switch t := v.(type) {
	case float64:
		if t == float64(int64(t)) {
			return fmt.Sprintf("%d", int64(t))
		}
		return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%f", t), "0"), ".")
	case string:
		return t
	case nil:
		return ""
	case map[string]any, []any:
		if b, err := json.Marshal(t); err == nil {
			return string(b)
		}
	}
	return fmt.Sprintf("%v", v)
}

func orAbsent(s string) string {
	if s == "" {
		return "absent"
	}
	return s
}
func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}
func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

// shellQuote makes one argument safe inside a hook command string. The harness
// runs a hook through a shell, and the paths here come from --config-dir and
// the registry directory — both caller-supplied, both allowed to contain
// spaces. Single quotes with the '"'"' escape is the portable form.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}
