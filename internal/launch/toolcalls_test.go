package launch

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/midagedev/outsource/internal/runs"
)

// Minimal session-transcript lines in the shape analyzeRun already walks:
// assistant turns carrying content blocks, tool calls among them. The incident
// this gate closes (2026-09-29) was a transcript of 23 lines, zero tool_use
// blocks, and a report claiming a self-test had passed.
const (
	tcUserLine = `{"type":"user","message":{"role":"user","content":"the spec"}}` + "\n"
	tcTextLine = `{"type":"assistant","message":{"model":"glm-5.3","content":[{"type":"text","text":"working"}]}}` + "\n"
	tcToolLine = `{"type":"assistant","message":{"model":"glm-5.3","content":[{"type":"tool_use","name":"Bash","input":{"command":"go test ./..."}}]}}` + "\n"
	// A sidechain (subagent) line: still this round's work, so its tool_use
	// counts. Delegated rounds have agents off, but the transcript format
	// allows the line and the counter must not silently drop it.
	tcSidechainToolLine = `{"type":"assistant","isSidechain":true,"message":{"model":"glm-5.3","content":[{"type":"tool_use","name":"Read","input":{}}]}}` + "\n"
)

// writeTrail writes a transcript fixture and returns its path.
func writeTrail(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// writeResultLog writes the --output-format json shape one finished claude-code
// round leaves in --log: a single result event whose result field is the
// report. Returns the log path.
func writeResultLog(t *testing.T, report string) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"type": "result", "subtype": "success", "session_id": "test-session",
		"result": report,
	})
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "run.log")
	if err := os.WriteFile(p, append(b, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// claudeRound builds the round finish() receives after a claude-code harness
// run: transcript located (trail), log written, sid known.
func claudeRound(logPath, trail string, marker string, allowNoTools bool) (*round, *bytes.Buffer, *bytes.Buffer) {
	var out, errb bytes.Buffer
	return &round{
		o: opts{harness: "claude-code", log: logPath, doneMarker: marker,
			model: "glm-5.3", allowNoTools: allowNoTools},
		p:        provider{name: "zai"},
		trail:    trail,
		sid:      "test-session",
		stdout:   &out,
		stderr:   &errb,
		specBody: "spec body\n" + marker,
	}, &out, &errb
}

func sentinelOf(t *testing.T, logPath string) string {
	t.Helper()
	b, err := os.ReadFile(logPath + ".rc")
	if err != nil {
		t.Fatalf("no sentinel at %s.rc: %v", logPath, err)
	}
	return string(b)
}

// (a) The incident's near-miss, closed: zero tool_use blocks, the marker
// present and correct — rc 0 used to be automatic, and the fabrication scored
// a pass. FAIL-first: make countToolUses return n+1 and this goes red.
func TestZeroToolCallsWithMarkerIsExit73(t *testing.T) {
	log := writeResultLog(t, "Report: all gates green, file written.\n\nDONE-tc")
	trail := writeTrail(t, tcUserLine+tcTextLine)
	r, out, errb := claudeRound(log, trail, "DONE-tc", false)

	if rc := r.finish(0); rc != ExitNoToolCalls {
		t.Fatalf("rc = %d, want %d (73); stderr:\n%s", rc, ExitNoToolCalls, errb.String())
	}
	sent := sentinelOf(t, log)
	for _, want := range []string{"tool_calls=0\n", "done_marker=found"} {
		if !strings.Contains(sent, want) {
			t.Fatalf("sentinel is missing %q; got:\n%s", want, sent)
		}
	}
	// The refusal names the report's last line, so a lead reading only the
	// terminal sees the claim that was made.
	if !strings.HasPrefix(errb.String(), "outsource: the round made no tool calls") {
		t.Fatalf("stderr must start with the contract prefix; got:\n%s", errb.String())
	}
	if !strings.Contains(errb.String(), `"DONE-tc"`) {
		t.Fatalf("stderr must name the report's last line; got:\n%s", errb.String())
	}
	if !strings.Contains(out.String(), "SESSION test-session") {
		t.Fatalf("SESSION line missing: %s", out.String())
	}
}

// (b) 72 keeps precedence: an absent marker is already a reason this round is
// not a pass, the codes do not stack, and the sentinel carries both facts.
func TestZeroToolCallsWithAbsentMarkerKeeps72(t *testing.T) {
	log := writeResultLog(t, "Report without the marker.\n\n완료 마커: `DONE-tc`")
	trail := writeTrail(t, tcUserLine+tcTextLine)
	r, _, errb := claudeRound(log, trail, "DONE-tc", false)

	if rc := r.finish(0); rc != ExitNoMarker {
		t.Fatalf("rc = %d, want %d (72) — the codes must not stack", rc, ExitNoMarker)
	}
	sent := sentinelOf(t, log)
	if !strings.Contains(sent, "tool_calls=0\n") {
		t.Fatalf("sentinel must still record tool_calls=0; got:\n%s", sent)
	}
	if strings.Contains(errb.String(), "the round made no tool calls") {
		t.Fatalf("no second refusal line when 72 already fired; got:\n%s", errb.String())
	}
}

// (c) A round that worked is counted and passes untouched. FAIL-first: make
// countToolUses return 0 always and the tool_calls=N assertion goes red.
func TestToolCallsCountedWhenPresent(t *testing.T) {
	log := writeResultLog(t, "Report.\n\nDONE-tc")
	trail := writeTrail(t, tcUserLine+tcToolLine+tcTextLine+tcToolLine)
	r, _, errb := claudeRound(log, trail, "DONE-tc", false)

	if rc := r.finish(0); rc != 0 {
		t.Fatalf("rc = %d, want 0; stderr:\n%s", rc, errb.String())
	}
	if sent := sentinelOf(t, log); !strings.Contains(sent, "tool_calls=2\n") {
		t.Fatalf("sentinel is missing tool_calls=2; got:\n%s", sent)
	}
	if errb.String() != "" {
		t.Fatalf("a working round must not print about tool calls; got:\n%s", errb.String())
	}
}

// (d) No trail path means the count cannot be taken — recorded as unknown,
// never as a zero that would turn a missing transcript into a fabrication
// verdict. rc unchanged.
func TestNoTrailPathRecordsUnknown(t *testing.T) {
	log := writeResultLog(t, "Report.\n\nDONE-tc")
	r, _, errb := claudeRound(log, "", "DONE-tc", false)

	if rc := r.finish(0); rc != 0 {
		t.Fatalf("rc = %d, want 0 — unknown never changes rc; stderr:\n%s", rc, errb.String())
	}
	sent := sentinelOf(t, log)
	if !strings.Contains(sent, "tool_calls=unknown (no trail path recorded)\n") {
		t.Fatalf("sentinel must record the unknown count with its reason; got:\n%s", sent)
	}
	if !strings.Contains(errb.String(), "could not be taken") {
		t.Fatalf("stderr must say the count could not be taken; got:\n%s", errb.String())
	}
}

// The trail the count comes from may only exist in the registry: the
// SessionStart hook reveals the transcript path into the run record, and a
// round whose analyzeRun found nothing still deserves a count. The sentinel
// then names the file the count came from.
func TestTrailPathReadFromRegistryWhenRevealWasLate(t *testing.T) {
	log := writeResultLog(t, "Report.\n\nDONE-tc")
	trail := writeTrail(t, tcUserLine+tcToolLine+tcToolLine)
	id := registerRun("tc-late-reveal", "zai", "claude-code", "glm-5.3",
		t.TempDir(), "/spec.md", log, "", "", "claude-transcript")
	if id == "" {
		t.Fatal("registerRun refused the round")
	}
	if err := setTrailForTest(id, trail); err != nil {
		t.Fatal(err)
	}
	r, _, _ := claudeRound(log, "", "DONE-tc", false)
	r.runID = id

	if rc := r.finish(0); rc != 0 {
		t.Fatalf("rc = %d, want 0", rc)
	}
	sent := sentinelOf(t, log)
	if !strings.Contains(sent, "tool_calls=2\n") {
		t.Fatalf("sentinel is missing the registry-sourced count; got:\n%s", sent)
	}
	if !strings.Contains(sent, "trail="+trail+"\n") {
		t.Fatalf("sentinel must name the file the count came from; got:\n%s", sent)
	}
}

// An unreadable trail is unknown with the reason — not zero, not a crash.
func TestUnreadableTrailRecordsUnknown(t *testing.T) {
	log := writeResultLog(t, "Report.\n\nDONE-tc")
	r, _, _ := claudeRound(log, filepath.Join(t.TempDir(), "gone.jsonl"), "DONE-tc", false)

	if rc := r.finish(0); rc != 0 {
		t.Fatalf("rc = %d, want 0", rc)
	}
	if sent := sentinelOf(t, log); !strings.Contains(sent, "tool_calls=unknown (") {
		t.Fatalf("sentinel must record unknown with the open error; got:\n%s", sent)
	}
}

// (e) A malformed line does not stop the count: the parsable lines are
// counted and the sentinel says partial. A partial count is a lower bound
// over a damaged file, so it never fires exit 73 — "zero tool_use blocks" is
// not something a file with unparseable lines can assert.
func TestMalformedLinesCountParsableAndSayPartial(t *testing.T) {
	log := writeResultLog(t, "Report.\n\nDONE-tc")
	trail := writeTrail(t, tcUserLine+tcToolLine+"{this is not json\n"+tcToolLine)
	r, _, errb := claudeRound(log, trail, "DONE-tc", false)

	if rc := r.finish(0); rc != 0 {
		t.Fatalf("rc = %d, want 0 — partial never fires 73; stderr:\n%s", rc, errb.String())
	}
	if sent := sentinelOf(t, log); !strings.Contains(sent, "tool_calls=2 (partial: 1 unparseable line)\n") {
		t.Fatalf("sentinel must count the parsable lines and say partial; got:\n%s", sent)
	}

	// All-tool lines unparseable: still partial, still not a zero verdict.
	log2 := writeResultLog(t, "Report.\n\nDONE-tc")
	trail2 := writeTrail(t, "{{\nnot json\n")
	r2, _, _ := claudeRound(log2, trail2, "DONE-tc", false)
	if rc := r2.finish(0); rc != 0 {
		t.Fatalf("rc = %d, want 0 — a partial zero must not become 73", rc)
	}
	if sent := sentinelOf(t, log2); !strings.Contains(sent, "tool_calls=0 (partial: 2 unparseable lines)\n") {
		t.Fatalf("sentinel must record the partial zero with its caveat; got:\n%s", sent)
	}
}

// (f) The opt-out: an answer-only round (a pure question) is allowed to have
// made no tool calls, and the allowance is recorded rather than silent.
func TestAllowNoToolsRecordsTheAllowance(t *testing.T) {
	log := writeResultLog(t, "Answer: 42.\n\nDONE-tc")
	trail := writeTrail(t, tcUserLine+tcTextLine)
	r, _, errb := claudeRound(log, trail, "DONE-tc", true)

	if rc := r.finish(0); rc != 0 {
		t.Fatalf("rc = %d, want 0 under --allow-no-tools; stderr:\n%s", rc, errb.String())
	}
	if sent := sentinelOf(t, log); !strings.Contains(sent, "tool_calls=0 (allowed)\n") {
		t.Fatalf("sentinel must record the allowance; got:\n%s", sent)
	}
	if !strings.Contains(errb.String(), "--allow-no-tools") {
		t.Fatalf("stderr must say the allowance was applied; got:\n%s", errb.String())
	}
}

// (g) No behaviour change on the other harnesses: no counting, no
// tool_calls line, rc untouched — even when the trail content looks countable.
func TestOtherHarnessesGetNoToolCallsLine(t *testing.T) {
	trail := writeTrail(t, tcUserLine+tcToolLine)
	for _, harness := range []string{"crush", "opencode", "agy", "muse"} {
		log := filepath.Join(t.TempDir(), "run.log")
		if err := os.WriteFile(log, []byte("report body\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		var out, errb bytes.Buffer
		r := &round{
			o:      opts{harness: harness, log: log, model: "m"},
			p:      provider{name: "zai"},
			trail:  trail,
			stdout: &out, stderr: &errb,
		}
		if rc := r.finish(0); rc != 0 {
			t.Fatalf("%s: rc = %d, want 0; stderr:\n%s", harness, rc, errb.String())
		}
		if sent := sentinelOf(t, log); strings.Contains(sent, "tool_calls=") {
			t.Fatalf("%s: sentinel must carry no tool_calls line; got:\n%s", harness, sent)
		}
	}
}

// The incident complement: no --done-marker at all, zero tool calls, a clean
// exit. The marker verdict has nothing to say; the tool-call verdict still
// must — a fabricated round with no marker in its spec is not a pass either.
func TestNoMarkerRoundWithZeroToolCallsStill73(t *testing.T) {
	log := writeResultLog(t, "Confident report, no marker asked for.")
	trail := writeTrail(t, tcUserLine+tcTextLine)
	r, _, errb := claudeRound(log, trail, "", false)

	if rc := r.finish(0); rc != ExitNoToolCalls {
		t.Fatalf("rc = %d, want %d (73); stderr:\n%s", rc, ExitNoToolCalls, errb.String())
	}
	if sent := sentinelOf(t, log); !strings.Contains(sent, "tool_calls=0\n") {
		t.Fatalf("sentinel is missing tool_calls=0; got:\n%s", sent)
	}
}

// A round that already failed keeps its rc — the count is recorded, nothing
// stacks, and no refusal line is printed for a round already refused.
func TestToolCallsRecordedOnFailedRounds(t *testing.T) {
	log := writeResultLog(t, "Report.\n\nDONE-tc")
	trail := writeTrail(t, tcUserLine+tcToolLine)
	r, _, errb := claudeRound(log, trail, "DONE-tc", false)

	if rc := r.finish(ExitModelIdentity); rc != ExitModelIdentity {
		t.Fatalf("rc = %d, want it unchanged (70)", rc)
	}
	if sent := sentinelOf(t, log); !strings.Contains(sent, "tool_calls=1\n") {
		t.Fatalf("sentinel must record the count on a failed round too; got:\n%s", sent)
	}
	if errb.String() != "" {
		t.Fatalf("no tool-call stderr for an already-failed round; got:\n%s", errb.String())
	}
}

// A sidechain (subagent) tool_use is still this round's work: it counts.
func TestSidechainToolUseCounts(t *testing.T) {
	n, bad, err := countToolUses(writeTrail(t, tcUserLine+tcSidechainToolLine+tcTextLine))
	if err != nil || bad != 0 {
		t.Fatalf("err=%v bad=%d, want clean", err, bad)
	}
	if n != 1 {
		t.Fatalf("sidechain tool_use not counted: n=%d, want 1", n)
	}
}

// The counter itself, on the shapes that matter beyond the verdict: empty
// file, non-assistant lines only, blank lines, multiple blocks per message.
func TestCountToolUsesShapes(t *testing.T) {
	cases := []struct {
		name string
		body string
		n    int
		bad  int
	}{
		{"empty file", "", 0, 0},
		{"blank lines only", "\n\n", 0, 0},
		{"no assistant lines", tcUserLine, 0, 0},
		{"two blocks in one message", `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash"},{"type":"tool_use","name":"Read"},{"type":"text","text":"hi"}]}}` + "\n", 2, 0},
		{"non-assistant tool_use-shaped line is not counted", `{"type":"user","message":{"content":[{"type":"tool_use"}]}}` + "\n", 0, 0},
	}
	for _, c := range cases {
		n, bad, err := countToolUses(writeTrail(t, c.body))
		if err != nil {
			t.Fatalf("%s: err=%v", c.name, err)
		}
		if n != c.n || bad != c.bad {
			t.Fatalf("%s: n=%d bad=%d, want n=%d bad=%d", c.name, n, bad, c.n, c.bad)
		}
	}
}

// setTrailForTest is the registry reveal a round's SessionStart hook makes.
// Direct call, because the hook itself is a separate process in production.
func setTrailForTest(id, path string) error {
	return runs.SetTrail(id, path)
}
