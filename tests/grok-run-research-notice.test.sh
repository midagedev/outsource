#!/usr/bin/env bash
# --research denies the write tools, so a spec that asks for a report file
# is un-satisfiable and the model retries forever. Field incident
# (2026-08-17): 301 turns of "writing the report" with no write tool, $5.77.
# The runner owns the contradiction: in research mode it must hand grok a
# spec that STARTS with the no-write notice, and must not mutate the
# caller's spec file to do it.
#
# Asserted here:
#   - research mode: the spec grok receives begins with the runner notice
#     and still contains the caller's spec body; the caller's file is
#     untouched.
#   - normal mode: the spec grok receives is the caller's file, no notice.
#
# Usage: tests/grok-run-research-notice.test.sh   (exit 0 = all pass)
set -uo pipefail

GROK_RUN="$(cd "$(dirname "$0")/.." && pwd)/skills/outsource/bin/grok-run.sh"
[ -x "$GROK_RUN" ] || { echo "not executable: $GROK_RUN" >&2; exit 2; }

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
mkdir -p "$TMP/state"
export XDG_STATE_HOME="$TMP/state"
export OUTSOURCE_RUNS_DIR="$TMP/runs"
export GROK_RUN_STARTUP_GRACE=10

# Fake grok: copies whatever --prompt-file it is given, then exits clean.
mkdir -p "$TMP/bin"
cat > "$TMP/bin/grok" <<FAKE
#!/usr/bin/env bash
while [ \$# -gt 0 ]; do
  if [ "\$1" = "--prompt-file" ]; then cp "\$2" "$TMP/received-spec.md"; shift 2; continue; fi
  shift
done
echo '{"type":"text","data":"working"}'
echo '{"type":"end","stopReason":"end_turn"}'
exit 0
FAKE
chmod +x "$TMP/bin/grok"
export PATH="$TMP/bin:$PATH"

printf '# my spec\nwrite a report\n' > "$TMP/spec.md"
ORIG_SUM="$(cksum < "$TMP/spec.md")"
mkdir -p "$TMP/cwd"

fail=0
note() { fail=$((fail + 1)); echo "FAIL  $*" >&2; }

# ── 1. research mode: notice prepended, body kept, caller file untouched ──
bash "$GROK_RUN" --foreground --cwd "$TMP/cwd" --spec "$TMP/spec.md" --log "$TMP/r.ndjson" \
  --label research-case --research >/dev/null 2>&1
if [ ! -f "$TMP/received-spec.md" ]; then
  note "fake grok never received a prompt file (research)"
else
  head -1 "$TMP/received-spec.md" | grep -q "runner notice — research mode" \
    || note "research spec does not start with the runner notice"
  grep -q "write a report" "$TMP/received-spec.md" \
    || note "caller's spec body missing from the research spec"
fi
[ "$(cksum < "$TMP/spec.md")" = "$ORIG_SUM" ] \
  || note "caller's spec file was mutated"

# ── 2. normal mode: no notice ─────────────────────────────────────────────
rm -f "$TMP/received-spec.md"
bash "$GROK_RUN" --foreground --cwd "$TMP/cwd" --spec "$TMP/spec.md" --log "$TMP/n.ndjson" \
  --label normal-case >/dev/null 2>&1
if [ ! -f "$TMP/received-spec.md" ]; then
  note "fake grok never received a prompt file (normal)"
else
  grep -q "runner notice — research mode" "$TMP/received-spec.md" \
    && note "normal mode must not carry the research notice"
  head -1 "$TMP/received-spec.md" | grep -q "# my spec" \
    || note "normal mode must pass the caller's spec through"
fi

if [ "$fail" -gt 0 ]; then echo "$fail failure(s)"; exit 1; fi
echo "ok: research notice prepended in research mode only, caller spec untouched"
