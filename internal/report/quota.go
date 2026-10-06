package report

import (
	"fmt"
	"os"
	"time"

	"github.com/midagedev/outsource/internal/human"
	"github.com/midagedev/outsource/internal/planlimit"
	"github.com/midagedev/outsource/internal/runs"
)

// reportNow is last-report's clock: which day "today" is when a time is
// printed as local hh:mm. A variable so a test pins it.
var reportNow = time.Now

// quotaNoReport is last-report's one-line answer for a round with no report
// because its provider's plan limit cut it (HTTP 429), or "" when that is not
// why. The question it answers is the lead's next one — when can this round go
// on, and how — so the line carries the reset time and the session to resume.
//
// The sentinel's fields decide. A sentinel written before the launcher
// recorded quota_exhausted has no such key; for one of those, the detector
// reads the trail the sentinel names, so a round that died before this change
// answers the same way. A round still asleep under --resume-on-reset has no
// sentinel yet; the registry says when it wakes.
//
// The reset shown is the plan quota API's (quota_reset_at) when the launcher
// could read it at the death, else the 429 text's (reset_at), and the line
// says which: on 2026-10-06 the text's reset came 1h51m after the plan took
// requests again.
func quotaNoReport(logPath string, now time.Time) string {
	b, err := os.ReadFile(logPath + ".rc")
	if err != nil {
		if rec := runs.FindByLog(logPath); rec != nil && rec.State() == runs.Waiting {
			return waitingLine(rec, now)
		}
		return ""
	}
	kv := parseSentinel(string(b))
	resetAt, from := kv[planlimit.KeyResetAt], "429 text"
	if t := kv[planlimit.KeyQuotaResetAt]; t != "" {
		resetAt, from = t, "plan"
	}
	switch kv[planlimit.KeyExhausted] {
	case "1":
	case "":
		// The detector reads a claude-code transcript; no other harness's
		// trail has that shape (and none of them records a reset time).
		if kv["trail"] == "" || kv["harness"] != "claude-code" {
			return ""
		}
		d, err := planlimit.Detect(kv["trail"], kv["provider"])
		if err != nil || !d.Died {
			return ""
		}
		resetAt, from = d.ResetAtField(), "429 text"
	default:
		return ""
	}
	finished := kv["finished"]
	if t, err := time.Parse(time.RFC3339, finished); err == nil {
		finished = human.Clock(t, now)
	}
	reset := planlimit.Unknown
	if t, ok := planlimit.ParseResetAt(resetAt); ok {
		reset = human.Clock(t, now) + " (" + from + ")"
	}
	sid := kv["session"]
	if sid == "" {
		sid = "<unknown: no session in " + logPath + ".rc>"
	}
	return fmt.Sprintf("no report: rate-limited (429) at %s, plan resets at %s; resume with --session %s",
		finished, reset, sid)
}

// waitingLine is the answer while --resume-on-reset sleeps: there is no report
// yet, and nobody needs to resume anything — the wrapper does.
func waitingLine(rec *runs.Record, now time.Time) string {
	wake := "an unknown time"
	var n int64
	if _, err := fmt.Sscan(rec.WaitingUntil, &n); err == nil && n > 0 {
		wake = human.Clock(time.Unix(n, 0), now)
	}
	return fmt.Sprintf("no report yet: rate-limited (429); the round is waiting for the plan reset and resumes its own session by %s at the latest (run %s)", wake, rec.ID)
}
