package tail

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/midagedev/outsource/internal/runs"
)

// A --resume-on-reset round sleeping through a plan limit is waiting, not
// over: follow mode stays on it and shows the resumed turns, which land in the
// same trail. FAIL-first: with the follow loop's check back on `!= Running` it
// ends at "── round …: waiting" and the resumed turn is never shown.
func TestFollowStaysWithAWaitingRound(t *testing.T) {
	old := pollInterval
	pollInterval = 5 * time.Millisecond
	defer func() { pollInterval = old }()

	dir := t.TempDir()
	trail := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(trail, []byte(transcriptFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	id := startRun(t, dir, "--trail-format", FormatClaudeTranscript)
	if err := runs.SetTrail(id, trail); err != nil {
		t.Fatal(err)
	}
	step := 0
	followProbe = func() {
		step++
		switch step {
		case 1:
			runs.SetWaiting(id, time.Now().Add(time.Hour), "2026-10-06T08:23:45Z", "429-text")
		case 2:
			fh, err := os.OpenFile(trail, os.O_APPEND|os.O_WRONLY, 0o644)
			if err == nil {
				fh.WriteString(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"the resumed turn"}]}}` + "\n")
				fh.Close()
			}
			runs.ClearWaiting(id)
		case 3:
			runs.Main([]string{"finish", id, "--rc", "0"}, os.Stdout, os.Stderr)
		}
	}
	defer func() { followProbe = nil }()

	done := make(chan int, 1)
	var stdout, stderr bytes.Buffer
	go func() { done <- Main([]string{id, "-f"}, strings.NewReader(""), &stdout, &stderr) }()
	select {
	case rc := <-done:
		if rc != 0 {
			t.Fatalf("rc=%d; stderr=%s", rc, stderr.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("follow did not end with the round")
	}
	out := stdout.String()
	if !strings.Contains(out, "the resumed turn") || !strings.Contains(out, "done rc=0") {
		t.Fatalf("follow must stay through the wait and end with the round:\n%s", out)
	}
}
