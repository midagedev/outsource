package launch

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/midagedev/outsource/internal/tail"
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

// requiredModelError is what stands between an empty --model and a harness
// building a malformed id out of the empty string. It is gated at the
// mechanism, not through a particular provider, because WHICH provider has no
// default is a fact that moves: openrouter had none between 2026-09-10 (when
// stealth/ox-alpha stopped serving) and 2026-09-17 (when stealth/union-alpha
// took the slot), and the stealth slot will empty again. This is a change of
// premise, not a relaxed standard — every assertion the end-to-end version made
// is made here, and the end-to-end path that a provider WITH a default takes is
// covered by TestProviderDefaultModelLaunchesWithoutTheFlag below.
//
// FAIL-first: make requiredModelError return ("", true) unconditionally and
// both halves of this fail.
func TestRequiredModelErrorDemandsAnIDOnlyWhereThereIsNoDefault(t *testing.T) {
	oc, _ := findHarness("opencode")
	cc, _ := findHarness("claude-code")

	// A provider with no default model, whatever it is called: the refusal has
	// to name the provider, the flag, and the form the harness wants — a
	// caller who is told only "pass --model" still does not know what to type.
	empty := provider{name: "someday-empty", defaultModel: ""}
	msg, ok := requiredModelError(empty, oc, "")
	if ok {
		t.Fatal("a provider with no default model must refuse an absent --model")
	}
	for _, want := range []string{"someday-empty", "no default model", "--model", "openrouter/<id>"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("refusal must contain %q, got: %s", want, msg)
		}
	}
	// An explicit --model satisfies it even with no default.
	if _, ok := requiredModelError(empty, oc, "openrouter/vendor/id"); !ok {
		t.Fatal("an explicit --model must satisfy the guard")
	}
	// A provider that HAS a default is unaffected, on any harness.
	for _, p := range providerTable {
		if p.defaultModel == "" {
			continue
		}
		if msg, ok := requiredModelError(p, cc, ""); !ok {
			t.Fatalf("provider %s has default %q and must pass, got: %s", p.name, p.defaultModel, msg)
		}
	}
}

// The other half of the same fact, end to end: a provider that has a default
// launches on a bare `--provider X` with no --model. This is what the stealth
// slot going empty broke last time and what filling it fixes, and it is the
// invocation every reference doc leads with.
//
// FAIL-first: blank the openrouter row's defaultModel and this fails at exit 64
// with "no default model" — which is precisely the state 2026-09-10 left behind.
func TestProviderDefaultModelLaunchesWithoutTheFlag(t *testing.T) {
	for _, p := range providerTable {
		if p.defaultModel == "" {
			t.Errorf("provider %s has no default model; a bare --provider %s cannot launch. "+
				"That is a legitimate state (the stealth slot empties), but it must be a deliberate "+
				"edit to this row, so update this test in the same commit.", p.name, p.name)
		}
	}

	// And the guard chain really does pass an absent --model through for one:
	// harness form check included, which is where an empty id would otherwise
	// become the malformed "openrouter/".
	or, _ := findProvider("openrouter")
	oc, _ := findHarness("opencode")
	if msg, ok := requiredModelError(or, oc, ""); !ok {
		t.Fatalf("openrouter now has a default and must not demand --model: %s", msg)
	}
	qualified, errMsg := qualifyOpencodeModel("", or.defaultModel)
	if errMsg != "" {
		t.Fatalf("the default model must qualify without a flag: %s", errMsg)
	}
	if qualified != "openrouter/"+or.defaultModel {
		t.Fatalf("qualified = %q, want openrouter/%s", qualified, or.defaultModel)
	}
	// The id reaching opencode's -m must round-trip back to what the identity
	// assertion compares against, inner slash and all.
	if got := requestedModelID(qualified); got != or.defaultModel {
		t.Fatalf("requestedModelID(%q) = %q, want %q", qualified, got, or.defaultModel)
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
	// Every routable cell must name the model a bare launch would run, because
	// that is the question the matrix is read to answer.
	for _, p := range providerTable {
		if p.defaultModel != "" && !strings.Contains(out, p.defaultModel) {
			t.Errorf("--list-wiring omits %s's default model %q:\n%s", p.name, p.defaultModel, out)
		}
	}

	// A provider with NO default must say so where the model would go, rather
	// than print a blank column the reader has to interpret. No live row is in
	// that state today (openrouter regained a default on 2026-09-17), but the
	// stealth slot empties — it did on 2026-09-10 — so the rendering is gated
	// over a synthetic table instead of being dropped with its last example.
	empty := renderWiring([]provider{{name: "openrouter", defaultModel: "", defaultHarness: "opencode"}})
	if !strings.Contains(empty, "(--model required)") {
		t.Errorf("a provider with no default model must be marked, not blank:\n%s", empty)
	}
}

// A harness row that declares a trail format the renderer does not know ships a
// `outsource tail` that cannot read it. The format list is the renderer's own,
// so the two cannot drift.
func TestEveryHarnessDeclaresARenderableTrail(t *testing.T) {
	for _, h := range harnessTable {
		if !tail.KnownFormat(h.trailFormat) {
			t.Fatalf("harness %s declares trailFormat %q, which internal/tail cannot render (known: %s, %s, %s)",
				h.name, h.trailFormat, tail.FormatClaudeTranscript, tail.FormatOpencodeEvents, tail.FormatLines)
		}
	}
}

// A nil trail column means "the round reveals its own", and exactly one harness
// has the machinery for that: writeHookSettings gives claude-code a SessionStart
// recorder. A new row with neither a path nor a reveal would register a format
// for a file nobody can name — which is the state this whole mechanism replaced
// (reported 2026-09-15: ten tool calls to find a live transcript).
func TestAHarnessWithNoTrailPathRevealsItAtRuntime(t *testing.T) {
	revealsAtRuntime := map[string]bool{"claude-code": true}
	for _, h := range harnessTable {
		if h.trail == nil && !revealsAtRuntime[h.name] {
			t.Fatalf("harness %s declares no trail path and has no runtime reveal; `outsource tail` could never find its trail", h.name)
		}
		if h.trail != nil && revealsAtRuntime[h.name] {
			t.Fatalf("harness %s both declares a trail path and reveals one at runtime — two owners of the same field", h.name)
		}
	}
}
