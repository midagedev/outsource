// Package tail follows a delegated round while it is still running.
//
// Why it exists: a round is registered, runs for twenty minutes, and prints a
// report at the end. Between those two moments the only things a lead could
// see were `runs`' elapsed time and an IDLE column — and on the claude-code
// harness the --log file is empty for the round's entire life, because
// `--output-format json` emits one object at exit. The live trail was always
// there (the harness writes its session transcript every turn), but nothing
// told you WHICH file it was: the projects/ directory holds one .jsonl per
// session and picking the newest breaks the moment two rounds share a cwd.
// Reported 2026-09-15 by a session that spent ten tool calls hunting for it.
//
// So the round now reveals its own trail (a SessionStart hook writes the path
// into the registry record — see launch.writeHookSettings and runs.SetTrail),
// and this tool is the supported way to read it. It owns both halves: the
// recorder the hook calls, and the reader a human calls.
package tail

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/midagedev/outsource/internal/runs"
)

const usage = `Follow a delegated round while it is still running.

  tail [<selector>] [-f] [-n N] [--raw] [--all] [-w N]

<selector> is a run id, a --log path or a --label. Omitted, it means "the one
round that is running". A label several rounds share resolves only when
exactly one of them is live; otherwise the candidates are listed and nothing
is guessed.

  -f, --follow      keep reading until the round finishes (it stops on its own)
  -n N              render the last N entries; 0 means all (default 40)
  -w N              clip each entry to N columns; 0 means no clipping (default 160)
      --raw         print the trail's own lines, unrendered, for piping to jq
      --all         include thinking blocks and tool results, not just what the
                    round said and did
      --max-seconds N  ceiling on --follow (default: none; it ends with the round)

Exit codes: 0 ok · 64 usage, or a selector that names more than one round ·
65 no such run, or the round has not revealed a trail yet
`

// Formats a trail can be in. The harness table (internal/launch/wiring.go)
// declares one per harness and a gate there holds it to this list, so an
// unrenderable format cannot be shipped by adding a row.
const (
	// FormatClaudeTranscript is the claude-code session transcript: one JSON
	// object per line, type=assistant|user, message.content a part array.
	// Measured 2026-09-15 (CLI 2.1.272) — a SessionStart hook reported
	// transcript_path under `claude -p`, and that file grows every turn.
	FormatClaudeTranscript = "claude-transcript"
	// FormatOpencodeEvents is opencode's `run --format json` stream: one event
	// per line, part.text / part.tool (references/opencode.md, 2026-08-23).
	FormatOpencodeEvents = "opencode-events"
	// FormatLines is a trail whose entries are not decoded — crush's text log,
	// agy's stream-json. Shown verbatim rather than described wrongly.
	FormatLines = "lines"
)

func KnownFormat(f string) bool {
	switch f {
	case FormatClaudeTranscript, FormatOpencodeEvents, FormatLines:
		return true
	}
	return false
}

const (
	ExitUsage   = 64
	ExitNoTrail = 65
)

// Main is the tool entry point. Two jobs, told apart by one flag, because they
// are two halves of one fact: where this round's live trail is.
func Main(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	for i, a := range args {
		if a == "--record-from-hook" {
			return record(args[i+1:], stdin, stderr)
		}
	}
	return view(args, stdout, stderr)
}

// ---- the recorder (hook side) ----------------------------------------------

// record is called by the round's own SessionStart hook, so it is silent and
// always succeeds by contract. Two reasons, both structural: the hook's stdout
// is injected into the model's first turn (a stray line would become part of
// the spec the delegate reads), and a registry that cannot be written must not
// be able to fail a round whose progress it was only trying to describe.
// OUTSOURCE_TAIL_DEBUG=1 makes it talk, for when the reveal is what is broken.
func record(args []string, stdin io.Reader, stderr io.Writer) int {
	debug := os.Getenv("OUTSOURCE_TAIL_DEBUG") != ""
	note := func(format string, a ...any) {
		if debug {
			fmt.Fprintf(stderr, "outsource tail --record-from-hook: "+format+"\n", a...)
		}
	}
	var id, runsDir string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		id, args = args[0], args[1:]
	}
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "--runs-dir" {
			runsDir = args[i+1]
		}
	}
	// The hook runs inside the harness, which does not promise to pass the
	// launcher's environment through. The registry directory is therefore an
	// explicit argument, not an inherited OUTSOURCE_RUNS_DIR.
	if runsDir != "" {
		_ = os.Setenv("OUTSOURCE_RUNS_DIR", runsDir)
	}
	b, err := io.ReadAll(io.LimitReader(stdin, 1<<20))
	if err != nil {
		note("cannot read the hook payload: %v", err)
		return 0
	}
	var payload struct {
		TranscriptPath string `json:"transcript_path"`
		SessionID      string `json:"session_id"`
	}
	if err := json.Unmarshal(b, &payload); err != nil {
		note("hook payload is not JSON: %v", err)
		return 0
	}
	if payload.TranscriptPath == "" {
		note("hook payload carries no transcript_path")
		return 0
	}
	if err := runs.SetTrail(id, payload.TranscriptPath); err != nil {
		note("%v", err)
		return 0
	}
	note("recorded trail for run %s: %s", id, payload.TranscriptPath)
	return 0
}

// ---- the reader ------------------------------------------------------------

type viewOpts struct {
	sel        string
	follow     bool
	last       int
	width      int
	raw        bool
	all        bool
	maxSeconds int
}

func view(args []string, stdout, stderr io.Writer) int {
	o := viewOpts{last: 40, width: 160}
	need := func(i int) (string, bool) {
		if i+1 >= len(args) {
			fmt.Fprintf(stderr, "tail: %s needs a value\n", args[i])
			return "", false
		}
		return args[i+1], true
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "-h", "--help":
			fmt.Fprint(stdout, usage)
			return 0
		case "-f", "--follow":
			o.follow = true
		case "--raw":
			o.raw = true
		case "--all":
			o.all = true
		case "-n", "-w", "--max-seconds":
			v, ok := need(i)
			if !ok {
				return ExitUsage
			}
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 {
				fmt.Fprintf(stderr, "tail: %s wants a non-negative number, got: %s\n", a, v)
				return ExitUsage
			}
			switch a {
			case "-n":
				o.last = n
			case "-w":
				o.width = n
			default:
				o.maxSeconds = n
			}
			i++
		default:
			if strings.HasPrefix(a, "-") {
				fmt.Fprintf(stderr, "tail: unknown flag: %s\n", a)
				return ExitUsage
			}
			if o.sel != "" {
				fmt.Fprintf(stderr, "tail: one selector at a time, got %q and %q\n", o.sel, a)
				return ExitUsage
			}
			o.sel = a
		}
	}

	rec, err := runs.Resolve(o.sel)
	if err != nil {
		var amb *runs.Ambiguous
		if errorsAs(err, &amb) {
			fmt.Fprintf(stderr, "tail: %v — name one of these instead:\n", err)
			for _, c := range amb.Candidates {
				fmt.Fprintf(stderr, "  %s  %-16s %s·%s  %s\n", c.ID, c.Label, c.Provider, c.Harness, c.State())
			}
			return ExitUsage
		}
		fmt.Fprintf(stderr, "tail: %v\n", err)
		return ExitNoTrail
	}

	// A round that has not revealed its trail yet is a state, not an error:
	// claude-code reveals it when its first turn starts. Follow mode waits for
	// the reveal; a one-shot read says so and stops.
	if rec.Trail == "" {
		if !o.follow {
			fmt.Fprintf(stderr, "tail: run %s (%s) has not revealed a trail yet — it is recorded on the round's first turn. Retry, or use -f to wait.\n", rec.ID, rec.Label)
			return ExitNoTrail
		}
		waited := time.Now()
		for rec.Trail == "" {
			if rec.State() != runs.Running {
				fmt.Fprintf(stderr, "tail: run %s ended (%s) without ever revealing a trail; log=%s\n", rec.ID, rec.State(), rec.Log)
				return ExitNoTrail
			}
			if o.maxSeconds > 0 && time.Since(waited) > time.Duration(o.maxSeconds)*time.Second {
				fmt.Fprintf(stderr, "tail: waited %ds for run %s to reveal a trail; giving up\n", o.maxSeconds, rec.ID)
				return ExitNoTrail
			}
			time.Sleep(pollInterval)
			if r := runs.FindByID(rec.ID); r != nil {
				rec = r
			}
		}
	}

	format := rec.TrailFormat
	if !KnownFormat(format) {
		format = FormatLines
	}
	fmt.Fprintf(stdout, "── %s · %s·%s · %s · %s\n", rec.Label, rec.Provider, rec.Harness, rec.State(), rec.Trail)

	f := &feeder{path: rec.Trail}
	r := &renderer{format: format, width: o.width, all: o.all, raw: o.raw, tools: map[string]string{}}

	// First pass is a tail: only the last N entries, so a twenty-minute round
	// does not arrive as two thousand lines.
	lines, err := f.next()
	if err != nil {
		// A revealed path that does not exist yet is the reveal winning a race
		// with the first write, not a failure: the hook fires at session start
		// and the harness creates the file on its first turn. Follow mode waits
		// it out; a one-shot read says so.
		if os.IsNotExist(err) && o.follow {
			lines = nil
		} else {
			fmt.Fprintf(stderr, "tail: cannot read the trail %s: %v\n", rec.Trail, err)
			return ExitNoTrail
		}
	}
	rendered := r.render(lines)
	if o.last > 0 && len(rendered) > o.last {
		fmt.Fprintf(stdout, "… %d earlier entries not shown (-n 0 for all)\n", len(rendered)-o.last)
		rendered = rendered[len(rendered)-o.last:]
	}
	for _, l := range rendered {
		fmt.Fprintln(stdout, l)
	}
	if !o.follow {
		return 0
	}

	// Follow mode ends with the round: the registry record is the authority on
	// that, never a timeout. A `tail -f` that outlives its subject is the
	// zombie-shell class this repo has already been bitten by.
	started := time.Now()
	for {
		time.Sleep(pollInterval)
		lines, err := f.next()
		if err == nil {
			for _, l := range r.render(lines) {
				fmt.Fprintln(stdout, l)
			}
		}
		if followProbe != nil {
			followProbe()
		}
		cur := runs.FindByID(rec.ID)
		if cur == nil || cur.State() != runs.Running {
			// One last read, so the final turn is never lost to the poll gap.
			if lines, err := f.next(); err == nil {
				for _, l := range r.render(lines) {
					fmt.Fprintln(stdout, l)
				}
			}
			st := "gone from the registry"
			if cur != nil {
				st = string(cur.State())
				if cur.RC != "" {
					st += " rc=" + cur.RC
				}
			}
			fmt.Fprintf(stdout, "── round %s: %s\n", rec.ID, st)
			return 0
		}
		if o.maxSeconds > 0 && time.Since(started) > time.Duration(o.maxSeconds)*time.Second {
			fmt.Fprintf(stdout, "── --max-seconds %d reached; the round is still running\n", o.maxSeconds)
			return 0
		}
	}
}

// pollInterval is how often follow mode looks for new entries. A variable so a
// test can make it instant.
var pollInterval = 1 * time.Second

// followProbe is a seam, and it exists for one reason: the final drain below
// closes a window — bytes landing between the read and the registry check —
// that no amount of sleeping in a test can hit on purpose. Without a seam the
// drain would be unproven code, and an unproven guard is the class of thing
// that quietly stops working. Always nil outside the test that sets it.
var followProbe func()

// errorsAs is errors.As without the import, kept local so this file's only
// dependency is the registry.
func errorsAs(err error, target **runs.Ambiguous) bool {
	if a, ok := err.(*runs.Ambiguous); ok {
		*target = a
		return true
	}
	return false
}

// ---- incremental reading ---------------------------------------------------

// feeder hands out whole lines that appeared since the last call. A trail is
// appended to while it is read, so the last line is routinely a partial one:
// it is held back rather than parsed as truncated JSON.
type feeder struct {
	path    string
	offset  int64
	partial []byte
}

// maxLine is the point past which a line is reported rather than decoded. A
// single tool_result carrying a large file read can be megabytes; that is the
// line that would otherwise abort a bufio.Scanner outright.
const maxLine = 4 << 20

func (f *feeder) next() ([]string, error) {
	fh, err := os.Open(f.path)
	if err != nil {
		return nil, err
	}
	defer fh.Close()
	if _, err := fh.Seek(f.offset, io.SeekStart); err != nil {
		return nil, err
	}
	br := bufio.NewReaderSize(fh, 1<<16)
	var out []string
	for {
		chunk, err := br.ReadBytes('\n')
		f.offset += int64(len(chunk))
		if err != nil {
			// No newline yet: hold the tail for the next call.
			f.partial = append(f.partial, chunk...)
			break
		}
		line := chunk
		if len(f.partial) > 0 {
			line = append(f.partial, chunk...)
			f.partial = nil
		}
		line = trimEOL(line)
		if len(line) == 0 {
			continue
		}
		if len(line) > maxLine {
			out = append(out, fmt.Sprintf("(a %d-byte entry was too large to decode)", len(line)))
			continue
		}
		out = append(out, string(line))
	}
	return out, nil
}

func trimEOL(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r') {
		b = b[:len(b)-1]
	}
	return b
}

// ---- rendering -------------------------------------------------------------

type renderer struct {
	format string
	width  int
	all    bool
	raw    bool
	// tools maps a tool_use id to its name, so a result can say which call it
	// answers. The transcript only carries the id on the result side.
	tools map[string]string
}

func (r *renderer) render(lines []string) []string {
	if r.raw {
		return lines
	}
	var out []string
	for _, l := range lines {
		switch r.format {
		case FormatClaudeTranscript:
			out = append(out, r.claudeLine(l)...)
		case FormatOpencodeEvents:
			out = append(out, r.opencodeLine(l)...)
		default:
			out = append(out, r.clip(l))
		}
	}
	return out
}

func (r *renderer) clip(s string) string {
	s = strings.Join(strings.Fields(strings.ReplaceAll(s, "\n", " ")), " ")
	if r.width <= 0 {
		return s
	}
	ru := []rune(s)
	if len(ru) <= r.width {
		return s
	}
	return string(ru[:r.width]) + "…"
}

type transcriptLine struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	Message   struct {
		Role    string          `json:"role"`
		Model   string          `json:"model"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

type contentPart struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	Name      string          `json:"name"`
	ID        string          `json:"id"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	IsError   bool            `json:"is_error"`
	Content   json.RawMessage `json:"content"`
}

func (r *renderer) claudeLine(line string) []string {
	var t transcriptLine
	if json.Unmarshal([]byte(line), &t) != nil {
		return nil // a line this tool does not understand is not news
	}
	if t.Type != "assistant" && t.Type != "user" {
		return nil
	}
	var parts []contentPart
	if json.Unmarshal(t.Message.Content, &parts) != nil {
		// A plain string content is the user's own prompt — the spec. Not worth
		// echoing back at whoever wrote it.
		return nil
	}
	stamp := clock(t.Timestamp)
	var out []string
	for _, p := range parts {
		switch p.Type {
		case "text":
			if s := strings.TrimSpace(p.Text); s != "" {
				out = append(out, stamp+"💬 "+r.clip(s))
			}
		case "thinking":
			if r.all {
				if s := strings.TrimSpace(p.Thinking); s != "" {
					out = append(out, stamp+"🤔 "+r.clip(s))
				}
			}
		case "tool_use":
			if p.ID != "" {
				r.tools[p.ID] = p.Name
			}
			out = append(out, stamp+"🔧 "+r.clip(p.Name+" "+toolArg(p.Input)))
		case "tool_result":
			name := r.tools[p.ToolUseID]
			if name == "" {
				name = "tool"
			}
			switch {
			case p.IsError:
				out = append(out, stamp+"✗ "+r.clip(name+": "+flatten(p.Content)))
			case r.all:
				out = append(out, stamp+"↩ "+r.clip(name+": "+flatten(p.Content)))
			}
		}
	}
	return out
}

// toolArg is the one field of a tool call worth putting on a line. The order is
// the order a reader wants it in: what was run, then what was touched.
func toolArg(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return ""
	}
	for _, k := range []string{"command", "file_path", "url", "pattern", "path", "prompt", "description", "query"} {
		if v, ok := m[k]; ok {
			if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
				return s
			}
		}
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return "(" + strings.Join(keys, ",") + ")"
}

// flatten turns a tool result's content — a string, or a part array — into one
// line. Results are the shape that varies most between harness versions, so
// anything unrecognised is shown as its own JSON rather than dropped.
func flatten(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []contentPart
	if json.Unmarshal(raw, &parts) == nil {
		var b []string
		for _, p := range parts {
			if p.Text != "" {
				b = append(b, p.Text)
			}
		}
		if len(b) > 0 {
			return strings.Join(b, " ")
		}
	}
	return string(raw)
}

type opencodeEvent struct {
	Type string `json:"type"`
	Part struct {
		Type  string          `json:"type"`
		Text  string          `json:"text"`
		Tool  string          `json:"tool"`
		State json.RawMessage `json:"state"`
	} `json:"part"`
}

func (r *renderer) opencodeLine(line string) []string {
	var e opencodeEvent
	if json.Unmarshal([]byte(line), &e) != nil {
		return nil
	}
	switch {
	case strings.TrimSpace(e.Part.Text) != "":
		return []string{"💬 " + r.clip(e.Part.Text)}
	case e.Part.Tool != "":
		return []string{"🔧 " + r.clip(e.Part.Tool+" "+stateArg(e.Part.State))}
	}
	return nil
}

func stateArg(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return ""
	}
	if in, ok := m["input"]; ok {
		if b, err := json.Marshal(in); err == nil {
			return toolArg(b)
		}
	}
	if s, ok := m["status"].(string); ok {
		return s
	}
	return ""
}

// clock renders the entry's own timestamp as local wall time, which is what a
// reader compares against "when did it go quiet". An absent or unparseable
// timestamp renders as nothing rather than as a wrong time.
func clock(ts string) string {
	if ts == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return ""
	}
	return t.Local().Format("15:04:05") + " "
}
