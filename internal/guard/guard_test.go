package guard

import (
	"bufio"
	"os"
	"strings"
	"testing"
)

// TestVerdictsMatchFrozenBoundary is the guard's real test. The 54 hand-written
// cases in tests/git-guard.test.sh assert the intent; this asserts the
// *boundary* — every verdict the shell implementation gave over a 670-command
// covering corpus, frozen at the commit that replaced it.
//
// It exists because a security guard whose only specification is its own source
// can drift in either direction with nothing to notice: a hole lets a delegate
// rewrite history, and a false refusal makes delegates work blind and teaches
// the next spec to drop the check that would have caught a wrong worktree. Both
// have happened here.
func TestVerdictsMatchFrozenBoundary(t *testing.T) {
	f, err := os.Open("testdata/verdicts.tsv")
	if err != nil {
		t.Fatalf("the frozen boundary is missing: %v", err)
	}
	defer f.Close()

	unescape := strings.NewReplacer(`\t`, "\t", `\n`, "\n", `\\`, `\`)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var checked, holes, falseRefusals int
	for sc.Scan() {
		line := sc.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		want, cmd, ok := strings.Cut(line, "\t")
		if !ok {
			t.Fatalf("malformed row: %q", line)
		}
		cmd = unescape.Replace(cmd)
		got, _ := Verdict(cmd)
		wantBlocked := want == "block"
		if got == wantBlocked {
			checked++
			continue
		}
		// Name the direction, because the two failures are not equally bad and a
		// reviewer needs to know which one they are looking at.
		if wantBlocked && !got {
			holes++
			t.Errorf("HOLE: %q was blocked and now is not", cmd)
		} else {
			falseRefusals++
			t.Errorf("FALSE REFUSAL: %q was allowed and now is blocked", cmd)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if checked < 600 {
		t.Fatalf("only %d verdicts checked; the corpus should be ~670 — a truncated golden file is not a pass", checked)
	}
	if holes > 0 || falseRefusals > 0 {
		t.Logf("%d holes, %d false refusals over %d verdicts", holes, falseRefusals, checked)
	}
}

func TestCommandExtractionConventions(t *testing.T) {
	env := func(v string) func(string) string {
		return func(k string) string {
			if k == "CRUSH_TOOL_INPUT_COMMAND" {
				return v
			}
			return ""
		}
	}
	none := func(string) string { return "" }

	// The environment wins outright, so a harness that sets it never pays for a
	// JSON parse.
	if got := commandFrom(env("git log"), strings.NewReader(`{"tool_input":{"command":"git commit"}}`), false); got != "git log" {
		t.Errorf("env must win, got %q", got)
	}
	// A terminal stdin is never read: an interactive invocation must not hang
	// waiting for a payload nobody is going to type.
	if got := commandFrom(none, strings.NewReader(`{"tool_input":{"command":"git commit"}}`), true); got != "" {
		t.Errorf("tty stdin must not be read, got %q", got)
	}
	// Every malformed shape yields no command, and therefore no block. A guard
	// that refused whatever it could not parse would break every tool call in a
	// round the moment a harness changed its hook shape.
	for _, payload := range []string{
		``, `not json`, `{}`, `null`, `[1,2,3]`, `{"other":1}`,
		`{"tool_input":null}`, `{"tool_input":[]}`, `{"tool_input":{}}`,
		`{"tool_input":{"command":null}}`, `{"tool_input":{"command":""}}`,
	} {
		if got := commandFrom(none, strings.NewReader(payload), false); got != "" {
			t.Errorf("payload %q yielded %q, want empty", payload, got)
		}
	}
	if got := commandFrom(none, strings.NewReader(`{"tool_input":{"command":"git push"}}`), false); got != "git push" {
		t.Errorf("well-formed payload gave %q", got)
	}
}

func TestReadOnlyGitStaysOpenAndPairedMutationsDoNot(t *testing.T) {
	// The two halves of the erase-then-deny design, stated as intent rather than
	// as corpus rows, so the reason survives even if the golden file is ever
	// regenerated.
	for _, allowed := range []string{
		"git log --oneline", "git show HEAD", "git diff", "git status",
		"git worktree list", "git -C /tmp worktree list",
		"git branch -a", "git branch --show-current", "git -C /tmp branch --show-current",
		"git -C /tmp worktree list; git -C /tmp branch --show-current",
		"git remote -v", "git config --get user.name",
		"gh pr list", "gh pr view 1", "gh api /repos/x/y",
	} {
		if blocked, _ := Verdict(allowed); blocked {
			t.Errorf("must stay allowed: %q", allowed)
		}
	}
	for _, blocked := range []string{
		"git commit -am x", "git -C /tmp commit", "env FOO=1 git push",
		"sudo git reset --hard", "git worktree list && git commit -am x",
		"git worktree add ../wt", "gh pr create", "gh api -X POST /x",
	} {
		if got, _ := Verdict(blocked); !got {
			t.Errorf("must stay blocked: %q", blocked)
		}
	}
}
