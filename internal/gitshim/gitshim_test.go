package gitshim

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The verbs that must never get through, and the read-only forms that must.
// The list itself is internal/guard's — this asserts the shim ASKS it rather
// than inventing a second opinion, which is the whole reason the guard has one
// owner.
//
// FAIL-first: the mechanism's absence was measured, not assumed. On 2026-09-18 a
// plain `muse exec` round was told to run `git commit --allow-empty -m probe`
// and did: exit_code 0, and HEAD moved to a root commit. muse has no hook and no
// definable permission profile, so without this shim that is the arm's baseline.
func TestShimRefusesWritesAndPassesReads(t *testing.T) {
	// The seam is the whole point of this test. Without it, an allowed command
	// reaches syscall.Exec and replaces THIS process with git — which is not a
	// hypothetical: on 2026-09-18 a deliberately bypassed guard did that here
	// and `go test` printed ok, having run `git commit` for real. So the test
	// records what would have run and asserts on it.
	type call struct {
		real string
		args []string
	}
	var ran []call
	orig := runGit
	runGit = func(real string, args []string, _, _ io.Writer) int {
		ran = append(ran, call{real, args})
		return 0
	}
	defer func() { runGit = orig }()

	// A real git has to be findable, or an allowed command cannot get past
	// realGit and the allow half would pass for the wrong reason.
	dir := t.TempDir()
	realDir := filepath.Join(dir, "real")
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	realPath := filepath.Join(realDir, "git")
	if err := os.WriteFile(realPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", realDir)

	blocked := [][]string{
		{"commit", "--allow-empty", "-m", "probe"},
		{"push", "origin", "main"},
		{"checkout", "-b", "x"},
		{"reset", "--hard", "HEAD~1"},
		{"add", "."},
		{"-C", "/some/repo", "commit", "-m", "x"},
	}
	for _, args := range blocked {
		ran = nil
		var stderr bytes.Buffer
		rc := Main(args, nil, &bytes.Buffer{}, &stderr)
		if rc != ExitBlocked {
			t.Errorf("git %s: rc=%d, want %d (blocked)", strings.Join(args, " "), rc, ExitBlocked)
		}
		// The load-bearing assertion: a refused command never reaches git.
		if len(ran) != 0 {
			t.Errorf("git %s: REACHED the real git despite being refused: %+v", strings.Join(args, " "), ran)
		}
		if !strings.Contains(stderr.String(), "refused") {
			t.Errorf("git %s: refusal must say so: %s", strings.Join(args, " "), stderr.String())
		}
		// The refusal quotes the command, because the round has to be able to
		// tell WHICH of its calls was stopped.
		if !strings.Contains(stderr.String(), args[0]) {
			t.Errorf("git %s: refusal must quote the command: %s", strings.Join(args, " "), stderr.String())
		}
	}

	// Read-only git is the other half: a guard that blocked everything would
	// pass the test above and make the arm useless.
	allowed := [][]string{
		{"status", "--porcelain"},
		{"log", "--oneline", "-3"},
		{"diff", "HEAD"},
		{"worktree", "list"},
		{"branch", "--list"},
		{"remote", "-v"},
	}
	for _, args := range allowed {
		ran = nil
		var stderr bytes.Buffer
		if rc := Main(args, nil, &bytes.Buffer{}, &stderr); rc != 0 {
			t.Errorf("git %s: rc=%d, want 0 (allowed); stderr=%s", strings.Join(args, " "), rc, stderr.String())
		}
		if len(ran) != 1 {
			t.Fatalf("git %s: want exactly one call to the real git, got %+v", strings.Join(args, " "), ran)
		}
		if ran[0].real != realPath {
			t.Errorf("git %s: ran %q, want the real git %q", strings.Join(args, " "), ran[0].real, realPath)
		}
		// argv must arrive untouched — the guard reads a reconstructed string,
		// but what RUNS is the original argv.
		if strings.Join(ran[0].args, " ") != strings.Join(args, " ") {
			t.Errorf("git %s: argv was altered: %v", strings.Join(args, " "), ran[0].args)
		}
	}
}

// ExitBlocked must not collide with git's own failure code, or a round cannot
// tell "the guard said no" from "git ran and failed".
func TestBlockedExitCodeIsDistinctFromGitFailure(t *testing.T) {
	if ExitBlocked == 1 || ExitBlocked == 0 {
		t.Fatalf("ExitBlocked=%d collides with git's own codes", ExitBlocked)
	}
}

// The shim IS the first git on PATH. If realGit used a plain lookup it would
// find itself and exec forever — a fork bomb wearing the name of the tool the
// round needs most.
//
// FAIL-first: drop the isSelf skip in realGit and this hangs or returns the
// shim's own path.
func TestRealGitSkipsTheShimItself(t *testing.T) {
	dir := t.TempDir()
	shimDir := filepath.Join(dir, "bin")
	if _, err := Write(shimDir, "/usr/local/bin/outsource"); err != nil {
		t.Fatal(err)
	}
	// A stand-in for the real git, later on PATH.
	realDir := filepath.Join(dir, "real")
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	realPath := filepath.Join(realDir, "git")
	if err := os.WriteFile(realPath, []byte("#!/bin/sh\necho real git\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+realDir)
	got, err := realGit()
	if err != nil {
		t.Fatalf("realGit: %v", err)
	}
	if got != realPath {
		t.Fatalf("realGit = %q, want the real one at %q", got, realPath)
	}

	// With nothing but the shim on PATH, it must say so rather than loop.
	t.Setenv("PATH", shimDir)
	if _, err := realGit(); err == nil {
		t.Fatal("a PATH holding only the shim must be an error, not a self-exec")
	} else if !strings.Contains(err.Error(), "behind this shim") {
		t.Fatalf("the error must explain the situation, got: %v", err)
	}
}

// The generated shim has to be executable, dispatch to this tool, and carry the
// marker realGit recognises it by.
func TestWriteProducesAnExecutableDispatchingShim(t *testing.T) {
	dir := t.TempDir()
	path, err := Write(filepath.Join(dir, "bin"), "/opt/my tools/outsource")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != "git" {
		t.Fatalf("the shim must be named git, got %s", path)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&0o111 == 0 {
		t.Fatalf("shim is not executable: %v", fi.Mode())
	}
	b, _ := os.ReadFile(path)
	body := string(b)
	if !strings.Contains(body, shimMarker) {
		t.Fatalf("shim lacks the marker realGit identifies it by:\n%s", body)
	}
	if !strings.Contains(body, "git-shim") {
		t.Fatalf("shim must dispatch to the git-shim tool:\n%s", body)
	}
	// A launcher path with a space has to survive the shell.
	if !strings.Contains(body, `'/opt/my tools/outsource'`) {
		t.Fatalf("the binary path must be shell-quoted:\n%s", body)
	}
	if !strings.Contains(body, `"$@"`) {
		t.Fatalf("arguments must be forwarded intact:\n%s", body)
	}
}

// PATH must be replaced, never appended to as a second entry: duplicate
// KEY=value entries are runtime-dependent, and the wrong one winning would put
// the real git first and silently retire the guard.
func TestPrependPathReplacesTheSingleEntry(t *testing.T) {
	env := []string{"HOME=/home/x", "PATH=/usr/bin:/bin", "TERM=dumb"}
	out := PrependPath(env, "/round/bin")
	var paths []string
	for _, e := range out {
		if k, v, _ := strings.Cut(e, "="); k == "PATH" {
			paths = append(paths, v)
		}
	}
	if len(paths) != 1 {
		t.Fatalf("want exactly one PATH entry, got %d: %v", len(paths), paths)
	}
	if !strings.HasPrefix(paths[0], "/round/bin"+string(os.PathListSeparator)) {
		t.Fatalf("the shim dir must come first: %s", paths[0])
	}
	if !strings.Contains(paths[0], "/usr/bin:/bin") {
		t.Fatalf("the inherited PATH must be preserved after it: %s", paths[0])
	}
	// Nothing else is disturbed.
	if len(out) != len(env) {
		t.Fatalf("PrependPath changed the entry count: %v", out)
	}
}
