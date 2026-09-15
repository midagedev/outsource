package launch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The generated settings carry two hooks, and each is load-bearing: PreToolUse
// is the git guard, SessionStart is how the round tells the registry where its
// live transcript is.
//
// FAIL-first (this file did not exist before 2026-09-15): the settings held only
// the guard, so nothing on disk ever named the round's own transcript. A session
// asked to follow a 24-minute round found run.log empty (--output-format json
// emits one object at exit), `runs` showing only "running 21m", and the
// projects/<cwd-slug>/ directory holding several .jsonl files with no way to
// tell which was this round's — ten tool calls to guess the newest, and that
// guess is simply wrong once two rounds share a cwd.
func TestHookSettingsCarryTheGuardAndTheTrailRecorder(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if err := writeHookSettings(path, "1789448829-4242", "/state/outsource/runs"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Hooks map[string][]struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Type    string `json:"type"`
				Command string `json:"command"`
				Timeout int    `json:"timeout"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("generated settings.json is not JSON: %v\n%s", err, b)
	}

	pre := doc.Hooks["PreToolUse"]
	if len(pre) != 1 || pre[0].Matcher != "Bash" || len(pre[0].Hooks) != 1 ||
		!strings.HasSuffix(pre[0].Hooks[0].Command, " guard") {
		t.Fatalf("the git guard must still be the Bash PreToolUse hook:\n%s", b)
	}

	start := doc.Hooks["SessionStart"]
	if len(start) != 1 || len(start[0].Hooks) != 1 {
		t.Fatalf("no SessionStart hook — the round would never reveal its transcript:\n%s", b)
	}
	h := start[0].Hooks[0]
	for _, want := range []string{
		"tail --record-from-hook",
		"'1789448829-4242'",
		"--runs-dir '/state/outsource/runs'",
	} {
		if !strings.Contains(h.Command, want) {
			t.Fatalf("SessionStart command is missing %q: %s", want, h.Command)
		}
	}
	// The recorder runs before the round's first turn, so its ceiling is short.
	if h.Timeout <= 0 || h.Timeout > 10 {
		t.Fatalf("SessionStart timeout %d is not a short ceiling in front of turn one", h.Timeout)
	}
}

// The registry directory is passed EXPLICITLY because the hook runs inside the
// harness, which makes no promise to pass OUTSOURCE_RUNS_DIR through. A recorder
// writing into the default directory while the launcher used another one would
// record nothing, silently — and the shell suites are exactly where that would
// hide, since they all set OUTSOURCE_RUNS_DIR.
func TestTrailRecorderIsGivenTheRegistryDirectoryAndQuotesIt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	odd := "/tmp/it's a dir/runs"
	if err := writeHookSettings(path, "1-2", odd); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	// The harness runs a hook command through a shell, so a path with a space or
	// a quote has to survive it.
	if !strings.Contains(string(b), `--runs-dir '/tmp/it'\"'\"'s a dir/runs'`) {
		t.Fatalf("the registry directory is not shell-quoted:\n%s", b)
	}
}

// No run id means the registry refused the round (registerRun returns an empty
// id on failure). Recording a trail against nothing is not a thing to try.
func TestNoRunIDMeansNoRecorderHook(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if err := writeHookSettings(path, "", "/state/runs"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if strings.Contains(string(b), "SessionStart") {
		t.Fatalf("an unregistered round must not get a recorder hook:\n%s", b)
	}
	if !strings.Contains(string(b), "PreToolUse") {
		t.Fatalf("the guard is unconditional:\n%s", b)
	}
}

func TestShellQuote(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"/plain/path", `'/plain/path'`},
		{"/with space/x", `'/with space/x'`},
		{"it's", `'it'"'"'s'`},
	} {
		if got := shellQuote(c.in); got != c.want {
			t.Fatalf("shellQuote(%q) = %s, want %s", c.in, got, c.want)
		}
	}
}
