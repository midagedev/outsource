package runs

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/midagedev/outsource/internal/human"
)

// A round cut by a plan limit (HTTP 429) is shown as what it is, not as a
// bare rc=1: measured 2026-10-06, a round killed by z.ai's 5-hour cap read
// "❌q38big rc=1" in every view while its sentinel already knew more.
//
//	⛔quota resets 20:32 (plan)   finished: the last attempt died of the plan limit
//	⏸quota → 17:24               waiting: --resume-on-reset wakes by then at the latest
//
// A finished row prefers the plan quota API's reset (quota_reset_at) to the
// 429 text's (reset_at), because on 2026-10-06 the text's reset came 1h51m
// after the plan took requests again; it says which one it shows.
//
// These read the run record and the <log>.rc sentinel's key=value lines only.
// Never the trail: `runs line` is on the status line's hot path, and a trail
// is megabytes.

// Where a reset time came from, as `runs json` names it. Fallback is only
// ever a wait's: the 429 text's reset proved wrong and the plan quota could
// not be read, so the wrapper waits an hour and its reset is "unknown".
const (
	ResetFromQuotaAPI = "quota-api"
	ResetFrom429Text  = "429-text"
	ResetFromFallback = "fallback"
)

// SetWaiting marks a live round as asleep until `until` at the latest, for
// the plan reset at resetAt (RFC3339 or "unknown") taken from source.
// Appended, like every field after start. A registry that cannot be written
// must not break the round, so callers ignore the error the way finishRun
// does.
func SetWaiting(id string, until time.Time, resetAt, source string) error {
	return appendFields(id, "waitingUntil", strconv.FormatInt(until.Unix(), 10),
		"waitingResetAt", resetAt, "waitingResetSource", source)
}

// ClearWaiting marks the round running again (an empty value is the last
// assignment, and Read takes the last).
func ClearWaiting(id string) error {
	return appendFields(id, "waitingUntil", "")
}

// quotaInfo is what a row knows about a plan-limit death.
type quotaInfo struct {
	exhausted    bool
	resetAt      string // RFC3339, "unknown", or "" when not exhausted
	source       string // ResetFromQuotaAPI, ResetFrom429Text, or the wait's own
	waitingUntil int64  // 0 unless the round is waiting
}

// quotaOf reads the plan-limit facts for one row: the record's own fields
// while the wrapper waits, the sentinel's once the round has finished.
//
// The sentinel lives at <log>.rc, and a later round launched with the same
// --log path replaces it. When both the record and the sentinel name a
// session and they differ, the sentinel is another round's and is ignored.
func quotaOf(r *Record, st State) quotaInfo {
	switch st {
	case Waiting:
		return quotaInfo{exhausted: true, resetAt: r.WaitingResetAt, source: r.WaitingResetSource,
			waitingUntil: atoi(r.WaitingUntil)}
	case Failed:
	default:
		return quotaInfo{}
	}
	if r.Log == "" {
		return quotaInfo{}
	}
	kv := sentinelFields(r.Log+".rc", "quota_exhausted", "reset_at", "quota_reset_at", "session")
	if kv["quota_exhausted"] != "1" {
		return quotaInfo{}
	}
	if s := kv["session"]; s != "" && r.Session != "" && s != r.Session {
		return quotaInfo{}
	}
	if t := kv["quota_reset_at"]; t != "" {
		return quotaInfo{exhausted: true, resetAt: t, source: ResetFromQuotaAPI}
	}
	reset := kv["reset_at"]
	if reset == "" {
		reset = "unknown"
	}
	return quotaInfo{exhausted: true, resetAt: reset, source: ResetFrom429Text}
}

// sentinelFields reads the named keys from a sentinel file; a missing file is
// an empty answer. Last assignment wins, as in a record.
func sentinelFields(path string, keys ...string) map[string]string {
	out := map[string]string{}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()
	want := map[string]bool{}
	for _, k := range keys {
		want[k] = true
	}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if k, v, ok := strings.Cut(sc.Text(), "="); ok && want[k] {
			out[k] = v
		}
	}
	return out
}

// mark is what every view prints for the row. A finished one reads
// "⛔quota resets 20:32 (plan)" when the plan quota API gave the reset and
// "(429 text)" when only the error text did; a waiting one reads
// "⏸quota → 17:24", the latest it wakes. The one-line view puts it after the
// label ("q38big ⛔quota resets 20:32 (plan)"), so the token reads the same in
// every view.
func (q quotaInfo) mark(st State, now int64) string {
	if st == Waiting {
		until := "unknown"
		if q.waitingUntil > 0 {
			until = human.Clock(time.Unix(q.waitingUntil, 0), time.Unix(now, 0))
		}
		return "⏸quota → " + until
	}
	t, err := time.Parse(time.RFC3339, q.resetAt)
	if err != nil {
		return "⛔quota resets unknown"
	}
	from := "429 text"
	if q.source == ResetFromQuotaAPI {
		from = "plan"
	}
	return fmt.Sprintf("⛔quota resets %s (%s)", human.Clock(t, time.Unix(now, 0)), from)
}

func waitingUntilJSON(q quotaInfo) *int64 {
	if q.waitingUntil == 0 {
		return nil
	}
	v := q.waitingUntil
	return &v
}
