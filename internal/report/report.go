// Package report extracts a delegated round's final report from its log,
// whatever the backend wrote it.
//
// The field problem this solves (2026-08-17, four rounds in one night): every
// finished round made the lead hand-write the same throwaway JSON extractor,
// twice over because the backends differ.
//
//	claude-code harness (run.log, JSONL): the last {"type":"result"} event's
//	  "result" field; older logs may only have long assistant text blocks.
//	grok CLI (streaming-json ndjson): there is no result event at all — the
//	  report is the concatenation of {"type":"text"} deltas after the LAST
//	  tool_call/tool_call_update event.
//	opencode CLI (`opencode run --format json`): events are step_start,
//	  tool_use, text, step_finish. The report is part.text of every "text"
//	  event after the last "tool_use" (same trailing-window rule as grok).
//	  A final step_finish with part.reason=="stop" marks the end of that
//	  turn but is not required — a died-mid-run log with trailing text is
//	  still a report.
//	agy CLI (`agy -p --output-format stream-json`, measured 2026-08-27):
//	  events are keyed "event", not "type". The final {"event":"result"}
//	  carries the whole response verbatim in result.response — an explicit
//	  result, same trust rank as claude-code's.
//	crush CLI: not JSONL at all. The log is the assistant's prose, with
//	  turns run together and no envelope of any kind (measured 2026-08-27:
//	  a finished round whose sentinel said done_marker=found returned exit
//	  65, "no report-shaped content" — the report was sitting in the file).
//	  For a log where NOT ONE line parsed as JSON, the report is the text
//	  from the last markdown heading onward; with no heading, the trailing
//	  text. Lowest trust rank, and gated on the whole file being non-JSON so
//	  it can never scavenge from a JSONL log that simply held no report.
//
// The shape is detected per line, so a log that mixes them — or a future
// harness that adopts any — still yields the report.
//
// This prints the delegate's words verbatim. It does NOT prove completion: the
// <log>.rc sentinel is the completion evidence, and a report without a sentinel
// is a round that has not finished.
package report

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/midagedev/outsource/internal/runs"
)

const (
	ExitUsage      = 64
	ExitNoReport   = 65
	ExitUnreadable = 66
)

// minLongText is how much assistant text has to be there before it is treated
// as a report. It is a guess by construction — the fallback exists only for
// logs with no result event — so the bar is set where a real report sits and a
// one-line acknowledgement does not.
const minLongText = 200

// Source says which arm produced the report. Structured means a JSON event
// boundary vouched for it — an explicit result, grok/opencode trailing text,
// or a long assistant text. PlainTail is the last resort for a log with no
// JSON anywhere: a boundary guessed from headings, or the whole text when
// there is none. Callers deciding how much to trust the report's *edges*
// (the done-marker verdict does) need the difference; callers printing the
// report do not.
type Source int

const (
	SourceStructured Source = iota
	SourcePlainTail
)

// Extract returns the report found in a log stream.
func Extract(r io.Reader) (string, bool) {
	rep, _, ok := ExtractSource(r)
	return rep, ok
}

// ExtractSource is Extract plus which arm produced the report.
func ExtractSource(r io.Reader) (string, Source, bool) {
	var (
		result       string
		haveResult   bool
		lastLongText string
		plainLines   []string
		sawJSON      bool
		grokParts    []string
		sawGrokTool  bool
	)
	sc := bufio.NewScanner(r)
	// A single result field can hold an entire report, far past the default
	// 64KB line limit; the shell read whole lines with no such cap.
	sc.Buffer(make([]byte, 0, 256*1024), 32*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var obj map[string]json.RawMessage
		if json.Unmarshal([]byte(line), &obj) != nil {
			// Non-JSON and truncated lines are skipped, never fatal — but
			// kept, because a crush log is nothing else. The plain-text arm
			// below only fires when no line in the whole file was JSON.
			plainLines = append(plainLines, sc.Text())
			continue
		}
		sawJSON = true
		var typ string
		if raw, ok := obj["type"]; ok {
			_ = json.Unmarshal(raw, &typ)
		}
		// agy keys its events "event" and nests the answer: the result
		// event's result.response is the model's full final text.
		if typ == "" {
			var evName string
			if raw, ok := obj["event"]; ok {
				_ = json.Unmarshal(raw, &evName)
			}
			if evName == "result" {
				var res struct {
					Response string `json:"response"`
				}
				if raw, ok := obj["result"]; ok && json.Unmarshal(raw, &res) == nil && res.Response != "" {
					result, haveResult = res.Response, true
				}
				continue
			}
		}
		switch typ {
		case "result":
			var s string
			if raw, ok := obj["result"]; ok && json.Unmarshal(raw, &s) == nil && s != "" {
				result, haveResult = s, true
			}
		case "assistant":
			var msg struct {
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			}
			if raw, ok := obj["message"]; ok && json.Unmarshal(raw, &msg) == nil {
				for _, c := range msg.Content {
					if c.Type == "text" && len(c.Text) >= minLongText {
						lastLongText = c.Text
					}
				}
			}
		case "tool_call", "tool_call_update", "tool_use":
			// A tool ran, so anything collected before it was not the final
			// report: reset. This is what keeps a marker quoted during planning
			// from being mistaken for the delegate's conclusion. tool_use is
			// opencode's name for the same boundary (measured 2026-08-23).
			sawGrokTool = true
			grokParts = nil
		case "text":
			if raw, ok := obj["data"]; ok {
				var s string
				if json.Unmarshal(raw, &s) == nil {
					grokParts = append(grokParts, s)
					break
				}
				var d struct {
					Text *string `json:"text"`
				}
				if json.Unmarshal(raw, &d) == nil && d.Text != nil {
					grokParts = append(grokParts, *d.Text)
					break
				}
			}
			// opencode: {"type":"text","part":{"text":"…"}}. Grok's "text"
			// events carry "data", not "part", so the two do not collide.
			if raw, ok := obj["part"]; ok {
				var p struct {
					Text string `json:"text"`
				}
				if json.Unmarshal(raw, &p) == nil && p.Text != "" {
					grokParts = append(grokParts, p.Text)
				}
			}
		}
	}

	// Preference order mirrors trustworthiness: an explicit result event is the
	// harness saying "this is the answer"; grok's trailing text is the answer by
	// construction, since nothing runs after it; a long assistant text is a
	// guess. The two grok arms are kept separate — one for a log whose shape a
	// tool event confirmed, one for a final turn that called no tools at all —
	// because collapsing them would accept trailing text from any log shape.
	var out string
	src := SourceStructured
	switch {
	case haveResult:
		out = result
	case sawGrokTool && len(grokParts) > 0:
		out = strings.Join(grokParts, "")
	case !sawGrokTool && len(grokParts) > 0:
		out = strings.Join(grokParts, "")
	case lastLongText != "":
		out = lastLongText
	case !sawJSON:
		out = plainTail(plainLines)
		src = SourcePlainTail
	}
	if strings.TrimSpace(out) == "" {
		return "", src, false
	}
	return strings.TrimSpace(out), src, true
}

// headingRe matches a markdown ATX heading at the start of a line, capturing
// its level. A crush report opens with one ("# GDK-962: … — final report"),
// which is the only boundary such a log offers: the prose before it is the
// round's narration, and the turns are run together with no separator at all.
var headingRe = regexp.MustCompile(`^(#{1,3}) \S`)

// plainTail is the last resort, for a log that is not JSON anywhere. It cuts
// at the last heading of the STRONGEST level the log contains — an H1 when
// there is one, else an H2, else an H3. Taking the last heading of any level
// was the first draft and it cut inside the report, at its final "## 6"
// section (measured on the round that prompted this). With no heading at all
// the whole text is the report: the caller has already established the file
// holds no structured event to prefer.
func plainTail(lines []string) string {
	best := 4 // stronger than any level we match
	start := 0
	for i, l := range lines {
		m := headingRe.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		switch level := len(m[1]); {
		case level < best:
			best, start = level, i
		case level == best:
			start = i
		}
	}
	return strings.Join(lines[start:], "\n")
}

// EndsWithMarker reports whether the report's LAST non-empty line is the
// completion marker. Contains() over the whole report was the field defect
// (2026-08-22, GDK-616 round): a final message saying "…the report will end
// with `DONE-X`" quoted the marker mid-sentence and was scored found while
// the round's gates were still running. The spec contract has always been
// "the last line is exactly the marker", so the verdict now reads exactly
// that line. Markdown decoration a model might wrap the token in (backticks,
// bold asterisks) is stripped; anything more — a marker inside a sentence,
// punctuation glued on — scores absent, which is the safe direction: absent
// sends the lead to the tree, found ends the audit.
func EndsWithMarker(rep, marker string) bool {
	if marker == "" {
		return false
	}
	lines := strings.Split(rep, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		line = strings.Trim(line, "`*")
		return line == marker
	}
	return false
}

// Main is the last-report entry point.
func Main(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "" {
		fmt.Fprintln(stderr, "usage: last-report <log-file> [--max-chars N]")
		return ExitUsage
	}
	path := args[0]
	args = args[1:]
	maxChars := 0
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--max-chars":
			if i+1 >= len(args) {
				fmt.Fprintln(stderr, "last-report: --max-chars needs a value")
				return ExitUsage
			}
			n, err := strconv.Atoi(args[i+1])
			if err != nil || n < 0 {
				fmt.Fprintf(stderr, "last-report: --max-chars wants a whole number, got: %s\n", args[i+1])
				return ExitUsage
			}
			maxChars = n
			i++
		default:
			fmt.Fprintf(stderr, "last-report: unknown flag: %s\n", args[i])
			return ExitUsage
		}
	}

	f, err := os.Open(path)
	if err != nil {
		fmt.Fprintf(stderr, "last-report: unreadable: %s\n", path)
		return ExitUnreadable
	}
	defer f.Close()

	rep, ok := Extract(f)
	if !ok {
		// Exit 65 rather than printing nothing, so a died-mid-run round is a
		// branch a caller can take and not a silence it has to interpret.
		// Measured 2026-08-22: a lead asked for the report of a round the
		// external killer had TERM'd 41 seconds earlier; the tool said only
		// "no report" while the sentinel already had rc=-1 + wrapper_signal=TERM.
		fmt.Fprintf(stderr, "last-report: no report-shaped content in %s\n", path)
		diagnoseNoReport(path, stderr)
		return ExitNoReport
	}
	// Truncation counts runes, not bytes: cutting a report mid-character would
	// put mojibake in front of a lead reading a delegate's own words.
	if maxChars > 0 {
		if r := []rune(rep); len(r) > maxChars {
			rep = string(r[:maxChars]) + fmt.Sprintf("\n… [truncated at %d chars]", maxChars)
		}
	}
	fmt.Fprintln(stdout, rep)
	return 0
}

// diagnoseNoReport appends what the sentinel or the run registry already
// knows, so exit 65 is not a blind "no report". Sentinel wins: a killed
// round has a .rc even when the registry still looks running for a moment.
func diagnoseNoReport(logPath string, stderr io.Writer) {
	rcPath := logPath + ".rc"
	if b, err := os.ReadFile(rcPath); err == nil {
		kv := parseSentinel(string(b))
		rc, finished, sig := kv["rc"], kv["finished"], kv["wrapper_signal"]
		if sig != "" {
			fmt.Fprintf(stderr, "no report: the round was killed (%s) at %s (rc=%s); see %s\n",
				sig, finished, rc, rcPath)
			return
		}
		fmt.Fprintf(stderr, "no report: the round finished at %s (rc=%s); see %s\n",
			finished, rc, rcPath)
		return
	}
	if rec := runs.FindByLog(logPath); rec != nil && rec.State() == runs.Running {
		fmt.Fprintln(stderr, "round still running — no report expected yet")
	}
}

func parseSentinel(s string) map[string]string {
	m := make(map[string]string)
	for _, line := range strings.Split(s, "\n") {
		k, v, ok := strings.Cut(line, "=")
		if ok {
			m[k] = v
		}
	}
	return m
}
