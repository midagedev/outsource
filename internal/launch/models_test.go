package launch

import (
	"fmt"
	"strings"
	"testing"
)

// TestModelAxisCharacterization pins modelVision and mappedModelError exactly
// as the pre-model-table source answers them, so the modelTable restructure
// (per-id facts moved out of the provider columns) can be proven
// behaviour-preserving: this file was written and run green BEFORE wiring.go
// was edited, and must stay green and unchanged after.
//
// It deliberately calls only functions that exist on both sides of the
// restructure (findProvider, modelVision, mappedModelError) — none of the
// removed columns, no modelTable — so the same source runs unmodified before
// and after.
func TestModelAxisCharacterization(t *testing.T) {
	// The mapping override must be off, or every refusal row silently passes.
	t.Setenv("OUTSOURCE_ALLOW_MAPPED_MODEL", "")

	cases := []struct {
		provider string
		model    string
		vision   bool
		mappedOK bool
	}{
		// zai: only glm-5.3-flash is measured to see; unlisted ids (incl. the
		// default glm-5.3) answer false; glm-5.2 is the one refused id.
		{"zai", "", false, true},
		{"zai", "glm-5.3", false, true},
		{"zai", "glm-5.3-flash", true, true},
		{"zai", "zai/glm-5.3-flash", true, true},
		{"zai", "zai/glm-5.3", false, true},
		{"zai", "glm-5.2", false, false},
		{"zai", "zai/glm-5.2", false, false},
		{"zai", "glm-4.6", false, true},
		{"zai", "glm-5.3-air", false, true},
		// xai: the guard passes every id, listed or not; zai's
		// glm-5.2 mapping must not leak across rows.
		{"xai", "", true, true},
		{"xai", "grok-4.6", true, true},
		{"xai", "xai/grok-4.6", true, true},
		{"xai", "grok-9", true, true},
		{"xai", "glm-5.2", true, true},
		// openrouter: a catalogue, unlistedVision-style pass for every id.
		{"openrouter", "", true, true},
		{"openrouter", "openrouter/x/y", true, true},
		{"openrouter", "openrouter/z-ai/glm-5.3-flash", true, true},
		// muse: one known model, measured to see.
		{"muse", "", true, true},
		{"muse", "muse-spark-1.3-contributor", true, true},
		{"muse", "other", true, true},
		// agy: measured on 3.7-flash-low, applied provider-wide.
		{"agy", "", true, true},
		{"agy", "gemini-3.8-flash-high", true, true},
		{"agy", "gemini-3.7-flash-high", true, true},
		{"agy", "gemini-9", true, true},
	}
	for _, c := range cases {
		p, ok := findProvider(c.provider)
		if !ok {
			t.Fatalf("provider %s missing from providerTable", c.provider)
		}
		if got := modelVision(p, c.model); got != c.vision {
			t.Errorf("modelVision(%s, %q) = %v, want %v", c.provider, c.model, got, c.vision)
		}
		msg, ok := mappedModelError(p, c.model)
		if ok != c.mappedOK {
			t.Errorf("mappedModelError(%s, %q) ok = %v, want %v (msg: %s)",
				c.provider, c.model, ok, c.mappedOK, msg)
			continue
		}
		if c.mappedOK {
			continue
		}
		// A refusal must name the requested id, the model that answers it, and
		// the override env — the caller's next command depends on all three.
		for _, want := range []string{c.model, "silently answered by", "OUTSOURCE_ALLOW_MAPPED_MODEL"} {
			if !strings.Contains(msg, want) {
				t.Errorf("mappedModelError(%s, %q) refusal must contain %q, got: %s",
					c.provider, c.model, want, msg)
			}
		}
	}
}

// The context-window fallback rule is the point of keeping contextWindow on
// the provider row: every zai id — listed, qualified or default — must get
// 1310720 exactly as it did before the model axis existed, while a provider
// with no measured window still gets 0 (nothing set, CLI default untouched).
//
// FAIL-first: delete the fallback return in contextWindowFor (return only the
// row override) and the zai half of this fails — every zai row's override is
// 0 today, so the fallback line is the only thing carrying the number.
func TestContextWindowFallsBackToTheProvider(t *testing.T) {
	zai, ok := findProvider("zai")
	if !ok {
		t.Fatal("the zai provider row is gone")
	}
	for _, m := range []string{"glm-5.3", "glm-5.3-flash", "glm-4.6", "zai/glm-4.6", ""} {
		if got := contextWindowFor(zai, m); got != 1310720 {
			t.Errorf("contextWindowFor(zai, %q) = %d, want 1310720 (the provider fallback: an unlisted id must keep the number the whole provider was measured with)", m, got)
		}
	}
	xai, ok := findProvider("xai")
	if !ok {
		t.Fatal("the xai provider row is gone")
	}
	for _, m := range []string{"grok-4.6", ""} {
		if got := contextWindowFor(xai, m); got != 0 {
			t.Errorf("contextWindowFor(xai, %q) = %d, want 0 (unmeasured stays unset; nothing is written to the CLI's env)", m, got)
		}
	}
}

// modelTableProblems is the modelTable consistency checker, over given tables
// so a violation fixture is a broken COPY, never a live-row edit (the same
// property renderWiring has). Empty means the tables agree.
func modelTableProblems(models []model) []string {
	var probs []string
	knownProvider := map[string]bool{}
	for _, p := range providerTable {
		knownProvider[p.name] = true
	}
	seenID := map[string]bool{}
	for _, m := range models {
		if !knownProvider[m.provider] {
			probs = append(probs, fmt.Sprintf("model %s/%s names provider %q, which is not in providerTable", m.provider, m.id, m.provider))
		}
		key := m.provider + "/" + m.id
		if seenID[key] {
			probs = append(probs, fmt.Sprintf("model %s is declared twice: the second row can never win a findModel lookup", key))
		}
		seenID[key] = true
		if m.answeredBy != "" {
			if target, ok := findModelRow(models, m.provider, m.answeredBy); ok && target.answeredBy != "" {
				probs = append(probs, fmt.Sprintf("model %s is answered by %s, which is itself answered by %s — a chain would refuse the only re-measurement path too", key, m.answeredBy, target.answeredBy))
			}
		}
	}
	for _, p := range providerTable {
		if p.defaultModel == "" {
			continue
		}
		row, ok := findModelRow(models, p.name, p.defaultModel)
		if !ok {
			probs = append(probs, fmt.Sprintf("provider %s defaults to %s, which has no modelTable row: the default's vision level is decided by unlistedVision, a fallback, for the one id every bare launch runs", p.name, p.defaultModel))
			continue
		}
		if row.answeredBy != "" {
			probs = append(probs, fmt.Sprintf("provider %s's default %s is a silent mapping (answered by %s): every bare --provider launch would be refused at launch", p.name, p.defaultModel, row.answeredBy))
		}
	}
	return probs
}

func findModelRow(models []model, providerName, id string) (model, bool) {
	for _, m := range models {
		if m.provider == providerName && m.id == id {
			return m, true
		}
	}
	return model{}, false
}

// TestModelTableAgrees is the recurrence gate for a model fact rotting in the
// wrong place: a row naming a provider that does not exist, the same id twice,
// a default with no row (so the guard's answer for the most-launched id of all
// silently becomes a fallback), a default that is itself a silent mapping
// (every bare launch refused), or a mapping chain (the override env is the
// only re-measurement path, and a chain refuses it too).
//
// Each clause is asserted twice: once on the live table (no problems), and
// once per violation fixture — a copy of modelTable with one row broken —
// asserting the checker names exactly that violation. The fixtures are the
// FAIL-first evidence in permanent form: a checker reduced to a no-op fails
// every fixture assertion below.
func TestModelTableAgrees(t *testing.T) {
	if probs := modelTableProblems(modelTable); len(probs) > 0 {
		for _, p := range probs {
			t.Errorf("live modelTable: %s", p)
		}
	}

	// mutate clones the live table and breaks one row, so a fixture never
	// edits a live row (the renderWiring property, applied to the checker).
	mutate := func(f func(m *model)) []model {
		out := append([]model(nil), modelTable...)
		for i := range out {
			f(&out[i])
		}
		return out
	}
	withRow := func(providerName, id string, f func(m *model)) []model {
		return mutate(func(m *model) {
			if m.provider == providerName && m.id == id {
				f(m)
			}
		})
	}

	fixtures := []struct {
		name string
		in   []model
		want string
	}{
		// Clause 1: every row's provider is a providerTable name.
		{"unknown provider", withRow("zai", "glm-5.2", func(m *model) { m.provider = "z.ai" }),
			`provider "z.ai", which is not in providerTable`},
		{"provider typo'd by hand", withRow("muse", "muse-spark-1.3-contributor", func(m *model) { m.provider = "Muse" }),
			`which is not in providerTable`},
		// Clause 2: no (provider, id) pair appears twice.
		{"duplicate id", append(append([]model(nil), modelTable...), model{provider: "zai", id: "glm-5.3"}),
			"zai/glm-5.3 is declared twice"},
		{"duplicate under a qualifier", append(append([]model(nil), modelTable...), model{provider: "xai", id: "grok-4.6", vision: visionShape}),
			"xai/grok-4.6 is declared twice"},
		// Clause 3: every non-empty defaultModel has a model row.
		{"default without a row", mutate(func(m *model) {
			if m.provider == "zai" && m.id == "glm-5.3" {
				m.id = "glm-5.3-b"
			}
		}), "defaults to glm-5.3, which has no modelTable row"},
		{"muse default without a row", mutate(func(m *model) {
			if m.provider == "muse" {
				m.id = "muse-spark-1.4-contributor"
			}
		}), "defaults to muse-spark-1.3-contributor, which has no modelTable row"},
		// Clause 4: no default is a row with answeredBy set.
		{"default is a silent mapping", withRow("zai", "glm-5.3", func(m *model) { m.answeredBy = "glm-4.6" }),
			"default glm-5.3 is a silent mapping"},
		{"agy default refused", withRow("agy", "gemini-3.8-flash-high", func(m *model) { m.answeredBy = "gemini-3.9" }),
			"is a silent mapping"},
		// Clause 5: an answeredBy target is not itself mapped (no chains).
		{"two-step chain", withRow("zai", "glm-5.3-flash", func(m *model) { m.answeredBy = "glm-5.2" }),
			"is itself answered by"},
		{"self-mapping", withRow("zai", "glm-5.2", func(m *model) { m.answeredBy = "glm-5.2" }),
			"is itself answered by"},
	}
	for _, fx := range fixtures {
		probs := modelTableProblems(fx.in)
		if len(probs) == 0 {
			t.Errorf("%s: the checker must flag this violation, got none", fx.name)
			continue
		}
		if !strings.Contains(strings.Join(probs, "\n"), fx.want) {
			t.Errorf("%s: checker must say %q, got: %s", fx.name, fx.want, strings.Join(probs, "\n"))
		}
	}
}
