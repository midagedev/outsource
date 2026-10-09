package launch

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/midagedev/outsource/internal/runs"
)

// The fake claude-code harness for the plan-limit tests: a shell script, not
// this test binary (TestMain refuses a re-exec). Each call is one attempt; the
// Nth line of $FAKE_CC_SCRIPT says how it ends — "429 <reset as z.ai writes
// it>" or "ok". It appends to the session transcript where analyzeRun looks
// ($CLAUDE_CONFIG_DIR/projects/<slug>/<sid>.jsonl), continues the same
// session on --resume, prints the one --output-format json object, and keeps
// its stdin and argv per attempt for the test to read. The 429 line is the
// 2026-10-06 incident's shape with the request id scrubbed.
const fakeClaudeScript = `#!/usr/bin/env bash
st="$FAKE_CC_STATE"
n=$(( $(cat "$st/count" 2>/dev/null || echo 0) + 1 ))
echo "$n" > "$st/count"
cat > "$st/stdin.$n"
printf '%s\n' "$*" > "$st/args.$n"
echo "fake claude stderr, attempt $n" >&2
if [ -e "$FAKE_CC_LOG.rc" ]; then echo "$n" >> "$st/sentinel-before-end"; fi
sid=11111111-2222-4333-8444-555555555555
while [ $# -gt 0 ]; do
  case "$1" in --resume) sid="$2"; shift 2 ;; *) shift ;; esac
done
dir="$CLAUDE_CONFIG_DIR/projects/-fake-cwd"
mkdir -p "$dir"
tr="$dir/$sid.jsonl"
printf '%s\n' '{"type":"user","message":{"role":"user","content":"input"}}' >> "$tr"
printf '%s\n' '{"type":"assistant","timestamp":"2026-10-06T06:29:58.000Z","message":{"model":"glm-5.3","content":[{"type":"tool_use","name":"Bash","input":{"command":"go test ./..."}}]}}' >> "$tr"
outcome="$(sed -n "${n}p" "$FAKE_CC_SCRIPT")"
case "$outcome" in
  429*)
    text="API Error: Request rejected (429) · [1308][Usage limit reached for 5 hour. Your limit will reset at ${outcome#429 }][2026100614304100000000000000000$n]"
    printf '{"type":"assistant","timestamp":"2026-10-06T06:30:41.477Z","isApiErrorMessage":true,"error":"rate_limit","apiErrorStatus":429,"message":{"model":"<synthetic>","content":[{"type":"text","text":"%s"}]}}\n' "$text" >> "$tr"
    printf '{"type":"last-prompt","lastPrompt":"input"}\n' >> "$tr"
    printf '{"type":"result","subtype":"success","is_error":true,"api_error_status":429,"session_id":"%s","result":"%s","usage":{"output_tokens":1}}\n' "$sid" "$text"
    exit 1 ;;
  *)
    printf '%s\n' '{"type":"assistant","timestamp":"2026-10-06T08:25:02.000Z","message":{"model":"glm-5.3","content":[{"type":"text","text":"Report: done."}]}}' >> "$tr"
    printf '{"type":"result","subtype":"success","session_id":"%s","result":"Report: done.\\n\\nDONE-QR","usage":{"output_tokens":1}}\n' "$sid"
    exit 0 ;;
esac
`

// No test in this package reads a real plan quota. The launch warning and
// the plan-limit path call readTightestWindow, whose real source is quota's
// network read; this makes every such call a failed read unless a test
// installs its own fake (and restores this one). Measured before it: the
// tests stayed offline only because credential.sh does not sit beside the
// test binary.
func init() {
	readTightestWindow = func(string) (planWindow, bool) { return planWindow{}, false }
}

// quotaRig is one fake-harness round: its files, and the injected clock,
// sleep and plan read. Nothing here sleeps for real or touches the network.
type quotaRig struct {
	t                         *testing.T
	cwd, spec, log, cfg, st   string
	bin                       string
	now                       time.Time
	sleeps                    int
	termAtSleep               int // the sleep that sends this process a real SIGTERM (1-based); 0 = never
	stopAtSleep               int // the sleep that records stopRequested in the run record; 0 = never
	statesSeen                []runs.State
	waitsSeen                 map[string]bool // "<source>|<reset>" of each wait the record showed
	sentinelSeenWhileSleeping bool
	longestSleep              time.Duration
	plan                      func(now time.Time) (planWindow, bool)
}

// harnessFreePath is the PATH every launch test starts from: system tools
// only. pinHarnessFreePath sets it, from TestMain, and refuses to run the
// package when any harness CLI is still reachable on it. Measured 2026-10-06:
// two tests here ran real CLIs from the caller's PATH — `claude -p --resume`
// against z.ai with a fake key (pid 1305), and `crush`, which rewrote the
// shared $TMPDIR/outsource-glm-cfg/crushrc. A test that wants a harness puts
// its own fake directory in front of this PATH; one whose fake goes missing
// then finds nothing, and fails closed.
const harnessFreePath = "/usr/bin:/bin:/usr/sbin:/sbin"

// harnessBins is every CLI a launch can spawn: the harness table's, plus
// grok-run's own.
func harnessBins() []string {
	bins := []string{"grok"}
	for _, h := range harnessTable {
		bins = append(bins, h.bin)
	}
	return bins
}

func pinHarnessFreePath() string {
	os.Setenv("PATH", harnessFreePath)
	for _, b := range harnessBins() {
		if p, err := exec.LookPath(b); err == nil {
			return fmt.Sprintf("launch tests: harness CLI %q is reachable at %s even on PATH=%s; a test that loses its fake would run it — refusing to run the package (exit 2)", b, p, harnessFreePath)
		}
	}
	return ""
}

// rigPath is the rig's whole PATH: the fake and the system tools it uses.
// With the caller's PATH behind the fake, a fake that went missing fell
// through to the real claude CLI — measured 2026-10-06, pid 1305 ran
// `claude -p --resume` against z.ai with the test's fake key until the lead
// stopped it.
func rigPath(bin string) string { return bin + ":/usr/bin:/bin" }

// realClaudeOnPath names a claude on PATH outside the fake's directory, or
// "". refuseRealClaude fails the test on one: the guard that keeps the
// incident above from repeating, whatever PATH the rig is given.
func realClaudeOnPath(fakeBin string) string {
	for _, d := range filepath.SplitList(os.Getenv("PATH")) {
		if d == fakeBin || d == "" {
			continue
		}
		if fi, err := os.Stat(filepath.Join(d, "claude")); err == nil && !fi.IsDir() {
			return filepath.Join(d, "claude")
		}
	}
	return ""
}

func refuseRealClaude(t *testing.T, fakeBin string) {
	t.Helper()
	if p := realClaudeOnPath(fakeBin); p != "" {
		t.Fatalf("the rig's PATH reaches a real claude at %s — a test that removes the fake would launch it; refusing to run", p)
	}
}

// isolateLaunch is for a test that calls OutsourceMain expecting a refusal or
// a missing harness: if the code under test regresses, the launch must still
// start nothing real. Measured 2026-10-06: with the --resume-on-reset refusal
// mutated out, such a test ran the real crush CLI on the caller's PATH and
// rewrote the shared $TMPDIR/outsource-glm-cfg/crushrc. So: a PATH with no
// harness on it, a private TMPDIR (the default config dir lives there), a
// private registry, and a key that is not one. Returns the test's dir.
func isolateLaunch(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty-bin")
	if err := os.MkdirAll(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", empty)
	t.Setenv("TMPDIR", dir)
	t.Setenv("OUTSOURCE_RUNS_DIR", filepath.Join(dir, "runs"))
	t.Setenv("ZAI_API_KEY", "test-key-not-a-real-credential")
	t.Setenv("XAI_API_KEY", "test-key-not-a-real-credential")
	return dir
}

func newQuotaRig(t *testing.T, outcomes ...string) *quotaRig {
	t.Helper()
	dir := t.TempDir()
	g := &quotaRig{t: t, cwd: filepath.Join(dir, "cwd"), spec: filepath.Join(dir, "spec.md"),
		log: filepath.Join(dir, "run.log"), cfg: filepath.Join(dir, "cfg"), st: filepath.Join(dir, "state"),
		bin: filepath.Join(dir, "bin"), now: time.Date(2026, 10, 6, 6, 31, 0, 0, time.UTC)}
	for _, d := range []string{g.cwd, g.st, g.bin} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(g.spec, []byte("do the thing\nDONE-QR\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "outcomes")
	if err := os.WriteFile(script, []byte(strings.Join(outcomes, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(g.bin, "claude"), []byte(fakeClaudeScript), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", rigPath(g.bin))
	refuseRealClaude(t, g.bin)
	t.Setenv("FAKE_CC_STATE", g.st)
	t.Setenv("FAKE_CC_SCRIPT", script)
	t.Setenv("FAKE_CC_LOG", g.log)
	t.Setenv("OUTSOURCE_RUNS_DIR", filepath.Join(dir, "runs"))
	t.Setenv("OUTSOURCE_PROVIDER", "zai")
	t.Setenv("OUTSOURCE_HARNESS", "")
	t.Setenv("GLM_DELEGATE_MODEL", "")
	t.Setenv("ZAI_API_KEY", "test-key-not-a-real-credential")
	t.Setenv("ZAI_BASE_URL", "")
	t.Setenv("ZAI_ANTHROPIC_BASE", "")
	pinLocal(t)

	oldNow, oldSleep, oldRead := quotaNow, quotaSleep, readTightestWindow
	t.Cleanup(func() { quotaNow, quotaSleep, readTightestWindow = oldNow, oldSleep, oldRead })
	quotaNow = func() time.Time { return g.now }
	quotaSleep = func(d time.Duration) {
		g.sleeps++
		if d > g.longestSleep {
			g.longestSleep = d
		}
		// The registry and the sentinel are sampled once a minute of the
		// injected clock, not read every tick: a two-hour wait is 7200 ticks.
		if g.sleeps%60 == 1 {
			if rec := runs.FindByLog(g.log); rec != nil {
				g.statesSeen = append(g.statesSeen, rec.State())
				if g.waitsSeen == nil {
					g.waitsSeen = map[string]bool{}
				}
				g.waitsSeen[rec.WaitingResetSource+"|"+rec.WaitingResetAt] = true
			}
			if _, err := os.Stat(g.log + ".rc"); err == nil {
				g.sentinelSeenWhileSleeping = true
			}
		}
		if g.sleeps == g.termAtSleep {
			// What `runs stop` does to a waiting wrapper: a real TERM, caught
			// by the round's signal hold (holdSignals). The test listens too,
			// only to know the signal was dispatched before the next tick;
			// the hold's goroutine then needs a moment to record it.
			seen := make(chan os.Signal, 1)
			signal.Notify(seen, syscall.SIGTERM)
			syscall.Kill(os.Getpid(), syscall.SIGTERM)
			select {
			case <-seen:
			case <-time.After(5 * time.Second):
				t.Errorf("the TERM sent to this process never arrived")
			}
			signal.Stop(seen)
			for i := 0; i < 100; i++ {
				runtime.Gosched()
			}
		}
		if g.sleeps == g.stopAtSleep {
			// The writer `runs stop` itself uses (track voice), so this test
			// covers the two tracks' seam: the request, then the wrapper's
			// reading of it, with no TERM yet.
			if rec := runs.FindByLog(g.log); rec != nil {
				if err := runs.RequestStop(rec.ID, "test-lead", "stopped while waiting", g.now); err != nil {
					t.Errorf("RequestStop: %v", err)
				}
			}
		}
		if g.sleeps > 2000000 {
			t.Fatalf("the wait loop did not end after %d sleeps", g.sleeps)
		}
		g.now = g.now.Add(d)
	}
	readTightestWindow = func(string) (planWindow, bool) {
		if g.plan != nil {
			return g.plan(g.now)
		}
		return planWindow{}, false // a failed read: the default for these rounds
	}
	return g
}

// pinLocal makes local time KST for the test, so "17:23" means what the
// incident's lead read. time.Local is fixed at start-up; TZ does not move it.
func pinLocal(t *testing.T) {
	old := time.Local
	time.Local = time.FixedZone("KST", 9*60*60)
	t.Cleanup(func() { time.Local = old })
}

func (g *quotaRig) launch(extra ...string) (rc int, stdout, stderr string) {
	var out, errb bytes.Buffer
	args := append([]string{"--foreground", "--cwd", g.cwd, "--spec", g.spec, "--log", g.log,
		"--config-dir", g.cfg, "--done-marker", "DONE-QR", "--label", "qrig"}, extra...)
	rc = OutsourceMain(args, &out, &errb)
	return rc, out.String(), errb.String()
}

func (g *quotaRig) attempts() int {
	b, _ := os.ReadFile(filepath.Join(g.st, "count"))
	n, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	return n
}

func (g *quotaRig) file(name string) string {
	b, _ := os.ReadFile(filepath.Join(g.st, name))
	return string(b)
}

func (g *quotaRig) sentinel() string {
	g.t.Helper()
	b, err := os.ReadFile(g.log + ".rc")
	if err != nil {
		g.t.Fatalf("no sentinel: %v", err)
	}
	return string(b)
}

func mustContain(t *testing.T, what, body string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(body, w) {
			t.Fatalf("%s is missing %q; got:\n%s", what, w, body)
		}
	}
}

func mustNotContain(t *testing.T, what, body string, nots ...string) {
	t.Helper()
	for _, w := range nots {
		if strings.Contains(body, w) {
			t.Fatalf("%s must not contain %q; got:\n%s", what, w, body)
		}
	}
}

// Item 1: a fake-harness round that dies of the plan limit leaves the three
// keys in its sentinel, and rc stays the harness's. FAIL-first: without the
// b.WriteString(r.quotaLines()) line in sentinelBody the sentinel holds
// rc=1 and none of the three keys.
func TestPlanLimitDeathWritesSentinelKeys(t *testing.T) {
	g := newQuotaRig(t, "429 2026-10-06 16:23:45")
	rc, out, errb := g.launch()
	if rc != 1 {
		t.Fatalf("rc = %d, want the harness's 1 unchanged; stderr:\n%s", rc, errb)
	}
	s := g.sentinel()
	mustContain(t, "sentinel", s,
		"rc=1\n",
		"quota_exhausted=1\n",
		"reset_at=2026-10-06T08:23:45Z\n",
		"api_error=429 [1308][Usage limit reached for 5 hour. Your limit will reset at 2026-10-06 16:23:45][20261006143041000000000000000001]\n",
		"session=11111111-2222-4333-8444-555555555555\n")
	// No flag, no resume bookkeeping.
	mustNotContain(t, "sentinel", s, "resumed_after_reset=", "reset_zone_suspect=")
	mustContain(t, "stdout", out, "SESSION 11111111-2222-4333-8444-555555555555")
	if g.attempts() != 1 || g.sleeps != 0 {
		t.Fatalf("without --resume-on-reset: attempts=%d sleeps=%d, want 1 and 0", g.attempts(), g.sleeps)
	}
}

// A round that finished normally says so: quota_exhausted=0 is a checked
// answer, not an absent one.
func TestNormalRoundRecordsNoPlanLimit(t *testing.T) {
	g := newQuotaRig(t, "ok")
	if rc, _, errb := g.launch(); rc != 0 {
		t.Fatalf("rc = %d; stderr:\n%s", rc, errb)
	}
	s := g.sentinel()
	mustContain(t, "sentinel", s, "quota_exhausted=0\n")
	mustNotContain(t, "sentinel", s, "reset_at=", "api_error=")
}

// Item 5, the whole path: the first attempt dies of the plan limit, the
// wrapper waits in state waiting with no sentinel on disk, resumes the same
// session with the exact resume prompt, and the second attempt reports. One
// sentinel, written at the end, with resumed_after_reset=1 and the checks over
// the whole trail. FAIL-first: with the table's run back on runClaudeCode
// (no resume loop) rc=1 and the sentinel reads quota_exhausted=1.
func TestResumeOnResetWaitsAndResumesTheSameSession(t *testing.T) {
	g := newQuotaRig(t, "429 2026-10-06 16:23:45", "ok")
	rc, out, errb := g.launch("--resume-on-reset")
	if rc != 0 {
		t.Fatalf("rc = %d, want 0 after one resume; stderr:\n%s", rc, errb)
	}
	if g.attempts() != 2 {
		t.Fatalf("attempts = %d, want 2", g.attempts())
	}
	// The wait ran to reset + 60 s on the injected clock, never on the real
	// one — every plan read failed, so the 429's reset was the bound — and it
	// ticked at most once a second, so a TERM ends it within a second.
	if want := time.Date(2026, 10, 6, 8, 24, 45, 0, time.UTC); !g.now.Equal(want) {
		t.Fatalf("woke at %s, want %s (reset 08:23:45Z + 60 s)", g.now.Format(time.RFC3339), want.Format(time.RFC3339))
	}
	if g.longestSleep > time.Second {
		t.Fatalf("a wait tick was %s; it must be at most 1 s", g.longestSleep)
	}
	if len(g.statesSeen) == 0 {
		t.Fatal("no sleep observed the registry")
	}
	for _, st := range g.statesSeen {
		if st != runs.Waiting {
			t.Fatalf("the record read %q during the wait, want waiting", st)
		}
	}
	if g.sentinelSeenWhileSleeping || g.file("sentinel-before-end") != "" {
		t.Fatal("a sentinel was on disk before the round ended — wait.sh would have stopped waiting")
	}
	if rec := runs.FindByLog(g.log); rec == nil || rec.State() != runs.Done {
		t.Fatalf("after the round the record should be done; got %+v", rec)
	}
	// The resume goes through --resume with the same session, and its input is
	// the prompt, verbatim, not the spec again.
	if a1 := g.file("args.1"); strings.Contains(a1, "--resume") {
		t.Fatalf("the first attempt must not resume: %s", a1)
	}
	mustContain(t, "second attempt's argv", g.file("args.2"), "--resume 11111111-2222-4333-8444-555555555555")
	want := "Your previous turn was cut by the provider's plan limit (HTTP 429) at 2026-10-06 15:30 KST. The limit has now reset.\n" +
		"Continue the same task from where you stopped. First re-check the working tree (git status, git diff --stat):\n" +
		"your last tool call may not have completed. Then carry on with the same spec, completion criteria and report\n" +
		"format.\n"
	if got := g.file("stdin.2"); got != want {
		t.Fatalf("resume prompt:\n%q\nwant:\n%q", got, want)
	}
	mustContain(t, "first attempt's stdin", g.file("stdin.1"), "do the thing")
	// <log>.err keeps both attempts' stderr: the resume appends.
	errLog, _ := os.ReadFile(g.log + ".err")
	mustContain(t, "<log>.err", string(errLog), "fake claude stderr, attempt 1\n",
		"--- outsource: resumed session 11111111-2222-4333-8444-555555555555 after the plan limit (resume 1) ---\n",
		"fake claude stderr, attempt 2\n")
	s := g.sentinel()
	mustContain(t, "sentinel", s,
		"rc=0\n", "resumed_after_reset=1\n", "quota_exhausted=0\n",
		"done_marker=found (DONE-QR)", "model_actual=glm-5.3\n", "tool_calls=2\n")
	mustNotContain(t, "sentinel", s, "wrapper_signal=", "reset_zone_suspect=")
	mustContain(t, "stdout", out, "SESSION 11111111-2222-4333-8444-555555555555")
	if strings.Count(out, "SESSION ") != 1 {
		t.Fatalf("one round, one SESSION line; got:\n%s", out)
	}
}

// TERM while waiting — what `runs stop` sends a waiting wrapper: the signal
// hold catches it, the wait ends on the next tick, and the wrapper writes the
// sentinel (quota fields and wrapper_signal) and exits; no child is running,
// so there is nothing to hold for. The TERM is real, sent to this process.
// FAIL-first: without the hold check in waitForPlan's tick the wait runs on
// to the reset (08:24:45Z) before anything notices the TERM.
func TestTermWhileWaitingWritesTheSentinelAndExits(t *testing.T) {
	g := newQuotaRig(t, "429 2026-10-06 16:23:45", "ok")
	g.termAtSleep = 3
	rc, _, errb := g.launch("--resume-on-reset")
	if rc != 1 {
		t.Fatalf("rc = %d, want the last attempt's 1; stderr:\n%s", rc, errb)
	}
	if g.attempts() != 1 {
		t.Fatalf("attempts = %d — the wrapper resumed after TERM", g.attempts())
	}
	mustContain(t, "sentinel", g.sentinel(),
		"rc=1\n", "quota_exhausted=1\n", "reset_at=2026-10-06T08:23:45Z\n",
		"resumed_after_reset=0\n", "wrapper_signal=TERM\n")
	mustContain(t, "stderr", errb, "not resuming — the wrapper received TERM while waiting")
	// It ended within ticks of the TERM, not at the deadline: the TERM went
	// out at 06:31:02Z on the injected clock.
	if limit := time.Date(2026, 10, 6, 6, 41, 0, 0, time.UTC); g.now.After(limit) {
		t.Fatalf("the wait ended at %s — it must end within a tick or so of the TERM (06:31:02Z)", g.now.Format(time.RFC3339))
	}
	if rec := runs.FindByLog(g.log); rec == nil || rec.State() != runs.Failed {
		t.Fatalf("after TERM the record should be finished (failed); got %+v", rec)
	}
}

// A third plan-limit death stops at two resumes. Each death names a new reset
// (the limit did reset, and the round spent it again), so each resume counts.
// FAIL-first: with maxResumes = 3 the fake's fourth line ("ok") is reached and
// rc is 0.
func TestThirdDeathStopsAtTwoResumes(t *testing.T) {
	g := newQuotaRig(t, "429 2026-10-06 16:23:45", "429 2026-10-06 21:00:00", "429 2026-10-07 01:00:00", "ok")
	rc, _, errb := g.launch("--resume-on-reset")
	if rc != 1 {
		t.Fatalf("rc = %d, want 1; stderr:\n%s", rc, errb)
	}
	if g.attempts() != 3 {
		t.Fatalf("attempts = %d, want 3 (one launch, two resumes)", g.attempts())
	}
	mustContain(t, "sentinel", g.sentinel(),
		"resumed_after_reset=2\n", "quota_exhausted=1\n", "reset_at=2026-10-06T17:00:00Z\n")
	mustContain(t, "stderr", errb, "not resuming — already resumed 2 times")
}

// A provider that keeps answering with the same reset text after the wrapper
// slept past it: the zone reading is suspect, the same-text re-429s do not
// count as resumes, and the loop still ends — at three re-waits. FAIL-first:
// without the re-wait cap the wrapper keeps resuming until the fake runs out
// of 429 lines and its ninth attempt reports: rc = 0, want 1.
func TestSameResetTextLoopEnds(t *testing.T) {
	same := "429 2026-10-06 16:23:45"
	g := newQuotaRig(t, same, same, same, same, same, same, same, same)
	rc, _, errb := g.launch("--resume-on-reset")
	if rc != 1 {
		t.Fatalf("rc = %d, want 1; stderr:\n%s", rc, errb)
	}
	// One normal wait plus three re-waits: five attempts in all.
	if g.attempts() != 5 {
		t.Fatalf("attempts = %d, want 5", g.attempts())
	}
	// Four resumes ran and none took, so none is counted.
	mustContain(t, "sentinel", g.sentinel(), "reset_zone_suspect=1\n", "resumed_after_reset=0\n", "quota_exhausted=1\n")
	mustContain(t, "stderr", errb, "the plan limit was still in force after 3 re-waits")
	// The record named what each wait was for: the text's reset first, then
	// the hour-long fallback with its reset unknown.
	for _, w := range []string{"429-text|2026-10-06T08:23:45Z", "fallback|unknown"} {
		if !g.waitsSeen[w] {
			t.Fatalf("no wait showed %q; saw %v", w, g.waitsSeen)
		}
	}
	// Each zone re-wait without a readable plan is an hour.
	if want := time.Date(2026, 10, 6, 11, 24, 45, 0, time.UTC); !g.now.Equal(want) {
		t.Fatalf("clock at %s, want %s", g.now.Format(time.RFC3339), want.Format(time.RFC3339))
	}
}

// A wrong zone must not burn a resume: at the deadline, a plan read that
// shows the tightest window still spent extends the wait to that window's
// reset and counts nothing. FAIL-first: with the deadline read skipped the
// wrapper resumes at 08:24:45Z.
func TestSpentWindowDefersTheResume(t *testing.T) {
	g := newQuotaRig(t, "429 2026-10-06 16:23:45", "ok")
	windowReset := time.Date(2026, 10, 6, 9, 0, 0, 0, time.UTC)
	g.plan = func(now time.Time) (planWindow, bool) {
		if now.Before(windowReset) {
			return planWindow{label: "5h", remaining: 0, text: "0.0", reset: windowReset}, true
		}
		return planWindow{label: "5h", remaining: 100, text: "100.0", reset: windowReset.Add(5 * time.Hour)}, true
	}
	if rc, _, errb := g.launch("--resume-on-reset"); rc != 0 {
		t.Fatalf("rc = %d; stderr:\n%s", rc, errb)
	}
	if want := windowReset.Add(resetGrace); !g.now.Equal(want) {
		t.Fatalf("resumed at %s, want %s (the spent window's reset + 60 s)", g.now.Format(time.RFC3339), want.Format(time.RFC3339))
	}
	mustContain(t, "sentinel", g.sentinel(), "resumed_after_reset=1\n", "rc=0\n")
}

// A reset further than 6 h ahead is not waited for: the round stays dead and
// its sentinel says when it could go on.
func TestResetBeyondTheHorizonIsNotWaitedFor(t *testing.T) {
	g := newQuotaRig(t, "429 2026-10-07 16:23:45", "ok")
	rc, _, errb := g.launch("--resume-on-reset")
	if rc != 1 || g.attempts() != 1 || g.sleeps != 0 {
		t.Fatalf("rc=%d attempts=%d sleeps=%d, want 1/1/0; stderr:\n%s", rc, g.attempts(), g.sleeps, errb)
	}
	mustContain(t, "sentinel", g.sentinel(), "reset_at=2026-10-07T08:23:45Z\n", "resumed_after_reset=0\n")
}

// --resume-on-reset on a harness that leaves no readable 429 death is a usage
// error naming the harness, before any round is registered.
func TestResumeOnResetRefusesOtherHarnesses(t *testing.T) {
	dir := isolateLaunch(t)
	spec := filepath.Join(dir, "spec.md")
	os.WriteFile(spec, []byte("x\n"), 0o644)
	var out, errb bytes.Buffer
	rc := OutsourceMain([]string{"--foreground", "--cwd", dir, "--spec", spec, "--log", filepath.Join(dir, "l"),
		"--config-dir", filepath.Join(dir, "cfg"), "--provider", "zai", "--harness", "crush", "--resume-on-reset"}, &out, &errb)
	if rc != ExitUsage {
		t.Fatalf("rc = %d, want %d", rc, ExitUsage)
	}
	mustContain(t, "stderr", errb.String(), "--resume-on-reset works on the claude-code harness only", "'crush'")
	if recs, _ := runs.List(); len(recs) != 0 {
		t.Fatalf("a refused launch registered %d rounds", len(recs))
	}
}

// The provider half of the --resume-on-reset pre-flight (2026-10-09). A
// provider whose 429 text carries no reset this launcher can read
// (planlimit.HasResetForm) would see every death as reset_at=unknown and
// never resume, so the flag is refused at launch, naming the provider and
// where it does work. The harness half is checked first and keeps its text.
// xai is asserted as the derivation gives it: planlimit has no reset form
// for xai, so it is refused the same way — a change from before, when xai on
// claude-code took the flag and then never resumed.
//
// FAIL-first: drop the HasResetForm check from resumeRefusal and the
// openrouter and xai rows come back "" (allowed).
func TestResumeRefusalNamesAProviderWithNoReadableReset(t *testing.T) {
	if msg := resumeRefusal("claude-code", "zai", true); msg != "" {
		t.Fatalf("claude-code+zai must be allowed, got: %s", msg)
	}
	for _, p := range []string{"openrouter", "xai"} {
		msg := resumeRefusal("claude-code", p, true)
		mustContain(t, "refusal for "+p, msg,
			"--resume-on-reset is refused for provider '"+p+"'",
			"carry no reset time this launcher can read",
			"it works for: zai")
	}
	// The harness reason wins, whatever the provider.
	for _, p := range []string{"zai", "openrouter"} {
		msg := resumeRefusal("crush", p, true)
		mustContain(t, "crush refusal for "+p, msg, "--resume-on-reset works on the claude-code harness only", "'crush'")
		mustNotContain(t, "crush refusal for "+p, msg, "refused for provider")
	}
	// Off is off.
	if msg := resumeRefusal("claude-code", "openrouter", false); msg != "" {
		t.Fatalf("without the flag nothing is refused, got: %s", msg)
	}
}

// End to end: refused before the registry records a round.
func TestResumeOnResetRefusesOpenrouterAtLaunch(t *testing.T) {
	dir, spec := isolateOpenrouterLaunch(t)
	rc, errs := launchOpenrouter(t, dir, spec, "--model", "nvidia/nemotron-3-ultra-550b-a55b:free", "--resume-on-reset")
	if rc != ExitUsage {
		t.Fatalf("rc = %d, want %d; stderr=%s", rc, ExitUsage, errs)
	}
	mustContain(t, "stderr", errs, "--resume-on-reset is refused for provider 'openrouter'")
	if recs, _ := runs.List(); len(recs) != 0 {
		t.Fatalf("a refused launch registered %d rounds", len(recs))
	}
}

// Item 4: the warning fires under 25 % left and is silent at 25 % and on a
// failed read. FAIL-first: with `<=` for `<` the 25.0 case prints.
func TestLaunchWarningThreshold(t *testing.T) {
	pinLocal(t)
	old := readTightestWindow
	t.Cleanup(func() { readTightestWindow = old })
	t.Setenv(detachedEnvKey, "")
	reset := time.Now().Add(2 * time.Hour)
	for _, c := range []struct {
		name string
		w    planWindow
		ok   bool
		want string
	}{
		{"24.9 left", planWindow{label: "5h", remaining: 24.9, text: "24.9", reset: reset}, true,
			"outsource: warning — zai 5h window 24.9% left (resets " + reset.In(time.Local).Format("15:04") + "); a long round may be cut by a 429 — --resume-on-reset waits and continues it.\n"},
		{"25.0 left", planWindow{label: "5h", remaining: 25, text: "25.0", reset: reset}, true, ""},
		{"read failed", planWindow{}, false, ""},
	} {
		readTightestWindow = func(string) (planWindow, bool) { return c.w, c.ok }
		var errb bytes.Buffer
		planWindowWarning("zai", &errb)
		if c.name == "24.9 left" && reset.In(time.Local).YearDay() != time.Now().In(time.Local).YearDay() {
			continue // the reset crossed local midnight; the clock form is pinned in human's test
		}
		if errb.String() != c.want {
			t.Fatalf("%s: got %q, want %q", c.name, errb.String(), c.want)
		}
	}
	// The --detach child stays silent: the parent already read and printed.
	readTightestWindow = func(string) (planWindow, bool) { return planWindow{label: "5h", remaining: 1, text: "1.0"}, true }
	t.Setenv(detachedEnvKey, "1")
	var errb bytes.Buffer
	planWindowWarning("zai", &errb)
	if errb.Len() != 0 {
		t.Fatalf("the detached child printed: %q", errb.String())
	}
}

// The warning is printed by the launch itself, before the --detach re-exec,
// and the launch goes on (here into a missing harness, exit 69, so no round
// starts). This is also how L3 runs with the built binary.
func TestLaunchPrintsTheWarningBeforeDetaching(t *testing.T) {
	old := readTightestWindow
	t.Cleanup(func() { readTightestWindow = old })
	readTightestWindow = func(string) (planWindow, bool) {
		return planWindow{label: "5h", remaining: 19.1, text: "19.1"}, true
	}
	dir := isolateLaunch(t)
	spec := filepath.Join(dir, "spec.md")
	os.WriteFile(spec, []byte("x\n"), 0o644)
	t.Setenv("OUTSOURCE_PROVIDER", "zai")
	t.Setenv("OUTSOURCE_HARNESS", "")
	var out, errb bytes.Buffer
	rc := OutsourceMain([]string{"--detach", "--cwd", dir, "--spec", spec, "--log", filepath.Join(dir, "l"),
		"--config-dir", filepath.Join(dir, "cfg")}, &out, &errb)
	if rc != ExitHarnessMissing {
		t.Fatalf("rc = %d, want %d; stderr:\n%s", rc, ExitHarnessMissing, errb.String())
	}
	mustContain(t, "stderr", errb.String(), "outsource: warning — zai 5h window 19.1% left (resets unknown)")
}

// The real plan read: quota's --json, the tightest window (not the first:
// measured 2026-08-16, the weekly window can be the tighter one), a 3 s
// budget. The fixture lists the looser window first. FAIL-first: taking
// Windows[0] reads the 1w window.
func TestReadTightestViaQuotaJSON(t *testing.T) {
	two := func(args []string, stdout, _ io.Writer) int {
		io.WriteString(stdout, `{"fetchedAt":"2026-10-06T07:59:52Z","provider":"zai","windows":[`+
			`{"label":"1w","remainingPercent":63.7,"nextResetTime":1791861812000},`+
			`{"label":"5h","remainingPercent":19.1,"nextResetTime":1791286369000}]}`+"\n")
		return 0
	}
	w, ok := readTightestVia(two, "zai", time.Second)
	if !ok || w.label != "5h" || w.text != "19.1" || w.reset.UTC().Format(time.RFC3339) != "2026-10-06T11:32:49Z" {
		t.Fatalf("got %+v ok=%v, want the 5h window at 19.1 resetting 11:32:49Z", w, ok)
	}
	failed := func([]string, io.Writer, io.Writer) int { return 2 }
	if _, ok := readTightestVia(failed, "zai", time.Second); ok {
		t.Fatal("a failed quota read must read as not ok")
	}
	block := make(chan struct{})
	defer close(block)
	slow := func([]string, io.Writer, io.Writer) int { <-block; return 0 }
	start := time.Now()
	if _, ok := readTightestVia(slow, "zai", 50*time.Millisecond); ok {
		t.Fatal("a slow read must read as not ok")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("the budget did not bound the read: %s", d)
	}
	// A provider quota does not cover answers at once, without a network call.
	if _, ok := readTightestVia(func(a []string, o, e io.Writer) int { return 64 }, "xai", time.Second); ok {
		t.Fatal("exit 64 (no plan quota for this provider) must read as not ok")
	}
}

// A resume that cannot start (here the CLI vanished during the wait) still
// owes the sentinel: earlier attempts ran. FAIL-first: without the un-bail in
// runClaudeCodeResuming, finish treats the round as never dispatched and
// writes no <log>.rc — wait.sh would wait forever.
func TestResumeThatCannotStartStillWritesTheSentinel(t *testing.T) {
	g := newQuotaRig(t, "429 2026-10-06 16:23:45", "ok")
	inner := quotaSleep
	quotaSleep = func(d time.Duration) {
		os.Remove(filepath.Join(g.bin, "claude"))
		inner(d)
	}
	rc, _, errb := g.launch("--resume-on-reset")
	if rc != ExitHarnessMissing {
		t.Fatalf("rc = %d, want %d; stderr:\n%s", rc, ExitHarnessMissing, errb)
	}
	mustContain(t, "sentinel", g.sentinel(), "rc=69\n", "quota_exhausted=1\n", "resumed_after_reset=1\n")
}

// The plan takes requests again before the 429 text's reset (measured
// 2026-10-06: q6khead answered at 06:33:39Z, 1h51m before the text's
// 08:23:45Z). The wait reads the plan every 5 min and resumes at the first
// read that shows the window no longer spent. FAIL-first: without the
// periodic read the wrapper waits to 08:24:45Z.
func TestEarlyResumeWhenThePlanTakesRequestsAgain(t *testing.T) {
	g := newQuotaRig(t, "429 2026-10-06 16:23:45", "ok")
	opens := time.Date(2026, 10, 6, 6, 44, 0, 0, time.UTC)
	g.plan = func(now time.Time) (planWindow, bool) {
		if now.Before(opens) {
			return planWindow{label: "5h", remaining: 0, text: "0.0", reset: opens}, true
		}
		return planWindow{label: "5h", remaining: 99.8, text: "99.8", reset: opens.Add(5 * time.Hour)}, true
	}
	rc, _, errb := g.launch("--resume-on-reset")
	if rc != 0 {
		t.Fatalf("rc = %d; stderr:\n%s", rc, errb)
	}
	// Reads at 06:36, 06:41 (spent), 06:46 (open): resumed at 06:46:00Z.
	if want := time.Date(2026, 10, 6, 6, 46, 0, 0, time.UTC); !g.now.Equal(want) {
		t.Fatalf("resumed at %s, want %s", g.now.Format(time.RFC3339), want.Format(time.RFC3339))
	}
	mustContain(t, "stderr", errb, "has 99.8% left — resuming before the 429's reset")
	mustContain(t, "sentinel", g.sentinel(), "resumed_after_reset=1\n", "rc=0\n")
}

// An early resume that meets the same limit again (the plan read said open,
// the provider did not) is not counted as a resume, and it spends the re-wait
// budget, so the loop still ends. FAIL-first: counting only re-429s past the
// parsed reset, the wrapper keeps resuming every 5 min until the fake runs
// out of 429 lines and its ninth attempt reports (rc = 0).
func TestEarlyResumeIntoTheSameLimitIsBounded(t *testing.T) {
	same := "429 2026-10-06 16:23:45"
	g := newQuotaRig(t, same, same, same, same, same, same, same, same)
	g.plan = func(time.Time) (planWindow, bool) {
		return planWindow{label: "5h", remaining: 100, text: "100.0", reset: time.Date(2026, 10, 6, 11, 0, 0, 0, time.UTC)}, true
	}
	rc, _, errb := g.launch("--resume-on-reset")
	if rc != 1 || g.attempts() != 5 {
		t.Fatalf("rc=%d attempts=%d, want 1 and 5; stderr:\n%s", rc, g.attempts(), errb)
	}
	mustContain(t, "sentinel", g.sentinel(), "resumed_after_reset=0\n", "quota_exhausted=1\n")
	mustContain(t, "stderr", errb, "the plan limit was still in force after 3 re-waits")
}

// The death-time plan read lands in the sentinel as quota_reset_at, beside the
// text's reset_at, for runs and last-report to prefer. A read whose reset is
// not after the death is stale and is not written. FAIL-first: without the
// read in detectQuota the sentinel has no quota_reset_at.
func TestDeathTimeQuotaReadIsRecorded(t *testing.T) {
	g := newQuotaRig(t, "429 2026-10-06 16:23:45")
	g.plan = func(time.Time) (planWindow, bool) {
		return planWindow{label: "5h", remaining: 0, text: "0.0", reset: time.Date(2026, 10, 6, 11, 32, 49, 0, time.UTC)}, true
	}
	if rc, _, errb := g.launch(); rc != 1 {
		t.Fatalf("rc = %d; stderr:\n%s", rc, errb)
	}
	mustContain(t, "sentinel", g.sentinel(), "reset_at=2026-10-06T08:23:45Z\nquota_reset_at=2026-10-06T11:32:49Z\napi_error=429 ")

	stale := newQuotaRig(t, "429 2026-10-06 16:23:45")
	stale.plan = func(time.Time) (planWindow, bool) {
		return planWindow{label: "5h", remaining: 0, text: "0.0", reset: time.Date(2026, 10, 6, 6, 0, 0, 0, time.UTC)}, true
	}
	stale.launch()
	mustNotContain(t, "sentinel", stale.sentinel(), "quota_reset_at=")
}

// `runs stop` records its request before it sends TERM. A wrapper that wakes
// in that gap reads the request and does not respawn: it writes the sentinel
// as for a TERM. FAIL-first: without the stopRequested check the wrapper
// resumes (attempts = 2, rc = 0).
func TestStopRequestedBeforeRespawn(t *testing.T) {
	g := newQuotaRig(t, "429 2026-10-06 16:23:45", "ok")
	g.stopAtSleep = 5
	rc, _, errb := g.launch("--resume-on-reset")
	if rc != 1 || g.attempts() != 1 {
		t.Fatalf("rc=%d attempts=%d, want 1 and 1; stderr:\n%s", rc, g.attempts(), errb)
	}
	mustContain(t, "stderr", errb, "not resuming — a stop was requested for this run (runs stop)")
	// The seam with `runs stop`: the wrapper's finish reads the same request
	// and names the lead in the sentinel (lead integration, 2026-10-06).
	mustContain(t, "sentinel", g.sentinel(), "rc=1\n", "quota_exhausted=1\n", "resumed_after_reset=0\n",
		"stopped_by=lead\n", "stop_reason=stopped while waiting\n")
}

// The rig's own guard: a claude anywhere on PATH but the fake's directory is
// found. FAIL-first: with realClaudeOnPath returning "" this reads no claude.
func TestRigGuardFindsARealClaudeOnPath(t *testing.T) {
	fake, other := t.TempDir(), t.TempDir()
	for _, d := range []string{fake, other} {
		if err := os.WriteFile(filepath.Join(d, "claude"), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", fake+":"+other)
	if got := realClaudeOnPath(fake); got != filepath.Join(other, "claude") {
		t.Fatalf("realClaudeOnPath = %q, want %s", got, filepath.Join(other, "claude"))
	}
	t.Setenv("PATH", rigPath(fake))
	if got := realClaudeOnPath(fake); got != "" {
		t.Fatalf("the rig PATH reaches %s", got)
	}
}

// When the parsed reset has proved wrong and the plan quota is readable, the
// wait is for the plan's own reset, and the record says so — that reset and
// "quota-api", never the 429 text's reset under the plan's name.
// FAIL-first: passing the text's reset_at into that wait shows
// "quota-api|2026-10-06T08:23:45Z".
func TestZoneSuspectWaitNamesThePlanReset(t *testing.T) {
	same := "429 2026-10-06 16:23:45"
	g := newQuotaRig(t, same, same, "ok")
	planReset := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	// Unreadable until the second attempt has died, so the first wait runs to
	// the text's reset and the only read naming 12:00Z is the zone path's.
	g.plan = func(now time.Time) (planWindow, bool) {
		if g.attempts() < 2 {
			return planWindow{}, false
		}
		return planWindow{label: "5h", remaining: 0, text: "0.0", reset: planReset}, true
	}
	g.launch("--resume-on-reset")
	if !g.waitsSeen["quota-api|2026-10-06T12:00:00Z"] || g.waitsSeen["quota-api|2026-10-06T08:23:45Z"] {
		t.Fatalf("the zone wait must name the plan's reset; saw %v", g.waitsSeen)
	}
	mustContain(t, "sentinel", g.sentinel(), "reset_zone_suspect=1\n")
}

// --require-quota is read once per launch: in the parent, before the --detach
// re-exec. The detached child, which the parent already gated, does not read
// it again. FAIL-first: without the detached-child check the child reads too
// (reads = 2).
func TestDetachedChildSkipsTheSecondQuotaGate(t *testing.T) {
	dir := isolateLaunch(t)
	spec := filepath.Join(dir, "spec.md")
	os.WriteFile(spec, []byte("x\n"), 0o644)
	t.Setenv("OUTSOURCE_PROVIDER", "zai")
	t.Setenv("OUTSOURCE_HARNESS", "")
	reads := 0
	old := requireQuotaRead
	t.Cleanup(func() { requireQuotaRead = old })
	requireQuotaRead = func([]string, io.Writer, io.Writer) int { reads++; return 0 }
	args := []string{"--cwd", dir, "--spec", spec, "--log", filepath.Join(dir, "l"),
		"--config-dir", filepath.Join(dir, "cfg"), "--require-quota", "10"}
	// The parent: gated, then refused at the missing harness (no re-exec).
	if rc := OutsourceMain(append([]string{"--detach"}, args...), io.Discard, io.Discard); rc != ExitHarnessMissing {
		t.Fatalf("parent rc = %d, want %d", rc, ExitHarnessMissing)
	}
	// The child, as the re-exec starts it: no --detach, OUTSOURCE_DETACHED=1.
	t.Setenv(detachedEnvKey, "1")
	if rc := OutsourceMain(args, io.Discard, io.Discard); rc != ExitHarnessMissing {
		t.Fatalf("child rc = %d, want %d", rc, ExitHarnessMissing)
	}
	if reads != 1 {
		t.Fatalf("--require-quota was read %d times, want 1 (the parent only)", reads)
	}
}

// --max-seconds next to --resume-on-reset is said to bound each attempt.
// FAIL-first: without the note the launch says nothing about it.
func TestMaxSecondsIsSaidToBoundEachAttempt(t *testing.T) {
	dir := isolateLaunch(t)
	spec := filepath.Join(dir, "spec.md")
	os.WriteFile(spec, []byte("x\n"), 0o644)
	t.Setenv("OUTSOURCE_PROVIDER", "zai")
	t.Setenv("OUTSOURCE_HARNESS", "")
	var errb bytes.Buffer
	OutsourceMain([]string{"--detach", "--cwd", dir, "--spec", spec, "--log", filepath.Join(dir, "l"),
		"--config-dir", filepath.Join(dir, "cfg"), "--max-seconds", "3600", "--resume-on-reset"}, io.Discard, &errb)
	mustContain(t, "stderr", errb.String(),
		"outsource: note — --max-seconds 3600 bounds each attempt, not the launch: with --resume-on-reset a launch runs up to 3 attempts")
	errb.Reset()
	OutsourceMain([]string{"--detach", "--cwd", dir, "--spec", spec, "--log", filepath.Join(dir, "l"),
		"--config-dir", filepath.Join(dir, "cfg"), "--max-seconds", "3600"}, io.Discard, &errb)
	mustNotContain(t, "stderr", errb.String(), "bounds each attempt")
}

// Every launch test starts with no harness CLI on PATH (TestMain pins it).
// FAIL-first: without the pin in TestMain, the developer's PATH reaches
// claude, crush, opencode and the rest.
func TestNoHarnessCLIIsReachable(t *testing.T) {
	if got := os.Getenv("PATH"); got != harnessFreePath {
		t.Fatalf("PATH = %q, want the pinned %q", got, harnessFreePath)
	}
	for _, b := range harnessBins() {
		if p, err := exec.LookPath(b); err == nil {
			t.Fatalf("harness CLI %q is reachable at %s", b, p)
		}
	}
}

// A resumed attempt is the same launch continuing the same session, so it
// keeps the lead token its first prompt named: a second token in the record
// would make round_send prefix one the round never saw (lead integration of
// tracks voice and quota, 2026-10-06). FAIL-first: minting on every attempt
// leaves two leadToken lines and a record token that differs from the first.
func TestResumeKeepsTheLaunchLeadToken(t *testing.T) {
	g := newQuotaRig(t, "429 2026-10-06 16:23:45", "ok")
	rc, _, errb := g.launch("--resume-on-reset")
	if rc != 0 || g.attempts() != 2 {
		t.Fatalf("rc=%d attempts=%d, want 0 and 2; stderr:\n%s", rc, g.attempts(), errb)
	}
	rec := runs.FindByLog(g.log)
	if rec == nil {
		t.Fatal("no run record")
	}
	b, err := os.ReadFile(filepath.Join(os.Getenv("OUTSOURCE_RUNS_DIR"), rec.ID+".run"))
	if err != nil {
		t.Fatal(err)
	}
	var tokens []string
	for _, line := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(line, "leadToken="); ok {
			tokens = append(tokens, v)
		}
	}
	if len(tokens) != 1 || tokens[0] == "" || rec.LeadToken != tokens[0] {
		t.Fatalf("leadToken lines = %q, record token %q; want exactly one, kept across the resume", tokens, rec.LeadToken)
	}
}
