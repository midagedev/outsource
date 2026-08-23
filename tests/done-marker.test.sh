#!/usr/bin/env bash
# --done-marker must mean the same thing on both launchers.
#
# Field incident this pins (2026-08-18, three times in one session): a
# finished round whose report lacked the marker was recorded as
# done_marker=absent in both sentinels, but the *exit codes* disagreed —
# grok-run.sh downgraded rc=0 to 70, outsource-run.sh left rc=0. Watchers
# then announced the same fact as "failed with exit code 70" or "completed"
# depending on which sister they launched. 70 was already the documented
# model-identity failure, so the grok path also collided with that meaning.
#
# A second incident, same session, sits upstream of that: three rounds
# launched with --done-marker whose specs never contained the string. The
# delegate cannot print a token it was never told to print, so every one
# of those delivered rounds reported absent. That is a usage error by the
# caller, refused at 64 before the provider is contacted.
#
# A third: grok-run.sh greps the final report (via last-report.sh);
# outsource-run.sh grepped the whole log, so a plan that quoted the marker
# counted as found. done_marker=found then meant two different things.
#
# Asserted here:
#   - marker present in spec + report  → rc stays 0, sentinel found
#   - marker present in spec, absent from report → dedicated code 72,
#     sentinel absent, stderr names the missing string
#   - both launchers emit that same code and the same-intent line
#   - 70 is still only the model-identity assertion (regression)
#   - --done-marker X with a spec that does not contain X → both launchers
#     refuse at 64, name the string and the spec path, never invoke the
#     provider, never register a round
#   - a log whose plan quotes the marker but whose final report does not
#     → done_marker=absent, done_marker_scope=report, on both launchers
#   - --done-marker together with --json-schema → refused at 64 before the
#     provider is contacted (a schema round's report IS the JSON object, so
#     the marker can never appear); --json-schema alone still launches
#
# Usage: tests/done-marker.test.sh   (exit 0 = all pass)
set -uo pipefail

HERE="$(cd "$(dirname "$0")/.." && pwd)"
GROK_RUN="$HERE/skills/outsource/bin/grok-run.sh"
OUT_RUN="$HERE/skills/outsource/bin/outsource-run.sh"
[ -x "$GROK_RUN" ] || { echo "not executable: $GROK_RUN" >&2; exit 2; }
[ -x "$OUT_RUN" ]  || { echo "not executable: $OUT_RUN" >&2; exit 2; }

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
mkdir -p "$TMP/state"
export XDG_STATE_HOME="$TMP/state"
export OUTSOURCE_RUNS_DIR="$TMP/runs"
export GROK_RUN_STARTUP_GRACE=10
export ZAI_API_KEY="test-key-not-a-real-credential"
LIVE_PATH="$PATH"

MARKER="DONE-MARKER-CONTRACT"
# Shared payload both launchers must print on a missing marker (prefix may
# differ: grok-run.sh: vs outsource:). The test greps this, not a tone.
INTENT="the round finished but --done-marker '$MARKER' is absent; not claiming a pass (exit 72). Judge by the tree, not this exit code."
# Shared payload for the preflight refuse (prefix may differ).
PREFLIGHT_INTENT="--done-marker '$MARKER' does not appear in the spec"
PREFLIGHT_FIX="Add that exact string as the spec's last line (the completion marker), then relaunch."

mkdir -p "$TMP/cwd"
# Spec carries the marker so a --done-marker launch is satisfiable. The
# preflight cases below use a separate file that does not.
printf 'do the thing\n%s\n' "$MARKER" > "$TMP/spec.md"
printf 'do the thing\n' > "$TMP/spec-no-marker.md"

# Hermetic by default: stub model CLIs, real launchers. Live grok rounds
# (the 2026-08-22 MARKER-CONTRACT flake: model omitted the last-line marker,
# launcher correctly scored absent/72, test went red) stay behind
# OUTSOURCE_LIVE_TESTS=1.
# shellcheck source=fake-backend.sh
. "$HERE/tests/fake-backend.sh"

pass=0
fail=0
note() { fail=$((fail + 1)); echo "FAIL  $*" >&2; }

# A plan that quotes the marker, then a tool event, then a final report
# that does not. last-report.sh yields only the tail; a log-wide grep
# matches the plan and lies "found".
PLAN_QUOTES_MARKER="$(printf '%s\n%s\n%s' \
  "{\"type\":\"text\",\"data\":\"planning: I will print ${MARKER} at the end\"}" \
  '{"type":"tool_call","data":{"name":"bash"}}' \
  '{"type":"text","data":"final report, work is done, no completion token"}')"

# ── grok-run.sh ──────────────────────────────────────────────────────────

run_grok() {  # <label> <text-in-report>
  local label="$1"
  unset FAKE_GROK_NDJSON
  FAKE_GROK_TEXT="$2"
  export FAKE_GROK_TEXT
  LOG="$TMP/${label}.ndjson"
  rm -f "$LOG" "$LOG.rc"
  set +e
  GROK_ERR="$TMP/${label}.wrapper.err"
  bash "$GROK_RUN" --foreground --cwd "$TMP/cwd" --spec "$TMP/spec.md" --log "$LOG" \
    --label "$label" --done-marker "$MARKER" >"$TMP/${label}.out" 2>"$GROK_ERR"
  GROK_RC=$?
  set -e
}

run_grok "grok-found" "$(printf 'report body\n%s' "$MARKER")"
if [ "$GROK_RC" -eq 0 ]; then
  grep -q '^done_marker=found$' "$LOG.rc" \
    || note "grok found: sentinel is not done_marker=found: $(cat "$LOG.rc")"
  pass=$((pass + 1))
else
  note "grok found: rc=$GROK_RC want=0; sentinel=$(cat "$LOG.rc" 2>/dev/null); err=$(cat "$GROK_ERR")"
fi

run_grok "grok-absent" "report body, no completion token"
if [ "$GROK_RC" -eq 72 ]; then
  grep -q '^done_marker=absent$' "$LOG.rc" \
    || note "grok absent: sentinel is not done_marker=absent: $(cat "$LOG.rc")"
  grep -qF -- "$INTENT" "$GROK_ERR" \
    || note "grok absent: stderr missing the shared intent line: $(cat "$GROK_ERR")"
  grep -qF -- "$MARKER" "$GROK_ERR" \
    || note "grok absent: stderr does not name the missing marker"
  pass=$((pass + 1))
else
  note "grok absent: rc=$GROK_RC want=72; sentinel=$(cat "$LOG.rc" 2>/dev/null); err=$(cat "$GROK_ERR")"
fi
GROK_ABSENT_RC=$GROK_RC
GROK_ABSENT_ERR="$(cat "$GROK_ERR")"

# ── outsource-run.sh (crush harness: no model-identity assertion) ────────

run_glm() {  # <label> <log-body>
  local label="$1"
  FAKE_CRUSH_OUTPUT="$2"
  export FAKE_CRUSH_OUTPUT
  LOG="$TMP/${label}.log"
  rm -f "$LOG" "$LOG.rc"
  set +e
  GLM_ERR="$TMP/${label}.wrapper.err"
  bash "$OUT_RUN" --foreground --cwd "$TMP/cwd" --spec "$TMP/spec.md" --log "$LOG" \
    --harness crush --label "$label" --done-marker "$MARKER" \
    --config-dir "$TMP/cfg-$label" >"$TMP/${label}.out" 2>"$GLM_ERR"
  GLM_RC=$?
  set -e
}

run_glm "glm-found" "crush log $MARKER tail"
if [ "$GLM_RC" -eq 0 ]; then
  grep -q '^done_marker=found' "$LOG.rc" \
    || note "glm found: sentinel is not done_marker=found: $(cat "$LOG.rc")"
  pass=$((pass + 1))
else
  note "glm found: rc=$GLM_RC want=0; sentinel=$(cat "$LOG.rc" 2>/dev/null); err=$(cat "$GLM_ERR")"
fi

run_glm "glm-absent" "crush log, no completion token"
if [ "$GLM_RC" -eq 72 ]; then
  grep -q '^done_marker=absent' "$LOG.rc" \
    || note "glm absent: sentinel is not done_marker=absent: $(cat "$LOG.rc")"
  grep -qF -- "$INTENT" "$GLM_ERR" \
    || note "glm absent: stderr missing the shared intent line: $(cat "$GLM_ERR")"
  grep -qF -- "$MARKER" "$GLM_ERR" \
    || note "glm absent: stderr does not name the missing marker"
  pass=$((pass + 1))
else
  note "glm absent: rc=$GLM_RC want=72; sentinel=$(cat "$LOG.rc" 2>/dev/null); err=$(cat "$GLM_ERR")"
fi
GLM_ABSENT_RC=$GLM_RC
GLM_ABSENT_ERR="$(cat "$GLM_ERR")"

# ── the round's core assertion: same code, same-intent line ──────────────
if [ "$GROK_ABSENT_RC" -eq 72 ] && [ "$GLM_ABSENT_RC" -eq 72 ] \
   && printf '%s' "$GROK_ABSENT_ERR" | grep -qF -- "$INTENT" \
   && printf '%s' "$GLM_ABSENT_ERR" | grep -qF -- "$INTENT"; then
  pass=$((pass + 1))
else
  note "parity: grok rc=$GROK_ABSENT_RC glm rc=$GLM_ABSENT_RC (want both 72 and the shared intent line)"
  echo "      grok stderr: $GROK_ABSENT_ERR" >&2
  echo "      glm  stderr: $GLM_ABSENT_ERR" >&2
fi

# ── 70 stays model-identity (unverifiable transcript) ────────────────────
ID_LOG="$TMP/identity.log"
rm -f "$ID_LOG" "$ID_LOG.rc"
set +e
bash "$OUT_RUN" --foreground --cwd "$TMP/cwd" --spec "$TMP/spec.md" --log "$ID_LOG" \
  --harness claude-code --label identity-70 --done-marker "$MARKER" \
  --config-dir "$TMP/cfg-identity" >"$TMP/identity.out" 2>"$TMP/identity.err"
ID_RC=$?
set -e
if [ "$ID_RC" -eq 70 ]; then
  grep -q 'MODEL ASSERTION FAILED' "$TMP/identity.err" \
    || note "identity 70: stderr does not name the model-identity failure: $(cat "$TMP/identity.err")"
  # Marker-absent must not steal 70. The sentinel may still record absent.
  [ "$ID_RC" -ne 72 ] || note "identity 70: marker path overwrote the assertion rc"
  pass=$((pass + 1))
else
  note "identity 70: rc=$ID_RC want=70; err=$(cat "$TMP/identity.err"); sentinel=$(cat "$ID_LOG.rc" 2>/dev/null)"
fi

# ── vision refusal copy (condition unchanged; wording must distinguish) ──
printf 'wire a capture harness; write frames/shot.png then decode pixels\n' > "$TMP/vision-spec.md"
set +e
bash "$OUT_RUN" --foreground --cwd "$TMP/cwd" --spec "$TMP/vision-spec.md" --log "$TMP/vision.log" \
  --label vision-copy --config-dir "$TMP/cfg-vision" \
  >"$TMP/vision.out" 2>"$TMP/vision.err"
VIS_RC=$?
set -e
if [ "$VIS_RC" -eq 65 ] \
   && grep -q -- '--no-vision-check' "$TMP/vision.err" \
   && grep -qi 'verdict' "$TMP/vision.err"; then
  pass=$((pass + 1))
else
  note "vision copy: rc=$VIS_RC want=65 plus --no-vision-check and a verdict/artifact distinction; err=$(cat "$TMP/vision.err")"
fi

# ── Part 1: refuse an unsatisfiable marker contract before the round ─────
# The spec does not contain MARKER. Both launchers must exit 64, name the
# string and the spec path, tell the lead to add the last line, and never
# invoke the provider (the canary is the proof — a log would also exist).

run_preflight_grok() {
  unset FAKE_GROK_NDJSON FAKE_GROK_TEXT
  export FAKE_PROVIDER_CANARY="$TMP/preflight-grok.canary"
  rm -f "$FAKE_PROVIDER_CANARY"
  LOG="$TMP/preflight-grok.ndjson"
  rm -f "$LOG" "$LOG.rc" "${LOG%.ndjson}.sid"
  set +e
  bash "$GROK_RUN" --foreground --cwd "$TMP/cwd" --spec "$TMP/spec-no-marker.md" --log "$LOG" \
    --label preflight-grok --done-marker "$MARKER" \
    >"$TMP/preflight-grok.out" 2>"$TMP/preflight-grok.err"
  GROK_PRE_RC=$?
  set -e
}

run_preflight_glm() {
  unset FAKE_CRUSH_OUTPUT
  export FAKE_PROVIDER_CANARY="$TMP/preflight-glm.canary"
  rm -f "$FAKE_PROVIDER_CANARY"
  LOG="$TMP/preflight-glm.log"
  rm -f "$LOG" "$LOG.rc"
  set +e
  bash "$OUT_RUN" --foreground --cwd "$TMP/cwd" --spec "$TMP/spec-no-marker.md" --log "$LOG" \
    --harness crush --label preflight-glm --done-marker "$MARKER" \
    --config-dir "$TMP/cfg-preflight-glm" \
    >"$TMP/preflight-glm.out" 2>"$TMP/preflight-glm.err"
  GLM_PRE_RC=$?
  set -e
}

run_registered() {  # <label> — 0 if runs.sh recorded this launch
  [ -d "$OUTSOURCE_RUNS_DIR" ] || return 1
  grep -l -- "label=$1" "$OUTSOURCE_RUNS_DIR"/*.run >/dev/null 2>&1
}

run_preflight_grok
if [ "$GROK_PRE_RC" -eq 64 ] \
   && grep -qF -- "$PREFLIGHT_INTENT" "$TMP/preflight-grok.err" \
   && grep -qF -- "$PREFLIGHT_FIX" "$TMP/preflight-grok.err" \
   && grep -qF -- "$TMP/spec-no-marker.md" "$TMP/preflight-grok.err" \
   && grep -qF -- "$MARKER" "$TMP/preflight-grok.err" \
   && [ ! -e "$TMP/preflight-grok.canary" ] \
   && [ ! -e "$TMP/preflight-grok.ndjson" ] \
   && [ ! -e "$TMP/preflight-grok.ndjson.rc" ] \
   && ! run_registered preflight-grok; then
  pass=$((pass + 1))
else
  note "grok preflight: rc=$GROK_PRE_RC want=64; canary=$([ -e "$TMP/preflight-grok.canary" ] && echo yes || echo no); log=$([ -e "$TMP/preflight-grok.ndjson" ] && echo yes || echo no); err=$(cat "$TMP/preflight-grok.err" 2>/dev/null)"
fi

unset FAKE_PROVIDER_CANARY
run_preflight_glm
if [ "$GLM_PRE_RC" -eq 64 ] \
   && grep -qF -- "$PREFLIGHT_INTENT" "$TMP/preflight-glm.err" \
   && grep -qF -- "$PREFLIGHT_FIX" "$TMP/preflight-glm.err" \
   && grep -qF -- "$TMP/spec-no-marker.md" "$TMP/preflight-glm.err" \
   && grep -qF -- "$MARKER" "$TMP/preflight-glm.err" \
   && [ ! -e "$TMP/preflight-glm.canary" ] \
   && [ ! -e "$TMP/preflight-glm.log" ] \
   && [ ! -e "$TMP/preflight-glm.log.rc" ] \
   && ! run_registered preflight-glm; then
  pass=$((pass + 1))
else
  note "glm preflight: rc=$GLM_PRE_RC want=64; canary=$([ -e "$TMP/preflight-glm.canary" ] && echo yes || echo no); log=$([ -e "$TMP/preflight-glm.log" ] && echo yes || echo no); err=$(cat "$TMP/preflight-glm.err" 2>/dev/null)"
fi
unset FAKE_PROVIDER_CANARY

if [ "$GROK_PRE_RC" -eq 64 ] && [ "$GLM_PRE_RC" -eq 64 ] \
   && grep -qF -- "$PREFLIGHT_INTENT" "$TMP/preflight-grok.err" \
   && grep -qF -- "$PREFLIGHT_INTENT" "$TMP/preflight-glm.err"; then
  pass=$((pass + 1))
else
  note "preflight parity: grok rc=$GROK_PRE_RC glm rc=$GLM_PRE_RC (want both 64 and the shared intent line)"
  echo "      grok stderr: $(cat "$TMP/preflight-grok.err" 2>/dev/null)" >&2
  echo "      glm  stderr: $(cat "$TMP/preflight-glm.err" 2>/dev/null)" >&2
fi

# ── Part 2: plan quotes the marker, final report does not → absent ───────
# Same log shape on both launchers (grok ndjson / last-report-readable
# JSONL). A whole-log grep would report found; the report-scope check
# must not.

run_grok_ndjson() {  # <label> <ndjson>
  local label="$1"
  unset FAKE_GROK_TEXT
  FAKE_GROK_NDJSON="$2"
  export FAKE_GROK_NDJSON
  LOG="$TMP/${label}.ndjson"
  rm -f "$LOG" "$LOG.rc"
  set +e
  GROK_ERR="$TMP/${label}.wrapper.err"
  bash "$GROK_RUN" --foreground --cwd "$TMP/cwd" --spec "$TMP/spec.md" --log "$LOG" \
    --label "$label" --done-marker "$MARKER" >"$TMP/${label}.out" 2>"$GROK_ERR"
  GROK_RC=$?
  set -e
}

run_grok_ndjson "grok-plan-quote" "$PLAN_QUOTES_MARKER"
if [ "$GROK_RC" -eq 72 ] \
   && grep -q '^done_marker=absent$' "$LOG.rc" \
   && grep -q '^done_marker_scope=report$' "$LOG.rc" \
   && grep -qF -- "$INTENT" "$GROK_ERR"; then
  pass=$((pass + 1))
else
  note "grok plan-quote: rc=$GROK_RC want=72+absent+scope=report; sentinel=$(cat "$LOG.rc" 2>/dev/null); err=$(cat "$GROK_ERR")"
fi
GROK_PLAN_RC=$GROK_RC
GROK_PLAN_SENTINEL="$(cat "$LOG.rc" 2>/dev/null || true)"

run_glm "glm-plan-quote" "$PLAN_QUOTES_MARKER"
if [ "$GLM_RC" -eq 72 ] \
   && grep -q '^done_marker=absent' "$LOG.rc" \
   && grep -q '^done_marker_scope=report$' "$LOG.rc" \
   && grep -qF -- "$INTENT" "$GLM_ERR"; then
  pass=$((pass + 1))
else
  note "glm plan-quote: rc=$GLM_RC want=72+absent+scope=report; sentinel=$(cat "$LOG.rc" 2>/dev/null); err=$(cat "$GLM_ERR")"
fi
GLM_PLAN_RC=$GLM_RC
GLM_PLAN_SENTINEL="$(cat "$LOG.rc" 2>/dev/null || true)"

if [ "$GROK_PLAN_RC" -eq 72 ] && [ "$GLM_PLAN_RC" -eq 72 ] \
   && printf '%s\n' "$GROK_PLAN_SENTINEL" | grep -q '^done_marker_scope=report$' \
   && printf '%s\n' "$GLM_PLAN_SENTINEL" | grep -q '^done_marker_scope=report$'; then
  pass=$((pass + 1))
else
  note "plan-quote parity: grok rc=$GROK_PLAN_RC glm rc=$GLM_PLAN_RC (want both 72, both scope=report)"
  echo "      grok sentinel: $GROK_PLAN_SENTINEL" >&2
  echo "      glm  sentinel: $GLM_PLAN_SENTINEL" >&2
fi

# ── Part 3: --done-marker + --json-schema is a contradiction, not a round ─
# Field incident (2026-08-18): a vision round launched with both flags
# returned a complete, schema-valid verdict and still exited 72
# done_marker=absent. Under a schema the final report *is* the JSON object,
# so a sentinel line beside it would violate the schema the same flag
# imposes — the marker can never be found. The lead read a false failure.
# Refused at 64 before the provider is contacted (canary proves it), even
# though the spec here *does* contain the marker: the spec-contains check
# cannot see this contradiction, so it needs its own guard.
export FAKE_PROVIDER_CANARY="$TMP/schema-marker.canary"
rm -f "$FAKE_PROVIDER_CANARY"
SCHEMA_LOG="$TMP/schema-marker.ndjson"
rm -f "$SCHEMA_LOG" "$SCHEMA_LOG.rc"
set +e
bash "$GROK_RUN" --foreground --cwd "$TMP/cwd" --spec "$TMP/spec.md" --log "$SCHEMA_LOG" \
  --label schema-marker --done-marker "$MARKER" \
  -- --json-schema '{"type":"object"}' \
  >"$TMP/schema-marker.out" 2>"$TMP/schema-marker.err"
SCHEMA_RC=$?
set -e
if [ "$SCHEMA_RC" -eq 64 ] \
   && grep -qF -- '--json-schema' "$TMP/schema-marker.err" \
   && grep -qF -- '--done-marker' "$TMP/schema-marker.err" \
   && grep -qi 'mutually exclusive' "$TMP/schema-marker.err" \
   && [ ! -e "$TMP/schema-marker.canary" ] \
   && [ ! -e "$SCHEMA_LOG" ] \
   && [ ! -e "$SCHEMA_LOG.rc" ] \
   && ! run_registered schema-marker; then
  pass=$((pass + 1))
else
  note "schema+marker: rc=$SCHEMA_RC want=64; canary=$([ -e "$TMP/schema-marker.canary" ] && echo yes || echo no); log=$([ -e "$SCHEMA_LOG" ] && echo yes || echo no); err=$(cat "$TMP/schema-marker.err" 2>/dev/null)"
fi

# A schema round WITHOUT --done-marker must still launch normally — the
# guard must key on the pair, not on --json-schema alone.
unset FAKE_PROVIDER_CANARY FAKE_GROK_NDJSON
FAKE_GROK_TEXT='{"verdict":"SHIP"}'
export FAKE_GROK_TEXT
SCHEMA_OK_LOG="$TMP/schema-nomarker.ndjson"
rm -f "$SCHEMA_OK_LOG" "$SCHEMA_OK_LOG.rc"
set +e
bash "$GROK_RUN" --foreground --cwd "$TMP/cwd" --spec "$TMP/spec.md" --log "$SCHEMA_OK_LOG" \
  --label schema-nomarker \
  -- --json-schema '{"type":"object"}' \
  >"$TMP/schema-nomarker.out" 2>"$TMP/schema-nomarker.err"
SCHEMA_OK_RC=$?
set -e
if [ "$SCHEMA_OK_RC" -eq 0 ] && [ -e "$SCHEMA_OK_LOG.rc" ] \
   && ! grep -q '^done_marker=' "$SCHEMA_OK_LOG.rc"; then
  pass=$((pass + 1))
else
  note "schema without marker: rc=$SCHEMA_OK_RC want=0 and no done_marker key; sentinel=$(cat "$SCHEMA_OK_LOG.rc" 2>/dev/null); err=$(cat "$TMP/schema-nomarker.err" 2>/dev/null)"
fi

# ── non-TTY refusal without --foreground names --detach (contract 2) ─────
# Two stdin shapes, both must refuse at 64 BEFORE the fake provider runs:
# a pipe (`echo |`), and /dev/null — which IS a char device on Unix and
# therefore slipped past a bare ModeCharDevice test (measured 2026-08-23:
# a Claude-harness Bash wires stdin to /dev/null, and a live foreground
# round sailed through the guard built for exactly that harness).
export FAKE_PROVIDER_CANARY="$TMP/tty-refuse.canary"
rm -f "$FAKE_PROVIDER_CANARY"
set +e
echo | bash "$GROK_RUN" --cwd "$TMP/cwd" --spec "$TMP/spec.md" --log "$TMP/tty-grok.ndjson" \
  --label tty-refuse-grok >"$TMP/tty-grok.out" 2>"$TMP/tty-grok.err"
TTY_GROK_RC=$?
echo | bash "$OUT_RUN" --cwd "$TMP/cwd" --spec "$TMP/spec.md" --log "$TMP/tty-glm.log" \
  --harness crush --label tty-refuse-glm --config-dir "$TMP/cfg-tty" \
  >"$TMP/tty-glm.out" 2>"$TMP/tty-glm.err"
TTY_GLM_RC=$?
set -e
if [ "$TTY_GROK_RC" -eq 64 ] && [ "$TTY_GLM_RC" -eq 64 ] \
   && grep -qF -- '--detach' "$TMP/tty-grok.err" \
   && grep -qF -- '--foreground' "$TMP/tty-grok.err" \
   && grep -qF -- '--detach' "$TMP/tty-glm.err" \
   && [ ! -e "$FAKE_PROVIDER_CANARY" ] \
   && [ ! -e "$TMP/tty-grok.ndjson.rc" ] \
   && [ ! -e "$TMP/tty-glm.log.rc" ]; then
  pass=$((pass + 1))
else
  note "non-TTY refusal: grok rc=$TTY_GROK_RC glm rc=$TTY_GLM_RC want=64+--detach; grok-err=$(cat "$TMP/tty-grok.err" 2>/dev/null); glm-err=$(cat "$TMP/tty-glm.err" 2>/dev/null)"
fi

# /dev/null stdin — the real harness shape. FAIL-first 2026-08-23: with the
# bare char-device test this case launched the round instead of refusing.
export FAKE_PROVIDER_CANARY="$TMP/devnull-refuse.canary"
rm -f "$FAKE_PROVIDER_CANARY"
set +e
bash "$GROK_RUN" --cwd "$TMP/cwd" --spec "$TMP/spec.md" --log "$TMP/devnull-grok.ndjson" \
  --label devnull-refuse-grok >"$TMP/devnull-grok.out" 2>"$TMP/devnull-grok.err" < /dev/null
DEVNULL_GROK_RC=$?
bash "$OUT_RUN" --cwd "$TMP/cwd" --spec "$TMP/spec.md" --log "$TMP/devnull-glm.log" \
  --harness crush --label devnull-refuse-glm --config-dir "$TMP/cfg-devnull" \
  >"$TMP/devnull-glm.out" 2>"$TMP/devnull-glm.err" < /dev/null
DEVNULL_GLM_RC=$?
set -e
if [ "$DEVNULL_GROK_RC" -eq 64 ] && [ "$DEVNULL_GLM_RC" -eq 64 ] \
   && grep -qF -- '--detach' "$TMP/devnull-grok.err" \
   && grep -qF -- '--detach' "$TMP/devnull-glm.err" \
   && [ ! -e "$FAKE_PROVIDER_CANARY" ] \
   && [ ! -e "$TMP/devnull-grok.ndjson.rc" ] \
   && [ ! -e "$TMP/devnull-glm.log.rc" ]; then
  pass=$((pass + 1))
else
  note "devnull refusal: grok rc=$DEVNULL_GROK_RC glm rc=$DEVNULL_GLM_RC want=64; grok-err=$(cat "$TMP/devnull-grok.err" 2>/dev/null); glm-err=$(cat "$TMP/devnull-glm.err" 2>/dev/null)"
fi
unset FAKE_PROVIDER_CANARY

# ── outsource-run --detach: parent returns, child writes the sentinel ────
FAKE_CRUSH_OUTPUT="crush log $MARKER tail"
export FAKE_CRUSH_OUTPUT
DETACH_LOG="$TMP/detach.log"
rm -f "$DETACH_LOG" "$DETACH_LOG.rc"
set +e
bash "$OUT_RUN" --detach --cwd "$TMP/cwd" --spec "$TMP/spec.md" --log "$DETACH_LOG" \
  --harness crush --label detach-glm --done-marker "$MARKER" \
  --config-dir "$TMP/cfg-detach" >"$TMP/detach.out" 2>"$TMP/detach.err"
DETACH_RC=$?
set -e
for _ in $(seq 1 50); do [ -f "$DETACH_LOG.rc" ] && break; sleep 0.1; done
if [ "$DETACH_RC" -eq 0 ] \
   && grep -q 'detached (pid=' "$TMP/detach.out" \
   && [ -f "$DETACH_LOG.rc" ] \
   && grep -q '^done_marker=found' "$DETACH_LOG.rc"; then
  pass=$((pass + 1))
else
  note "outsource-run --detach: rc=$DETACH_RC want=0+detached+sentinel found; out=$(cat "$TMP/detach.out" 2>/dev/null); err=$(cat "$TMP/detach.err" 2>/dev/null); sentinel=$(cat "$DETACH_LOG.rc" 2>/dev/null)"
fi

# Live grok rounds spend quota and flake on model behavior (2026-08-22
# MARKER-CONTRACT: the launcher correctly scored absent → 72; the test
# expected found). The hermetic grok-absent case above pins that verdict.
if [ "${OUTSOURCE_LIVE_TESTS:-}" = "1" ]; then
  echo "done-marker: OUTSOURCE_LIVE_TESTS=1 — live grok round (spends quota)"
  LIVE_LOG="$TMP/live.ndjson"
  rm -f "$LIVE_LOG" "$LIVE_LOG.rc"
  set +e
  env PATH="$LIVE_PATH" bash "$GROK_RUN" --foreground --cwd "$TMP/cwd" \
    --spec "$TMP/spec.md" --log "$LIVE_LOG" --label live-marker \
    --done-marker "$MARKER" >"$TMP/live.out" 2>"$TMP/live.err"
  LIVE_RC=$?
  set -e
  if [ -f "$LIVE_LOG.rc" ] && grep -q '^done_marker=' "$LIVE_LOG.rc"; then
    pass=$((pass + 1))
  else
    note "live grok: rc=$LIVE_RC no sentinel/done_marker; err=$(cat "$TMP/live.err" 2>/dev/null)"
  fi
else
  echo "done-marker: skipping live grok rounds (set OUTSOURCE_LIVE_TESTS=1 to enable; they spend quota and are flaky on model behavior, not launcher behavior)"
fi

echo "done-marker: $pass passed, $fail failed"
[ "$fail" -eq 0 ]
