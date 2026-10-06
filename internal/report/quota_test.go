package report

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The 2026-10-06 incident's shapes, scrubbed: the claude-code log a 429 leaves
// (its result field is the API error line), the sentinel the launcher wrote
// then (no quota keys), and the trail it names.
const (
	limitText   = "API Error: Request rejected (429) · [1308][Usage limit reached for 5 hour. Your limit will reset at 2026-10-06 16:23:45][20261006143041000000000000000000]"
	limitResult = `{"type":"result","subtype":"success","is_error":true,"api_error_status":429,"session_id":"c70e202b-0000-4000-8000-000000000000","result":"` + limitText + `"}` + "\n"
	limitTrail  = `{"type":"assistant","timestamp":"2026-10-06T06:29:58.000Z","message":{"model":"glm-5.3","content":[{"type":"text","text":"working"}]}}` + "\n" +
		`{"type":"assistant","timestamp":"2026-10-06T06:30:41.477Z","isApiErrorMessage":true,"error":"rate_limit","apiErrorStatus":429,"message":{"model":"<synthetic>","content":[{"type":"text","text":"` + limitText + `"}]}}` + "\n" +
		`{"type":"last-prompt","lastPrompt":"the spec"}` + "\n"
	oldSentinel = "rc=1\nfinished=2026-10-06T06:30:41Z\nharness=claude-code\nprovider=zai\nsession=c70e202b-0000-4000-8000-000000000000\n"
	wantLine    = "no report: rate-limited (429) at 15:30, plan resets at 17:23 (429 text); resume with --session c70e202b-0000-4000-8000-000000000000"
)

func pinKST(t *testing.T) {
	old := time.Local
	time.Local = time.FixedZone("KST", 9*60*60)
	t.Cleanup(func() { time.Local = old })
}

// incidentNow is a moment on the incident's local day, so the times print as
// hh:mm.
var incidentNow = time.Date(2026, 10, 6, 7, 0, 0, 0, time.UTC)

func writeLog(t *testing.T, log, sentinel string) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("OUTSOURCE_RUNS_DIR", filepath.Join(dir, "runs"))
	p := filepath.Join(dir, "glm.log")
	if err := os.WriteFile(p, []byte(log), 0o644); err != nil {
		t.Fatal(err)
	}
	if sentinel != "" {
		if err := os.WriteFile(p+".rc", []byte(sentinel), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

// From a new sentinel's fields. FAIL-first: without the quota_exhausted case
// quotaNoReport returns "" and last-report printed the API error line as the
// report (exit 0).
func TestRateLimitedLineFromSentinelFields(t *testing.T) {
	pinKST(t)
	p := writeLog(t, limitResult, oldSentinel+"quota_exhausted=1\nreset_at=2026-10-06T08:23:45Z\napi_error=429 [1308][…]\n")
	if got := quotaNoReport(p, incidentNow); got != wantLine {
		t.Fatalf("got  %q\nwant %q", got, wantLine)
	}
	// The sentinel decides: with quota_exhausted=0 the trail is not read and
	// the round is not called rate-limited.
	p0 := writeLog(t, limitResult, oldSentinel+"quota_exhausted=0\ntrail=/nonexistent/would-say-429.jsonl\n")
	if got := quotaNoReport(p0, incidentNow); got != "" {
		t.Fatalf("quota_exhausted=0 must decide; got %q", got)
	}
}

// From a sentinel written before this change: the detector reads the trail
// the sentinel names. FAIL-first: without the "" case's fallback the line is
// empty for the incident's own sentinel.
func TestRateLimitedLineFromOldSentinelTrail(t *testing.T) {
	pinKST(t)
	dir := t.TempDir()
	trail := filepath.Join(dir, "c70e202b.jsonl")
	if err := os.WriteFile(trail, []byte(limitTrail), 0o644); err != nil {
		t.Fatal(err)
	}
	p := writeLog(t, limitResult, oldSentinel+"trail="+trail+"\ndone_marker=absent (DONE-q38big)\n")
	if got := quotaNoReport(p, incidentNow); got != wantLine {
		t.Fatalf("got  %q\nwant %q", got, wantLine)
	}
}

// last-report itself: exit 65 and exactly one line, nothing on stdout — the
// API error line is not a report. FAIL-first: without the IsAPIErrorText check
// in Main the API error line is printed to stdout with exit 0.
func TestLastReportOnARateLimitedRound(t *testing.T) {
	pinKST(t)
	p := writeLog(t, limitResult, oldSentinel+"quota_exhausted=1\nreset_at=2026-10-06T08:23:45Z\n")
	var out, errb bytes.Buffer
	if rc := Main([]string{p}, &out, &errb); rc != ExitNoReport {
		t.Fatalf("rc = %d, want %d; stdout=%q", rc, ExitNoReport, out.String())
	}
	if out.Len() != 0 {
		t.Fatalf("stdout must be empty; got %q", out.String())
	}
	lines := strings.Split(strings.TrimRight(errb.String(), "\n"), "\n")
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "no report: rate-limited (429) at ") ||
		!strings.HasSuffix(lines[0], "; resume with --session c70e202b-0000-4000-8000-000000000000") {
		t.Fatalf("want one rate-limited line; got:\n%s", errb.String())
	}
}

// Any other API error alone is still not a report, and says what it was.
func TestAPIErrorOnlyIsNoReport(t *testing.T) {
	log := `{"type":"result","is_error":true,"result":"API Error: 500 {\"error\":\"internal\"}"}` + "\n"
	p := writeLog(t, log, "rc=1\nfinished=2026-10-06T06:30:41Z\nharness=claude-code\nquota_exhausted=0\n")
	var out, errb bytes.Buffer
	if rc := Main([]string{p}, &out, &errb); rc != ExitNoReport || out.Len() != 0 {
		t.Fatalf("rc=%d stdout=%q", rc, out.String())
	}
	if !strings.Contains(errb.String(), "its only result is an API error: API Error: 500") ||
		!strings.Contains(errb.String(), "no report: the round finished at") {
		t.Fatalf("stderr:\n%s", errb.String())
	}
}

// A round asleep under --resume-on-reset has no sentinel yet; the registry
// says when it wakes, and nobody is told to resume it by hand.
func TestWaitingRoundSaysWhenItResumes(t *testing.T) {
	pinKST(t)
	p := writeLog(t, limitResult, "")
	runsDir := os.Getenv("OUTSOURCE_RUNS_DIR")
	if err := os.MkdirAll(runsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	wake := time.Date(2026, 10, 6, 8, 24, 45, 0, time.UTC)
	rec := fmt.Sprintf("id=1-2\npid=%d\nlabel=q\nlog=%s\nstartedAt=1\nwaitingUntil=%d\nwaitingResetAt=2026-10-06T08:23:45Z\nwaitingResetSource=429-text\n", os.Getpid(), p, wake.Unix())
	if err := os.WriteFile(filepath.Join(runsDir, "1-2.run"), []byte(rec), 0o644); err != nil {
		t.Fatal(err)
	}
	got := quotaNoReport(p, incidentNow)
	if want := "no report yet: rate-limited (429); the round is waiting for the plan reset and resumes its own session by 17:24 at the latest (run 1-2)"; got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
}

// The plan quota API's reset, read at the death, is the one shown when the
// sentinel has it, and the line says so. FAIL-first: ignoring quota_reset_at
// prints "17:23 (429 text)".
func TestRateLimitedLinePrefersTheQuotaAPIReset(t *testing.T) {
	pinKST(t)
	p := writeLog(t, limitResult, oldSentinel+"quota_exhausted=1\nreset_at=2026-10-06T08:23:45Z\nquota_reset_at=2026-10-06T11:32:49Z\n")
	want := "no report: rate-limited (429) at 15:30, plan resets at 20:32 (plan); resume with --session c70e202b-0000-4000-8000-000000000000"
	if got := quotaNoReport(p, incidentNow); got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
}

// The generic no-report diagnosis prints the sentinel's finish time in local
// wall-clock time, like the rate-limited line, with the raw UTC value after
// it. FAIL-first: without the conversion it prints "at 2026-10-06T06:30:41Z".
func TestDiagnoseNoReportPrintsLocalTime(t *testing.T) {
	pinKST(t)
	old := reportNow
	reportNow = func() time.Time { return incidentNow }
	t.Cleanup(func() { reportNow = old })
	p := writeLog(t, `{"type":"assistant","message":{"content":[{"type":"text","text":"short ack"}]}}`+"\n",
		"rc=-1\nfinished=2026-10-06T06:30:41Z\nharness=crush\nwrapper_signal=TERM\n")
	var out, errb bytes.Buffer
	if rc := Main([]string{p}, &out, &errb); rc != ExitNoReport {
		t.Fatalf("rc = %d", rc)
	}
	if !strings.Contains(errb.String(), "no report: the round was killed (TERM) at 15:30 local, 2026-10-06T06:30:41Z (rc=-1)") {
		t.Fatalf("stderr:\n%s", errb.String())
	}
}
