package launch

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/midagedev/outsource/internal/audit"
)

// The trail seal: sentinelBody must hash the trail (and its subagents) into
// the sentinel after the harness has exited, and `outsource audit` must read
// that same seal back — ok, then mismatch after one byte changes, absent for
// a sentinel that predates it. The write side and the read side live in one
// place (audit.SealLines); these tests pin them to each other end to end.

func sealLine(body, prefix string) string {
	for _, l := range strings.Split(body, "\n") {
		if strings.HasPrefix(l, prefix) {
			return l
		}
	}
	return ""
}

func TestSentinelSealsExistingTrail(t *testing.T) {
	dir := t.TempDir()
	trail := filepath.Join(dir, "session.jsonl")
	os.WriteFile(trail, []byte("one\n"), 0o644)
	log := filepath.Join(dir, "round.log")
	os.WriteFile(log, []byte("report\nDONE-x\n"), 0o644)

	r, _, _ := claudeRound(log, trail, "DONE-x", false)
	r.toolCallsLine = "tool_calls=1"
	body := r.sentinelBody(0, "done_marker=found (DONE-x)\n", time.Now().UTC())
	os.WriteFile(log+".rc", []byte(body), 0o644)

	h := sha256.Sum256([]byte("one\n"))
	if got := sealLine(body, "trail_sha256="); got != "trail_sha256="+hex.EncodeToString(h[:]) {
		t.Fatalf("trail_sha256: %q", got)
	}
	if got := sealLine(body, "trail_bytes="); got != "trail_bytes=4" {
		t.Fatalf("trail_bytes: %q", got)
	}
	// Right after trail=, before tool_calls= — a reader grepping the sentinel
	// in order sees the hash beside the path it seals.
	if strings.Index(body, "trail=") > strings.Index(body, "trail_sha256=") ||
		strings.Index(body, "trail_sha256=") > strings.Index(body, "tool_calls=") {
		t.Fatalf("seal lines misplaced:\n%s", body)
	}
}

func TestSentinelSealUnreadableTrail(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "round.log")
	os.WriteFile(log, []byte("report\nDONE-x\n"), 0o644)
	r, _, _ := claudeRound(log, filepath.Join(dir, "gone.jsonl"), "DONE-x", false)
	body := r.sentinelBody(0, "", time.Now().UTC())
	got := sealLine(body, "trail_sha256=")
	if !strings.HasPrefix(got, "trail_sha256=unavailable (") {
		t.Fatalf("an unreadable trail must seal as unavailable, got %q", got)
	}
	// Never fails the round: the sentinel is still written, in full.
	if !strings.Contains(body, "harness=claude-code") || !strings.Contains(body, "model_requested=glm-5.3") {
		t.Fatalf("sentinel incomplete after seal failure:\n%s", body)
	}
}

func TestSentinelSealsSubagentsSorted(t *testing.T) {
	dir := t.TempDir()
	trail := filepath.Join(dir, "session.jsonl")
	os.WriteFile(trail, []byte("main\n"), 0o644)
	sub := filepath.Join(dir, "session", "subagents")
	os.MkdirAll(sub, 0o755)
	// Written in non-sorted order on purpose: the digest is over lines sorted
	// by basename, so creation order must not leak into it.
	os.WriteFile(filepath.Join(sub, "z-agent.jsonl"), []byte("Z\n"), 0o644)
	os.WriteFile(filepath.Join(sub, "a-agent.jsonl"), []byte("A\n"), 0o644)
	os.WriteFile(filepath.Join(sub, "a-agent.jsonl.meta.json"), []byte("{}"), 0o644) // not a transcript

	log := filepath.Join(dir, "round.log")
	os.WriteFile(log, []byte("report\nDONE-x\n"), 0o644)
	r, _, _ := claudeRound(log, trail, "DONE-x", false)
	body := r.sentinelBody(0, "", time.Now().UTC())

	ha := sha256.Sum256([]byte("A\n"))
	hz := sha256.Sum256([]byte("Z\n"))
	want := "a-agent.jsonl " + hex.EncodeToString(ha[:]) + "\n" +
		"z-agent.jsonl " + hex.EncodeToString(hz[:]) + "\n"
	w := sha256.Sum256([]byte(want))
	if got := sealLine(body, "subagents_sha256="); got != "subagents_sha256="+hex.EncodeToString(w[:]) {
		t.Fatalf("subagents_sha256:\n got %q\nwant subagents_sha256=%s", got, hex.EncodeToString(w[:]))
	}

	// A trail with no subagents directory gets no subagents line at all.
	bare := filepath.Join(dir, "bare.jsonl")
	os.WriteFile(bare, []byte("main\n"), 0o644)
	r2, _, _ := claudeRound(log, bare, "DONE-x", false)
	if l := sealLine(r2.sentinelBody(0, "", time.Now().UTC()), "subagents_sha256="); l != "" {
		t.Fatalf("no subagents on disk must mean no subagents_sha256 line, got %q", l)
	}
}

// Test 6's end to end: a sentinel this package wrote reads back ok through
// audit.Main, mismatch (exit 3) after one byte of the trail changes, and
// absent for a pre-seal sentinel.
func TestAuditReadsLauncherSeal(t *testing.T) {
	dir := t.TempDir()
	runsDir := filepath.Join(dir, "runs")
	os.MkdirAll(runsDir, 0o755)
	t.Setenv("OUTSOURCE_RUNS_DIR", runsDir)
	trail := filepath.Join(dir, "session.jsonl")
	os.WriteFile(trail, []byte("one\ntwo\n"), 0o644)
	log := filepath.Join(dir, "round.log")
	os.WriteFile(log, []byte("report\nDONE-x\n"), 0o644)

	r, _, _ := claudeRound(log, trail, "DONE-x", false)
	r.runID = "1790000100-700"
	r.toolCallsLine = "tool_calls=1"
	os.WriteFile(log+".rc", []byte(r.sentinelBody(0, "done_marker=found (DONE-x)\ndone_marker_scope=report\n", time.Now().UTC())), 0o644)

	rec := "id=1790000100-700\npid=999700\nlabel=seal-check\nprovider=zai\nharness=claude-code\n" +
		"model=glm-5.3\ncwd=" + dir + "\nlog=" + log + "\ntrail=" + trail +
		"\nstartedAt=1790000100\nrc=0\n"
	os.WriteFile(filepath.Join(runsDir, "1790000100-700.run"), []byte(rec), 0o644)

	run := func() int {
		var out, errb bytes.Buffer
		return audit.Main([]string{"1790000100-700"}, &out, &errb)
	}
	if rc := run(); rc != 0 {
		t.Fatal("fresh seal must read ok (exit 0)")
	}
	os.WriteFile(trail, []byte("one\ntwX\n"), 0o644)
	if rc := run(); rc != 3 {
		t.Fatalf("tampered trail must exit 3, got %d", rc)
	}
	// A pre-seal sentinel (no trail_sha256 line) reads absent and exits 0.
	os.WriteFile(log+".rc", []byte("rc=0\ntrail="+trail+"\ntool_calls=1\n"), 0o644)
	if rc := run(); rc != 0 {
		t.Fatalf("pre-seal sentinel must read absent (exit 0), got %d", rc)
	}
}
