// Package guard is the PreToolUse guard for delegated runs.
//
// It reads the actual command string, which is strictly stronger than glob
// denies: `git -C <path> commit` and `env ... git push` are caught too.
//
// Two call conventions, both supported so one guard serves every harness:
//
//	crush        — command arrives in $CRUSH_TOOL_INPUT_COMMAND
//	claude-code  — hook JSON arrives on stdin as {"tool_input":{"command":…}}
//
// Exit 2 blocks the one call; the agent sees stderr and can try again.
//
// This is a security boundary, so the port carries a rule the other packages do
// not: the patterns below are the shell's patterns, character for character.
// They are not tidied, merged or "improved". Any change to them is a change to
// what a delegate can do to a repository, and the only evidence that this port
// preserved the boundary is that both implementations return the same verdict
// on every command in the corpus gate — which compares exit codes, not
// messages, because the code is the boundary and the message is advice.
package guard

import (
	"encoding/json"
	"fmt"
	"github.com/midagedev/outsource/internal/telemetry"
	"io"
	"os"
	"regexp"
)

// ExitBlocked is what a refusal returns. Not 1: a hook exiting 2 is what makes
// the harness surface stderr to the model and continue, rather than treating the
// guard itself as broken.
const ExitBlocked = 2

// git subcommands that change repository state. Read-only git (log, show, diff,
// blame, status, ls-files, rev-parse, worktree list) stays available on purpose:
// blanket bans cripple investigation work, and a delegate that cannot read
// history writes worse code.
const denyGit = `commit|push|checkout|switch|stash|restore|add|rm|mv|reset|rebase|merge|cherry-pick|revert|tag|branch|worktree|clean|filter-branch|update-ref|apply|am|fetch|pull|clone|remote|submodule|config|gc|prune|reflog|notes|replace|sparse-checkout|bisect`

// Global flags that may sit between `git` and the subcommand. Two shapes: a flag
// that swallows the next word (-C <path>, -c <k=v>) and a long flag carrying its
// value inline (--git-dir=…).
//
// ONE definition, used by both passes below. That is the whole point. The two
// passes once spelled this differently — the deny pass understood `-C <path>`
// and the allow pass did not — so `git -C <repo> worktree list` had its
// read-only form left un-erased and then tripped the deny pass on `worktree`. A
// delegate that opened by proving which tree it was in got blocked for doing
// exactly what the specs ask of it (measured 2026-08-16). The corpus gate takes
// this pattern crossed with every listing form to full product for that reason.
const gitFlags = `([[:space:]]+(-[A-Za-z-]+([[:space:]]+[^[:space:]]+)?|--[a-z-]+(=[^[:space:]]+)?))*`

// Listing forms of otherwise-mutating subcommands. Delegation specs routinely
// open with `git worktree list` to prove which tree the agent is in, so these
// must survive the deny pass. `branch --show-current` joined the list 2026-09-05:
// two worktree rounds were told to prove their branch with it and both were
// blocked (the paired `worktree list` was erased, the branch half tripped the
// deny pass), so they fell back to weaker evidence.
const allowRO = `(worktree[[:space:]]+list|branch[[:space:]]+(-[alvr]+|--list|--show-current)|remote[[:space:]]+(-v|--verbose|show)|config[[:space:]]+(--get|--get-all|--list|-l))`

var (
	// Erase the read-only forms, then run the deny pass on what is left. A line
	// that pairs a listing with a mutation (`git worktree list && git commit
	// -am x`) still trips the deny pass, because only the listing half is
	// erased.
	reAllowRO = regexp.MustCompile(`(?i)git` + gitFlags + `[[:space:]]+` + allowRO)

	// `git` anywhere in the pipeline, with flags like -C/-c/--git-dir before the
	// subcommand, and ignoring a leading sudo/env VAR=…
	reDenyGit = regexp.MustCompile(`(?i)(^|[;&|(]|\bsudo\b|\benv\b[^;&|]*)[[:space:]]*git` + gitFlags + `[[:space:]]+(` + denyGit + `)\b`)

	// gh commands that publish or mutate remote state.
	reDenyGH = regexp.MustCompile(`(?i)(^|[;&|(])[[:space:]]*gh[[:space:]]+(pr[[:space:]]+(create|merge|close|edit|ready|review)|repo[[:space:]]+(create|delete|edit|fork|sync)|release|workflow[[:space:]]+run|api[[:space:]]+-X[[:space:]]*(POST|PATCH|PUT|DELETE))`)
)

// Messages are Korean because the delegate reads them and this skill's specs are
// written for a Korean-speaking lead. They are advice, not the boundary — the
// exit code is the boundary — so they are also the one thing the corpus gate
// deliberately does not compare.
const (
	msgGit = "BLOCKED: git 상태 변경은 리드 전용이다. 읽기 전용 git(log/show/diff/blame/status)만 허용된다. 복원이 필요하면 리드에게 요청하라."
	msgGH  = "BLOCKED: PR/릴리스/원격 변경은 리드 전용이다. 읽기(gh pr list/view, gh api GET)만 허용된다."
)

// Verdict reports whether a command must be blocked, and why.
func Verdict(cmd string) (blocked bool, message string) {
	if cmd == "" {
		return false, ""
	}
	scan := reAllowRO.ReplaceAllString(cmd, "GIT_RO")
	if reDenyGit.MatchString(scan) {
		return true, msgGit
	}
	if reDenyGH.MatchString(scan) {
		return true, msgGH
	}
	return false, ""
}

// commandFrom resolves the command out of whichever convention delivered it.
// The environment wins, and stdin is only consulted when it is not a terminal —
// otherwise an interactive invocation would hang waiting for a payload nobody
// is going to type.
func commandFrom(env func(string) string, stdin io.Reader, stdinIsTerminal bool) string {
	if c := env("CRUSH_TOOL_INPUT_COMMAND"); c != "" {
		return c
	}
	if stdinIsTerminal || stdin == nil {
		return ""
	}
	raw, err := io.ReadAll(stdin)
	if err != nil || len(raw) == 0 {
		return ""
	}
	// A malformed payload yields no command, and therefore no block. That is
	// deliberate: a guard that refused whatever it could not parse would break
	// every tool call in a round the moment a harness changed its hook shape,
	// and the guard is a belt — the spec's own rules and worktree isolation are
	// the other layers.
	var d struct {
		ToolInput json.RawMessage `json:"tool_input"`
	}
	if json.Unmarshal(raw, &d) != nil || len(d.ToolInput) == 0 {
		return ""
	}
	var ti struct {
		Command *string `json:"command"`
	}
	if json.Unmarshal(d.ToolInput, &ti) != nil || ti.Command == nil {
		return ""
	}
	return *ti.Command
}

// Main is the guard entry point.
func Main(_ []string, stdin io.Reader, _ io.Writer, stderr io.Writer) int {
	isTTY := false
	if f, ok := stdin.(*os.File); ok {
		if fi, err := f.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
			isTTY = true
		}
	}
	cmd := commandFrom(os.Getenv, stdin, isTTY)
	if blocked, msg := Verdict(cmd); blocked {
		fmt.Fprintln(stderr, msg)
		// Which KIND was blocked, never the command itself: a command line can
		// carry an env assignment or a path, and this file is meant to be safe to
		// read months later. A count of these is the most useful number in the log
		// — it says which delegates keep trying to do the lead's job.
		which := "git"
		if msg == msgGH {
			which = "gh"
		}
		telemetry.Note("why", "blocked "+which)
		return ExitBlocked
	}
	return 0
}
