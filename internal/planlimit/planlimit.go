// Package planlimit decides whether a delegated round died of its provider's
// plan limit (an HTTP 429 such as z.ai's 5-hour usage cap) and, when it did,
// when the provider says the limit resets.
//
// Why a package of its own: two readers need the same answer from the same
// trail — the launcher, which writes it into the <log>.rc sentinel after a
// claude-code round exits and decides whether --resume-on-reset waits, and
// last-report, which falls back to the trail for a sentinel written before the
// sentinel carried the answer. It imports nothing but the standard library, so
// both can import it without a cycle and without pulling the audit package's
// transcript parser into last-report. The run registry (`runs`) deliberately
// does NOT import it: `runs line` is on the status line's hot path and reads
// sentinel fields only, never a trail.
//
// The incident (2026-10-06): a GLM round on the claude-code harness was cut by
// z.ai's 5-hour limit. Its sentinel said rc=1 and done_marker=absent, its
// "report" was the API error line, and nothing anywhere said "plan limit" or
// when it would reset — the lead read the trail by hand and resumed the
// session with a hand-written spec.
package planlimit

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Sentinel keys. The launcher writes them and last-report and `runs` read
// them; one spelling, here.
const (
	KeyExhausted = "quota_exhausted"
	KeyResetAt   = "reset_at"
	// KeyQuotaResetAt is the plan quota API's own reset for its tightest
	// window, read once when the round died. Readers prefer it to reset_at,
	// the 429 text's reset, and say which one they show (see resetForms for
	// why the text's reset is not enough).
	KeyQuotaResetAt = "quota_reset_at"
	KeyAPIError     = "api_error"
	KeyResumed      = "resumed_after_reset"
	KeyZoneSuspect  = "reset_zone_suspect"
	// Unknown is the reset_at value when the error text carried no reset time
	// this package can read. Never a guess: a wrong time would make
	// --resume-on-reset sleep through a usable window or resume into a spent one.
	Unknown = "unknown"
)

// maxMessage caps the api_error text. The z.ai message measured on
// 2026-10-06 is 113 characters; the cap keeps a provider that returns a page
// of HTML from turning one sentinel line into a page.
const maxMessage = 160

// Death is what Detect found at the end of a trail.
type Death struct {
	// Died is true when the LAST assistant entry is Claude Code's own API-error
	// line for a plan limit. A 429 earlier in the trail that the harness got
	// past (its retry, a resumed session) is not a death.
	Died bool
	// At is the error line's own timestamp; zero when the line has none.
	At time.Time
	// Status is the HTTP status as Claude Code recorded it ("429"), or the
	// error kind ("rate_limit") when no status was recorded.
	Status string
	// Message is the provider's part of the error text, flattened to one line
	// and capped at maxMessage runes.
	Message string
	// ResetText is the reset time exactly as the provider wrote it, "" when the
	// text carries none in this provider's form. Two deaths with the same
	// ResetText describe the same, not-yet-reset limit.
	ResetText string
	// ResetAt is ResetText read in the provider's zone; zero when ResetText is
	// absent or unparsable.
	ResetAt time.Time
}

// ResetAtField is the sentinel's reset_at value: RFC3339 UTC, or "unknown".
func (d Death) ResetAtField() string {
	if d.ResetAt.IsZero() {
		return Unknown
	}
	return d.ResetAt.UTC().Format(time.RFC3339)
}

// APIErrorField is the sentinel's api_error value: "429 [1308][Usage limit …]".
func (d Death) APIErrorField() string {
	return strings.TrimSpace(d.Status + " " + d.Message)
}

// ParseResetAt reads a reset_at value back. ok is false for "unknown", an
// empty field and anything that is not RFC3339.
func ParseResetAt(s string) (time.Time, bool) {
	if s == "" || s == Unknown {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// resetForm is how one provider writes the reset time inside its 429 text.
type resetForm struct {
	re     *regexp.Regexp // first group: the time as written
	layout string
	zone   *time.Location
}

// resetForms is keyed by provider (the launcher's provider name). A provider
// with no row still dies of a plan limit; its reset is simply unknown.
//
// zai, measured 2026-10-06: "[1308][Usage limit reached for 5 hour. Your
// limit will reset at 2026-10-06 16:23:45][<request id>]". The time carries no
// zone. The request id opens with the server's own clock — 20261006143041… on
// a line whose UTC timestamp is 06:30:41Z — so z.ai writes UTC+8, and 16:23:45
// is 08:23:45Z. The same offset held on 429s at 03:21:50Z and 08:24:01Z, and at
// 08:24Z the text's reset (19:32:49 → 11:32:49Z) equalled the quota API's 5h
// reset to the second.
//
// The zone is right; the time is not always the moment the plan takes
// requests again. Measured the same day on round q6khead: a 429 at 06:32:00Z
// said "reset at 16:23:45" (08:23:45Z), the same session was resumed and
// glm-5.3 answered at 06:33:39Z, and the quota API's 5h window that then ran
// to 11:32:49Z was anchored at 06:32:49Z — 1h51m before the text's reset. (A
// 429 at 03:21:50Z in another round had named 06:31:05Z.) So the launcher
// records the quota API's reset beside this one (quota_reset_at), readers
// prefer it, and --resume-on-reset reads the plan every five minutes while it
// waits, keeping this reset only as the upper bound.
var resetForms = map[string]resetForm{
	"zai": {
		re:     regexp.MustCompile(`reset at (\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2})`),
		layout: "2006-01-02 15:04:05",
		zone:   time.FixedZone("UTC+8", 8*60*60),
	},
}

// ParseReset finds the reset time in an error text in the provider's form.
// text is "" when the form does not match (or the provider has no row); at is
// zero when it matched but did not parse — "unknown", never a guess.
func ParseReset(provider, msg string) (text string, at time.Time) {
	f, ok := resetForms[provider]
	if !ok {
		return "", time.Time{}
	}
	m := f.re.FindStringSubmatch(msg)
	if m == nil {
		return "", time.Time{}
	}
	t, err := time.ParseInLocation(f.layout, m[1], f.zone)
	if err != nil {
		return m[1], time.Time{}
	}
	return m[1], t.UTC()
}

// trailLine is the part of a claude-code transcript line this package reads.
type trailLine struct {
	Type              string `json:"type"`
	Timestamp         string `json:"timestamp"`
	IsSidechain       bool   `json:"isSidechain"`
	IsAPIErrorMessage bool   `json:"isApiErrorMessage"`
	Error             string `json:"error"`
	APIErrorStatus    int    `json:"apiErrorStatus"`
	Message           struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// Detect reads a claude-code session transcript and reports whether the round
// died of a plan limit. The last assistant entry decides — not the last line:
// the harness writes bookkeeping lines (last-prompt, cost-state) after it.
// Sidechain (subagent) lines are skipped; the main chain is the round.
//
// An error means the trail could not be read to the end, and the caller
// learns nothing from the Death returned with it.
func Detect(trailPath, provider string) (Death, error) {
	f, err := os.Open(trailPath)
	if err != nil {
		return Death{}, err
	}
	defer f.Close()
	var last *trailLine
	sc := bufio.NewScanner(f)
	// Transcript lines run large (215,623 bytes measured on a working round);
	// the cap matches the tool-call counter's.
	sc.Buffer(make([]byte, 0, 256*1024), 32*1024*1024)
	for sc.Scan() {
		b := sc.Bytes()
		// Cheap filter first: most lines of a long trail are tool results.
		if !bytes.Contains(b, []byte(`"assistant"`)) {
			continue
		}
		var l trailLine
		if json.Unmarshal(b, &l) != nil || l.Type != "assistant" || l.IsSidechain {
			continue
		}
		last = &l
	}
	if err := sc.Err(); err != nil {
		return Death{}, err
	}
	if last == nil || !last.IsAPIErrorMessage {
		return Death{}, nil
	}
	if last.APIErrorStatus != 429 && last.Error != "rate_limit" {
		return Death{}, nil
	}
	text := contentText(last.Message.Content)
	d := Death{Died: true, Status: last.Error, Message: shortMessage(text)}
	if last.APIErrorStatus != 0 {
		d.Status = strconv.Itoa(last.APIErrorStatus)
	}
	if t, err := time.Parse(time.RFC3339Nano, last.Timestamp); err == nil {
		d.At = t.UTC()
	}
	d.ResetText, d.ResetAt = ParseReset(provider, text)
	return d, nil
}

// contentText joins the text blocks of message.content.
func contentText(raw json.RawMessage) string {
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return s
		}
		return ""
	}
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" && b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, " ")
}

// shortMessage keeps the provider's part of Claude Code's error text: from the
// first '[' when there is one ("[1308][Usage limit reached …][<request id>]"),
// else whatever follows the "API Error:" prefix. One line, capped.
func shortMessage(text string) string {
	s := strings.Join(strings.Fields(text), " ")
	if i := strings.IndexByte(s, '['); i >= 0 {
		s = s[i:]
	} else {
		s = strings.TrimSpace(strings.TrimPrefix(s, "API Error:"))
	}
	if r := []rune(s); len(r) > maxMessage {
		s = string(r[:maxMessage-1]) + "…"
	}
	return s
}

// IsAPIErrorText reports whether a "report" is nothing but Claude Code's own
// API-error line. A round cut by a 429 leaves exactly that as the result
// field of its log, and last-report printed it as if the delegate had written
// it (measured 2026-10-06).
func IsAPIErrorText(s string) bool {
	s = strings.TrimSpace(s)
	return strings.HasPrefix(s, "API Error:") && !strings.Contains(s, "\n")
}
