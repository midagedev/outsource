package launch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAgyPairing(t *testing.T) {
	if msg := pairingRefusal("agy", "agy"); msg != "" {
		t.Fatalf("agy on agy must be allowed, got: %s", msg)
	}
	if msg := pairingRefusal("agy", "zai"); msg == "" {
		t.Fatal("agy harness with provider zai must be refused")
	}
	if msg := pairingRefusal("claude-code", "agy"); msg == "" {
		t.Fatal("provider agy on the claude-code harness must be refused")
	}
	if msg := pairingRefusal("crush", "agy"); msg == "" {
		t.Fatal("provider agy on the crush harness must be refused")
	}
}

func TestAgyDefaultHarness(t *testing.T) {
	if h := defaultHarness("agy", ""); h != "agy" {
		t.Fatalf("provider agy must default to its own harness, got %s", h)
	}
	if h := defaultHarness("agy", "claude-code"); h != "claude-code" {
		t.Fatalf("an explicit --harness must win, got %s", h)
	}
}

func TestAgyProviderVision(t *testing.T) {
	p, ok := findProvider("agy")
	if !ok {
		t.Fatal("provider agy missing from the table")
	}
	if !modelVision(p, "gemini-3.7-flash-high") {
		t.Fatal("agy is measured to see pixels; provider vision must be true")
	}
	if p.defaultModel != "gemini-3.7-flash-high" {
		t.Fatalf("default model must be the high effort tier (user decision 2026-08-27), got %s", p.defaultModel)
	}
}

func TestAgyEnsureGitGuard(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")

	// A file agy has never written: the guard creates it.
	if err := agyEnsureGitGuard(path); err != nil {
		t.Fatalf("guard on a missing file: %v", err)
	}
	assertGuardPresent(t, path)

	// The user's existing keys survive, and the install is idempotent.
	seed := map[string]any{
		"enableTelemetry":   false,
		"trustedWorkspaces": []string{"/some/where"},
		"permissions": map[string]any{
			"allow": []string{"command(npm test)"},
			"deny":  []string{"command(rm -rf)"},
		},
	}
	b, _ := json.Marshal(seed)
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := agyEnsureGitGuard(path); err != nil {
		t.Fatalf("guard on an existing file: %v", err)
	}
	first, _ := os.ReadFile(path)
	if err := agyEnsureGitGuard(path); err != nil {
		t.Fatalf("second install: %v", err)
	}
	second, _ := os.ReadFile(path)
	if string(first) != string(second) {
		t.Fatal("guard install is not idempotent: the file changed on a second run")
	}
	var doc struct {
		EnableTelemetry   *bool    `json:"enableTelemetry"`
		TrustedWorkspaces []string `json:"trustedWorkspaces"`
		Permissions       struct {
			Allow []string `json:"allow"`
			Deny  []string `json:"deny"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal(second, &doc); err != nil {
		t.Fatalf("rewritten settings unparseable: %v", err)
	}
	if doc.EnableTelemetry == nil || *doc.EnableTelemetry {
		t.Fatal("enableTelemetry:false was not preserved")
	}
	if len(doc.TrustedWorkspaces) != 1 || doc.TrustedWorkspaces[0] != "/some/where" {
		t.Fatal("trustedWorkspaces was not preserved")
	}
	if len(doc.Permissions.Allow) != 1 || doc.Permissions.Allow[0] != "command(npm test)" {
		t.Fatal("the user's allow rules were not preserved")
	}
	if doc.Permissions.Deny[0] != "command(rm -rf)" {
		t.Fatal("the user's own deny rule must stay first")
	}
	assertGuardPresent(t, path)

	// A file that is not JSON is refused, not clobbered.
	if err := os.WriteFile(path, []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := agyEnsureGitGuard(path); err == nil {
		t.Fatal("an unparseable settings file must refuse, not rewrite")
	}
	if b, _ := os.ReadFile(path); string(b) != "not json" {
		t.Fatal("the unparseable file was modified")
	}
}

func assertGuardPresent(t *testing.T, path string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Permissions struct {
			Deny []string `json:"deny"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("settings unparseable after guard: %v", err)
	}
	have := map[string]bool{}
	for _, d := range doc.Permissions.Deny {
		have[d] = true
	}
	for _, rule := range agyGitDenyRules {
		if !have[rule] {
			t.Fatalf("deny rule missing after guard install: %s", rule)
		}
	}
}

// The sample lines are verbatim from measured probes (2026-08-27, agy 1.1.21).
const agySampleStream = `{"event":"init","conversation_id":"17b98e71-8fa7-491f-8158-c41fd3f5ca51","init":{"model":"gemini-3.7-flash-low","cwd":"/tmp/x","tools":["view_file"],"permission_mode":"always-proceed"}}
{"event":"step_update","step_update":{"conversation_id":"17b98e71-8fa7-491f-8158-c41fd3f5ca51","step_index":0,"state":"DONE","step_type":"user_input"}}
{"event":"result","result":{"conversation_id":"17b98e71-8fa7-491f-8158-c41fd3f5ca51","status":"SUCCESS","response":"PING2\nDONE-x\n","duration_seconds":1.8,"num_turns":1,"usage":{"input_tokens":15881,"output_tokens":3}}}
`

func TestParseAgyStream(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "run.log")
	if err := os.WriteFile(log, []byte(agySampleStream), 0o644); err != nil {
		t.Fatal(err)
	}
	st := parseAgyStream(log)
	if st.conversationID != "17b98e71-8fa7-491f-8158-c41fd3f5ca51" {
		t.Fatalf("conversation id: %q", st.conversationID)
	}
	if st.initModel != "gemini-3.7-flash-low" {
		t.Fatalf("init model: %q", st.initModel)
	}
	if st.status != "SUCCESS" {
		t.Fatalf("status: %q", st.status)
	}
}

func TestParseAgyStreamCanceled(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "run.log")
	body := `{"event":"result","result":{"conversation_id":"x","status":"CANCELED","response":"","error":"permission denied"}}` + "\n"
	if err := os.WriteFile(log, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	st := parseAgyStream(log)
	if st.status != "CANCELED" || st.errText != "permission denied" {
		t.Fatalf("status=%q err=%q", st.status, st.errText)
	}
}

func TestAssertAgyIdentityFromEcho(t *testing.T) {
	// No conversation db exists for this sid; the echo fallback decides.
	actual, src, verdict := assertAgyIdentity("no-such-sid-outsource-test", "gemini-3.7-flash-high", "gemini-3.7-flash-high")
	if verdict != "ok" || actual != "gemini-3.7-flash-high" {
		t.Fatalf("echo match must pass: verdict=%s actual=%s src=%s", verdict, actual, src)
	}
	if !strings.Contains(src, "echo") {
		t.Fatalf("echo-level evidence must be labelled as such, got: %s", src)
	}
	_, _, verdict = assertAgyIdentity("no-such-sid-outsource-test", "gemini-3.7-flash-high", "gemini-3.1-pro-high")
	if verdict != "mismatch" {
		t.Fatalf("an init model differing from the request is a mismatch, got %s", verdict)
	}
	_, _, verdict = assertAgyIdentity("no-such-sid-outsource-test", "gemini-3.7-flash-high", "")
	if verdict != "absent" {
		t.Fatalf("no evidence at all must be absent, got %s", verdict)
	}
}

func TestAgyModelSlugScan(t *testing.T) {
	blob := []byte("\x00\x01gemini-3.7-flash\xffgemini-3.7-flash-low\x00claude-sonnet-4-6\x00MODEL_PLACEHOLDER_M300")
	found := map[string]bool{}
	for _, m := range agyModelSlug.FindAll(blob, -1) {
		found[string(m)] = true
	}
	for _, want := range []string{"gemini-3.7-flash", "gemini-3.7-flash-low", "claude-sonnet-4-6"} {
		if !found[want] {
			t.Fatalf("slug scan missed %s (found: %v)", want, found)
		}
	}
}

func TestAgyPrintTimeout(t *testing.T) {
	if got := agyPrintTimeout(""); got != "1440m" {
		t.Fatalf("default ceiling: %s", got)
	}
	if got := agyPrintTimeout("120"); got != "1440m" {
		t.Fatalf("a small --max-seconds keeps the floor (the watchdog kills first): %s", got)
	}
	if got := agyPrintTimeout("172800"); got != "2890m" {
		t.Fatalf("a huge --max-seconds pushes the ceiling above itself: %s", got)
	}
}
