package launch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/midagedev/outsource/internal/catalog"
	"github.com/midagedev/outsource/internal/config"
)

// The launcher's use of the catalogue (internal/catalog): `--model free`
// picks an id from a catalogue provider's current offer, a named id on a
// catalogue provider is checked against that offer before the round starts,
// and the id's listed context reaches the claude-code harness's window
// (contextEnv). `outsource models --pick` (models_cli.go) prints what the
// same resolver would choose.
//
// The process that parses the caller's argv does all of it. A --detach
// child never calls the catalogue: the parent rewrites --model to the id it
// picked and hands the selector and the context over in one internal flag
// (catalogueHandoffFlag), so the child runs exactly what the parent chose
// even when the catalogue moved in between.

// ---- the catalogue seam ------------------------------------------------------

// catalogueSwitchEnv is the user-facing off switch: OUTSOURCE_CATALOG=off
// makes no catalogue request at all — for an air-gapped machine, and for
// anyone who wants no catalogue traffic. A named id then launches with a note
// and the tables' window, `--model free` refuses, and `outsource models`
// exits 1. loadCatalogUnlessOff is its one reader.
const catalogueSwitchEnv = "OUTSOURCE_CATALOG"

var errCatalogueOff = errors.New("the catalogue is switched off (" + catalogueSwitchEnv + "=off)")

// loadCatalog is the one seam every catalogue read in this package goes
// through — `outsource models`, `--model free` and the named-id pre-flight.
// Tests replace it with fixture loaders.
var loadCatalog = loadCatalogUnlessOff

// catalogNetwork is what the default seam calls once the switch allows it:
// catalog.Load, which fetches from OpenRouter and runs opencode. A variable
// only so this package's TestMain can make reaching it fatal.
var catalogNetwork = catalog.Load

func loadCatalogUnlessOff(ctx context.Context, opts catalog.Options) ([]catalog.Entry, catalog.Meta, error) {
	if strings.EqualFold(strings.TrimSpace(os.Getenv(catalogueSwitchEnv)), "off") {
		return nil, catalog.Meta{}, errCatalogueOff
	}
	return catalogNetwork(ctx, opts)
}

// catalogueSource is one provider's Source after a load: the catalogue's own
// when it reported one, else a Source whose Err says why nothing loaded (the
// switch, or Load's refusal of the options).
func catalogueSource(meta catalog.Meta, err error, provider string) catalog.Source {
	src := meta.Providers[provider]
	if !src.Loaded() && src.Err == nil {
		src.Err = err
		if src.Err == nil {
			src.Err = errors.New("the catalogue returned no source for " + provider)
		}
	}
	return src
}

// ---- --model free --------------------------------------------------------------

// freeSelector is the --model value that asks the launcher to pick a free id.
// Only this literal: openrouter/free is OpenRouter's own router id and goes
// through the named-id pre-flight, which refuses it as a router.
const freeSelector = "free"

// minFreeContext is the smallest window --model free accepts. The claude-code
// system prompt, its tool schemas and a spec take a large share of a smaller
// window before the round has read a single file.
const minFreeContext = 128000

// freeFilter is the first filter a catalogue entry fails, in the order the
// resolver applies them; keepFree passes all of them. The data policy is the
// last filter and is applied after these (it alone can be lifted).
type freeFilter int

const (
	keepFree         freeFilter = iota
	dropNotFree                 // a price is missing, non-zero or -1
	dropRouter                  // free, but a router: the identity check cannot pin it
	dropNoTools                 // every harness here drives the model through tool calls
	dropExpired                 // the listing's expiration date is today or past, or does not parse
	dropWithdrawn               // the catalogue's status word is neither empty nor active
	dropSmallContext            // Context under minFreeContext, or not stated
	dropMapped                  // modelTable says another model answers this id (answeredBy)
)

// freeDrop is the cheap filters, a–d of the pick plus the mapped-id
// exclusion. The data policy is not here: it needs a request per OpenRouter
// id, so Load resolves it only for entries this already passes.
//
// A mapped id is EXCLUDED rather than picked and then refused: the pick is
// the resolver's choice, not the caller's, and `outsource models --pick` must
// name the id a launch would really run. answeredByOf is the mapped-model
// guard's own rule, so OUTSOURCE_ALLOW_MAPPED_MODEL=1 lifts both alike.
func freeDrop(p provider, e catalog.Entry, today time.Time) freeFilter {
	switch {
	case !e.Free:
		return dropNotFree
	case e.Router:
		return dropRouter
	case !e.Tools:
		return dropNoTools
	}
	if e.Expires != "" {
		if ended, parsed := listingEnded(e.Expires, today); ended || !parsed {
			return dropExpired
		}
	}
	switch {
	case !e.Active():
		// zen marks withdrawn ids deprecated (measured 2026-10-09: hy3-free,
		// x-preview-f-free, muse-spark-1.2-contributor-free).
		return dropWithdrawn
	case e.Context < minFreeContext:
		return dropSmallContext
	case answeredByOf(p, e.ID) != "":
		return dropMapped
	}
	return keepFree
}

// listingEnded reads a catalogue's expiration date (OpenRouter's
// expiration_date, measured as YYYY-MM-DD; an RFC 3339 timestamp is read as
// its UTC date). A listing passes only with a date after today, UTC: on the
// date itself it has ended. parsed is false for a date neither form reads.
func listingEnded(expires string, today time.Time) (ended, parsed bool) {
	d, err := time.Parse("2006-01-02", expires)
	if err != nil {
		t, err := time.Parse(time.RFC3339, expires)
		if err != nil {
			return false, false
		}
		d = t.UTC()
	}
	return d.Format("2006-01-02") <= today.UTC().Format("2006-01-02"), true
}

// freeCounts is how many entries of one catalogue each filter dropped. Every
// entry is counted once, at the first filter it fails.
type freeCounts struct {
	listed                                        int
	notFree, routers, noTools, expired, withdrawn int
	smallContext, mapped, policy                  int
}

// String is the counts as the refusal and `models --pick` print them. strict
// adds how to lift the data-policy filter when it dropped anything.
func (c freeCounts) String(strict bool) string {
	s := fmt.Sprintf("%d listed; %d not free, %d routers, %d no tools, %d expired, %d withdrawn, %d under %d ctx, %d answered by another model, %d data policy",
		c.listed, c.notFree, c.routers, c.noTools, c.expired, c.withdrawn, c.smallContext, minFreeContext, c.mapped, c.policy)
	if strict && c.policy > 0 {
		s += " (allow with free.allowTraining or --allow-free-training)"
	}
	return s
}

func (c *freeCounts) count(f freeFilter) {
	switch f {
	case dropNotFree:
		c.notFree++
	case dropRouter:
		c.routers++
	case dropNoTools:
		c.noTools++
	case dropExpired:
		c.expired++
	case dropWithdrawn:
		c.withdrawn++
	case dropSmallContext:
		c.smallContext++
	case dropMapped:
		c.mapped++
	}
}

// freeRequest is one resolution's inputs: the launch's own, or `models
// --pick`'s stand-ins for them.
type freeRequest struct {
	p       provider
	harness string // the harness the pick is written for (harnessFormID)
	cwd     string // the round's --cwd, checked against free.denyPaths
	cfg     *config.Config
	// allowTraining is --allow-free-training; the config's free.allowTraining
	// is read from cfg, and either one lifts the policy filter.
	allowTraining bool
	refresh       bool
	now           time.Time
}

// freePick is what the resolver chose, and everything it counted on the way.
type freePick struct {
	entry      catalog.Entry
	model      string          // the id as the round runs it: entry.ID in the harness's form
	candidates []catalog.Entry // in rank order; entry is the first
	counts     freeCounts
	strict     bool // only no-train-no-retain qualified
	source     catalog.Source
}

// line is the one stderr line a launch prints for its pick, and the first
// line of `models --pick`.
func (f freePick) line(now time.Time) string {
	return fmt.Sprintf("outsource: --model free → %s (%s, %d ctx; %d candidates; catalogue %s %s)",
		f.model, orDefault(f.entry.Policy, catalog.PolicyUnknown), f.entry.Context, len(f.candidates), f.source.From, ageCell(f.source, now))
}

// resolveFree is `--model free`: the one resolver the launcher and `outsource
// models --pick` call. The second result is a refusal (exit 64 at both
// callers); with it empty, the pick is good.
//
// In order: the provider must be a catalogue; the round's cwd must match no
// free.denyPaths glob (checked before anything loads); the catalogue must
// load. Its entries then pass freeDrop's filters and the data policy
// (no-train-no-retain only, unless the config's free.allowTraining or
// --allow-free-training lifts it — then trains, retains and unknown pass
// too), and the survivors are ranked by rankFree.
func resolveFree(req freeRequest) (freePick, string) {
	p := req.p
	if !isCatalogue(p.name) {
		return freePick{}, fmt.Sprintf("--model free picks from a catalogue; provider %s has none (catalogues: %s)",
			p.name, strings.Join(catalog.Providers(), ", "))
	}
	if msg := denyPathRefusal("--model free", req.cwd, req.cfg); msg != "" {
		return freePick{}, msg
	}
	allow := req.allowTraining
	if v, _ := req.cfg.AllowTraining(); v {
		allow = true
	}
	today := req.now.UTC()
	entries, meta, err := loadCatalog(context.Background(), catalog.Options{
		Providers: []string{p.name},
		Refresh:   req.refresh,
		// Policies only for entries that already pass the cheap filters: on
		// OpenRouter each one costs a request (cached for an hour), and the
		// free, tool-capable, large-window ids are a handful of the listing.
		PolicyFor: func(e catalog.Entry) bool { return e.Provider == p.name && freeDrop(p, e, today) == keepFree },
	})
	src := catalogueSource(meta, err, p.name)
	if !src.Loaded() {
		return freePick{}, "--model free cannot pick: " + src.Err.Error()
	}
	pick := freePick{strict: !allow, source: src}
	for _, e := range entries {
		if e.Provider != p.name {
			continue
		}
		pick.counts.listed++
		if f := freeDrop(p, e, today); f != keepFree {
			pick.counts.count(f)
			continue
		}
		if !allow && e.Policy != catalog.PolicyNoTrainNoRetain {
			pick.counts.policy++
			continue
		}
		pick.candidates = append(pick.candidates, e)
	}
	if len(pick.candidates) == 0 {
		return pick, fmt.Sprintf("--model free: no candidate on %s — %s", p.name, pick.counts.String(pick.strict))
	}
	cfgDefault, _ := req.cfg.DefaultModel(p.name)
	rankFree(p, pick.candidates, cfgDefault)
	pick.entry = pick.candidates[0]
	pick.model = harnessFormID(p, req.harness, pick.entry.ID)
	return pick, ""
}

// rankFree orders the candidates, best first: the config's
// providers.<p>.defaultModel when it is one of them (a default that did not
// qualify is simply not here — ignored, not refused); then ids with a
// modelTable row, in table order, since a row is something this launcher
// measured; then the larger context; then the id, so the order is total.
func rankFree(p provider, cands []catalog.Entry, cfgDefault string) {
	tableRank := func(id string) int {
		for i, m := range modelTable {
			if m.provider == p.name && m.id == id {
				return i
			}
		}
		return len(modelTable)
	}
	sort.SliceStable(cands, func(i, j int) bool {
		a, b := cands[i], cands[j]
		if da, db := a.ID == cfgDefault, b.ID == cfgDefault; da != db {
			return da
		}
		if ra, rb := tableRank(a.ID), tableRank(b.ID); ra != rb {
			return ra < rb
		}
		if a.Context != b.Context {
			return a.Context > b.Context
		}
		return a.ID < b.ID
	})
}

// ---- free.denyPaths ------------------------------------------------------------

// denyPathRefusal is the free.denyPaths gate: the refusal when the round's
// cwd matches one of the config's globs, "" otherwise. what names the launch
// being refused.
func denyPathRefusal(what, cwd string, cfg *config.Config) string {
	globs, _ := cfg.DenyPaths()
	g := denyPathMatch(cwd, globs)
	if g == "" {
		return ""
	}
	return fmt.Sprintf("outsource: %s refused — the round's cwd %s matches free.denyPaths glob %q in %s, which keeps free models out of that tree; run a priced model there, or edit free.denyPaths",
		what, cwd, g, cfg.Path)
}

// denyPathMatch returns the first glob the cwd matches, or "".
//
// The cwd is tried as given (made absolute) and with its symlinks resolved, so
// a link into a denied tree is caught. Each glob is tried as written (`~`
// expanded to the home dir) and with its literal leading directories resolved
// too, so a tree reached through a symlinked home still matches its own
// resolved cwd. A glob that is not absolute after expansion matches nothing.
func denyPathMatch(cwd string, globs []string) string {
	if len(globs) == 0 {
		return ""
	}
	home, _ := os.UserHomeDir()
	cwds := []string{}
	if abs, err := filepath.Abs(cwd); err == nil {
		cwds = append(cwds, abs)
		if real, err := filepath.EvalSymlinks(abs); err == nil && real != abs {
			cwds = append(cwds, real)
		}
	}
	for _, g := range globs {
		for _, pat := range globForms(expandHome(g, home)) {
			for _, c := range cwds {
				if globMatch(pat, c) {
					return g
				}
			}
		}
	}
	return ""
}

// expandHome expands a leading `~` (alone or `~/…`) to the home dir; `~user`
// is left as written.
func expandHome(g, home string) string {
	if g == "~" {
		return home
	}
	if rest, ok := strings.CutPrefix(g, "~/"); ok {
		return filepath.Join(home, rest)
	}
	return g
}

// globForms is the glob as written, plus — when its literal leading
// directories exist and resolve elsewhere — the same glob over their resolved
// path.
func globForms(pat string) []string {
	pat = filepath.Clean(pat)
	forms := []string{pat}
	segs := strings.Split(pat, "/")
	lit := len(segs)
	for i, s := range segs {
		if strings.ContainsAny(s, `*?[\`) {
			lit = i
			break
		}
	}
	prefix := strings.Join(segs[:lit], "/")
	if prefix == "" {
		return forms
	}
	real, err := filepath.EvalSymlinks(prefix)
	if err != nil || real == prefix {
		return forms
	}
	return append(forms, filepath.Join(append([]string{real}, segs[lit:]...)...))
}

// globMatch matches a path against a glob segment by segment: `**` matches
// any number of segments, none included (so dir/** matches dir itself), and
// every other segment is filepath.Match's.
func globMatch(pattern, path string) bool {
	var match func(ps, xs []string) bool
	match = func(ps, xs []string) bool {
		if len(ps) == 0 {
			return len(xs) == 0
		}
		if ps[0] == "**" {
			for k := 0; k <= len(xs); k++ {
				if match(ps[1:], xs[k:]) {
					return true
				}
			}
			return false
		}
		if len(xs) == 0 {
			return false
		}
		if ok, err := filepath.Match(ps[0], xs[0]); err != nil || !ok {
			return false
		}
		return match(ps[1:], xs[1:])
	}
	return match(strings.Split(filepath.Clean(pattern), "/"), strings.Split(filepath.Clean(path), "/"))
}

// ---- a named id on a catalogue provider ------------------------------------------

// namedCheck is the pre-flight's verdict on one named id.
type namedCheck struct {
	entry   catalog.Entry
	found   bool     // entry is the catalogue's listing of the id; its Context feeds contextEnv
	refusal string   // non-empty: exit 64 with it
	notes   []string // stderr lines, printed whatever the verdict
}

// preflightNamed checks an id the caller named (by --model, the config or the
// table) against its catalogue before the round starts:
//
//   - absent from a listing fetched within the hour: asked once more with
//     Refresh, since a fresh cache can predate a new id; still absent → refused;
//   - a router → refused: the identity check cannot pin a model picked per request;
//   - past its expiration date → refused;
//   - no tool support → refused: every harness here drives tool calls;
//   - listed free while the cwd matches free.denyPaths → refused;
//   - a status other than active → a warning, not a refusal (the caller chose it).
//
// When the catalogue cannot answer — switched off, no network and no cache,
// or a stale cache standing in for a failed fetch that does not list the id —
// the round proceeds with one note: the network must not stop a round the
// caller named. Except under free.denyPaths: there the launcher cannot tell
// whether the id is free, and a privacy gate must not open silently on a
// network failure (lead decision, 2026-10-09), so the round is refused.
func preflightNamed(p provider, harnessName, model, cwd string, cfg *config.Config, now time.Time) namedCheck {
	bare := bareID(p, harnessName, model)
	load := func(refresh bool) ([]catalog.Entry, catalog.Source) {
		entries, meta, err := loadCatalog(context.Background(), catalog.Options{Providers: []string{p.name}, Refresh: refresh})
		return entries, catalogueSource(meta, err, p.name)
	}
	find := func(entries []catalog.Entry) (catalog.Entry, bool) {
		for _, e := range entries {
			if e.Provider == p.name && e.ID == bare {
				return e, true
			}
		}
		return catalog.Entry{}, false
	}
	skipped := func(why string) namedCheck {
		globs, _ := cfg.DenyPaths()
		if g := denyPathMatch(cwd, globs); g != "" {
			return namedCheck{refusal: fmt.Sprintf("outsource: --model %s refused — the launcher cannot tell whether %s is free because %s, and the round's cwd %s is under free.denyPaths %q in %s; run it outside that tree, or retry once the catalogue answers",
				model, model, why, cwd, g, cfg.Path)}
		}
		return namedCheck{notes: []string{fmt.Sprintf("outsource: catalogue pre-flight skipped for %s on %s — %s", model, p.name, why)}}
	}

	entries, src := load(false)
	if !src.Loaded() {
		return skipped(src.Err.Error())
	}
	e, ok := find(entries)
	refreshed := false
	if !ok && src.From == catalog.FromCache && src.Err == nil {
		// A listing from within the hour can predate a new id: ask once more.
		if again, src2 := load(true); src2.Loaded() {
			entries, src, refreshed = again, src2, true
			e, ok = find(entries)
		}
	}
	if !ok {
		at := src.FetchedAt.UTC().Format(time.RFC3339)
		if src.Err != nil && !refreshed {
			// A stale cache standing in for a failed fetch: its silence about
			// a newer id proves nothing.
			return skipped(fmt.Sprintf("%s is not in the cached catalogue (fetched %s) and a fresh one could not be fetched: %v", bare, at, src.Err))
		}
		msg := fmt.Sprintf("outsource: --model %s is not in the %s catalogue (fetched %s)", model, p.name, at)
		if src.Err != nil {
			msg += fmt.Sprintf("; the refresh failed: %v", src.Err)
		}
		return namedCheck{refusal: msg + fmt.Sprintf(" — see what it offers: outsource models --provider %s", p.name)}
	}

	chk := namedCheck{entry: e, found: true}
	switch {
	case e.Router:
		chk.refusal = fmt.Sprintf("outsource: --model %s is a router on %s: it picks a different model per request, so the launcher's model-identity check cannot pin it — name one concrete id (outsource models --provider %s)", model, p.name, p.name)
		return chk
	case !e.Tools:
		chk.refusal = fmt.Sprintf("outsource: --model %s does not accept tool calls (the %s catalogue lists no tool support), and every harness here drives the model through tool calls — pick one that does: outsource models --provider %s --tools", model, p.name, p.name)
		return chk
	}
	if e.Expires != "" {
		ended, parsed := listingEnded(e.Expires, now)
		switch {
		case ended:
			chk.refusal = fmt.Sprintf("outsource: --model %s is past its listing's expiration date %s on %s — pick another: outsource models --provider %s", model, e.Expires, p.name, p.name)
			return chk
		case !parsed:
			chk.notes = append(chk.notes, fmt.Sprintf("outsource: warning — the %s catalogue lists %s with an expiration date this launcher cannot read (%q); launching it as named", p.name, model, e.Expires))
		}
	}
	if e.Free {
		if msg := denyPathRefusal(fmt.Sprintf("--model %s (a free id on %s)", model, p.name), cwd, cfg); msg != "" {
			chk.refusal = msg
			return chk
		}
	}
	if !e.Active() {
		chk.notes = append(chk.notes, fmt.Sprintf("outsource: warning — the %s catalogue marks %s as %q, not active; launching it as named", p.name, model, e.Status))
	}
	return chk
}

// ---- the --detach handoff -----------------------------------------------------

// catalogueHandoffFlag carries the parent's catalogue answer to its --detach
// child: `<selector>:<context>`, e.g. free:262144 after a free pick, :1000000
// after a named id the catalogue listed. Internal — accepted only by a
// process marked OUTSOURCE_DETACHED=1, so no caller can hand a launch a
// context (or skip its pre-flights) by typing it, and not in the usage line.
const catalogueHandoffFlag = "--catalogue-handoff"

func handoffValue(selector string, catalogueContext int) string {
	return selector + ":" + strconv.Itoa(catalogueContext)
}

func parseHandoff(v string) (selector string, catalogueContext int, err error) {
	sel, n, ok := strings.Cut(v, ":")
	if !ok || (sel != "" && sel != freeSelector) {
		return "", 0, fmt.Errorf("%s wants <selector>:<context> with selector %q or empty, got: %s", catalogueHandoffFlag, freeSelector, v)
	}
	ctx, err := strconv.Atoi(n)
	if err != nil || ctx < 0 {
		return "", 0, fmt.Errorf("%s wants a non-negative context, got: %s", catalogueHandoffFlag, v)
	}
	return sel, ctx, nil
}

// detachArgs is the argv the --detach child re-runs (reexecDetached drops
// --detach itself): the caller's own, with --model rewritten to the id
// --model free picked (modelIdx is the index of the last --model value, -1
// when the argv has none), plus the handoff when there is anything to hand
// over. A child with no handoff runs on the tables alone, as a round on a
// provider with no catalogue does.
func detachArgs(args []string, modelIdx int, o opts) []string {
	out := append([]string(nil), args...)
	if o.selector != "" {
		if modelIdx >= 0 && modelIdx < len(out) {
			out[modelIdx] = o.model
		} else {
			out = append(out, "--model", o.model)
		}
	}
	if o.selector != "" || o.catalogueContext > 0 {
		out = append(out, catalogueHandoffFlag, handoffValue(o.selector, o.catalogueContext))
	}
	return out
}
