package launch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/midagedev/outsource/internal/catalog"
	"github.com/midagedev/outsource/internal/human"
)

// `outsource models` lives here, not in internal/catalog, because it joins
// the catalogue with this launcher's own tables — modelTable's measured
// vision, the provider row's default model and default harness — and the
// catalog package must not import launch.

// loadCatalog is ModelsMain's call into the catalog package, a variable so a
// test points it at an httptest server and a fake opencode.
var loadCatalog = catalog.Load

const modelsUsage = "usage: outsource models [--provider openrouter|zen] [--free] [--tools] [--policy] [--refresh] [--json]"

// modelsHelp describes each flag by what ModelsMain and catalog.Load do with
// it; keep it in step with both.
const modelsHelp = `
What the catalogue providers offer right now, joined with what this launcher
measured. Catalogues are cached for an hour under
${XDG_CACHE_HOME:-~/.cache}/outsource/catalog.

  --provider P  only this catalogue (openrouter or zen); default both
  --free        only ids with both prices zero that are not routers
  --tools       only ids that accept tool calls
  --policy      resolve each listed id's data policy (on with --free; on
                openrouter it costs one request per id, cached for an hour)
  --refresh     fetch again even when the cache is fresh (a failed fetch
                still falls back to the cached catalogue)
  --json        print JSON instead of the table

Exit 0 when at least one catalogue loaded, 1 when none did, 64 on usage.`

// ModelsMain prints the catalogue table (or --json) for the catalogue
// providers. One provider failing does not fail the command: its footer line
// says why, and only when nothing loaded at all does it exit 1.
func ModelsMain(args []string, stdout, stderr io.Writer) int {
	var provs []string
	var free, tools, policy, refresh, asJSON bool
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--provider":
			if i+1 >= len(args) {
				fmt.Fprintf(stderr, "models: --provider needs a value\n%s\n", modelsUsage)
				return ExitUsage
			}
			i++
			if !isCatalogue(args[i]) {
				fmt.Fprintf(stderr, "models: --provider %s is not a catalogue (catalogues: %s)\n", args[i], strings.Join(catalog.Providers(), ", "))
				return ExitUsage
			}
			provs = append(provs, args[i])
		case "--free":
			free = true
		case "--tools":
			tools = true
		case "--policy":
			policy = true
		case "--refresh":
			refresh = true
		case "--json":
			asJSON = true
		case "-h", "--help":
			fmt.Fprintln(stdout, modelsUsage+"\n"+modelsHelp)
			return 0
		default:
			fmt.Fprintf(stderr, "models: unknown argument: %s\n%s\n", args[i], modelsUsage)
			return ExitUsage
		}
	}

	keep := func(e catalog.Entry) bool {
		if free && !e.FreeNonRouter() {
			return false
		}
		if tools && !e.Tools {
			return false
		}
		return true
	}
	opts := catalog.Options{Providers: provs, Refresh: refresh}
	// A policy costs a request per OpenRouter id, so it is resolved for the
	// rows this call will print and no others.
	if policy || free {
		opts.PolicyFor = keep
	}
	entries, meta, err := loadCatalog(context.Background(), opts)
	if meta.Providers == nil {
		// Load refused the options themselves; the checks above should
		// have caught every such case first.
		fmt.Fprintf(stderr, "models: %v\n", err)
		return 1
	}

	var rows []catalog.Entry
	total := map[string]int{}
	for _, e := range entries {
		total[e.Provider]++
		if keep(e) {
			rows = append(rows, e)
		}
	}
	sortModels(rows)

	now := time.Now()
	if asJSON {
		if err := writeModelsJSON(stdout, rows, meta, now); err != nil {
			fmt.Fprintf(stderr, "models: %v\n", err)
			return 1
		}
	} else {
		writeModelsTable(stdout, rows)
		fmt.Fprintln(stdout)
		writeModelsFooter(stdout, meta, total, now)
	}

	for _, s := range meta.Providers {
		if s.Loaded() {
			return 0
		}
	}
	fmt.Fprintln(stderr, "models: no catalogue could be loaded")
	return 1
}

func isCatalogue(name string) bool {
	for _, p := range catalog.Providers() {
		if p == name {
			return true
		}
	}
	return false
}

// providerRank orders catalogue providers as providerTable does, so this
// listing reads in the same order as --list-wiring.
func providerRank(name string) int {
	for i, n := range providerNameList() {
		if n == name {
			return i
		}
	}
	return len(providerTable)
}

// sortModels: provider in providerTable order, then free first, then the
// larger context first, then id.
func sortModels(rows []catalog.Entry) {
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if ra, rb := providerRank(a.Provider), providerRank(b.Provider); ra != rb {
			return ra < rb
		}
		if a.Free != b.Free {
			return a.Free
		}
		if a.Context != b.Context {
			return a.Context > b.Context
		}
		return a.ID < b.ID
	})
}

// measuredVision is modelTable's vision word for (provider, id), and false
// for an id this launcher has no row for.
func measuredVision(e catalog.Entry) (string, bool) {
	m, ok := findModel(e.Provider, e.ID)
	if !ok {
		return "", false
	}
	return visionWord(m.vision), true
}

func priceCell(e catalog.Entry) string {
	switch {
	case e.Free:
		return "free"
	case !e.PriceKnown:
		return "-"
	default:
		return fmt.Sprintf("$%.2f/$%.2f", e.PriceIn, e.PriceOut)
	}
}

func modelNotes(e catalog.Entry) string {
	var notes []string
	if e.Router {
		notes = append(notes, "router")
	}
	if e.Expires != "" {
		notes = append(notes, "expires "+e.Expires)
	}
	if p, ok := findProvider(e.Provider); ok && p.defaultModel != "" && p.defaultModel == e.ID {
		notes = append(notes, "default")
	}
	if e.Status != "" && e.Status != "active" {
		notes = append(notes, "status "+e.Status)
	}
	return strings.Join(notes, "; ")
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func writeModelsTable(w io.Writer, rows []catalog.Entry) {
	table := [][]string{{"PROVIDER", "MODEL", "CONTEXT", "PRICE", "TOOLS", "INPUT", "DATA", "MEASURED", "NOTES"}}
	for _, e := range rows {
		tools := "no"
		if e.Tools {
			tools = "yes"
		}
		measured, _ := measuredVision(e)
		table = append(table, []string{
			e.Provider, e.ID, contextCell(e.Context), priceCell(e), tools,
			orDash(strings.Join(e.Inputs, ",")), orDash(e.Policy), orDash(measured), modelNotes(e),
		})
	}
	width := make([]int, len(table[0]))
	for _, r := range table {
		for i, c := range r {
			width[i] = max(width[i], len(c))
		}
	}
	for _, r := range table {
		var b strings.Builder
		for i, c := range r {
			fmt.Fprintf(&b, "%-*s  ", width[i], c)
		}
		// Trailing spaces trimmed, as writeModelLine does for --list-wiring.
		fmt.Fprintln(w, strings.TrimRight(b.String(), " "))
	}
}

// writeModelsFooter says, per provider, where the rows came from and what
// went wrong: network or cache and its age, a fetch that failed under a
// stale cache, a cache that could not be written, and the policy side.
func writeModelsFooter(w io.Writer, meta catalog.Meta, total map[string]int, now time.Time) {
	for _, p := range metaProviders(meta) {
		s := meta.Providers[p]
		if !s.Loaded() {
			fmt.Fprintf(w, "%s: no catalogue — %v\n", p, s.Err)
			continue
		}
		line := fmt.Sprintf("%s: %d ids from %s (age %s)", p, total[p], s.From, ageCell(s, now))
		if s.Err != nil {
			line += fmt.Sprintf("; fetch failed, showing the cache: %v", s.Err)
		}
		if s.CacheErr != nil {
			line += fmt.Sprintf("; cache not written: %v", s.CacheErr)
		}
		fmt.Fprintln(w, line)
	}
	// A list that came back with an error issued no verdict, stale cache or
	// not (resolveOpenRouterPolicies), so the footer says unknown either way.
	pl := meta.PolicyList
	switch {
	case pl.Err != nil:
		fmt.Fprintf(w, "%s data policy: provider list unavailable, every verdict unknown — %v\n", catalog.OpenRouter, pl.Err)
	case pl.Loaded():
		line := fmt.Sprintf("%s data policy: provider list from %s (age %s); endpoint lists %d network, %d cache, %d failed",
			catalog.OpenRouter, pl.From, ageCell(pl, now), meta.Endpoints.Network, meta.Endpoints.Cache, meta.Endpoints.Failed)
		if meta.Endpoints.CacheErr != nil {
			line += fmt.Sprintf("; endpoint cache not written: %v", meta.Endpoints.CacheErr)
		}
		if pl.CacheErr != nil {
			line += fmt.Sprintf("; list cache not written: %v", pl.CacheErr)
		}
		fmt.Fprintln(w, line)
	}
}

func ageCell(s catalog.Source, now time.Time) string {
	return human.Secs(int64(s.Age(now) / time.Second))
}

// metaProviders is the requested providers in providerTable order.
func metaProviders(meta catalog.Meta) []string {
	var out []string
	for p := range meta.Providers {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return providerRank(out[i]) < providerRank(out[j]) })
	return out
}

// The --json shape is a contract: a setup pane reads it. Every key is always
// present; a value the catalogue does not state is null, never 0 or "".
type modelsJSON struct {
	GeneratedAt string                        `json:"generated_at"`
	Providers   map[string]modelsProviderJSON `json:"providers"`
	Models      []modelJSON                   `json:"models"`
}

// modelsProviderJSON: source is "network", "cache" or null (nothing
// loaded); error is the failed fetch — non-null beside "cache" when a stale
// cache stands in for it.
type modelsProviderJSON struct {
	Source    *string `json:"source"`
	FetchedAt *string `json:"fetched_at"`
	Error     *string `json:"error"`
}

type modelJSON struct {
	Provider       string   `json:"provider"`
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Context        *int     `json:"context"`
	MaxOutput      *int     `json:"max_output"`
	Free           bool     `json:"free"`
	PriceIn        *float64 `json:"price_in"`
	PriceOut       *float64 `json:"price_out"`
	Tools          bool     `json:"tools"`
	Inputs         []string `json:"inputs"`
	Router         bool     `json:"router"`
	Expires        *string  `json:"expires"`
	Status         *string  `json:"status"`
	Policy         *string  `json:"policy"`
	PolicySource   *string  `json:"policy_source"`
	MeasuredVision *string  `json:"measured_vision"`
	DefaultHarness *string  `json:"default_harness"`
}

func strOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func intOrNil(n int) *int {
	if n == 0 {
		return nil
	}
	return &n
}

func writeModelsJSON(w io.Writer, rows []catalog.Entry, meta catalog.Meta, now time.Time) error {
	doc := modelsJSON{
		GeneratedAt: now.UTC().Format(time.RFC3339),
		Providers:   map[string]modelsProviderJSON{},
		Models:      make([]modelJSON, 0, len(rows)),
	}
	for p, s := range meta.Providers {
		pj := modelsProviderJSON{Source: strOrNil(s.From)}
		if !s.FetchedAt.IsZero() {
			at := s.FetchedAt.UTC().Format(time.RFC3339)
			pj.FetchedAt = &at
		}
		if s.Err != nil {
			msg := s.Err.Error()
			pj.Error = &msg
		}
		doc.Providers[p] = pj
	}
	for _, e := range rows {
		m := modelJSON{
			Provider:     e.Provider,
			ID:           e.ID,
			Name:         e.Name,
			Context:      intOrNil(e.Context),
			MaxOutput:    intOrNil(e.MaxOutput),
			Free:         e.Free,
			Tools:        e.Tools,
			Inputs:       append([]string{}, e.Inputs...),
			Router:       e.Router,
			Expires:      strOrNil(e.Expires),
			Status:       strOrNil(e.Status),
			Policy:       strOrNil(e.Policy),
			PolicySource: strOrNil(e.PolicySource),
		}
		if e.PriceKnown {
			in, out := e.PriceIn, e.PriceOut
			m.PriceIn, m.PriceOut = &in, &out
		}
		if v, ok := measuredVision(e); ok {
			m.MeasuredVision = &v
		}
		if p, ok := findProvider(e.Provider); ok {
			m.DefaultHarness = strOrNil(p.defaultHarness)
		}
		doc.Models = append(doc.Models, m)
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(doc)
}
