// Package audit turns what a delegated round actually DID into a reviewable
// event list.
//
// Why it exists: a round reports what it did and the lead reviews a git diff,
// and neither shows what actually ran — which commands, which files written by
// which tool, what was denied and by whom, what messages reached the round
// mid-flight, which model answered each request. Every claude-code round
// already leaves a full session transcript and every agy round a stream-json
// log, so the evidence exists; it is just not shaped for review. This tool
// reshapes either source into one uniform event list (the event taxonomy
// borrows Apache Maka's RuntimeEvent kinds: text, function_call,
// function_response, permissionDecision, tokenUsage) and adds the one piece
// Maka's log lacks — tamper evidence. The launcher seals the trail's sha256
// into the <log>.rc sentinel when the round ends (internal/launch), and audit
// re-verifies it here.
//
// It is read-only by construction: it writes no registry row, no trail, no
// sentinel, and the only git it will run is diff, status, ls-files and
// rev-parse.
package audit

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/midagedev/outsource/internal/runs"
)

const usage = `Show what a delegated round actually did, read from its trail.

  audit [<selector>] [--json] [--events] [--base <rev>] [--log <path>] [--label <name>]

<selector> is exactly tail's: a run id, a --log path or a --label; omitted, it
means "the one round that is running". --label spells the label selector out
for the caller who wants the flag form; it resolves through the same path.
claude-code rounds are audited from the session transcript plus every .jsonl
under its subagents/ directory; agy rounds from the stream-json log. --log
also works after the registry record has been pruned: the <log>.rc sentinel
then supplies the harness, trail and session.

      --json       one JSON event object per line, in transcript order
      --events     one readable line per event, cut to $COLUMNS (default 160)
      --base REV   the tree cross-check diffs the work tree against REV
                   instead of taking git status

The default output is a review summary: models (with drift), tools, every shell
command with observation flags, files written by file tools, a cross-check of
those writes against the tree, inbound messages, subagents, and the trail seal.

Exit codes: 0 printed (seal ok, extended or absent) · 3 seal mismatch · 64 usage, or a
selector that names more than one round · 65 no such run, or no trail yet ·
69 a harness this tool cannot read yet`

// Exit codes. 3 is this tool's own: a tampered trail is not a usage error and
// not a missing run, and a watcher must be able to tell "reviewed fine" from
// "the evidence changed after the round ended" by the code alone.
const (
	ExitSealMismatch = 3
	ExitUsage        = 64
	ExitNoTrail      = 65
	ExitUnsupported  = 69
)

// Main is the tool entry point, registered in cmd/outsource.
func Main(args []string, stdout, stderr io.Writer) int {
	o, rc, done := parseArgs(args, stdout, stderr)
	if done {
		return rc
	}

	rec, rc := resolveRecord(o, stderr)
	if rc != 0 {
		return rc
	}

	// Which file holds the evidence, per harness. claude-code's live record is
	// the session transcript (the --log file stays empty until the harness
	// exits); agy's stream-json log is both.
	var trail string
	switch rec.Harness {
	case "claude-code":
		trail = rec.Trail
	case "agy":
		trail = rec.Log
	default:
		fmt.Fprintf(stderr, "audit: harness %s is not supported yet (claude-code, agy)\n", rec.Harness)
		return ExitUnsupported
	}
	if trail == "" {
		fmt.Fprintf(stderr, "audit: run %s (%s) has not revealed a trail yet — it is recorded on the round's first turn\n", rec.ID, rec.Label)
		return ExitNoTrail
	}

	t, err := ParseTrail(trail, rec.Harness)
	if err != nil {
		fmt.Fprintf(stderr, "audit: cannot read the trail %s: %v\n", trail, err)
		return ExitNoTrail
	}

	seal, sentinel := ReadSeal(rec.Log + ".rc")
	t.Sentinel = sentinel
	t.Finalize(seal)

	switch {
	case o.json:
		printJSON(t, stdout)
	case o.events:
		printEvents(t, stdout, o.width())
	default:
		printSummary(t, rec, seal, o.base, stdout)
	}
	if seal.Verdict == "mismatch" {
		return ExitSealMismatch
	}
	return 0
}

// parseArgs mirrors tail's flag grammar for the shared selector and adds this
// tool's own flags. A selector is positional and singular, exactly as in tail.
type cliOpts struct {
	sel    string
	json   bool
	events bool
	base   string
	log    string
}

func (o *cliOpts) width() int {
	if v := os.Getenv("COLUMNS"); v != "" {
		n := 0
		for _, c := range v {
			if c < '0' || c > '9' {
				return 160
			}
			n = n*10 + int(c-'0')
		}
		if n > 0 {
			return n
		}
	}
	return 160
}

func parseArgs(args []string, stdout, stderr io.Writer) (cliOpts, int, bool) {
	var o cliOpts
	need := func(i int) (string, bool) {
		if i+1 >= len(args) {
			fmt.Fprintf(stderr, "audit: %s needs a value\n", args[i])
			return "", false
		}
		return args[i+1], true
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "-h", "--help":
			fmt.Fprint(stdout, usage)
			return o, 0, true // help exits 0 before any resolution
		case "--json":
			o.json = true
		case "--events":
			o.events = true
		case "--base", "--log", "--label":
			// --label spells the positional selector out; it feeds the same
			// sel, through the same runs.Resolve, never a second resolver.
			v, ok := need(i)
			if !ok {
				return o, ExitUsage, true
			}
			switch a {
			case "--base":
				o.base = v
			case "--log":
				o.log = v
			default:
				if o.sel != "" {
					fmt.Fprintf(stderr, "audit: one selector at a time, got %q and %q\n", o.sel, v)
					return o, ExitUsage, true
				}
				o.sel = v
			}
			i++
		default:
			if strings.HasPrefix(a, "-") {
				fmt.Fprintf(stderr, "audit: unknown flag: %s\n", a)
				return o, ExitUsage, true
			}
			if o.sel != "" {
				fmt.Fprintf(stderr, "audit: one selector at a time, got %q and %q\n", o.sel, a)
				return o, ExitUsage, true
			}
			o.sel = a
		}
	}
	if o.json && o.events {
		fmt.Fprintln(stderr, "audit: --json and --events are mutually exclusive")
		return o, ExitUsage, true
	}
	return o, 0, false
}

// resolveRecord finds the round to audit. The selector path is exactly tail's
// (runs.Resolve, including the ambiguity refusal), so the two tools can never
// disagree about which round a selector names. --log is the one addition: a
// record is kept for a day and then pruned, while the log and its sentinel
// stay, so a post-mortem a week later still has an addressable artifact.
func resolveRecord(o cliOpts, stderr io.Writer) (*runs.Record, int) {
	if o.log != "" {
		if rec := runs.FindByLog(o.log); rec != nil {
			return rec, 0
		}
		rec, err := recordFromSentinel(o.log)
		if err != nil {
			fmt.Fprintf(stderr, "audit: %v\n", err)
			return nil, ExitNoTrail
		}
		return rec, 0
	}
	rec, err := runs.Resolve(o.sel)
	if err != nil {
		if amb, ok := err.(*runs.Ambiguous); ok {
			fmt.Fprintf(stderr, "audit: %v — name one of these instead:\n", err)
			for _, c := range amb.Candidates {
				fmt.Fprintf(stderr, "  %s  %-16s %s·%s  %s\n", c.ID, c.Label, c.Provider, c.Harness, c.State())
			}
			return nil, ExitUsage
		}
		fmt.Fprintf(stderr, "audit: %v\n", err)
		return nil, ExitNoTrail
	}
	return rec, 0
}

// recordFromSentinel rebuilds just enough of a record to audit a pruned round:
// harness, trail and session (exactly what the task named), plus the model and
// rc the sentinel recorded. Cwd is gone with the record, so the tree
// cross-check is skipped for these — honestly, not by crashing.
func recordFromSentinel(log string) (*runs.Record, error) {
	m, err := readSentinel(log + ".rc")
	if err != nil {
		return nil, fmt.Errorf("no run matches log %s and no sentinel at %s.rc — the round is neither in the registry nor on disk", log, log)
	}
	base := filepath.Base(log)
	if ext := filepath.Ext(base); ext != "" {
		base = strings.TrimSuffix(base, ext)
	}
	rec := &runs.Record{
		ID:          base,
		Label:       base,
		Harness:     m["harness"],
		Log:         log,
		Trail:       m["trail"],
		Session:     m["session"],
		Model:       m["model_requested"],
		ModelActual: m["model_actual"],
		RC:          m["rc"],
		FinishedAt:  m["finished"],
	}
	return rec, nil
}

// clock renders an event timestamp as local wall time, matching tail's reader
// convention so a lead correlating the two views reads the same clock.
func clock(ts string) string {
	if ts == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return ""
	}
	return t.Local().Format("15:04:05")
}

func clockDate(ts string) string {
	if ts == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return ""
	}
	return t.Local().Format("2006-01-02 15:04:05")
}
