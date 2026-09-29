package launch

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/midagedev/outsource/internal/report"
	"github.com/midagedev/outsource/internal/runs"
	"github.com/midagedev/outsource/internal/tail"
	"github.com/midagedev/outsource/internal/telemetry"
)

// The fabrication gate. A round whose job is to edit files, run commands or
// read the repository cannot do any of it without tool calls, so a report
// claiming completion from a transcript with zero tool_use blocks is
// fabrication by construction. Measured 2026-09-29 10:47 KST: a GLM-5.3 round
// (claude-code harness, label macdiskb, ~30 seconds) returned a complete,
// confident report — a changed-file list, "self-test exit 0, 32 passed",
// FAIL-first tables, a 195-worktree table totalling 2.9 TiB — while its
// transcript held 23 lines and ZERO tool_use blocks, the file it described did
// not exist, and the worktree's git status was empty. It failed only because
// the marker arrived wrapped in backticks after a Korean label (exit 72 by
// accident); printed bare, the launcher would have scored rc=0.
//
// The launcher already holds the transcript — it is the trail — so the count
// is taken at finish, recorded in the sentinel, and a zero on an otherwise
// clean exit is refused as exit 73. The verdict runs after the done-marker
// check, so an absent marker keeps 72: the codes never stack, and the
// sentinel's tool_calls=0 already tells that reader the rest.

// countToolUses counts the tool_use content blocks in assistant messages of a
// claude-code session transcript: one JSON object per line, tool calls as
// blocks of message.content. Every tool counts, whatever its name, and
// sidechain (subagent) lines count too — they are still this round's work.
//
// A line that is not JSON does not stop the count: it is skipped and
// reported, because a transcript written by the harness can carry a truncated
// tail around an intact body. A line the scanner cannot deliver at all (too
// long, read error) aborts with an error instead — the count would then cover
// an unknown prefix of the file, and a zero read off a truncated scan could
// fail a round whose tool calls were all in the missing tail.
func countToolUses(path string) (n, unparseable int, err error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	// Transcript lines run large — a measured working round's longest was
	// 215,623 bytes (attachment blocks) — so the cap matches report.go's.
	sc.Buffer(make([]byte, 0, 256*1024), 32*1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		// Two stages, because "unparseable" must mean the LINE is not JSON: a
		// user line carries message.content as a plain string (measured in the
		// incident transcript), which a typed struct over the whole line would
		// reject — and a valid line miscounted as damaged would stamp partial
		// onto every round that only read its spec.
		var probe struct {
			Type    string          `json:"type"`
			Message json.RawMessage `json:"message"`
		}
		if json.Unmarshal(line, &probe) != nil {
			unparseable++
			continue
		}
		if probe.Type != "assistant" || len(probe.Message) == 0 {
			continue
		}
		var msg struct {
			Content []struct {
				Type string `json:"type"`
			} `json:"content"`
		}
		if json.Unmarshal(probe.Message, &msg) != nil {
			// An assistant line whose message is another shape (content as a
			// plain string): valid JSON, no blocks to count.
			continue
		}
		for _, c := range msg.Content {
			if c.Type == "tool_use" {
				n++
			}
		}
	}
	if err := sc.Err(); err != nil {
		return 0, 0, err
	}
	return n, unparseable, nil
}

// toolCallVerdict counts this round's tool calls and turns the count into the
// sentinel line it returns, plus possibly exit 73. Every harness whose trail
// is not a claude-code transcript gets a no-op empty line: counting there is
// future work, deliberately not this change.
func (r *round) toolCallVerdict(rc int) (line string, rcOut int) {
	h, known := findHarness(r.o.harness)
	if !known || h.trailFormat != tail.FormatClaudeTranscript {
		return "", rc
	}
	// The count must come from the file the sentinel names as the trail. The
	// claude-code harness reveals its transcript path only once the session
	// exists: runClaudeCode fills r.trail from the transcript it had to locate
	// for the identity assertion, and a round whose analysis found nothing
	// still has the SessionStart hook's reveal in the registry.
	trail := r.trail
	if trail == "" && r.runID != "" {
		if rec := runs.FindByID(r.runID); rec != nil && rec.Trail != "" {
			trail = rec.Trail
			r.trail = trail
		}
	}
	if trail == "" {
		return r.unknownToolCalls(rc, "no trail path recorded")
	}
	n, bad, err := countToolUses(trail)
	if err != nil {
		return r.unknownToolCalls(rc, sentinelReason(err.Error()))
	}
	if bad > 0 {
		// A partial count is a lower bound over a damaged file, not the
		// "zero tool_use blocks" fact exit 73 asserts, so the verdict stays off
		// it and the caveat travels with the number.
		return fmt.Sprintf("tool_calls=%d (partial: %d unparseable %s)", n, bad, lineWord(bad)), rc
	}
	if n > 0 {
		return fmt.Sprintf("tool_calls=%d", n), rc
	}
	if r.o.allowNoTools {
		if rc == 0 {
			fmt.Fprintln(r.stderr, "outsource: --allow-no-tools: the round made no tool calls; recorded as allowed, exit code unchanged")
		}
		telemetry.Note("why", "zero tool calls, allowed by flag")
		return "tool_calls=0 (allowed)", rc
	}
	if rc == 0 {
		last := r.reportLastLine()
		if last == "" {
			last = "<no report could be extracted from the log>"
		}
		fmt.Fprintf(r.stderr, "outsource: the round made no tool calls — the transcript %s holds zero tool_use blocks, but the report claims completion (its last line: %q); a round that edits files, runs commands or reads the repo cannot do any of that without a tool call, so not claiming a pass (exit 73). Pass --allow-no-tools if this round is legitimately answer-only.\n", trail, last)
		telemetry.Note("why", "zero tool calls: report claims work, transcript holds none")
		return "tool_calls=0", ExitNoToolCalls
	}
	return "tool_calls=0", rc
}

// unknownToolCalls records a count that could not be taken. It never changes
// the exit code: "could not count" is not "counted zero", and writing 0 here
// would turn a missing transcript into a fabrication verdict.
func (r *round) unknownToolCalls(rc int, reason string) (string, int) {
	if rc == 0 {
		fmt.Fprintf(r.stderr, "outsource: the round's tool-call count could not be taken (%s); recorded as tool_calls=unknown, exit code unchanged\n", reason)
	}
	return "tool_calls=unknown (" + sentinelReason(reason) + ")", rc
}

// reportLastLine is the report's final line as the marker verdict reads it —
// the line that claimed completion, for the exit-73 message to quote.
func (r *round) reportLastLine() string {
	if r.o.log == "" {
		return ""
	}
	f, err := os.Open(r.o.log)
	if err != nil {
		return ""
	}
	defer f.Close()
	rep, _, ok := report.ExtractSource(f)
	if !ok {
		return ""
	}
	return report.LastLine(rep)
}

// sentinelReason keeps a reason on one line: the sentinel is line-oriented,
// and an error string is the one reason source that may carry a newline.
func sentinelReason(s string) string {
	return strings.ReplaceAll(strings.TrimSpace(s), "\n", " ")
}

func lineWord(n int) string {
	if n == 1 {
		return "line"
	}
	return "lines"
}
