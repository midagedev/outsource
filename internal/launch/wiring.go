package launch

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/midagedev/outsource/internal/tail"
)

// This file is the single owner of "what can run where".
//
// Three tables answer every routing question this launcher has: `providerTable`
// says which models an account offers and how they behave, `harnessTable` says
// which CLI drives them headlessly, and `modelTable` owns what was measured
// about a single model id — its vision level, whether the endpoint silently
// answers it with another id, and its context window when it differs from the
// provider's. Everything downstream DERIVES from them —
// the harness-name validation, the detach PATH lookup, the progress-trail
// location, the dispatch, the pairing matrix, the help text. Adding an arm is
// one row; withdrawing one is one row.
//
// Before this file those answers lived in nine separate places inside
// outsource.go (a provider table, a vision map plus a function, two
// hand-written switches over harness names, a pairing switch, a default-harness
// switch, a model-seed switch, a detach binary switch, a progress-dir switch),
// and a half-wired arm was something only a reader could catch. The
// consistency test in wiring_test.go now catches it.
//
// Every measured fact below carries the date it was measured on. Those comments
// are this repo's memory: they are the reason a column has the value it has, and
// they move with the value, never away from it.

// ---- providers -------------------------------------------------------------

// provider is one account/endpoint the launcher can route a round to.
//
// Credentials for zai/xai live in internal/cred (env var first, then this
// skill's 0600 store, then discovery of files another tool already wrote).
// openrouter does not: opencode owns its own auth store
// (~/.local/share/opencode/auth.json), and a cred row would be a second
// owner of a secret this launcher never touches. agy likewise: the Google
// Antigravity CLI is provider and harness in one, and auth lives in the
// Google plan.
//
// url is the provider's DEFAULT endpoint; cred.Base may point it at the same
// account's other region (z.ai's coding plan ships on api.z.ai globally and
// open.bigmodel.cn in mainland China). The column is consumed by the
// claude-code harness (ANTHROPIC_BASE_URL); the crush harness resolves
// endpoints through crush's own provider registry — measured: crush's built-in
// zai points at https://api.z.ai/api/coding/paas/v4, not the
// Anthropic-compatible URL, so forcing this column into `provider add` would
// break the working zai path. opencode and agy resolve endpoints themselves,
// so their url is empty for the same reason.
type provider struct {
	name string
	url  string

	// qualifier is the provider id a qualifying harness (crush, opencode)
	// writes before the model id in --model: crush's zai/<id>, opencode's
	// openrouter/<id>. EMPTY means "same as name" — true for every row but
	// zen, whose launcher name is zen while the id opencode's CLI uses for
	// OpenCode Zen is opencode ("opencode" is already the harness's name; a
	// provider and a harness are different things). qualifierOf is the single
	// owner of the mapping: a call site building p.name+"/" on its own stops
	// agreeing with it exactly where it matters — the identity assertion
	// compares the export's providerID against the qualifier.
	qualifier string

	// defaultModel is what a round runs when --model is absent. EMPTY means
	// this provider has no routable default and --model is required — see
	// requiredModelError, which refuses at launch rather than letting a
	// harness build a malformed id out of the empty string.
	defaultModel string

	// defaultHarness is the CLI that drives this provider when --harness is
	// absent. It must be a harness whose providers column lists this provider;
	// TestWiringTablesAgree asserts exactly that.
	defaultHarness string

	// modelEnv, when set, is an environment variable that seeds --model for
	// THIS provider only. GLM_DELEGATE_MODEL applied to every provider once
	// leaked a glm-* id into opencode's -m, which opencode then rejected.
	modelEnv string

	// unlistedVision is what the vision guard answers for an id that has no
	// modelTable row, or whose row is visionUnmeasured. It is the single
	// owner the vision guard asks — never a provider-name test at a call
	// site.
	unlistedVision bool

	// contextWindow is the real input ceiling, in tokens, for the
	// claude-code harness only. It is the FALLBACK for every id of this
	// provider: a modelTable row's non-zero contextWindow overrides it for
	// that id (contextWindowFor is the single owner of that rule). ZERO means
	// "not measured here" and nothing is set, leaving the CLI's own behaviour
	// untouched.
	//
	// It exists because the CLI's unknown-model default is not a display
	// value: it is ENFORCED client-side. Measured 2026-09-20 on zai — the CLI
	// does not know `glm-5.3`, applied a 200000 ceiling, and killed a ~215k
	// token prompt with "Prompt is too long" before any request left the
	// machine. The same content with the window set went through and the
	// endpoint reported 243868 input tokens. So a round reading a few large
	// files was dying against a limit its model does not have.
	//
	// The fallback lives on the provider row because the harness serves
	// several models and their windows differ; a per-id fact lives in
	// modelTable. A wrong number is worse than none, since it would refuse
	// work the model could do.
	contextWindow int

	// pairingNote explains why this provider is not wired on other harnesses.
	// It is appended to the pairing refusal so the message says why, not just
	// what.
	pairingNote string
}

var providerTable = []provider{
	{
		name:           "zai",
		url:            "https://api.z.ai/api/anthropic",
		defaultModel:   "glm-5.3",
		defaultHarness: "claude-code",
		modelEnv:       "GLM_DELEGATE_MODEL",
		// zai is the one provider whose ids were probed individually, so the
		// per-id vision levels live in modelTable; an id with no row (or an
		// unmeasured one) REFUSES a pixel verdict rather than guessing — that
		// was the old per-id list's answer too, and it stays the answer here.
		unlistedVision: false,
		// 1310720, the exact figure behind z.ai's "1M-token context window"
		// for GLM-5.3 and GLM-5.3-Flash (their model page), and what the CLI
		// reports verbatim once told. Measured to work: 243868 input tokens
		// accepted where 200000 had refused.
		contextWindow: 1310720,
	},
	{
		name:           "xai",
		url:            "https://api.x.ai",
		defaultModel:   "grok-4.6",
		defaultHarness: "claude-code",
		// No grok vision probe on record through this launcher; the guard
		// passes rather than refuses on a guess, as this row always has.
		unlistedVision: true,
	},
	{
		name: "openrouter",
		// EMPTY, since 2026-09-18. `stealth/union-alpha` held this slot from
		// 2026-09-16 and stopped serving on the 18th, exactly as the row said it
		// would: a probe round came back rc=1 with the endpoint's own 404 body —
		// "Thank you for participating in the Stealth Union Alpha testing period.
		// This model was Unbiased's Pareto." That is the second occupant of this
		// slot to lapse (stealth/ox-alpha, 2026-09-10, was unveiled as
		// glm-5.3-flash), and the pattern is now measured twice: an unnamed lab
		// puts a model up free while it evaluates, then withdraws it and names
		// it. The unveiled id is live and priced ($2.5/M in, $7.5/M out on
		// unbiased/pareto), so it is a model a caller may name — but it is not
		// free, and a pay-per-token default nobody asked for is the one thing
		// this column must not be. So the field goes back to empty and
		// requiredModelError resumes asking the caller for an id.
		defaultHarness: "opencode",
		// unlistedVision is true for the deferring reason rather than a
		// claim: OpenRouter is a catalogue, the caller names the id per round,
		// and this launcher keeps no capability table for ids it has not
		// probed. The guard's question is only "do pixels reach the model",
		// and on this harness the answer is measured yes — two shape probes
		// through opencode's read tool on the slot's last occupant, both
		// correct (a drawn "4", a drawn "T"). The harness carries pixels; which
		// id reads them is the caller's choice, now more literally than before.
		//
		// What is NOT safe to infer from that pass: colour. The same two
		// rounds read a uniform #1E50DC as "#560000, dark maroon-red" and a
		// uniform #E8A020 as "#F5F5F5, off-white" — wrong hue and wrong
		// lightness, reported with stated high confidence both times. Pixels
		// arriving is not colour fidelity, and a guard that only gates the
		// former must not be read as certifying the latter. references/
		// opencode.md carries this where a spec author will meet it.
		unlistedVision: true,
		pairingNote:    "opencode owns its own auth store and resolves endpoints itself, so there is no Anthropic-compatible URL and no cred row for openrouter",
	},
	{
		name:      "zen",
		qualifier: "opencode",
		// Measured 2026-10-09 (opencode CLI 1.18.21). OpenCode Zen is
		// opencode's own hosted provider; the id opencode's CLI uses for it is
		// `opencode`, which is why this row carries a qualifier — the launcher
		// name is zen because "opencode" is already the harness's name, and a
		// provider and a harness are different things. `opencode models
		// --refresh` lists opencode/step-5-preview-free ("Step 5 Preview
		// Free"); `opencode models opencode --verbose` shows for it: api url
		// https://opencode.ai/zen/v1, cost input 0 / output 0, limit context
		// 1000000 / output 65536, capabilities input text+image+video,
		// reasoning true, toolcall true, status active. A raw `opencode run
		// --format json --pure -m opencode/step-5-preview-free` answered rc=0
		// while opencode's auth.json held ONLY an openrouter key — free ids
		// need no Zen login, which is also why the credential preflight
		// (opencodeCredsMissing) does not gate this provider.
		//
		// The free window is limited. Zen's docs (https://opencode.ai/docs/zen/
		// fetched 2026-10-09) say "Step 5 Preview Free is free on OpenCode for
		// a limited time" and give no end date; the one-week figure comes from
		// OpenCode's announcement of 2026-10-09, not from the docs. When the
		// window ends: empty
		// defaultModel and add "zen" to emptyByDesign in wiring_test.go IN THE
		// SAME COMMIT — the lapse story the openrouter row above already
		// tells twice over.
		//
		// Privacy is per-id, not per-provider. The docs' zero-retention
		// sentence ("Its provider follows a zero-retention policy and does not
		// use your data for model training") is about THIS id; other free Zen
		// ids differ — big-pickle: "During its free period, collected data may
		// be used to improve the model"; the muse-spark contributor-free ids
		// trade training permission; nemotron: "Trial use only". Read the
		// id's own terms before routing another.
		defaultModel:   "step-5-preview-free",
		defaultHarness: "opencode",
		// Zen is a catalogue like openrouter: the caller names the id per
		// round and this launcher keeps no capability table for ids it has
		// not probed, so the guard defers (passes) — the same deferring
		// reason as openrouter's row, not a claim about any unlisted id.
		unlistedVision: true,
		// The api reports a 1000000 context limit, but this column exists for
		// the claude-code harness only and zen never runs there; 0 leaves the
		// opencode CLI's own limit untouched.
		contextWindow: 0,
		pairingNote:   "opencode owns its own auth store and resolves endpoints itself, so there is no Anthropic-compatible URL and no cred row for zen",
	},
	{
		name: "muse",
		// Muse Code is provider and harness in one, like agy: the model is
		// Meta's and auth is an OAuth device flow the CLI owns at
		// ~/.config/muse/auth.json, so there is no cred row and no URL here.
		//
		// The Anthropic-compatible endpoint is NOT the path, which is worth
		// saying because the CLI advertises one. Measured 2026-09-18: a direct
		// call to api.meta.ai/v1/messages carrying an API key answered
		// `billing_error`, while the CLI's own session ran in the same minute.
		defaultModel:   "muse-spark-1.3-contributor",
		defaultHarness: "muse",
		// muse-spark-1.3-contributor is the one routed id and carries its own
		// measured vision level in modelTable; any other muse id keeps the
		// pass this row has always given.
		unlistedVision: true,
		pairingNote:    "the muse CLI owns its own OAuth credentials and resolves its endpoint itself, so there is no Anthropic-compatible URL and no cred row for muse",
	},
	{
		name: "agy",
		// The Google Antigravity CLI is provider and harness in one — auth and
		// quota live in the Google plan, so no cred row and no URL.
		defaultModel:   "gemini-3.8-flash-high",
		defaultHarness: "agy",
		// Measured 2026-08-27 on gemini-3.7-flash-low: a solid #1E50DC PNG was
		// named "#1e50dc" exactly and a white-7 shape probe answered "7". The
		// default is the high effort tier by user decision 2026-08-27 ("flash는
		// high만 써") — medium/low exist but are not routed. The family moved to
		// 3.8 by user decision 2026-09-05 ("agy 최근 버전이 3.8로 올라왔는데
		// 앞으로 그거 쓰도록") the day `agy models` started listing it; the
		// vision and speed measurements above are 3.7's and have not been re-run
		// on 3.8.
		//
		// That is why unlistedVision is true and the routed id's modelTable
		// row says visionUnmeasured: the exact-hex result belongs to an id
		// (3.7-flash-low) this launcher no longer routes, so it explains the
		// fallback instead of claiming a measurement for 3.8.
		unlistedVision: true,
		pairingNote:    "the Antigravity CLI drives itself; there is no Anthropic-compatible URL",
	},
}

// The provider-column forms of the vision fact are gone: what a model does
// with pixels is a fact about the model, and it lives in modelTable now. A
// provider keeps only unlistedVision, the answer for ids nobody has measured.

func findProvider(name string) (provider, bool) {
	for _, p := range providerTable {
		if p.name == name {
			return p, true
		}
	}
	return provider{}, false
}

// qualifierOf is the single owner of "which id the CLI writes before the
// model id": the row's qualifier when it declares one, the provider's own
// name otherwise. Every qualifier use goes through it — the harness model
// form rules, the qualified-model parsing, and the form hints — so a row
// whose launcher name and CLI id differ (zen) changes one column, not every
// call site.
func qualifierOf(p provider) string {
	if p.qualifier != "" {
		return p.qualifier
	}
	return p.name
}

// providerNameList is the routable provider names, in table order. Every
// human-facing list of providers derives from it, so a new row cannot be
// routable and undocumented at the same time.
func providerNameList() []string {
	out := make([]string, 0, len(providerTable))
	for _, p := range providerTable {
		out = append(out, p.name)
	}
	return out
}

func providerNames() string {
	return strings.Join(providerNameList(), " ") + " "
}

// seedModel applies the provider's own model environment variable. An explicit
// --model always wins, and a provider without modelEnv ignores every such var.
func seedModel(providerName, model string) string {
	if model != "" {
		return model
	}
	p, ok := findProvider(providerName)
	if !ok || p.modelEnv == "" {
		return ""
	}
	return os.Getenv(p.modelEnv)
}

// ---- models -----------------------------------------------------------------

// visionLevel is what a measured probe showed a model can do with pixels.
type visionLevel int

const (
	visionUnmeasured   visionLevel = iota // no probe on record: the provider's unlistedVision decides
	visionBlind                           // probe showed it does not see pixels
	visionShape                           // reads shape; colour answers were wrong
	visionColourFamily                    // reads shape and names the colour family; exact hex is off
	visionExactHex                        // named a uniform fill's hex exactly
)

// model is one id a provider routes, with what was measured about it.
type model struct {
	provider      string      // a providerTable name
	id            string      // the bare id, as the endpoint takes it
	vision        visionLevel // zero value = unmeasured
	contextWindow int         // claude-code harness only; 0 = use the provider's
	answeredBy    string      // non-empty: the endpoint silently answers this id with answeredBy — refused at launch
}

// modelTable is the model axis of the wiring: facts that belong to one
// (provider, id) pair rather than to an account or a CLI. The derived guards
// below (modelVision, mappedModelError, contextWindowFor) are its only
// readers, and no call site tests a provider name. openrouter has no rows
// here on purpose — it is a catalogue, the caller names the id per round, and
// its provider row's unlistedVision carries the deferral.
var modelTable = []model{
	{
		provider: "zai",
		id:       "glm-5.3",
		// Only the DEFAULT is blind. Measured 2026-08-27 on a white-7-on-black
		// probe: glm-5.3 answered "Y" while glm-5.3-flash answered "7", and
		// through the claude-code harness's Read tool flash also named a solid
		// #1E50DC fill as #2244DD (per-channel error ~5%).
		vision: visionBlind,
	},
	{
		provider: "zai",
		id:       "glm-5.3-flash",
		// The probe is the 2026-08-27 one on the glm-5.3 row above: flash
		// answered "7" on the white-7 shape probe and named a #1E50DC fill as
		// #2244DD (~5% per channel). flash is the officially unveiled ox-alpha, whose vision this skill
		// had already measured on OpenRouter. Colour fidelity on the raw API
		// (no harness) was weaker in one probe — treat flash as reliable for
		// shape/layout/presence and usable-but-verify for exact colour.
		vision: visionColourFamily,
	},
	{
		provider: "zai",
		id:       "glm-5.2",
		// Measured 2026-08-27 (two probes each): the response's `model` field
		// came back "glm-5.3" for a "glm-5.2" request — the field is not an
		// echo, because it differs from the request — while glm-5.3,
		// glm-5.3-flash and glm-4.6 were honoured verbatim and a nonexistent id
		// (glm-5.2-flash) errored loudly (code 1214). So a glm-5.2 round can
		// never be a glm-5.2 round: on claude-code the identity assertion would
		// burn the whole round and then exit 70; on crush there is no assertion
		// at all and the misassignment would be permanent and silent. Refusing
		// at launch is the only guard that covers both harnesses.
		vision:     visionUnmeasured,
		answeredBy: "glm-5.3",
	},
	{
		provider: "xai",
		id:       "grok-4.6",
		// No probe on record; the xai provider row's unlistedVision answers
		// for this id, as it does for every id without a row.
		vision: visionUnmeasured,
	},
	{
		provider: "zen",
		id:       "step-5-preview-free",
		// Measured 2026-10-09 through opencode's read tool (--auto, sequential
		// probes): a drawn white `7` on black → `7` (high confidence); a
		// drawn `L` → `L` (high); a uniform #1E50DC fill → `#3A5BF0`, "Royal
		// blue" (medium) — right colour family, per-channel error 4–11%, so
		// the standing rule applies: shape and colour family, not exact hex.
		// Two hazards measured in the same session: one probe answered `S`
		// with high confidence WITHOUT calling read at all (no tool_use in
		// its log) — a verdict with no read tool call in the log is not a
		// verdict — and one colour probe computed the hex with a bash/PIL
		// script instead of looking. Judge the transcript, not only the
		// answer.
		vision: visionColourFamily,
	},
	{
		provider: "muse",
		id:       "muse-spark-1.3-contributor",
		// Measured 2026-09-18 through the CLI's own read tool: a drawn white
		// "H" on black was read back as "H", and a uniform #1E50DC fill as
		// "#0000FF, blue" — the right colour NAME with the exact value well
		// off. So: shape, layout and colour family yes; exact hex no, the same
		// standing rule this skill applies everywhere. Unlike the openrouter
		// row this is one known model rather than a catalogue, so the column
		// states a measurement instead of deferring.
		vision: visionColourFamily,
	},
	{
		provider: "agy",
		id:       "gemini-3.8-flash-high",
		// 3.8 is routed (user decision 2026-09-05) but its vision has not been
		// re-measured; the agy provider row keeps the 3.7-flash-low result,
		// which is why unlistedVision is true there.
		vision: visionUnmeasured,
	},
}

// findModel is the modelTable lookup every model-axis guard goes through. id
// is the BARE form; the callers strip the provider qualifier (crush's zai/…).
func findModel(providerName, id string) (model, bool) {
	for _, m := range modelTable {
		if m.provider == providerName && m.id == id {
			return m, true
		}
	}
	return model{}, false
}

// modelVision answers "can THIS round see pixels". model may be
// provider-qualified (crush's zai/…, opencode's openrouter/… and, for zen,
// opencode/… — qualifierOf owns the prefix); an empty model resolves to the
// provider's default. A row with a measured vision level answers for its id
// (anything above blind sees); everything else falls to the provider's
// unlistedVision.
func modelVision(p provider, model string) bool {
	bare := strings.TrimPrefix(model, qualifierOf(p)+"/")
	if bare == "" {
		bare = p.defaultModel
	}
	if m, ok := findModel(p.name, bare); ok && m.vision != visionUnmeasured {
		return m.vision != visionBlind
	}
	return p.unlistedVision
}

// contextWindowFor is the claude-code harness's input ceiling for one id: the
// model row's window when it declares one, else the provider's fallback. An
// unlisted id (zai's glm-4.6) gets the provider column exactly as before the
// model axis existed.
func contextWindowFor(p provider, model string) int {
	bare := strings.TrimPrefix(model, qualifierOf(p)+"/")
	if bare == "" {
		bare = p.defaultModel
	}
	if m, ok := findModel(p.name, bare); ok && m.contextWindow > 0 {
		return m.contextWindow
	}
	return p.contextWindow
}

// mappedModelError refuses, at launch, a model id measured to be silently
// answered by a different model (the model row's answeredBy). model may be
// bare (claude-code) or provider-qualified (crush's zai/…).
// OUTSOURCE_ALLOW_MAPPED_MODEL=1 overrides — that exists for re-measuring the
// mapping, not for routing.
func mappedModelError(p provider, model string) (string, bool) {
	if model == "" {
		return "", true
	}
	bare := strings.TrimPrefix(model, qualifierOf(p)+"/")
	m, mapped := findModel(p.name, bare)
	if !mapped || m.answeredBy == "" || os.Getenv("OUTSOURCE_ALLOW_MAPPED_MODEL") == "1" {
		return "", true
	}
	return fmt.Sprintf("--model %s is silently answered by %s on the %s endpoint (measured: the response model field differs from the request). The round could never run the model you asked for — request %s explicitly, or set OUTSOURCE_ALLOW_MAPPED_MODEL=1 to re-measure the mapping.",
		model, m.answeredBy, p.name, m.answeredBy), false
}

// requiredModelError refuses a provider with no routable default when --model
// was not given. Without it the empty string reaches the harness and becomes a
// malformed id there — inside the --detach child, where nothing can print.
func requiredModelError(p provider, h harness, model string) (string, bool) {
	if model != "" || p.defaultModel != "" {
		return "", true
	}
	msg := fmt.Sprintf("provider %s has no default model — pass --model explicitly", p.name)
	if h.modelFormHint != "" {
		// <provider> becomes the provider's own qualifier, so the hint names
		// the form THIS provider's rounds take: zen renders opencode/<id> on
		// the opencode harness, not a generic placeholder and not zen/<id>.
		msg += fmt.Sprintf(" (form on the %s harness: %s)", h.name,
			strings.ReplaceAll(h.modelFormHint, "<provider>", qualifierOf(p)))
	}
	return msg + ".", false
}

// ---- harnesses -------------------------------------------------------------

// harness is one CLI that drives a model headlessly. The model is the point;
// the harness is only how it is driven.
type harness struct {
	name string

	// bin is the executable that must be on PATH. Looked up before a --detach
	// re-exec, because the child has no terminal to report a missing CLI to.
	bin string

	// providers is the pairing matrix, and the only thing that is. A provider
	// absent from this column cannot run on this harness.
	providers []string

	// progress reports where the round leaves a LIVE trail, so the registry can
	// tell a round that is working from one that is stuck without interrupting
	// either. It is not the --log file for every harness: the claude-code
	// harness writes that only at the end, so a healthy round shows an empty
	// log for its entire life.
	progress func(o opts) string

	// trail is the file a human can FOLLOW while the round is alive, as opposed
	// to progress, which only needs an mtime. nil means the round reveals it
	// itself and the registry learns it later: claude-code knows its transcript
	// path only once its first turn starts, which is why guessing the newest
	// .jsonl in progress/ was the previous answer and broke as soon as two
	// rounds shared a cwd (reported 2026-09-15).
	trail func(o opts) string

	// trailFormat names how that file reads, so `outsource tail` renders it
	// instead of guessing. Every row declares one, and it must be a format the
	// renderer knows — TestEveryHarnessDeclaresARenderableTrail holds that.
	trailFormat string

	// run dispatches the round. One method per harness file.
	run func(*round) int

	// modelForm is the harness's own rule about the SHAPE of --model, checked
	// in OutsourceMain BEFORE the --detach re-exec. It takes the provider row
	// because the shape is <qualifier>/<id> and the qualifier is the
	// provider's, not the harness's — zen's rounds on the opencode harness
	// are opencode/<id>. Past that re-exec there is no caller left to tell:
	// that is how an unqualified --model once came back as "detached (pid=…)"
	// and exit 0 over a round that was already dead (measured 2026-08-26,
	// crush). nil means the harness accepts a bare id.
	modelForm func(model string, p provider) (msg string, ok bool)

	// effortFlag says this harness accepts a reasoning-effort level, so
	// --effort is honoured rather than refused. A column and not a harness-name
	// comparison at the call site: that comparison was written when
	// claude-code was the only one, and muse (--reasoning-effort) made it wrong
	// the day it was added.
	effortFlag bool

	// modelFormHint renders that rule for a human, in the message that asks for
	// a --model this launcher has no default for. The token <provider> is
	// replaced with the provider's qualifier at both consumers
	// (requiredModelError, renderWiring), so one hint serves every provider
	// a harness drives.
	modelFormHint string

	// resumeOnReset says this harness leaves a plan-limit (HTTP 429) death the
	// launcher can read (internal/planlimit) and a session it can resume, so
	// --resume-on-reset is honoured rather than refused. claude-code only, as
	// of 2026-10-06: its transcript marks the error line (isApiErrorMessage,
	// apiErrorStatus 429) and `claude -p --resume <sid>` continues the session.
	resumeOnReset bool
}

var harnessTable = []harness{
	{
		name:      "claude-code",
		bin:       "claude",
		providers: []string{"zai", "xai"},
		// The harness writes into projects/**.jsonl every turn.
		progress: func(o opts) string { return filepath.Join(o.configDir, "claude", "projects") },
		// Which of those .jsonl files is THIS round's is reported by the round
		// itself, through the SessionStart hook in its generated settings.
		trail:       nil,
		trailFormat: tail.FormatClaudeTranscript,
		effortFlag:  true,
		run:         (*round).runClaudeCodeResuming,

		// Its transcript marks a plan-limit death and its session resumes.
		resumeOnReset: true,
	},
	{
		name:      "crush",
		bin:       "crush",
		providers: []string{"zai", "xai"},
		// crush writes into crush.db-wal and logs/crush.log every few seconds.
		progress: func(o opts) string { return filepath.Join(o.configDir, "data") },
		// That log is the readable half of it, and it is plain text: shown
		// verbatim rather than described as something it is not.
		trail:         func(o opts) string { return filepath.Join(o.configDir, "data", "logs", "crush.log") },
		trailFormat:   tail.FormatLines,
		run:           (*round).runCrush,
		modelForm:     crushModelFormError,
		modelFormHint: "<provider>/<id>",
	},
	{
		name:      "opencode",
		bin:       "opencode",
		providers: []string{"openrouter", "zen"},
		// `--format json` flushes one JSONL event at a time onto --log while the
		// process is still running (measured 2026-08-23), so the log file itself
		// is the live trail.
		progress:      func(o opts) string { return o.log },
		trail:         func(o opts) string { return o.log },
		trailFormat:   tail.FormatOpencodeEvents,
		run:           (*round).runOpencode,
		modelForm:     opencodeModelFormError,
		modelFormHint: "<provider>/<id>",
	},
	{
		name:      "agy",
		bin:       "agy",
		providers: []string{"agy"},
		// stream-json events land on --log as the round progresses, same as
		// opencode. The event shape this launcher parses is init/result only, so
		// the trail is shown verbatim rather than half-decoded.
		trail:       func(o opts) string { return o.log },
		trailFormat: tail.FormatLines,
		progress:    func(o opts) string { return o.log },
		run:         (*round).runAgy,
	},
	{
		name:      "muse",
		bin:       "muse",
		providers: []string{"muse"},
		// `muse exec --json` flushes one JSONL envelope at a time onto --log
		// while the process is still running (measured 2026-09-18: the log grew
		// at 4s, 8s, 16s, 20s and 24s of a 24s round), so the log file is both
		// the progress signal and the readable trail.
		progress:    func(o opts) string { return o.log },
		trail:       func(o opts) string { return o.log },
		trailFormat: tail.FormatMuseEvents,
		// muse exec --reasoning-effort takes none|minimal|low|medium|high|
		// xhigh|max|ultra — a superset of this launcher's five names.
		effortFlag: true,
		run:        (*round).runMuse,
	},
}

// effortHarnesses is the harnesses that honour --effort, in table order, so a
// refusal names where the flag does work.
func effortHarnesses() []string {
	out := []string{}
	for _, h := range harnessTable {
		if h.effortFlag {
			out = append(out, h.name)
		}
	}
	return out
}

func findHarness(name string) (harness, bool) {
	for _, h := range harnessTable {
		if h.name == name {
			return h, true
		}
	}
	return harness{}, false
}

// harnessNameList is the wired harness names, in table order.
func harnessNameList() []string {
	out := make([]string, 0, len(harnessTable))
	for _, h := range harnessTable {
		out = append(out, h.name)
	}
	return out
}

func harnessNames() string {
	return strings.Join(harnessNameList(), ", ")
}

// drives reports whether this harness is wired for that provider.
func (h harness) drives(providerName string) bool {
	for _, p := range h.providers {
		if p == providerName {
			return true
		}
	}
	return false
}

// harnessesFor lists the harnesses wired for a provider, so a refusal can say
// where the round SHOULD go instead of only where it cannot.
func harnessesFor(providerName string) []string {
	out := []string{}
	for _, h := range harnessTable {
		if h.drives(providerName) {
			out = append(out, h.name)
		}
	}
	return out
}

// defaultHarness resolves --harness from the provider table when the flag is
// absent.
func defaultHarness(providerName, harnessName string) string {
	if harnessName != "" {
		return harnessName
	}
	if p, ok := findProvider(providerName); ok && p.defaultHarness != "" {
		return p.defaultHarness
	}
	return "claude-code"
}

// pairingRefusal is the one-line reason a (harness, provider) pair is not
// wired; empty means the pair is allowed. Derived entirely from the providers
// column, so a pair cannot be refused here and accepted at dispatch. Checked
// before the registry records a round that was never going to launch.
func pairingRefusal(harnessName, providerName string) string {
	h, ok := findHarness(harnessName)
	if !ok {
		return fmt.Sprintf("unknown harness: %s (known: %s)", harnessName, harnessNames())
	}
	if h.drives(providerName) {
		return ""
	}
	msg := fmt.Sprintf("harness %s does not drive provider %s — %s drives: %s",
		h.name, providerName, h.name, strings.Join(h.providers, " "))
	if where := harnessesFor(providerName); len(where) > 0 {
		msg += fmt.Sprintf("; provider %s runs on: %s", providerName, strings.Join(where, " "))
	}
	if p, ok := findProvider(providerName); ok && p.pairingNote != "" {
		msg += " (" + p.pairingNote + ")"
	}
	return msg
}

// wiringMatrix renders the routable (provider, harness) cells with their
// defaults, then the per-id model facts under them. It is what `outsource-run
// --list-wiring` prints, so "what can run where" is one command rather than a
// read of this file.
func wiringMatrix() string {
	return renderWiring(providerTable, modelTable)
}

// renderWiring is the matrix over given provider and model tables, so a
// synthetic row can be rendered in a test without editing a live one.
func renderWiring(providers []provider, models []model) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%-12s %-14s %-28s %s\n", "PROVIDER", "HARNESS", "DEFAULT MODEL", "NOTES")
	for _, p := range providers {
		for _, hname := range harnessesFor(p.name) {
			def := p.defaultModel
			notes := []string{}
			if def == "" {
				def = "(--model required)"
			}
			if hname == p.defaultHarness {
				notes = append(notes, "default harness")
			}
			if h, ok := findHarness(hname); ok && h.modelFormHint != "" {
				notes = append(notes, "--model form "+strings.ReplaceAll(h.modelFormHint, "<provider>", qualifierOf(p)))
			}
			if p.modelEnv != "" {
				notes = append(notes, "seeds from $"+p.modelEnv)
			}
			fmt.Fprintf(&b, "%-12s %-14s %-28s %s\n", p.name, hname, def, strings.Join(notes, "; "))
		}
	}
	// The model axis under the pairing matrix: what was measured per id, and
	// what the vision guard answers for the ids nobody has measured.
	b.WriteString("\n")
	writeModelBlock(&b, providers, models)
	return b.String()
}

// writeModelBlock renders the modelTable half of --list-wiring. Rows are
// grouped by provider in providerTable order, model rows in modelTable order,
// each provider ending on its (other ids) line — the guard's answer for an
// unlisted id. Trailing spaces are trimmed per line so a notes-less row does
// not end in padding.
func writeModelBlock(b *strings.Builder, providers []provider, models []model) {
	b.WriteString("MODELS (measured per id; ids not listed fall back to the provider)\n")
	writeModelLine(b, "PROVIDER", "MODEL", "VISION", "CONTEXT", "NOTES")
	for _, p := range providers {
		for _, m := range models {
			if m.provider != p.name {
				continue
			}
			notes := []string{}
			if m.id == p.defaultModel {
				notes = append(notes, "default")
			}
			if m.answeredBy != "" {
				notes = append(notes, "refused at launch: answered by "+m.answeredBy)
			}
			writeModelLine(b, p.name, m.id, visionWord(m.vision),
				contextCell(contextWindowFor(p, m.id)), strings.Join(notes, "; "))
		}
		// The provider fallback verbatim: this line is about ids with no row,
		// and a default row's window override must not leak into it.
		guard := "guard refuses"
		if p.unlistedVision {
			guard = "guard passes"
		}
		writeModelLine(b, p.name, "(other ids)", guard, contextCell(p.contextWindow), "")
	}
}

func writeModelLine(b *strings.Builder, providerName, id, vision, context, notes string) {
	line := fmt.Sprintf("%-12s %-30s %-14s %-10s %s", providerName, id, vision, context, notes)
	b.WriteString(strings.TrimRight(line, " ") + "\n")
}

// visionWord is visionLevel's one rendering, so the probe vocabulary in
// --list-wiring cannot drift from the skill docs that define it.
func visionWord(v visionLevel) string {
	switch v {
	case visionBlind:
		return "blind"
	case visionShape:
		return "shape"
	case visionColourFamily:
		return "colour-family"
	case visionExactHex:
		return "exact-hex"
	default:
		return "unmeasured"
	}
}

func contextCell(w int) string {
	if w == 0 {
		return "-"
	}
	return strconv.Itoa(w)
}
