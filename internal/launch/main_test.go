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

	"github.com/midagedev/outsource/internal/config"
)

// selfReexecExit is the test binary's refusal when it is re-executed as the
// launcher (see TestMain).
const selfReexecExit = 3

// testmainEnvKey is the test binary's own recursion bound. TestMain sets it
// in its own environment, so every child the launcher re-executes inherits it
// through os.Environ() and refuses on sight — whatever argv the child was
// given, including a first argument that starts with '-' or none at all.
const testmainEnvKey = "OUTSOURCE_LAUNCH_TESTMAIN"

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
	// suite — which reaches the same re-exec again (2026-09-29: 681 launch.test
	// and 336 orphaned children on one Mac when the bound was disabled). Two
	// bounds, in this order:
	//
	// The marker first: it does not depend on argv shape at all, so it also
	// catches a re-exec whose first argument starts with '-' — the shape `go
	// test` itself uses, which the positional refusal below deliberately
	// lets through.
	if os.Getenv(testmainEnvKey) != "" {
		fmt.Fprintf(os.Stderr, "launch tests: this test binary was re-executed with %s set; a launcher path under test reached os.Executable(), and rerunning the suite from here recurses — refusing (exit %d)\n", testmainEnvKey, selfReexecExit)
		os.Exit(selfReexecExit)
	}
	os.Setenv(testmainEnvKey, "1")
	// Then the positional refusal, which names the subcommand that re-executed
	// us (guard, tail, credential, git-shim, grok-run/outsource-run via
	// --detach) — evidence the marker's message does not carry.
	if len(os.Args) > 1 && !strings.HasPrefix(os.Args[1], "-") {
		fmt.Fprintf(os.Stderr, "launch tests: this test binary was re-executed as %q; a launcher path under test reached os.Executable(), and rerunning the suite from here recurses — refusing (exit %d)\n", os.Args[1:], selfReexecExit)
		os.Exit(selfReexecExit)
	}
	// A delegated round's shell exports these (the launcher marks every
	// harness child), and 13 tests refused through the nesting guard before
	// the behaviour they assert (measured 2026-10-06) — `go test
	// ./internal/launch/` must give the same answer inside a round as in the
	// lead's shell. Unsetting OUTSOURCE_ROUND here removes the old recursion
	// bound, which is why the marker above exists: the bound is replaced, not
	// dropped. Tests that want one of these markers set it themselves with
	// t.Setenv (restored automatically when they end).
	os.Unsetenv("OUTSOURCE_ROUND")
	os.Unsetenv("OUTSOURCE_DETACHED")
	os.Unsetenv("OUTSOURCE_ALLOW_NESTED")
	dir, err := os.MkdirTemp("", "outsource-launch-test-runs-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "launch tests: cannot create a registry dir:", err)
		os.Exit(2)
	}
	os.Setenv("OUTSOURCE_RUNS_DIR", filepath.Join(dir, "runs"))
	// A private TMPDIR too: a launch without --config-dir writes the shared
	// $TMPDIR/outsource-glm-cfg, and a test once rewrote the real crushrc
	// there (measured 2026-10-06). TestTempDirIsPrivate pins it.
	if err := os.MkdirAll(filepath.Join(dir, "tmp"), 0o700); err != nil {
		fmt.Fprintln(os.Stderr, "launch tests: cannot create a private TMPDIR:", err)
		os.Exit(2)
	}
	os.Setenv("TMPDIR", filepath.Join(dir, "tmp"))
	// The user's config file must not reach a launch test either (the same
	// class as the registry and TMPDIR floors above): a developer's real
	// ~/.config/outsource/config.json, or an OUTSOURCE_CONFIG inherited from
	// the caller's shell, would disable providers and swap default models
	// under the tests. It points at a path nothing creates — a missing file is
	// an empty config — so every test starts from no user choices unless it
	// sets its own with t.Setenv. TestConfigFileIsolationFromTheUsersOwn pins
	// it; FAIL-first: without this line, a temp HOME whose config disables zai
	// turns zai launch tests red.
	os.Setenv("OUTSOURCE_CONFIG", filepath.Join(dir, "absent-config", "config.json"))
	// No harness CLI is reachable from a launch test (pinHarnessFreePath).
	if msg := pinHarnessFreePath(); msg != "" {
		fmt.Fprintln(os.Stderr, msg)
		os.RemoveAll(dir)
		os.Exit(2)
	}
	rc := m.Run()
	os.RemoveAll(dir)
	os.Exit(rc)
}

// reexecRefused runs this test binary the way a launcher path under test does
// (os.Executable() plus the given argv and environment) and fails the test
// unless it refuses fast with selfReexecExit and stderr containing want. The
// child runs in its own process group and the group is killed on timeout, so
// a regression is bounded to one test's worth of recursion rather than the
// machine.
func reexecRefused(t *testing.T, argv, env []string, want string) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(self, argv...)
	cmd.Env = env
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
	if !strings.Contains(stderr.String(), want) {
		t.Fatalf("refusal does not contain %q; stderr: %s", want, stderr.String())
	}
}

// withoutTestmainEnvKey returns env minus the TestMain re-exec marker, for the
// one test that deliberately re-executes this binary and expects the
// positional refusal to answer: the parent's marker is inherited otherwise
// and refuses first, at the marker, hiding the check under test.
func withoutTestmainEnvKey(env []string) []string {
	prefix := testmainEnvKey + "="
	out := make([]string, 0, len(env))
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			continue
		}
		out = append(out, e)
	}
	return out
}

// TestSelfReexecIsRefused pins TestMain's positional refusal: the test binary
// re-executed the way the launcher does it (a positional subcommand) exits by
// name at once instead of rerunning the suite. The marker is stripped from
// the child's environment so the positional check is the one under test.
func TestSelfReexecIsRefused(t *testing.T) {
	reexecRefused(t, []string{"guard"}, withoutTestmainEnvKey(os.Environ()), "re-executed as")
}

// TestSelfReexecMarkerIsRefused pins the argv-shape-independent bound: the
// test binary re-executed with only a -test.* flag (the shape the positional
// refusal deliberately lets through) and the TestMain marker inherited
// through the environment must still refuse instead of running the suite.
func TestSelfReexecMarkerIsRefused(t *testing.T) {
	reexecRefused(t, []string{"-test.run=XXX_none"}, os.Environ(), testmainEnvKey)
}

// TestTempDirIsPrivate pins TestMain's private TMPDIR, so no launch test can
// write the shared $TMPDIR/outsource-glm-cfg (a test once rewrote the real
// crushrc there, 2026-10-06). FAIL-first: without the Setenv in TestMain,
// os.TempDir() is the user's own temp dir.
func TestTempDirIsPrivate(t *testing.T) {
	runsDir := os.Getenv("OUTSOURCE_RUNS_DIR")
	want := filepath.Join(filepath.Dir(runsDir), "tmp")
	if got := os.TempDir(); filepath.Clean(got) != want {
		t.Fatalf("os.TempDir() = %q, want the package's private %q", got, want)
	}
}

// TestConfigFileIsolationFromTheUsersOwn pins TestMain's config floor: the
// user config every launch test reads is a path inside the package's private
// directory that does not exist, never the developer's ~/.config/outsource/
// config.json. FAIL-first: without the Setenv in TestMain, OUTSOURCE_CONFIG is
// empty here (or the caller's own) and the config resolves to the real one.
func TestConfigFileIsolationFromTheUsersOwn(t *testing.T) {
	private := filepath.Dir(os.Getenv("OUTSOURCE_RUNS_DIR"))
	path, source := config.ResolvePath()
	if source != "OUTSOURCE_CONFIG" || !strings.HasPrefix(path, private+string(filepath.Separator)) {
		t.Fatalf("the launch tests' config is %s (source %s), want a path under the package's private %s", path, source, private)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("the launch tests' config %s must not exist (stat: %v): a missing file is the empty config every test starts from", path, err)
	}
}
