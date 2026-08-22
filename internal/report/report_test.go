package report

import (
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
