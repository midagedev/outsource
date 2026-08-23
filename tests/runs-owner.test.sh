#!/usr/bin/env bash
# Ownership scoping in runs.sh, and the record-id collision it exposed.
#
# The registry is machine-wide and has to stay that way — an orphaned round
# must be findable from wherever you happen to be standing. The scoping lives
# at the reading end, and it fails in two directions:
#
#   too wide  — another window's rounds show up in your status line and read
#               as your own work; two windows narrate each other.
#   too narrow — a round an in-process teammate launched vanishes from the
#               lead's own view, which is the view the lead uses to notice a
#               round died.
#
# So every case below asserts both what must appear and what must not.
#
#   RUNS_SH=/path/to/runs.sh tests/runs-owner.test.sh

set -uo pipefail

RUNS_SH="${RUNS_SH:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/skills/outsource/bin/runs.sh}"
[ -x "$RUNS_SH" ] || { echo "not executable: $RUNS_SH" >&2; exit 2; }

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
mkdir -p "$TMP/state"
export XDG_STATE_HOME="$TMP/state"
export OUTSOURCE_RUNS_DIR="$TMP/runs"

pass=0; fail=0

labels() {  # run `runs.sh list` with the given filter flags, print labels sorted
  "$RUNS_SH" list "$@" 2>/dev/null | awk 'NR>1 && NF>3 {print $2}' | sort | tr '\n' ' ' | sed 's/ $//'
}

expect() {  # <description> <expected-labels> <filter args...>
  local desc="$1" want="$2"; shift 2
  local got; got="$(labels "$@")"
  if [ "$got" = "$want" ]; then
    pass=$(( pass + 1 ))
  else
    fail=$(( fail + 1 ))
    printf 'FAIL  %s\n      want: [%s]\n      got:  [%s]\n' "$desc" "$want" "$got"
  fi
}

# Four rounds, all launched in the same second by the same pid on purpose:
# that is the id-collision case, and a silent overwrite here would delete a
# record rather than fail loudly.
"$RUNS_SH" start --pid $$ --label a-session --provider zai --harness crush \
  --spec /s/a.md --owner SESS-A --owner-claude-pid 9001 >/dev/null
"$RUNS_SH" start --pid $$ --label b-teammate --provider zai --harness crush \
  --spec /s/b.md --owner SESS-SUB --owner-claude-pid 9001 >/dev/null
"$RUNS_SH" start --pid $$ --label c-stranger --provider zai --harness crush \
  --spec /s/c.md --owner SESS-Z --owner-claude-pid 9999 >/dev/null
# No owner fields at all: a record written by a launcher older than this
# feature, or one launched outside Claude Code.
"$RUNS_SH" start --pid $$ --label d-legacy --provider zai --harness crush \
  --spec /s/d.md >/dev/null

n_records=$(ls "$OUTSOURCE_RUNS_DIR"/*.run 2>/dev/null | wc -l | tr -d ' ')
if [ "$n_records" = 4 ]; then
  pass=$(( pass + 1 ))
else
  fail=$(( fail + 1 ))
  echo "FAIL  four same-second same-pid launches must keep four records; got $n_records"
fi

expect "no filter lists everything, including unowned"    "a-session b-teammate c-stranger d-legacy"
expect "session id alone matches only that session"       "a-session"                --owner SESS-A
expect "claude pid alone matches the whole process tree"  "a-session b-teammate"     --owner-claude-pid 9001
expect "both keys: either one matching is enough"         "a-session b-teammate"     --owner SESS-A --owner-claude-pid 9001
expect "a teammate's session id still finds the lead's"   "a-session b-teammate"     --owner SESS-SUB --owner-claude-pid 9001
expect "a session with no rounds shows none, not all"     ""                         --owner SESS-NONE --owner-claude-pid 7777
expect "empty owner values do not match unowned records"  ""                         --owner SESS-NONE
# The status line passes an empty --owner-claude-pid whenever CLAUDE_PID is
# unset, which is most of the time. That empty value must narrow nothing and
# must not turn the filter off.
expect "an empty pid neither widens nor disables"         "a-session"                --owner SESS-A --owner-claude-pid ""
expect "an empty session id falls back to the pid"        "a-session b-teammate"     --owner "" --owner-claude-pid 9001

# json and line read through the same filter; a divergence between the three
# would mean the status line and the listing disagree about what is running.
n_json=$("$RUNS_SH" json --owner-claude-pid 9001 2>/dev/null \
  | python3 -c 'import json,sys; print(len(json.load(sys.stdin)))' 2>/dev/null)
if [ "$n_json" = 2 ]; then pass=$(( pass + 1 )); else
  fail=$(( fail + 1 )); echo "FAIL  json must apply the same filter as list; want 2, got ${n_json:-<none>}"
fi
if "$RUNS_SH" line --owner-claude-pid 9001 2>/dev/null | grep -q 'a-session' &&
   ! "$RUNS_SH" line --owner-claude-pid 9001 2>/dev/null | grep -q 'c-stranger'; then
  pass=$(( pass + 1 ))
else
  fail=$(( fail + 1 )); echo "FAIL  line must apply the same filter as list"
fi

printf '\nruns-owner: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
