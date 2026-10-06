package planlimit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Fixture lines in the shape of the 2026-10-06 incident trail (fields
// abridged, request ids scrubbed to a fixed fake). The real trail ends with
// the 429 line followed by two bookkeeping lines, so the fixture does too.
const (
	userLine   = `{"type":"user","timestamp":"2026-10-06T06:12:05.000Z","message":{"role":"user","content":"the spec"}}` + "\n"
	workLine   = `{"type":"assistant","timestamp":"2026-10-06T06:29:58.000Z","message":{"model":"glm-5.3","content":[{"type":"tool_use","name":"Bash","input":{"command":"go test ./..."}}]}}` + "\n"
	limitLine  = `{"type":"assistant","timestamp":"2026-10-06T06:30:41.477Z","isApiErrorMessage":true,"error":"rate_limit","apiErrorStatus":429,"apiErrorIsTransient":false,"message":{"model":"<synthetic>","content":[{"type":"text","text":"API Error: Request rejected (429) · [1308][Usage limit reached for 5 hour. Your limit will reset at 2026-10-06 16:23:45][20261006143041000000000000000000]"}]}}` + "\n"
	answerLine = `{"type":"assistant","timestamp":"2026-10-06T08:25:02.000Z","message":{"model":"glm-5.3","content":[{"type":"text","text":"Report.\n\nDONE-X"}]}}` + "\n"
	tailLines  = `{"type":"last-prompt","lastPrompt":"the spec"}` + "\n" + `{"type":"cost-state","totalCostUSD":1}` + "\n"
)

func writeTrail(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// The incident: the last assistant entry is the 429. FAIL-first: with the
// status test in Detect inverted, Died reads false here.
func TestFinal429IsADeath(t *testing.T) {
	d, err := Detect(writeTrail(t, userLine+workLine+limitLine+tailLines), "zai")
	if err != nil {
		t.Fatal(err)
	}
	if !d.Died {
		t.Fatalf("a trail ending in a 429 is a plan-limit death; got %+v", d)
	}
	if got, want := d.APIErrorField(), "429 [1308][Usage limit reached for 5 hour. Your limit will reset at 2026-10-06 16:23:45][20261006143041000000000000000000]"; got != want {
		t.Fatalf("api_error = %q, want %q", got, want)
	}
	if want := time.Date(2026, 10, 6, 6, 30, 41, 477000000, time.UTC); !d.At.Equal(want) {
		t.Fatalf("At = %v, want %v", d.At, want)
	}
	if d.ResetText != "2026-10-06 16:23:45" {
		t.Fatalf("ResetText = %q", d.ResetText)
	}
}

// The request id opens with z.ai's own clock, 14:30:41 on a 06:30:41Z line, so
// the zone-less reset is UTC+8. FAIL-first: parsed in UTC the reset reads
// 16:23:45Z, eight hours late.
func TestZaiResetIsUTCPlus8(t *testing.T) {
	d, err := Detect(writeTrail(t, userLine+limitLine), "zai")
	if err != nil {
		t.Fatal(err)
	}
	if got := d.ResetAtField(); got != "2026-10-06T08:23:45Z" {
		t.Fatalf("reset_at = %s, want 2026-10-06T08:23:45Z", got)
	}
}

// A 429 the harness got past — its retry answered, or the session was
// resumed — is history, not the way the round ended. FAIL-first: deciding on
// "any 429 in the trail" reads Died here.
func TestEarlier429FollowedByAnAnswerIsNotADeath(t *testing.T) {
	d, err := Detect(writeTrail(t, userLine+workLine+limitLine+answerLine+tailLines), "zai")
	if err != nil {
		t.Fatal(err)
	}
	if d.Died {
		t.Fatalf("a 429 followed by a real answer is not a death; got %+v", d)
	}
}

// A reset this package cannot read is "unknown", never a guess: a text with no
// reset phrase, a phrase whose time does not parse, and a provider with no row
// in the table. FAIL-first: falling back to time.Now()+5h (a guess) gives a
// non-"unknown" reset_at in each case.
func TestUnparsableResetIsUnknown(t *testing.T) {
	cases := map[string]struct{ provider, text string }{
		"no reset phrase":    {"zai", "API Error: Request rejected (429) · [1302][Rate limit reached for requests]"},
		"impossible time":    {"zai", "API Error: Request rejected (429) · [1308][Your limit will reset at 2026-13-45 99:99:99]"},
		"provider with none": {"xai", "API Error: Request rejected (429) · [1308][Your limit will reset at 2026-10-06 16:23:45]"},
	}
	for name, c := range cases {
		line := strings.Replace(limitLine, "API Error: Request rejected (429) · [1308][Usage limit reached for 5 hour. Your limit will reset at 2026-10-06 16:23:45][20261006143041000000000000000000]", c.text, 1)
		d, err := Detect(writeTrail(t, userLine+line), c.provider)
		if err != nil {
			t.Fatal(err)
		}
		if !d.Died {
			t.Fatalf("%s: still a plan-limit death; got %+v", name, d)
		}
		if got := d.ResetAtField(); got != Unknown {
			t.Fatalf("%s: reset_at = %s, want unknown", name, got)
		}
	}
}

// error=rate_limit without a recorded status is still the plan limit; a
// different API error at the end (a 500) is not.
func TestRateLimitKindAndOtherErrors(t *testing.T) {
	noStatus := strings.Replace(limitLine, `"apiErrorStatus":429,`, "", 1)
	d, _ := Detect(writeTrail(t, userLine+noStatus), "zai")
	if !d.Died || d.Status != "rate_limit" {
		t.Fatalf("error=rate_limit alone is a death named by its kind; got %+v", d)
	}
	server := strings.NewReplacer(`"error":"rate_limit"`, `"error":"server_error"`, `"apiErrorStatus":429`, `"apiErrorStatus":500`).Replace(limitLine)
	if d, _ := Detect(writeTrail(t, userLine+server), "zai"); d.Died {
		t.Fatalf("a final 500 is not a plan limit; got %+v", d)
	}
}

// The api_error field stays one line and at most 160 runes, whatever the
// provider sent.
func TestAPIErrorIsCappedAndFlat(t *testing.T) {
	long := "API Error: Request rejected (429) · [" + strings.Repeat("x", 300) + "\n" + "]"
	line := strings.Replace(limitLine, "API Error: Request rejected (429) · [1308][Usage limit reached for 5 hour. Your limit will reset at 2026-10-06 16:23:45][20261006143041000000000000000000]", strings.ReplaceAll(long, "\n", `\n`), 1)
	d, _ := Detect(writeTrail(t, userLine+line), "zai")
	if n := len([]rune(d.Message)); n > maxMessage || strings.Contains(d.Message, "\n") {
		t.Fatalf("message must be one line of at most %d runes; got %d: %q", maxMessage, n, d.Message)
	}
}

func TestIsAPIErrorText(t *testing.T) {
	if !IsAPIErrorText("API Error: Request rejected (429) · [1308][…]") {
		t.Fatal("the API error line alone is no report")
	}
	if IsAPIErrorText("Report.\nAPI Error: seen once, then fixed\nDONE-X") || IsAPIErrorText("Report.") {
		t.Fatal("a real report is a report")
	}
}

// L2, live and read-only: the incident trail itself. Skipped unless the path
// is given, because it is another session's file on one machine.
//
//	PLANLIMIT_LIVE_TRAIL=<trail> go test ./internal/planlimit -run Live -v
func TestLiveTrail(t *testing.T) {
	p := os.Getenv("PLANLIMIT_LIVE_TRAIL")
	if p == "" {
		t.Skip("PLANLIMIT_LIVE_TRAIL not set")
	}
	d, err := Detect(p, "zai")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("died=%v reset_at=%s api_error=%s at=%s", d.Died, d.ResetAtField(), d.APIErrorField(), d.At.Format(time.RFC3339))
	if want := os.Getenv("PLANLIMIT_LIVE_WANT"); want != "" && d.ResetAtField() != want {
		t.Fatalf("reset_at = %s, want %s", d.ResetAtField(), want)
	}
}
