package launch

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestQualifyOpencodeModel(t *testing.T) {
	// FAIL-first would have been a one-slash split: "openrouter/stealth/ox-alpha"
	// → prefix "openrouter", id "stealth" and a leftover "ox-alpha".
	got, errMsg := qualifyOpencodeModel("", "stealth/ox-alpha")
	if errMsg != "" || got != "openrouter/stealth/ox-alpha" {
		t.Fatalf("default: got %q err %q, want openrouter/stealth/ox-alpha", got, errMsg)
	}
	got, errMsg = qualifyOpencodeModel("openrouter/stealth/ox-alpha", "stealth/ox-alpha")
	if errMsg != "" || got != "openrouter/stealth/ox-alpha" {
		t.Fatalf("slash-in-id: got %q err %q", got, errMsg)
	}
	if _, errMsg = qualifyOpencodeModel("openrouter/", "stealth/ox-alpha"); errMsg == "" {
		t.Fatal("empty remainder must be rejected")
	}
	if _, errMsg = qualifyOpencodeModel("zai/glm-5.3", "stealth/ox-alpha"); errMsg == "" {
		t.Fatal("non-openrouter prefix must be rejected")
	}
	if _, errMsg = qualifyOpencodeModel("stealth/ox-alpha", "stealth/ox-alpha"); errMsg == "" {
		t.Fatal("bare id without openrouter/ must be rejected")
	}
}

func TestSeedModelScopesGLMEnvToZai(t *testing.T) {
	t.Setenv("GLM_DELEGATE_MODEL", "glm-5.3")
	if got := seedModel("openrouter", ""); got != "" {
		t.Fatalf("openrouter must ignore GLM_DELEGATE_MODEL, got %q", got)
	}
	if got := seedModel("zai", ""); got != "glm-5.3" {
		t.Fatalf("zai still reads GLM_DELEGATE_MODEL, got %q", got)
	}
	if got := seedModel("zai", "glm-5.2"); got != "glm-5.2" {
		t.Fatalf("--model must win over the env, got %q", got)
	}
}

func TestDefaultHarnessOpenrouter(t *testing.T) {
	if got := defaultHarness("openrouter", ""); got != "opencode" {
		t.Fatalf("openrouter default harness = %q, want opencode", got)
	}
	if got := defaultHarness("zai", ""); got != "claude-code" {
		t.Fatalf("zai default harness = %q, want claude-code", got)
	}
	if got := defaultHarness("openrouter", "crush"); got != "crush" {
		t.Fatalf("explicit harness must win, got %q", got)
	}
}

func TestPairingAllowedCells(t *testing.T) {
	// The five cells that must keep working: GLM/xAI on the two existing
	// harnesses, and openrouter on opencode.
	for _, c := range []struct{ harness, provider string }{
		{"claude-code", "zai"},
		{"crush", "zai"},
		{"claude-code", "xai"},
		{"crush", "xai"},
		{"opencode", "openrouter"},
	} {
		if msg := pairingRefusal(c.harness, c.provider); msg != "" {
			t.Errorf("%s+%s refused: %s", c.provider, c.harness, msg)
		}
	}
}

func TestPairingMatrixRefusals(t *testing.T) {
	dir := t.TempDir()
	spec := filepath.Join(dir, "spec.md")
	if err := os.WriteFile(spec, []byte("do a thing\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OUTSOURCE_RUNS_DIR", filepath.Join(dir, "runs"))
	t.Setenv("OUTSOURCE_HARNESS", "")
	t.Setenv("OUTSOURCE_PROVIDER", "")
	t.Setenv("GLM_DELEGATE_MODEL", "glm-5.3")

	cases := []struct {
		provider, harness, want string
	}{
		{"zai", "opencode", "opencode harness requires provider openrouter"},
		{"xai", "opencode", "opencode harness requires provider openrouter"},
		{"openrouter", "claude-code", "provider openrouter is not wired on the claude-code harness"},
		{"openrouter", "crush", "provider openrouter is not wired on the crush harness"},
	}
	for _, c := range cases {
		var stderr bytes.Buffer
		args := []string{
			"--cwd", dir, "--spec", spec, "--log", filepath.Join(dir, "x.log"),
			"--provider", c.provider, "--harness", c.harness, "--label", "pair-test",
		}
		rc := OutsourceMain(args, &bytes.Buffer{}, &stderr)
		if rc != ExitUsage {
			t.Errorf("%s+%s: rc=%d, want %d; stderr=%s", c.provider, c.harness, rc, ExitUsage, stderr.String())
		}
		if !strings.Contains(stderr.String(), c.want) {
			t.Errorf("%s+%s: stderr %q, want substring %q", c.provider, c.harness, stderr.String(), c.want)
		}
		ents, _ := os.ReadDir(filepath.Join(dir, "runs"))
		if len(ents) != 0 {
			t.Errorf("%s+%s: a refused pair must not register a round, found %d", c.provider, c.harness, len(ents))
		}
	}
}

func TestOpenrouterDefaultHarnessIsOpencodeNotPairingRefuse(t *testing.T) {
	// FAIL-first (verbatim, before provider-aware default):
	//   stderr contained "provider openrouter is not wired on the claude-code harness"
	// --done-marker that is not in the spec refuses after pairing. If the
	// default harness were still claude-code, pairing would fire instead.
	dir := t.TempDir()
	spec := filepath.Join(dir, "spec.md")
	if err := os.WriteFile(spec, []byte("do a thing\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OUTSOURCE_RUNS_DIR", filepath.Join(dir, "runs"))
	t.Setenv("OUTSOURCE_HARNESS", "")
	t.Setenv("GLM_DELEGATE_MODEL", "glm-5.3")
	var stderr bytes.Buffer
	rc := OutsourceMain([]string{
		"--cwd", dir, "--spec", spec, "--log", filepath.Join(dir, "x.log"),
		"--provider", "openrouter", "--done-marker", "NOT-IN-SPEC",
	}, &bytes.Buffer{}, &stderr)
	if rc != ExitUsage {
		t.Fatalf("rc=%d, want %d; stderr=%s", rc, ExitUsage, stderr.String())
	}
	if strings.Contains(stderr.String(), "not wired") {
		t.Fatalf("defaulted to a refused pair: %s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "--done-marker") {
		t.Fatalf("want done-marker preflight after pairing, got %s", stderr.String())
	}
}

func TestEnvironWithoutDropsInheritedConfigDir(t *testing.T) {
	t.Setenv("OPENCODE_CONFIG_DIR", "/orca/hooks")
	t.Setenv("OPENCODE_PERMISSION", `{"bash":"allow"}`)
	env := environWithout("OPENCODE_CONFIG_DIR", "OPENCODE_PERMISSION")
	for _, e := range env {
		k, _, _ := strings.Cut(e, "=")
		if k == "OPENCODE_CONFIG_DIR" || k == "OPENCODE_PERMISSION" {
			t.Fatalf("inherited %s leaked: %s", k, e)
		}
	}
}

func TestOpenrouterCredsPositivelyAbsent(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dir)
	if openrouterCredsPositivelyAbsent() {
		t.Fatal("missing auth.json must not be treated as proof of absence")
	}
	oc := filepath.Join(dir, "opencode")
	if err := os.MkdirAll(oc, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oc, "auth.json"), []byte(`{"openai":{"type":"api","key":"sk"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if !openrouterCredsPositivelyAbsent() {
		t.Fatal("parseable auth.json without openrouter is positive absence")
	}
	if err := os.WriteFile(filepath.Join(oc, "auth.json"), []byte(`{"openrouter":{"type":"api","key":"sk-or-x"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if openrouterCredsPositivelyAbsent() {
		t.Fatal("a present openrouter key is not absence")
	}
	if err := os.WriteFile(filepath.Join(oc, "auth.json"), []byte(`{"openrouter":{"type":"api","key":"  "}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if !openrouterCredsPositivelyAbsent() {
		t.Fatal("whitespace-only key is positive absence")
	}
}

func TestParseOpencodeExportIdentity(t *testing.T) {
	ok := []byte(`{"info":{"model":{"id":"stealth/ox-alpha","providerID":"openrouter"}},"messages":[{"info":{"role":"user"}},{"info":{"role":"assistant","modelID":"stealth/ox-alpha","providerID":"openrouter"}}]}`)
	actual, _, verdict := parseOpencodeExport(ok, "openrouter/stealth/ox-alpha", "")
	if verdict != "ok" || actual != "stealth/ox-alpha" {
		t.Fatalf("ok case: actual=%q verdict=%q", actual, verdict)
	}
	mismatch := []byte(`{"messages":[{"info":{"role":"assistant","modelID":"glm-5.3","providerID":"openrouter"}}]}`)
	_, _, verdict = parseOpencodeExport(mismatch, "openrouter/stealth/ox-alpha", "")
	if verdict != "mismatch" {
		t.Fatalf("mismatch case: verdict=%q", verdict)
	}
	none := []byte(`{"messages":[{"info":{"role":"user"}}]}`)
	_, _, verdict = parseOpencodeExport(none, "openrouter/stealth/ox-alpha", "")
	if verdict != "absent" {
		t.Fatalf("no assistant: verdict=%q", verdict)
	}
	_, _, verdict = parseOpencodeExport([]byte("not json"), "openrouter/stealth/ox-alpha", "")
	if verdict != "absent" {
		t.Fatalf("garbage: verdict=%q", verdict)
	}
}

func TestWriteOpencodeConfigDeniesGitCommitAllowsWorktreeList(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "opencode.json")
	if err := writeOpencodeConfig(path); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.Contains(s, `"git commit *":"deny"`) {
		t.Fatalf("missing git commit deny:\n%s", s)
	}
	if !strings.Contains(s, `"git worktree list":"allow"`) {
		t.Fatalf("missing worktree list allow:\n%s", s)
	}
	// last-match-wins: the allow must appear after the worktree deny
	denyAt := strings.Index(s, `"git worktree *":"deny"`)
	allowAt := strings.Index(s, `"git worktree list":"allow"`)
	if denyAt < 0 || allowAt < 0 || allowAt < denyAt {
		t.Fatalf("worktree list allow must follow worktree deny (deny=%d allow=%d)", denyAt, allowAt)
	}
	var parsed map[string]any
	if err := json.Unmarshal(b, &parsed); err != nil {
		t.Fatalf("generated opencode.json is not JSON: %v\n%s", err, s)
	}
}

func TestFirstSessionID(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "log.jsonl")
	body := "{\"type\":\"step_start\",\"sessionID\":\"ses_abc\"}\n{\"type\":\"text\",\"sessionID\":\"ses_abc\"}\n"
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := firstSessionID(p); got != "ses_abc" {
		t.Fatalf("firstSessionID = %q, want ses_abc", got)
	}
}

func TestRequestedModelIDKeepsInnerSlash(t *testing.T) {
	if got := requestedModelID("openrouter/stealth/ox-alpha"); got != "stealth/ox-alpha" {
		t.Fatalf("got %q", got)
	}
}

func TestOpencodeEnvReplacesInheritedPWD(t *testing.T) {
	// FAIL-first (measured 2026-08-23, before opencodeEnv owned PWD): with
	// process cwd=A and inherited PWD=B, the session's directory was B and
	// the round's write landed in B — the launcher shell's PWD overrode
	// --cwd. opencode reads $PWD, not the process working directory.
	t.Setenv("PWD", "/somewhere/else")
	t.Setenv("OLDPWD", "/somewhere/older")
	env := opencodeEnv("/cfg/opencode", "/round/cwd")
	var pwd string
	for _, e := range env {
		k, v, _ := strings.Cut(e, "=")
		switch k {
		case "PWD":
			if pwd != "" {
				t.Fatalf("duplicate PWD entries: %q and %q", pwd, v)
			}
			pwd = v
		case "OLDPWD":
			t.Fatalf("inherited OLDPWD leaked: %s", e)
		}
	}
	if pwd != "/round/cwd" {
		t.Fatalf("PWD = %q, want /round/cwd", pwd)
	}
}

func TestParseOpencodeExportWrongDirectory(t *testing.T) {
	// The recurrence gate for the PWD leak: a session that records a
	// directory other than --cwd fails the round even when the model matched.
	exp := []byte(`{"info":{"directory":"/launcher/shell/cwd","model":{"id":"stealth/ox-alpha","providerID":"openrouter"}},"messages":[{"info":{"role":"assistant","modelID":"stealth/ox-alpha","providerID":"openrouter"}}]}`)
	actual, _, verdict := parseOpencodeExport(exp, "openrouter/stealth/ox-alpha", "/round/cwd")
	if verdict != "wrongdir" {
		t.Fatalf("verdict=%q, want wrongdir", verdict)
	}
	if actual != "/launcher/shell/cwd" {
		t.Fatalf("actual=%q, want the offending directory", actual)
	}
	// Same directory (modulo cleaning) passes.
	_, _, verdict = parseOpencodeExport(exp, "openrouter/stealth/ox-alpha", "/launcher/shell/cwd/")
	if verdict != "ok" {
		t.Fatalf("same dir: verdict=%q, want ok", verdict)
	}
	// An export without a directory field skips the check rather than failing.
	noDir := []byte(`{"messages":[{"info":{"role":"assistant","modelID":"stealth/ox-alpha","providerID":"openrouter"}}]}`)
	if _, _, v := parseOpencodeExport(noDir, "openrouter/stealth/ox-alpha", "/round/cwd"); v != "ok" {
		t.Fatalf("missing directory field must fail open, got %q", v)
	}
}

// An identity assertion that fails at the END of a --detach round has no
// terminal to explain itself to: <log>.err carries the harness's stderr, not
// the launcher's. Measured 2026-08-26 — an ox-alpha round finished its work,
// exited 70, and left model_actual= empty with the reason nowhere on disk.
// The sentinel is the completion evidence, so the verdict belongs in it.
func TestSentinelCarriesTheIdentityVerdict(t *testing.T) {
	r := &round{
		o:            opts{model: "openrouter/stealth/ox-alpha", harness: "opencode"},
		p:            provider{name: "openrouter"},
		modelVerdict: "absent",
		modelSource:  "no assistant message in opencode export",
	}
	got := r.sentinelBody(ExitModelIdentity, "", time.Time{})
	for _, want := range []string{
		"model_verdict=absent",
		"model_source=no assistant message in opencode export",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("sentinel is missing %q; got:\n%s", want, got)
		}
	}
}

// A verified round says so too: an empty verdict would leave the reader
// guessing whether the check ran at all.
func TestSentinelOmitsVerdictLinesWhenUnset(t *testing.T) {
	r := &round{o: opts{model: "m", harness: "opencode"}, p: provider{name: "openrouter"}}
	got := r.sentinelBody(0, "", time.Time{})
	if strings.Contains(got, "model_verdict=") || strings.Contains(got, "model_source=") {
		t.Fatalf("unset verdict must not render a line; got:\n%s", got)
	}
}
