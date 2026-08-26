package launch

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --detach must not swallow usage errors: everything checkable before the
// re-exec (here: a done-marker the spec never contains) still fails
// synchronously on the caller's terminal.
func TestDetachKeepsUsageErrorsSynchronous(t *testing.T) {
	dir := t.TempDir()
	spec := filepath.Join(dir, "spec.md")
	if err := os.WriteFile(spec, []byte("do the thing\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A fake grok on PATH so LookPath (which precedes the marker check) passes.
	fake := filepath.Join(dir, "grok")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	rc := GrokMain([]string{
		"--detach",
		"--cwd", dir, "--spec", spec, "--log", filepath.Join(dir, "run.ndjson"),
		"--done-marker", "DONE-NEVER-IN-SPEC",
	}, io.Discard, io.Discard)
	if rc != ExitUsage {
		t.Fatalf("want ExitUsage (%d) before any detach, got %d", ExitUsage, rc)
	}
	if _, err := os.Stat(filepath.Join(dir, "run.ndjson.rc")); err == nil {
		t.Fatal("a refused launch must not write a sentinel")
	}
}

func TestNonTTYForegroundRefusal(t *testing.T) {
	if stdinIsTerminal() {
		t.Skip("stdin is a terminal; refusal only fires on non-TTY")
	}
	dir := t.TempDir()
	spec := filepath.Join(dir, "spec.md")
	if err := os.WriteFile(spec, []byte("do the thing\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	rc := GrokMain([]string{
		"--cwd", dir, "--spec", spec, "--log", filepath.Join(dir, "run.ndjson"),
	}, io.Discard, &stderr)
	if rc != ExitUsage {
		t.Fatalf("want ExitUsage (%d) on non-TTY without --detach/--foreground, got %d stderr=%s", ExitUsage, rc, stderr.String())
	}
	msg := stderr.String()
	for _, want := range []string{"--detach", "--foreground", "2026-08-22", "30-min"} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal must name %q; got %s", want, msg)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "run.ndjson.rc")); err == nil {
		t.Fatal("a refused launch must not write a sentinel")
	}
}

func TestForegroundOptOutAllowsNonTTY(t *testing.T) {
	if stdinIsTerminal() {
		t.Skip("stdin is a terminal; this covers the non-TTY --foreground opt-out")
	}
	dir := t.TempDir()
	spec := filepath.Join(dir, "spec.md")
	if err := os.WriteFile(spec, []byte("do the thing\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fake := filepath.Join(dir, "grok")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nprintf '{\"type\":\"text\",\"data\":\"ok\"}\\n'\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GROK_RUN_STARTUP_GRACE", "2")
	t.Setenv("OUTSOURCE_RUNS_DIR", filepath.Join(dir, "runs"))
	rc := GrokMain([]string{
		"--foreground",
		"--cwd", dir, "--spec", spec, "--log", filepath.Join(dir, "run.ndjson"),
		"--label", "fg-opt-out",
	}, io.Discard, io.Discard)
	if rc != 0 {
		t.Fatalf("--foreground on non-TTY must proceed, got rc=%d", rc)
	}
}

func TestDetachedEnvSkipsRefusal(t *testing.T) {
	if stdinIsTerminal() {
		t.Skip("stdin is a terminal")
	}
	t.Setenv(detachedEnvKey, "1")
	dir := t.TempDir()
	spec := filepath.Join(dir, "spec.md")
	if err := os.WriteFile(spec, []byte("do the thing\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fake := filepath.Join(dir, "grok")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nprintf '{\"type\":\"text\",\"data\":\"ok\"}\\n'\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GROK_RUN_STARTUP_GRACE", "2")
	t.Setenv("OUTSOURCE_RUNS_DIR", filepath.Join(dir, "runs"))
	rc := GrokMain([]string{
		"--cwd", dir, "--spec", spec, "--log", filepath.Join(dir, "run.ndjson"),
		"--label", "already-detached",
	}, io.Discard, io.Discard)
	if rc != 0 {
		t.Fatalf("OUTSOURCE_DETACHED=1 must skip the non-TTY refusal, got rc=%d", rc)
	}
}

// The log must exist the moment --detach returns.
//
// The skill's recipe is "launch detached, then arm bin/wait.sh over the logs
// you just started", and the waiter refuses a log that is not there yet on
// purpose (a typo'd path must not poll forever). Before the parent created
// the log, that refusal was a race against the child reaching its own
// os.Create, so the documented recipe failed intermittently with exit 64.
func TestDetachCreatesLogBeforeReturning(t *testing.T) {
	dir := t.TempDir()
	spec := filepath.Join(dir, "spec.md")
	if err := os.WriteFile(spec, []byte("do the thing\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A fake grok that outlives the assertion, so the child cannot be what
	// creates the log within the window being measured.
	fake := filepath.Join(dir, "grok")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nsleep 5\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	log := filepath.Join(dir, "run.ndjson")
	rc := GrokMain([]string{
		"--detach", "--cwd", dir, "--spec", spec, "--log", log,
	}, io.Discard, io.Discard)
	if rc != 0 {
		t.Fatalf("detach should have started, got rc=%d", rc)
	}
	if _, err := os.Stat(log); err != nil {
		t.Fatalf("log absent when --detach returned: %v — an immediately armed waiter refuses this", err)
	}
	// The waiter's own precondition, which is the thing that actually broke.
	if rc := WaitMain([]string{"--timeout", "1", log}, io.Discard, io.Discard); rc == ExitUsage {
		t.Fatal("wait refused the log the launcher just reported — the recipe is racy")
	}
}

// A --log path that cannot be created is a usage error on the caller's
// terminal, not a round that appears to start and vanishes: the detached
// child has no terminal to report it on.
func TestDetachRefusesUnwritableLogPath(t *testing.T) {
	dir := t.TempDir()
	spec := filepath.Join(dir, "spec.md")
	if err := os.WriteFile(spec, []byte("do the thing\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fake := filepath.Join(dir, "grok")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	var errBuf bytes.Buffer
	rc := GrokMain([]string{
		"--detach", "--cwd", dir, "--spec", spec,
		"--log", filepath.Join(dir, "no-such-dir", "run.ndjson"),
	}, io.Discard, &errBuf)
	if rc != ExitUsage {
		t.Fatalf("want ExitUsage (%d), got %d", ExitUsage, rc)
	}
	if !strings.Contains(errBuf.String(), "cannot create --log") {
		t.Fatalf("the refusal must name the log path; got %q", errBuf.String())
	}
}
