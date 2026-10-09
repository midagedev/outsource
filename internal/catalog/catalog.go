// Package catalog is the single owner of "what a catalogue provider offers
// right now": the model listings of OpenRouter and OpenCode Zen, each id's
// data policy, and the on-disk cache that keeps both cheap to ask again.
//
// Two of the launcher's providers are catalogues whose offer changes week to
// week — free ids appear and vanish (the openrouter row in
// internal/launch/wiring.go tells that story twice over). This package reads
// them; it does not route. What this launcher MEASURED about an id (vision,
// silent remapping) lives in launch's modelTable, and `outsource models`
// (internal/launch/models_cli.go) joins the two. The dependency points one
// way on purpose: catalog imports nothing from launch, so a later round that
// picks a free id or pre-flights a launch can use it from anywhere.
//
// The provider names here are the launcher's (zen, not opencode).
// models_cli_test.go asserts they still name providerTable rows, and that
// ZenQualifier still equals launch's qualifierOf for zen.
package catalog

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
)

// Launcher provider names of the two catalogues.
const (
	OpenRouter = "openrouter"
	Zen        = "zen"

	// ZenQualifier is the provider id opencode's own CLI uses for OpenCode
	// Zen: `opencode models opencode` lists ids as opencode/<id>. It mirrors
	// the zen row's qualifier column in launch's providerTable.
	ZenQualifier = "opencode"
)

// Providers is every catalogue provider, in the order Load reports them.
func Providers() []string { return []string{OpenRouter, Zen} }

// Data policies, the one vocabulary every verdict uses. Unknown is a verdict
// in its own right, never a gap to fill with a guess.
const (
	PolicyNoTrainNoRetain = "no-train-no-retain"
	PolicyRetains         = "retains"
	PolicyTrains          = "trains"
	PolicyUnknown         = "unknown"
)

// Where a Source's data came from.
const (
	FromNetwork = "network"
	FromCache   = "cache"
)

// FreshFor is how long cached data is used without asking again. It covers
// the catalogues, OpenRouter's provider policy list and each id's endpoint
// list alike.
const FreshFor = time.Hour

// Entry is one id a catalogue offers.
type Entry struct {
	Provider string // OpenRouter or Zen
	// ID is the bare id as the provider's default harness takes it:
	// nvidia/…:free for openrouter (the :free suffix is part of it — it names
	// a different endpoint set than the slug without it), step-5-preview-free
	// for zen.
	ID        string
	Name      string
	Context   int // input window in tokens; 0 when the catalogue does not say
	MaxOutput int // 0 when the catalogue does not say

	// Free is true only when the catalogue lists both prices and both are
	// exactly zero. A missing, empty or negative price is never free.
	Free bool
	// PriceKnown is false when the catalogue gives no fixed per-token price:
	// a missing field, or OpenRouter's "-1", which its routers carry because
	// they bill whatever model they pick. PriceIn/PriceOut are then 0 and
	// must not be read as a price — a caller sorting by price would otherwise
	// rank every router as the cheapest id on the list.
	PriceKnown bool
	PriceIn    float64 // USD per million input tokens
	PriceOut   float64 // USD per million output tokens

	Tools  bool     // the id accepts tool calls
	Inputs []string // input modalities, in inputOrder

	// Router is true for an id that picks a different model per request, so
	// the launcher's identity check cannot pin it (see isRouter).
	Router  bool
	Expires string // when the listing ends (OpenRouter's expiration_date); "" when none
	Status  string // the catalogue's own status word (zen); "" when it has none

	// Policy is one of the Policy* constants, or "" when no policy was
	// resolved for this id (Options.PolicyFor said no).
	Policy string
	// PolicySource names where the verdict came from: the OpenRouter
	// providers behind the id with each one's own verdict, or the dated zen
	// docs table, or why the verdict is unknown.
	PolicySource string
}

// FreeNonRouter is "free, and one model answers": what a caller choosing a
// free id to launch wants. openrouter/free is listed at price 0 and is a
// router — free to call, but which model answers is decided per request, so
// Free alone would let an automatic pick land on it.
func (e Entry) FreeNonRouter() bool { return e.Free && !e.Router }

// Options steers Load. The zero value loads every catalogue, uses fresh
// caches, and resolves no policy.
type Options struct {
	// Providers names which catalogues to load; empty means all of them.
	Providers []string
	// Refresh fetches even over a fresh cache — catalogues, the provider
	// policy list, endpoint lists — and asks opencode to refresh its own
	// (--refresh). A failed catalogue fetch still falls back to the cache,
	// as it does without Refresh; a failed policy fetch is still unknown.
	Refresh bool
	// PolicyFor selects the entries whose data policy to resolve; nil
	// resolves none. On OpenRouter every selected non-router id costs one
	// request (its endpoint list, cached for FreshFor), which is why this is
	// a selector rather than a switch: `outsource models --free` asks for
	// the ids it will print, a launch pre-flight for the one it will run.
	PolicyFor func(Entry) bool

	// OpenRouterBase replaces https://openrouter.ai, for tests.
	OpenRouterBase string
	// Opencode replaces running the opencode CLI, for tests. It receives the
	// arguments after the binary name and returns stdout only.
	Opencode func(ctx context.Context, args ...string) ([]byte, error)
}

// Source says where one piece of catalogue data came from.
type Source struct {
	// From is FromNetwork or FromCache, or "" when nothing could be loaded.
	From string
	// FetchedAt is when the data in use was fetched; zero when nothing loaded.
	FetchedAt time.Time
	// Err is the failed fetch, if any. With From == FromCache it is the
	// reason a stale cache is in use; with From == "" it is why nothing is.
	Err error
	// CacheErr is a failure to write the cache after a good fetch. The data
	// is still good; the next call will simply fetch again.
	CacheErr error
}

// Loaded reports whether this source produced data.
func (s Source) Loaded() bool { return s.From != "" }

// Age is how old the data in use is at now; zero when nothing loaded.
func (s Source) Age(now time.Time) time.Duration {
	if s.FetchedAt.IsZero() {
		return 0
	}
	return now.Sub(s.FetchedAt)
}

// EndpointStats counts where the OpenRouter endpoint lists behind the policy
// verdicts came from.
type EndpointStats struct {
	Network, Cache, Failed int
	// CacheErr is a failure to write the fetched lists to the cache.
	CacheErr error
}

// Meta is how Load got what it returned.
type Meta struct {
	// Providers holds one Source per requested catalogue.
	Providers map[string]Source
	// PolicyList is OpenRouter's provider data-policy list; zero when no
	// OpenRouter id needed a policy.
	PolicyList Source
	// Endpoints counts the per-id endpoint lists consulted for those
	// policies.
	Endpoints EndpointStats
}

// Load reads the requested catalogues — each from a fresh cache, else the
// network (or opencode), else a stale cache — and resolves data policies for
// the entries opts.PolicyFor selects. One catalogue failing never fails the
// other: its Meta source carries the error and its entries are simply absent.
//
// Entries come in Providers() order, each catalogue in its listing's order.
// The error is non-nil only for an unknown provider name, or when no
// requested catalogue loaded at all (then it joins every provider's error).
func Load(ctx context.Context, opts Options) ([]Entry, Meta, error) {
	want, err := requested(opts.Providers)
	if err != nil {
		return nil, Meta{}, err
	}
	meta := Meta{Providers: map[string]Source{}}

	type result struct {
		entries []Entry
		src     Source
	}
	results := make([]result, len(want))
	var wg sync.WaitGroup
	for i, p := range want {
		wg.Add(1)
		go func() {
			defer wg.Done()
			switch p {
			case OpenRouter:
				results[i].entries, results[i].src = loadOpenRouter(ctx, opts)
			case Zen:
				results[i].entries, results[i].src = loadZen(ctx, opts)
			}
		}()
	}
	wg.Wait()

	var all []Entry
	var errs []error
	for i, p := range want {
		meta.Providers[p] = results[i].src
		if !results[i].src.Loaded() {
			errs = append(errs, fmt.Errorf("%s: %w", p, results[i].src.Err))
		}
		all = append(all, results[i].entries...)
	}
	if opts.PolicyFor != nil {
		resolvePolicies(ctx, all, opts, &meta)
	}
	if len(errs) == len(want) {
		return all, meta, errors.Join(errs...)
	}
	return all, meta, nil
}

// requested resolves Options.Providers to a known, de-duplicated list in
// Providers() order.
func requested(names []string) ([]string, error) {
	if len(names) == 0 {
		return Providers(), nil
	}
	asked := map[string]bool{}
	for _, n := range names {
		known := false
		for _, p := range Providers() {
			if n == p {
				known = true
			}
		}
		if !known {
			return nil, fmt.Errorf("catalog: unknown provider %q (catalogues: %s)", n, strings.Join(Providers(), ", "))
		}
		asked[n] = true
	}
	var out []string
	for _, p := range Providers() {
		if asked[p] {
			out = append(out, p)
		}
	}
	return out, nil
}

// resolvePolicies fills Policy and PolicySource on the selected entries.
func resolvePolicies(ctx context.Context, entries []Entry, opts Options, meta *Meta) {
	var orIdx []int
	for i := range entries {
		e := &entries[i]
		if !opts.PolicyFor(*e) {
			continue
		}
		switch {
		case e.Router:
			// A router's policy is whichever model it picks for a request,
			// so no endpoint list can answer for it.
			e.Policy, e.PolicySource = PolicyUnknown, "router: the model is picked per request"
		case e.Provider == Zen:
			e.Policy, e.PolicySource = zenPolicy(e.ID)
		case e.Provider == OpenRouter:
			orIdx = append(orIdx, i)
		}
	}
	if len(orIdx) > 0 {
		resolveOpenRouterPolicies(ctx, entries, orIdx, opts, meta)
	}
}

// providerPolicy is one OpenRouter provider's row of the data-policy list.
// A nil field is a fact the list did not state.
type providerPolicy struct {
	Training           *bool
	TrainingOpenRouter *bool
	RetainsPrompts     *bool
	RetentionDays      *float64
}

// policyOf is the policy conjunction, and the only place it is decided. An id
// may be served by any of its endpoints' providers, so the verdict must hold
// for every one of them:
//
//   - no endpoint listed → unknown (the conjunction over nobody is not a
//     clean bill of health);
//   - any provider trains → trains, even when another is unknown: one known
//     training provider already rules the id out;
//   - else any provider missing from the list, or with a policy the list
//     leaves unstated → unknown;
//   - else any provider retains prompts → retains;
//   - else → no-train-no-retain.
//
// "Trains" counts trainingOpenRouter as well as training: the list carries
// both, and data trained on by OpenRouter is trained on all the same
// (measured 2026-10-09: Thinking Machines is the one provider with it set).
func policyOf(providers []string, list map[string]providerPolicy) (verdict, source string) {
	if len(providers) == 0 {
		return PolicyUnknown, "no endpoints listed"
	}
	var trains, unknown, retains bool
	parts := make([]string, 0, len(providers))
	for _, name := range providers {
		p, ok := list[name]
		switch {
		case !ok:
			unknown = true
			parts = append(parts, name+" (not in provider list)")
		case isTrue(p.Training) || isTrue(p.TrainingOpenRouter):
			trains = true
			parts = append(parts, name+" (trains)")
		case p.Training == nil || p.RetainsPrompts == nil:
			unknown = true
			parts = append(parts, name+" (policy not stated)")
		case *p.RetainsPrompts:
			retains = true
			if p.RetentionDays != nil {
				parts = append(parts, fmt.Sprintf("%s (retains %gd)", name, *p.RetentionDays))
			} else {
				parts = append(parts, name+" (retains)")
			}
		default:
			parts = append(parts, name+" (no-train-no-retain)")
		}
	}
	source = "providers: " + strings.Join(parts, ", ")
	switch {
	case trains:
		return PolicyTrains, source
	case unknown:
		return PolicyUnknown, source
	case retains:
		return PolicyRetains, source
	default:
		return PolicyNoTrainNoRetain, source
	}
}

func isTrue(b *bool) bool { return b != nil && *b }

// inputOrder is the order Inputs is reported in, whatever order (or map) the
// catalogue used, so the same modalities read the same on both providers and
// on every run.
var inputOrder = []string{"text", "image", "file", "pdf", "audio", "video"}

// normInputs de-duplicates modalities and orders them by inputOrder, with any
// modality not named there after them, alphabetically.
func normInputs(in []string) []string {
	seen := map[string]bool{}
	for _, m := range in {
		if m != "" {
			seen[m] = true
		}
	}
	out := make([]string, 0, len(seen))
	for _, m := range inputOrder {
		if seen[m] {
			out = append(out, m)
			delete(seen, m)
		}
	}
	var rest []string
	for m := range seen {
		rest = append(rest, m)
	}
	slices.Sort(rest)
	return append(out, rest...)
}
