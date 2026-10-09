package catalog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// OpenRouter's three public endpoints. None takes a key, and none is ever
// sent one: the requests carry a User-Agent and nothing else.
//
// The provider policy list is NOT a documented API. It is the data behind
// OpenRouter's documented Provider Logging table
// (https://openrouter.ai/docs/guides/privacy/provider-logging), served by its
// own frontend, and it may change shape without notice — which is why a
// failure to read it turns every verdict that needs it into unknown rather
// than into a guess. There is no per-endpoint policy in the public endpoints
// API (checked on 14 free ids, 2026-10-09), so the policy is per provider.
const (
	defaultOpenRouterBase = "https://openrouter.ai"
	modelsPath            = "/api/v1/models"
	providersPath         = "/api/frontend/v1/all-providers"
	userAgent             = "outsource-catalog (+https://github.com/midagedev/outsource)"

	// maxBody bounds one response. The model listing measured 782136 bytes
	// on 2026-10-09; forty times that is a server gone wrong, not a catalogue.
	maxBody = 32 << 20

	// endpointWorkers bounds the endpoint-list requests in flight at once.
	endpointWorkers = 8
)

// httpClient is the one client every catalogue request goes through.
var httpClient = &http.Client{Timeout: 20 * time.Second}

func openRouterBase(opts Options) string {
	if opts.OpenRouterBase != "" {
		return strings.TrimRight(opts.OpenRouterBase, "/")
	}
	return defaultOpenRouterBase
}

// get fetches one public URL. Any status but 200 is an error: an error page
// must never be parsed, let alone cached, as a catalogue.
func get(ctx context.Context, u string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", u, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: HTTP %d", u, resp.StatusCode)
	}
	if len(body) > maxBody {
		return nil, fmt.Errorf("GET %s: response larger than %d bytes", u, maxBody)
	}
	return body, nil
}

func loadOpenRouter(ctx context.Context, opts Options) ([]Entry, Source) {
	u := openRouterBase(opts) + modelsPath
	return loadCached(OpenRouter, u, opts.Refresh,
		func() ([]byte, error) { return get(ctx, u) },
		parseOpenRouterModels)
}

// orModel is the part of one /api/v1/models entry this package reads.
// Pointers mark fields whose absence must stay distinguishable from zero.
type orModel struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	ContextLength *float64 `json:"context_length"`
	Architecture  struct {
		InputModalities []string `json:"input_modalities"`
	} `json:"architecture"`
	Pricing struct {
		Prompt     *string `json:"prompt"`
		Completion *string `json:"completion"`
	} `json:"pricing"`
	TopProvider struct {
		MaxCompletionTokens *float64 `json:"max_completion_tokens"`
	} `json:"top_provider"`
	SupportedParameters []string `json:"supported_parameters"`
	ExpirationDate      *string  `json:"expiration_date"`
}

func parseOpenRouterModels(body []byte) ([]Entry, error) {
	var doc struct {
		Data *[]orModel `json:"data"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("openrouter model listing: %w", err)
	}
	if doc.Data == nil || len(*doc.Data) == 0 {
		return nil, errors.New("openrouter model listing: no models in data")
	}
	out := make([]Entry, 0, len(*doc.Data))
	for _, m := range *doc.Data {
		if m.ID == "" {
			continue
		}
		in, out1 := parsePrice(m.Pricing.Prompt), parsePrice(m.Pricing.Completion)
		e := Entry{
			Provider: OpenRouter,
			ID:       m.ID,
			Name:     m.Name,
			Context:  intOf(m.ContextLength),
			// top_provider is the endpoint OpenRouter routes to first; it is
			// the only output ceiling the listing states.
			MaxOutput:  intOf(m.TopProvider.MaxCompletionTokens),
			PriceKnown: in.known && out1.known,
			Tools:      contains(m.SupportedParameters, "tools"),
			Inputs:     normInputs(m.Architecture.InputModalities),
			Router:     isRouter(m.ID, in, out1),
		}
		if e.PriceKnown {
			e.PriceIn, e.PriceOut = in.perMillion, out1.perMillion
			e.Free = in.zero && out1.zero
		}
		if m.ExpirationDate != nil {
			e.Expires = *m.ExpirationDate
		}
		out = append(out, e)
	}
	return out, nil
}

// isRouter marks the ids whose answering model is picked per request.
//
// Ids under openrouter/ are OpenRouter's own routers (openrouter/auto,
// openrouter/free, openrouter/fusion, …). A negative price is the other mark:
// measured 2026-10-09, exactly seven ids carried "-1" for both prices —
// openrouter/auto, auto-beta, fusion, pareto-code and bodybuilder, plus
// typesafe/jev-router and nvidia/switchyard, which sit outside openrouter/
// and describe themselves as picking or switching between models. A -1 price
// means "whatever the picked model costs", which is the router property
// itself.
func isRouter(id string, in, out price) bool {
	return strings.HasPrefix(id, "openrouter/") || in.negative || out.negative
}

// price is one parsed OpenRouter price string.
type price struct {
	known      bool    // a non-negative decimal was listed
	zero       bool    // ... and it is exactly zero, decided on the string, not on a float
	negative   bool    // OpenRouter's -1: priced per request by a router
	perMillion float64 // USD per million tokens when known
}

// parsePrice reads one price: a decimal string in USD per token ("0",
// "0.0", "0.0000025"). Exact rational arithmetic decides zero-ness and does
// the per-million shift, so "0.000000001" stays priced (it renders as $0.00
// per million, but it is not free) and "0.0000025" becomes exactly 2.5. A
// missing or empty field, or anything that does not parse, is unknown —
// never zero.
func parsePrice(s *string) price {
	if s == nil || strings.TrimSpace(*s) == "" {
		return price{}
	}
	r, ok := new(big.Rat).SetString(strings.TrimSpace(*s))
	if !ok {
		return price{}
	}
	if r.Sign() < 0 {
		return price{negative: true}
	}
	pm, _ := new(big.Rat).Mul(r, big.NewRat(1_000_000, 1)).Float64()
	return price{known: true, zero: r.Sign() == 0, perMillion: pm}
}

func intOf(f *float64) int {
	if f == nil || *f < 0 {
		return 0
	}
	return int(*f)
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// ---- data policy -----------------------------------------------------------

// parseProviderPolicies reads the all-providers list into name → policy. The
// response measured on 2026-10-09 is {"data": [...]} (92 entries); a bare
// array is accepted too, since this is an undocumented frontend API and the
// array is what the entries themselves are.
func parseProviderPolicies(body []byte) (map[string]providerPolicy, error) {
	type row struct {
		Name       string `json:"name"`
		DataPolicy *struct {
			Training           *bool    `json:"training"`
			TrainingOpenRouter *bool    `json:"trainingOpenRouter"`
			RetainsPrompts     *bool    `json:"retainsPrompts"`
			RetentionDays      *float64 `json:"retentionDays"`
		} `json:"dataPolicy"`
	}
	var rows []row
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		if err := json.Unmarshal(trimmed, &rows); err != nil {
			return nil, fmt.Errorf("openrouter provider list: %w", err)
		}
	} else {
		var doc struct {
			Data *[]row `json:"data"`
		}
		if err := json.Unmarshal(trimmed, &doc); err != nil {
			return nil, fmt.Errorf("openrouter provider list: %w", err)
		}
		if doc.Data != nil {
			rows = *doc.Data
		}
	}
	if len(rows) == 0 {
		return nil, errors.New("openrouter provider list: no providers")
	}
	out := make(map[string]providerPolicy, len(rows))
	for _, r := range rows {
		if r.Name == "" {
			continue
		}
		p := providerPolicy{}
		if r.DataPolicy != nil {
			p = providerPolicy{
				Training:           r.DataPolicy.Training,
				TrainingOpenRouter: r.DataPolicy.TrainingOpenRouter,
				RetainsPrompts:     r.DataPolicy.RetainsPrompts,
				RetentionDays:      r.DataPolicy.RetentionDays,
			}
		}
		out[r.Name] = p
	}
	return out, nil
}

// parseEndpoints reads /api/v1/models/<id>/endpoints into the provider
// names that serve the id, in the listing's order, without duplicates. An
// empty list is a valid answer (policyOf makes it unknown); a response with
// no data object is not.
func parseEndpoints(body []byte) ([]string, error) {
	var doc struct {
		Data *struct {
			Endpoints []struct {
				ProviderName string `json:"provider_name"`
			} `json:"endpoints"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("endpoint list: %w", err)
	}
	if doc.Data == nil {
		return nil, errors.New("endpoint list: no data")
	}
	var names []string
	for _, ep := range doc.Data.Endpoints {
		if ep.ProviderName != "" && !contains(names, ep.ProviderName) {
			names = append(names, ep.ProviderName)
		}
	}
	return names, nil
}

// endpointsURL is the endpoint list of one id, queried with the id EXACTLY as
// listed. The :free suffix is part of it: measured 2026-10-09,
// nvidia/nemotron-3-ultra-550b-a55b:free lists only Nvidia at price 0, while
// the canonical slug the listing's links.details points at lists four PAID
// providers. Each path segment is escaped on its own, so the slash between
// author and slug stays a slash and ':' stays itself.
func endpointsURL(base, id string) string {
	segs := strings.Split(id, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return base + modelsPath + "/" + strings.Join(segs, "/") + "/endpoints"
}

// resolveOpenRouterPolicies gives each selected OpenRouter entry (idx into
// entries) its verdict: the provider list once, then each id's endpoint list
// (cached per id for FreshFor), then policyOf. A failed fetch of either is
// unknown for the ids it touches — never a guess, and never a stale list
// standing in for a fresh one. That includes the provider list: loadCached
// falls back to a stale copy when the fetch fails, which is right for a
// catalogue but not for a verdict, so a list that came back with an error is
// treated as no list.
func resolveOpenRouterPolicies(ctx context.Context, entries []Entry, idx []int, opts Options, meta *Meta) {
	base := openRouterBase(opts)
	listURL := base + providersPath
	list, src := loadCached(OpenRouter+"-providers", listURL, opts.Refresh,
		func() ([]byte, error) { return get(ctx, listURL) },
		parseProviderPolicies)
	meta.PolicyList = src
	if !src.Loaded() || src.Err != nil {
		reason := "provider policy list unavailable: "
		if src.Loaded() {
			reason = "provider policy list fetch failed and the cached list is stale: "
		}
		for _, i := range idx {
			entries[i].Policy = PolicyUnknown
			entries[i].PolicySource = reason + src.Err.Error()
		}
		return
	}

	cache := readEndpointCache()
	now := time.Now()
	fresh := map[string]endpointRecord{}

	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, endpointWorkers)
	for _, i := range idx {
		id := entries[i].ID
		if rec, ok := cache[id]; ok && !opts.Refresh && isFresh(rec.FetchedAt, now) {
			entries[i].Policy, entries[i].PolicySource = policyOf(rec.Providers, list)
			meta.Endpoints.Cache++
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			body, err := get(ctx, endpointsURL(base, id))
			var names []string
			if err == nil {
				names, err = parseEndpoints(body)
			}
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				entries[i].Policy = PolicyUnknown
				entries[i].PolicySource = "endpoint list fetch failed: " + err.Error()
				meta.Endpoints.Failed++
				return
			}
			entries[i].Policy, entries[i].PolicySource = policyOf(names, list)
			fresh[id] = endpointRecord{FetchedAt: now, Providers: names}
			meta.Endpoints.Network++
		}()
	}
	wg.Wait()
	if len(fresh) > 0 {
		// A failed write costs only the requests again next call: the
		// verdicts already stand on the data just fetched.
		meta.Endpoints.CacheErr = writeEndpointCache(cache, fresh, now)
	}
}
