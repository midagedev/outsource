package report

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestEndsWithMarker pins the 2026-08-22 field false-positive (gadak GDK-616
// round): the delegate's FINAL message was a promise — "…the report's last
// line will be `DONE-GDK616`" — and Contains() over the report scored it
// found while the round's gates were still running. The verdict is the
// spec's own contract: the last non-empty line IS the marker.
func TestEndsWithMarker(t *testing.T) {
	cases := []struct {
		name   string
		rep    string
		marker string
		want   bool
	}{
		{"exact last line", "work done.\n\nDONE-X", "DONE-X", true},
		{"backticked last line", "done.\n`DONE-X`", "DONE-X", true},
		{"bold last line", "done.\n**DONE-X**", "DONE-X", true},
		{"trailing blank lines", "done.\nDONE-X\n\n\n", "DONE-X", true},
		// The incident: marker quoted mid-sentence in the last line.
		{"promissory sentence", "대기 중 — 이후 마지막 줄 `DONE-X`)를 작성하는 것입니다.", "DONE-X", false},
		{"marker mid-report only", "DONE-X\nbut then more text", "DONE-X", false},
		{"punctuation glued", "done.\nDONE-X.", "DONE-X", false},
		{"empty report", "", "DONE-X", false},
		{"empty marker", "DONE-X", "", false},
	}
	for _, c := range cases {
		if got := EndsWithMarker(c.rep, c.marker); got != c.want {
			t.Errorf("%s: EndsWithMarker(%q, %q) = %v, want %v", c.name, c.rep, c.marker, got, c.want)
		}
	}
}

// Trimmed from a real `opencode run --format json` capture (2026-08-23 E1).
// The first text event is a planning sentence that must NOT survive a later
// tool_use; the report is part.text after the last tool_use.
const opencodeJSONL = `{"type":"step_start","sessionID":"ses_test","part":{"type":"step-start"}}
{"type":"text","sessionID":"ses_test","part":{"type":"text","text":"PLAN: I will write hello.txt then report."}}
{"type":"tool_use","sessionID":"ses_test","part":{"type":"tool","tool":"write","state":{"status":"completed","input":{"filePath":"hello.txt","content":"hi\n"},"output":"Wrote file successfully."}}}
{"type":"step_finish","sessionID":"ses_test","part":{"type":"step-finish","reason":"tool-calls"}}
{"type":"step_start","sessionID":"ses_test","part":{"type":"step-start"}}
{"type":"text","sessionID":"ses_test","part":{"type":"text","text":"Created hello.txt with hi and a newline.\nDONE-OC"}}
{"type":"step_finish","sessionID":"ses_test","part":{"type":"step-finish","reason":"stop"}}
`

func TestExtractOpencodeTextAfterLastToolUse(t *testing.T) {
	// FAIL-first (verbatim, before Extract learned part.text):
	//   Extract returned no report for an opencode JSONL with a trailing text part
	got, ok := Extract(strings.NewReader(opencodeJSONL))
	if !ok {
		t.Fatal("Extract returned no report for an opencode JSONL with a trailing text part")
	}
	if !strings.Contains(got, "Created hello.txt") {
		t.Errorf("report missing final text, got %q", got)
	}
	if !EndsWithMarker(got, "DONE-OC") {
		t.Errorf("report should end with DONE-OC, got %q", got)
	}
	if strings.Contains(got, "PLAN:") {
		t.Errorf("planning text before the last tool_use leaked into the report: %q", got)
	}
	if strings.Contains(got, "Wrote file successfully") {
		t.Errorf("tool output leaked into the report: %q", got)
	}
}

func TestExtractOpencodeToollessText(t *testing.T) {
	// A round that never called a tool still has a report (E3-auto's "Red").
	log := `{"type":"step_start","sessionID":"ses_t","part":{"type":"step-start"}}
{"type":"text","sessionID":"ses_t","part":{"type":"text","text":"Red"}}
{"type":"step_finish","sessionID":"ses_t","part":{"type":"step-finish","reason":"stop"}}
`
	got, ok := Extract(strings.NewReader(log))
	if !ok || got != "Red" {
		t.Fatalf("toolless opencode text: got %q ok=%v, want Red", got, ok)
	}
}

func TestExtractCrushPlainText(t *testing.T) {
	// A crush log is not JSONL at all: narration and report run together as
	// prose. Shape taken verbatim from the GDK-962 round (2026-08-27), whose
	// sentinel said done_marker=found while last-report exited 65.
	log := `I'll start by reading the key files.Now the resolver:Now the tests:

# GDK-962: the final report

## 1. Files changed
- edit.go

## 6. Deliberately left untouched
- the changelog

DONE-p962
`
	got, ok := Extract(strings.NewReader(log))
	if !ok {
		t.Fatal("a plain-text crush log must yield its report")
	}
	if !strings.HasPrefix(got, "# GDK-962: the final report") {
		t.Fatalf("must cut at the H1, not inside the report; got: %.60q", got)
	}
	if strings.Contains(got, "I'll start by reading") {
		t.Fatalf("narration before the heading must not be included; got: %.60q", got)
	}
	if !EndsWithMarker(got, "DONE-p962") {
		t.Fatalf("the marker must survive to the last line; got tail: %.40q", got[len(got)-40:])
	}

	// The arm is gated on the whole file being non-JSON: a JSONL log that
	// simply held no report must still be "no report", never scavenged.
	if _, ok := Extract(strings.NewReader(`{"type":"system","subtype":"init"}` + "\n")); ok {
		t.Fatal("a JSONL log with no report must stay empty, not fall through to the plain-text arm")
	}
}

func TestLastReportNamesKilledSentinel(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "run.ndjson")
	if err := os.WriteFile(log, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sentinel := "rc=-1\nfinished=2026-08-22T17:08:28Z\nwrapper_signal=TERM\n"
	if err := os.WriteFile(log+".rc", []byte(sentinel), 0o644); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	rc := Main([]string{log}, io.Discard, &stderr)
	if rc != ExitNoReport {
		t.Fatalf("want ExitNoReport (%d), got %d", ExitNoReport, rc)
	}
	got := stderr.String()
	if !strings.Contains(got, "no report-shaped content") {
		t.Errorf("must keep the original 65 line, got %s", got)
	}
	for _, want := range []string{"killed (TERM)", "2026-08-22T17:08:28Z", "rc=-1", log + ".rc"} {
		if !strings.Contains(got, want) {
			t.Errorf("sentinel diagnosis missing %q; got %s", want, got)
		}
	}
}

func TestLastReportNamesRunningRound(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("OUTSOURCE_RUNS_DIR", dir)
	log := filepath.Join(dir, "live.log")
	if err := os.WriteFile(log, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	pid := strconv.Itoa(os.Getpid())
	rec := "id=test-running\npid=" + pid + "\nlabel=live\nlog=" + log + "\nstartedAt=1\n"
	if err := os.WriteFile(filepath.Join(dir, "test-running.run"), []byte(rec), 0o644); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	rc := Main([]string{log}, io.Discard, &stderr)
	if rc != ExitNoReport {
		t.Fatalf("want ExitNoReport (%d), got %d stderr=%s", ExitNoReport, rc, stderr.String())
	}
	if !strings.Contains(stderr.String(), "round still running — no report expected yet") {
		t.Errorf("running diagnosis missing; got %s", stderr.String())
	}
}
