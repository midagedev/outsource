package runs

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/midagedev/outsource/internal/telemetry"
)

// cmdStop is `runs stop <label|id> [--reason T] [--any-owner] [--kill-after N]`:
// the documented way to stop a round.
//
// Why it exists: a TERM to the outsource-run wrapper does nothing, by design
// (internal/launch/signal_hold.go — the wrapper holds TERM so its sentinel
// survives a caller's timeout), and a lead that wanted a round stopped had to
// find the `claude -p` child by hand (2026-10-06). So this signals the harness
// child the registry recorded at spawn — never a pid found by searching — and
// only after checking that pid is still that child: alive, and its parent is
// the wrapper on record. The stop request is written to the record first, so
// the wrapper can put stopped_by=lead in the sentinel.
//
// Exit codes: 0 stopped (sentinel written) · 3 no such round · 64 usage,
// ambiguous, foreign or pid mismatch · 1 anything else. 3 and not this
// package's ExitNoSuchID (65): the codes are the stop contract's own.
func cmdStop(args []string, stdout, stderr io.Writer) int {
	if nestedStopRefusal(stderr) {
		return ExitUsage
	}
	var sel, reason string
	anyOwner := false
	killAfter := defaultStopKillAfter
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--reason", "--kill-after":
			if i+1 >= len(args) {
				fmt.Fprintf(stderr, "runs stop: %s needs a value\n", a)
				return ExitUsage
			}
			v := args[i+1]
			i++
			if a == "--reason" {
				reason = v
				continue
			}
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 {
				fmt.Fprintf(stderr, "runs stop: --kill-after wants a positive whole number of seconds, got: %s\n", v)
				return ExitUsage
			}
			killAfter = time.Duration(n) * time.Second
		case "--any-owner":
			anyOwner = true
		default:
			if strings.HasPrefix(a, "-") {
				fmt.Fprintf(stderr, "runs stop: unknown flag: %s\n", a)
				return ExitUsage
			}
			if sel != "" {
				fmt.Fprintf(stderr, "runs stop: one round at a time, got %q and %q\n", sel, a)
				return ExitUsage
			}
			sel = a
		}
	}
	if sel == "" {
		// Resolve("") means "the one running round", which is the right
		// default for watching and the wrong one for killing.
		fmt.Fprintln(stderr, "runs stop: name the round (a label, a run id or its --log path); stop never picks one for you")
		return ExitUsage
	}

	rec, err := Resolve(sel)
	if err != nil {
		if amb, ok := err.(*Ambiguous); ok {
			fmt.Fprintf(stderr, "runs stop: %v — name one of these by run id instead:\n", err)
			for _, c := range amb.Candidates {
				fmt.Fprintf(stderr, "  %s  %-16s %s·%s  %s\n", c.ID, c.Label, c.Provider, c.Harness, c.State())
			}
			return ExitUsage
		}
		fmt.Fprintf(stderr, "runs stop: %v\n", err)
		return exitStopNoSuch
	}

	session, claudePid := os.Getenv("CLAUDE_CODE_SESSION_ID"), os.Getenv("CLAUDE_PID")
	if !anyOwner && !callerOwns(rec, session, claudePid) {
		fmt.Fprintf(stderr, "runs stop: %s (%s) belongs to another session (owner %s) — not yours to stop; pass --any-owner if you mean to\n",
			rec.Label, rec.ID, orDash(rec.OwnerSession))
		return ExitUsage
	}
	by := session
	if by == "" {
		by = "cli"
	}

	st := rec.State()
	// A round waiting out a quota reset (track quota's --resume-on-reset) has
	// no child running, so the wrapper itself is the only process to stop.
	// Compared by name so this package keeps no constant of its own for it.
	if string(st) == "waiting" {
		return stopWaiting(rec, by, reason, stdout, stderr)
	}
	if st != Running {
		rc := ""
		if rec.RC != "" {
			rc = " rc=" + rec.RC
		}
		fmt.Fprintf(stderr, "runs stop: %s (%s) is not running (%s%s) — nothing to stop\n", rec.Label, rec.ID, st, rc)
		return exitStopFailed
	}

	child, why := verifiedChild(rec)
	if why != "" {
		fmt.Fprintf(stderr, "runs stop: refusing to signal %s (%s): %s\n", rec.Label, rec.ID, why)
		return ExitUsage
	}
	if err := RequestStop(rec.ID, by, reason, time.Now()); err != nil {
		fmt.Fprintf(stderr, "runs stop: could not record the stop request (%v) — nothing was signalled\n", err)
		return exitStopFailed
	}
	// TERM to the child alone: the claude CLI catches it and exits 143 on its
	// own (measured 2026-10-06, CLI 2.1.291; whether its tool subprocesses go
	// with it was not measured). KILL cannot be caught, so the escalation
	// goes to the child's whole process group — only when the child leads
	// its own group, as the launcher starts it.
	if err := syscall.Kill(child.pid, syscall.SIGTERM); err != nil {
		fmt.Fprintf(stderr, "runs stop: TERM to pid %d failed: %v\n", child.pid, err)
		return exitStopFailed
	}
	fmt.Fprintf(stdout, "runs stop: sent TERM to %s's harness child (pid %d)\n", rec.Label, child.pid)
	if !waitGone(child.pid, killAfter) {
		target, what := child.pid, fmt.Sprintf("pid %d", child.pid)
		if child.pgid == child.pid {
			target, what = -child.pid, fmt.Sprintf("process group %d", child.pid)
		}
		if err := syscall.Kill(target, syscall.SIGKILL); err != nil {
			fmt.Fprintf(stderr, "runs stop: the child outlived TERM by %s and KILL to %s failed: %v\n", killAfter, what, err)
			return exitStopFailed
		}
		fmt.Fprintf(stdout, "runs stop: the child was still alive %s after TERM — sent KILL to %s\n", killAfter, what)
	}
	return awaitSentinel(rec, stdout, stderr)
}

const (
	exitStopNoSuch = 3
	exitStopFailed = 1
)

// nestedStopRefusal is the stop side of the launcher's nestedLaunchRefusal
// (internal/launch/outsource.go), in the same shape and with the same
// override: a harness child carries OUTSOURCE_ROUND=1, and a delegate does
// not stop rounds — with --any-owner it could stop a sibling another lead is
// waiting on. Stopping is the lead's job, like launching. The variable names
// are spelled here because this package cannot import the launcher's.
func nestedStopRefusal(stderr io.Writer) bool {
	if os.Getenv("OUTSOURCE_ROUND") != "1" || os.Getenv("OUTSOURCE_ALLOW_NESTED") == "1" {
		return false
	}
	fmt.Fprintln(stderr, "runs stop: this process is already inside a delegated round (OUTSOURCE_ROUND=1), and a delegate does not stop rounds — finish your own spec and report; stopping a round is the lead's job. If a lead is deliberately nesting launchers, set OUTSOURCE_ALLOW_NESTED=1.")
	telemetry.Note("why", "nested stop refused: a delegate tried to stop a round")
	return true
}

// defaultStopKillAfter is how long a TERMed child gets before KILL.
const defaultStopKillAfter = 20 * time.Second

// stopSentinelWait bounds the wait for the wrapper's sentinel after the
// child is gone; stopPoll is the cadence of every wait here.
var (
	stopSentinelWait = 30 * time.Second
	stopPoll         = 100 * time.Millisecond
)

// callerOwns is the `⇄` test from the stop side. A record with no owner at
// all (launched outside Claude Code) is nobody's, so there is no other
// session to override. A caller with no identity of its own (a plain shell)
// owns only those.
func callerOwns(r *Record, session, claudePid string) bool {
	if r.OwnerSession == "" && r.OwnerClaudePid == "" {
		return true
	}
	if session == "" && claudePid == "" {
		return false
	}
	return filter{owner: session, ownerPid: claudePid}.mine(r)
}

type childProc struct{ pid, pgid int }

// verifiedChild returns the recorded harness child once it checks out, or the
// reason it does not. A pid alone is not proof: the child may have exited and
// the number been reused, so its parent must still be the wrapper on record.
func verifiedChild(r *Record) (childProc, string) {
	pid := int(atoi(r.ChildPid))
	if pid <= 0 {
		return childProc{}, "no harness child pid on record (launched by an older outsource-run, or by grok-run) — there is no verified pid to signal"
	}
	if !Alive(r.ChildPid) {
		return childProc{}, fmt.Sprintf("its harness child (pid %d) has already exited; the wrapper (pid %s) is finishing its paperwork — watch %s.rc", pid, r.Pid, orDash(r.Log))
	}
	ppid, pgid, err := procParent(pid)
	if err != nil {
		return childProc{}, fmt.Sprintf("cannot read pid %d's parent: %v", pid, err)
	}
	if wrapper := int(atoi(r.Pid)); ppid != wrapper {
		return childProc{}, fmt.Sprintf("pid %d's parent is %d, not the wrapper (pid %s) on record — that pid is no longer this round's child", pid, ppid, r.Pid)
	}
	return childProc{pid: pid, pgid: pgid}, ""
}

// procParent reads a pid's parent and process group through ps(1), which
// answers the same way on macOS and Linux without a syscall package.
func procParent(pid int) (ppid, pgid int, err error) {
	out, err := exec.Command("ps", "-o", "ppid=,pgid=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0, 0, fmt.Errorf("ps: %v", err)
	}
	f := strings.Fields(string(out))
	if len(f) != 2 {
		return 0, 0, fmt.Errorf("ps printed %q", strings.TrimSpace(string(out)))
	}
	ppid, err1 := strconv.Atoi(f[0])
	pgid, err2 := strconv.Atoi(f[1])
	if err1 != nil || err2 != nil {
		return 0, 0, fmt.Errorf("ps printed %q", strings.TrimSpace(string(out)))
	}
	return ppid, pgid, nil
}

// waitGone polls until pid is gone or d passes; true when it is gone.
func waitGone(pid int, d time.Duration) bool {
	deadline := time.Now().Add(d)
	p := strconv.Itoa(pid)
	for Alive(p) {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(stopPoll)
	}
	return true
}

// stopWaiting stops a round with no child running: TERM to the wrapper, whose
// wait loop ends on it.
func stopWaiting(rec *Record, by, reason string, stdout, stderr io.Writer) int {
	wrapper := int(atoi(rec.Pid))
	if wrapper <= 0 || !Alive(rec.Pid) {
		fmt.Fprintf(stderr, "runs stop: %s (%s) is waiting, but its wrapper (pid %s) is gone\n", rec.Label, rec.ID, rec.Pid)
		return exitStopFailed
	}
	if err := RequestStop(rec.ID, by, reason, time.Now()); err != nil {
		fmt.Fprintf(stderr, "runs stop: could not record the stop request (%v) — nothing was signalled\n", err)
		return exitStopFailed
	}
	if err := syscall.Kill(wrapper, syscall.SIGTERM); err != nil {
		fmt.Fprintf(stderr, "runs stop: TERM to the wrapper (pid %d) failed: %v\n", wrapper, err)
		return exitStopFailed
	}
	fmt.Fprintf(stdout, "runs stop: %s is waiting with no child running — sent TERM to its wrapper (pid %d)\n", rec.Label, wrapper)
	return awaitSentinel(rec, stdout, stderr)
}

// awaitSentinel waits for the wrapper's <log>.rc and prints its rc= line. A
// round launched without --log has no sentinel; its registry rc is the answer.
func awaitSentinel(rec *Record, stdout, stderr io.Writer) int {
	deadline := time.Now().Add(stopSentinelWait)
	for {
		if rec.Log != "" {
			if b, err := os.ReadFile(rec.Log + ".rc"); err == nil {
				if line := rcLine(b); line != "" {
					fmt.Fprintf(stdout, "runs stop: %s stopped — %s.rc: %s\n", rec.Label, rec.Log, line)
					return 0
				}
			}
		} else if cur := FindByID(rec.ID); cur != nil && cur.RC != "" {
			fmt.Fprintf(stdout, "runs stop: %s stopped — registry: rc=%s (no --log, so no sentinel)\n", rec.Label, cur.RC)
			return 0
		}
		if time.Now().After(deadline) {
			where := "the registry rc"
			if rec.Log != "" {
				where = rec.Log + ".rc"
			}
			fmt.Fprintf(stderr, "runs stop: signalled, but no %s within %s — the wrapper (pid %s) may still be finishing; check `outsource runs`\n", where, stopSentinelWait, rec.Pid)
			return exitStopFailed
		}
		time.Sleep(stopPoll)
	}
}

func rcLine(b []byte) string {
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, "rc=") {
			return l
		}
	}
	return ""
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
