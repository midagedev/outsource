package launch

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWiringTablesAgree is the recurrence gate for a half-wired arm.
//
// Before wiring.go, "what can run where" was spread over nine places in
// outsource.go, and adding or withdrawing an arm meant editing all of them by
// hand. Nothing checked that they agreed: a provider could name a default
// harness that refused it, or a harness could be dispatchable and invisible to
// the flag validation. That class of defect is only reachable through the
// tables now, and this test closes it.
//
// FAIL-first: drop "zai" from the claude-code row's providers and this fails
// with "default harness claude-code does not drive it"; blank a harness's bin
// and it fails on the PATH-lookup column that --detach needs.
func TestWiringTablesAgree(t *testing.T) {
	seenHarness := map[string]bool{}
	for _, h := range harnessTable {
		if h.name == "" {
			t.Fatal("a harness row has no name")
		}
		if seenHarness[h.name] {
			t.Fatalf("harness %s is declared twice", h.name)
		}
		seenHarness[h.name] = true
		// bin is what --detach looks up on PATH before re-execing; an empty
		// one would look up "" and report a missing CLI with no name in it.
		if h.bin == "" {
			t.Errorf("harness %s has no bin: --detach cannot check PATH for it", h.name)
		}
		// run is the dispatch. A row without it is routable by the flag
		// validation and then refused at dispatch — the exact split this file
		// exists to remove.
		if h.run == nil {
			t.Errorf("harness %s has no run func: it would validate and then fail at dispatch", h.name)
		}
		// progress is what the registry watches to tell working from wedged.
		if h.progress == nil {
			t.Errorf("harness %s has no progress func: runs would show no live trail", h.name)
		}
		if len(h.providers) == 0 {
			t.Errorf("harness %s drives no provider: it can never be paired", h.name)
		}
		for _, pn := range h.providers {
			if _, ok := findProvider(pn); !ok {
				t.Errorf("harness %s lists provider %q, which is not in providerTable", h.name, pn)
			}
		}
		// A hint without a rule (or a rule without a hint) sends the caller to
		// a form nothing enforces, or enforces one nothing explains.
		if (h.modelForm == nil) != (h.modelFormHint == "") {
			t.Errorf("harness %s: modelForm and modelFormHint must be set together (form=%v hint=%q)",
				h.name, h.modelForm != nil, h.modelFormHint)
		}
	}

	seenProvider := map[string]bool{}
	for _, p := range providerTable {
		if p.name == "" {
			t.Fatal("a provider row has no name")
		}
		if seenProvider[p.name] {
			t.Fatalf("provider %s is declared twice", p.name)
		}
		seenProvider[p.name] = true
		if p.defaultHarness == "" {
			t.Errorf("provider %s has no defaultHarness", p.name)
			continue
		}
		h, ok := findHarness(p.defaultHarness)
		if !ok {
			t.Errorf("provider %s defaults to harness %q, which is not in harnessTable", p.name, p.defaultHarness)
			continue
		}
		// The one that actually bit: a default that the pairing matrix refuses
		// means every plain `--provider X` invocation dies on a usage error.
		if !h.drives(p.name) {
			t.Errorf("provider %s defaults to harness %s, but that harness does not drive it (drives: %s)",
				p.name, h.name, strings.Join(h.providers, " "))
		}
		if len(harnessesFor(p.name)) == 0 {
			t.Errorf("provider %s is driven by no harness: it cannot be routed", p.name)
		}
	}
}

// The pairing matrix is derived from one column, so an allowed cell and a
// refused one cannot disagree. These are the cells that must keep working.
func TestPairingAllowedCells(t *testing.T) {
	for _, c := range []struct{ harness, provider string }{
		{"claude-code", "zai"},
		{"crush", "zai"},
		{"claude-code", "xai"},
		{"crush", "xai"},
		{"opencode", "openrouter"},
		{"agy", "agy"},
	} {
		if msg := pairingRefusal(c.harness, c.provider); msg != "" {
			t.Errorf("%s+%s refused: %s", c.provider, c.harness, msg)
		}
	}
}

// A refused pair must be refused BEFORE the registry records it — a round that
// was never going to launch is not a round.
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
		{"zai", "opencode", "harness opencode does not drive provider zai"},
		{"xai", "opencode", "harness opencode does not drive provider xai"},
		{"openrouter", "claude-code", "harness claude-code does not drive provider openrouter"},
		{"openrouter", "crush", "harness crush does not drive provider openrouter"},
		{"zai", "agy", "harness agy does not drive provider zai"},
		{"agy", "claude-code", "harness claude-code does not drive provider agy"},
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
		// The refusal says where the provider DOES run, so the reader's next
		// command is in the message.
		if where := harnessesFor(c.provider); len(where) > 0 && !strings.Contains(stderr.String(), strings.Join(where, " ")) {
			t.Errorf("%s+%s: refusal must name where it runs (%s), got: %s",
				c.provider, c.harness, strings.Join(where, " "), stderr.String())
		}
		ents, _ := os.ReadDir(filepath.Join(dir, "runs"))
		if len(ents) != 0 {
			t.Errorf("%s+%s: a refused pair must not register a round, found %d", c.provider, c.harness, len(ents))
		}
	}
}

// An unknown --harness is refused by the table, and the message lists what is
// actually wired rather than a hand-maintained string that can go stale.
func TestUnknownHarnessNamesTheWiredOnes(t *testing.T) {
	dir := t.TempDir()
	spec := filepath.Join(dir, "spec.md")
	if err := os.WriteFile(spec, []byte("do a thing\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OUTSOURCE_RUNS_DIR", filepath.Join(dir, "runs"))
	var stderr bytes.Buffer
	rc := OutsourceMain([]string{
		"--cwd", dir, "--spec", spec, "--log", filepath.Join(dir, "x.log"),
		"--provider", "zai", "--harness", "nope", "--label", "unknown-harness",
	}, &bytes.Buffer{}, &stderr)
	if rc != ExitUsage {
		t.Fatalf("rc=%d, want %d; stderr=%s", rc, ExitUsage, stderr.String())
	}
	for _, h := range harnessTable {
		if !strings.Contains(stderr.String(), h.name) {
			t.Fatalf("refusal must name wired harness %s, got: %s", h.name, stderr.String())
		}
	}
}

func TestDefaultHarnessComesFromTheProviderRow(t *testing.T) {
	for _, p := range providerTable {
		if got := defaultHarness(p.name, ""); got != p.defaultHarness {
			t.Errorf("defaultHarness(%s) = %q, want %q", p.name, got, p.defaultHarness)
		}
	}
	// An explicit --harness always wins over the row's default.
	if got := defaultHarness("openrouter", "crush"); got != "crush" {
		t.Fatalf("explicit harness must win, got %q", got)
	}
	// An unknown provider still resolves to something dispatchable rather than
	// an empty harness name.
	if got := defaultHarness("no-such-provider", ""); got != "claude-code" {
		t.Fatalf("unknown provider fallback = %q, want claude-code", got)
	}
}

// seedModel reads the provider row's own env var, so one provider's model pin
// can never reach another's --model. FAIL-first (measured, pre-table): a
// GLM_DELEGATE_MODEL applied to every provider leaked a glm-* id into
// opencode's -m and opencode rejected it.
func TestSeedModelIsScopedByTheProviderRow(t *testing.T) {
	t.Setenv("GLM_DELEGATE_MODEL", "glm-5.3")
	if got := seedModel("openrouter", ""); got != "" {
		t.Fatalf("openrouter has no modelEnv and must ignore GLM_DELEGATE_MODEL, got %q", got)
	}
	if got := seedModel("agy", ""); got != "" {
		t.Fatalf("agy has no modelEnv and must ignore GLM_DELEGATE_MODEL, got %q", got)
	}
	if got := seedModel("zai", ""); got != "glm-5.3" {
		t.Fatalf("zai still reads GLM_DELEGATE_MODEL, got %q", got)
	}
	if got := seedModel("zai", "glm-4.6"); got != "glm-4.6" {
		t.Fatalf("--model must win over the env, got %q", got)
	}
}

// A provider whose only routed model was withdrawn (openrouter, after
// stealth/ox-alpha stopped serving on 2026-09-10) must ask for --model at
// launch. FAIL-first: with defaultModel empty and no such guard, the empty
// string reached qualifyOpencodeModel and became "openrouter/" — a malformed
// id raised INSIDE the --detach child, where nothing can print, so the caller
// saw "detached (pid=…)" and exit 0 over a round that was already dead.
func TestProviderWithoutDefaultModelDemandsOne(t *testing.T) {
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
		"--provider", "openrouter", "--label", "no-default-model",
	}, &bytes.Buffer{}, &stderr)
	if rc != ExitUsage {
		t.Fatalf("rc=%d, want %d; stderr=%s", rc, ExitUsage, stderr.String())
	}
	for _, want := range []string{"no default model", "--model", "openrouter/<id>"} {
		if !strings.Contains(stderr.String(), want) {
			t.Fatalf("refusal must contain %q, got: %s", want, stderr.String())
		}
	}
	ents, _ := os.ReadDir(filepath.Join(dir, "runs"))
	if len(ents) != 0 {
		t.Fatalf("a refused launch must not register a round, found %d", len(ents))
	}
	// A provider that HAS a default is unaffected by the same guard.
	zai, _ := findProvider("zai")
	cc, _ := findHarness("claude-code")
	if msg, ok := requiredModelError(zai, cc, ""); !ok {
		t.Fatalf("zai has a default model and must pass, got: %s", msg)
	}
}

// The opencode model-form rule is checked before the --detach re-exec, the
// same place crush's is. FAIL-first: this arm's check lived only inside
// runOpencode, i.e. inside the detached child.
func TestOpencodeModelFormIsPreflighted(t *testing.T) {
	h, ok := findHarness("opencode")
	if !ok || h.modelForm == nil {
		t.Fatal("the opencode harness must declare a modelForm rule")
	}
	if msg, ok := h.modelForm("zai/glm-5.3", "openrouter"); ok {
		t.Fatalf("a non-openrouter prefix must be refused, got ok (msg=%q)", msg)
	}
	if _, ok := h.modelForm("openrouter/z-ai/glm-5.3-flash", "openrouter"); !ok {
		t.Fatal("a well-formed openrouter/<vendor>/<id> must pass")
	}
	// Empty is not a form error: requiredModelError owns that case, and the
	// two guards must not both claim it with different messages.
	if _, ok := h.modelForm("", "openrouter"); !ok {
		t.Fatal("an empty model is not a form error")
	}
}

// --list-wiring is the debuggability half: "what can run where" answered by a
// command instead of a read of wiring.go.
func TestListWiringPrintsEveryRoutableCell(t *testing.T) {
	var stdout bytes.Buffer
	if rc := OutsourceMain([]string{"--list-wiring"}, &stdout, &bytes.Buffer{}); rc != 0 {
		t.Fatalf("--list-wiring rc=%d, want 0", rc)
	}
	out := stdout.String()
	for _, p := range providerTable {
		for _, hn := range harnessesFor(p.name) {
			line := ""
			for _, l := range strings.Split(out, "\n") {
				if strings.HasPrefix(l, p.name+" ") && strings.Contains(l, hn) {
					line = l
					break
				}
			}
			if line == "" {
				t.Errorf("--list-wiring omits the routable cell %s+%s:\n%s", p.name, hn, out)
			}
		}
	}
	// A provider with no default must say so where the model would go, not
	// print a blank column the reader has to interpret.
	if !strings.Contains(out, "(--model required)") {
		t.Errorf("--list-wiring must mark a provider with no default model:\n%s", out)
	}
}
