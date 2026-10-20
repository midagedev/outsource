package launch

import (
	"bytes"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

var quotedFlag = regexp.MustCompile(`"(-[^"]*)"`)

// parserFlags returns the flags of the first `switch args[i]` in src: every
// quoted string on a `case` line, up to that switch's `default:`.
func parserFlags(t *testing.T, file string) map[string]bool {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	start := strings.Index(src, "switch args[i] {")
	if start < 0 {
		t.Fatalf("%s: no `switch args[i]` parser", file)
	}
	end := strings.Index(src[start:], "\n\t\tdefault:")
	if end < 0 {
		t.Fatalf("%s: parser has no default arm", file)
	}
	got := map[string]bool{}
	for _, line := range strings.Split(src[start:start+end], "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "case ") {
			continue
		}
		for _, m := range quotedFlag.FindAllStringSubmatch(line, -1) {
			got[m[1]] = true
		}
	}
	if len(got) < 5 {
		t.Fatalf("%s: extracted only %d flags; the extraction is broken", file, len(got))
	}
	return got
}

func tableFlags(groups []helpGroup) (map[string]int, []string) {
	got := map[string]int{}
	for _, g := range groups {
		for _, r := range g.rows {
			for _, f := range strings.Split(r.flag, ", ") {
				got[f]++
			}
		}
	}
	var dup []string
	for f, n := range got {
		if n > 1 {
			dup = append(dup, f)
		}
	}
	return got, dup
}

func checkDrift(t *testing.T, file string, groups []helpGroup) {
	t.Helper()
	parser := parserFlags(t, file)
	table, dup := tableFlags(groups)
	var problems []string
	for f := range parser {
		if table[f] == 0 {
			problems = append(problems, "parser flag missing from help table: "+f)
		}
	}
	for f := range table {
		if !parser[f] {
			problems = append(problems, "help table flag the parser lacks: "+f)
		}
	}
	for _, f := range dup {
		problems = append(problems, "flag listed more than once: "+f)
	}
	sort.Strings(problems)
	for _, p := range problems {
		t.Error(p)
	}
}

// FAIL-first: deleting any row from outsourceHelp or grokHelp makes the
// matching test name the flag ("parser flag missing from help table: --x").
func TestOutsourceHelpCoversParser(t *testing.T) {
	checkDrift(t, "outsource.go", outsourceHelp())
}

func TestGrokHelpCoversParser(t *testing.T) {
	checkDrift(t, "grok.go", grokHelp)
}

func TestHelpRendersOneLinePerFlag(t *testing.T) {
	var out bytes.Buffer
	if rc := OutsourceMain([]string{"--help"}, &out, &bytes.Buffer{}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	lines := strings.Split(out.String(), "\n")
	if lines[0] != "usage: outsource-run --cwd <dir> --spec <file> --log <file> [flags]" {
		t.Errorf("synopsis: %q", lines[0])
	}
	for _, f := range []string{"--list-wiring", "--resume-on-reset", "--effort"} {
		n := 0
		for _, l := range lines[1:] {
			if strings.HasPrefix(strings.TrimSpace(l), f+" ") || strings.Contains(l, " "+f+" ") && strings.HasPrefix(l, "  ") {
				n++
			}
		}
		if n != 1 {
			t.Errorf("%s appears on %d help lines, want 1", f, n)
		}
	}
	var g bytes.Buffer
	if rc := GrokMain([]string{"-h"}, &g, &bytes.Buffer{}); rc != 0 || !strings.HasPrefix(g.String(), "usage: grok-run ") {
		t.Errorf("grok help rc=%d out=%q", rc, g.String())
	}
}
