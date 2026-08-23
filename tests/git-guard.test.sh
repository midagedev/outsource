#!/usr/bin/env bash
# Table test for bin/git-guard.sh — the PreToolUse guard that keeps a delegated
# round from changing repository state.
#
# This guard is a security boundary with no test behind it until now, and it
# shipped a real hole in the *permissive* direction of its own contract:
# `git -C <repo> worktree list` was blocked because the allow pass and the deny
# pass spelled "global flags" differently. A delegate that opened by proving
# which tree it was in got refused for following the spec (2026-08-16).
#
# Both failure directions matter and both are asserted here:
#   - a mutation that slips through is a lost repository
#   - a read-only call that is refused makes agents work blind, and the next
#     spec quietly drops the check that would have caught a wrong tree
#
# Usage: tests/git-guard.test.sh   (exit 0 = all pass)
set -uo pipefail

GUARD="$(cd "$(dirname "$0")/.." && pwd)/skills/outsource/bin/git-guard.sh"
[ -x "$GUARD" ] || { echo "not executable: $GUARD" >&2; exit 1; }

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
mkdir -p "$TMP/state"
export XDG_STATE_HOME="$TMP/state"

pass=0
fail=0

# check <expect: allow|block> <command>
check() {
  local expect="$1" cmd="$2" rc
  CRUSH_TOOL_INPUT_COMMAND="$cmd" bash "$GUARD" >/dev/null 2>&1
  rc=$?
  local got="allow"
  [ "$rc" -eq 2 ] && got="block"
  if [ "$got" = "$expect" ]; then
    pass=$((pass + 1))
  else
    fail=$((fail + 1))
    printf 'FAIL  want=%-5s got=%-5s  %s\n' "$expect" "$got" "$cmd" >&2
  fi
}

# ── Read-only git must survive, including with global flags ──────────────
# The -C forms are the regression this file was created for.
check allow 'git status --short'
check allow 'git log --oneline -5'
check allow 'git show HEAD'
check allow 'git diff --stat'
check allow 'git blame README.md'
check allow 'git rev-parse HEAD'
check allow 'git ls-files'
check allow 'git worktree list'
check allow 'git -C /Users/me/repo/proj worktree list'
check allow 'git -C /tmp/wt worktree list | head -3'
check allow 'git --git-dir=/tmp/x/.git worktree list'
check allow 'git -c core.pager=cat worktree list'
check allow 'git branch --list'
check allow 'git branch -a'
check allow 'git remote -v'
check allow 'git config --get user.email'
# Read-only git as one step of a compound command stays allowed.
check allow 'cd /tmp && git status --short && echo done'

# ── State changes must be blocked, however they are dressed ──────────────
check block 'git commit -am wip'
check block 'git push origin main'
check block 'git checkout -- file.txt'
check block 'git switch main'
check block 'git stash'
check block 'git restore src/main.go'
check block 'git add -A'
check block 'git reset --hard HEAD~1'
check block 'git rebase main'
check block 'git merge feature'
check block 'git tag v1.0.0'
check block 'git branch newthing'
check block 'git worktree add /tmp/wt main'
check block 'git clean -fd'
check block 'git fetch origin'
check block 'git pull'
check block 'git config user.email x@y.z'
# Flags before the subcommand must not smuggle a mutation past the guard.
check block 'git -C /Users/me/repo/proj commit -am wip'
check block 'git --git-dir=/tmp/x/.git push'
check block 'sudo git push'
check block 'env FOO=1 git push origin main'
# A listing paired with a mutation is still a mutation: erasing the read-only
# half must not launder the rest of the line.
check block 'git worktree list && git commit -am x'
check block 'git -C /tmp/wt worktree list; git push'
check block 'echo hi | git commit -am piped'

# ── gh: publishing and remote mutation are lead-only ─────────────────────
check block 'gh pr create --fill'
check block 'gh pr merge 12 --squash'
check block 'gh release create v1.0.0'
check block 'gh repo delete foo'
check block 'gh api -X POST /repos/o/r/issues'
check allow 'gh pr list'
check allow 'gh pr view 12'
check allow 'gh run list --limit 5'
check allow 'gh api /repos/o/r/commits'

# ── Non-git commands are none of the guard's business ───────────────────
check allow 'go test ./...'
check allow 'npm run build'
check allow 'rg "git commit" docs/'
check allow 'echo "git push" >> notes.txt'

printf '\ngit-guard: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
