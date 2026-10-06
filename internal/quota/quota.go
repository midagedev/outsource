// Package quota reads a delegation backend's remaining plan quota, so a round
// can be sized — or refused — before it is launched instead of dying mid-flight.
// Read-only; keeps no state.
//
// Providers, and where each one's numbers come from:
//
//	zai   the coding plan's own console API, authenticated with the api key
//	      bin/credential.sh resolves:
//	        /api/monitor/usage/quota/limit   data.level + data.limits[]
//	        /api/biz/subscription/list       plan name/status/validity
//	      Two rolling windows (5-hour and weekly) with real credit counts.
//
//	grok  the Grok CLI's own billing proxy, authenticated with the OAuth access
//	      token the CLI stores — grok has no api key:
//	        /v1/billing?format=credits   weekly credit usage percent
//	        /v1/billing                  monthly included budget (fallback)
//	      Percent only: xAI exposes no credit counts here, so allowance,
//	      consumed and remaining are null and the percentage is the whole signal.
//
// Credentials never reach a log, a file this code writes, or a command line. The
// shell version had to work for that — it rode the key in on curl's stdin as a
// config file, because an argument would show up in a process listing. Speaking
// HTTP directly removes the problem rather than defending against it: the token
// exists as a header in this process's memory and nowhere else.
package quota

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Exit codes are the contract: the launcher's --require-quota gate branches on
// them, and so does the status line's refresher.
const (
	ExitOK           = 0
	ExitNoCredential = 1
	ExitEndpoint     = 2
	ExitGated        = 3
	ExitUsage        = 64
)

// pyNum is a number that remembers its printed form.
//
// Python distinguishes int from float and str() shows it: an API percentage of
// 23 prints "23", while a computed round(x, 1) prints "76.9" and, crucially,
// "12.0" rather than "12". Collapsing both into float64 changed the human output
// AND the --json shape — caught on a smoke test against the live plan, where the
// shell said "88.0% used / 12.0% left" and the first Go draft said "89% / 11%".
//
// So provider numbers are decoded with json.Number, which preserves the exact
// token the API sent, and computed percentages are formatted to one decimal the
// way round(x, 1) then str() does.
type pyNum struct {
	text string
	f    float64
}

func (p pyNum) MarshalJSON() ([]byte, error) { return []byte(p.text), nil }
func (p pyNum) String() string               { return p.text }
func (p pyNum) Float() float64               { return p.f }

// fromToken keeps a provider's own formatting.
func fromToken(n json.Number) pyNum {
	f, _ := n.Float64()
	return pyNum{text: n.String(), f: f}
}

// computed1 is round(x, 1) followed by str(): always at least one decimal.
func computed1(f float64) pyNum {
	return pyNum{text: strconv.FormatFloat(f, 'f', 1, 64), f: f}
}

// Window is one rolling allowance. allowance/consumed/remaining are null when
// the provider reports only a percentage (grok). percentage and remainingPercent
// are always present — that is what --require-window gates on, so the gate works
// on both providers.
type Window struct {
	Label            string  `json:"label"`
	Unit             *int64  `json:"unit"`
	Number           *pyNum  `json:"number"`
	Allowance        *int64  `json:"allowance"`
	Consumed         *int64  `json:"consumed"`
	Remaining        *int64  `json:"remaining"`
	Percentage       *pyNum  `json:"percentage"`
	RemainingPercent pyNum   `json:"remainingPercent"`
	NextResetTime    int64   `json:"nextResetTime"`
	NextResetTimeIso *string `json:"nextResetTimeIso"`

	// Not serialized: display helpers, the shell's _hhmm and _rel.
	hhmm string
	rel  string
}

// Subscription is plan identity. Error carries the degrade note when the
// identity call failed; the quota numbers still print, because they are the
// load-bearing part.
type Subscription struct {
	ProductName *string `json:"productName"`
	Status      *string `json:"status"`
	Valid       *string `json:"valid"`
	Error       *string `json:"error"`
}

// Report is the --json shape. Another script depends on it — keep it stable.
type Report struct {
	FetchedAt    string       `json:"fetchedAt"`
	Provider     string       `json:"provider"`
	Level        *string      `json:"level"`
	Subscription Subscription `json:"subscription"`
	Windows      []Window     `json:"windows"` // shortest window first
}

type failure struct{ msg string }

func (f failure) Error() string { return f.msg }

func fail(format string, a ...any) error { return failure{fmt.Sprintf(format, a...)} }

// ---- window construction ---------------------------------------------------

func newWindow(label string, resetMillis int64, percentage *pyNum,
	allowance, consumed, remaining *int64, unit *int64, number *pyNum, now time.Time) Window {
	// percentage is consumed %; remainingPercent is what --require-window gates
	// on. Prefer real counts when the provider gives them, because 91 vs 90.8 is
	// the difference between a report and a gate agreeing.
	var remPct pyNum
	switch {
	case allowance != nil && *allowance != 0:
		var rem int64
		if remaining != nil {
			rem = *remaining
		}
		remPct = computed1(100.0 * float64(rem) / float64(*allowance))
	case percentage != nil:
		remPct = computed1(100.0 - percentage.Float())
	default:
		remPct = computed1(100.0)
	}
	w := Window{
		Label: label, Unit: unit, Number: number,
		Allowance: allowance, Consumed: consumed, Remaining: remaining,
		Percentage: percentage, RemainingPercent: remPct,
		NextResetTime: resetMillis,
		hhmm:          "?", rel: relative(resetMillis, now),
	}
	if resetMillis != 0 {
		t := time.UnixMilli(resetMillis)
		iso := t.UTC().Format("2006-01-02T15:04:05Z")
		w.NextResetTimeIso = &iso
		w.hhmm = t.Local().Format("15:04") // local, as the shell printed it
	}
	return w
}

func relative(ms int64, now time.Time) string {
	if ms == 0 {
		return "reset time unknown"
	}
	d := time.UnixMilli(ms).Sub(now)
	if d <= 0 {
		return "now" // defensive: a live rolling window always resets ahead
	}
	s := int64(d.Seconds())
	return fmt.Sprintf("in %dh %dm", s/3600, s%3600/60)
}

// round1 matches Python's round(x, 1) closely enough for these magnitudes; the
// values are percentages between 0 and 100 and are only ever compared against a
// floor or printed.
func round1(f float64) float64 {
	return float64(int64(f*10+copysign(0.5, f))) / 10
}

func copysign(mag, sign float64) float64 {
	if sign < 0 {
		return -mag
	}
	return mag
}

// ---- transport -------------------------------------------------------------

type response struct {
	status int
	body   []byte
	err    error // network-level failure; the body is meaningless then
}

func get(url string, headers map[string]string) response {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return response{err: fmt.Errorf("bad url: %w", err)}
	}
	req.Header.Set("Accept", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return response{err: err}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return response{err: err}
	}
	return response{status: resp.StatusCode, body: body}
}

// siblingScript resolves a tool that still lives beside the binary. During the
// port some tools are Go and some are shell; this is the seam, and it disappears
// as each one moves.
//
// The dispatcher runs this binary from its download cache when there is no
// local build (no binary is committed to git), and beside a cached executable
// there are no shims — they live in the skill's bin/, which the dispatcher
// exports as OUTSOURCE_SKILL_DIR for exactly this case. The executable's own
// directory stays the fallback, so a local build beside the shims behaves as
// before.
func siblingScript(name string) string {
	if dir := os.Getenv("OUTSOURCE_SKILL_DIR"); dir != "" {
		p := filepath.Join(dir, "bin", name)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	exe, err := os.Executable()
	if err != nil {
		return name
	}
	return filepath.Join(filepath.Dir(exe), name)
}

func credential(args ...string) (string, error) {
	out, err := exec.Command(siblingScript("credential.sh"), args...).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func pyPtr(p pyNum) *pyNum    { return &p }
func strPtr(s string) *string { return &s }

func asInt(v any) (int64, bool) {
	switch t := v.(type) {
	case float64:
		return int64(t), true
	case json.Number:
		if n, err := t.Int64(); err == nil {
			return n, true
		}
		// A float-shaped token still has an integer part, which is what
		// Python's int() would have raised on — so treat it as absent instead.
		return 0, false
	}
	return 0, false
}

func asFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case json.Number:
		f, err := t.Float64()
		return f, err == nil
	case bool:
		return 0, false
	}
	return 0, false
}

// asNumber keeps the provider's own token when there is one.
func asNumber(v any) (pyNum, bool) {
	if n, ok := v.(json.Number); ok {
		return fromToken(n), true
	}
	if f, ok := asFloat(v); ok {
		return computed1(f), true
	}
	return pyNum{}, false
}

// asMoney reads grok's {"val": …}, where val is a number on some accounts and a
// decimal string on others — reject one and the monthly budget silently
// disappears.
func asMoney(v any) (float64, bool) {
	m, ok := v.(map[string]any)
	if !ok {
		return 0, false
	}
	switch t := m["val"].(type) {
	case float64:
		return t, true
	case json.Number:
		f, err := t.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
		return f, err == nil
	}
	return 0, false
}

func parseISOMillis(v any) (float64, bool) {
	s, ok := v.(string)
	if !ok || s == "" {
		return 0, false
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05"} {
		if t, err := time.Parse(layout, strings.Replace(s, "Z", "+00:00", 1)); err == nil {
			return float64(t.UnixMilli()), true
		}
	}
	return 0, false
}

// decodeJSON preserves number tokens. Every provider body goes through here, so
// no path can accidentally lose a provider's formatting.
func decodeJSON(b []byte, out *map[string]any) error {
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.UseNumber()
	return dec.Decode(out)
}

var errNoCredential = errors.New("no usable credential")
