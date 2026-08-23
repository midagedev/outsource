package launch

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --detach must not swallow usage errors: a done-marker the spec never
// contains still fails synchronously on the caller's terminal, same as grok-run.
func TestOutsourceDetachKeepsUsageErrorsSynchronous(t *testing.T) {
	dir := t.TempDir()
	spec := filepath.Join(dir, "spec.md")
	if err := os.WriteFile(spec, []byte("do the thing\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rc := OutsourceMain([]string{
		"--detach",
		"--cwd", dir, "--spec", spec, "--log", filepath.Join(dir, "run.log"),
		"--harness", "crush",
		"--done-marker", "DONE-NEVER-IN-SPEC",
	}, io.Discard, io.Discard)
	if rc != ExitUsage {
		t.Fatalf("want ExitUsage (%d) before any detach, got %d", ExitUsage, rc)
	}
	if _, err := os.Stat(filepath.Join(dir, "run.log.rc")); err == nil {
		t.Fatal("a refused launch must not write a sentinel")
	}
}

func TestOutsourceNonTTYForegroundRefusal(t *testing.T) {
	if stdinIsTerminal() {
		t.Skip("stdin is a terminal; refusal only fires on non-TTY")
	}
	dir := t.TempDir()
	spec := filepath.Join(dir, "spec.md")
	if err := os.WriteFile(spec, []byte("do the thing\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	rc := OutsourceMain([]string{
		"--cwd", dir, "--spec", spec, "--log", filepath.Join(dir, "run.log"),
		"--harness", "crush",
	}, io.Discard, &stderr)
	if rc != ExitUsage {
		t.Fatalf("want ExitUsage (%d) on non-TTY without --detach/--foreground, got %d stderr=%s", ExitUsage, rc, stderr.String())
	}
	msg := stderr.String()
	for _, want := range []string{"--detach", "--foreground", "2026-08-22"} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal must name %q; got %s", want, msg)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "run.log.rc")); err == nil {
		t.Fatal("a refused launch must not write a sentinel")
	}
}

func TestOutsourceUnknownDetachGone(t *testing.T) {
	var stdout, stderr bytes.Buffer
	rc := OutsourceMain([]string{"-h"}, &stdout, &stderr)
	if rc != 0 {
		t.Fatalf("help rc=%d", rc)
	}
	if !strings.Contains(stdout.String(), "--detach") {
		t.Fatalf("help must list --detach, got %s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "--foreground") {
		t.Fatalf("help must list --foreground, got %s", stdout.String())
	}
}
