package launch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/midagedev/outsource/internal/catalog"
)

// modelsEnv points `outsource models` at an httptest OpenRouter and a fake
// opencode, both fed with internal/catalog/testdata — no test here touches the
// network or runs opencode. It sets XDG_CACHE_HOME to a temp dir FIRST, so a
// test's cache is its own (TestMain's private one is shared by the package).
// The fixture replaces the seam (loadCatalog) whole, with catalog.Load over
// the fakes: TestMain's OUTSOURCE_CATALOG=off floor belongs to the seam's
// default, which a fixture stands in for.
type modelsEnv struct {
	modelsStatus int
	listStatus   int
	opencodeErr  error
}

func (m *modelsEnv) install(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	read := func(name string) []byte {
		b, err := os.ReadFile(filepath.Join("..", "catalog", "testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	models, list, zen := read("openrouter-models.json"), read("openrouter-providers.json"), read("zen-models.txt")
	endpoints := map[string][]string{
		"nvidia/nemotron-3-ultra-550b-a55b:free": {"Nvidia"},
		"inclusionai/ling-3.1-flash":             {"Novita"},
		"poolside/laguna-s-2.1:free":             {"Poolside"},
		"google/gemma-4-31b-it:free":             {"Google AI Studio"},
		"thinkingmachines/inkling:free":          {"Thinking Machines"},
		"fixture/zero-decimal:free":              {"Novita", "Nvidia"},
		"fixture/no-tools:free":                  {"Liquid"},
		"fixture/unlisted-provider:free":         {"Mystery Cloud"},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fail := func(status int) bool {
			if status != 0 {
				http.Error(w, "fake failure", status)
				return true
			}
			return false
		}
		switch p := r.URL.Path; {
		case p == "/api/v1/models":
			if !fail(m.modelsStatus) {
				w.Write(models)
			}
		case p == "/api/frontend/v1/all-providers":
			if !fail(m.listStatus) {
				w.Write(list)
			}
		case strings.HasSuffix(p, "/endpoints"):
			id := strings.TrimSuffix(strings.TrimPrefix(p, "/api/v1/models/"), "/endpoints")
			names, ok := endpoints[id]
			if !ok {
				http.NotFound(w, r)
				return
			}
			var eps []map[string]string
			for _, n := range names {
				eps = append(eps, map[string]string{"provider_name": n})
			}
			json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"id": id, "endpoints": eps}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	runner := func(context.Context, ...string) ([]byte, error) {
		if m.opencodeErr != nil {
			return nil, m.opencodeErr
		}
		return zen, nil
	}
	orig := loadCatalog
	loadCatalog = func(ctx context.Context, o catalog.Options) ([]catalog.Entry, catalog.Meta, error) {
		o.OpenRouterBase, o.Opencode = srv.URL, runner
		return catalog.Load(ctx, o)
	}
	t.Cleanup(func() { loadCatalog = orig })
}

func runModels(t *testing.T, args ...string) (rc int, stdout, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	rc = ModelsMain(args, &out, &errb)
	return rc, out.String(), errb.String()
}

// tableRows parses the table: header line first, rows until the blank line
// before the footer. Each row is its whitespace fields; NOTES may span
// several.
func tableRows(t *testing.T, out string) [][]string {
	t.Helper()
	lines := strings.Split(out, "\n")
	if len(lines) == 0 || !strings.HasPrefix(lines[0], "PROVIDER ") {
		t.Fatalf("no table header:\n%s", out)
	}
	var rows [][]string
	for _, l := range lines[1:] {
		if l == "" {
			break
		}
		rows = append(rows, strings.Fields(l))
	}
	return rows
}

func rowFor(rows [][]string, provider, id string) []string {
	for _, r := range rows {
		if len(r) >= 2 && r[0] == provider && r[1] == id {
			return r
		}
	}
	return nil
}

// ---- clause 5: the command -------------------------------------------------

func TestModelsFreeExcludesRoutersAndPriced(t *testing.T) {
	(&modelsEnv{}).install(t)
	rc, out, errOut := runModels(t, "--free")
	if rc != 0 {
		t.Fatalf("rc=%d stderr=%s", rc, errOut)
	}
	rows := tableRows(t, out)
	for _, id := range []string{"openrouter/free", "openrouter/auto", "nvidia/switchyard", "unbiased/pareto", "fixture/tiny-price", "fixture/no-price"} {
		if rowFor(rows, "openrouter", id) != nil {
			t.Errorf("--free listed %s (a router or a priced id)", id)
		}
	}
	if rowFor(rows, "zen", "fixture-priced") != nil {
		t.Error("--free listed zen's priced id")
	}
	for _, id := range []string{"nvidia/nemotron-3-ultra-550b-a55b:free", "inclusionai/ling-3.1-flash", "fixture/zero-decimal:free"} {
		r := rowFor(rows, "openrouter", id)
		if r == nil {
			t.Errorf("--free dropped free id %s", id)
			continue
		}
		// PRICE is column 3, DATA column 6: --free resolves policies by default.
		if r[3] != "free" || r[6] == "-" {
			t.Errorf("%s: PRICE %q DATA %q, want free and a resolved policy", id, r[3], r[6])
		}
	}
	if r := rowFor(rows, "zen", "step-5-preview-free"); r == nil || r[6] != catalog.PolicyNoTrainNoRetain {
		t.Errorf("zen step-5-preview-free row: %v", r)
	}

	// The same filter in JSON: nothing priced, nothing routed.
	rc, out, _ = runModels(t, "--free", "--json")
	if rc != 0 {
		t.Fatalf("--free --json rc=%d", rc)
	}
	var doc struct {
		Models []struct {
			ID     string `json:"id"`
			Free   bool   `json:"free"`
			Router bool   `json:"router"`
		} `json:"models"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Models) == 0 {
		t.Fatal("--free --json listed nothing")
	}
	for _, m := range doc.Models {
		if !m.Free || m.Router {
			t.Errorf("--free --json listed %s (free=%v router=%v)", m.ID, m.Free, m.Router)
		}
	}
}

func keysOf(m map[string]any) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sorted(s ...string) []string {
	out := append([]string(nil), s...)
	sort.Strings(out)
	return out
}

func TestModelsJSONShape(t *testing.T) {
	(&modelsEnv{}).install(t)
	rc, out, errOut := runModels(t, "--json")
	if rc != 0 {
		t.Fatalf("rc=%d stderr=%s", rc, errOut)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if got, want := keysOf(doc), sorted("generated_at", "providers", "models"); !reflect.DeepEqual(got, want) {
		t.Errorf("top-level keys %v, want %v", got, want)
	}
	if _, err := time.Parse(time.RFC3339, doc["generated_at"].(string)); err != nil {
		t.Errorf("generated_at: %v", err)
	}

	provs := doc["providers"].(map[string]any)
	if got, want := keysOf(provs), sorted(catalog.Providers()...); !reflect.DeepEqual(got, want) {
		t.Errorf("providers %v, want %v", got, want)
	}
	for name, v := range provs {
		p := v.(map[string]any)
		if got, want := keysOf(p), sorted("source", "fetched_at", "error"); !reflect.DeepEqual(got, want) {
			t.Errorf("providers.%s keys %v, want %v", name, got, want)
		}
		if p["source"] != "network" || p["error"] != nil || p["fetched_at"] == nil {
			t.Errorf("providers.%s = %v", name, p)
		}
	}

	modelKeys := sorted("provider", "id", "name", "context", "max_output", "free", "price_in", "price_out",
		"tools", "inputs", "router", "expires", "status", "policy", "policy_source", "measured_vision", "default_harness")
	models := doc["models"].([]any)
	if len(models) == 0 {
		t.Fatal("no models")
	}
	byKey := map[string]map[string]any{}
	for _, v := range models {
		m := v.(map[string]any)
		if got := keysOf(m); !reflect.DeepEqual(got, modelKeys) {
			t.Errorf("model keys %v, want %v", got, modelKeys)
		}
		prov, id := m["provider"].(string), m["id"].(string)
		byKey[prov+"/"+id] = m
		// default_harness is the provider row's, whatever that row says today.
		p, ok := findProvider(prov)
		if !ok {
			t.Fatalf("model from %s, which has no providerTable row", prov)
		}
		if m["default_harness"] != p.defaultHarness {
			t.Errorf("%s/%s default_harness %v, want the provider row's %q", prov, id, m["default_harness"], p.defaultHarness)
		}
		if _, isArr := m["inputs"].([]any); !isArr {
			t.Errorf("%s/%s inputs %v, want an array", prov, id, m["inputs"])
		}
	}

	// measured_vision is modelTable's word for an id with a row, null for one
	// without. Derived from the row rather than a literal, as default_harness
	// is: a re-measure edits modelTable, not this test.
	step, ok := byKey["zen/step-5-preview-free"]
	if !ok {
		t.Fatal("zen/step-5-preview-free missing")
	}
	row, ok := findModel("zen", "step-5-preview-free")
	if !ok {
		t.Fatal("zen/step-5-preview-free has no modelTable row")
	}
	if step["measured_vision"] != visionWord(row.vision) || step["measured_vision"] == "unmeasured" {
		t.Errorf("zen/step-5-preview-free measured_vision %v, want %q", step["measured_vision"], visionWord(row.vision))
	}
	if m := byKey["zen/big-pickle"]; m["measured_vision"] != nil {
		t.Errorf("zen/big-pickle (no row) measured_vision %v, want null", m["measured_vision"])
	}

	// Absent facts are null, never 0 or "".
	if m := byKey["openrouter/openrouter/auto"]; m["price_in"] != nil || m["price_out"] != nil || m["free"] != false || m["router"] != true {
		t.Errorf("openrouter/auto (-1 prices): %v", m)
	}
	if m := byKey["openrouter/unbiased/pareto"]; m["price_in"] != 2.5 || m["price_out"] != 7.5 {
		t.Errorf("unbiased/pareto prices: %v / %v", m["price_in"], m["price_out"])
	}
	if m := byKey["openrouter/fixture/no-tools:free"]; m["max_output"] != nil {
		t.Errorf("unstated max_output: %v, want null", m["max_output"])
	}
	if m := byKey["openrouter/poolside/laguna-s-2.1:free"]; m["expires"] != "2026-10-31" {
		t.Errorf("expires: %v", m["expires"])
	}
	// No --policy and no --free: nothing resolved, so nothing claimed.
	if m := byKey["openrouter/nvidia/nemotron-3-ultra-550b-a55b:free"]; m["policy"] != nil || m["policy_source"] != nil {
		t.Errorf("policy without --policy: %v / %v", m["policy"], m["policy_source"])
	}
}

func TestModelsMeasuredColumn(t *testing.T) {
	(&modelsEnv{}).install(t)
	rc, out, _ := runModels(t, "--provider", "zen")
	if rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	rows := tableRows(t, out)
	row, _ := findModel("zen", "step-5-preview-free")
	r := rowFor(rows, "zen", "step-5-preview-free")
	// MEASURED is column 7; NOTES from 8 on.
	if r == nil || r[7] != visionWord(row.vision) || r[7] != "colour-family" {
		t.Errorf("step-5-preview-free row %v, want MEASURED colour-family", r)
	}
	if !strings.Contains(strings.Join(r[8:], " "), "default") {
		t.Errorf("step-5-preview-free is zen's default model; NOTES %v", r[8:])
	}
	if r := rowFor(rows, "zen", "big-pickle"); r == nil || r[7] != "-" {
		t.Errorf("big-pickle row %v, want MEASURED -", r)
	}
	for _, r := range rows {
		if r[0] != "zen" {
			t.Errorf("--provider zen listed %v", r)
		}
	}
}

func TestModelsSortOrder(t *testing.T) {
	(&modelsEnv{}).install(t)
	_, out, _ := runModels(t)
	rows := tableRows(t, out)
	idx := map[string]int{}
	for i, r := range rows {
		idx[r[0]+"/"+r[1]] = i
	}
	before := func(a, b string) {
		ia, okA := idx[a]
		ib, okB := idx[b]
		if !okA || !okB || ia >= ib {
			t.Errorf("want %s (row %d) before %s (row %d)", a, ia, b, ib)
		}
	}
	// providerTable order: openrouter's row precedes zen's.
	before("openrouter/unbiased/pareto", "zen/big-pickle")
	// Free first, then the larger context.
	before("openrouter/nvidia/nemotron-3-ultra-550b-a55b:free", "openrouter/unbiased/pareto")
	before("openrouter/nvidia/nemotron-3-ultra-550b-a55b:free", "openrouter/inclusionai/ling-3.1-flash")
	before("zen/step-5-preview-free", "zen/big-pickle")
	before("zen/fixture-unknown-free", "zen/fixture-priced")
	// Equal context breaks by id.
	before("openrouter/google/gemma-4-31b-it:free", "openrouter/inclusionai/ling-3.1-flash")
}

func TestModelsExitCodes(t *testing.T) {
	t.Run("every provider failed", func(t *testing.T) {
		(&modelsEnv{modelsStatus: http.StatusInternalServerError, opencodeErr: errors.New("fake: opencode exited 1")}).install(t)
		rc, out, errOut := runModels(t)
		if rc != 1 {
			t.Errorf("rc=%d, want 1", rc)
		}
		if !strings.Contains(out, "openrouter: no catalogue") || !strings.Contains(out, "zen: no catalogue") ||
			!strings.Contains(errOut, "no catalogue could be loaded") {
			t.Errorf("footer/stderr do not say why:\n%s\n%s", out, errOut)
		}
		rc, out, _ = runModels(t, "--json")
		if rc != 1 {
			t.Errorf("--json rc=%d, want 1", rc)
		}
		var doc struct {
			Providers map[string]struct {
				Source *string `json:"source"`
				Error  *string `json:"error"`
			} `json:"providers"`
		}
		if err := json.Unmarshal([]byte(out), &doc); err != nil {
			t.Fatalf("--json on total failure is not JSON: %v", err)
		}
		for p, s := range doc.Providers {
			if s.Source != nil || s.Error == nil {
				t.Errorf("%s: source %v error %v, want null and an error", p, s.Source, s.Error)
			}
		}
	})
	t.Run("one provider failed", func(t *testing.T) {
		(&modelsEnv{opencodeErr: errors.New("fake: opencode exited 1")}).install(t)
		rc, out, _ := runModels(t)
		if rc != 0 {
			t.Errorf("rc=%d, want 0 — openrouter loaded", rc)
		}
		if !strings.Contains(out, "zen: no catalogue — fake: opencode exited 1") || !strings.Contains(out, "openrouter: 14 ids from network") {
			t.Errorf("footer:\n%s", out)
		}
	})
	t.Run("policy list down", func(t *testing.T) {
		(&modelsEnv{listStatus: http.StatusBadGateway}).install(t)
		rc, out, _ := runModels(t, "--free", "--provider", "openrouter")
		if rc != 0 {
			t.Errorf("rc=%d", rc)
		}
		if !strings.Contains(out, "openrouter data policy: provider list unavailable") {
			t.Errorf("footer:\n%s", out)
		}
		for _, r := range tableRows(t, out) {
			if r[6] != catalog.PolicyUnknown {
				t.Errorf("%s: DATA %q without a provider list, want unknown", r[1], r[6])
			}
		}
	})
	(&modelsEnv{}).install(t)
	for _, args := range [][]string{{"--bogus"}, {"--provider"}, {"--provider", "opencode"}, {"--provider", "zai"}, {"extra"}} {
		if rc, _, _ := runModels(t, args...); rc != ExitUsage {
			t.Errorf("%v: rc=%d, want %d", args, rc, ExitUsage)
		}
	}
	if rc, out, _ := runModels(t, "--help"); rc != 0 || !strings.Contains(out, "usage: outsource models") {
		t.Errorf("--help: rc=%d", rc)
	}
}

// TestCatalogueProvidersAreWired holds the catalog package's literals to the
// wiring tables, since catalog cannot import launch: every catalogue is a
// providerTable row a harness drives, and ZenQualifier is zen's qualifier.
func TestCatalogueProvidersAreWired(t *testing.T) {
	for _, name := range catalog.Providers() {
		p, ok := findProvider(name)
		if !ok {
			t.Errorf("catalogue %q has no providerTable row", name)
			continue
		}
		if len(harnessesFor(name)) == 0 {
			t.Errorf("catalogue %q has no harness", name)
		}
		if name == catalog.Zen && qualifierOf(p) != catalog.ZenQualifier {
			t.Errorf("zen's qualifier is %q, catalog.ZenQualifier %q", qualifierOf(p), catalog.ZenQualifier)
		}
		if name == catalog.OpenRouter && qualifierOf(p) != catalog.OpenRouter {
			t.Errorf("openrouter's qualifier is %q", qualifierOf(p))
		}
	}
}
