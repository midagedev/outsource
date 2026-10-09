package catalog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// OpenCode Zen has no public listing API that opencode does not own, so the
// catalogue is what opencode itself accepts: `opencode models opencode
// --verbose --pure`. --pure because a user's environment may point
// OPENCODE_CONFIG_DIR at plugin hooks; stdout only, because opencode prints a
// FORCE_COLOR/NO_COLOR warning on stderr on some machines.
//
// Measured 2026-10-09 (opencode 1.18.21, no Zen login): the listing held 11
// ids, all free. Its cost figures are models.dev's, in USD per million tokens
// — the same file lists anthropic's claude-haiku-4-5 at input 1 / output 5,
// which is that model's $1/M and $5/M. No Zen id was priced that day, so the
// unit is read from that sibling, not from a Zen id.
const zenTimeout = 60 * time.Second

// zenArgs is the opencode command line after the binary name. --refresh
// always: opencode runs only when our own cache missed or went stale (or the
// caller asked), and without it opencode answers from its own models.dev
// cache or, with none, a snapshot built into the binary — measured
// 2026-10-09, 7 ids with three deprecated and step-5-preview-free missing,
// against 11 refreshed. So the cost is at most one models.dev fetch per
// FreshFor, and it buys a list an automatic pick can trust.
func zenArgs() []string {
	return []string{"models", ZenQualifier, "--verbose", "--pure", "--refresh"}
}

func loadZen(ctx context.Context, opts Options) ([]Entry, Source) {
	run := opts.Opencode
	if run == nil {
		run = runOpencode
	}
	args := zenArgs()
	return loadCached(Zen, "opencode "+strings.Join(args, " "), opts.Refresh,
		func() ([]byte, error) {
			cctx, cancel := context.WithTimeout(ctx, zenTimeout)
			defer cancel()
			return run(cctx, args...)
		},
		parseZen)
}

// errOpencodeMissing is the zen catalogue's answer on a machine without
// opencode: unavailable, said plainly, and nothing else fails with it.
var errOpencodeMissing = errors.New("opencode is not on PATH — the zen catalogue is what `opencode models opencode` lists")

// runOpencode runs the opencode CLI from PATH and returns its stdout. stderr
// is read only for the error message.
func runOpencode(ctx context.Context, args ...string) ([]byte, error) {
	bin, err := exec.LookPath("opencode")
	if err != nil {
		return nil, errOpencodeMissing
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if i := strings.LastIndexByte(msg, '\n'); i >= 0 {
			msg = msg[i+1:]
		}
		if msg != "" {
			return nil, fmt.Errorf("opencode %s: %w: %s", strings.Join(args, " "), err, msg)
		}
		return nil, fmt.Errorf("opencode %s: %w", strings.Join(args, " "), err)
	}
	return out, nil
}

// zenModel is the part of one `opencode models --verbose` block this package
// reads. Pointers mark costs whose absence must stay distinguishable from 0.
type zenModel struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
	Cost   *struct {
		Input  *float64 `json:"input"`
		Output *float64 `json:"output"`
	} `json:"cost"`
	Limit struct {
		Context int `json:"context"`
		Output  int `json:"output"`
	} `json:"limit"`
	Capabilities struct {
		Toolcall bool            `json:"toolcall"`
		Input    map[string]bool `json:"input"`
	} `json:"capabilities"`
}

// parseZen reads the block format: a line `opencode/<id>` at column 0, then
// a pretty-printed JSON object over as many lines as it takes, repeated, the
// last block possibly without a trailing newline. Lines before the first
// header belong to no block and are skipped. A block whose JSON does not
// decode, or whose id disagrees with its header, fails the whole parse: a
// format change should be loud, and the cache keeps the last good listing.
func parseZen(body []byte) ([]Entry, error) {
	prefix := ZenQualifier + "/"
	type block struct {
		header string
		lines  []string
	}
	var blocks []block
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimRight(line, "\r")
		// A header is the bare qualified id; pretty-printed JSON lines are
		// indented or are braces, so they never start with the prefix.
		if strings.HasPrefix(line, prefix) && !strings.ContainsAny(line, " \t{}\"") {
			blocks = append(blocks, block{header: strings.TrimPrefix(line, prefix)})
			continue
		}
		if len(blocks) > 0 {
			blocks[len(blocks)-1].lines = append(blocks[len(blocks)-1].lines, line)
		}
	}
	if len(blocks) == 0 {
		return nil, fmt.Errorf("zen model listing: no %s<id> blocks in opencode's output", prefix)
	}
	out := make([]Entry, 0, len(blocks))
	for _, b := range blocks {
		var m zenModel
		if err := json.Unmarshal([]byte(strings.Join(b.lines, "\n")), &m); err != nil {
			return nil, fmt.Errorf("zen model listing: block %s%s: %w", prefix, b.header, err)
		}
		if m.ID != b.header {
			return nil, fmt.Errorf("zen model listing: block %s%s carries id %q", prefix, b.header, m.ID)
		}
		e := Entry{
			Provider:  Zen,
			ID:        m.ID,
			Name:      m.Name,
			Context:   m.Limit.Context,
			MaxOutput: m.Limit.Output,
			Tools:     m.Capabilities.Toolcall,
			Status:    m.Status,
		}
		if m.Cost != nil && m.Cost.Input != nil && m.Cost.Output != nil &&
			*m.Cost.Input >= 0 && *m.Cost.Output >= 0 {
			e.PriceKnown = true
			e.PriceIn, e.PriceOut = *m.Cost.Input, *m.Cost.Output
			e.Free = e.PriceIn == 0 && e.PriceOut == 0
		}
		var in []string
		for k, v := range m.Capabilities.Input {
			if v {
				in = append(in, k)
			}
		}
		e.Inputs = normInputs(in)
		out = append(out, e)
	}
	return out, nil
}

// ---- data policy -----------------------------------------------------------

// zenPolicySource dates the table below. Zen publishes no policy API; its
// docs page states terms per id, and the quotes are the evidence. A future
// update edits the table and this date together.
const zenPolicySource = "zen docs 2026-10-09"

// zenTerms is https://opencode.ai/docs/zen/ as fetched on 2026-10-09: each
// row's quote is the docs' own sentence, and its ids are the ones the docs
// attach it to.
var zenTerms = []struct {
	policy string
	quote  string
	ids    []string
}{
	{
		policy: PolicyNoTrainNoRetain,
		quote:  "Its provider follows a zero-retention policy and does not use your data for model training.",
		ids:    []string{"step-5-preview-free", "space-bunny-free", "longcat-2.5-preview-free"},
	},
	{
		policy: PolicyRetains,
		quote:  "Jev APIs: Prompts and other inputs are not used for training. Data is retained in accordance with TypeSafe AI's Privacy Policy.",
		ids:    []string{"jev-1.13-free"},
	},
	{
		policy: PolicyTrains,
		quote:  "During its free period, collected data may be used to improve the model.",
		ids: []string{"big-pickle", "exo-free", "mimo-v2.6-flash-free", "mimo-v2.5-free",
			"ling-3.1-flash-free", "ling-3.0-flash-fin-free"},
	},
	{
		policy: PolicyTrains,
		quote:  "Trial use only — do not submit personal or confidential data. Your use is logged … to improve NVIDIA products",
		ids:    []string{"nemotron-3-ultra-free", "nemotron-3.5-lightning-free"},
	},
	{
		policy: PolicyTrains,
		quote:  "permission to use your prompts and completions to train future Meta models",
		ids:    []string{"muse-spark-1.3-contributor-free"},
	},
}

// zenPolicy is the table lookup. An id the docs did not name on that date is
// unknown, whatever its neighbours say.
func zenPolicy(id string) (verdict, source string) {
	for _, t := range zenTerms {
		if contains(t.ids, id) {
			return t.policy, zenPolicySource
		}
	}
	return PolicyUnknown, zenPolicySource + ": id not listed"
}
