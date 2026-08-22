// Package runs is the registry of delegated runs: who is running right now,
// on what, for how long, and how the last few ended.
//
// Why a written record rather than a `ps` grep at each call site: a launched
// round is invisible between "I started it" and "it printed a report", and
// that gap is where a lead loses track of which tracks are still alive, how
// long they have been running, and which one died without a report. `ps` can
// answer the first question and none of the others — a killed round leaves no
// process at all, so the interesting state (started, never finished) is
// exactly the state a process listing cannot represent.
//
// One record per run, key=value lines — the same shape as the launcher's
// <log>.rc completion sentinel, so it is greppable, diffable, and readable
// with no parser. This package is the only writer; every reader goes through
// it so the format has one owner.
package runs

import (
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Tunables. These were shell constants; the values and their reasoning are
// carried over unchanged.
const (
	// Finished records survive a day, then prune drops them.
	KeepSecondsDefault int64 = 86400
	// `line` also shows runs that ended this recently — an outcome is news
	// for ten minutes.
	RecentSeconds int64 = 600
	// Default for how long a running round may write nothing before it is
	// flagged. The axis is deliberately NOT elapsed time: measured across ten
	// delivered rounds, duration ran 13 minutes to 1h50m and tracked message
	// count almost linearly, so flagging on elapsed time flags exactly the
	// rounds that must not be disturbed while a round stuck in a loop at
	// minute three goes unnoticed. Progress separates them.
	DefaultStallSeconds int64 = 600
)

// Dir is where records live: $OUTSOURCE_RUNS_DIR, else
// ${XDG_STATE_HOME:-~/.local/state}/outsource/runs. Machine-wide on purpose —
// an orphan has to be findable from wherever you happen to be — with the
// ownership filter applied at the reading end instead.
func Dir() string {
	if d := os.Getenv("OUTSOURCE_RUNS_DIR"); d != "" {
		return d
	}
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			home = os.Getenv("HOME")
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "outsource", "runs")
}

// StallSeconds honours OUTSOURCE_RUN_STALL. A value that is not a positive
// integer falls back to the default rather than disabling the check: a typo in
// an env var must not silently turn off stall detection.
func StallSeconds() int64 {
	if v := os.Getenv("OUTSOURCE_RUN_STALL"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			return n
		}
	}
	return DefaultStallSeconds
}

// State is what a run can be in, and each is decided from the record plus the
// liveness of its pid.
type State string

const (
	// Running: no rc recorded, and the pid is alive.
	Running State = "running"
	// Orphan: no rc, and the pid is gone — the round died without finishing
	// (killed, machine slept, harness crashed). This is the state that makes
	// the registry worth keeping: nothing else on the machine still remembers
	// the round existed.
	Orphan State = "orphan"
	Done   State = "done"   // rc = 0
	Failed State = "failed" // rc != 0; the launcher's exit codes carry the reason
)

// Record is one delegated run. Every field is a single line in the file, so
// anything that could carry a newline is flattened on write. Nothing is ever
// eval'd: reads split on the first '=' and assign into these fields only.
type Record struct {
	ID             string
	Pid            string
	Label          string
	Provider       string
	Harness        string
	Model          string
	Cwd            string
	Spec           string
	Log            string
	ProgressDir    string
	OwnerSession   string
	OwnerClaudePid string
	StartedAt      string
	RC             string
	FinishedAt     string
	Session        string
	ModelActual    string
}

// sanitize flattens newlines so one value stays one line.
func sanitize(s string) string {
	return strings.NewReplacer("\n", " ", "\r", " ").Replace(s)
}

func atoi(s string) int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// isInt reports whether a field can be emitted as a JSON number. The shell
// version used str.lstrip("-").isdigit(), so a leading minus is allowed and
// nothing else is.
func isInt(s string) bool {
	if s == "" {
		return false
	}
	t := strings.TrimPrefix(s, "-")
	if t == "" {
		return false
	}
	for _, r := range t {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// Read loads one record file. A file whose id is missing is treated as
// unreadable, exactly as the shell version did — a half-written record must
// not become a run with an empty name.
func Read(path string) (*Record, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	r := &Record{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "=")
		if !ok {
			continue
		}
		// Later assignments win, which is what makes finish() an append
		// rather than a rewrite: the start fields are the launcher's record of
		// what it launched, and a finish must never be able to rewrite history.
		switch k {
		case "id":
			r.ID = v
		case "pid":
			r.Pid = v
		case "label":
			r.Label = v
		case "provider":
			r.Provider = v
		case "harness":
			r.Harness = v
		case "model":
			r.Model = v
		case "cwd":
			r.Cwd = v
		case "spec":
			r.Spec = v
		case "log":
			r.Log = v
		case "progressDir":
			r.ProgressDir = v
		case "ownerSession":
			r.OwnerSession = v
		case "ownerClaudePid":
			r.OwnerClaudePid = v
		case "startedAt":
			r.StartedAt = v
		case "rc":
			r.RC = v
		case "finishedAt":
			r.FinishedAt = v
		case "session":
			r.Session = v
		case "modelActual":
			r.ModelActual = v
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if r.ID == "" {
		return nil, fmt.Errorf("no id in %s", path)
	}
	return r, nil
}

// List returns every record oldest-first. The <startedAt>-<pid> name makes a
// plain sorted glob chronological, and that ordering is part of the contract:
// callers render in it.
func List() ([]*Record, error) {
	dir := Dir()
	ents, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // absent directory is "no runs", not an error
		}
		return nil, err
	}
	names := make([]string, 0, len(ents))
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".run") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	out := make([]*Record, 0, len(names))
	for _, n := range names {
		r, err := Read(filepath.Join(dir, n))
		if err != nil {
			continue // unreadable records are skipped, never fatal
		}
		out = append(out, r)
	}
	return out, nil
}

// Alive reports whether the process still exists. Signal 0 cannot distinguish
// "that pid exited" from "that pid exited and the number was reused". A reused
// pid can only make an orphan look alive, never the reverse, and the elapsed
// time printed next to it is the tell.
func Alive(pid string) bool {
	n, err := strconv.Atoi(strings.TrimSpace(pid))
	if err != nil || n <= 0 {
		return false
	}
	p, err := os.FindProcess(n)
	if err != nil {
		return false
	}
	return p.Signal(sysSignalZero) == nil
}

func (r *Record) State() State {
	if r.RC == "" {
		if Alive(r.Pid) {
			return Running
		}
		return Orphan
	}
	if r.RC == "0" {
		return Done
	}
	return Failed
}

// Elapsed is how long this run has been, or was, alive.
func (r *Record) Elapsed(now int64) int64 {
	end := now
	if r.FinishedAt != "" {
		end = atoi(r.FinishedAt)
	}
	s := end - atoi(r.StartedAt)
	if s < 0 {
		s = 0
	}
	return s
}

// Idle is seconds since this round last wrote anything, and whether that is
// knowable at all. Unknown is never reported as zero: "we cannot see progress"
// and "it just wrote" must not look the same.
//
// ProgressDir is usually a harness data directory (claude-code projects/,
// crush data/). The opencode harness's trail is the --log file itself: its
// JSONL is flushed per event while the round is still running (measured
// 2026-08-23). A regular file is therefore a valid trail, not an error.
func (r *Record) Idle(now int64) (int64, bool) {
	if r.ProgressDir == "" {
		return 0, false
	}
	newest, ok := newestMtime(r.ProgressDir)
	if !ok {
		return 0, false
	}
	s := now - newest
	if s < 0 {
		s = 0
	}
	return s, true
}

// Stalled is a running round with no sign of life for StallSeconds. Never true
// for a finished one, and never true when progress cannot be observed at all.
func (r *Record) Stalled(now int64) bool {
	if r.State() != Running {
		return false
	}
	idle, ok := r.Idle(now)
	return ok && idle >= StallSeconds()
}

// newestMtime is the newest modification time of a trail. A regular file is
// used as-is (opencode's --log). A directory is walked; both harness data
// directories hold a handful of files, so that stays cheap. os.Stat is
// portable, which retires the BSD-vs-GNU `stat` branch the shell version
// carried.
func newestMtime(path string) (int64, bool) {
	fi, err := os.Stat(path)
	if err != nil {
		return 0, false
	}
	if !fi.IsDir() {
		return fi.ModTime().Unix(), true
	}
	var newest int64
	found := false
	_ = filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // an unreadable subtree is not a fatal answer
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if m := info.ModTime().Unix(); m > newest {
			newest, found = m, true
		}
		return nil
	})
	return newest, found
}

// HarnessShort is what fits in a status line: claude-code renders as cc,
// opencode as oc, everything else as itself.
func HarnessShort(h string) string {
	switch h {
	case "claude-code":
		return "cc"
	case "opencode":
		return "oc"
	default:
		return h
	}
}

func nowUnix() int64 { return time.Now().Unix() }
