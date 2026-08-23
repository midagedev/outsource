#!/usr/bin/env bash
# last-report.sh — both backend shapes, and the silences between them.
#
# The dangerous failure is not a wrong report but a *stale* one: grok text
# deltas from an early turn surviving into the answer, or a result event
# being shadowed by later chatter. Every case pairs the wanted content with
# decoy content that must NOT appear.
#
#   LAST_REPORT=/path/to/last-report.sh tests/last-report.test.sh

set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN="${LAST_REPORT:-$HERE/skills/outsource/bin/last-report.sh}"
[ -x "$BIN" ] || { echo "not executable: $BIN" >&2; exit 2; }

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
mkdir -p "$TMP/state"
export XDG_STATE_HOME="$TMP/state"
export OUTSOURCE_RUNS_DIR="$TMP/runs"

pass=0; fail=0
out=""; rc=0

run() {  # <log body on stdin> [extra flags...]
  cat > "$TMP/log"
  out="$("$BIN" "$TMP/log" "$@" 2>&1)"; rc=$?
}

ok() {  # <desc> <want-rc> <must-appear|-> <must-not-appear|->
  local desc="$1" want_rc="$2" must="$3" mustnot="$4" bad=""
  [ "$rc" = "$want_rc" ] || bad="rc=$rc want=$want_rc"
  if [ "$must" != "-" ] && ! printf '%s' "$out" | grep -q -- "$must"; then
    bad="$bad; missing: $must"
  fi
  if [ "$mustnot" != "-" ] && printf '%s' "$out" | grep -q -- "$mustnot"; then
    bad="$bad; unexpected: $mustnot"
  fi
  if [ -z "$bad" ]; then pass=$(( pass + 1 )); else
    fail=$(( fail + 1 ))
    printf 'FAIL  %s\n      %s\n      output: %s\n' "$desc" "$bad" "$out"
  fi
}

# ---- claude-code harness (run.log JSONL) ------------------------------------

run <<'EOF'
{"type":"assistant","message":{"content":[{"type":"text","text":"working on it, a moment"}]}}
{"type":"result","result":"FINAL REPORT ALPHA\nDONE-X"}
EOF
ok "a result event is the report" 0 "FINAL REPORT ALPHA" "working on it"

run <<'EOF'
{"type":"result","result":"EARLY DRAFT"}
{"type":"result","result":"FINAL REPORT BETA"}
EOF
ok "the LAST result event wins" 0 "FINAL REPORT BETA" "EARLY DRAFT"

LONG="$(printf 'assistant text long enough to be a report body %.0s' $(seq 1 8))"
run <<EOF
{"type":"assistant","message":{"content":[{"type":"text","text":"$LONG tail-marker"}]}}
EOF
ok "no result event falls back to long assistant text" 0 "tail-marker" "-"

run <<'EOF'
{"type":"assistant","message":{"content":[{"type":"text","text":"short ack"}]}}
EOF
ok "short chatter alone is not a report (exit 65)" 65 "no report-shaped content" "-"

# ---- grok CLI (streaming-json ndjson) ---------------------------------------

run <<'EOF'
{"type":"text","data":"stale turn-one prose that a tool call supersedes"}
{"type":"tool_call","data":{"name":"bash"}}
{"type":"text","data":"REPORT PART ONE "}
{"type":"text","data":"AND PART TWO"}
EOF
ok "grok: text after the last tool event, concatenated" 0 "REPORT PART ONE AND PART TWO" "stale turn-one"

run <<'EOF'
{"type":"text","data":"first "}
{"type":"tool_call","data":{}}
{"type":"text","data":"middle"}
{"type":"tool_call_update","data":{}}
{"type":"text","data":"THE REAL TAIL"}
EOF
ok "grok: tool_call_update also resets the window" 0 "THE REAL TAIL" "middle"

run <<'EOF'
{"type":"text","data":"a toolless final answer"}
EOF
ok "grok: a log with no tool events still yields its text" 0 "a toolless final answer" "-"

run <<'EOF'
{"type":"tool_call","data":{}}
EOF
ok "grok: tool call then silence is a died round (exit 65)" 65 "no report-shaped content" "-"

# ---- shared edges ------------------------------------------------------------

run <<'EOF'
not json at all
{"type":"result","result":"SURVIVES GARBAGE"}
also not json
EOF
ok "non-JSON lines are skipped, not fatal" 0 "SURVIVES GARBAGE" "-"

run --max-chars 10 <<'EOF'
{"type":"result","result":"0123456789ABCDEF"}
EOF
ok "--max-chars truncates and says so" 0 "truncated at 10" "ABCDEF"

printf '' > "$TMP/log"
out="$("$BIN" "$TMP/log" 2>&1)"; rc=$?
ok "an empty log is a died round, not a crash" 65 "no report-shaped content" "-"

out="$("$BIN" "$TMP/definitely-absent" 2>&1)"; rc=$?
ok "a missing file is its own error (66)" 66 "unreadable" "-"

# ---- sentinel / registry diagnosis on exit 65 --------------------------------
printf '' > "$TMP/killed.log"
printf 'rc=-1\nfinished=2026-08-22T17:08:28Z\nwrapper_signal=TERM\n' > "$TMP/killed.log.rc"
out="$("$BIN" "$TMP/killed.log" 2>&1)"; rc=$?
ok "killed sentinel is named on exit 65" 65 "killed (TERM)" "-"

printf '' > "$TMP/running.log"
mkdir -p "$OUTSOURCE_RUNS_DIR"
printf 'id=shell-running\npid=%s\nlabel=live\nlog=%s\nstartedAt=1\n' "$$" "$TMP/running.log" \
  > "$OUTSOURCE_RUNS_DIR/shell-running.run"
out="$("$BIN" "$TMP/running.log" 2>&1)"; rc=$?
ok "running round is named on exit 65" 65 "round still running" "-"

printf '\nlast-report: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
