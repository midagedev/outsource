// Package telemetry records what this skill's tools were asked to do and how it
// went, to a local file, so the next round can be run better than the last one.
//
// LOCAL ONLY. Nothing here opens a socket. There is no endpoint, no upload, no
// identifier, and no background sender — the word "telemetry" usually implies all
// four, so it is worth saying plainly: this writes one line to a file under your
// own state directory and that is the entire mechanism. Disable it with
// OUTSOURCE_TELEMETRY=0.
//
// WHY it can be this cheap: the port gave every tool an exit-code vocabulary and
// put every tool behind one dispatcher. So the useful question — "what keeps going
// wrong, and is it the caller or the provider?" — is answerable from (tool, exit
// code, duration) alone:
//
//	64  the caller guessed a flag, or stated a contract the spec cannot satisfy
//	65  a spec that wants eyes was sent to a backend that has none
//	66  a round was refused because the plan could not finish it
//	69  the harness CLI is not installed
//	70  the model that answered was not the one requested
//	72  the round ran and its completion marker never appeared
//	124 a round was cut by its own ceiling
//	 2  (guard) a delegate tried a git command it is not allowed
//
// A rate on any of those is a finding about how rounds are being launched, which
// is the thing worth improving.
//
// WHAT IS NEVER RECORDED: flag values, except a small allowlist of enums that
// cannot carry anything private (harness, provider, git profile). No paths, no
// spec content, no stdin, no environment, no credential — not even a redacted
// one. Flag NAMES are recorded, because "which flags were passed" is the signal
// and "what they pointed at" is not. The run registry already holds paths for
// live rounds and is scoped to the machine; this file is meant to be safe to read
// months later without wondering what is in it.
package telemetry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// maxBytes caps the file. It rolls to .1 rather than truncating, so the most
// recent history always survives a roll and nothing grows without bound.
const maxBytes = 2 << 20

// Event is one line of the log.
type Event struct {
	TS      string            `json:"ts"`
	Tool    string            `json:"tool"`
	RC      int               `json:"rc"`
	MS      int64             `json:"ms"`
	Flags   []string          `json:"flags,omitempty"`
	Details map[string]string `json:"details,omitempty"`
}

// safeFlagValues are the only flags whose VALUE is recorded. Each is a closed set
// of words chosen by this repo, so none of them can carry a path, a secret, or
// anything a user typed freely.
var safeFlagValues = map[string]bool{
	"--harness":     true,
	"--provider":    true,
	"--git-profile": true,
}

var (
	mu      sync.Mutex
	details = map[string]string{}
)

// Note attaches a short reason to the event this process will write. Tools call it
// at the point they decide something went wrong, because the dispatcher can see
// the exit code but not why.
//
// Package-level state, which is normally worth avoiding — justified here because
// each of these processes serves exactly one invocation and exits. The
// alternative is threading a recorder through every function that might fail,
// which would be more code in more places for a facility that must never matter
// enough to complicate a call site.
func Note(key, value string) {
	mu.Lock()
	defer mu.Unlock()
	details[key] = value
}

func enabled() bool { return os.Getenv("OUTSOURCE_TELEMETRY") != "0" }

// Path is the log's location: beside the run registry, under the same state
// directory, so everything this skill remembers lives in one place.
func Path() string {
	if p := os.Getenv("OUTSOURCE_TELEMETRY_FILE"); p != "" {
		return p
	}
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			home = os.Getenv("HOME")
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "outsource", "telemetry.jsonl")
}

// flagNames extracts the flags a caller passed, without their values.
func flagNames(args []string) ([]string, map[string]string) {
	var names []string
	vals := map[string]string{}
	seen := map[string]bool{}
	for i, a := range args {
		// Past `--` the words belong to a command the tool runs (slot's
		// wrapped command, grok-run's extra grok flags), not to the tool.
		if a == "--" {
			break
		}
		if !strings.HasPrefix(a, "--") {
			continue
		}
		name := a
		if eq := strings.Index(a, "="); eq > 0 {
			name = a[:eq]
		}
		if !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
		if safeFlagValues[name] && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
			vals[strings.TrimPrefix(name, "--")] = args[i+1]
		}
	}
	return names, vals
}

// policy decides whether an invocation is worth a line.
//
// Two tools are far too frequent to log every call: the guard fires on every Bash
// tool call of every round, and the status line re-renders constantly. Logging
// their successes would bury everything else and make the file grow by megabytes
// a day. They record only the events that mean something — a block, or a failure.
func worthRecording(tool string, rc int) bool {
	switch tool {
	case "guard", "statusline":
		return rc != 0
	}
	return true
}

// Record appends one event. Every failure path here is swallowed: telemetry is
// bookkeeping, and a tool must never fail because its log could not be written.
func Record(tool string, args []string, rc int, started time.Time) {
	if !enabled() || !worthRecording(tool, rc) {
		return
	}
	names, vals := flagNames(args)
	mu.Lock()
	for k, v := range details {
		vals[k] = v
	}
	mu.Unlock()
	if len(vals) == 0 {
		vals = nil
	}
	ev := Event{
		TS:    time.Now().UTC().Format("2006-01-02T15:04:05Z"),
		Tool:  tool,
		RC:    rc,
		MS:    time.Since(started).Milliseconds(),
		Flags: names, Details: vals,
	}
	line, err := json.Marshal(ev)
	if err != nil {
		return
	}
	p := Path()
	if os.MkdirAll(filepath.Dir(p), 0o755) != nil {
		return
	}
	roll(p)
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	f.Write(append(line, '\n'))
}

// roll moves the file aside once it is large enough. One generation is kept: the
// point is a bounded window of recent history, not an archive.
func roll(p string) {
	fi, err := os.Stat(p)
	if err != nil || fi.Size() < maxBytes {
		return
	}
	_ = os.Rename(p, p+".1")
}
