package launch

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/midagedev/outsource/internal/catalog"
	"github.com/midagedev/outsource/internal/config"
	"github.com/midagedev/outsource/internal/runs"
)

// Tests for free.go: `--model free`, the named-id pre-flight, the --detach
// handoff and the catalogue's part of the claude-code harness's context
// numbers. Every catalogue answer here comes from a fixture installed on the
// seam (useCatalog); TestMain's floors keep everything else off the network.

const (
	nnr    = catalog.PolicyNoTrainNoRetain
	trains = catalog.PolicyTrains
)

// freeEntry is a free, tool-capable catalogue entry; tests change the fields
// a case is about.
func freeEntry(provider, id string, ctx int, policy string) catalog.Entry {
	return catalog.Entry{Provider: provider, ID: id, Name: id, Context: ctx, Free: true, PriceKnown: true, Tools: true, Policy: policy}
}

func dateFrom(now time.Time, days int) string {
	return now.UTC().AddDate(0, 0, days).Format("2006-01-02")
}

// catalogFixture is a fixture loader on the seam: serve answers each call,
// calls records what the launcher asked for. Like catalog.Load it returns only
// the requested providers' entries, and a policy only for the entries the
// caller's PolicyFor selected — so a caller that forgets to ask for policies
// sees none, as it would against the real catalogue.
type catalogFixture struct {
	calls []catalog.Options
	serve func(o catalog.Options) ([]catalog.Entry, catalog.Source)
}

// useCatalog installs a fixture serving entries from the network, fetched
// now; a test replaces serve for other sources.
func useCatalog(t *testing.T, entries ...catalog.Entry) *catalogFixture {
	t.Helper()
	fx := &catalogFixture{serve: func(catalog.Options) ([]catalog.Entry, catalog.Source) {
		return entries, catalog.Source{From: catalog.FromNetwork, FetchedAt: time.Now()}
	}}
	old := loadCatalog
	t.Cleanup(func() { loadCatalog = old })
	loadCatalog = fx.load
	return fx
}

func (fx *catalogFixture) load(_ context.Context, o catalog.Options) ([]catalog.Entry, catalog.Meta, error) {
	fx.calls = append(fx.calls, o)
	all, src := fx.serve(o)
	provs := o.Providers
	if len(provs) == 0 {
		provs = catalog.Providers()
	}
	meta := catalog.Meta{Providers: map[string]catalog.Source{}}
	var out []catalog.Entry
	for _, p := range provs {
		meta.Providers[p] = src
		if !src.Loaded() {
			continue
		}
		for _, e := range all {
			if e.Provider != p {
				continue
			}
			if o.PolicyFor == nil || !o.PolicyFor(e) {
				e.Policy = ""
			}
			out = append(out, e)
		}
	}
	if !src.Loaded() {
		return nil, meta, src.Err
	}
	return out, meta, nil
}

// swapModelTable appends rows to modelTable for this test only.
func swapModelTable(t *testing.T, rows ...model) {
	t.Helper()
	old := modelTable
	t.Cleanup(func() { modelTable = old })
	modelTable = append(append([]model(nil), old...), rows...)
}

func cfgFrom(t *testing.T, body string) *config.Config {
	t.Helper()
	userConfig(t, body)
	c, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func mustProvider(t *testing.T, name string) provider {
	t.Helper()
	p, ok := findProvider(name)
	if !ok {
		t.Fatalf("provider %s is gone", name)
	}
	return p
}

// ---- the launch rig --------------------------------------------------------

// fakeClaudeFree is a claude-code harness that answers as whatever it was
// asked to run: it writes its environment to $FREE_FAKE_ENV, appends one
// assistant turn (one tool call, message.model = $ANTHROPIC_MODEL) to the
// session transcript where analyzeRun looks — written anew each call, since
// rounds of one test share a config dir — and prints the result object.
const fakeClaudeFree = `#!/usr/bin/env bash
cat >/dev/null
env > "$FREE_FAKE_ENV"
sid=22222222-3333-4444-8555-666666666666
dir="$CLAUDE_CONFIG_DIR/projects/-fake-cwd"
mkdir -p "$dir"
printf '{"type":"assistant","message":{"model":"%s","content":[{"type":"tool_use","name":"Bash","input":{"command":"true"}}]}}\n' "$ANTHROPIC_MODEL" > "$dir/$sid.jsonl"
printf '{"type":"result","subtype":"success","session_id":"%s","result":"done","usage":{"output_tokens":1}}\n' "$sid"
`

// freeRig is one launch's files on an isolated machine (isolateOpenrouterLaunch:
// no real HOME, XDG dirs, registry or key), with a fake claude and a fake
// opencode on PATH. opencode never runs: its rounds are --detach and the
// re-exec is captured (captureDetach).
type freeRig struct {
	t                                   *testing.T
	dir, spec, cwd, log, cfgDir, envOut string
}

func newFreeRig(t *testing.T) *freeRig {
	t.Helper()
	dir, spec := isolateOpenrouterLaunch(t)
	g := &freeRig{t: t, dir: dir, spec: spec, cwd: filepath.Join(dir, "cwd"), log: filepath.Join(dir, "round.log"),
		cfgDir: filepath.Join(dir, "cfg"), envOut: filepath.Join(dir, "child.env")}
	bin := filepath.Join(dir, "bin")
	for _, d := range []string{bin, g.cwd, filepath.Join(dir, "home")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(fakeClaudeFree), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "opencode"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", rigPath(bin))
	refuseRealClaude(t, bin)
	t.Setenv("FREE_FAKE_ENV", g.envOut)
	t.Setenv("OPENROUTER_API_KEY", "test-key-not-a-real-credential")
	for _, k := range []string{"ZAI_BASE_URL", "ZAI_ANTHROPIC_BASE", "GLM_DELEGATE_MODEL", "OUTSOURCE_ALLOW_MAPPED_MODEL", detachedEnvKey, slotEnvKey, runLabelEnvKey} {
		t.Setenv(k, "")
	}
	return g
}

func (g *freeRig) run(extra ...string) (int, string) {
	g.t.Helper()
	var errb bytes.Buffer
	args := append([]string{"--cwd", g.cwd, "--spec", g.spec, "--log", g.log, "--config-dir", g.cfgDir, "--label", "free-test"}, extra...)
	rc := OutsourceMain(args, io.Discard, &errb)
	return rc, errb.String()
}

func (g *freeRig) sentinel() string {
	g.t.Helper()
	b, err := os.ReadFile(g.log + ".rc")
	if err != nil {
		g.t.Fatalf("no sentinel: %v", err)
	}
	return string(b)
}

func (g *freeRig) registered() int {
	recs, _ := runs.List()
	return len(recs)
}

// captureDetach replaces the --detach re-exec with a recorder: the argv the
// child would get, and no child.
func captureDetach(t *testing.T) *[]string {
	t.Helper()
	var got []string
	old := detachExec
	t.Cleanup(func() { detachExec = old })
	detachExec = func(_ string, args []string, _, _ string, _, _ io.Writer) int {
		got = append([]string(nil), args...)
		return 0
	}
	return &got
}

func withoutDetach(args []string) []string {
	var out []string
	for _, a := range args {
		if a != "--detach" {
			out = append(out, a)
		}
	}
	return out
}

// argAfter is the value after the last occurrence of flag in args.
func argAfter(args []string, flag string) string {
	v := ""
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag {
			v = args[i+1]
		}
	}
	return v
}

// ---- 1. the free pick -------------------------------------------------------

// Strict policy picks the no-train-no-retain id over a larger one that
// trains; the config's free.allowTraining and, separately, the launch flag
// let the larger one win; the config default wins when it qualifies and is
// ignored — not refused — when it does not. FAIL-first: with the policy
// filter removed, the strict leg picks vendor/big:free (the larger context).
func TestFreePickPolicyAndConfigDefault(t *testing.T) {
	or := mustProvider(t, "openrouter")
	now := time.Now()
	useCatalog(t,
		freeEntry("openrouter", "vendor/small:free", 262144, nnr),
		freeEntry("openrouter", "vendor/mid:free", 300000, nnr),
		freeEntry("openrouter", "vendor/big:free", 1000000, trains),
	)
	pickWith := func(body string, flag bool) (freePick, string) {
		t.Helper()
		return resolveFree(freeRequest{p: or, harness: "claude-code", cwd: t.TempDir(), cfg: cfgFrom(t, body), allowTraining: flag, now: now})
	}
	for _, c := range []struct {
		name, body string
		flag       bool
		want       string
	}{
		{"strict: the clean id over the larger one that trains", `{}`, false, "vendor/mid:free"},
		{"free.allowTraining: the larger one wins", `{"free": {"allowTraining": true}}`, false, "vendor/big:free"},
		{"--allow-free-training: the larger one wins", `{}`, true, "vendor/big:free"},
		{"a qualifying config default wins", `{"providers": {"openrouter": {"defaultModel": "vendor/small:free"}}}`, false, "vendor/small:free"},
		{"a config default that does not qualify is ignored", `{"providers": {"openrouter": {"defaultModel": "vendor/big:free"}}}`, false, "vendor/mid:free"},
		{"a config default absent from the catalogue is ignored", `{"providers": {"openrouter": {"defaultModel": "vendor/gone:free"}}}`, false, "vendor/mid:free"},
	} {
		got, refusal := pickWith(c.body, c.flag)
		if refusal != "" || got.model != c.want {
			t.Errorf("%s: picked %q (refusal %q), want %q", c.name, got.model, refusal, c.want)
		}
	}
	// The flag through the launch, not only the resolver: the child argv
	// carries the larger id.
	g := newFreeRig(t)
	child := captureDetach(t)
	userConfig(t, `{}`)
	if rc, errs := g.run("--detach", "--provider", "openrouter", "--model", "free", "--allow-free-training"); rc != 0 {
		t.Fatalf("--allow-free-training launch: rc=%d; stderr=%s", rc, errs)
	}
	if m := argAfter(*child, "--model"); m != "vendor/big:free" {
		t.Fatalf("--allow-free-training launch ran %q, want vendor/big:free; argv %v", m, *child)
	}
}

// Ranking past the config default: modelTable rows in table order, then the
// larger context, then the id.
func TestFreePickRanking(t *testing.T) {
	or := mustProvider(t, "openrouter")
	swapModelTable(t, model{provider: "openrouter", id: "vendor/measured:free"})
	useCatalog(t,
		freeEntry("openrouter", "vendor/b:free", 500000, nnr),
		freeEntry("openrouter", "vendor/a:free", 500000, nnr),
		freeEntry("openrouter", "vendor/huge:free", 900000, nnr),
		freeEntry("openrouter", "vendor/measured:free", 200000, nnr),
	)
	got, refusal := resolveFree(freeRequest{p: or, harness: "claude-code", cwd: t.TempDir(), cfg: cfgFrom(t, `{}`), now: time.Now()})
	if refusal != "" {
		t.Fatal(refusal)
	}
	var ids []string
	for _, e := range got.candidates {
		ids = append(ids, e.ID)
	}
	if want := "vendor/measured:free vendor/huge:free vendor/a:free vendor/b:free"; strings.Join(ids, " ") != want {
		t.Fatalf("rank order %v, want %s", ids, want)
	}
}

// ---- 2. the filters -----------------------------------------------------------

// Each kind of unusable id is dropped and counted, and the nothing-qualifies
// refusal carries every count. The boundaries pass: exactly 128000 ctx, a
// listing that ends tomorrow. FAIL-first: drop the Context test from freeDrop
// and the 127999 and 0-ctx ids reach the policy filter instead ("0 under
// 128000 ctx").
func TestFreeFiltersDropAndCount(t *testing.T) {
	or := mustProvider(t, "openrouter")
	now := time.Now()
	router := freeEntry("openrouter", "openrouter/free", 200000, nnr)
	router.Router = true
	noTools := freeEntry("openrouter", "vendor/no-tools:free", 262144, nnr)
	noTools.Tools = false
	expired := freeEntry("openrouter", "vendor/expired:free", 262144, nnr)
	expired.Expires = dateFrom(now, 0)
	priced := freeEntry("openrouter", "vendor/priced", 262144, nnr)
	priced.Free, priced.PriceIn, priced.PriceOut = false, 2.5, 7.5
	bad := []catalog.Entry{
		priced, router, noTools, expired,
		freeEntry("openrouter", "vendor/small:free", 127999, nnr),
		freeEntry("openrouter", "vendor/unstated:free", 0, nnr),
		freeEntry("openrouter", "vendor/trains:free", 262144, trains),
	}
	fx := useCatalog(t, bad...)
	cfg := cfgFrom(t, `{}`)
	picked, refusal := resolveFree(freeRequest{p: or, harness: "claude-code", cwd: t.TempDir(), cfg: cfg, now: now})
	want := "--model free: no candidate on openrouter — 7 listed; 1 not free, 1 routers, 1 no tools, 1 expired, 0 withdrawn, 2 under 128000 ctx, 0 answered by another model, 1 data policy (allow with free.allowTraining or --allow-free-training)"
	if refusal != want {
		t.Fatalf("refusal %q (picked %q)\nwant %q", refusal, picked.model, want)
	}
	// The policy lookups were asked only for what passes the cheap filters.
	sel := fx.calls[0].PolicyFor
	for _, e := range bad[:6] {
		if sel(e) {
			t.Errorf("PolicyFor selected %s, which a cheap filter drops — a policy request per such id", e.ID)
		}
	}
	if !sel(bad[6]) {
		t.Error("PolicyFor must select an id that passes every cheap filter")
	}

	// The boundaries qualify.
	edge := freeEntry("openrouter", "vendor/edge:free", minFreeContext, nnr)
	ends := freeEntry("openrouter", "vendor/ends-tomorrow:free", 100000+minFreeContext, nnr)
	ends.Expires = dateFrom(now, 1)
	useCatalog(t, append(bad, edge, ends)...)
	got, refusal := resolveFree(freeRequest{p: or, harness: "claude-code", cwd: t.TempDir(), cfg: cfg, now: now})
	if refusal != "" || len(got.candidates) != 2 || got.model != "vendor/ends-tomorrow:free" {
		t.Fatalf("boundary ids: pick %q of %d candidates, refusal %q", got.model, len(got.candidates), refusal)
	}
	if got.counts.String(true) != "9 listed; 1 not free, 1 routers, 1 no tools, 1 expired, 0 withdrawn, 2 under 128000 ctx, 0 answered by another model, 1 data policy (allow with free.allowTraining or --allow-free-training)" {
		t.Fatalf("counts beside a pick: %s", got.counts.String(true))
	}
	// An expiration date that does not parse is not "a date after today".
	odd := freeEntry("openrouter", "vendor/odd-date:free", 262144, nnr)
	odd.Expires = "soon"
	if freeDrop(or, odd, now) != dropExpired {
		t.Error("an unreadable expiration date must drop the id from a pick")
	}
}

// The launch prints the refusal and exits 64 before anything is registered.
func TestFreeNothingQualifiesRefusesTheLaunch(t *testing.T) {
	g := newFreeRig(t)
	userConfig(t, `{}`)
	useCatalog(t, freeEntry("openrouter", "vendor/trains:free", 262144, trains))
	rc, errs := g.run("--detach", "--provider", "openrouter", "--model", "free")
	if rc != ExitUsage || !strings.Contains(errs, "--model free: no candidate on openrouter — 1 listed;") || !strings.Contains(errs, "1 data policy (allow with") {
		t.Fatalf("rc=%d, want %d with the counts; stderr=%s", rc, ExitUsage, errs)
	}
	if n := g.registered(); n != 0 {
		t.Fatalf("a refused pick registered %d round(s)", n)
	}
}

// --model free is a catalogue's word: on any other provider it is refused,
// naming the catalogues, and nothing is loaded.
func TestFreeNeedsACatalogueProvider(t *testing.T) {
	g := newFreeRig(t)
	userConfig(t, `{}`)
	fx := useCatalog(t)
	rc, errs := g.run("--detach", "--provider", "zai", "--model", "free")
	if want := "--model free picks from a catalogue; provider zai has none (catalogues: openrouter, zen)\n"; rc != ExitUsage || !strings.HasSuffix(errs, want) {
		t.Fatalf("rc=%d stderr=%q, want %d ending %q", rc, errs, ExitUsage, want)
	}
	if len(fx.calls) != 0 {
		t.Fatalf("a refused provider loaded the catalogue %d time(s)", len(fx.calls))
	}
	// Only the literal: openrouter/free is OpenRouter's router id, refused by
	// the named-id pre-flight as one, never resolved.
	router := freeEntry("openrouter", "openrouter/free", 200000, nnr)
	router.Router = true
	useCatalog(t, router)
	if rc, errs := g.run("--detach", "--provider", "openrouter", "--model", "openrouter/free"); rc != ExitUsage || !strings.Contains(errs, "is a router on openrouter") {
		t.Fatalf("openrouter/free: rc=%d; stderr=%s", rc, errs)
	}
}

// Catalogue unavailable (no network, no cache): --model free cannot pick,
// and says the Source's error.
func TestFreeCatalogueUnavailable(t *testing.T) {
	or := mustProvider(t, "openrouter")
	fx := useCatalog(t)
	fx.serve = func(catalog.Options) ([]catalog.Entry, catalog.Source) {
		return nil, catalog.Source{Err: errors.New("dial tcp: lookup openrouter.ai: no such host")}
	}
	_, refusal := resolveFree(freeRequest{p: or, harness: "claude-code", cwd: t.TempDir(), cfg: cfgFrom(t, `{}`), now: time.Now()})
	if want := "--model free cannot pick: dial tcp: lookup openrouter.ai: no such host"; refusal != want {
		t.Fatalf("refusal %q, want %q", refusal, want)
	}
}

// ---- 3. the harness's form, the sentinel, and the --detach child -------------

// The pick reaches each harness in its form: bare on claude-code,
// <qualifier>/<id> on opencode — openrouter's and zen's own qualifiers.
func TestFreePickInTheHarnessForm(t *testing.T) {
	g := newFreeRig(t)
	userConfig(t, `{}`)
	useCatalog(t,
		freeEntry("openrouter", "vendor/clean:free", 262144, nnr),
		freeEntry("zen", "clean-free", 1000000, nnr),
	)
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"--provider", "openrouter"}, "vendor/clean:free"},
		{[]string{"--provider", "openrouter", "--harness", "opencode"}, "openrouter/vendor/clean:free"},
		{[]string{"--provider", "zen"}, "opencode/clean-free"},
	} {
		child := captureDetach(t)
		rc, errs := g.run(append([]string{"--detach", "--model", "free"}, c.args...)...)
		if rc != 0 {
			t.Fatalf("%v: rc=%d; stderr=%s", c.args, rc, errs)
		}
		if m := argAfter(*child, "--model"); m != c.want {
			t.Fatalf("%v: the child runs %q, want %q; argv %v", c.args, m, c.want, *child)
		}
		mustContain(t, "the pick line", errs, "outsource: --model free → "+c.want+" (no-train-no-retain, ")
	}
}

// A foreground round: the pick line on stderr, model_requested= the id as
// run and model_selector=free in the sentinel, the registry's model, and the
// catalogue's window with the cap in the harness's environment. FAIL-first:
// without the model_selector line in sentinelBody the sentinel lacks it.
func TestFreePickForegroundRound(t *testing.T) {
	g := newFreeRig(t)
	userConfig(t, `{}`)
	useCatalog(t, freeEntry("openrouter", "vendor/clean:free", 1000000, nnr))
	rc, errs := g.run("--foreground", "--provider", "openrouter", "--model", "free")
	if rc != 0 {
		t.Fatalf("rc=%d; stderr=%s", rc, errs)
	}
	mustContain(t, "stderr", errs, "outsource: --model free → vendor/clean:free (no-train-no-retain, 1000000 ctx; 1 candidates; catalogue network ")
	mustContain(t, "sentinel", g.sentinel(), "model_requested=vendor/clean:free\n", "model_selector=free\n", "model_actual=vendor/clean:free\n")
	if rec := runs.FindByLog(g.log); rec == nil || rec.Model != "vendor/clean:free" {
		t.Fatalf("the registry must record the id as run, got %+v", rec)
	}
	env := childEnv(t, g.envOut)
	if env["ANTHROPIC_MODEL"] != "vendor/clean:free" || env["CLAUDE_CODE_MAX_CONTEXT_TOKENS"] != "1000000" || env["CLAUDE_CODE_AUTO_COMPACT_WINDOW"] != "600000" {
		t.Fatalf("harness env: model %q window %q cap %q, want vendor/clean:free 1000000 600000",
			env["ANTHROPIC_MODEL"], env["CLAUDE_CODE_MAX_CONTEXT_TOKENS"], env["CLAUDE_CODE_AUTO_COMPACT_WINDOW"])
	}

	// A named id writes no selector line.
	rc, errs = g.run("--foreground", "--provider", "openrouter", "--model", "vendor/clean:free")
	if rc != 0 {
		t.Fatalf("named: rc=%d; stderr=%s", rc, errs)
	}
	mustNotContain(t, "a named round's sentinel", g.sentinel(), "model_selector=")
}

// The --detach child runs the parent's pick and the parent's window and cap,
// and never calls the catalogue: between the two, the catalogue changes so
// that a re-resolution would pick another id with another context. FAIL-first:
// make detachArgs return args unchanged and the child refuses --model free
// without its parent's pick (exit 64); let the child resolve and it runs
// vendor/child-would-pick:free with a 262144 window and no cap.
func TestFreePickDetachChildRunsTheParentsPick(t *testing.T) {
	g := newFreeRig(t)
	userConfig(t, `{}`)
	useCatalog(t, freeEntry("openrouter", "vendor/parent-pick:free", 1000000, nnr))
	got := captureDetach(t)
	rc, errs := g.run("--detach", "--provider", "openrouter", "--model", "free")
	if rc != 0 {
		t.Fatalf("parent: rc=%d; stderr=%s", rc, errs)
	}
	if h := argAfter(*got, catalogueHandoffFlag); h != "free:1000000" {
		t.Fatalf("the handoff is %q, want free:1000000; argv %v", h, *got)
	}

	childFx := useCatalog(t, freeEntry("openrouter", "vendor/child-would-pick:free", 262144, nnr))
	t.Setenv(detachedEnvKey, "1")
	var errb bytes.Buffer
	if rc := OutsourceMain(withoutDetach(*got), io.Discard, &errb); rc != 0 {
		t.Fatalf("child: rc=%d; stderr=%s", rc, errb.String())
	}
	env := childEnv(t, g.envOut)
	if env["ANTHROPIC_MODEL"] != "vendor/parent-pick:free" || env["CLAUDE_CODE_MAX_CONTEXT_TOKENS"] != "1000000" || env["CLAUDE_CODE_AUTO_COMPACT_WINDOW"] != "600000" {
		t.Fatalf("child harness env: model %q window %q cap %q, want the parent's vendor/parent-pick:free 1000000 600000",
			env["ANTHROPIC_MODEL"], env["CLAUDE_CODE_MAX_CONTEXT_TOKENS"], env["CLAUDE_CODE_AUTO_COMPACT_WINDOW"])
	}
	mustContain(t, "child sentinel", g.sentinel(), "model_requested=vendor/parent-pick:free\n", "model_selector=free\n")
	if len(childFx.calls) != 0 {
		t.Fatalf("the child called the catalogue seam %d time(s)", len(childFx.calls))
	}
}

// A named id's catalogue context travels the same way, and a round with
// nothing to hand over carries no handoff at all.
func TestNamedIdContextReachesTheDetachChild(t *testing.T) {
	g := newFreeRig(t)
	userConfig(t, `{}`)
	useCatalog(t, freeEntry("openrouter", "vendor/named:free", 262144, nnr))
	got := captureDetach(t)
	if rc, errs := g.run("--detach", "--provider", "openrouter", "--model", "vendor/named:free"); rc != 0 {
		t.Fatalf("rc=%d; stderr=%s", rc, errs)
	}
	if h := argAfter(*got, catalogueHandoffFlag); h != ":262144" || argAfter(*got, "--model") != "vendor/named:free" {
		t.Fatalf("named id: handoff %q model %q; argv %v", h, argAfter(*got, "--model"), *got)
	}
	if rc, errs := g.run("--detach", "--provider", "zai"); rc != 0 {
		t.Fatalf("zai: rc=%d; stderr=%s", rc, errs)
	}
	if strings.Contains(strings.Join(*got, " "), catalogueHandoffFlag) {
		t.Fatalf("a round with no catalogue answer carries a handoff: %v", *got)
	}
}

// The handoff flag is the child's only: anywhere else it is an unknown flag,
// so no caller can hand a launch a context by typing it. A malformed value
// is refused, and a child given --model free without one refuses rather than
// resolving.
func TestCatalogueHandoffIsTheChildsOnly(t *testing.T) {
	g := newFreeRig(t)
	userConfig(t, `{}`)
	fx := useCatalog(t)
	rc, errs := g.run("--detach", "--provider", "openrouter", "--model", "vendor/x:free", catalogueHandoffFlag, ":999999")
	if rc != ExitUsage || !strings.Contains(errs, "unknown flag: "+catalogueHandoffFlag) {
		t.Fatalf("handoff outside a child: rc=%d stderr=%s", rc, errs)
	}
	t.Setenv(detachedEnvKey, "1")
	for _, v := range []string{"paid:1", "free:-1", "free", "free:x"} {
		if rc, errs := g.run("--provider", "openrouter", "--model", "vendor/x:free", catalogueHandoffFlag, v); rc != ExitUsage || !strings.Contains(errs, catalogueHandoffFlag+" wants") {
			t.Fatalf("handoff %q: rc=%d stderr=%s", v, rc, errs)
		}
	}
	if rc, errs := g.run("--provider", "openrouter", "--model", "free"); rc != ExitUsage || !strings.Contains(errs, "without its parent's pick") {
		t.Fatalf("child with --model free: rc=%d stderr=%s", rc, errs)
	}
	if len(fx.calls) != 0 {
		t.Fatalf("a child called the catalogue %d time(s)", len(fx.calls))
	}
}

// ---- 4. free.denyPaths ---------------------------------------------------------

// A cwd under ~/work/** (home faked) refuses --model free and a named free
// id; a priced named id and a cwd outside pass; a symlinked cwd into the
// denied tree is refused. The refusal names the glob and the config file, and
// comes before the catalogue is loaded. FAIL-first: without the resolved-glob
// form (globForms returning the glob alone), the symlinked cwd launches —
// its resolved path is under /private/var while ~ expands under /var.
func TestFreeDenyPaths(t *testing.T) {
	g := newFreeRig(t)
	home := filepath.Join(g.dir, "home")
	work := filepath.Join(home, "work", "proj")
	outside := filepath.Join(home, "other")
	for _, d := range []string{work, outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(g.dir, "link-into-work")
	if err := os.Symlink(work, link); err != nil {
		t.Fatal(err)
	}
	path := userConfig(t, `{"free": {"denyPaths": ["~/work/**"]}}`)
	priced := freeEntry("openrouter", "vendor/priced", 262144, nnr)
	priced.Free = false
	fx := useCatalog(t, freeEntry("openrouter", "vendor/clean:free", 262144, nnr), priced)
	captureDetach(t)
	launch := func(cwd string, args ...string) (int, string) {
		t.Helper()
		g.cwd = cwd
		return g.run(append([]string{"--detach", "--provider", "openrouter"}, args...)...)
	}

	rc, errs := launch(work, "--model", "free")
	if rc != ExitUsage || !strings.Contains(errs, `matches free.denyPaths glob "~/work/**" in `+path) {
		t.Fatalf("--model free under ~/work: rc=%d stderr=%s", rc, errs)
	}
	if len(fx.calls) != 0 {
		t.Fatalf("the deny check must come before the catalogue is loaded; %d load(s)", len(fx.calls))
	}
	if rc, errs := launch(work, "--model", "vendor/clean:free"); rc != ExitUsage || !strings.Contains(errs, "--model vendor/clean:free (a free id on openrouter) refused") {
		t.Fatalf("named free id under ~/work: rc=%d stderr=%s", rc, errs)
	}
	if rc, errs := launch(link, "--model", "free"); rc != ExitUsage || !strings.Contains(errs, "free.denyPaths") {
		t.Fatalf("symlinked cwd into ~/work: rc=%d stderr=%s", rc, errs)
	}
	if rc, errs := launch(work, "--model", "vendor/priced"); rc != 0 {
		t.Fatalf("a priced named id under ~/work must pass: rc=%d stderr=%s", rc, errs)
	}
	if rc, errs := launch(outside, "--model", "free"); rc != 0 {
		t.Fatalf("--model free outside the glob must pass: rc=%d stderr=%s", rc, errs)
	}
	if n := g.registered(); n != 0 {
		t.Fatalf("detached launches registered %d round(s) in the parent", n)
	}
}

// The glob rules, cell by cell: ** matches any depth and the directory
// itself, * one segment, ~ the home dir; a relative glob matches nothing.
func TestDenyPathGlobs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, c := range []struct {
		glob, cwd string
		want      bool
	}{
		{"~/work/**", filepath.Join(home, "work"), true},
		{"~/work/**", filepath.Join(home, "work", "a", "b"), true},
		{"~/work/**", filepath.Join(home, "workshop"), false},
		{"~/work/*", filepath.Join(home, "work", "a"), true},
		{"~/work/*", filepath.Join(home, "work", "a", "b"), false},
		{"/srv/**/secret", "/srv/x/y/secret", true},
		{"/srv/**/secret", "/srv/secret", true},
		{"/srv/**/secret", "/srv/x/secret/deeper", false},
		{"~", home, true},
		{"work/**", filepath.Join(home, "work"), false},
	} {
		got := denyPathMatch(c.cwd, []string{c.glob}) != ""
		if got != c.want {
			t.Errorf("glob %q on %s: match=%v, want %v", c.glob, c.cwd, got, c.want)
		}
	}
}

// ---- 5. the named-id pre-flight ---------------------------------------------------

// Absent from a fresh cache: one refresh, then refused naming the id, the
// catalogue's fetch time and the listing command; found on the refresh, it
// launches. Absent from a network listing: refused without a second load.
// FAIL-first: without the refresh, the first leg refuses after one load and
// the boundary leg (the id arrives with the refresh) is refused too.
func TestNamedIdAbsentRefreshesOnceThenRefuses(t *testing.T) {
	g := newFreeRig(t)
	userConfig(t, `{}`)
	captureDetach(t)
	cachedAt := time.Date(2026, 10, 9, 8, 0, 0, 0, time.UTC)
	listing := []catalog.Entry{freeEntry("openrouter", "vendor/other:free", 262144, nnr)}
	fx := useCatalog(t)
	fx.serve = func(o catalog.Options) ([]catalog.Entry, catalog.Source) {
		if o.Refresh {
			return listing, catalog.Source{From: catalog.FromNetwork, FetchedAt: cachedAt.Add(time.Minute)}
		}
		return listing, catalog.Source{From: catalog.FromCache, FetchedAt: cachedAt}
	}
	rc, errs := g.run("--detach", "--provider", "openrouter", "--model", "vendor/ghost:free")
	want := "outsource: --model vendor/ghost:free is not in the openrouter catalogue (fetched 2026-10-09T08:01:00Z) — see what it offers: outsource models --provider openrouter"
	if rc != ExitUsage || !strings.Contains(errs, want) {
		t.Fatalf("absent: rc=%d stderr=%s\nwant %s", rc, errs, want)
	}
	if len(fx.calls) != 2 || fx.calls[0].Refresh || !fx.calls[1].Refresh {
		t.Fatalf("absent from a fresh cache must load once more with Refresh; calls %+v", fx.calls)
	}

	listing = append(listing, freeEntry("openrouter", "vendor/ghost:free", 262144, nnr))
	fx.serve = func(o catalog.Options) ([]catalog.Entry, catalog.Source) {
		if o.Refresh {
			return listing, catalog.Source{From: catalog.FromNetwork, FetchedAt: time.Now()}
		}
		return listing[:1], catalog.Source{From: catalog.FromCache, FetchedAt: time.Now()}
	}
	if rc, errs := g.run("--detach", "--provider", "openrouter", "--model", "vendor/ghost:free"); rc != 0 {
		t.Fatalf("found on the refresh: rc=%d stderr=%s", rc, errs)
	}

	fx.calls = nil
	fx.serve = func(catalog.Options) ([]catalog.Entry, catalog.Source) {
		return listing[:1], catalog.Source{From: catalog.FromNetwork, FetchedAt: time.Now()}
	}
	if rc, errs := g.run("--detach", "--provider", "openrouter", "--model", "vendor/ghost:free"); rc != ExitUsage || len(fx.calls) != 1 {
		t.Fatalf("absent from the network listing: rc=%d after %d load(s), want %d after 1; stderr=%s", rc, len(fx.calls), ExitUsage, errs)
	}
}

// A router, an expired listing and an id without tools are refused, each
// saying why; a zen id marked deprecated is warned about and launched.
// FAIL-first: without the Tools case in preflightNamed, the no-tools id
// launches (rc 0).
func TestNamedIdRefusals(t *testing.T) {
	g := newFreeRig(t)
	userConfig(t, `{}`)
	captureDetach(t)
	now := time.Now()
	router := freeEntry("openrouter", "openrouter/auto", 2000000, "")
	router.Router, router.Free = true, false
	expired := freeEntry("openrouter", "poolside/laguna:free", 262144, "")
	expired.Expires = dateFrom(now, -1)
	noTools := freeEntry("openrouter", "google/lyria:free", 262144, "")
	noTools.Tools = false
	deprecated := freeEntry("zen", "old-free", 262144, "")
	deprecated.Status = "deprecated"
	useCatalog(t, router, expired, noTools, deprecated)
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"--provider", "openrouter", "--model", "openrouter/auto"}, "is a router on openrouter: it picks a different model per request"},
		{[]string{"--provider", "openrouter", "--model", "poolside/laguna:free"}, "past its listing's expiration date " + expired.Expires},
		{[]string{"--provider", "openrouter", "--model", "google/lyria:free"}, "does not accept tool calls"},
	} {
		if rc, errs := g.run(append([]string{"--detach"}, c.args...)...); rc != ExitUsage || !strings.Contains(errs, c.want) {
			t.Errorf("%v: rc=%d stderr=%s, want %d with %q", c.args, rc, errs, ExitUsage, c.want)
		}
	}
	rc, errs := g.run("--detach", "--provider", "zen", "--model", "opencode/old-free")
	if rc != 0 || !strings.Contains(errs, `outsource: warning — the zen catalogue marks opencode/old-free as "deprecated", not active; launching it as named`) {
		t.Fatalf("deprecated named zen id: rc=%d stderr=%s", rc, errs)
	}
	if n := g.registered(); n != 0 {
		t.Fatalf("refused launches registered %d round(s)", n)
	}
}

// The catalogue cannot answer: the round the caller named proceeds with one
// note — no network and no cache, or a stale cache (behind a failed fetch)
// that does not list the id.
func TestNamedIdProceedsWhenTheCatalogueCannotAnswer(t *testing.T) {
	g := newFreeRig(t)
	userConfig(t, `{}`)
	got := captureDetach(t)
	fx := useCatalog(t)
	fx.serve = func(catalog.Options) ([]catalog.Entry, catalog.Source) {
		return nil, catalog.Source{Err: errors.New("dial tcp: no route to host")}
	}
	rc, errs := g.run("--detach", "--provider", "openrouter", "--model", "vendor/x:free")
	if rc != 0 || !strings.Contains(errs, "outsource: catalogue pre-flight skipped for vendor/x:free on openrouter — dial tcp: no route to host") {
		t.Fatalf("unavailable: rc=%d stderr=%s", rc, errs)
	}
	if strings.Contains(strings.Join(*got, " "), catalogueHandoffFlag) {
		t.Fatalf("no catalogue answer, yet a handoff: %v", *got)
	}
	fx.calls = nil
	fx.serve = func(catalog.Options) ([]catalog.Entry, catalog.Source) {
		return []catalog.Entry{freeEntry("openrouter", "vendor/other:free", 262144, "")},
			catalog.Source{From: catalog.FromCache, FetchedAt: time.Now().Add(-3 * time.Hour), Err: errors.New("HTTP 502")}
	}
	rc, errs = g.run("--detach", "--provider", "openrouter", "--model", "vendor/x:free")
	if rc != 0 || !strings.Contains(errs, "is not in the cached catalogue") || !strings.Contains(errs, "HTTP 502") || len(fx.calls) != 1 {
		t.Fatalf("stale cache: rc=%d after %d load(s); stderr=%s", rc, len(fx.calls), errs)
	}
}

// ---- 6. the context window and the cap ----------------------------------------

// contextEnv cell by cell: a model row beats the catalogue, the catalogue
// beats the provider fallback, the cap is applied as a min, and pins win.
// FAIL-first: with the compactCap < window test dropped, the 262144 id gets
// CLAUDE_CODE_AUTO_COMPACT_WINDOW=600000 — above its own window.
func TestContextEnv(t *testing.T) {
	or, zai := mustProvider(t, "openrouter"), mustProvider(t, "zai")
	swapModelTable(t, model{provider: "openrouter", id: "vendor/measured:free", contextWindow: 500000})
	none := func(string) string { return "" }
	pinned := func(k, v string) func(string) string {
		return func(key string) string {
			if key == k {
				return v
			}
			return ""
		}
	}
	for _, c := range []struct {
		name      string
		p         provider
		model     string
		catalogue int
		capTokens int
		getenv    func(string) string
		want      string
	}{
		{"model row beats the catalogue", or, "vendor/measured:free", 1000000, 600000, none, "CLAUDE_CODE_MAX_CONTEXT_TOKENS=500000"},
		{"the catalogue beats the provider fallback", or, "vendor/listed:free", 1000000, 600000, none, "CLAUDE_CODE_MAX_CONTEXT_TOKENS=1000000 CLAUDE_CODE_AUTO_COMPACT_WINDOW=600000"},
		{"no catalogue figure: the provider fallback (0 for openrouter)", or, "vendor/listed:free", 0, 600000, none, ""},
		{"zai glm-5.3: its window and the 600000 cap", zai, "glm-5.3", 0, 600000, none, "CLAUDE_CODE_MAX_CONTEXT_TOKENS=1310720 CLAUDE_CODE_AUTO_COMPACT_WINDOW=600000"},
		{"a 262144 id gets nothing new", or, "vendor/listed:free", 262144, 600000, none, "CLAUDE_CODE_MAX_CONTEXT_TOKENS=262144"},
		{"a 200000 cap on a 262144 id", or, "vendor/listed:free", 262144, 200000, none, "CLAUDE_CODE_MAX_CONTEXT_TOKENS=262144 CLAUDE_CODE_AUTO_COMPACT_WINDOW=200000"},
		{"a pinned window wins, and the cap is weighed against it", zai, "glm-5.3", 0, 600000, pinned("CLAUDE_CODE_MAX_CONTEXT_TOKENS", "400000"), ""},
		{"a pinned window above the cap still gets the cap", zai, "glm-5.3", 0, 600000, pinned("CLAUDE_CODE_MAX_CONTEXT_TOKENS", "800000"), "CLAUDE_CODE_AUTO_COMPACT_WINDOW=600000"},
		{"a pinned window that is not a number leaves the cap unset", zai, "glm-5.3", 0, 600000, pinned("CLAUDE_CODE_MAX_CONTEXT_TOKENS", "big"), ""},
		{"a pinned cap wins", zai, "glm-5.3", 0, 600000, pinned("CLAUDE_CODE_AUTO_COMPACT_WINDOW", "50000"), "CLAUDE_CODE_MAX_CONTEXT_TOKENS=1310720"},
		{"xai: unmeasured, nothing set", mustProvider(t, "xai"), "grok-4.6", 0, 600000, none, ""},
	} {
		if got := strings.Join(contextEnv(c.p, c.model, c.catalogue, c.capTokens, c.getenv), " "); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

// End to end through the fake claude: zai glm-5.3 gets the cap; a config
// cap of 200000 on a 262144 catalogue id sets 200000; a caller's pin reaches
// the harness unchanged and the launcher adds nothing of its own; a cap that
// is not a positive whole number refuses at load. FAIL-first: without the
// contextEnv line in runClaudeCode, the zai round's env has no cap.
func TestContextReachesTheHarness(t *testing.T) {
	g := newFreeRig(t)
	userConfig(t, `{}`)
	if rc, errs := g.run("--foreground", "--provider", "zai"); rc != 0 {
		t.Fatalf("zai: rc=%d stderr=%s", rc, errs)
	}
	if env := childEnv(t, g.envOut); env["CLAUDE_CODE_MAX_CONTEXT_TOKENS"] != "1310720" || env["CLAUDE_CODE_AUTO_COMPACT_WINDOW"] != "600000" {
		t.Fatalf("zai glm-5.3: window %q cap %q, want 1310720 and 600000", env["CLAUDE_CODE_MAX_CONTEXT_TOKENS"], env["CLAUDE_CODE_AUTO_COMPACT_WINDOW"])
	}

	userConfig(t, `{"context": {"autoCompactWindow": 200000}}`)
	useCatalog(t, freeEntry("openrouter", "vendor/mid:free", 262144, nnr))
	if rc, errs := g.run("--foreground", "--provider", "openrouter", "--model", "vendor/mid:free"); rc != 0 {
		t.Fatalf("openrouter: rc=%d stderr=%s", rc, errs)
	}
	if env := childEnv(t, g.envOut); env["CLAUDE_CODE_MAX_CONTEXT_TOKENS"] != "262144" || env["CLAUDE_CODE_AUTO_COMPACT_WINDOW"] != "200000" {
		t.Fatalf("262144 id, cap 200000: window %q cap %q", env["CLAUDE_CODE_MAX_CONTEXT_TOKENS"], env["CLAUDE_CODE_AUTO_COMPACT_WINDOW"])
	}

	t.Setenv("CLAUDE_CODE_MAX_CONTEXT_TOKENS", "123456")
	t.Setenv("CLAUDE_CODE_AUTO_COMPACT_WINDOW", "50000")
	if rc, errs := g.run("--foreground", "--provider", "zai"); rc != 0 {
		t.Fatalf("pinned: rc=%d stderr=%s", rc, errs)
	}
	if env := childEnv(t, g.envOut); env["CLAUDE_CODE_MAX_CONTEXT_TOKENS"] != "123456" || env["CLAUDE_CODE_AUTO_COMPACT_WINDOW"] != "50000" {
		t.Fatalf("pins: window %q cap %q, want the caller's 123456 and 50000", env["CLAUDE_CODE_MAX_CONTEXT_TOKENS"], env["CLAUDE_CODE_AUTO_COMPACT_WINDOW"])
	}

	for _, v := range []string{"0", "-1", `"600k"`} {
		path := userConfig(t, `{"context": {"autoCompactWindow": `+v+`}}`)
		rc, errs := g.run("--foreground", "--provider", "zai")
		if rc != ExitUsage || !strings.Contains(errs, "refusing to launch") || !strings.Contains(errs, path) || !strings.Contains(errs, "context.autoCompactWindow must be a positive whole number") {
			t.Fatalf("autoCompactWindow %s: rc=%d stderr=%s", v, rc, errs)
		}
	}
}

// ---- 7. status and the mapped-model rule -----------------------------------------

// A zen id marked deprecated is dropped from the pick (and counted); an id
// whose model row says another model answers it is excluded even though a row
// would rank it first, and as the only candidate it never launches.
// FAIL-first: without the dropMapped case, the mapped id is picked (its row
// ranks it first).
func TestFreePickStatusAndMapping(t *testing.T) {
	zen := mustProvider(t, "zen")
	deprecated := freeEntry("zen", "bigger-free", 2000000, nnr)
	deprecated.Status = "deprecated"
	active := freeEntry("zen", "active-free", 262144, nnr)
	active.Status = "active"
	useCatalog(t, deprecated, active)
	got, refusal := resolveFree(freeRequest{p: zen, harness: "opencode", cwd: t.TempDir(), cfg: cfgFrom(t, `{}`), now: time.Now()})
	if refusal != "" || got.model != "opencode/active-free" || got.counts.withdrawn != 1 {
		t.Fatalf("zen: pick %q (withdrawn %d), refusal %q; want opencode/active-free with 1 withdrawn", got.model, got.counts.withdrawn, refusal)
	}

	g := newFreeRig(t)
	userConfig(t, `{}`)
	swapModelTable(t, model{provider: "openrouter", id: "vendor/mapped:free", answeredBy: "vendor/other"})
	useCatalog(t,
		freeEntry("openrouter", "vendor/mapped:free", 1000000, nnr),
		freeEntry("openrouter", "vendor/honest:free", 262144, nnr),
	)
	child := captureDetach(t)
	if rc, errs := g.run("--detach", "--provider", "openrouter", "--model", "free"); rc != 0 || argAfter(*child, "--model") != "vendor/honest:free" {
		t.Fatalf("mapped beside an honest id: rc=%d model %q; stderr=%s", rc, argAfter(*child, "--model"), errs)
	}
	useCatalog(t, freeEntry("openrouter", "vendor/mapped:free", 1000000, nnr))
	rc, errs := g.run("--detach", "--provider", "openrouter", "--model", "free")
	if rc != ExitUsage || !strings.Contains(errs, "1 answered by another model") {
		t.Fatalf("the mapped id alone: rc=%d stderr=%s", rc, errs)
	}
	if n := g.registered(); n != 0 {
		t.Fatalf("%d round(s) registered", n)
	}
}

// ---- 8. OUTSOURCE_CATALOG=off -------------------------------------------------------

// The switch, through the seam's own default (no fixture; TestMain's fence
// stands behind it): a named id launches with the note and the tables'
// window, --model free exits 64 saying the catalogue is off, and `outsource
// models` exits 1. FAIL-first: without the switch in loadCatalogUnlessOff,
// every leg reaches catalogueFence and panics.
func TestCatalogueOffSwitch(t *testing.T) {
	g := newFreeRig(t)
	userConfig(t, `{}`)
	t.Setenv(catalogueSwitchEnv, "off")
	rc, errs := g.run("--foreground", "--provider", "openrouter", "--model", "vendor/x:free")
	if rc != 0 || !strings.Contains(errs, "outsource: catalogue pre-flight skipped for vendor/x:free on openrouter — the catalogue is switched off (OUTSOURCE_CATALOG=off)") {
		t.Fatalf("named id: rc=%d stderr=%s", rc, errs)
	}
	if env := childEnv(t, g.envOut); env["CLAUDE_CODE_MAX_CONTEXT_TOKENS"] != "" || env["CLAUDE_CODE_AUTO_COMPACT_WINDOW"] != "" {
		t.Fatalf("the tables give openrouter no window, yet the harness got %q / %q", env["CLAUDE_CODE_MAX_CONTEXT_TOKENS"], env["CLAUDE_CODE_AUTO_COMPACT_WINDOW"])
	}
	rc, errs = g.run("--detach", "--provider", "openrouter", "--model", "free")
	if want := "--model free cannot pick: the catalogue is switched off (OUTSOURCE_CATALOG=off)\n"; rc != ExitUsage || !strings.HasSuffix(errs, want) {
		t.Fatalf("--model free: rc=%d stderr=%q, want %d ending %q", rc, errs, ExitUsage, want)
	}
	rc, _, mErr := runModels(t, "--free")
	if rc != 1 || mErr != "models: the catalogue is switched off (OUTSOURCE_CATALOG=off)\n" {
		t.Fatalf("outsource models: rc=%d stderr=%q, want 1 saying the catalogue is off", rc, mErr)
	}
}

// ---- 9. bareID ------------------------------------------------------------------------

// bareID strips the harness's qualifier only where its modelForm qualifies,
// and contextWindowFor reads through it: OpenRouter's own openrouter/<id>
// on claude-code is that id, not a qualified "<id>". FAIL-first: with
// contextWindowFor's old strings.TrimPrefix, the openrouter/probe-alpha row
// is looked up as "probe-alpha" and its 333333 window is lost (0).
func TestBareID(t *testing.T) {
	or, zen, zai := mustProvider(t, "openrouter"), mustProvider(t, "zen"), mustProvider(t, "zai")
	for _, c := range []struct {
		p              provider
		harness, model string
		want           string
	}{
		{or, "claude-code", "openrouter/auto", "openrouter/auto"},
		{or, "claude-code", "nvidia/nemotron-3-ultra-550b-a55b:free", "nvidia/nemotron-3-ultra-550b-a55b:free"},
		{or, "opencode", "openrouter/z-ai/glm-5.3", "z-ai/glm-5.3"},
		{or, "opencode", "openrouter/openrouter/auto", "openrouter/auto"},
		{zen, "opencode", "opencode/x", "x"},
		{zen, "opencode", "step-5-preview-free", "step-5-preview-free"},
		{zai, "crush", "zai/glm-5.3", "glm-5.3"},
		{zai, "claude-code", "glm-5.3", "glm-5.3"},
	} {
		if got := bareID(c.p, c.harness, c.model); got != c.want {
			t.Errorf("bareID(%s, %s, %q) = %q, want %q", c.p.name, c.harness, c.model, got, c.want)
		}
		if got := harnessFormID(c.p, c.harness, c.want); bareID(c.p, c.harness, got) != c.want {
			t.Errorf("harnessFormID(%s, %s, %q) = %q does not strip back", c.p.name, c.harness, c.want, got)
		}
	}
	swapModelTable(t, model{provider: "openrouter", id: "openrouter/probe-alpha", contextWindow: 333333})
	if got := contextWindowFor(or, "openrouter/probe-alpha"); got != 333333 {
		t.Fatalf("contextWindowFor(openrouter, openrouter/probe-alpha) = %d, want the row's 333333", got)
	}
	// The derived harness set is the table's.
	for _, h := range harnessTable {
		if qualifyingHarness[h.name] != (h.modelForm != nil) {
			t.Errorf("qualifyingHarness[%s] = %v, but its modelForm is set: %v", h.name, qualifyingHarness[h.name], h.modelForm != nil)
		}
	}
}

// ---- 10. isolation -------------------------------------------------------------------

// TestMain's catalogue floors: a private XDG_CACHE_HOME, the switch off, and
// a fence where catalog.Load would be reached from the seam's default — which
// fails the binary when a test lifts the switch without a fixture.
// FAIL-first: remove the three floor lines from TestMain and this names each
// (and the cache resolves to the developer's ~/.cache).
func TestCatalogueIsolationFloors(t *testing.T) {
	private := filepath.Dir(os.Getenv("OUTSOURCE_RUNS_DIR"))
	if dir := catalog.CacheDir(); !strings.HasPrefix(dir, private+string(filepath.Separator)) {
		t.Errorf("the launch tests' catalogue cache is %s, want a path under the package's private %s", dir, private)
	}
	if v := os.Getenv(catalogueSwitchEnv); v != "off" {
		t.Errorf("%s = %q in the launch tests, want off", catalogueSwitchEnv, v)
	}
	if reflect.ValueOf(catalogNetwork).Pointer() == reflect.ValueOf(catalog.Load).Pointer() {
		t.Fatal("the seam's default reaches catalog.Load — the real network — in the launch tests")
	}
	if reflect.ValueOf(loadCatalog).Pointer() != reflect.ValueOf(loadCatalogUnlessOff).Pointer() {
		t.Fatal("a test left its catalogue fixture installed")
	}
	t.Setenv(catalogueSwitchEnv, "")
	defer func() {
		r := recover()
		if r == nil || !strings.Contains(r.(string), "a test reached the real catalogue loader") {
			t.Fatalf("lifting the switch with no fixture must hit the fence, got %v", r)
		}
	}()
	loadCatalog(context.Background(), catalog.Options{Providers: []string{catalog.OpenRouter}})
}

// ---- E. outsource models --pick ---------------------------------------------------------

// `outsource models --pick free` prints what --model free would choose with
// the launcher's own resolver: the same pick line (without the launcher's
// prefix), every filter's count and the ranked candidates; exit 0 with a
// pick, 64 without. FAIL-first: a --pick that ignored --allow-free-training
// prints vendor/clean:free in the second leg.
func TestModelsPick(t *testing.T) {
	userConfig(t, `{}`)
	useCatalog(t,
		freeEntry("openrouter", "vendor/clean:free", 262144, nnr),
		freeEntry("openrouter", "vendor/big:free", 1000000, trains),
		freeEntry("zen", "zen-clean-free", 300000, nnr),
	)
	cwd := t.TempDir()
	rc, out, errOut := runModels(t, "--pick", "free", "--provider", "openrouter", "--cwd", cwd)
	if rc != 0 {
		t.Fatalf("rc=%d stderr=%s", rc, errOut)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 3 ||
		!strings.HasPrefix(lines[0], "openrouter: --model free → vendor/clean:free (no-train-no-retain, 262144 ctx; 1 candidates; catalogue network ") ||
		lines[1] != "openrouter: 2 listed; 0 not free, 0 routers, 0 no tools, 0 expired, 0 withdrawn, 0 under 128000 ctx, 0 answered by another model, 1 data policy (allow with free.allowTraining or --allow-free-training)" ||
		lines[2] != "openrouter: candidates in rank order: vendor/clean:free" {
		t.Fatalf("--pick output:\n%s", out)
	}
	if _, out, _ = runModels(t, "--pick", "free", "--provider", "openrouter", "--allow-free-training"); !strings.Contains(out, "→ vendor/big:free (trains, 1000000 ctx; 2 candidates;") {
		t.Fatalf("--allow-free-training:\n%s", out)
	}
	// Both catalogues when --provider is absent, each in its default
	// harness's form.
	if _, out, _ = runModels(t, "--pick", "free"); !strings.Contains(out, "openrouter: --model free → vendor/clean:free") || !strings.Contains(out, "zen: --model free → opencode/zen-clean-free") {
		t.Fatalf("both catalogues:\n%s", out)
	}
	// No pick: the launcher's refusal on stderr, exit 64.
	useCatalog(t, freeEntry("openrouter", "vendor/big:free", 1000000, trains))
	rc, _, errOut = runModels(t, "--pick", "free", "--provider", "openrouter")
	if rc != ExitUsage || !strings.Contains(errOut, "openrouter: --model free: no candidate on openrouter — 1 listed;") {
		t.Fatalf("no pick: rc=%d stderr=%s", rc, errOut)
	}
	// A denied --cwd is the launch's refusal too.
	home := t.TempDir()
	t.Setenv("HOME", home)
	os.MkdirAll(filepath.Join(home, "work"), 0o755)
	userConfig(t, `{"free": {"denyPaths": ["~/work/**"]}}`)
	if rc, _, errOut := runModels(t, "--pick", "free", "--provider", "openrouter", "--cwd", filepath.Join(home, "work")); rc != ExitUsage || !strings.Contains(errOut, "free.denyPaths") {
		t.Fatalf("denied cwd: rc=%d stderr=%s", rc, errOut)
	}
	for _, args := range [][]string{{"--pick", "paid"}, {"--pick"}, {"--cwd", "x"}, {"--allow-free-training"}, {"--pick", "free", "--json"}, {"--pick", "free", "--free"}} {
		if rc, _, _ := runModels(t, args...); rc != ExitUsage {
			t.Errorf("%v: rc=%d, want %d", args, rc, ExitUsage)
		}
	}
}

// ---- gate feedback (2026-10-09) ----------------------------------------------------

// free.denyPaths fails closed: when the catalogue cannot answer — switched
// off, nothing loaded, or a stale cache that does not list the id — a named
// id on a catalogue provider in a denied cwd is refused, since the launcher
// cannot tell whether it is free. A table default counts as named. Outside a
// denied cwd the skip stays a note. FAIL-first: without the deny check in
// preflightNamed's skip, every denied leg launches (rc 0).
func TestNamedIdFailsClosedUnderDenyPaths(t *testing.T) {
	g := newFreeRig(t)
	home := filepath.Join(g.dir, "home")
	work := filepath.Join(home, "work", "proj")
	outside := filepath.Join(home, "other")
	for _, d := range []string{work, outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	path := userConfig(t, `{"free": {"denyPaths": ["~/work/**"]}}`)
	captureDetach(t)
	launch := func(cwd string, args ...string) (int, string) {
		t.Helper()
		g.cwd = cwd
		return g.run(append([]string{"--detach"}, args...)...)
	}
	denied := func(model, because string) string {
		return "outsource: --model " + model + " refused — the launcher cannot tell whether " + model + " is free because " + because +
			", and the round's cwd " + work + ` is under free.denyPaths "~/work/**" in ` + path
	}

	// Switched off: the seam's own default, no fixture.
	t.Setenv(catalogueSwitchEnv, "off")
	rc, errs := launch(work, "--provider", "openrouter", "--model", "vendor/x:free")
	if want := denied("vendor/x:free", "the catalogue is switched off (OUTSOURCE_CATALOG=off)"); rc != ExitUsage || !strings.Contains(errs, want) {
		t.Fatalf("off, denied cwd: rc=%d stderr=%s\nwant %d with %s", rc, errs, ExitUsage, want)
	}
	if rc, errs := launch(work, "--provider", "zen"); rc != ExitUsage || !strings.Contains(errs, "--model step-5-preview-free refused — the launcher cannot tell") {
		t.Fatalf("off, denied cwd, zen's table default: rc=%d stderr=%s", rc, errs)
	}
	if rc, errs := launch(outside, "--provider", "openrouter", "--model", "vendor/x:free"); rc != 0 || !strings.Contains(errs, "catalogue pre-flight skipped for vendor/x:free") {
		t.Fatalf("off, cwd outside: rc=%d stderr=%s, want the note and a launch", rc, errs)
	}

	// Nothing loaded.
	fx := useCatalog(t)
	fx.serve = func(catalog.Options) ([]catalog.Entry, catalog.Source) {
		return nil, catalog.Source{Err: errors.New("dial tcp: no route to host")}
	}
	if rc, errs := launch(work, "--provider", "openrouter", "--model", "vendor/x:free"); rc != ExitUsage || !strings.Contains(errs, denied("vendor/x:free", "dial tcp: no route to host")) {
		t.Fatalf("unavailable, denied cwd: rc=%d stderr=%s", rc, errs)
	}

	// A stale cache that does not list the id.
	fx.serve = func(catalog.Options) ([]catalog.Entry, catalog.Source) {
		return []catalog.Entry{freeEntry("openrouter", "vendor/other:free", 262144, "")},
			catalog.Source{From: catalog.FromCache, FetchedAt: time.Now().Add(-3 * time.Hour), Err: errors.New("HTTP 502")}
	}
	if rc, errs := launch(work, "--provider", "openrouter", "--model", "vendor/x:free"); rc != ExitUsage || !strings.Contains(errs, "the launcher cannot tell whether vendor/x:free is free because vendor/x:free is not in the cached catalogue") || !strings.Contains(errs, "HTTP 502") {
		t.Fatalf("stale cache, denied cwd: rc=%d stderr=%s", rc, errs)
	}
	if rc, errs := launch(outside, "--provider", "openrouter", "--model", "vendor/x:free"); rc != 0 || !strings.Contains(errs, "catalogue pre-flight skipped") {
		t.Fatalf("stale cache, cwd outside: rc=%d stderr=%s", rc, errs)
	}
	if n := g.registered(); n != 0 {
		t.Fatalf("%d round(s) registered", n)
	}
}

// --session with --model free is refused before anything loads: a resumed
// session keeps the model it ran, and a pick could land on another. A named
// id with --session, and --model free without it, pass. FAIL-first: without
// the session case, the free launch resolves and runs (rc 0, one load).
func TestFreeWithSessionIsRefused(t *testing.T) {
	g := newFreeRig(t)
	userConfig(t, `{}`)
	captureDetach(t)
	fx := useCatalog(t, freeEntry("openrouter", "vendor/clean:free", 262144, nnr))
	rc, errs := g.run("--detach", "--provider", "openrouter", "--model", "free", "--session", "11111111-2222-4333-8444-555555555555")
	if rc != ExitUsage || !strings.Contains(errs, "--session resumes a session with the model it ran, and --model free would pick again — pass that id instead (the earlier round's sentinel has it as model_requested=)") {
		t.Fatalf("--model free --session: rc=%d stderr=%s", rc, errs)
	}
	if len(fx.calls) != 0 {
		t.Fatalf("the refusal must come before anything loads; %d load(s)", len(fx.calls))
	}
	if rc, errs := g.run("--detach", "--provider", "openrouter", "--model", "vendor/clean:free", "--session", "11111111-2222-4333-8444-555555555555"); rc != 0 {
		t.Fatalf("a named id with --session: rc=%d stderr=%s", rc, errs)
	}
	if rc, errs := g.run("--detach", "--provider", "openrouter", "--model", "free"); rc != 0 {
		t.Fatalf("--model free without --session: rc=%d stderr=%s", rc, errs)
	}
}

// Every variable contextEnv can hand a claude-code round is one the shell
// suites scrub (tests/hermetic-env.sh): a round's own shell carries them, and
// an inherited one is a pin the launcher keeps. FAIL-first: drop
// CLAUDE_CODE_AUTO_COMPACT_WINDOW from hermetic_env_names and this names it.
func TestHermeticScrubCoversTheContextVariables(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "tests", "hermetic-env.sh"))
	if err != nil {
		t.Fatal(err)
	}
	start := strings.Index(string(b), "hermetic_env_names=(")
	if start < 0 {
		t.Fatal("tests/hermetic-env.sh has no hermetic_env_names=( list")
	}
	list := string(b)[start:]
	list = list[:strings.Index(list, ")")]
	names := map[string]bool{}
	for _, f := range strings.Fields(list) {
		names[f] = true
	}
	env := contextEnv(mustProvider(t, "zai"), "glm-5.3", 0, 600000, func(string) string { return "" })
	if len(env) != 2 {
		t.Fatalf("zai glm-5.3 should get both context variables, got %v", env)
	}
	for _, e := range env {
		k, _, _ := strings.Cut(e, "=")
		if !names[k] {
			t.Errorf("contextEnv sets %s, which tests/hermetic-env.sh does not scrub", k)
		}
	}
}

// catalog.Entry.Active is the one rule for a catalogue's status word, read by
// the pick (withdrawn), the named-id warning and `outsource models`' NOTES.
// FAIL-first: an Active that answers true for every word lets the
// deprecated and beta rows through here and drops "status deprecated" from
// the NOTES.
func TestEntryActive(t *testing.T) {
	for status, want := range map[string]bool{"": true, "active": true, "deprecated": false, "beta": false} {
		if got := (catalog.Entry{Status: status}).Active(); got != want {
			t.Errorf("Entry{Status: %q}.Active() = %v, want %v", status, got, want)
		}
	}
	if n := modelNotes(catalog.Entry{Provider: "zen", ID: "old-free", Status: "deprecated"}); !strings.Contains(n, "status deprecated") {
		t.Errorf("NOTES for a deprecated id: %q", n)
	}
	if n := modelNotes(catalog.Entry{Provider: "zen", ID: "live-free", Status: "active"}); strings.Contains(n, "status") {
		t.Errorf("NOTES for an active id: %q", n)
	}
}
