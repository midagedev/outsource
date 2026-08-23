#!/usr/bin/env bash
# grok-run.sh must survive a signal to the wrapper and still write the
# sentinel — the round's completion evidence.
#
# Field incident this pins (2026-08-17, second occurrence of the class): a
# lead ran grok-run.sh in the foreground and the caller's own timeout
# SIGTERMed the wrapper at 2 minutes. The grok child survived and finished
# the round correctly, but the sentinel writer lived in the dead wrapper, so
# `<log>.rc` never appeared and every watcher keyed on it waited forever.
# The first occurrence (same week) lost a sentinel to a dying wrapper shell
# the same way. "Foreground by design" makes the wrapper the caller's to
# kill — so the wrapper must treat TERM/INT/HUP as "hold and finish the
# paperwork", never as "abandon the evidence".
#
# Asserted here:
#   - TERM to the wrapper mid-round: wrapper keeps waiting, child finishes,
#     sentinel exists with the child's rc and a wrapper_signal breadcrumb.
#   - No signal: sentinel written as before (the fix must not regress the
#     normal path).
#
# Usage: tests/grok-run-signal.test.sh   (exit 0 = all pass)
set -uo pipefail

GROK_RUN="$(cd "$(dirname "$0")/.." && pwd)/skills/outsource/bin/grok-run.sh"
[ -x "$GROK_RUN" ] || { echo "not executable: $GROK_RUN" >&2; exit 2; }

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
mkdir -p "$TMP/state"
export XDG_STATE_HOME="$TMP/state"
export OUTSOURCE_RUNS_DIR="$TMP/runs"
export GROK_RUN_STARTUP_GRACE=10

# Fake grok: emits ndjson, then outlives the window in which the wrapper is
# signalled, then finishes clean. Ignores every flag grok-run.sh passes.
mkdir -p "$TMP/bin"
cat > "$TMP/bin/grok" <<'FAKE'
#!/usr/bin/env bash
echo '{"type":"text","data":"working"}'
sleep 3
echo '{"type":"end","stopReason":"end_turn"}'
exit 0
FAKE
chmod +x "$TMP/bin/grok"
export PATH="$TMP/bin:$PATH"

echo "do the thing" > "$TMP/spec.md"
mkdir -p "$TMP/cwd"

pass=0
fail=0
note() { fail=$((fail + 1)); echo "FAIL  $*" >&2; }

# ── 1. TERM to the wrapper mid-round: sentinel still appears ─────────────
LOG="$TMP/signal.ndjson"
bash "$GROK_RUN" --foreground --cwd "$TMP/cwd" --spec "$TMP/spec.md" --log "$LOG" \
  --label signal-case >/dev/null 2>&1 &
WRAP=$!
for _ in $(seq 1 50); do [ -s "$LOG" ] && break; sleep 0.2; done
[ -s "$LOG" ] || note "fake grok never wrote the log"
kill -TERM "$WRAP" 2>/dev/null
wait "$WRAP" 2>/dev/null
# The child needs its full 3 s regardless of what the wrapper did.
for _ in $(seq 1 60); do [ -f "$LOG.rc" ] && break; sleep 0.2; done

if [ -f "$LOG.rc" ]; then
  grep -q '^rc=0$' "$LOG.rc" || note "signal case: sentinel rc is not the child's 0: $(cat "$LOG.rc")"
  grep -q '^wrapper_signal=TERM$' "$LOG.rc" || note "signal case: no wrapper_signal=TERM breadcrumb"
  pass=$((pass + 1))
else
  note "signal case: no sentinel after wrapper TERM (the incident, reproduced)"
fi

# ── 2. Normal path unregressed: no signal, sentinel as before ────────────
LOG2="$TMP/normal.ndjson"
bash "$GROK_RUN" --foreground --cwd "$TMP/cwd" --spec "$TMP/spec.md" --log "$LOG2" \
  --label normal-case >/dev/null 2>&1
if [ -f "$LOG2.rc" ]; then
  grep -q '^rc=0$' "$LOG2.rc" || note "normal case: rc is not 0: $(cat "$LOG2.rc")"
  grep -q '^wrapper_signal=' "$LOG2.rc" && note "normal case: breadcrumb present without a signal"
  pass=$((pass + 1))
else
  note "normal case: no sentinel"
fi

echo "grok-run-signal: $pass passed, $fail failed"
[ "$fail" -eq 0 ]
