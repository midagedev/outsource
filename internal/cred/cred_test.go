package cred

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// isolate points every path this package resolves at a fresh temp tree and
// blanks every variable a source reads, so no test here can read the real
// store, the real opencode auth store or a real key. Returns the tree's root.
func isolate(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", filepath.Join(dir, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(dir, "claude"))
	for _, k := range []string{"OPENROUTER_API_KEY", "OPENROUTER_BASE_URL", "OUTSOURCE_OPENROUTER_BASE_URL", "ZAI_API_KEY", "ZAI_BASE_URL", "XAI_API_KEY", "XAI_BASE_URL"} {
		t.Setenv(k, "")
	}
	return dir
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// where runs `credential <provider> --where` and returns its stdout line.
func where(t *testing.T, provider string) string {
	t.Helper()
	var out, errb bytes.Buffer
	if rc := Main([]string{provider, "--where"}, &out, &errb); rc != 0 {
		t.Fatalf("credential %s --where: rc=%d, stderr=%s", provider, rc, errb.String())
	}
	return strings.TrimSpace(out.String())
}

// The openrouter row's resolution order: the environment, then this skill's
// 0600 store, then — read only — the key `opencode auth login` already left
// in opencode's auth store. Each step is taken away in turn, and the auth
// store must be byte-identical (and keep its mode) after every read.
//
// FAIL-first: drop the opencode source from sources() and the last step
// resolves nothing ("no API key for provider 'openrouter'").
func TestOpenrouterResolutionOrder(t *testing.T) {
	dir := isolate(t)
	store := filepath.Join(dir, "config", "outsource", "credentials")
	auth := filepath.Join(dir, "data", "opencode", "auth.json")
	authBody := `{"anthropic":{"type":"oauth","refresh":"r","access":"a","expires":1},"openrouter":{"type":"api","key":"from-opencode"}}`
	writeFile(t, store, "ZAI_API_KEY=zai-store\nOPENROUTER_API_KEY=from-store\n")
	writeFile(t, auth, authBody)

	t.Setenv("OPENROUTER_API_KEY", "from-env")
	if k, ok := KeyOrExplain("openrouter", &bytes.Buffer{}); !ok || k != "from-env" {
		t.Fatalf("env set: got %q ok=%v, want from-env", k, ok)
	}
	if got := where(t, "openrouter"); got != "$OPENROUTER_API_KEY" {
		t.Fatalf("env set: --where = %q", got)
	}

	t.Setenv("OPENROUTER_API_KEY", "")
	if k, ok := KeyOrExplain("openrouter", &bytes.Buffer{}); !ok || k != "from-store" {
		t.Fatalf("store present: got %q ok=%v, want from-store", k, ok)
	}
	if got := where(t, "openrouter"); got != store {
		t.Fatalf("store present: --where = %q, want %s", got, store)
	}

	if err := os.Remove(store); err != nil {
		t.Fatal(err)
	}
	if k, ok := KeyOrExplain("openrouter", &bytes.Buffer{}); !ok || k != "from-opencode" {
		t.Fatalf("auth store only: got %q ok=%v, want from-opencode", k, ok)
	}
	if got := where(t, "openrouter"); got != auth {
		t.Fatalf("auth store only: --where = %q, want %s", got, auth)
	}

	// Read only: same bytes, same mode, after five resolutions.
	b, err := os.ReadFile(auth)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != authBody {
		t.Fatalf("opencode's auth store changed:\n%s", b)
	}
	if fi, err := os.Stat(auth); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("opencode's auth store mode changed: %v %v", fi.Mode(), err)
	}
	// And nothing was copied into this skill's store.
	if _, err := os.Stat(store); !os.IsNotExist(err) {
		t.Fatalf("resolution must not write the store, stat err=%v", err)
	}
}

// What the auth store can look like when it holds no openrouter key: absent,
// unparseable, without the entry, an entry of the wrong shape, or a blank key.
// Each yields "" — no key, no error — and the refusal still names the file,
// so the user learns where it looked.
func TestOpencodeAuthStoreWithoutAKeyYieldsNothing(t *testing.T) {
	dir := isolate(t)
	auth := filepath.Join(dir, "data", "opencode", "auth.json")
	cases := []struct{ name, body string }{
		{"missing", ""},
		{"unparseable", `{"openrouter":`},
		{"no openrouter entry", `{"openai":{"type":"api","key":"sk"}}`},
		{"entry is not an object", `{"openrouter":"sk-or-x"}`},
		{"blank key", `{"openrouter":{"type":"api","key":"  "}}`},
	}
	for _, c := range cases {
		os.Remove(auth)
		if c.body != "" {
			writeFile(t, auth, c.body)
		}
		if got := opencodeAuthKey(auth, "openrouter"); got != "" {
			t.Errorf("%s: opencodeAuthKey = %q, want empty", c.name, got)
		}
		var errb bytes.Buffer
		k, ok := KeyOrExplain("openrouter", &errb)
		if ok || k != "" {
			t.Errorf("%s: KeyOrExplain = %q ok=%v, want nothing", c.name, k, ok)
		}
		for _, want := range []string{"$OPENROUTER_API_KEY (environment)", auth + " (openrouter.key", "export OPENROUTER_API_KEY=..."} {
			if !strings.Contains(errb.String(), want) {
				t.Errorf("%s: the refusal must name %q, got:\n%s", c.name, want, errb.String())
			}
		}
	}
	// An entry of another provider in a shape this package does not decode
	// must not hide the openrouter entry beside it.
	writeFile(t, auth, `{"anthropic":"not-an-object","openrouter":{"type":"api","key":"sk-or-x"}}`)
	if got := opencodeAuthKey(auth, "openrouter"); got != "sk-or-x" {
		t.Fatalf("a foreign entry's shape hid the openrouter key: got %q", got)
	}
}

// The opencode source is openrouter's only: zai and xai keep exactly the
// sources they had, so a key in opencode's store can never answer for them.
func TestOpencodeAuthSourceIsOpenrouterOnly(t *testing.T) {
	isolate(t)
	for _, name := range []string{"zai", "xai"} {
		for _, s := range sources(providers[name]) {
			if strings.Contains(s.note, "opencode") {
				t.Errorf("provider %s gained the opencode source: %s", name, s.note)
			}
		}
	}
}

// OpencodeAuthPath is the one place the auth store's location is decided —
// the launcher's pre-flight reads the same path through it.
func TestOpencodeAuthPath(t *testing.T) {
	dir := isolate(t)
	if got, want := OpencodeAuthPath(), filepath.Join(dir, "data", "opencode", "auth.json"); got != want {
		t.Fatalf("with XDG_DATA_HOME: %q, want %q", got, want)
	}
	t.Setenv("XDG_DATA_HOME", "")
	if got, want := OpencodeAuthPath(), filepath.Join(dir, "home", ".local", "share", "opencode", "auth.json"); got != want {
		t.Fatalf("without XDG_DATA_HOME: %q, want %q", got, want)
	}
}

// Every message that lists providers is derived from the table, sorted, so
// it cannot go stale. FAIL-first: put back the hand-written list this
// replaced (zai and xai only) and the credential refusal fails on openrouter.
func TestUnknownProviderListsTheKnownOnesSorted(t *testing.T) {
	isolate(t)
	const want = "(known: openrouter xai zai)"
	var errb bytes.Buffer
	if rc := Main([]string{"nope"}, &bytes.Buffer{}, &errb); rc != ExitUsage || !strings.Contains(errb.String(), want) {
		t.Errorf("credential: rc=%d stderr=%q, want %d and %q", rc, errb.String(), ExitUsage, want)
	}
	errb.Reset()
	if _, ok := KeyOrExplain("nope", &errb); ok || !strings.Contains(errb.String(), want) {
		t.Errorf("KeyOrExplain: stderr=%q, want %q", errb.String(), want)
	}
	errb.Reset()
	if rc := VerifyMain([]string{"nope"}, &bytes.Buffer{}, &errb, strings.NewReader("k")); rc != ExitUsage || !strings.Contains(errb.String(), want) {
		t.Errorf("verify-key: rc=%d stderr=%q, want %d and %q", rc, errb.String(), ExitUsage, want)
	}
	errb.Reset()
	if rc := VerifyMain(nil, &bytes.Buffer{}, &errb, strings.NewReader("")); rc != ExitUsage || !strings.Contains(errb.String(), "verify-key <openrouter|xai|zai>") {
		t.Errorf("verify-key usage: rc=%d stderr=%q", rc, errb.String())
	}
}

// The launcher's endpoint for openrouter is the row's default unless
// OUTSOURCE_OPENROUTER_BASE_URL overrides it; no z.ai host logic applies. The
// generic OPENROUTER_BASE_URL is ignored on purpose (2026-10-09): other tools
// set it to …/api/v1, which the claude CLI would turn into …/api/v1/v1/messages.
func TestOpenrouterBase(t *testing.T) {
	isolate(t)
	if got := Base("openrouter", "https://openrouter.ai/api"); got != "https://openrouter.ai/api" {
		t.Fatalf("default: %q", got)
	}
	t.Setenv("OPENROUTER_BASE_URL", "https://openrouter.ai/api/v1")
	if got := Base("openrouter", "https://openrouter.ai/api"); got != "https://openrouter.ai/api" {
		t.Fatalf("the generic OPENROUTER_BASE_URL must not move the launcher's endpoint, got %q", got)
	}
	t.Setenv("OUTSOURCE_OPENROUTER_BASE_URL", "http://127.0.0.1:9/api")
	if got := Base("openrouter", "https://openrouter.ai/api"); got != "http://127.0.0.1:9/api" {
		t.Fatalf("override: %q", got)
	}
}

// verify-key openrouter against a local server: the request is a GET of
// /api/v1/key with the key as a Bearer token; 200 accepts, 401/403 reject
// with the body's error.message on stdout, and a status that says nothing
// about the key (429, 5xx) or no answer at all is unverifiable (2) — never
// "rejected". FAIL-first: route openrouter to the "no free endpoint" branch
// and every case returns 3.
func TestVerifyOpenrouter(t *testing.T) {
	isolate(t)
	status, body := 0, ""
	var gotPath, gotAuth, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth, gotMethod = r.URL.Path, r.Header.Get("Authorization"), r.Method
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	old := openrouterKeyURL
	t.Cleanup(func() { openrouterKeyURL = old })
	openrouterKeyURL = srv.URL + "/api/v1/key"

	for _, c := range []struct {
		name      string
		status    int
		body      string
		rc        int
		stdoutHas string
		stderrHas string
	}{
		{"accepted", 200, `{"data":{"label":"sk-or-v1-abc...","limit":null}}`, 0, "", ""},
		{"rejected 401", 401, `{"error":{"message":"No auth credentials found","code":401}}`, 1, "No auth credentials found", ""},
		{"rejected 403 without a message", 403, `forbidden`, 1, "HTTP 403", ""},
		{"rate limited", 429, `{"error":{"message":"Rate limit exceeded","code":429}}`, ExitEndpointUnreachable, "", "HTTP 429"},
		{"server error", 502, `bad gateway`, ExitEndpointUnreachable, "", "HTTP 502"},
	} {
		status, body = c.status, c.body
		var out, errb bytes.Buffer
		rc := VerifyMain([]string{"openrouter"}, &out, &errb, strings.NewReader("test-key-not-real\n"))
		if rc != c.rc {
			t.Errorf("%s: rc=%d, want %d (stdout=%q stderr=%q)", c.name, rc, c.rc, out.String(), errb.String())
		}
		if gotMethod != "GET" || gotPath != "/api/v1/key" || gotAuth != "Bearer test-key-not-real" {
			t.Errorf("%s: request was %s %s auth=%q, want GET /api/v1/key with the Bearer key", c.name, gotMethod, gotPath, gotAuth)
		}
		if c.stdoutHas != "" && !strings.Contains(out.String(), c.stdoutHas) {
			t.Errorf("%s: stdout %q, want %q", c.name, out.String(), c.stdoutHas)
		}
		if c.stderrHas != "" && !strings.Contains(errb.String(), c.stderrHas) {
			t.Errorf("%s: stderr %q, want %q", c.name, errb.String(), c.stderrHas)
		}
		if c.rc == 0 && out.Len() != 0 {
			t.Errorf("%s: an accepted key prints nothing, got %q", c.name, out.String())
		}
	}

	// No answer at all: the network failed, not the key.
	srv.Close()
	var errb bytes.Buffer
	if rc := VerifyMain([]string{"openrouter"}, &bytes.Buffer{}, &errb, strings.NewReader("k")); rc != ExitEndpointUnreachable || !strings.Contains(errb.String(), "could not reach openrouter.ai") {
		t.Fatalf("unreachable: rc=%d stderr=%q, want %d", rc, errb.String(), ExitEndpointUnreachable)
	}
}
