package runs

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/midagedev/outsource/internal/human"
)

// quotaFixture writes one record (and, for a finished round, its sentinel)
// into a fresh registry, and returns the log path.
func quotaFixture(t *testing.T, record, sentinel string) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("OUTSOURCE_RUNS_DIR", dir)
	log := filepath.Join(dir, "q38big.log")
	if err := os.WriteFile(filepath.Join(dir, "1791267124-4242.run"),
		[]byte("id=1791267124-4242\nlabel=q38big\nprovider=zai\nharness=claude-code\nlog="+log+"\n"+record), 0o644); err != nil {
		t.Fatal(err)
	}
	if sentinel != "" {
		if err := os.WriteFile(log+".rc", []byte(sentinel), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return log
}

func views(t *testing.T) (line, list string, rows []map[string]any) {
	t.Helper()
	var a, b, c bytes.Buffer
	if rc := Main([]string{"line"}, &a, &bytes.Buffer{}); rc != 0 {
		t.Fatalf("runs line rc=%d", rc)
	}
	if rc := Main([]string{"list"}, &b, &bytes.Buffer{}); rc != 0 {
		t.Fatalf("runs list rc=%d", rc)
	}
	if rc := Main([]string{"json"}, &c, &bytes.Buffer{}); rc != 0 {
		t.Fatalf("runs json rc=%d", rc)
	}
	if err := json.Unmarshal(c.Bytes(), &rows); err != nil || len(rows) != 1 {
		t.Fatalf("runs json: %v %s", err, c.String())
	}
	return a.String(), b.String(), rows
}

// A round its plan limit killed reads ⛔quota and the local reset time in
// every view, not a bare rc=1; json carries quotaExhausted and resetAt. The
// fields come from the sentinel. FAIL-first: with the sentinel read disabled
// in quotaOf the line reads "❌q38big rc=1".
func TestPlanLimitDeathRendersInEveryView(t *testing.T) {
	now := time.Now()
	reset := now.Add(53 * time.Minute).UTC().Truncate(time.Second)
	quotaFixture(t,
		fmt.Sprintf("pid=1\nstartedAt=%d\nrc=1\nfinishedAt=%d\nsession=s-1\n", now.Unix()-1200, now.Unix()-60),
		"rc=1\nsession=s-1\nquota_exhausted=1\nreset_at="+reset.Format(time.RFC3339)+"\napi_error=429 [1308][…]\n")
	clock := human.Clock(reset, now)
	line, list, rows := views(t)
	if want := "q38big ⛔quota resets " + clock + " (429 text)"; !strings.Contains(line, want) || strings.Contains(line, "rc=1") {
		t.Fatalf("line = %q, want %q and no bare rc", line, want)
	}
	if want := "⛔quota resets " + clock + " (429 text)  rc=1"; !strings.Contains(list, want) {
		t.Fatalf("list is missing %q:\n%s", want, list)
	}
	if rows[0]["quotaExhausted"] != true || rows[0]["resetAt"] != reset.Format(time.RFC3339) ||
		rows[0]["resetSource"] != "429-text" || rows[0]["state"] != "failed" {
		t.Fatalf("json row: %v", rows[0])
	}
}

// When the launcher could read the plan quota at the death, its reset is the
// one shown, and every view says so: on 2026-10-06 the 429 text's reset came
// 1h51m after the plan took requests again. FAIL-first: ignoring
// quota_reset_at, the line shows the text's reset with "(429 text)".
func TestQuotaAPIResetIsPreferred(t *testing.T) {
	now := time.Now()
	text := now.Add(2 * time.Hour).UTC().Truncate(time.Second)
	api := now.Add(10 * time.Minute).UTC().Truncate(time.Second)
	quotaFixture(t,
		fmt.Sprintf("pid=1\nstartedAt=%d\nrc=1\nfinishedAt=%d\n", now.Unix()-1200, now.Unix()-60),
		"rc=1\nquota_exhausted=1\nreset_at="+text.Format(time.RFC3339)+"\nquota_reset_at="+api.Format(time.RFC3339)+"\n")
	clock := human.Clock(api, now)
	line, list, rows := views(t)
	if want := "q38big ⛔quota resets " + clock + " (plan)"; !strings.Contains(line, want) {
		t.Fatalf("line = %q, want %q", line, want)
	}
	if want := "⛔quota resets " + clock + " (plan)"; !strings.Contains(list, want) {
		t.Fatalf("list is missing %q:\n%s", want, list)
	}
	if rows[0]["resetAt"] != api.Format(time.RFC3339) || rows[0]["resetSource"] != "quota-api" {
		t.Fatalf("json row: %v", rows[0])
	}
}

// A reset the 429 text did not carry renders as unknown, never a time.
func TestPlanLimitDeathWithUnknownReset(t *testing.T) {
	now := time.Now()
	quotaFixture(t, fmt.Sprintf("pid=1\nstartedAt=%d\nrc=1\nfinishedAt=%d\n", now.Unix()-600, now.Unix()-60),
		"rc=1\nquota_exhausted=1\nreset_at=unknown\n")
	line, _, rows := views(t)
	if !strings.Contains(line, "q38big ⛔quota resets unknown") || rows[0]["resetAt"] != "unknown" {
		t.Fatalf("line=%q row=%v", line, rows[0])
	}
}

// A sentinel written by a LATER round on the same --log path is not this
// record's: the sessions differ, so the row stays a plain failure.
func TestAnotherRoundsSentinelIsIgnored(t *testing.T) {
	now := time.Now()
	quotaFixture(t, fmt.Sprintf("pid=1\nstartedAt=%d\nrc=1\nfinishedAt=%d\nsession=s-old\n", now.Unix()-600, now.Unix()-60),
		"rc=1\nsession=s-new\nquota_exhausted=1\nreset_at=2026-10-06T08:23:45Z\n")
	line, _, rows := views(t)
	if !strings.Contains(line, "❌q38big rc=1") || rows[0]["quotaExhausted"] != false {
		t.Fatalf("line=%q row=%v", line, rows[0])
	}
}

// --resume-on-reset asleep: state waiting, ⏸quota and the wake time in every
// view, a live item on the line (it counts in 🛠), and running again once
// cleared. FAIL-first: without the Waiting branch in State() the record reads
// running and the line shows ▶.
func TestWaitingRoundRendersInEveryView(t *testing.T) {
	now := time.Now()
	until := now.Add(time.Hour).Truncate(time.Second)
	log := quotaFixture(t, fmt.Sprintf("pid=%d\nstartedAt=%d\n", os.Getpid(), now.Unix()-1200), "")
	if err := SetWaiting("1791267124-4242", until, "2026-10-06T08:23:45Z", ResetFrom429Text); err != nil {
		t.Fatal(err)
	}
	clock := human.Clock(until, now)
	line, list, rows := views(t)
	if want := "🛠1 q38big ⏸quota → " + clock; !strings.Contains(line, want) {
		t.Fatalf("line = %q, want %q", line, want)
	}
	if !strings.Contains(list, "waiting ") || !strings.Contains(list, "⏸quota → "+clock) {
		t.Fatalf("list:\n%s", list)
	}
	r := rows[0]
	if r["state"] != "waiting" || r["quotaExhausted"] != true || r["resetAt"] != "2026-10-06T08:23:45Z" ||
		r["resetSource"] != "429-text" || r["waitingUntil"] != float64(until.Unix()) {
		t.Fatalf("json row: %v", r)
	}
	// The resume clears it: running again, no quota mark.
	if err := ClearWaiting("1791267124-4242"); err != nil {
		t.Fatal(err)
	}
	if rec := FindByLog(log); rec == nil || rec.State() != Running {
		t.Fatalf("after ClearWaiting: %+v", rec)
	}
	line, _, rows = views(t)
	if !strings.Contains(line, "▶q38big") || rows[0]["waitingUntil"] != nil {
		t.Fatalf("line=%q row=%v", line, rows[0])
	}
}

// A waiting wrapper is live: dismiss refuses it (its final append would land
// in a deleted file), and Resolve picks it as the one live round.
func TestWaitingRoundIsLive(t *testing.T) {
	now := time.Now()
	quotaFixture(t, fmt.Sprintf("pid=%d\nstartedAt=%d\nwaitingUntil=%d\n", os.Getpid(), now.Unix()-60, now.Unix()+600), "")
	var out, errb bytes.Buffer
	if rc := Main([]string{"dismiss", "1791267124-4242"}, &out, &errb); rc != ExitRefused {
		t.Fatalf("dismiss rc = %d, want %d; %s", rc, ExitRefused, errb.String())
	}
	if !strings.Contains(errb.String(), "is waiting") {
		t.Fatalf("refusal: %s", errb.String())
	}
	if r, err := Resolve("q38big"); err != nil || r.ID != "1791267124-4242" {
		t.Fatalf("Resolve: %v %v", r, err)
	}
	if !Waiting.Live() || !Running.Live() || Failed.Live() || Orphan.Live() || Done.Live() {
		t.Fatal("Live is running or waiting, nothing else")
	}
}

// The help names both live states dismiss refuses. FAIL-first: the old text
// says only "66 refused (running)".
func TestUsageNamesTheWaitingRefusal(t *testing.T) {
	var out bytes.Buffer
	Main([]string{"-h"}, &out, &bytes.Buffer{})
	if !strings.Contains(out.String(), "66 refused (running or waiting)") {
		t.Fatalf("usage:\n%s", out.String())
	}
}
