package tail

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/midagedev/outsource/internal/runs"
)

// The measured SessionStart payload, verbatim from the probe that established
// this whole mechanism: CLI 2.1.272, `claude -p`, an isolated
// CLAUDE_CONFIG_DIR, 2026-09-15. Keeping the real shape as the fixture is the
// point — a hand-written one would not have told us the event fires headless.
const sessionStartPayload = `{
  "session_id": "c78a2726-d0ad-4194-bdc7-24bade1b05e6",
  "transcript_path": "/tmp/cfg/claude/projects/-Users-hckim-repo-toktape/c78a2726-d0ad-4194-bdc7-24bade1b05e6.jsonl",
  "cwd": "/Users/hckim/repo/toktape",
  "hook_event_name": "SessionStart",
  "source": "startup"
}`

// startRun registers a round the way the launcher does, and returns its id.
func startRun(t *testing.T, dir string, extra ...string) string {
	t.Helper()
	t.Setenv("OUTSOURCE_RUNS_DIR", dir)
	var out bytes.Buffer
	// The test process's own pid, so the record reads as `running`: Alive() is a
	// signal-0 probe, and pid 1 answers EPERM rather than nil for a non-root
	// caller, which would make every fixture an orphan.
	args := append([]string{"start", "--pid", strconv.Itoa(os.Getpid()), "--label", "probe",
		"--provider", "zai", "--harness", "claude-code", "--log", "/tmp/x.log"}, extra...)
	if rc := runs.Main(args, &out, os.Stderr); rc != 0 {
		t.Fatalf("runs start rc=%d", rc)
	}
	return strings.TrimSpace(out.String())
}

// The reveal, end to end: the hook's payload on stdin becomes a trail on the
// record. FAIL-first is the absence of the whole path — before this change the
// registry had no trail field and nothing wrote one, so a running round's
// transcript could only be guessed at from the projects/ directory.
func TestRecordFromHookPutsTheTranscriptOnTheRecord(t *testing.T) {
	dir := t.TempDir()
	id := startRun(t, dir)

	var stderr bytes.Buffer
	rc := Main([]string{"--record-from-hook", id, "--runs-dir", dir},
		strings.NewReader(sessionStartPayload), &bytes.Buffer{}, &stderr)
	if rc != 0 {
		t.Fatalf("recorder rc=%d, want 0; stderr=%s", rc, stderr.String())
	}
	got := runs.FindByID(id)
	if got == nil {
		t.Fatal("the record went missing")
	}
	want := "/tmp/cfg/claude/projects/-Users-hckim-repo-toktape/c78a2726-d0ad-4194-bdc7-24bade1b05e6.jsonl"
	if got.Trail != want {
		t.Fatalf("trail = %q, want %q", got.Trail, want)
	}
}

// The recorder sits in front of the round's first turn and its stdout is
// injected into that turn. It must therefore print nothing and exit 0 whatever
// it is handed — a broken reveal must not be able to fail, or pollute, the
// round it was only describing.
func TestRecorderIsSilentAndSucceedsOnEveryBadInput(t *testing.T) {
	dir := t.TempDir()
	id := startRun(t, dir)
	cases := []struct {
		name  string
		args  []string
		stdin string
	}{
		{"good", []string{"--record-from-hook", id, "--runs-dir", dir}, sessionStartPayload},
		{"garbage stdin", []string{"--record-from-hook", id, "--runs-dir", dir}, "not json"},
		{"no transcript_path", []string{"--record-from-hook", id, "--runs-dir", dir}, `{"session_id":"x"}`},
		{"unknown run id", []string{"--record-from-hook", "1-999999", "--runs-dir", dir}, sessionStartPayload},
		{"no run id", []string{"--record-from-hook", "--runs-dir", dir}, sessionStartPayload},
		{"empty stdin", []string{"--record-from-hook", id, "--runs-dir", dir}, ""},
	}
	for _, c := range cases {
		var stdout, stderr bytes.Buffer
		if rc := Main(c.args, strings.NewReader(c.stdin), &stdout, &stderr); rc != 0 {
			t.Fatalf("%s: rc=%d, want 0", c.name, rc)
		}
		if stdout.Len() != 0 {
			t.Fatalf("%s: wrote %q to stdout — that lands in the model's first turn", c.name, stdout.String())
		}
		if stderr.Len() != 0 {
			t.Fatalf("%s: wrote %q to stderr without OUTSOURCE_TAIL_DEBUG", c.name, stderr.String())
		}
	}
}

// An explicit --runs-dir is what makes the reveal work at all: the hook runs
// inside the harness, which makes no promise to pass the launcher's
// environment through. FAIL-first for this one is trivial to see — drop the
// flag from the generated hook command and the recorder writes into the
// default directory, where the launcher's record is not.
func TestRecorderWritesIntoTheRunsDirItWasGiven(t *testing.T) {
	dir := t.TempDir()
	id := startRun(t, dir)
	// Point the environment somewhere else entirely; the flag must win.
	t.Setenv("OUTSOURCE_RUNS_DIR", t.TempDir())
	if rc := Main([]string{"--record-from-hook", id, "--runs-dir", dir},
		strings.NewReader(sessionStartPayload), &bytes.Buffer{}, &bytes.Buffer{}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	b, err := os.ReadFile(filepath.Join(dir, id+".run"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "trail=/tmp/cfg/claude/projects/") {
		t.Fatalf("record in the given dir carries no trail:\n%s", b)
	}
}

// A claude-code transcript, in the shape analyzeRun already reads: one JSON
// object per line, message.content a part array.
const transcriptFixture = `{"type":"user","message":{"role":"user","content":"the spec text"}}
{"type":"assistant","timestamp":"2026-09-15T04:57:02.000Z","message":{"role":"assistant","model":"glm-5.3","content":[{"type":"thinking","thinking":"planning"},{"type":"text","text":"Reading the launcher first."}]}}
{"type":"assistant","timestamp":"2026-09-15T04:57:05.000Z","message":{"role":"assistant","model":"glm-5.3","content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"go test ./internal/launch/","description":"run the unit gates"}}]}}
{"type":"user","timestamp":"2026-09-15T04:57:31.000Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","is_error":true,"content":"FAIL github.com/midagedev/outsource/internal/launch"}]}}
{"type":"summary","summary":"not a turn"}
`

func TestRenderClaudeTranscript(t *testing.T) {
	r := &renderer{format: FormatClaudeTranscript, width: 0, tools: map[string]string{}}
	got := r.render(splitLines(transcriptFixture))
	want := []string{
		"13:57:02 💬 Reading the launcher first.",
		"13:57:05 🔧 Bash go test ./internal/launch/",
		"13:57:31 ✗ Bash: FAIL github.com/midagedev/outsource/internal/launch",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("rendered:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	// --all adds the thinking block and the non-error results; without it the
	// view is what the round SAID and DID, which is the question being asked.
	all := &renderer{format: FormatClaudeTranscript, width: 0, all: true, tools: map[string]string{}}
	if out := strings.Join(all.render(splitLines(transcriptFixture)), "\n"); !strings.Contains(out, "🤔 planning") {
		t.Fatalf("--all must show thinking:\n%s", out)
	}
}

// A tool_result carrying a large file read is routinely over a megabyte, and
// that is exactly the line that aborts a default bufio.Scanner — which would
// silently end the render at the most interesting moment.
func TestOversizeLineIsReportedNotFatal(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "trail.jsonl")
	big := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"` +
		strings.Repeat("x", 5<<20) + `"}]}}`
	body := big + "\n" + `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"after"}]}}` + "\n"
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	f := &feeder{path: p}
	lines, err := f.next()
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2 (an oversize marker and the line after it)", len(lines))
	}
	if !strings.Contains(lines[0], "too large to decode") {
		t.Fatalf("oversize line must be reported: %q", lines[0])
	}
	r := &renderer{format: FormatClaudeTranscript, width: 0, tools: map[string]string{}}
	if out := strings.Join(r.render(lines), "\n"); !strings.Contains(out, "after") {
		t.Fatalf("the line after an oversize one must still render:\n%s", out)
	}
}

// The trail is appended to while it is read, so the final line is routinely a
// partial JSON object. Rendering it as truncated JSON, or losing it when the
// rest arrives, both lose the newest turn — the one being waited for.
func TestFeederHoldsBackAPartialLineUntilItIsComplete(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "trail.jsonl")
	first := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"one"}]}}` + "\n"
	half := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"te`
	rest := `xt","text":"two"}]}}` + "\n"
	if err := os.WriteFile(p, []byte(first+half), 0o644); err != nil {
		t.Fatal(err)
	}
	f := &feeder{path: p}
	lines, err := f.next()
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 {
		t.Fatalf("got %d complete lines, want 1 (the partial is held back): %q", len(lines), lines)
	}
	fh, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fh.WriteString(rest); err != nil {
		t.Fatal(err)
	}
	fh.Close()
	lines, err = f.next()
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 || !strings.Contains(lines[0], `"two"`) {
		t.Fatalf("the completed line must arrive whole, got %q", lines)
	}
}

// Ambiguity is refused with the candidates, never resolved by picking one. The
// guess this replaces — "the newest .jsonl in the cwd slug" — is precisely what
// broke when two rounds shared a cwd.
func TestAmbiguousLabelListsCandidatesAndRefuses(t *testing.T) {
	dir := t.TempDir()
	a := startRun(t, dir)
	b := startRun(t, dir)
	if a == b {
		t.Fatal("the two rounds need distinct ids")
	}
	var stdout, stderr bytes.Buffer
	rc := Main([]string{"probe"}, strings.NewReader(""), &stdout, &stderr)
	if rc != ExitUsage {
		t.Fatalf("rc=%d, want %d (usage); stderr=%s", rc, ExitUsage, stderr.String())
	}
	for _, want := range []string{a, b, "matches 2 runs"} {
		if !strings.Contains(stderr.String(), want) {
			t.Fatalf("the refusal must name %q:\n%s", want, stderr.String())
		}
	}
}

// "Not revealed yet" is a state, not a failure of the tool, and it says how it
// ends. A round that has been alive for a minute with no trail is a real
// signal; silently printing nothing would hide it.
func TestNoTrailYetExplainsItself(t *testing.T) {
	dir := t.TempDir()
	id := startRun(t, dir)
	var stdout, stderr bytes.Buffer
	if rc := Main([]string{id}, strings.NewReader(""), &stdout, &stderr); rc != ExitNoTrail {
		t.Fatalf("rc=%d, want %d", rc, ExitNoTrail)
	}
	if !strings.Contains(stderr.String(), "has not revealed a trail yet") {
		t.Fatalf("unhelpful refusal: %s", stderr.String())
	}
}

// The end-to-end read: a revealed trail is resolved from a label and rendered.
func TestViewRendersARevealedTrail(t *testing.T) {
	dir := t.TempDir()
	trail := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(trail, []byte(transcriptFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	id := startRun(t, dir, "--trail-format", FormatClaudeTranscript)
	if err := runs.SetTrail(id, trail); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if rc := Main([]string{id, "-w", "0"}, strings.NewReader(""), &stdout, &stderr); rc != 0 {
		t.Fatalf("rc=%d; stderr=%s", rc, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{trail, "💬 Reading the launcher first.", "🔧 Bash go test ./internal/launch/"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output is missing %q:\n%s", want, out)
		}
	}
}

// Follow mode ends with the round, on the registry's word. A `tail -f` that
// outlives its subject is the zombie-shell class this repo has been bitten by
// (2026-09-11: ten shells, 9-21 hours old).
func TestFollowStopsWhenTheRoundFinishes(t *testing.T) {
	old := pollInterval
	pollInterval = 5 * time.Millisecond
	defer func() { pollInterval = old }()

	dir := t.TempDir()
	trail := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(trail, []byte(transcriptFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	id := startRun(t, dir, "--trail-format", FormatClaudeTranscript)
	if err := runs.SetTrail(id, trail); err != nil {
		t.Fatal(err)
	}

	// The last turn lands in the window the drain exists for: after the read
	// that would have seen it, before the registry check that ends the loop. A
	// harness process and the launcher's rc write have no ordering between them
	// from a reader's point of view, so this is an ordering that really happens
	// — and it is the only one a test cannot reach by sleeping.
	appended := false
	followProbe = func() {
		if appended {
			return
		}
		appended = true
		fh, err := os.OpenFile(trail, os.O_APPEND|os.O_WRONLY, 0o644)
		if err == nil {
			fh.WriteString(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"the last word"}]}}` + "\n")
			fh.Close()
		}
		runs.Main([]string{"finish", id, "--rc", "0"}, os.Stdout, os.Stderr)
	}
	defer func() { followProbe = nil }()

	done := make(chan int, 1)
	var stdout, stderr bytes.Buffer
	go func() { done <- Main([]string{id, "-f"}, strings.NewReader(""), &stdout, &stderr) }()
	select {
	case rc := <-done:
		if rc != 0 {
			t.Fatalf("rc=%d; stderr=%s", rc, stderr.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("follow did not end with the round — it must stop on rc, not on a timeout")
	}
	out := stdout.String()
	if !strings.Contains(out, "the last word") {
		t.Fatalf("the final turn was lost to the poll gap:\n%s", out)
	}
	if !strings.Contains(out, "rc=0") {
		t.Fatalf("the close-out line must carry the round's rc:\n%s", out)
	}
}

func TestKnownFormatIsTheRenderersOwnList(t *testing.T) {
	for _, f := range []string{FormatClaudeTranscript, FormatOpencodeEvents, FormatLines} {
		if !KnownFormat(f) {
			t.Fatalf("%q must be known", f)
		}
	}
	if KnownFormat("stream-json-but-decoded") {
		t.Fatal("an undeclared format must not pass")
	}
}

func splitLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}

// muse streams prose in fragments: a measured round (2026-09-18) split one
// paragraph across fifteen run.output.delta events, and rendering each as its
// own 💬 line shredded the follow view mid-word. Consecutive speech is one
// utterance.
//
// FAIL-first: drop the coalesceSpeech call in render and this returns a line
// per fragment, with words split across them.
func TestMuseSpeechIsCoalescedButToolCallsStillBreakIt(t *testing.T) {
	ev := func(pt, body string) string {
		return `{"payload_type":"` + pt + `","payload":` + body + `}`
	}
	lines := []string{
		ev("run.output.delta", `{"text":"Created fizz"}`),
		ev("run.output.delta", `{"text":".py and ran "}`),
		ev("run.output.delta", `{"text":"the tests."}`),
		ev("tool.result", `{"text":"all tests passed","correlation_facts":{"tool_name":"bash","outcome":"success"}}`),
		ev("run.output.delta", `{"text":"Done."}`),
	}
	r := &renderer{format: FormatMuseEvents, width: 0, tools: map[string]string{}}
	got := r.render(lines)
	want := []string{
		"💬 Created fizz.py and ran the tests.",
		"🔧 bash all tests passed",
		"💬 Done.",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("rendered:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// A failed tool call is the line a reader is looking for, so it is marked and
// carries the outcome rather than being rendered as ordinary work.
func TestMuseFailedToolCallIsMarked(t *testing.T) {
	line := `{"payload_type":"tool.result","payload":{"text":"outsource git guard: refused ` + "`git commit`" + `\nBLOCKED","correlation_facts":{"tool_name":"bash","outcome":"error"}}}`
	r := &renderer{format: FormatMuseEvents, width: 0, tools: map[string]string{}}
	got := r.render([]string{line})
	if len(got) != 1 || !strings.HasPrefix(got[0], "✗ ") {
		t.Fatalf("a failed tool call must be marked, got %q", got)
	}
	if !strings.Contains(got[0], "error") || !strings.Contains(got[0], "refused") {
		t.Fatalf("the mark must carry the outcome and the headline: %q", got[0])
	}
	// Only the headline: muse's results are multi-line and the rest is the log's job.
	if strings.Contains(got[0], "BLOCKED") {
		t.Fatalf("only the first line belongs in the follow view: %q", got[0])
	}
}
