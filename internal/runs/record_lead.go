package runs

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// The writers behind a lead's voice and its stop lever. Each is an APPEND,
// like finish() and appendReveal: Read takes the last assignment of a key, so
// a later value wins without ever rewriting what was there, and the panel —
// which reads every record every 5 s — never sees a half-truncated file.
// Unlike appendReveal these keys may legitimately change: a resumed spawn has
// a new child, so a second childPid is the answer, not an intruder.

// appendFields writes key=value lines to one record in a single write, so a
// reader sees all of them or none of them.
func appendFields(id string, kv ...string) error {
	if id == "" {
		return fmt.Errorf("appendFields needs a run id")
	}
	if len(kv)%2 != 0 {
		return fmt.Errorf("appendFields wants key/value pairs, got %d values", len(kv))
	}
	file, err := recordPath(id)
	if err != nil {
		return err
	}
	var b strings.Builder
	for i := 0; i < len(kv); i += 2 {
		fmt.Fprintf(&b, "%s=%s\n", kv[i], sanitize(kv[i+1]))
	}
	f, err := os.OpenFile(file, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(b.String())
	return err
}

// SetLead records who launched a claude-code round and the token its prompt
// names. The token is a credential (a note carrying it gets spec authority),
// so it only ever lands in a record file that is readable by its owner alone:
// records are created 0600 (cmdStart), and this chmods again in case the
// record came from an older binary that created it 0644. Measured before
// this change (2026-10-06): records -rw-r--r-- in a drwxr-xr-x directory
// under a drwxr-x--- home whose group is staff — every local account on a
// Mac — so any local user could have read a token stored there.
func SetLead(id, socket, token string) error {
	file, err := recordPath(id)
	if err != nil {
		return err
	}
	if err := os.Chmod(file, 0o600); err != nil {
		return err // never write the token into a file we could not close off
	}
	return appendFields(id, "leadSocket", socket, "leadToken", token)
}

// SetChildPid records the harness child of a spawn. Called on every spawn, so
// after a resume the record names the live child, not the first one.
func SetChildPid(id string, pid int) error {
	return appendFields(id, "childPid", strconv.Itoa(pid))
}

// RequestStop records that someone asked this round to stop, BEFORE the
// signal is sent: the wrapper reads it once its child has exited, and a
// request written after the signal could lose that race.
func RequestStop(id, by, reason string, at time.Time) error {
	return appendFields(id,
		"stopRequested", at.UTC().Format("2006-01-02T15:04:05Z"),
		"stopBy", by,
		"stopReason", reason)
}

// SetEnding records how the harness child ended when a signal ended it, and
// where the signal came from. Written by the wrapper before `runs finish`
// appends the rc, so no reader ever sees an rc without its source.
func SetEnding(id, signal, source string) error {
	return appendFields(id, "harnessSignal", signal, "signalSource", source)
}

// ending is how a finished row says it was stopped on purpose or killed
// from outside, rather than a bare rc: ■ stopped when a lead asked
// (`runs stop`), ✗ <SIG> ext when a signal nobody on record sent ended the
// harness. ok is false for every other ending — the rc carries those.
func ending(r *Record) (glyph, word string, ok bool) {
	switch {
	case r.StopRequested != "":
		return "■", "stopped", true
	case r.HarnessSignal != "" && r.SignalSource == "external":
		return "✗", r.HarnessSignal + " ext", true
	}
	return "", "", false
}
