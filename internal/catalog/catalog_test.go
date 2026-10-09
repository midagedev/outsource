package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// No test here touches the network or runs opencode: OpenRouter is an
// httptest server fed with testdata/ (trimmed from the real responses of
// 2026-10-09, plus fixture/… rows for boundaries the live listing does not
// show), and opencode is a fake runner fed with testdata/zen-models.txt.

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// fakeOpenRouter serves the three public paths and checks every request for
// the two things a catalogue request must never get wrong: no credential,
// and a User-Agent naming outsource.
type fakeOpenRouter struct {
	t   *testing.T
	srv *httptest.Server

	mu            sync.Mutex
	hits          map[string]int
	headers       []http.Header
	modelsStatus  int // 0 = 200
	modelsBody    []byte
	listStatus    int
	listBody      []byte
	endpoints     map[string][]string // id exactly as listed → provider names
	failEndpoints map[string]bool
}

func newFakeOpenRouter(t *testing.T) *fakeOpenRouter {
	f := &fakeOpenRouter{
		t:          t,
		hits:       map[string]int{},
		modelsBody: fixture(t, "openrouter-models.json"),
		listBody:   fixture(t, "openrouter-providers.json"),
		endpoints: map[string][]string{
			"nvidia/nemotron-3-ultra-550b-a55b:free": {"Nvidia"},
			// The canonical slug serves different, paid providers (measured
			// 2026-10-09). Asking it instead of the listed :free id would turn
			// a training verdict into a clean one.
			"nvidia/nemotron-3-ultra-550b-a55b-20260604": {"DeepInfra", "BaseTen", "BaseTen", "Venice"},
			"inclusionai/ling-3.1-flash":                 {"Novita"},
			"poolside/laguna-s-2.1:free":                 {"Poolside"},
			"google/gemma-4-31b-it:free":                 {"Google AI Studio"},
			"thinkingmachines/inkling:free":              {"Thinking Machines"},
			"unbiased/pareto":                            {"Venice"},
			"fixture/zero-decimal:free":                  {"Novita", "Nvidia"},
			"fixture/tiny-price":                         {"DeepInfra"},
			"fixture/no-price":                           {"DeepInfra"},
			"fixture/no-tools:free":                      {"Liquid"},
			"fixture/unlisted-provider:free":             {"Mystery Cloud"},
			// Routers must never be asked; if one is, it gets a clean verdict
			// so the regression shows in the verdict as well as the hit count.
			"openrouter/free": {"Novita"},
		},
		failEndpoints: map[string]bool{},
	}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeOpenRouter) serve(w http.ResponseWriter, r *http.Request) {
	if a := r.Header.Get("Authorization"); a != "" {
		f.t.Errorf("request to %s carried Authorization %q", r.URL.Path, a)
	}
	if ua := r.Header.Get("User-Agent"); !strings.Contains(ua, "outsource") {
		f.t.Errorf("request to %s carried User-Agent %q, want one naming outsource", r.URL.Path, ua)
	}
	f.mu.Lock()
	f.hits[r.URL.Path]++
	f.headers = append(f.headers, r.Header.Clone())
	modelsStatus, modelsBody := f.modelsStatus, f.modelsBody
	listStatus, listBody := f.listStatus, f.listBody
	f.mu.Unlock()

	reply := func(status int, body []byte) {
		if status != 0 && status != http.StatusOK {
			http.Error(w, "fake failure", status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
	}
	path := r.URL.Path
	switch {
	case path == modelsPath:
		reply(modelsStatus, modelsBody)
	case path == providersPath:
		reply(listStatus, listBody)
	case strings.HasPrefix(path, modelsPath+"/") && strings.HasSuffix(path, "/endpoints"):
		id := strings.TrimSuffix(strings.TrimPrefix(path, modelsPath+"/"), "/endpoints")
		f.mu.Lock()
		names, ok := f.endpoints[id]
		fail := f.failEndpoints[id]
		f.mu.Unlock()
		if fail {
			http.Error(w, "fake failure", http.StatusInternalServerError)
			return
		}
		if !ok {
			http.NotFound(w, r)
			return
		}
		eps := make([]map[string]any, 0, len(names))
		for _, n := range names {
			eps = append(eps, map[string]any{"name": n + " | " + id, "model_id": id, "provider_name": n,
				"pricing": map[string]string{"prompt": "0", "completion": "0"}})
		}
		b, _ := json.Marshal(map[string]any{"data": map[string]any{"id": id, "endpoints": eps}})
		reply(0, b)
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeOpenRouter) hitCount(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hits[path]
}

func (f *fakeOpenRouter) totalHits() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.hits {
		n += c
	}
	return n
}

func (f *fakeOpenRouter) endpointHits() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for p, c := range f.hits {
		if strings.HasSuffix(p, "/endpoints") {
			n += c
		}
	}
	return n
}

func (f *fakeOpenRouter) resetHits() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hits = map[string]int{}
}

func (f *fakeOpenRouter) set(fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn()
}

// fakeOpencode stands in for `opencode models opencode --verbose --pure`.
type fakeOpencode struct {
	mu    sync.Mutex
	calls [][]string
	out   []byte
	err   error
}

func (f *fakeOpencode) run(_ context.Context, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, append([]string(nil), args...))
	if f.err != nil {
		return nil, f.err
	}
	return f.out, nil
}

func (f *fakeOpencode) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

type env struct {
	or    *fakeOpenRouter
	oc    *fakeOpencode
	cache string
}

// newEnv is the only way a test here reaches Load: it points XDG_CACHE_HOME
// at a temp dir FIRST, so a developer's real catalogue cache can never decide
// a test's outcome, then builds the fakes.
func newEnv(t *testing.T) *env {
	t.Helper()
	cache := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache)
	return &env{
		or:    newFakeOpenRouter(t),
		oc:    &fakeOpencode{out: fixture(t, "zen-models.txt")},
		cache: cache,
	}
}

func (e *env) opts() Options {
	return Options{OpenRouterBase: e.or.srv.URL, Opencode: e.oc.run}
}

func load(t *testing.T, o Options) ([]Entry, Meta) {
	t.Helper()
	entries, meta, err := Load(context.Background(), o)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return entries, meta
}

func byID(t *testing.T, entries []Entry, provider, id string) Entry {
	t.Helper()
	for _, e := range entries {
		if e.Provider == provider && e.ID == id {
			return e
		}
	}
	t.Fatalf("no %s entry %q", provider, id)
	return Entry{}
}

func hasID(entries []Entry, provider, id string) bool {
	for _, e := range entries {
		if e.Provider == provider && e.ID == id {
			return true
		}
	}
	return false
}

// ---- clause 1: OpenRouter parse --------------------------------------------

func TestOpenRouterParse(t *testing.T) {
	entries, err := parseOpenRouterModels(fixture(t, "openrouter-models.json"))
	if err != nil {
		t.Fatal(err)
	}
	get := func(id string) Entry { return byID(t, entries, OpenRouter, id) }

	// Free is decided on the price strings: "0" and "0.0" are free.
	if e := get("nvidia/nemotron-3-ultra-550b-a55b:free"); !e.Free || !e.PriceKnown || e.PriceIn != 0 || e.PriceOut != 0 {
		t.Errorf(`"0"/"0": Free=%v PriceKnown=%v in=%v out=%v, want free`, e.Free, e.PriceKnown, e.PriceIn, e.PriceOut)
	}
	if e := get("fixture/zero-decimal:free"); !e.Free {
		t.Errorf(`"0.0"/"0.0": Free=false, want free`)
	}
	// "0.0000025" is priced, and per million it is exactly 2.5 (7.5 out).
	if e := get("unbiased/pareto"); e.Free || !e.PriceKnown || e.PriceIn != 2.5 || e.PriceOut != 7.5 {
		t.Errorf(`"0.0000025"/"0.0000075": Free=%v PriceKnown=%v in=%v out=%v, want priced 2.5/7.5`, e.Free, e.PriceKnown, e.PriceIn, e.PriceOut)
	}
	// Rounds to $0.00 per million in a table, but it is not free.
	if e := get("fixture/tiny-price"); e.Free || !e.PriceKnown || e.PriceIn != 0.001 {
		t.Errorf(`"0.000000001": Free=%v PriceKnown=%v in=%v, want priced 0.001`, e.Free, e.PriceKnown, e.PriceIn)
	}
	// A missing price is unknown, never zero.
	if e := get("fixture/no-price"); e.Free || e.PriceKnown {
		t.Errorf("missing pricing: Free=%v PriceKnown=%v, want neither", e.Free, e.PriceKnown)
	}

	// Tools from supported_parameters.
	if e := get("nvidia/nemotron-3-ultra-550b-a55b:free"); !e.Tools {
		t.Error(`supported_parameters has "tools": Tools=false`)
	}
	if e := get("fixture/no-tools:free"); e.Tools {
		t.Error(`supported_parameters lacks "tools": Tools=true`)
	}

	// Routers: under openrouter/, and the -1 price outside it.
	if e := get("openrouter/free"); !e.Router || !e.Free || e.FreeNonRouter() {
		t.Errorf("openrouter/free: Router=%v Free=%v FreeNonRouter=%v, want a free router that FreeNonRouter rejects", e.Router, e.Free, e.FreeNonRouter())
	}
	if e := get("openrouter/auto"); !e.Router || e.Free || e.PriceKnown {
		t.Errorf(`openrouter/auto ("-1"): Router=%v Free=%v PriceKnown=%v, want a router with no known price`, e.Router, e.Free, e.PriceKnown)
	}
	if e := get("nvidia/switchyard"); !e.Router || e.PriceKnown {
		t.Errorf(`nvidia/switchyard ("-1", outside openrouter/): Router=%v PriceKnown=%v, want a router with no known price`, e.Router, e.PriceKnown)
	}
	if e := get("nvidia/nemotron-3-ultra-550b-a55b:free"); e.Router || !e.FreeNonRouter() {
		t.Errorf("a pinned free id: Router=%v FreeNonRouter=%v", e.Router, e.FreeNonRouter())
	}

	// expiration_date carried; null is "".
	if e := get("poolside/laguna-s-2.1:free"); e.Expires != "2026-10-31" {
		t.Errorf("Expires=%q, want 2026-10-31", e.Expires)
	}
	if e := get("nvidia/nemotron-3-ultra-550b-a55b:free"); e.Expires != "" {
		t.Errorf("null expiration_date: Expires=%q, want empty", e.Expires)
	}

	// Context, output ceiling, modalities in the one order.
	if e := get("nvidia/nemotron-3-ultra-550b-a55b:free"); e.Context != 1000000 || e.MaxOutput != 65536 || e.Name != "NVIDIA: Nemotron 3 Ultra (free)" {
		t.Errorf("nemotron: Context=%d MaxOutput=%d Name=%q", e.Context, e.MaxOutput, e.Name)
	}
	if e := get("fixture/no-tools:free"); !reflect.DeepEqual(e.Inputs, []string{"text", "image", "video"}) || e.MaxOutput != 0 {
		t.Errorf("modalities [video text image text], null max: Inputs=%v MaxOutput=%d", e.Inputs, e.MaxOutput)
	}

	for _, bad := range []string{`{}`, `{"data": []}`, `<html>rate limited</html>`} {
		if _, err := parseOpenRouterModels([]byte(bad)); err == nil {
			t.Errorf("parse(%q): nil error, want one — an empty or foreign body is not a catalogue", bad)
		}
	}
}

// ---- clause 2: the policy conjunction --------------------------------------

func TestPolicyConjunction(t *testing.T) {
	list, err := parseProviderPolicies(fixture(t, "openrouter-providers.json"))
	if err != nil {
		t.Fatal(err)
	}
	f, tr := false, true
	list["OpenRouter Trains"] = providerPolicy{Training: &f, TrainingOpenRouter: &tr, RetainsPrompts: &f}

	cases := []struct {
		providers []string
		want      string
	}{
		{[]string{"Novita"}, PolicyNoTrainNoRetain},
		{[]string{"Novita", "Nvidia"}, PolicyTrains},
		{[]string{"Poolside"}, PolicyRetains},
		{[]string{"Google AI Studio"}, PolicyRetains},
		{[]string{"Novita", "Poolside"}, PolicyRetains},
		{[]string{"Mystery Cloud"}, PolicyUnknown},
		// Order boundaries: trains beats unknown, unknown beats a clean
		// provider, and nobody at all is not clean.
		{[]string{"Nvidia", "Mystery Cloud"}, PolicyTrains},
		{[]string{"Mystery Cloud", "Nvidia"}, PolicyTrains},
		{[]string{"Novita", "Mystery Cloud"}, PolicyUnknown},
		{nil, PolicyUnknown},
		// A listed provider whose policy leaves retention unstated.
		{[]string{"Fixture Unstated"}, PolicyUnknown},
		{[]string{"Thinking Machines"}, PolicyTrains},
		{[]string{"OpenRouter Trains"}, PolicyTrains},
	}
	for _, c := range cases {
		got, src := policyOf(c.providers, list)
		if got != c.want {
			t.Errorf("policyOf(%v) = %s (%s), want %s", c.providers, got, src, c.want)
		}
	}
	if _, src := policyOf([]string{"Google AI Studio"}, list); !strings.Contains(src, "Google AI Studio (retains 55d)") {
		t.Errorf("source %q does not name the provider and its retention", src)
	}

	// The list shape: {"data": [...]} as measured, a bare array as the spec
	// described it; an empty list is not a list.
	if l, err := parseProviderPolicies([]byte(`[{"name":"Novita","dataPolicy":{"training":false,"retainsPrompts":false}}]`)); err != nil || len(l) != 1 {
		t.Errorf("bare array: %v, %v", l, err)
	}
	for _, bad := range []string{`{"data": []}`, `[]`, `<html>`} {
		if _, err := parseProviderPolicies([]byte(bad)); err == nil {
			t.Errorf("parseProviderPolicies(%q): nil error", bad)
		}
	}
}

func TestPoliciesThroughLoad(t *testing.T) {
	e := newEnv(t)
	e.or.set(func() { e.or.failEndpoints["fixture/tiny-price"] = true })
	o := e.opts()
	o.Providers = []string{OpenRouter}
	o.PolicyFor = func(Entry) bool { return true }
	entries, meta := load(t, o)

	want := map[string]string{
		"nvidia/nemotron-3-ultra-550b-a55b:free": PolicyTrains,
		"inclusionai/ling-3.1-flash":             PolicyNoTrainNoRetain,
		"poolside/laguna-s-2.1:free":             PolicyRetains,
		"fixture/zero-decimal:free":              PolicyTrains,
		"fixture/unlisted-provider:free":         PolicyUnknown,
		"fixture/tiny-price":                     PolicyUnknown, // its endpoint list failed
		"openrouter/free":                        PolicyUnknown, // a router
		"openrouter/auto":                        PolicyUnknown,
	}
	for id, w := range want {
		if got := byID(t, entries, OpenRouter, id); got.Policy != w {
			t.Errorf("%s: policy %q (%s), want %s", id, got.Policy, got.PolicySource, w)
		}
	}
	if src := byID(t, entries, OpenRouter, "fixture/tiny-price").PolicySource; !strings.Contains(src, "fetch failed") {
		t.Errorf("failed fetch source %q does not say so", src)
	}
	if src := byID(t, entries, OpenRouter, "nvidia/nemotron-3-ultra-550b-a55b:free").PolicySource; !strings.Contains(src, "Nvidia (trains)") {
		t.Errorf("source %q does not name the training provider", src)
	}
	// The id is asked exactly as listed, :free included; the canonical slug never.
	if n := e.or.hitCount(modelsPath + "/nvidia/nemotron-3-ultra-550b-a55b:free/endpoints"); n != 1 {
		t.Errorf("endpoints of the :free id asked %d times, want 1", n)
	}
	if n := e.or.hitCount(modelsPath + "/nvidia/nemotron-3-ultra-550b-a55b-20260604/endpoints"); n != 0 {
		t.Errorf("endpoints of the canonical slug asked %d times, want 0", n)
	}
	// Routers are never asked.
	for _, r := range []string{"openrouter/free", "openrouter/auto", "nvidia/switchyard"} {
		if n := e.or.hitCount(modelsPath + "/" + r + "/endpoints"); n != 0 {
			t.Errorf("router %s's endpoints asked %d times, want 0", r, n)
		}
	}
	if meta.Endpoints.Failed != 1 || meta.PolicyList.From != FromNetwork {
		t.Errorf("meta: endpoints %+v, list %+v", meta.Endpoints, meta.PolicyList)
	}
}

func TestPolicyListUnavailableIsUnknown(t *testing.T) {
	e := newEnv(t)
	e.or.set(func() { e.or.listStatus = http.StatusBadGateway })
	o := e.opts()
	o.Providers = []string{OpenRouter}
	o.PolicyFor = Entry.FreeNonRouter
	entries, meta := load(t, o)
	for _, x := range entries {
		if !x.FreeNonRouter() {
			if x.Policy != "" {
				t.Errorf("%s: policy %q resolved for an unselected id", x.ID, x.Policy)
			}
			continue
		}
		if x.Policy != PolicyUnknown || !strings.Contains(x.PolicySource, "provider policy list unavailable") {
			t.Errorf("%s: %q (%s), want unknown for want of the list", x.ID, x.Policy, x.PolicySource)
		}
	}
	if meta.PolicyList.Loaded() || meta.PolicyList.Err == nil {
		t.Errorf("PolicyList %+v, want not loaded with its error", meta.PolicyList)
	}
	if n := e.or.endpointHits(); n != 0 {
		t.Errorf("%d endpoint requests without a provider list to judge them by", n)
	}
}

// A catalogue may fall back to a stale cache; a verdict may not. With the
// list fetch failing over an aged cached list, every verdict is unknown.
func TestStalePolicyListIssuesNoVerdict(t *testing.T) {
	e := newEnv(t)
	o := e.opts()
	o.Providers = []string{OpenRouter}
	o.PolicyFor = Entry.FreeNonRouter
	load(t, o)
	ageCache(t, OpenRouter+"-providers", 2*time.Hour)
	e.or.set(func() { e.or.listStatus = http.StatusInternalServerError })
	e.or.resetHits()

	entries, meta := load(t, o)
	if pl := meta.PolicyList; pl.From != FromCache || pl.Err == nil {
		t.Fatalf("PolicyList %+v, want the stale cache with the fetch error", pl)
	}
	for _, x := range entries {
		if !x.FreeNonRouter() {
			continue
		}
		if x.Policy != PolicyUnknown || !strings.Contains(x.PolicySource, "stale") {
			t.Errorf("%s: %q (%s), want unknown for want of a current list", x.ID, x.Policy, x.PolicySource)
		}
	}
	if n := e.or.endpointHits(); n != 0 {
		t.Errorf("%d endpoint requests with no current list to judge them by", n)
	}
}

func TestNoPolicyWithoutSelector(t *testing.T) {
	e := newEnv(t)
	entries, meta := load(t, e.opts())
	for _, x := range entries {
		if x.Policy != "" || x.PolicySource != "" {
			t.Errorf("%s/%s: policy %q without PolicyFor", x.Provider, x.ID, x.Policy)
		}
	}
	if n := e.or.hitCount(providersPath) + e.or.endpointHits(); n != 0 {
		t.Errorf("%d policy requests without PolicyFor", n)
	}
	if meta.PolicyList.Loaded() {
		t.Errorf("PolicyList %+v without PolicyFor", meta.PolicyList)
	}
}

// ---- clause 3: Zen ---------------------------------------------------------

func TestZenParse(t *testing.T) {
	body := fixture(t, "zen-models.txt")
	if body[len(body)-1] == '\n' {
		t.Fatal("fixture must end without a newline: the trailing-block case depends on it")
	}
	entries, err := parseZen(body)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, e := range entries {
		ids = append(ids, e.ID)
	}
	if want := []string{"big-pickle", "step-5-preview-free", "nemotron-3-ultra-free", "fixture-priced", "fixture-unknown-free"}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("ids %v, want %v", ids, want)
	}
	get := func(id string) Entry { return byID(t, entries, Zen, id) }

	if e := get("step-5-preview-free"); !e.Free || !e.Tools || e.Context != 1000000 || e.MaxOutput != 65536 ||
		e.Status != "active" || e.Name != "Step 5 Preview Free" || !reflect.DeepEqual(e.Inputs, []string{"text", "image", "video"}) {
		t.Errorf("step-5-preview-free: %+v", e)
	}
	if e := get("big-pickle"); !reflect.DeepEqual(e.Inputs, []string{"text"}) || !e.Tools {
		t.Errorf("big-pickle (no image): Inputs=%v Tools=%v", e.Inputs, e.Tools)
	}
	if e := get("fixture-priced"); e.Free || !e.PriceKnown || e.PriceIn != 3 || e.PriceOut != 15 || e.Tools ||
		e.Status != "beta" || !reflect.DeepEqual(e.Inputs, []string{"text", "image", "pdf"}) {
		t.Errorf("fixture-priced: %+v", e)
	}
	// The last block runs to EOF with no newline after it.
	if e := get("fixture-unknown-free"); !e.Free || e.Context != 131072 {
		t.Errorf("trailing block: %+v", e)
	}

	// Lines before the first header belong to no block.
	pre, err := parseZen(append([]byte("Refreshing models...\n\n"), body...))
	if err != nil || len(pre) != len(entries) {
		t.Errorf("with a preamble: %d entries, %v", len(pre), err)
	}
	// A missing cost is not free.
	if e, err := parseZen([]byte("opencode/x\n{\n  \"id\": \"x\",\n  \"limit\": {\"context\": 10}\n}\n")); err != nil || e[0].Free || e[0].PriceKnown {
		t.Errorf("no cost: %+v, %v", e, err)
	}
	for name, bad := range map[string]string{
		"empty":       "",
		"no header":   "{\"id\": \"x\"}\n",
		"id mismatch": "opencode/x\n{\"id\": \"y\"}\n",
		"broken json": "opencode/x\n{\"id\": \"x\",\n",
	} {
		if _, err := parseZen([]byte(bad)); err == nil {
			t.Errorf("%s: nil error", name)
		}
	}
}

func TestZenPolicyTable(t *testing.T) {
	cases := map[string]string{
		"step-5-preview-free":             PolicyNoTrainNoRetain,
		"space-bunny-free":                PolicyNoTrainNoRetain,
		"longcat-2.5-preview-free":        PolicyNoTrainNoRetain,
		"jev-1.13-free":                   PolicyRetains,
		"big-pickle":                      PolicyTrains,
		"ling-3.0-flash-fin-free":         PolicyTrains,
		"nemotron-3-ultra-free":           PolicyTrains,
		"nemotron-3.5-lightning-free":     PolicyTrains,
		"muse-spark-1.3-contributor-free": PolicyTrains,
		"fixture-unknown-free":            PolicyUnknown,
	}
	for id, want := range cases {
		got, src := zenPolicy(id)
		if got != want || !strings.HasPrefix(src, "zen docs 2026-10-09") {
			t.Errorf("zenPolicy(%s) = %s (%s), want %s from the dated docs", id, got, src, want)
		}
	}

	// Through Load: the table answers, and opencode was asked exactly so.
	e := newEnv(t)
	o := e.opts()
	o.Providers = []string{Zen}
	o.PolicyFor = Entry.FreeNonRouter
	entries, meta := load(t, o)
	if p := byID(t, entries, Zen, "step-5-preview-free").Policy; p != PolicyNoTrainNoRetain {
		t.Errorf("step-5-preview-free via Load: %q", p)
	}
	if p := byID(t, entries, Zen, "fixture-unknown-free").Policy; p != PolicyUnknown {
		t.Errorf("unlisted zen id via Load: %q", p)
	}
	if p := byID(t, entries, Zen, "fixture-priced").Policy; p != "" {
		t.Errorf("priced id not selected, but policy %q", p)
	}
	// --refresh on every run since 2026-10-09 (TestZenMissRefreshesOpencode
	// says why); before that this pinned the argv without it.
	if !reflect.DeepEqual(e.oc.calls, [][]string{{"models", "opencode", "--verbose", "--pure", "--refresh"}}) {
		t.Errorf("opencode calls %v", e.oc.calls)
	}
	if meta.PolicyList.Loaded() || e.or.totalHits() != 0 {
		t.Errorf("a zen-only load reached OpenRouter: %d hits", e.or.totalHits())
	}
}

// ---- clause 4: the cache ---------------------------------------------------

// ageCache rewrites a cache file's stamp to d ago.
func ageCache(t *testing.T, name string, d time.Duration) {
	t.Helper()
	c, ok := readCache(name)
	if !ok {
		t.Fatalf("no cache %s to age", name)
	}
	c.FetchedAt = time.Now().Add(-d)
	if err := writeCache(name, c); err != nil {
		t.Fatal(err)
	}
}

func allPolicies(Entry) bool { return true }

func TestFreshCacheMakesNoRequest(t *testing.T) {
	e := newEnv(t)
	o := e.opts()
	o.PolicyFor = Entry.FreeNonRouter
	first, _ := load(t, o)
	if e.or.totalHits() == 0 || e.oc.callCount() != 1 {
		t.Fatalf("first load: %d hits, %d opencode calls — nothing was fetched", e.or.totalHits(), e.oc.callCount())
	}
	e.or.resetHits()
	second, meta := load(t, o)
	if n := e.or.totalHits(); n != 0 {
		t.Errorf("fresh cache: the server saw %d requests (%v), want 0", n, e.or.hits)
	}
	if n := e.oc.callCount(); n != 1 {
		t.Errorf("fresh cache: opencode ran %d times in all, want 1", n)
	}
	for _, p := range Providers() {
		if s := meta.Providers[p]; s.From != FromCache || s.Err != nil {
			t.Errorf("%s: %+v, want a clean cache source", p, s)
		}
	}
	if meta.PolicyList.From != FromCache || meta.Endpoints.Cache == 0 || meta.Endpoints.Network != 0 {
		t.Errorf("policy side: list %+v, endpoints %+v", meta.PolicyList, meta.Endpoints)
	}
	if !reflect.DeepEqual(first, second) {
		t.Error("the cached load differs from the fetched one")
	}
}

func TestStaleCacheIsRefetched(t *testing.T) {
	e := newEnv(t)
	load(t, e.opts())
	ageCache(t, OpenRouter, 2*time.Hour)
	ageCache(t, Zen, 2*time.Hour)
	e.or.resetHits()
	_, meta := load(t, e.opts())
	if n := e.or.hitCount(modelsPath); n != 1 {
		t.Errorf("stale openrouter cache: %d listing requests, want 1", n)
	}
	if n := e.oc.callCount(); n != 2 {
		t.Errorf("stale zen cache: opencode ran %d times in all, want 2", n)
	}
	for _, p := range Providers() {
		if s := meta.Providers[p]; s.From != FromNetwork {
			t.Errorf("%s: %+v, want network", p, s)
		}
	}
}

func TestFailedFetchFallsBackToCache(t *testing.T) {
	e := newEnv(t)
	load(t, e.opts())
	ageCache(t, OpenRouter, 2*time.Hour)
	ageCache(t, Zen, 2*time.Hour)
	e.or.set(func() { e.or.modelsStatus = http.StatusServiceUnavailable })
	e.oc.err = errors.New("fake: opencode exited 1")

	entries, meta := load(t, e.opts())
	now := time.Now()
	for _, p := range Providers() {
		s := meta.Providers[p]
		if s.From != FromCache || s.Err == nil {
			t.Errorf("%s: %+v, want the cache with the fetch error beside it", p, s)
		}
		if age := s.Age(now); age < 2*time.Hour || age > 2*time.Hour+time.Minute {
			t.Errorf("%s: age %v, want about 2h", p, age)
		}
	}
	if !hasID(entries, OpenRouter, "unbiased/pareto") || !hasID(entries, Zen, "step-5-preview-free") {
		t.Error("fallback entries missing")
	}
}

func TestBadBodyDoesNotPoisonCache(t *testing.T) {
	e := newEnv(t)
	load(t, e.opts())
	ageCache(t, OpenRouter, 2*time.Hour)
	before, _ := os.ReadFile(cachePath(OpenRouter))
	e.or.set(func() { e.or.modelsBody = []byte("<html>upstream error</html>") })

	_, meta := load(t, e.opts())
	if s := meta.Providers[OpenRouter]; s.From != FromCache || s.Err == nil {
		t.Errorf("a 200 carrying HTML: %+v, want the old cache and the parse error", s)
	}
	after, _ := os.ReadFile(cachePath(OpenRouter))
	if string(before) != string(after) {
		t.Error("an unparseable 200 overwrote the cache")
	}
}

func TestNoCacheAndFailedFetchFailsThatProviderOnly(t *testing.T) {
	e := newEnv(t)
	e.or.set(func() { e.or.modelsStatus = http.StatusInternalServerError })
	entries, meta, err := Load(context.Background(), e.opts())
	if err != nil {
		t.Fatalf("Load: %v — zen loaded, so this is not a total failure", err)
	}
	if s := meta.Providers[OpenRouter]; s.Loaded() || s.Err == nil {
		t.Errorf("openrouter: %+v, want an error and nothing loaded", s)
	}
	if s := meta.Providers[Zen]; s.From != FromNetwork {
		t.Errorf("zen: %+v, want network", s)
	}
	for _, x := range entries {
		if x.Provider != Zen {
			t.Errorf("entry from %s although it failed: %s", x.Provider, x.ID)
		}
	}
	if len(entries) == 0 {
		t.Error("zen entries missing")
	}

	// Both failing is the one total failure. A fresh env, since the load above
	// cached zen.
	e2 := newEnv(t)
	e2.or.set(func() { e2.or.modelsStatus = http.StatusInternalServerError })
	e2.oc.err = errors.New("fake: opencode exited 1")
	_, meta2, err := Load(context.Background(), e2.opts())
	if err == nil {
		t.Error("every provider failed with no cache: nil error")
	}
	for _, p := range Providers() {
		if meta2.Providers[p].Loaded() {
			t.Errorf("%s loaded from nowhere: %+v", p, meta2.Providers[p])
		}
	}
}

func TestRefreshAlwaysFetches(t *testing.T) {
	e := newEnv(t)
	o := e.opts()
	o.PolicyFor = Entry.FreeNonRouter
	load(t, o)
	e.or.resetHits()
	o.Refresh = true
	_, meta := load(t, o)
	if n := e.or.hitCount(modelsPath); n != 1 {
		t.Errorf("refresh over a fresh cache: %d listing requests, want 1", n)
	}
	if n := e.or.hitCount(providersPath); n != 1 {
		t.Errorf("refresh: %d provider-list requests, want 1", n)
	}
	if meta.Endpoints.Cache != 0 || meta.Endpoints.Network == 0 {
		t.Errorf("refresh: endpoints %+v, want every list fetched", meta.Endpoints)
	}
	last := e.oc.calls[len(e.oc.calls)-1]
	if last[len(last)-1] != "--refresh" {
		t.Errorf("refresh: opencode ran %v, want --refresh passed on", last)
	}
	for _, p := range Providers() {
		if s := meta.Providers[p]; s.From != FromNetwork {
			t.Errorf("%s: %+v, want network", p, s)
		}
	}
}

// Our zen cache missing (or stale) is the only time opencode runs, and
// opencode answers from its own models.dev cache — or, with none, from a
// snapshot built into the binary. Measured 2026-10-09: with an empty opencode
// cache it listed 7 ids, three of which models.dev marks deprecated, and
// missed step-5-preview-free, which the refreshed listing (11 ids) had. Free
// ids change weekly, and this list feeds an automatic pick, so every opencode
// run asks it to refresh: at most one models.dev fetch per FreshFor.
// FAIL-first: with --refresh passed only on Options.Refresh, the first load
// over an empty cache ran opencode without it.
func TestZenMissRefreshesOpencode(t *testing.T) {
	e := newEnv(t)
	load(t, e.opts())
	if len(e.oc.calls) != 1 {
		t.Fatalf("opencode ran %d times over an empty cache, want 1", len(e.oc.calls))
	}
	first := e.oc.calls[0]
	if first[len(first)-1] != "--refresh" {
		t.Errorf("a zen cache miss ran opencode %v, want --refresh passed on", first)
	}
	load(t, e.opts())
	if len(e.oc.calls) != 1 {
		t.Errorf("a fresh zen cache ran opencode again (%d runs)", len(e.oc.calls))
	}
}

func TestUnknownProviderRefused(t *testing.T) {
	newEnv(t)
	if _, _, err := Load(context.Background(), Options{Providers: []string{"opencode"}}); err == nil {
		t.Error(`Providers ["opencode"] (the CLI's name, not the launcher's): nil error`)
	}
	if got, err := requested([]string{Zen, OpenRouter, Zen}); err != nil || !reflect.DeepEqual(got, Providers()) {
		t.Errorf("requested: %v, %v", got, err)
	}
}

// ---- the request itself ----------------------------------------------------

func TestNoKeyIsEverSent(t *testing.T) {
	e := newEnv(t)
	const key = "sk-or-v1-must-never-leave-this-machine"
	t.Setenv("OPENROUTER_API_KEY", key)
	o := e.opts()
	o.PolicyFor = allPolicies
	load(t, o)
	if e.or.totalHits() == 0 {
		t.Fatal("no request reached the fake")
	}
	for _, h := range e.or.headers {
		for k, vs := range h {
			for _, v := range vs {
				if strings.Contains(v, key) || strings.EqualFold(k, "Authorization") || strings.EqualFold(k, "Cookie") {
					t.Errorf("header %s: %q", k, v)
				}
			}
		}
	}
}

func TestOpencodeMissingFailsZenOnly(t *testing.T) {
	e := newEnv(t)
	t.Setenv("PATH", t.TempDir())
	o := e.opts()
	o.Opencode = nil // the real runner, which must find no opencode
	entries, meta, err := Load(context.Background(), o)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if s := meta.Providers[Zen]; !errors.Is(s.Err, errOpencodeMissing) || s.Loaded() {
		t.Errorf("zen: %+v, want unavailable for want of opencode", s)
	}
	if s := meta.Providers[OpenRouter]; s.From != FromNetwork || !hasID(entries, OpenRouter, "unbiased/pareto") {
		t.Errorf("openrouter: %+v", s)
	}
}

// ---- clause 6: isolation ---------------------------------------------------

func TestCacheDir(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", xdg)
	if got, want := CacheDir(), filepath.Join(xdg, "outsource", "catalog"); got != want {
		t.Errorf("CacheDir() = %s, want %s", got, want)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", "")
	if got, want := CacheDir(), filepath.Join(home, ".cache", "outsource", "catalog"); got != want {
		t.Errorf("empty XDG_CACHE_HOME: CacheDir() = %s, want %s", got, want)
	}
}

func TestEndpointsURLKeepsTheListedID(t *testing.T) {
	for id, want := range map[string]string{
		"nvidia/nemotron-3-ultra-550b-a55b:free": "B/api/v1/models/nvidia/nemotron-3-ultra-550b-a55b:free/endpoints",
		"inclusionai/ling-3.1-flash":             "B/api/v1/models/inclusionai/ling-3.1-flash/endpoints",
		"odd/a b?c":                              "B/api/v1/models/odd/a%20b%3Fc/endpoints",
	} {
		if got := endpointsURL("B", id); got != want {
			t.Errorf("endpointsURL(%q) = %s, want %s", id, got, want)
		}
	}
}

func ExampleEntry_FreeNonRouter() {
	fmt.Println(Entry{Free: true, Router: true}.FreeNonRouter(), Entry{Free: true}.FreeNonRouter())
	// Output: false true
}
