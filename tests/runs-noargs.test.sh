#!/usr/bin/env bash
# runs.sh with no arguments — the form the README leads with.
#
# `case "${1:-list}"` defaulted correctly, but each branch then ran `shift`,
# which fails when there are no positional args; under `set -e` the script
# exited 1 having printed nothing. Silence plus rc=1 is indistinguishable from
# "no rounds on record", so a lead checking what was in flight was told nothing
# at all (measured 2026-08-18, five live rounds invisible).
#
#   RUNS_SH=/path/to/runs.sh tests/runs-noargs.test.sh

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

# 1. Empty registry, no arguments: says so, out loud, rc=0.
out="$(bash "$RUNS" 2>&1)"; rc=$?
ok "bare invocation on an empty registry" 0 "$rc" "$out" "no delegated runs" "-"

# 2. A live round must appear in the bare listing — the regression that hid
#    five of them. Registered against this test's own pid, which is alive.
ID="$(bash "$RUNS" start --pid $$ --label bare-check --provider xai \
        --harness grok-cli --spec /tmp/bare.md)"

out="$(bash "$RUNS" 2>&1)"; rc=$?
ok "bare invocation lists a running round" 0 "$rc" "$out" "bare-check" "-"

# 3. The explicit form and the default form must agree — a fix that only
#    repaired one of them would leave the docs lying in the other direction.
#
#    The two elapsed columns are blanked before comparing, and that is not a
#    weakening. ELAPSED renders in seconds for a round this young, so two
#    invocations that straddle a second boundary printed "2s" and "3s" and this
#    case failed — measured twice in about thirty suite runs, and reproduced
#    deliberately by sleeping 1s between the calls. What this case protects is
#    that the default verb IS `list`; that the columns themselves are right is
#    asserted by runs-owner. A test that fails on the clock teaches people to
#    re-run the suite until it is green, which costs more than the case is worth.
#
#    Columns 41-55 are ELAPSED and IDLE in the fixed-width listing format, and
#    the same blanking is applied to both sides, so it cannot hide a real
#    difference anywhere else on the line.
blank_clock() { sed 's/^\(.\{40\}\).\{0,15\}/\1               /'; }
bare="$(bash "$RUNS" 2>&1 | blank_clock)"
explicit="$(bash "$RUNS" list 2>&1 | blank_clock)"
[ "$bare" = "$explicit" ] && ok "bare == list" 0 0 "" "-" "-" \
  || ok "bare == list" 0 1 "bare:[$bare] list:[$explicit]" "-" "-"

# 4. Filter flags still parse with no subcommand in front of them.
out="$(bash "$RUNS" --label bare-check 2>&1)"; rc=$?
ok "bare invocation accepts filter flags" 0 "$rc" "$out" "bare-check" "-"

# 5. A bad subcommand still names itself (it is read after the shift now).
out="$(bash "$RUNS" nope 2>&1)"; rc=$?
ok "unknown subcommand names the offender" 64 "$rc" "$out" "unknown subcommand: nope" "-"

bash "$RUNS" finish "$ID" --rc 0 >/dev/null 2>&1 || true

printf '\n%s: %d passed, %d failed\n' "$(basename "$0")" "$pass" "$fail"
[ "$fail" -eq 0 ]
