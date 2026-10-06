package runs

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

// `runs stop` refuses everything it cannot verify, before any signal. Each
// case below starts its own `sleep` processes (the only pids it may touch) and
// asserts the refusal AND that the would-be target is still alive afterwards.
// The live stop itself is exercised through the real launcher in
// internal/launch/voice_test.go.

// sleeper starts a process this test owns and kills it at the end.
func sleeper(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	return cmd.Process.Pid
}

// startRecord registers a round the way the launcher does and returns its id.
func startRecord(t *testing.T, label string, pid int, owner string) string {
	t.Helper()
	var out, errb bytes.Buffer
	args := []string{"start", "--pid", strconv.Itoa(pid), "--label", label, "--provider", "zai",
		"--harness", "claude-code", "--log", filepath.Join(t.TempDir(), label+".log")}
	if owner != "" {
		args = append(args, "--owner", owner)
	}
	if rc := Main(args, &out, &errb); rc != 0 {
		t.Fatalf("runs start rc=%d: %s", rc, errb.String())
	}
	return strings.TrimSpace(out.String())
}

func stopRig(t *testing.T) {
	t.Helper()
	t.Setenv("OUTSOURCE_RUNS_DIR", filepath.Join(t.TempDir(), "runs"))
	t.Setenv("CLAUDE_CODE_SESSION_ID", "me")
	t.Setenv("CLAUDE_PID", "")
	// A round's shell exports these; the suite must answer the same inside
	// one (the nested refusal has its own test).
	t.Setenv("OUTSOURCE_ROUND", "")
	t.Setenv("OUTSOURCE_ALLOW_NESTED", "")
}

func stop(args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	rc := Main(append([]string{"stop"}, args...), &out, &errb)
	return rc, out.String(), errb.String()
}

func alive(t *testing.T, pid int) {
	t.Helper()
	if syscall.Kill(pid, 0) != nil {
		t.Fatalf("pid %d is gone — a refusal signalled it", pid)
	}
}

// A label two live rounds share is refused with both candidates named.
//
// FAIL-first: with the Ambiguous branch removed, the error falls through to
// "no such round" and exits 3.
func TestStopRefusesAnAmbiguousLabel(t *testing.T) {
	stopRig(t)
	a, b := sleeper(t), sleeper(t)
	idA := startRecord(t, "twin", a, "me")
	idB := startRecord(t, "twin", b, "me")
	rc, _, errb := stop("twin")
	if rc != ExitUsage || !strings.Contains(errb, idA) || !strings.Contains(errb, idB) {
		t.Fatalf("rc=%d, want 64 naming %s and %s; stderr:\n%s", rc, idA, idB, errb)
	}
	alive(t, a)
	alive(t, b)
}

// Another session's round is refused unless --any-owner; with it, the next
// check — the child's parent — still has to pass, and here it does not (the
// recorded child is this test's sleep, not the wrapper's), so nothing is
// signalled either way.
//
// FAIL-first: with callerOwns always true, the first call gets past ownership
// and refuses on the parent instead, so the "another session" check fails;
// with the parent check removed, the --any-owner call records a stop, TERMs
// the sleep and exits 1 after its sentinel wait instead of refusing with 64.
func TestStopRefusesAForeignRoundAndAChildOfAnotherParent(t *testing.T) {
	stopRig(t)
	wrapper, child := sleeper(t), sleeper(t)
	id := startRecord(t, "foreign", wrapper, "someone-else")
	if err := SetChildPid(id, child); err != nil {
		t.Fatal(err)
	}
	rc, _, errb := stop("foreign")
	if rc != ExitUsage || !strings.Contains(errb, "belongs to another session") {
		t.Fatalf("foreign: rc=%d, want 64 'belongs to another session'; stderr:\n%s", rc, errb)
	}
	rc, _, errb = stop("foreign", "--any-owner")
	want := "parent is " + strconv.Itoa(os.Getpid()) + ", not the wrapper (pid " + strconv.Itoa(wrapper) + ")"
	if rc != ExitUsage || !strings.Contains(errb, want) {
		t.Fatalf("--any-owner: rc=%d, want 64 %q; stderr:\n%s", rc, want, errb)
	}
	alive(t, child)
	alive(t, wrapper)
	if r := FindByID(id); r.StopRequested != "" {
		t.Fatalf("a refused stop must record no request, got stopRequested=%s", r.StopRequested)
	}
}

// The rest of the refusal table: no selector, no such round, a round that is
// not running, and a running round with no recorded child. FAIL-first: with
// the no-child branch removed, the refusal names "pid 0 has already exited"
// instead of the missing record.
func TestStopRefusalTable(t *testing.T) {
	stopRig(t)
	if rc, _, errb := stop(); rc != ExitUsage || !strings.Contains(errb, "name the round") {
		t.Fatalf("no selector: rc=%d stderr=%s", rc, errb)
	}
	if rc, _, _ := stop("nope"); rc != exitStopNoSuch {
		t.Fatalf("no such round: rc=%d, want 3", rc)
	}
	// A pid that is certainly gone: one this test started and reaped.
	gone := exec.Command("true")
	if err := gone.Run(); err != nil {
		t.Fatal(err)
	}
	startRecord(t, "dead", gone.Process.Pid, "me")
	if rc, _, errb := stop("dead"); rc != exitStopFailed || !strings.Contains(errb, "is not running") {
		t.Fatalf("not running: rc=%d stderr=%s", rc, errb)
	}
	live := sleeper(t)
	startRecord(t, "nochild", live, "me")
	if rc, _, errb := stop("nochild"); rc != ExitUsage || !strings.Contains(errb, "no harness child pid on record") {
		t.Fatalf("no child: rc=%d stderr=%s", rc, errb)
	}
	alive(t, live)
	if rc, _, errb := stop("nochild", "--kill-after", "0"); rc != ExitUsage || !strings.Contains(errb, "--kill-after") {
		t.Fatalf("bad --kill-after: rc=%d stderr=%s", rc, errb)
	}
}

// A finished row says how it ended when a lead stopped it or an outside
// signal killed it: ■ and ✗ on the one-line view, the same words after the
// rc in the full listing. An ordinary failure keeps ❌ rc=N.
//
// FAIL-first: with cmdLine's ending() guard disabled, the line reads
// "❌halted rc=143" and the ■ check fails.
func TestFinishedRowsRenderStopAndExternalKill(t *testing.T) {
	stopRig(t)
	gone := exec.Command("true")
	if err := gone.Run(); err != nil {
		t.Fatal(err)
	}
	mk := func(label string, kv ...string) {
		id := startRecord(t, label, gone.Process.Pid, "")
		if len(kv) > 0 {
			if err := appendFields(id, kv...); err != nil {
				t.Fatal(err)
			}
		}
		var errb bytes.Buffer
		if rc := Main([]string{"finish", id, "--rc", "143"}, &errb, &errb); rc != 0 {
			t.Fatal(errb.String())
		}
	}
	mk("halted", "stopRequested", "2026-10-06T08:00:00Z", "stopBy", "me", "stopReason", "wrong premise",
		"harnessSignal", "TERM", "signalSource", "lead-stop")
	mk("shot", "harnessSignal", "TERM", "signalSource", "external")
	mk("plain")
	var line, list, errb bytes.Buffer
	Main([]string{"line"}, &line, &errb)
	for _, want := range []string{"■halted stopped", "✗shot TERM ext", "❌plain rc=143"} {
		if !strings.Contains(line.String(), want) {
			t.Fatalf("runs line is missing %q: %s", want, line.String())
		}
	}
	Main([]string{"list"}, &list, &errb)
	for _, want := range []string{"rc=143 ■stopped — wrong premise", "rc=143 ✗TERM ext"} {
		if !strings.Contains(list.String(), want) {
			t.Fatalf("runs list is missing %q:\n%s", want, list.String())
		}
	}
}

// childPid is a last-wins append, not a reveal: a resumed spawn records a new
// child and that one is the answer (appendReveal would park it as a conflict
// and leave the dead first child as the target). FAIL-first: with SetChildPid
// written through appendReveal, childPid stays "111".
func TestChildPidLastSpawnWins(t *testing.T) {
	stopRig(t)
	id := startRecord(t, "respawn", os.Getpid(), "me")
	for _, pid := range []int{111, 222} {
		if err := SetChildPid(id, pid); err != nil {
			t.Fatal(err)
		}
	}
	if r := FindByID(id); r.ChildPid != "222" {
		t.Fatalf("childPid=%q, want the latest spawn 222", r.ChildPid)
	}
}

// A new record is owner-only: it is where a claude-code round's lead token
// lands (the default registry is under a home every local Mac account can
// read through group staff). FAIL-first: with cmdStart back at 0644 the mode
// reads -rw-r--r--.
func TestRecordsAreOwnerOnly(t *testing.T) {
	stopRig(t)
	id := startRecord(t, "mode", os.Getpid(), "me")
	fi, err := os.Stat(filepath.Join(Dir(), id+".run"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("new record mode %v, want -rw-------", fi.Mode().Perm())
	}
}

// Inside a delegated round (OUTSOURCE_ROUND=1, as every harness child has it)
// `runs stop` refuses before it resolves anything: a delegate does not stop
// rounds. OUTSOURCE_ALLOW_NESTED=1 lifts it, as it does for launching.
//
// FAIL-first: with the nestedStopRefusal call removed, the first stop gets as
// far as the missing child and refuses with "no harness child pid" instead.
func TestStopRefusesInsideADelegatedRound(t *testing.T) {
	stopRig(t)
	live := sleeper(t)
	id := startRecord(t, "sibling", live, "")
	t.Setenv("OUTSOURCE_ROUND", "1")
	rc, _, errb := stop("sibling", "--any-owner")
	if rc != ExitUsage || !strings.Contains(errb, "a delegate does not stop rounds") {
		t.Fatalf("rc=%d, want 64 'a delegate does not stop rounds'; stderr:\n%s", rc, errb)
	}
	alive(t, live)
	if r := FindByID(id); r.StopRequested != "" {
		t.Fatalf("a refused stop must record no request, got stopRequested=%s", r.StopRequested)
	}
	t.Setenv("OUTSOURCE_ALLOW_NESTED", "1")
	if rc, _, errb := stop("sibling"); rc != ExitUsage || !strings.Contains(errb, "no harness child pid") {
		t.Fatalf("with OUTSOURCE_ALLOW_NESTED=1 the stop must reach its own checks: rc=%d stderr=%s", rc, errb)
	}
	alive(t, live)
}

// A registry directory runs start creates is owner-only; one that already
// exists keeps whatever mode it had (nothing chmods a directory it found).
//
// FAIL-first: with cmdStart's MkdirAll back at 0755, the fresh directory's
// permissions read -rwxr-xr-x.
func TestRegistryDirIsCreatedOwnerOnly(t *testing.T) {
	stopRig(t)
	startRecord(t, "fresh", os.Getpid(), "me")
	fi, err := os.Stat(Dir())
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o700 {
		t.Fatalf("new registry dir mode %v, want -rwx------", fi.Mode().Perm())
	}
	existing := filepath.Join(t.TempDir(), "old-runs")
	if err := os.Mkdir(existing, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(existing, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OUTSOURCE_RUNS_DIR", existing)
	startRecord(t, "old", os.Getpid(), "me")
	if fi, _ := os.Stat(existing); fi.Mode().Perm() != 0o755 {
		t.Fatalf("an existing registry dir was changed to %v", fi.Mode().Perm())
	}
}
