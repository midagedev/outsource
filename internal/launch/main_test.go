package launch

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// selfReexecExit is the test binary's refusal when it is re-executed as the
// launcher (see TestMain).
const selfReexecExit = 3

// TestMain points the run registry at a throwaway directory for the whole
// package. A launch path registers its round before it can refuse or fail, so
// a test that forgets its own t.Setenv("OUTSOURCE_RUNS_DIR", …) writes records
// into the developer's real registry, owned by whatever session ran `go test`
// — measured 2026-09-20: one `go test ./internal/launch/` left 26 failed
// "rounds" in a live session's status line. Tests that set their own directory
// still win; this is the floor under the ones that do not.
func TestMain(m *testing.M) {
	// The launcher re-executes os.Executable() (the git guard hook, the
	// crushrc credential call, --detach). Under `go test` that binary is this
	// test binary, and it ignores a positional argument and reruns the whole
	// suite — which reaches the same re-exec again. Only the nesting guard
	// bounded that recursion; with OUTSOURCE_ALLOW_NESTED=1 set it did not
	// (2026-09-29: 681 launch.test and 336 orphaned children on one Mac).
	// `go test` passes only -test.* flags, so a positional first argument is a
	// re-exec: refuse it by name instead of running the suite.
	if len(os.Args) > 1 && !strings.HasPrefix(os.Args[1], "-") {
		fmt.Fprintf(os.Stderr, "launch tests: this test binary was re-executed as %q; a launcher path under test reached os.Executable(), and rerunning the suite from here recurses — refusing (exit %d)\n", os.Args[1:], selfReexecExit)
		os.Exit(selfReexecExit)
	}
	dir, err := os.MkdirTemp("", "outsource-launch-test-runs-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "launch tests: cannot create a registry dir:", err)
		os.Exit(2)
	}
	os.Setenv("OUTSOURCE_RUNS_DIR", filepath.Join(dir, "runs"))
	rc := m.Run()
	os.RemoveAll(dir)
	os.Exit(rc)
}

// TestSelfReexecIsRefused pins TestMain's refusal: the test binary re-executed
// the way the launcher does it (a positional subcommand) exits by name at once
// instead of rerunning the suite. The child runs in its own process group and
// the group is killed on timeout, so a regression is bounded to one test's
// worth of recursion rather than the machine.
func TestSelfReexecIsRefused(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(self, "guard")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err = <-done:
	case <-time.After(20 * time.Second):
		syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-done
		t.Fatal("re-executed test binary did not exit within 20 s: it is rerunning the suite")
	}
	ee, ok := err.(*exec.ExitError)
	if !ok || ee.ExitCode() != selfReexecExit {
		syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		t.Fatalf("re-exec exit = %v, want %d; stderr: %s", err, selfReexecExit, stderr.String())
	}
	if !strings.Contains(stderr.String(), "re-executed as") {
		t.Fatalf("refusal does not name itself; stderr: %s", stderr.String())
	}
}
