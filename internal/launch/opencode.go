package launch

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/midagedev/outsource/internal/telemetry"
)

// runOpencode drives `opencode run --format json` against OpenRouter.
//
// Field-measured 2026-08-23 (opencode 1.18.21, stealth/ox-alpha):
//   - stdin is the spec; `-m openrouter/stealth/ox-alpha` is honoured
//   - `--format json` writes one JSONL event per line and flushes each
//     while the process is still running (the --log file is the live trail)
//   - an unattended tool round completes without `--auto` for in-cwd writes,
//     but a `read` of a path outside cwd is `external_directory` (default
//     ask) and is rejected headless; `--auto` auto-approves ask without
//     lifting deny (git commit still blocked)
//   - `--pure` skips external plugins (the user's OPENCODE_CONFIG_DIR
//     otherwise points at orca hooks)
//   - `opencode export <sessionID>` is the model-identity ground truth
func (r *round) runOpencode() int {
	if _, err := exec.LookPath("opencode"); err != nil {
		fmt.Fprintln(r.stderr, "harness opencode needs the 'opencode' CLI on PATH")
		r.bailed = true
		return ExitHarnessMissing
	}
	model, errMsg := qualifyOpencodeModel(r.o.model, r.p.defaultModel)
	if errMsg != "" {
		fmt.Fprintln(r.stderr, errMsg)
		r.bailed = true
		return ExitUsage
	}
	r.o.model = model

	ocHome := filepath.Join(r.o.configDir, "opencode")
	if err := os.MkdirAll(ocHome, 0o755); err != nil {
		fmt.Fprintf(r.stderr, "outsource: %v\n", err)
		r.bailed = true
		return ExitUsage
	}
	if err := writeOpencodeConfig(filepath.Join(ocHome, "opencode.json")); err != nil {
		fmt.Fprintf(r.stderr, "outsource: could not write the opencode config: %v\n", err)
		r.bailed = true
		return ExitUsage
	}

	logPath := r.o.log
	if logPath == "" {
		logPath = os.DevNull
	}
	// Keep stderr out of the log: opencode prints a FORCE_COLOR/NO_COLOR
	// warning there, and one such line ahead of the JSON makes the whole
	// log unparseable.
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

	args := []string{"run", "--format", "json", "--auto", "--pure", "-m", r.o.model}
	if r.o.session != "" {
		args = append(args, "-s", r.o.session)
	}
	cmd := exec.Command("opencode", args...)
	cmd.Dir = r.o.cwd
	cmd.Stdin = specf
	cmd.Stdout = logf
	if errf != nil {
		cmd.Stderr = errf
	} else {
		cmd.Stderr = r.stderr
	}
	cmd.Env = opencodeEnv(ocHome, r.o.cwd)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	rc := r.runChild(cmd, logf, errf)
	r.sid = firstSessionID(logPath)
	if r.timedOut {
		r.timedOutNote()
		return ExitTimedOut
	}

	assertCode := 0
	switch {
	case rc != 0:
		fmt.Fprintf(r.stderr, "outsource: run failed (rc=%d); model-identity assertion skipped\n", rc)
	case r.o.log == "":
		fmt.Fprintln(r.stderr, "outsource: no --log given; model-identity assertion skipped (nothing to verify against)")
	case r.sid == "":
		fmt.Fprintf(r.stderr, "outsource: MODEL ASSERTION FAILED — no sessionID in %s, cannot verify that '%s' answered (exit 70)\n",
			r.o.log, r.o.model)
		telemetry.Note("why", "model unverifiable: no session id")
		assertCode = ExitModelIdentity
	default:
		actual, src, verdict := assertOpencodeIdentity(r.sid, r.o.model, r.o.cwd, cmd.Env)
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
		case "wrongdir":
			fmt.Fprintf(r.stderr, "outsource: DIRECTORY MISMATCH — the round ran in '%s' but --cwd was '%s' (evidence: %s); its writes landed outside the requested tree, failing the round (exit 70)\n",
				orUnknown(actual), r.o.cwd, orNone(src))
			telemetry.Note("why", "directory mismatch: round ran outside --cwd")
			assertCode = ExitModelIdentity
		default:
			fmt.Fprintf(r.stderr, "outsource: MODEL ASSERTION FAILED — %s; cannot verify that '%s' answered, so not claiming a pass (exit 70)\n",
				orNone(src), r.o.model)
			telemetry.Note("why", "model unverifiable: opencode export")
			assertCode = ExitModelIdentity
		}
	}
	if rc != 0 {
		return rc
	}
	return assertCode
}

// qualifyOpencodeModel returns provider/id. The model id itself may contain
// slashes (stealth/ox-alpha); a naive one-slash split is wrong. The prefix
// must be openrouter/ and the remainder non-empty.
func qualifyOpencodeModel(model, defaultID string) (qualified string, errMsg string) {
	if model == "" {
		model = "openrouter/" + defaultID
	}
	rest, ok := strings.CutPrefix(model, "openrouter/")
	if !ok || rest == "" {
		return "", fmt.Sprintf("--model must be openrouter/<id> for the opencode harness, got: %s", model)
	}
	return model, ""
}

func requestedModelID(qualified string) string {
	rest, ok := strings.CutPrefix(qualified, "openrouter/")
	if !ok {
		return qualified
	}
	return rest
}

// opencodeDenyGit mirrors internal/guard's denyGit list. The guard itself is
// not attached here — opencode has no PreToolUse hook in this launcher —
// so the permission block is the mechanical ban. Listing forms that the
// guard explicitly allows are re-allowed *after* the matching deny so
// last-match-wins restores them. Do not import guard: this round must not
// edit that package.
var opencodeDenyGit = []string{
	"commit", "push", "checkout", "switch", "stash", "restore", "add",
	"rm", "mv", "reset", "rebase", "merge", "cherry-pick", "revert",
	"tag", "branch", "worktree", "clean", "filter-branch", "update-ref",
	"apply", "am", "fetch", "pull", "clone", "remote", "submodule",
	"config", "gc", "prune", "reflog", "notes", "replace",
	"sparse-checkout", "bisect",
}

// Listing forms, emitted after the denies so they win. Same set the guard
// erases before its deny pass (worktree list, branch -a/--list, remote -v,
// config --get/--list).
var opencodeAllowGit = []string{
	"git worktree list",
	"git worktree list *",
	"git branch -a",
	"git branch -a *",
	"git branch -l",
	"git branch -l *",
	"git branch -v",
	"git branch -v *",
	"git branch -r",
	"git branch -r *",
	"git branch --list",
	"git branch --list *",
	"git remote -v",
	"git remote --verbose",
	"git remote show",
	"git remote show *",
	"git config --get",
	"git config --get *",
	"git config --get-all",
	"git config --get-all *",
	"git config --list",
	"git config --list *",
	"git config -l",
	"git config -l *",
}

var opencodeDenyGH = []string{
	"gh pr create", "gh pr create *",
	"gh pr merge", "gh pr merge *",
	"gh pr close", "gh pr close *",
	"gh pr edit", "gh pr edit *",
	"gh pr ready", "gh pr ready *",
	"gh pr review", "gh pr review *",
	"gh repo create", "gh repo create *",
	"gh repo delete", "gh repo delete *",
	"gh repo edit", "gh repo edit *",
	"gh repo fork", "gh repo fork *",
	"gh repo sync", "gh repo sync *",
	"gh release", "gh release *",
	"gh workflow run", "gh workflow run *",
}

func writeOpencodeConfig(path string) error {
	// Built as a string so bash-rule key order is stable: last match wins,
	// so the listing allows must come after the matching denies. encoding/json
	// on a map would scramble that.
	var bash strings.Builder
	bash.WriteString(`{"*":"allow"`)
	for _, sub := range opencodeDenyGit {
		fmt.Fprintf(&bash, `,"git %s":"deny","git %s *":"deny","git -C * %s":"deny","git -C * %s *":"deny"`,
			sub, sub, sub, sub)
	}
	for _, a := range opencodeAllowGit {
		fmt.Fprintf(&bash, `,%q:"allow"`, a)
	}
	for _, g := range opencodeDenyGH {
		fmt.Fprintf(&bash, `,%q:"deny"`, g)
	}
	bash.WriteByte('}')
	cfg := fmt.Sprintf(`{
  "$schema": "https://opencode.ai/config.json",
  "autoupdate": false,
  "permission": {
    "bash": %s
  }
}
`, bash.String())
	return os.WriteFile(path, []byte(cfg), 0o644)
}

// opencodeEnv is the round's environment. PWD is not cosmetic here: opencode
// resolves the session's directory from $PWD, not from the process working
// directory (measured 2026-08-23: with process cwd=A and PWD=B the session
// recorded B, and the round's writes landed in B — the launcher-shell's own
// PWD leaked through os.Environ() and quietly overrode --cwd). cmd.Dir alone
// therefore does not confine the round; PWD must be replaced with --cwd.
func opencodeEnv(ocHome, cwd string) []string {
	return nestedEnv(append(environWithout("OPENCODE_CONFIG_DIR", "OPENCODE_CONFIG", "OPENCODE_CONFIG_CONTENT", "OPENCODE_PERMISSION", "PWD", "OLDPWD"),
		"OPENCODE_CONFIG_DIR="+ocHome,
		"OPENCODE_DISABLE_AUTOUPDATE=1",
		"PWD="+cwd,
	))
}

// environWithout copies os.Environ() with the named keys removed. Appending
// OPENCODE_CONFIG_DIR=… onto a slice that already contains one is
// runtime-dependent (first vs last wins); filtering first makes the override
// the only entry.
func environWithout(keys ...string) []string {
	drop := make(map[string]bool, len(keys))
	for _, k := range keys {
		drop[k] = true
	}
	env := os.Environ()
	out := make([]string, 0, len(env))
	for _, e := range env {
		k, _, _ := strings.Cut(e, "=")
		if drop[k] {
			continue
		}
		out = append(out, e)
	}
	return out
}

func openrouterAuthPath() string {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "opencode", "auth.json")
}

// openrouterCredsPositivelyAbsent is true only when auth.json exists, parses,
// and has no usable openrouter key. A missing file is not proof (credentials
// may live in opencode.db); an unreadable or unparseable file fails open.
func openrouterCredsPositivelyAbsent() bool {
	path := openrouterAuthPath()
	if path == "" {
		return false
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var d map[string]json.RawMessage
	if json.Unmarshal(b, &d) != nil {
		return false
	}
	raw, ok := d["openrouter"]
	if !ok {
		return true
	}
	var e struct {
		Key string `json:"key"`
	}
	if json.Unmarshal(raw, &e) != nil {
		return false
	}
	return strings.TrimSpace(e.Key) == ""
}

func firstSessionID(logPath string) string {
	f, err := os.Open(logPath)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 256*1024), 32*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var o struct {
			SessionID string `json:"sessionID"`
		}
		if json.Unmarshal([]byte(line), &o) == nil && o.SessionID != "" {
			return o.SessionID
		}
	}
	return ""
}

type opencodeExport struct {
	Info struct {
		Directory string `json:"directory"`
		Model     struct {
			ID         string `json:"id"`
			ProviderID string `json:"providerID"`
		} `json:"model"`
	} `json:"info"`
	Messages []struct {
		Info struct {
			Role       string `json:"role"`
			ModelID    string `json:"modelID"`
			ProviderID string `json:"providerID"`
		} `json:"info"`
	} `json:"messages"`
}

// parseOpencodeExport is the identity check, split out so tests can feed it
// a captured export without spawning the CLI. wantDir is the round's --cwd:
// a session whose recorded directory is somewhere else did its work in the
// wrong tree (the PWD leak above was exactly that, and the artifact landed
// outside --cwd with every other signal green), so it fails the same
// assertion. Empty wantDir skips the directory check.
func parseOpencodeExport(raw []byte, requested, wantDir string) (actual, source, verdict string) {
	start := bytesIndexBrace(raw)
	if start < 0 {
		return "", "unparseable export", "absent"
	}
	var doc opencodeExport
	if json.Unmarshal(raw[start:], &doc) != nil {
		return "", "unparseable export", "absent"
	}
	if wantDir != "" && doc.Info.Directory != "" && !samePath(doc.Info.Directory, wantDir) {
		return doc.Info.Directory, "opencode export session directory", "wrongdir"
	}
	wantID := requestedModelID(requested)
	var answered []string
	for _, m := range doc.Messages {
		if m.Info.Role != "assistant" {
			continue
		}
		if m.Info.ProviderID != "openrouter" || m.Info.ModelID != wantID {
			got := m.Info.ProviderID + "/" + m.Info.ModelID
			return got, "opencode export assistant message", "mismatch"
		}
		answered = append(answered, m.Info.ModelID)
	}
	if len(answered) == 0 {
		return "", "no assistant message in opencode export", "absent"
	}
	return answered[len(answered)-1], "opencode export", "ok"
}

func bytesIndexBrace(b []byte) int {
	for i, c := range b {
		if c == '{' {
			return i
		}
	}
	return -1
}

func assertOpencodeIdentity(sid, requested, wantDir string, env []string) (actual, source, verdict string) {
	cmd := exec.Command("opencode", "export", sid)
	cmd.Env = env
	cmd.Stdin = nil
	out, err := cmd.Output()
	if err != nil {
		return "", "opencode export failed", "absent"
	}
	return parseOpencodeExport(out, requested, wantDir)
}

// samePath compares two directories after resolving symlinks (macOS aliases
// /tmp to /private/tmp, so a raw string compare would call the same place two
// places). A path that fails to resolve is compared as cleaned text.
func samePath(a, b string) bool {
	norm := func(p string) string {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return r
		}
		return filepath.Clean(p)
	}
	return norm(a) == norm(b)
}
