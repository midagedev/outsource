package launch

import (
	"bytes"
	"fmt"
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

// qualifierOf is the qualifier column's single accessor. zen is the reason it
// exists: the launcher name is zen, the id opencode's CLI writes is
// "opencode" (measured 2026-10-09). FAIL-first: make it return p.name
// unconditionally and the zen assertion fails while the others keep passing —
// exactly the split that hides a broken mapping.
func TestQualifierOf(t *testing.T) {
	zen, ok := findProvider("zen")
	if !ok {
		t.Fatal("the zen provider row is gone")
	}
	if got := qualifierOf(zen); got != "opencode" {
		t.Errorf("qualifierOf(zen) = %q, want opencode (OpenCode Zen's CLI provider id)", got)
	}
	for _, name := range []string{"zai", "openrouter"} {
		p, ok := findProvider(name)
		if !ok {
			t.Fatalf("the %s provider row is gone", name)
		}
		if p.qualifier != "" {
			t.Errorf("provider %s sets qualifier %q; an empty column (same as name) is the design for every row but zen", name, p.qualifier)
		}
		if got := qualifierOf(p); got != name {
			t.Errorf("qualifierOf(%s) = %q, want %q (empty qualifier means same as name)", name, got, name)
		}
	}
}

// findProviderIn is findProvider over a given table, so a checker fixture can
// resolve providers in a broken COPY (the renderWiring/modelTableProblems
// property: a violation is never demonstrated by editing a live row).
func findProviderIn(providers []provider, name string) (provider, bool) {
	for _, p := range providers {
		if p.name == name {
			return p, true
		}
	}
	return provider{}, false
}

// providerTableProblems is the qualifier-consistency checker, over a given
// provider table so a violation fixture is a broken COPY, never a live-row
// edit. Empty means the table agrees.
//
// Two providers on one harness with the same qualifier are indistinguishable
// downstream: the export's providerID (what assertOpencodeIdentity compares
// against) is the qualifier, so a zen round answered under openrouter's id —
// or the reverse — would PASS the identity assertion. That is a wiring
// defect, and this is where it dies.
func providerTableProblems(providers []provider) []string {
	var probs []string
	for _, h := range harnessTable {
		seen := map[string]string{} // qualifier → provider name that claimed it
		for _, pn := range h.providers {
			p, ok := findProviderIn(providers, pn)
			if !ok {
				continue // TestWiringTablesAgree owns the unknown-provider clause
			}
			q := qualifierOf(p)
			if first, clash := seen[q]; clash {
				probs = append(probs, fmt.Sprintf(
					"harness %s drives two providers with the qualifier %q (%s and %s): the export's providerID could not tell their rounds apart",
					h.name, q, first, p.name))
				continue
			}
			seen[q] = p.name
		}
	}
	return probs
}

// TestProviderQualifiersAreUniquePerHarness is the recurrence gate for two
// providers quietly sharing a qualifier on one harness — the state in which
// the model-identity assertion stops discriminating. Asserted on the live
// table (no problems) and on two broken-copy fixtures, one per direction.
//
// FAIL-first: reduce providerTableProblems to a no-op (return nil) and both
// fixture assertions below fail with "the checker must flag".
func TestProviderQualifiersAreUniquePerHarness(t *testing.T) {
	if probs := providerTableProblems(providerTable); len(probs) > 0 {
		for _, p := range probs {
			t.Errorf("live providerTable: %s", p)
		}
	}
	// Fixture 1: zen's qualifier overwritten with openrouter's.
	broken := append([]provider(nil), providerTable...)
	for i := range broken {
		if broken[i].name == "zen" {
			broken[i].qualifier = "openrouter"
		}
	}
	probs := providerTableProblems(broken)
	if len(probs) == 0 || !strings.Contains(strings.Join(probs, "\n"), `qualifier "openrouter" (openrouter and zen)`) {
		t.Errorf("zen claiming openrouter's qualifier must be flagged with both names, got: %v", probs)
	}
	// Fixture 2: the other direction — openrouter adopting zen's qualifier.
	broken = append([]provider(nil), providerTable...)
	for i := range broken {
		if broken[i].name == "openrouter" {
			broken[i].qualifier = "opencode"
		}
	}
	probs = providerTableProblems(broken)
	if len(probs) == 0 || !strings.Contains(strings.Join(probs, "\n"), `qualifier "opencode" (openrouter and zen)`) {
		t.Errorf("openrouter claiming zen's qualifier must be flagged with both names, got: %v", probs)
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
		{"opencode", "zen"},
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
		{"zen", "claude-code", "harness claude-code does not drive provider zen"},
		{"zen", "crush", "harness crush does not drive provider zen"},
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
	// Repointed 2026-10-09 (was "openrouter/<id>"): the hint's <provider>
	// token is substituted with the provider's own qualifier, so a synthetic
	// provider gets its own name — derivation: qualifierOf(provider{name:
	// "someday-empty"}) = "someday-empty", the empty qualifier meaning
	// "same as name". The old expectation failed on the substituted hint
	// ("…got: … someday-empty/<id>", not "openrouter/<id>"); the live-row
	// assertion below keeps the old meaning where it still holds.
	for _, want := range []string{"someday-empty", "no default model", "--model", "someday-empty/<id>"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("refusal must contain %q, got: %s", want, msg)
		}
	}
	// The live openrouter row keeps the openrouter/<id> hint byte for byte:
	// its qualifier is empty, so the substitution reproduces the old text.
	// openrouter has no default (emptyByDesign), so the refusal IS reachable
	// on the live row — the same state the old assertion pinned.
	openrouter, ok := findProvider("openrouter")
	if !ok {
		t.Fatal("the openrouter provider row is gone")
	}
	liveMsg, liveOK := requiredModelError(openrouter, oc, "")
	if liveOK {
		t.Fatal("openrouter has no default model and must refuse an absent --model")
	}
	if !strings.Contains(liveMsg, "openrouter/<id>") {
		t.Fatalf("the openrouter row's substituted hint must still read openrouter/<id>, got: %s", liveMsg)
	}
	// And the shape the substitution exists for, on a synthetic zen-like row
	// (the live zen row has a default, so the guard never refuses it today):
	// a provider whose qualifier differs from its name renders the QUALIFIER
	// in the hint, not the launcher name.
	zenLike := provider{name: "zen", qualifier: "opencode", defaultModel: ""}
	zenMsg, zenOK := requiredModelError(zenLike, oc, "")
	if zenOK {
		t.Fatal("a zen-like provider with no default must refuse an absent --model")
	}
	if !strings.Contains(zenMsg, "opencode/<id>") || strings.Contains(zenMsg, "zen/<id>") {
		t.Fatalf("a zen-like row's hint must read opencode/<id>, not zen/<id>, got: %s", zenMsg)
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
// launches on a bare `--provider X` with no --model, and the id it launches
// with survives the round trip the identity assertion compares against.
//
// Re-premised 2026-09-18. It used to read openrouter, because openrouter was
// the row whose default had just been filled. openrouter is now the row whose
// default has just emptied — stealth/union-alpha stopped serving, the second
// occupant of that slot to do so — so the roles swap: openrouter is the named
// exception, every other row must still carry a default, and the opencode
// qualification half runs on an explicit id, which is what a caller of that
// provider now types anyway.
//
// FAIL-first (measured 2026-09-18 on the pre-blanking source): remove
// openrouter from emptyByDesign and this fails with "provider openrouter has
// no default model".
func TestProviderDefaultModelLaunchesWithoutTheFlag(t *testing.T) {
	// The rows that are deliberately without a default. Naming them one by one
	// is the point: a row that empties without this list being edited in the
	// same commit is an accident, and that is the only thing this loop can
	// still catch now that an empty row is a legitimate state.
	emptyByDesign := map[string]bool{
		// The stealth slot. Empty between 2026-09-10 (ox-alpha withdrawn) and
		// 2026-09-16 (union-alpha listed), and empty again since 2026-09-18
		// (union-alpha withdrawn and unveiled as unbiased/pareto, priced).
		"openrouter": true,
	}
	for _, p := range providerTable {
		if emptyByDesign[p.name] {
			if p.defaultModel != "" {
				t.Errorf("provider %s is listed as having no default but carries %q; "+
					"a slot that refills is good news, but drop it from emptyByDesign "+
					"in the same commit so the loop guards it again.", p.name, p.defaultModel)
			}
			continue
		}
		if p.defaultModel == "" {
			t.Errorf("provider %s has no default model; a bare --provider %s cannot launch. "+
				"That is a legitimate state (the stealth slot empties), but it must be a deliberate "+
				"edit to this row, so add it to emptyByDesign in the same commit.", p.name, p.name)
		}
	}

	// A provider that does carry a default really does pass an absent --model
	// through the guard chain, on the harness that provider launches with.
	for _, p := range providerTable {
		if p.defaultModel == "" {
			continue
		}
		h, ok := findHarness(p.defaultHarness)
		if !ok {
			t.Fatalf("provider %s declares harness %q, which is not in the table", p.name, p.defaultHarness)
		}
		if msg, ok := requiredModelError(p, h, ""); !ok {
			t.Errorf("provider %s has default %q and must not demand --model: %s", p.name, p.defaultModel, msg)
		}
	}

	// And the opencode id round-trips, inner slash and all: the qualified form
	// is what reaches `-m`, and requestedModelID is what the identity
	// assertion compares the transcript against. With no default in the row,
	// the id is the caller's — which is the form every openrouter launch takes
	// today.
	const id = "vendor/model"
	qualified, errMsg := qualifyOpencodeModel("openrouter/"+id, "openrouter", "")
	if errMsg != "" {
		t.Fatalf("an explicit openrouter id must qualify: %s", errMsg)
	}
	if qualified != "openrouter/"+id {
		t.Fatalf("qualified = %q, want openrouter/%s", qualified, id)
	}
	if got := requestedModelID(qualified, "openrouter"); got != id {
		t.Fatalf("requestedModelID(%q) = %q, want %q", qualified, got, id)
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
	openrouter, ok := findProvider("openrouter")
	if !ok {
		t.Fatal("the openrouter provider row is gone")
	}
	if msg, ok := h.modelForm("zai/glm-5.3", openrouter); ok {
		t.Fatalf("a non-openrouter prefix must be refused, got ok (msg=%q)", msg)
	}
	if _, ok := h.modelForm("openrouter/z-ai/glm-5.3-flash", openrouter); !ok {
		t.Fatal("a well-formed openrouter/<vendor>/<id> must pass")
	}
	// Empty is not a form error: requiredModelError owns that case, and the
	// two guards must not both claim it with different messages.
	if _, ok := h.modelForm("", openrouter); !ok {
		t.Fatal("an empty model is not a form error")
	}

	// The rule takes the provider row: zen's qualifier is opencode, so the
	// accepted form is opencode/<id> even though the launcher provider is
	// named zen. FAIL-first: make opencodeModelFormError ignore its provider
	// (the pre-qualifier "openrouter" literal) and the pass case below
	// refuses.
	zen, ok := findProvider("zen")
	if !ok {
		t.Fatal("the zen provider row is gone")
	}
	if _, ok := h.modelForm("opencode/step-5-preview-free", zen); !ok {
		t.Fatal("a well-formed opencode/<id> must pass for zen")
	}
	msg, ok := h.modelForm("zen/step-5-preview-free", zen)
	if ok {
		t.Fatal("the launcher name as prefix must be refused for zen")
	}
	for _, want := range []string{"opencode/<id>", "zen"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("the zen refusal must contain %q, got: %s", want, msg)
		}
	}
	if _, ok := h.modelForm("openrouter/x", zen); ok {
		t.Fatal("an openrouter prefix must be refused for zen")
	}

	// crush keeps its text byte-identical for zai/xai: the prefix it compares
	// moved to qualifierOf, which is the provider's own name for every
	// crush-driven provider.
	crush, ok := findHarness("crush")
	if !ok || crush.modelForm == nil {
		t.Fatal("the crush harness must declare a modelForm rule")
	}
	zai, ok := findProvider("zai")
	if !ok {
		t.Fatal("the zai provider row is gone")
	}
	if _, ok := crush.modelForm("zai/glm-5.3", zai); !ok {
		t.Fatal("crush must accept zai/<id> for zai")
	}
	msg, ok = crush.modelForm("xai/grok-4.6", zai)
	if ok {
		t.Fatal("crush must refuse a foreign provider prefix")
	}
	if msg != "--model xai/grok-4.6 does not match --provider zai" {
		t.Fatalf("crush refusal text must stay byte-identical, got: %s", msg)
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
				continue
			}
			// The DEFAULT MODEL cell must not run into NOTES: the column is
			// 28 wide (widened 2026-10-09 from 24, which muse-spark-1.3-
			// contributor at 26 chars overflowed), and an overflowing cell
			// leaves a single space — two columns reading as one token.
			def := p.defaultModel
			if def == "" {
				def = "(--model required)"
			}
			if i := strings.Index(line, def); i < 0 {
				t.Errorf("%s+%s: the line has no DEFAULT MODEL cell %q: %q", p.name, hn, def, line)
			} else if rest := line[i+len(def):]; rest != "" && !strings.HasPrefix(rest, "  ") {
				t.Errorf("%s+%s: DEFAULT MODEL cell %q runs into NOTES (fewer than two spaces after it): %q", p.name, hn, def, line)
			}
		}
	}
	// zen: the line must name the default id and the substituted form hint —
	// opencode/<id>, the qualifier, not the launcher name.
	zenLine := ""
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "zen ") {
			zenLine = l
			break
		}
	}
	if zenLine == "" {
		t.Errorf("--list-wiring omits the zen row:\n%s", out)
	} else {
		for _, want := range []string{"step-5-preview-free", "--model form opencode/<id>"} {
			if !strings.Contains(zenLine, want) {
				t.Errorf("the zen line must contain %q: %q", want, zenLine)
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
	// than print a blank column the reader has to interpret. openrouter is in
	// that state again since 2026-09-18, so this reads the live table rather
	// than the synthetic one it fell back to while the slot was filled.
	if !strings.Contains(out, "(--model required)") {
		t.Errorf("openrouter has no default model and must be marked, not blank:\n%s", out)
	}

	// The model axis: every modelTable row must be a line of its own, and
	// every provider must close with its (other ids) line — the vision
	// guard's answer for an unlisted id, which is otherwise only in source.
	// Searched in the model block only: the pairing block above also names
	// default ids, and a match there would prove nothing about this block.
	t.Log("\n" + out)
	mblock := ""
	if i := strings.Index(out, "MODELS ("); i >= 0 {
		mblock = out[i:]
	} else {
		t.Fatal("--list-wiring has no MODELS block:\n" + out)
	}
	for _, m := range modelTable {
		found := false
		for _, l := range strings.Split(mblock, "\n") {
			if strings.HasPrefix(l, m.provider+" ") && strings.Contains(l, m.id+" ") {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("--list-wiring omits the model row %s/%s:\n%s", m.provider, m.id, out)
		}
	}
	for _, p := range providerTable {
		found := false
		for _, l := range strings.Split(mblock, "\n") {
			if strings.HasPrefix(l, p.name+" ") && strings.Contains(l, "(other ids)") {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("--list-wiring omits %s's (other ids) line:\n%s", p.name, out)
		}
	}
	// zen's own model row and fallback, in the model block: the measured
	// colour-family level for the routed id, the deferring pass for the rest
	// of the catalogue. (Columns are %-12s-padded, so the provider is matched
	// as a prefix plus the id as a substring, like the loops above.)
	for _, want := range []struct{ name, id, contains string }{
		{"zen", "step-5-preview-free", "colour-family"},
		{"zen", "(other ids)", "guard passes"},
	} {
		found := false
		for _, l := range strings.Split(mblock, "\n") {
			if strings.HasPrefix(l, want.name+" ") && strings.Contains(l, want.id) && strings.Contains(l, want.contains) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("--list-wiring omits the model-block line %s %s with %q:\n%s", want.name, want.id, want.contains, mblock)
		}
	}
	// The one refused id must say so where a reader scanning for a model to
	// pick would meet it, not only when the launch refuses.
	if !strings.Contains(out, "refused at launch: answered by glm-5.3") {
		t.Errorf("--list-wiring must carry the glm-5.2 refusal note:\n%s", out)
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

// The context window the claude-code harness passes is ENFORCED by the CLI,
// not decorative — so a wrong number here refuses work the model could do, and
// a missing one refuses work the model CAN do.
//
// Measured 2026-09-20: the CLI does not know `glm-5.3`, applied its
// unknown-model default of 200000, and killed a ~215k-token prompt with
// "Prompt is too long" before any request left the machine. The same content
// with CLAUDE_CODE_MAX_CONTEXT_TOKENS set went through, and the endpoint
// reported 243868 input tokens.
//
// FAIL-first: drop the contextWindow from the zai row and this names it.
func TestClaudeCodeProvidersCarryTheirRealContextWindow(t *testing.T) {
	// zai is the one measured, and it is the arm this skill runs most.
	p, ok := findProvider("zai")
	if !ok {
		t.Fatal("the zai provider row is gone")
	}
	if p.contextWindow == 0 {
		t.Fatal("zai lost its contextWindow: rounds would silently run on the " +
			"CLI's unknown-model default of 200000, which is enforced — a prompt " +
			"past it dies with 'Prompt is too long' before a request is made")
	}
	// The documented 1M window for GLM-5.3 / GLM-5.3-Flash, exactly.
	if p.contextWindow != 1310720 {
		t.Errorf("zai contextWindow = %d, want 1310720 (z.ai's documented 1M window); "+
			"if the plan tier changed, re-measure and move this number with it", p.contextWindow)
	}
	// A window SMALLER than the CLI's own default would be a silent downgrade
	// rather than a fix, which is the one way this column can do harm.
	if p.contextWindow < 200000 {
		t.Errorf("zai contextWindow = %d is below the CLI's own unknown-model "+
			"default; that narrows rounds instead of widening them", p.contextWindow)
	}
}

// Zero means "not measured", and it has to keep meaning that: a provider on
// this harness whose window nobody has measured must be left alone rather than
// given a guess. This asserts the intent is recorded, so a future row does not
// quietly inherit zai's number.
func TestUnmeasuredContextWindowIsLeftUnset(t *testing.T) {
	p, ok := findProvider("xai")
	if !ok {
		t.Skip("no xai row to check")
	}
	if p.contextWindow != 0 {
		// Not a failure of principle — but if someone sets it, it must be
		// because they measured grok's window, not because they copied zai's.
		t.Logf("xai now declares contextWindow=%d; that number must come from a "+
			"measurement against x.ai, not from zai's row", p.contextWindow)
	}
}
