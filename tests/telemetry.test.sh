#!/usr/bin/env bash
# End-to-end: what the telemetry log is allowed to contain.
#
# The unit tests cover the extraction rule. This covers the claim that matters —
# that after real invocations carrying real paths, spec text and marker names,
# none of it is in the file. A privacy property asserted only at the function that
# implements it is a property that survives until someone adds a second writer.
set -uo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.." || exit 2
BIN="$(pwd)/skills/outsource/bin/outsource"
[ -x "$BIN" ] || { echo "telemetry: no binary at $BIN — run ./build.sh" >&2; exit 2; }

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
mkdir -p "$TMP/state"
export XDG_STATE_HOME="$TMP/state"
export OUTSOURCE_TELEMETRY_FILE="$TMP/telemetry.jsonl"
export OUTSOURCE_RUNS_DIR="$TMP/runs"

# Identifying strings planted in every channel a tool reads: the path, the spec
# body, a marker name, and a label.
SECRET_DIR="$TMP/acquisition-diligence-2026"
mkdir -p "$SECRET_DIR"
printf 'the codename is BLUEHERON and the buyer is unnamed\n' > "$SECRET_DIR/plan.md"

pass=0; fail=0
ok()  { pass=$((pass+1)); }
bad() { fail=$((fail+1)); printf 'FAIL  %s\n' "$1" >&2; }

# Calls that fail on purpose, since a failure records the most detail.
"$BIN" outsource-run --foreground --cwd "$SECRET_DIR" --spec "$SECRET_DIR/plan.md" \
  --harness crush --label BLUEHERON-track --done-marker DONE-BLUEHERON >/dev/null 2>&1
"$BIN" spec-lint --root "$SECRET_DIR" "$SECRET_DIR/plan.md" >/dev/null 2>&1
"$BIN" last-report "$SECRET_DIR/plan.md" >/dev/null 2>&1
CRUSH_TOOL_INPUT_COMMAND="git -C $SECRET_DIR commit -am BLUEHERON" \
  "$BIN" guard </dev/null >/dev/null 2>&1

[ -s "$OUTSOURCE_TELEMETRY_FILE" ] && ok || bad "nothing was recorded at all"

for leak in "acquisition-diligence-2026" "BLUEHERON" "plan.md" "$TMP" "codename" "buyer"; do
  if grep -qF -- "$leak" "$OUTSOURCE_TELEMETRY_FILE" 2>/dev/null; then
    bad "the log contains '$leak' — values, paths and spec text must never be recorded"
  else
    ok
  fi
done

# The signal must still be there, or the privacy is free and the log is useless.
for want in '"tool":"outsource-run"' '"--done-marker"' '"tool":"guard"' '"why":"blocked git"'; do
  grep -qF -- "$want" "$OUTSOURCE_TELEMETRY_FILE" && ok \
    || bad "the log is missing $want — flag names and reasons are the point"
done

# Every line must be one parseable object; a summary that skips malformed lines
# would hide a writer that corrupts the file.
if python3 - "$OUTSOURCE_TELEMETRY_FILE" <<'PY'
import json, sys
for i, line in enumerate(open(sys.argv[1]), 1):
    line = line.strip()
    if not line:
        continue
    try:
        json.loads(line)
    except Exception as e:
        print("line %d is not JSON: %s" % (i, e)); sys.exit(1)
PY
then ok; else bad "the log holds a line that is not JSON"; fi

# An allowed git command is the common case and must cost nothing.
before="$(wc -l < "$OUTSOURCE_TELEMETRY_FILE")"
for _ in 1 2 3 4 5; do
  CRUSH_TOOL_INPUT_COMMAND='git log --oneline' "$BIN" guard </dev/null >/dev/null 2>&1
done
after="$(wc -l < "$OUTSOURCE_TELEMETRY_FILE")"
[ "$before" = "$after" ] && ok \
  || bad "5 allowed guard calls added $((after-before)) lines; the guard fires on every tool call and must record only blocks"

# And the off switch must be absolute.
lines_before="$(wc -l < "$OUTSOURCE_TELEMETRY_FILE")"
OUTSOURCE_TELEMETRY=0 "$BIN" runs nope >/dev/null 2>&1
[ "$lines_before" = "$(wc -l < "$OUTSOURCE_TELEMETRY_FILE")" ] && ok \
  || bad "OUTSOURCE_TELEMETRY=0 still wrote a line"

# The summary must read the file it wrote.
# Captured first and filtered second, deliberately. Written as
# `"$BIN" telemetry | grep -q ...` this check FAILED against a working summary:
# grep -q exits on its first match, the producer takes SIGPIPE, and under
# `set -o pipefail` the pipeline reports 141. That is the same hazard this repo
# documents for piping a gate through `tail`, in a third disguise — the producer
# does not even have to be a gate, only to be killed by the reader.
summary="$("$BIN" telemetry 2>&1)"; src_rc=$?
if printf '%s' "$summary" | grep -q "failures by kind"; then ok
else bad "the summary does not report failures (rc=$src_rc): $(printf '%s' "$summary" | head -3 | tr '\n' '|')"; fi

printf '\ntelemetry: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
