package launch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestQualifyOpencodeModel(t *testing.T) {
	// An OpenRouter id is vendor/model, so the qualified form carries two
	// slashes. FAIL-first would have been a one-slash split:
	// "openrouter/z-ai/glm-5.3-flash" → prefix "openrouter", id "z-ai" and a
	// leftover "glm-5.3-flash". The id here is a fixture for that shape, not a
	// claim about what this skill routes.
	got, errMsg := qualifyOpencodeModel("", "z-ai/glm-5.3-flash")
	if errMsg != "" || got != "openrouter/z-ai/glm-5.3-flash" {
		t.Fatalf("default: got %q err %q, want openrouter/z-ai/glm-5.3-flash", got, errMsg)
	}
	got, errMsg = qualifyOpencodeModel("openrouter/z-ai/glm-5.3-flash", "z-ai/glm-5.3-flash")
	if errMsg != "" || got != "openrouter/z-ai/glm-5.3-flash" {
		t.Fatalf("slash-in-id: got %q err %q", got, errMsg)
	}
	if _, errMsg = qualifyOpencodeModel("openrouter/", "z-ai/glm-5.3-flash"); errMsg == "" {
		t.Fatal("empty remainder must be rejected")
	}
	if _, errMsg = qualifyOpencodeModel("zai/glm-5.3", "z-ai/glm-5.3-flash"); errMsg == "" {
		t.Fatal("non-openrouter prefix must be rejected")
	}
	if _, errMsg = qualifyOpencodeModel("z-ai/glm-5.3-flash", "z-ai/glm-5.3-flash"); errMsg == "" {
		t.Fatal("bare id without openrouter/ must be rejected")
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
	ok := []byte(`{"info":{"model":{"id":"z-ai/glm-5.3-flash","providerID":"openrouter"}},"messages":[{"info":{"role":"user"}},{"info":{"role":"assistant","modelID":"z-ai/glm-5.3-flash","providerID":"openrouter"}}]}`)
	actual, _, verdict := parseOpencodeExport(ok, "openrouter/z-ai/glm-5.3-flash", "")
	if verdict != "ok" || actual != "z-ai/glm-5.3-flash" {
		t.Fatalf("ok case: actual=%q verdict=%q", actual, verdict)
	}
	mismatch := []byte(`{"messages":[{"info":{"role":"assistant","modelID":"glm-5.3","providerID":"openrouter"}}]}`)
	_, _, verdict = parseOpencodeExport(mismatch, "openrouter/z-ai/glm-5.3-flash", "")
	if verdict != "mismatch" {
		t.Fatalf("mismatch case: verdict=%q", verdict)
	}
	none := []byte(`{"messages":[{"info":{"role":"user"}}]}`)
	_, _, verdict = parseOpencodeExport(none, "openrouter/z-ai/glm-5.3-flash", "")
	if verdict != "absent" {
		t.Fatalf("no assistant: verdict=%q", verdict)
	}
	_, _, verdict = parseOpencodeExport([]byte("not json"), "openrouter/z-ai/glm-5.3-flash", "")
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
	if got := requestedModelID("openrouter/z-ai/glm-5.3-flash"); got != "z-ai/glm-5.3-flash" {
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
	exp := []byte(`{"info":{"directory":"/launcher/shell/cwd","model":{"id":"z-ai/glm-5.3-flash","providerID":"openrouter"}},"messages":[{"info":{"role":"assistant","modelID":"z-ai/glm-5.3-flash","providerID":"openrouter"}}]}`)
	actual, _, verdict := parseOpencodeExport(exp, "openrouter/z-ai/glm-5.3-flash", "/round/cwd")
	if verdict != "wrongdir" {
		t.Fatalf("verdict=%q, want wrongdir", verdict)
	}
	if actual != "/launcher/shell/cwd" {
		t.Fatalf("actual=%q, want the offending directory", actual)
	}
	// Same directory (modulo cleaning) passes.
	_, _, verdict = parseOpencodeExport(exp, "openrouter/z-ai/glm-5.3-flash", "/launcher/shell/cwd/")
	if verdict != "ok" {
		t.Fatalf("same dir: verdict=%q, want ok", verdict)
	}
	// An export without a directory field skips the check rather than failing.
	noDir := []byte(`{"messages":[{"info":{"role":"assistant","modelID":"z-ai/glm-5.3-flash","providerID":"openrouter"}}]}`)
	if _, _, v := parseOpencodeExport(noDir, "openrouter/z-ai/glm-5.3-flash", "/round/cwd"); v != "ok" {
		t.Fatalf("missing directory field must fail open, got %q", v)
	}
}

// An identity assertion that fails at the END of a --detach round has no
// terminal to explain itself to: <log>.err carries the harness's stderr, not
// the launcher's. Measured 2026-08-26 — an opencode round finished its work,
// exited 70, and left model_actual= empty with the reason nowhere on disk.
// The sentinel is the completion evidence, so the verdict belongs in it.
func TestSentinelCarriesTheIdentityVerdict(t *testing.T) {
	r := &round{
		o:            opts{model: "openrouter/z-ai/glm-5.3-flash", harness: "opencode"},
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
