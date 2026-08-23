#!/usr/bin/env bash
# runs.sh dismiss — the after-reading verb for orphans, and its one refusal.
#
# The refusal is the point: dismiss exists because prune keeps orphans, and
# the same reasoning must keep dismiss away from anything still alive.
#
#   RUNS_SH=/path/to/runs.sh tests/runs-dismiss.test.sh

set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
RUNS="${RUNS_SH:-$HERE/skills/outsource/bin/runs.sh}"
[ -x "$RUNS" ] || { echo "not executable: $RUNS" >&2; exit 2; }

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
mkdir -p "$TMP/state"
export XDG_STATE_HOME="$TMP/state"
export OUTSOURCE_RUNS_DIR="$TMP/runs"

pass=0; fail=0
ok() {  # <desc> <want-rc> <got-rc> <output> <must|-> <mustnot|->
  local desc="$1" want="$2" got="$3" out="$4" must="$5" mustnot="$6" bad=""
  [ "$got" = "$want" ] || bad="rc=$got want=$want"
  [ "$must" = "-" ] || printf '%s' "$out" | grep -q -- "$must" || bad="$bad; missing: $must"
  if [ "$mustnot" != "-" ] && printf '%s' "$out" | grep -q -- "$mustnot"; then bad="$bad; unexpected: $mustnot"; fi
  if [ -z "$bad" ]; then pass=$(( pass + 1 )); else
    fail=$(( fail + 1 )); printf 'FAIL  %s\n      %s\n      output: %s\n' "$desc" "$bad" "$out"
  fi
}

# An orphan: registered with a pid that is certainly gone.
DEAD_PID=99999
while kill -0 "$DEAD_PID" 2>/dev/null; do DEAD_PID=$(( DEAD_PID - 1 )); done
ORPHAN_ID="$(bash "$RUNS" start --pid "$DEAD_PID" --label ghost --provider xai --harness grok-cli --spec /tmp/x.md)"

out="$(bash "$RUNS" list 2>&1)"; ok "the orphan is visible before dismissal" 0 $? "$out" "ghost" "-"

out="$(bash "$RUNS" dismiss "$ORPHAN_ID" 2>&1)"; rc=$?
ok "an orphan can be dismissed" 0 "$rc" "$out" "dismissed $ORPHAN_ID" "-"

out="$(bash "$RUNS" list 2>&1)"; ok "and it is gone from the listing" 0 $? "$out" "no delegated runs" "ghost"

out="$(bash "$RUNS" dismiss "$ORPHAN_ID" 2>&1)"; rc=$?
ok "dismissing twice is a named error, not a silent ok" 65 "$rc" "$out" "no such run" "-"

# A finished (failed) record can also be dismissed once read.
FAIL_ID="$(bash "$RUNS" start --pid "$DEAD_PID" --label redround --provider zai --harness claude-code --spec /tmp/y.md)"
bash "$RUNS" finish "$FAIL_ID" --rc 70 > /dev/null
out="$(bash "$RUNS" dismiss "$FAIL_ID" 2>&1)"; rc=$?
ok "a finished record can be dismissed" 0 "$rc" "$out" "dismissed" "-"

# The refusal: a record whose pid is alive is running work.
LIVE_ID="$(bash "$RUNS" start --pid $$ --label liveround --provider xai --harness grok-cli --spec /tmp/z.md)"
out="$(bash "$RUNS" dismiss "$LIVE_ID" 2>&1)"; rc=$?
ok "a running round is refused" 66 "$rc" "$out" "work, not residue" "-"
out="$(bash "$RUNS" list 2>&1)"; ok "and it survives the attempt" 0 $? "$out" "liveround" "-"

out="$(bash "$RUNS" dismiss 2>&1)"; rc=$?
ok "no id is a usage error" 64 "$rc" "$out" "needs a run id" "-"

printf '\nruns-dismiss: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
