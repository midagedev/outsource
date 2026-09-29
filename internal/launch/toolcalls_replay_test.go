package launch

import (
	"os"
	"strings"
	"testing"
)

// TestReplayRealTranscripts re-runs the counter on real session transcripts,
// outside fixtures. It exists for the 2026-09-29 incident: the gate was
// written because one real transcript held 23 lines and zero tool_use blocks
// under a report claiming "32 passed", and the number that justifies a gate
// should stay reproducible against the artifact that produced it.
//
// Opt-in, because the paths are machine-local:
//
//	OUTSOURCE_TOOLCOUNT_REPLAY=/path/a.jsonl,/path/b.jsonl go test -v \
//	    -run TestReplayRealTranscripts ./internal/launch/
//
// Each path is counted and logged with -v; a path may carry an expected count
// as `path=N`, which turns the replay into an assertion.
func TestReplayRealTranscripts(t *testing.T) {
	spec := os.Getenv("OUTSOURCE_TOOLCOUNT_REPLAY")
	if spec == "" {
		t.Skip("set OUTSOURCE_TOOLCOUNT_REPLAY=<transcript>[,<transcript>=N] to replay real transcripts")
	}
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		path, want := part, -1
		if eq := strings.Index(part, "="); eq >= 0 {
			path = strings.TrimSpace(part[:eq])
			n := 0
			for _, c := range strings.TrimSpace(part[eq+1:]) {
				if c < '0' || c > '9' {
					t.Fatalf("bad expectation in %q: want =N", part)
				}
				n = n*10 + int(c-'0')
			}
			want = n
		}
		got, bad, err := countToolUses(path)
		if err != nil {
			t.Errorf("%s: count failed: %v", path, err)
			continue
		}
		if want >= 0 && got != want {
			t.Errorf("%s: tool_use = %d, want %d (unparseable: %d)", path, got, want, bad)
			continue
		}
		t.Logf("%s: tool_use=%d unparseable=%d", path, got, bad)
	}
}
