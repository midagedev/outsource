package launch

import (
	"fmt"
	"io"
	"strings"
)

// helpRow is one flag of a launcher's --help. The tables below are the only
// place the help text lives; help_test.go reads the parser's `case "--x"` arms
// and fails when a flag is missing from a table or a table has a stale one
// (measured 2026-10-20: the old run-on usage line named no meanings, and a
// lead could not grep a flag's purpose).
type helpRow struct {
	flag, arg, meaning string
}

// helpGroup is a heading and the rows under it.
type helpGroup struct {
	title string
	rows  []helpRow
}

const helpLabelCap = 30

// renderHelp prints the synopsis, then one aligned line per flag under its
// heading. The column width is taken from the longest "flag ARG" in the table.
func renderHelp(w io.Writer, synopsis string, groups []helpGroup) {
	fmt.Fprintln(w, synopsis)
	width := 0
	label := func(r helpRow) string {
		if r.arg == "" {
			return r.flag
		}
		return r.flag + " " + r.arg
	}
	for _, g := range groups {
		for _, r := range g.rows {
			// A label past helpLabelCap (the harness and provider sets) runs
			// into its meaning instead of pushing every other row right.
			if n := len(label(r)); n > width && n <= helpLabelCap {
				width = n
			}
		}
	}
	for _, g := range groups {
		fmt.Fprintf(w, "\n%s:\n", g.title)
		for _, r := range g.rows {
			fmt.Fprintf(w, "  %-*s   %s\n", width, label(r), r.meaning)
		}
	}
}

// outsourceHelp is built per call so the harness and provider lists stay
// derived: a new wiring arm cannot be routable and undocumented at once.
func outsourceHelp() []helpGroup {
	harnesses := strings.Join(harnessNameList(), "|")
	providers := strings.Join(providerNameList(), "|")
	return []helpGroup{
		{"required", []helpRow{
			{"--cwd", "<dir>", "directory the round works in; must exist"},
			{"--spec", "<file>", "the spec the delegate is given"},
			{"--log", "<file>", "the harness's output log; the .rc sentinel lands beside it"},
		}},
		{"model and backend", []helpRow{
			{"--model", "<M|free>", "model id; free picks one from the live free catalogue"},
			{"--allow-free-training", "", "let --model free pick an id whose data policy trains, retains or is unknown"},
			{"--harness", "<" + harnesses + ">", "CLI that drives the round (default $OUTSOURCE_HARNESS, else the provider's own; see --list-wiring)"},
			{"--provider", "<" + providers + ">", "provider the model is billed on (default $OUTSOURCE_PROVIDER, else zai)"},
			{"--effort", "<low|medium|high|xhigh|max>", "claude-code harness reasoning effort; other harnesses refuse it"},
			{"--config-dir", "<dir>", "directory holding the harness's own config and session data"},
		}},
		{"round control", []helpRow{
			{"--session", "<id>", "resume this harness session instead of starting a new one"},
			{"--label", "<L>", "purpose label shown in the run registry"},
			{"--done-marker", "<M>", "string the spec ends with; a clean exit without it in the report is not a pass"},
			{"--require-quota", "<N>", "refuse to launch (exit 66) when the plan window has less than N percent left"},
			{"--max-seconds", "<N>", "hard-kill the round after N seconds (exit 124)"},
			{"--resume-on-reset", "", "after a plan-limit (429) cut, wait for the reset and resume the session, at most twice"},
			{"--allow-agent", "", "crush harness: keep the agent/task tools, which bypass the git guard"},
			{"--no-vision-check", "", "do not refuse a spec that names an image for a model that cannot see (exit 65)"},
			{"--allow-no-tools", "", "claude-code harness: accept a zero-tool-call round (answer-only) instead of exit 73"},
		}},
		{"launch mode", []helpRow{
			{"--detach", "", "run in the background and return at once (recommended)"},
			{"--foreground", "", "run in this process until the round ends"},
			{"--list-wiring", "", "print which harness and provider combinations can run, then exit"},
			{"-h, --help", "", "print this help"},
		}},
	}
}

func printOutsourceHelp(w io.Writer) {
	renderHelp(w, "usage: outsource-run --cwd <dir> --spec <file> --log <file> [flags]", outsourceHelp())
}

var grokHelp = []helpGroup{
	{"required", []helpRow{
		{"--cwd", "<dir>", "directory the round works in; must exist"},
		{"--spec", "<file>", "the spec the delegate is given"},
		{"--log", "<file.ndjson>", "the round's NDJSON log; the .rc sentinel lands beside it"},
	}},
	{"model and tools", []helpRow{
		{"--model", "<M>", "grok model id (default grok-4.7)"},
		{"--reasoning-effort", "<E>", "grok reasoning effort (default xhigh)"},
		{"--max-turns", "<N>", "turn ceiling for the round (default 1200)"},
		{"--git-profile", "<strict|readonly-plus|trusted>", "git/gh deny set for the round (default strict)"},
		{"--research", "", "no write tools; the final message is the only deliverable"},
		{"--", "<grok flags...>", "everything after it is passed to grok unchanged"},
	}},
	{"round control", []helpRow{
		{"--label", "<L>", "purpose label shown in the run registry"},
		{"--done-marker", "<M>", "string the spec ends with; a clean exit without it in the report is exit 72"},
		{"--resume", "<SID>", "resume a stopped grok session"},
	}},
	{"launch mode", []helpRow{
		{"--detach", "", "run in the background and return at once (recommended)"},
		{"--foreground", "", "run in this process until the round ends"},
		{"-h, --help", "", "print this help"},
	}},
}

func printGrokHelp(w io.Writer) {
	renderHelp(w, "usage: grok-run --cwd <dir> --spec <file> --log <file.ndjson> [flags]", grokHelp)
}
