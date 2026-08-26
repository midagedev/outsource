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

	// An isolated CLAUDE_CONFIG_DIR keeps the user's own Claude Code untouched and
	// gives this track its own settings/session store.
	ccHome := filepath.Join(r.o.configDir, "claude")
	if err := os.MkdirAll(ccHome, 0o755); err != nil {
		fmt.Fprintf(r.stderr, "outsource: %v\n", err)
		r.bailed = true
		return ExitUsage
	}
	if err := writeHookSettings(filepath.Join(ccHome, "settings.json")); err != nil {
		fmt.Fprintf(r.stderr, "outsource: could not write the guard hook: %v\n", err)
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
	cmdArgs = append(cmdArgs, "--permission-mode", "bypassPermissions", "--output-format", "json")
	cmd := exec.Command("claude", cmdArgs...)
	cmd.Dir = r.o.cwd
	cmd.Stdin = specf
	cmd.Stdout = logf
	if errf != nil {
		cmd.Stderr = errf
	} else {
		cmd.Stderr = r.stderr
	}
	cmd.Env = nestedEnv(append(os.Environ(),
		"ANTHROPIC_BASE_URL="+base,
		"ANTHROPIC_AUTH_TOKEN="+key,
		"ANTHROPIC_MODEL="+r.o.model,
		"CLAUDE_CONFIG_DIR="+ccHome,
	))
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

// writeHookSettings attaches the git guard the way this harness wants it: the
// hook receives the tool call as JSON on stdin.
//
// It points at the BINARY rather than the git-guard.sh shim, which is the one
// place in this port where the compatibility name is deliberately bypassed. The
// guard fires on every Bash tool call of every round, and the shim costs an extra
// fork each time — measured 15ms through the shim against 10ms direct. The
// decision the guard makes is identical either way; the shim execs this same
// binary.
func writeHookSettings(path string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	settings := map[string]any{
		"hooks": map[string]any{
			"PreToolUse": []any{
				map[string]any{
					"matcher": "Bash",
					"hooks": []any{
						map[string]any{"type": "command", "command": self + " guard", "timeout": 10},
					},
				},
			},
		},
	}
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
