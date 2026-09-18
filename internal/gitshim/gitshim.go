// Package gitshim is the git guard for a harness that has no hook and no
// permission config: it becomes the `git` the round finds on PATH.
//
// Every other arm attaches the guard the way its harness allows — claude-code
// takes a PreToolUse hook, opencode and agy take a permission/deny block in a
// generated config. The muse CLI takes neither: it has an approval mode and a
// sandbox, and a named --permission-profile whose profiles cannot be defined
// from any user-writable document (measured 2026-09-18: every candidate member
// of the enterprise `defaults` and `policy` planes was rejected as
// unknown_member). Left alone, a headless round commits — measured, the same
// day: `git commit --allow-empty` inside a muse exec round moved HEAD.
//
// So the guard moves down a layer, to the name itself. The launcher writes a
// `git` shim into the round's own bin directory and puts that directory first
// on the round's PATH; the shim asks internal/guard — still the single owner of
// what is refused — and execs the real git when the answer is no. Measured the
// same day: inside the round, `command -v git` resolved to the shim, the commit
// was refused, and HEAD stayed unborn.
//
// This is a belt, not a cage, and it is the same belt the other arms wear: a
// round that calls /usr/bin/git by absolute path, or shells out through a
// language binding, is past it — exactly as a round that hides a commit from
// opencode's command-pattern deny list is past that one. Worktree isolation and
// the spec's own rules are the other layers.
package gitshim

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/midagedev/outsource/internal/guard"
)

// ExitBlocked is what a refused git invocation exits with. It is deliberately
// not 1: a round must be able to tell "the guard said no" from "git ran and
// failed", and 1 is what git itself returns for an ordinary error.
const ExitBlocked = 97

// ExitNoGit is "there is no real git behind this shim" — a broken install
// rather than a refusal, and it must not be mistaken for one.
const ExitNoGit = 127

// Main is the shim. argv is everything after the tool name, i.e. git's own
// arguments, and the process was invoked as `git` from the round's point of
// view.
func Main(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	if blocked, cmd, msg := Decide(args); blocked {
		fmt.Fprintf(stderr, "outsource git guard: refused `%s`\n\n%s\n", cmd, msg)
		return ExitBlocked
	}

	real, err := realGit()
	if err != nil {
		fmt.Fprintf(stderr, "outsource git guard: %v\n", err)
		return ExitNoGit
	}
	return runGit(real, args, stdout, stderr)
}

// runGit hands the call to the real git. It is a variable for one reason, and
// the reason was measured rather than imagined: a test that lets the shim reach
// syscall.Exec has its OWN process replaced by git, which destroys the
// assertion and runs the command for real. On 2026-09-18 a FAIL-first proof did
// exactly that — the guard was bypassed on purpose, the shim exec'd
// `git commit --allow-empty` against this repository, and `go test` reported
// ok because the test binary was gone by the time it could fail. A gate that
// can be satisfied by disappearing is not a gate. With the seam, a test
// observes that a refused command never arrives here.
var runGit = func(real string, args []string, stdout, stderr io.Writer) int {
	// exec, not run: the round's shell should see git's own exit code, signals
	// and terminal behaviour, with no wrapper process left in between. Falling
	// back to a child process keeps the shim working if exec is unavailable.
	if err := syscall.Exec(real, append([]string{real}, args...), os.Environ()); err != nil {
		c := exec.Command(real, args...)
		c.Stdin, c.Stdout, c.Stderr = os.Stdin, stdout, stderr
		if runErr := c.Run(); runErr != nil {
			if ee, ok := runErr.(*exec.ExitError); ok {
				return ee.ExitCode()
			}
			fmt.Fprintf(stderr, "outsource git guard: %v\n", runErr)
			return ExitNoGit
		}
	}
	return 0
}

// Decide is the guard's answer for one git argv, separated from running
// anything so the decision can be asserted on its own.
func Decide(args []string) (blocked bool, cmd, message string) {
	// The command string is reconstructed for the guard, which owns the rule
	// and reads a shell-ish line. Quoting each argument would defeat the
	// regexes (they match a bare verb after `git`), and this string is never
	// executed — it is only ever matched against. The real invocation passes
	// argv through untouched, so nothing here can change what runs.
	cmd = "git " + strings.Join(args, " ")
	blocked, message = guard.Verdict(cmd)
	return blocked, cmd, message
}

// realGit finds the git this shim stands in front of.
//
// It cannot simply use exec.LookPath: the shim IS the first `git` on PATH, so
// that call finds the shim and the shim would exec itself forever. Every PATH
// entry holding a `git` that resolves back to this executable is therefore
// skipped, which also covers the installed-copy case where the same shim
// appears under two paths.
func realGit() (string, error) {
	self, err := os.Executable()
	if err == nil {
		self, _ = filepath.EvalSymlinks(self)
	}
	var skipped []string
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir == "" {
			continue
		}
		cand := filepath.Join(dir, "git")
		fi, err := os.Stat(cand)
		if err != nil || fi.IsDir() || fi.Mode()&0o111 == 0 {
			continue
		}
		if isSelf(cand, self) {
			skipped = append(skipped, cand)
			continue
		}
		return cand, nil
	}
	if len(skipped) > 0 {
		return "", fmt.Errorf("no real git on PATH behind this shim (skipped %s)", strings.Join(skipped, ", "))
	}
	return "", fmt.Errorf("no git found on PATH")
}

// isSelf reports whether a candidate path is this executable, or a script that
// dispatches into it. The shim the launcher writes is a two-line sh script, so
// a content test is what identifies it — a path comparison alone would miss it.
func isSelf(cand, self string) bool {
	if resolved, err := filepath.EvalSymlinks(cand); err == nil && self != "" && resolved == self {
		return true
	}
	b, err := os.ReadFile(cand)
	if err != nil {
		return false
	}
	// Bounded read: a real git is a multi-megabyte binary and the marker sits
	// in the first line of a tiny script.
	if len(b) > 4096 {
		b = b[:4096]
	}
	return strings.Contains(string(b), shimMarker)
}

// shimMarker identifies a generated shim to realGit. It is a comment in the
// script, so it costs nothing at run time and cannot be confused with a real
// git — no git binary contains it.
const shimMarker = "outsource-git-guard-shim"

// Write puts the shim in dir and returns the path. The caller puts dir first on
// the round's PATH.
//
// A script rather than a symlink, for the reason build.sh already records about
// the tool shims: a symlink would resolve argv[0] to the outsource binary,
// which would then dispatch on the name `outsource` rather than `git`. The
// script names the tool explicitly instead.
func Write(dir, selfPath string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "git")
	body := fmt.Sprintf("#!/bin/sh\n# %s — generated per round; the guard is internal/guard.\nexec %s git-shim \"$@\"\n",
		shimMarker, shellQuoteArg(selfPath))
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		return "", err
	}
	return path, nil
}

// shellQuoteArg is the same single-quote form the claude-code hook settings
// use: the launcher's own path can contain spaces.
func shellQuoteArg(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

// PrependPath returns env with dir first on PATH. Filter-then-append, because
// duplicate KEY=value entries are runtime-dependent and only a single entry is
// safe — the same rule opencodeEnv follows for PWD.
func PrependPath(env []string, dir string) []string {
	out := make([]string, 0, len(env)+1)
	old := ""
	for _, e := range env {
		if k, v, ok := strings.Cut(e, "="); ok && k == "PATH" {
			old = v
			continue
		}
		out = append(out, e)
	}
	if old == "" {
		old = os.Getenv("PATH")
	}
	return append(out, "PATH="+dir+string(os.PathListSeparator)+old)
}
