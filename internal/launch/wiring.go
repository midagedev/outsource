package launch

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/midagedev/outsource/internal/tail"
)

// This file is the single owner of "what can run where".
//
// Two tables answer every routing question this launcher has: `providerTable`
// says which models an account offers and how they behave, `harnessTable` says
// which CLI drives them headlessly. Everything downstream DERIVES from them —
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

	// vision answers "can this model see pixels", given the bare (unqualified)
	// model id. nil means no model on this provider can. It is the single
	// owner the vision guard asks — never a provider-name test at a call site.
	vision func(model string) bool

	// silentMappings records model ids the endpoint accepts WITHOUT error but
	// answers with a different model. Refused at launch: such a round could
	// never be the round that was asked for.
	silentMappings map[string]string

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
		// Only the DEFAULT is blind. Measured 2026-08-27 on a white-7-on-black
		// probe: glm-5.3 answered "Y" while glm-5.3-flash answered "7", and
		// through the claude-code harness's Read tool flash also named a solid
		// #1E50DC fill as #2244DD (per-channel error ~5%). flash is the
		// officially unveiled ox-alpha, whose vision this skill had already
		// measured on OpenRouter. Colour fidelity on the raw API (no harness)
		// was weaker in one probe — treat flash as reliable for
		// shape/layout/presence and usable-but-verify for exact colour.
		vision: visionModels("glm-5.3-flash"),
		// Measured 2026-08-27 (two probes each): the response's `model` field
		// came back "glm-5.3" for a "glm-5.2" request — the field is not an
		// echo, because it differs from the request — while glm-5.3,
		// glm-5.3-flash and glm-4.6 were honoured verbatim and a nonexistent id
		// (glm-5.2-flash) errored loudly (code 1214). So a glm-5.2 round can
		// never be a glm-5.2 round: on claude-code the identity assertion would
		// burn the whole round and then exit 70; on crush there is no assertion
		// at all and the misassignment would be permanent and silent. Refusing
		// at launch is the only guard that covers both harnesses.
		silentMappings: map[string]string{"glm-5.2": "glm-5.3"},
	},
	{
		name:           "xai",
		url:            "https://api.x.ai",
		defaultModel:   "grok-4.6",
		defaultHarness: "claude-code",
		vision:         visionAlways,
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
		// Still visionAlways, and still for the deferring reason rather than a
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
		vision:      visionAlways,
		pairingNote: "opencode owns its own auth store and resolves endpoints itself, so there is no Anthropic-compatible URL and no cred row for openrouter",
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
		vision:      visionAlways,
		pairingNote: "the Antigravity CLI drives itself; there is no Anthropic-compatible URL",
	},
}

// visionAlways is the vision column for a provider whose routed models all see
// pixels.
func visionAlways(string) bool { return true }

// visionModels builds a vision column from the exact bare model ids measured to
// see pixels. An id absent from the list is blind as far as the guard is
// concerned — the guard's job is to refuse a verdict nobody measured.
func visionModels(ids ...string) func(string) bool {
	set := make(map[string]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	return func(model string) bool { return set[model] }
}

func findProvider(name string) (provider, bool) {
	for _, p := range providerTable {
		if p.name == name {
			return p, true
		}
	}
	return provider{}, false
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

// modelVision answers "can THIS round see pixels". model may be
// provider-qualified (crush's zai/…, opencode's openrouter/…).
func modelVision(p provider, model string) bool {
	if p.vision == nil {
		return false
	}
	return p.vision(strings.TrimPrefix(model, p.name+"/"))
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

// mappedModelError refuses, at launch, a model id measured to be silently
// answered by a different model. model may be bare (claude-code) or
// provider-qualified (crush's zai/…). OUTSOURCE_ALLOW_MAPPED_MODEL=1
// overrides — that exists for re-measuring the mapping, not for routing.
func mappedModelError(p provider, model string) (string, bool) {
	if model == "" || len(p.silentMappings) == 0 {
		return "", true
	}
	bare := strings.TrimPrefix(model, p.name+"/")
	answered, mapped := p.silentMappings[bare]
	if !mapped || os.Getenv("OUTSOURCE_ALLOW_MAPPED_MODEL") == "1" {
		return "", true
	}
	return fmt.Sprintf("--model %s is silently answered by %s on the %s endpoint (measured: the response model field differs from the request). The round could never run the model you asked for — request %s explicitly, or set OUTSOURCE_ALLOW_MAPPED_MODEL=1 to re-measure the mapping.",
		model, answered, p.name, answered), false
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
		msg += fmt.Sprintf(" (form on the %s harness: %s)", h.name, h.modelFormHint)
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
	// in OutsourceMain BEFORE the --detach re-exec. Past that re-exec there is
	// no caller left to tell: that is how an unqualified --model once came back
	// as "detached (pid=…)" and exit 0 over a round that was already dead
	// (measured 2026-08-26, crush). nil means the harness accepts a bare id.
	modelForm func(model, provider string) (msg string, ok bool)

	// modelFormHint renders that rule for a human, in the message that asks for
	// a --model this launcher has no default for.
	modelFormHint string
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
		run:         (*round).runClaudeCode,
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
		modelFormHint: "provider/id",
	},
	{
		name:      "opencode",
		bin:       "opencode",
		providers: []string{"openrouter"},
		// `--format json` flushes one JSONL event at a time onto --log while the
		// process is still running (measured 2026-08-23), so the log file itself
		// is the live trail.
		progress:      func(o opts) string { return o.log },
		trail:         func(o opts) string { return o.log },
		trailFormat:   tail.FormatOpencodeEvents,
		run:           (*round).runOpencode,
		modelForm:     opencodeModelFormError,
		modelFormHint: "openrouter/<id>",
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
// defaults. It is what `outsource-run --list-wiring` prints, so "what can run
// where" is one command rather than a read of this file.
func wiringMatrix() string {
	return renderWiring(providerTable)
}

// renderWiring is the matrix over a given provider table, so the empty-default
// column can be rendered in a test without emptying a live row.
func renderWiring(providers []provider) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%-12s %-14s %-24s %s\n", "PROVIDER", "HARNESS", "DEFAULT MODEL", "NOTES")
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
				notes = append(notes, "--model form "+h.modelFormHint)
			}
			if p.modelEnv != "" {
				notes = append(notes, "seeds from $"+p.modelEnv)
			}
			fmt.Fprintf(&b, "%-12s %-14s %-24s %s\n", p.name, hname, def, strings.Join(notes, "; "))
		}
	}
	return b.String()
}
