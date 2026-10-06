package launch

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/midagedev/outsource/internal/human"
	"github.com/midagedev/outsource/internal/planlimit"
	"github.com/midagedev/outsource/internal/quota"
	"github.com/midagedev/outsource/internal/runs"
	"github.com/midagedev/outsource/internal/telemetry"
)

// A round cut by its provider's plan limit (HTTP 429). Measured 2026-10-06: a
// GLM round on z.ai's coding plan died mid-task on the 5-hour cap; its
// sentinel said rc=1 and nothing else, last-report printed the API error line
// as the report, `runs` showed a bare rc=1, and the lead resumed it — and a
// second one — by hand with a written resume spec and --session. This file is
// the launcher's half of the fix:
//
//   - after a claude-code round exits, the detector (internal/planlimit) reads
//     its trail; on a plan-limit death the plan quota is read once and the
//     sentinel records quota_exhausted, reset_at, quota_reset_at, api_error;
//   - before a round starts, a nearly spent plan window is said out loud;
//   - --resume-on-reset waits until the plan takes requests again and resumes
//     the same session, through the same spawn site as the first attempt.

const (
	// maxResumes is the most resumes one launch spends. A round that dies of
	// the plan limit three times is a round that does not fit the plan.
	maxResumes = 2
	// maxResetHorizon is how far ahead a reset may be for the wrapper to wait
	// for it. Past that the round stays dead and the sentinel says when it
	// could go on: a weekly window spent for days is a lead's decision.
	maxResetHorizon = 6 * time.Hour
	// resetGrace is slept past the reset, so the first resumed request does not
	// race the provider's own clock.
	resetGrace = 60 * time.Second
	// maxRewaits bounds every wait that is not a counted resume: a re-429 with
	// the reset text already met (the limit had not reset), a reset already in
	// the past (the zone reading is suspect), and a plan window still spent at
	// the deadline. One budget, so the loop has one bound.
	maxRewaits = 3
	// zoneFallbackWait is the re-wait when the reset text has proved wrong and
	// the plan quota cannot be read either.
	zoneFallbackWait = 60 * time.Minute
	// waitTick is the longest single sleep. A TERM is caught by the signal
	// hold (`runs stop` sends one to a waiting wrapper), and the wait ends on
	// the next tick. Checking the wall clock every tick also keeps a closed
	// lid from stretching a wait: Go timers run on the monotonic clock, which
	// stops while a Mac sleeps.
	waitTick = time.Second
	// planReadEvery is how often a wait reads the plan quota. The 429 text's
	// reset is only the upper bound (see planlimit's resetForms: on
	// 2026-10-06 the plan took requests again 1h51m before it).
	planReadEvery = 5 * time.Minute
	// planReadBudget is the most a plan-quota read may delay a launch or a
	// resume. A slow read is a failed read, and a failed read is silent.
	planReadBudget = 3 * time.Second
	// warnBelowPercent: under this much left in the tightest window, the
	// launch says so (and launches anyway — --require-quota is the refusal).
	warnBelowPercent = 25.0
	// spentBelowPercent is "still spent": a resume into less than one percent
	// dies again within a turn or two. A choice, not a measurement. The one
	// spent window read on 2026-10-06 showed 0.0 left.
	spentBelowPercent = 1.0
)

// quotaState is what the plan-limit path knows about this round. It lives on
// the round so the sentinel is still written from one place (finish).
type quotaState struct {
	// checked is true once the detector read the final attempt's trail; only
	// then does the sentinel carry quota_exhausted (0 or 1).
	checked bool
	death   planlimit.Death
	// planReset is the plan quota API's reset for its tightest window, read
	// once when the attempt died; zero when unread or not after the death.
	planReset time.Time
	// resumed counts the resumes that took — the sentinel's
	// resumed_after_reset and the number maxResumes bounds. A resume met by a
	// re-429 with the same reset text found the limit not yet reset, so it is
	// taken back off; the re-wait budget bounds those instead.
	resumed     int
	zoneSuspect bool
	// resumePrompt replaces the spec on the harness's stdin for a resumed
	// attempt (runClaudeCode reads it).
	resumePrompt string
}

// requireQuotaRead is the --require-quota gate's call into quota, a variable
// so a test can count the reads (the --detach child must not repeat it).
var requireQuotaRead = quota.Main

// The clock and the sleep of --resume-on-reset. Variables so tests inject
// both and no test sleeps for real.
var (
	quotaNow   = time.Now
	quotaSleep = time.Sleep
)

// resumeRefusal is the --resume-on-reset pre-flight: the harness table says
// which harness leaves a plan-limit death this launcher can read and resume.
func resumeRefusal(harnessName string, on bool) string {
	if !on {
		return ""
	}
	if h, ok := findHarness(harnessName); ok && h.resumeOnReset {
		return ""
	}
	var where []string
	for _, h := range harnessTable {
		if h.resumeOnReset {
			where = append(where, h.name)
		}
	}
	return fmt.Sprintf("outsource: --resume-on-reset works on the %s harness only — harness '%s' leaves no plan-limit death this launcher can read and resume; drop the flag",
		strings.Join(where, ", "), harnessName)
}

// maxSecondsPerAttemptNote says, at launch, what --max-seconds means next to
// --resume-on-reset: the watchdog starts with each spawn (runChild), so it
// bounds each attempt, not the launch — up to 1 + maxResumes attempts, plus
// the waits between them, which no watchdog counts. Said where the caller can
// still read it, before any --detach re-exec.
func maxSecondsPerAttemptNote(o opts, stderr io.Writer) {
	if !o.resumeOnReset || o.maxSeconds == "" || os.Getenv(detachedEnvKey) == "1" {
		return
	}
	fmt.Fprintf(stderr, "outsource: note — --max-seconds %s bounds each attempt, not the launch: with --resume-on-reset a launch runs up to %d attempts, and the waits between them are not counted.\n",
		o.maxSeconds, 1+maxResumes)
}

// runClaudeCodeResuming is the claude-code harness's dispatch: one attempt,
// the plan-limit verdict on its trail, and — under --resume-on-reset — the
// wait-and-resume loop. Every attempt is runClaudeCode, so every spawn goes
// through the one spawn site (runChild); there is no second path.
//
// The sentinel is not written here: finish writes it once, after the last
// attempt, with that attempt's fields and the identity, done-marker and
// tool-call checks over the whole trail (one session, one file). Until then
// there is no <log>.rc, so `outsource wait` keeps waiting.
func (r *round) runClaudeCodeResuming() int {
	rc := r.runClaudeCode()
	if r.bailed {
		return rc
	}
	r.detectQuota()
	if !r.o.resumeOnReset {
		return rc
	}
	lastReset := ""
	rewaits := 0
	for {
		d := r.quota.death
		if !r.quota.checked || !d.Died || r.timedOut {
			return rc
		}
		stop := func(why string) int {
			fmt.Fprintf(r.stderr, "outsource: --resume-on-reset: not resuming — %s\n", why)
			telemetry.Note("why", "resume-on-reset stopped")
			return rc
		}
		if d.ResetAt.IsZero() {
			return stop("the 429 text gave no reset time this launcher can read (reset_at=unknown)")
		}
		if r.sid == "" {
			return stop("no session id in the log to resume")
		}
		if s := r.hold.name(); s != "" {
			return stop("the wrapper received " + s + " while the round ran")
		}
		now := quotaNow()
		sameText := lastReset != "" && d.ResetText == lastReset
		if sameText {
			// The same limit, not reset yet: the resume that met it did not
			// take, so it does not count.
			r.quota.resumed--
		}
		pastReset := !now.Before(d.ResetAt)
		if sameText || pastReset {
			if rewaits >= maxRewaits {
				return stop(fmt.Sprintf("the plan limit was still in force after %d re-waits", maxRewaits))
			}
			rewaits++
		}
		// What the wait is for, as the registry shows it: the reset, and
		// where that time came from.
		deadline, waitReset, source := d.ResetAt.Add(resetGrace), d.ResetAtField(), runs.ResetFrom429Text
		if pastReset {
			// Still limited after the parsed reset: the zone is inferred from
			// the request id, never stated, so trust the plan's own clock, or
			// an hour.
			r.quota.zoneSuspect = true
			deadline, waitReset, source = now.Add(zoneFallbackWait), planlimit.Unknown, runs.ResetFromFallback
			if w, ok := readTightestWindow(r.p.name); ok && w.reset.After(now) {
				deadline, waitReset, source = w.reset.Add(resetGrace), w.reset.UTC().Format(time.RFC3339), runs.ResetFromQuotaAPI
			}
		}
		if deadline.Sub(now) > maxResetHorizon+resetGrace {
			return stop(fmt.Sprintf("the reset is more than %s ahead (%s)", maxResetHorizon, human.Clock(deadline, now)))
		}
		if r.quota.resumed >= maxResumes {
			return stop(fmt.Sprintf("already resumed %d times — the limit per launch", maxResumes))
		}
		fmt.Fprintf(r.stderr, "outsource: --resume-on-reset: plan limit (429) — waiting until %s at the latest (reading the plan quota every %s), then resuming session %s (resume %d of %d)\n",
			human.Clock(deadline, now), planReadEvery, r.sid, r.quota.resumed+1, maxResumes)
		if why, done := r.waitForPlan(deadline, waitReset, source, &rewaits); done {
			return stop(why)
		}
		// `runs stop` records its request before it sends TERM; a wrapper that
		// woke in between must not respawn the harness.
		if stopRequested(r.runID) {
			return stop("a stop was requested for this run (runs stop)")
		}
		if s := r.hold.name(); s != "" {
			return stop("the wrapper received " + s + " while waiting")
		}
		lastReset = d.ResetText
		at := d.At
		if at.IsZero() {
			at = now
		}
		r.quota.resumed++
		r.quota.resumePrompt = resumePrompt(at)
		r.o.session = r.sid
		if r.runID != "" {
			_ = runs.ClearWaiting(r.runID)
		}
		rc = r.runClaudeCode()
		if r.bailed {
			// The resume could not start (the CLI or the credential went
			// missing during the wait). Earlier attempts did run, so the
			// sentinel is still owed: it keeps their plan-limit fields and
			// takes this exit code.
			r.bailed = false
			return rc
		}
		r.detectQuota()
	}
}

// openErrLog opens <log>.err for one claude-code attempt: created fresh for
// the first, appended to for a resumed one behind a separator line, so the
// stderr of the attempts before a plan-limit death survives the resume.
func (r *round) openErrLog() (*os.File, error) {
	if r.quota.resumePrompt == "" {
		return os.Create(r.o.log + ".err")
	}
	f, err := os.OpenFile(r.o.log+".err", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err == nil {
		fmt.Fprintf(f, "--- outsource: resumed session %s after the plan limit (resume %d) ---\n", r.o.session, r.quota.resumed)
	}
	return f, err
}

// resumePrompt is the resumed attempt's whole input, verbatim from the spec of
// this feature; at is when the 429 happened.
func resumePrompt(at time.Time) string {
	return "Your previous turn was cut by the provider's plan limit (HTTP 429) at " +
		at.In(time.Local).Format("2006-01-02 15:04 MST") + ". The limit has now reset.\n" +
		"Continue the same task from where you stopped. First re-check the working tree (git status, git diff --stat):\n" +
		"your last tool call may not have completed. Then carry on with the same spec, completion criteria and report\n" +
		"format.\n"
}

// detectQuota runs the detector over the attempt's trail: the transcript
// analyzeRun located, else the one the SessionStart hook revealed to the
// registry. No trail, or an unreadable one, leaves checked false and the
// sentinel without quota_exhausted — unknown is not zero. On a death it also
// reads the plan quota once, because the 429 text's reset is not always when
// the plan takes requests again.
func (r *round) detectQuota() {
	r.quota.checked, r.quota.death, r.quota.planReset = false, planlimit.Death{}, time.Time{}
	trail := r.trail
	if trail == "" && r.runID != "" {
		if rec := runs.FindByID(r.runID); rec != nil {
			trail = rec.Trail
		}
	}
	if trail == "" {
		return
	}
	d, err := planlimit.Detect(trail, r.p.name)
	if err != nil {
		return
	}
	r.quota.checked, r.quota.death = true, d
	if !d.Died {
		return
	}
	at := d.At
	if at.IsZero() {
		at = quotaNow()
	}
	// Only a reset after the death says when the limit ends; an older one is
	// a stale read, and recording it would mislead every reader.
	if w, ok := readTightestWindow(r.p.name); ok && w.reset.After(at) {
		r.quota.planReset = w.reset
	}
}

// waitForPlan sleeps until the plan takes requests again, with the registry
// record in state waiting. It ticks at most once a second and ends at once
// when the signal hold has caught a TERM, INT or HUP (`runs stop` sends TERM).
// Every planReadEvery it reads the plan quota and ends early when the tightest
// window is no longer spent. deadline — the 429 text's reset + 60 s — is the
// upper bound when the reads fail. At the deadline one more read decides: a
// window still spent extends the wait to that window's reset, inside the
// horizon and the re-wait budget. stop is true when the round must not be
// resumed, and why says why.
func (r *round) waitForPlan(deadline time.Time, resetAt, source string, rewaits *int) (why string, stop bool) {
	if r.runID != "" {
		_ = runs.SetWaiting(r.runID, deadline, resetAt, source)
	}
	nextRead := quotaNow().Add(planReadEvery)
	for {
		if s := r.hold.name(); s != "" {
			return "the wrapper received " + s + " while waiting", true
		}
		now := quotaNow()
		if !now.Before(deadline) {
			w, ok := readTightestWindow(r.p.name)
			now = quotaNow()
			if !ok || w.remaining >= spentBelowPercent || !w.reset.After(now) {
				// Not readable, not spent, or a reset already behind us: the
				// attempt itself is the judge, and a re-429 with the same text
				// is not counted as a resume.
				return "", false
			}
			target := w.reset.Add(resetGrace)
			if target.Sub(now) > maxResetHorizon+resetGrace {
				return fmt.Sprintf("the plan's %s window is still spent and resets more than %s ahead (%s)", w.label, maxResetHorizon, human.Clock(w.reset, now)), true
			}
			if *rewaits >= maxRewaits {
				return fmt.Sprintf("the plan's %s window was still spent after %d re-waits", w.label, maxRewaits), true
			}
			*rewaits++
			fmt.Fprintf(r.stderr, "outsource: --resume-on-reset: the plan's %s window is still spent (%s%% left); waiting until %s at the latest\n",
				w.label, w.text, human.Clock(target, now))
			deadline = target
			if r.runID != "" {
				_ = runs.SetWaiting(r.runID, deadline, w.reset.UTC().Format(time.RFC3339), runs.ResetFromQuotaAPI)
			}
			nextRead = now.Add(planReadEvery)
			continue
		}
		if !now.Before(nextRead) {
			if w, ok := readTightestWindow(r.p.name); ok && w.remaining >= spentBelowPercent {
				fmt.Fprintf(r.stderr, "outsource: --resume-on-reset: the plan's %s window has %s%% left — resuming before the 429's reset\n", w.label, w.text)
				return "", false
			}
			now = quotaNow()
			nextRead = now.Add(planReadEvery)
		}
		left := deadline.Sub(now)
		if left > waitTick {
			left = waitTick
		}
		quotaSleep(left)
	}
}

// stopRequested reports whether `runs stop` (track voice) has asked this run
// to end. It records the request in the run record before it sends TERM, so a
// wrapper waking in that gap reads it here and does not respawn. The key is
// read raw from the record file because the field is the other track's; at
// merge this becomes runs.Record.StopRequested.
func stopRequested(runID string) bool {
	if runID == "" {
		return false
	}
	rec := runs.FindByID(runID)
	return rec != nil && rec.StopRequested != ""
}

// quotaLines renders the plan-limit fields for the sentinel.
func (r *round) quotaLines() string {
	var b strings.Builder
	if r.quota.checked {
		if d := r.quota.death; d.Died {
			fmt.Fprintf(&b, "%s=1\n%s=%s\n", planlimit.KeyExhausted, planlimit.KeyResetAt, d.ResetAtField())
			if !r.quota.planReset.IsZero() {
				fmt.Fprintf(&b, "%s=%s\n", planlimit.KeyQuotaResetAt, r.quota.planReset.UTC().Format(time.RFC3339))
			}
			fmt.Fprintf(&b, "%s=%s\n", planlimit.KeyAPIError, d.APIErrorField())
		} else {
			fmt.Fprintf(&b, "%s=0\n", planlimit.KeyExhausted)
		}
	}
	if r.quota.zoneSuspect {
		fmt.Fprintf(&b, "%s=1\n", planlimit.KeyZoneSuspect)
	}
	if r.o.resumeOnReset {
		fmt.Fprintf(&b, "%s=%d\n", planlimit.KeyResumed, r.quota.resumed)
	}
	return b.String()
}

// ---- the plan-quota read ---------------------------------------------------

// planWindow is the tightest window of one plan-quota read.
type planWindow struct {
	label     string
	remaining float64
	text      string // remaining percent as the quota tool printed it ("19.1")
	reset     time.Time
}

// readTightestWindow is the plan read behind the launch warning, the
// death-time quota_reset_at and the waits: quota's own --json output, the way
// --require-quota calls it, under planReadBudget. A variable so tests use a
// fake source. ok is false for every way a read can fail — a provider with no
// plan quota (quota exits 64 at once, no network), no credential, an endpoint
// error, a slow answer.
var readTightestWindow = func(provider string) (planWindow, bool) {
	return readTightestVia(quota.Main, provider, planReadBudget)
}

func readTightestVia(main func([]string, io.Writer, io.Writer) int, provider string, budget time.Duration) (planWindow, bool) {
	type answer struct {
		rc  int
		out []byte
	}
	ch := make(chan answer, 1)
	go func() {
		// The buffer is the goroutine's own: after a timeout nobody reads it,
		// and the read finishes (or times out at its own 30 s) on its own.
		var buf bytes.Buffer
		rc := main([]string{"--provider", provider, "--json"}, &buf, io.Discard)
		ch <- answer{rc, buf.Bytes()}
	}()
	timer := time.NewTimer(budget)
	defer timer.Stop()
	var a answer
	select {
	case a = <-ch:
	case <-timer.C:
		return planWindow{}, false
	}
	if a.rc != quota.ExitOK {
		return planWindow{}, false
	}
	var rep struct {
		Windows []struct {
			Label            string      `json:"label"`
			RemainingPercent json.Number `json:"remainingPercent"`
			NextResetTime    int64       `json:"nextResetTime"`
		} `json:"windows"`
	}
	if json.Unmarshal(a.out, &rep) != nil || len(rep.Windows) == 0 {
		return planWindow{}, false
	}
	var best planWindow
	for i, w := range rep.Windows {
		f, err := w.RemainingPercent.Float64()
		if err != nil {
			return planWindow{}, false
		}
		if i == 0 || f < best.remaining {
			best = planWindow{label: w.Label, remaining: f, text: w.RemainingPercent.String()}
			if w.NextResetTime > 0 {
				best.reset = time.UnixMilli(w.NextResetTime)
			}
		}
	}
	return best, true
}

// planWindowWarning is the launch's one line when the plan is nearly spent.
// Measured 2026-10-06: a round launched into a nearly spent 5-hour window was
// cut by a 429 mid-task, and the launcher had read nothing — the quota is read
// only under --require-quota, which refuses rather than warns. This never
// refuses and never prints on a failed or slow read. The --detach child skips
// it: the parent already read and printed, and the child's stderr goes nowhere.
func planWindowWarning(provider string, stderr io.Writer) {
	if os.Getenv(detachedEnvKey) == "1" {
		return
	}
	w, ok := readTightestWindow(provider)
	if !ok || w.remaining >= warnBelowPercent {
		return
	}
	resets := planlimit.Unknown
	if !w.reset.IsZero() {
		resets = human.Clock(w.reset, quotaNow())
	}
	fmt.Fprintf(stderr, "outsource: warning — %s %s window %s%% left (resets %s); a long round may be cut by a 429 — --resume-on-reset waits and continues it.\n",
		provider, w.label, w.text, resets)
}
