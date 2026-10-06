#!/usr/bin/env bash
# `outsource audit` — what a delegated round actually ran, from its trail.
#
# Pinned here, each from a real review need:
#   - the three live paths: a claude-code transcript (with subagents), an agy
#     stream-json log, and a pruned round reached through --log + sentinel
#   - the trail seal end to end: ok on the sealed sentinel, exit 3 the moment
#     one byte of the trail changes after the fact, absent for a sentinel
#     that predates the seal
#   - the exit codes: 64 ambiguous label, 65 unknown id / no trail, 69 a
#     harness audit cannot read (crush)
#   - one cross-session message that arrives as enqueue, queued_command and
#     remove is ONE event, not three
#   - MODEL DRIFT on a subagent that answered on a model other than the one
#     its spawn asked for
#   - command observation flags on the summary, and --json/--events shapes
#
# The fixtures are the synthetic ones in internal/audit/testdata/ — no real
# transcript content or private path ever enters this suite.
#
# Usage: tests/audit.test.sh   (exit 0 = all pass)
set -uo pipefail

HERE="$(cd "$(dirname "$0")/.." && pwd)"
BIN="$HERE/skills/outsource/bin/outsource"
SHIM="$HERE/skills/outsource/bin/audit.sh"
[ -x "$BIN" ] || { echo "not built: $BIN (run ./build.sh first)" >&2; exit 2; }
[ -x "$SHIM" ] || { echo "no shim: $SHIM (run ./build.sh first)" >&2; exit 2; }

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
mkdir -p "$TMP/runs" "$TMP/cwd"
export OUTSOURCE_RUNS_DIR="$TMP/runs"

FIX="$HERE/internal/audit/testdata"

sum() {  # sum [file] — sha256 of the file, or of stdin with no argument
  if command -v sha256sum >/dev/null 2>&1; then
    [ $# = 0 ] && sha256sum || sha256sum "$1"
  else
    [ $# = 0 ] && shasum -a 256 || shasum -a 256 "$1"
  fi | cut -d' ' -f1
}

fail=0
check() {  # <desc> <cond...>
  local desc="$1"; shift
  if "$@"; then echo "  ok: $desc"; else echo "  FAIL: $desc"; fail=1; fi
}
has() { [ "${2#*"$3"}" != "$2" ]; }

# ---- a sealed claude-code round ---------------------------------------------

cp "$FIX/claude-main.jsonl" "$TMP/session.jsonl"
LOG="$TMP/round.log"
printf 'report body\nDONE-fixture-main\n' > "$LOG"
TRAIL_SHA="$(sum "$TMP/session.jsonl")"
TRAIL_BYTES="$(wc -c < "$TMP/session.jsonl" | tr -d ' ')"
cat > "$LOG.rc" <<EOF
rc=0
finished=2026-10-06T01:00:20Z
harness=claude-code
provider=zai
model_requested=glm-5.3
model_actual=glm-5.3
session=fixture-session
trail=$TMP/session.jsonl
trail_sha256=$TRAIL_SHA
trail_bytes=$TRAIL_BYTES
tool_calls=6
done_marker=found (DONE-fixture-main)
done_marker_scope=report
EOF
cat > "$TMP/runs/1790000001-101.run" <<EOF
id=1790000001-101
pid=999101
label=fixture-main
provider=zai
harness=claude-code
model=glm-5.3
cwd=$TMP/cwd
log=$LOG
trail=$TMP/session.jsonl
startedAt=1790000001
finishedAt=1790000021
rc=0
EOF

OUT="$("$BIN" audit 1790000001-101 2>"$TMP/err")"; RC=$?
echo "── claude-code fixture"
check "summary exits 0 on a sealed trail" [ "$RC" = 0 ]
check "seal ok" has x "$OUT" "seal ok"
check "requests counted by message id, not line" has x "$OUT" "2 requests"
EV="$("$BIN" audit --events 1790000001-101 2>/dev/null)"
check "guard denial blamed on git-guard, not the hook carrying it" has x "$EV" "by=git-guard"
check "mod denial blamed on mod" has x "$EV" "by=mod"
check "both denials counted in Tools" has x "$OUT" "2 denials"
check "git commit flagged" has x "$OUT" "[git-write]"
check "write outside cwd marked" has x "$OUT" "OUTSIDE-CWD"
check "one inbound message (enqueue+attachment+remove dedup)" has x "$OUT" "TESTTOKEN-MSG-1"
check "message sender named" has x "$OUT" "lead-session-test"
check "tree cross-check skips a non-repo cwd honestly" has x "$OUT" "not a git work tree"
if printf '%s' "$OUT" | grep -q 'MODEL DRIFT (requested'; then
  echo "  FAIL: main loop on the requested model marked as drift"; fail=1
else
  echo "  ok: no MODEL DRIFT on the requested model"
fi

echo "── seal verdicts"
# A byte inside the sealed prefix flips: a mismatch, and the exit code says 3.
FLIP_LEN=$((TRAIL_BYTES - 1))
python3 - "$TMP/session.jsonl" "$FLIP_LEN" <<'PYEOF'
import sys
p, n = sys.argv[1], int(sys.argv[2])
b = bytearray(open(p, "rb").read())
b[n] ^= 1
open(p, "wb").write(b)
PYEOF
OUT="$("$BIN" audit 1790000001-101 2>"$TMP/err")"; RC=$?
check "a flipped byte in the sealed prefix exits 3" [ "$RC" = 3 ]
check "mismatch leads the header" has x "$OUT" "SEAL MISMATCH"

# A resumed session appends to the same transcript: extended, exit 0. The
# sealed content is restored first — the flip above must not ride along into
# the prefix this case checks.
cp "$FIX/claude-main.jsonl" "$TMP/session.jsonl"
python3 - "$TMP/session.jsonl" <<'PYEOF'
import sys
p = sys.argv[1]
b = open(p, "rb").read()
open(p, "wb").write(b + b"{\"type\":\"assistant\",\"timestamp\":\"2026-10-06T01:00:30.000Z\",\"message\":{\"role\":\"assistant\",\"id\":\"msg_TESTEEEE00000000000001\",\"model\":\"glm-5.3\",\"usage\":{\"input_tokens\":10,\"output_tokens\":2},\"content\":[{\"type\":\"text\",\"text\":\"resumed\"}]}}\n")
PYEOF
OUT="$("$BIN" audit 1790000001-101 2>"$TMP/err")"; RC=$?
check "an appended trail (resumed session) exits 0" [ "$RC" = 0 ]
check "header says seal extended" has x "$OUT" "seal extended"
check "extended names the appended bytes" has x "$OUT" "bytes appended after the seal"

# Shorter than the seal recorded: a truncation, back to exit 3.
truncate -s "$FLIP_LEN" "$TMP/session.jsonl"
OUT="$("$BIN" audit 1790000001-101 2>"$TMP/err")"; RC=$?
check "a truncated trail exits 3" [ "$RC" = 3 ]
check "truncation named" has x "$OUT" "shorter than the seal recorded"

# ---- an agy round ------------------------------------------------------------

AGYLOG="$TMP/agy-round.log"
cp "$FIX/agy.log" "$AGYLOG"
AGY_SHA="$(sum "$AGYLOG")"
AGY_BYTES="$(wc -c < "$AGYLOG" | tr -d ' ')"
cat > "$AGYLOG.rc" <<EOF
rc=0
finished=2026-10-06T02:00:20Z
harness=agy
provider=agy
model_requested=gemini-3.8-flash-high
model_actual=gemini-3.8-flash-high
session=agy-fixture-session
trail=$AGYLOG
trail_sha256=$AGY_SHA
trail_bytes=$AGY_BYTES
done_marker=found (DONE-agy-fixture)
done_marker_scope=report
EOF
cat > "$TMP/runs/1790000002-202.run" <<EOF
id=1790000002-202
pid=999202
label=agy-fixture
provider=agy
harness=agy
model=gemini-3.8-flash-high
cwd=$TMP/cwd
log=$AGYLOG
trail=$AGYLOG
startedAt=1790000002
rc=0
EOF

OUT="$("$BIN" audit --log "$AGYLOG" 2>"$TMP/err")"; RC=$?
echo "── agy fixture"
check "agy round audits through --log" [ "$RC" = 0 ]
check "agy model named" has x "$OUT" "gemini-3.8-flash-high"
check "run_command commands listed" has x "$OUT" "mkdir -p /tmp/round/out"
check "curl|sh observed" has x "$OUT" "[network-install]"
check "write_to_file target listed from init.tools" has x "$OUT" "/tmp/round/report.md"
check "error step surfaced in Tools" has x "$OUT" "1 error"

# ---- a subagent round --------------------------------------------------------

SUB="$TMP/sub"
cp "$FIX/claude-sub.jsonl" "$SUB.jsonl"
mkdir -p "$SUB/subagents"
cp "$FIX/claude-sub/subagents/"* "$SUB/subagents/"
SUBLOG="$TMP/sub-round.log"
printf 'report\nDONE-fixture-sub\n' > "$SUBLOG"
SUB_SHA="$(sum "$SUB.jsonl")"
SUB_BYTES="$(wc -c < "$SUB.jsonl" | tr -d ' ')"
# The subagents digest: "<basename> <sha256>\n" per transcript, sorted by
# basename — the basename keeps its .jsonl.
SUBAG_SHA="$(printf 'agent-testsub00000001.jsonl %s\n' "$(sum "$SUB/subagents/agent-testsub00000001.jsonl")" | sum)"
cat > "$SUBLOG.rc" <<EOF
rc=0
finished=2026-10-06T02:00:40Z
harness=claude-code
provider=zai
model_requested=glm-5.3
model_actual=glm-5.3
session=fixture-sub-session
trail=$SUB.jsonl
trail_sha256=$SUB_SHA
trail_bytes=$SUB_BYTES
subagents_sha256=$SUBAG_SHA
tool_calls=3
done_marker=found (DONE-fixture-sub)
done_marker_scope=report
EOF
cat > "$TMP/runs/1790000003-303.run" <<EOF
id=1790000003-303
pid=999303
label=fixture-sub
provider=zai
harness=claude-code
model=glm-5.3
cwd=$TMP/cwd
log=$SUBLOG
trail=$SUB.jsonl
startedAt=1790000003
rc=0
EOF

OUT="$("$BIN" audit 1790000003-303 2>"$TMP/err")"; RC=$?
echo "── subagent fixture"
check "subagent round sealed over subagents too" [ "$RC" = 0 ]
check "seal ok with subagents_sha256" has x "$OUT" "seal ok"
check "subagent model drift marked" has x "$OUT" "MODEL DRIFT (spawn asked opus)"
printf 'tamper\n' >> "$SUB/subagents/agent-testsub00000001.jsonl"
OUT="$("$BIN" audit 1790000003-303 2>"$TMP/err")"; RC=$?
check "tampered subagent transcript exits 3" [ "$RC" = 3 ]
check "mismatch names subagents" has x "$OUT" "subagents_sha256 differs"

# ---- exit codes and output modes ---------------------------------------------

echo "── exit codes and modes"
cat > "$TMP/runs/1790000004-404.run" <<EOF
id=1790000004-404
pid=999404
label=fixture-main
provider=zai
harness=crush
model=glm-5.3
cwd=$TMP/cwd
log=$TMP/crush.log
trail=$TMP/session.jsonl
startedAt=1790000004
rc=0
EOF
cat > "$TMP/runs/1790000005-505.run" <<EOF
id=1790000005-505
pid=999505
label=fixture-main
provider=zai
harness=claude-code
model=glm-5.3
cwd=$TMP/cwd
log=$TMP/e.log
trail=
startedAt=1790000005
rc=0
EOF

"$BIN" audit fixture-main >/dev/null 2>"$TMP/err"; RC=$?
check "ambiguous label exits 64 with candidates" [ "$RC" = 64 ]
check "candidates listed" has x "$(cat "$TMP/err")" "name one of these"
"$BIN" audit 1799999999-000 >/dev/null 2>"$TMP/err"; RC=$?
check "unknown id exits 65" [ "$RC" = 65 ]
"$BIN" audit 1790000004-404 >/dev/null 2>"$TMP/err"; RC=$?
check "crush record exits 69" [ "$RC" = 69 ]
check "69 refusal names the harness" has x "$(cat "$TMP/err")" "harness crush is not supported yet (claude-code, agy)"
"$BIN" audit 1790000005-505 >/dev/null 2>"$TMP/err"; RC=$?
check "no trail yet exits 65" [ "$RC" = 65 ]
"$BIN" audit --bogus-flag >/dev/null 2>"$TMP/err"; RC=$?
check "an unknown flag exits 64" [ "$RC" = 64 ]
# --label resolves through the same selector as the positional form.
"$BIN" audit --label agy-fixture >/dev/null 2>&1; RC=$?
check "--label selects the round (flag form of the selector)" [ "$RC" = 0 ]
"$BIN" audit --json --events x >/dev/null 2>"$TMP/err"; RC=$?
check "--json and --events refuse together" [ "$RC" = 64 ]

# --json: one object per line, seq from 1, termination last
J="$("$BIN" audit --json --log "$AGYLOG" 2>/dev/null)"
E="$(COLUMNS=160 "$BIN" audit --events --log "$AGYLOG" 2>/dev/null)"
check "--json ends on the termination event" has x "$(printf '%s\n' "$J" | tail -1)" '"kind":"termination"'
check "--json starts at seq 1" has x "$(printf '%s\n' "$J" | head -1)" '"seq":1,'
check "--json and --events agree on the event count" \
  [ "$(printf '%s\n' "$J" | wc -l)" = "$(printf '%s\n' "$E" | wc -l)" ]
# awk's length counts bytes and the clip mark "…" is 3 of them, so 40
# display columns bound the line at 43 bytes.
LONGEST="$(COLUMNS=40 "$BIN" audit --events --log "$AGYLOG" 2>/dev/null | awk '{ print length($0) }' | sort -rn | head -1)"
check "--events clips to COLUMNS=40" [ "${LONGEST:-0}" -le 43 ]

# ---- a pruned round: --log + sentinel, no registry row ------------------------

echo "── pruned round"
rm "$TMP/runs/"*.run
# A pre-seal sentinel: what a round finished before this change leaves.
cat > "$AGYLOG.rc" <<EOF2
rc=0
finished=2026-10-06T02:00:20Z
harness=agy
provider=agy
model_requested=gemini-3.8-flash-high
model_actual=gemini-3.8-flash-high
session=agy-fixture-session
trail=$AGYLOG
tool_calls=unknown (pre-seal round)
done_marker=found (DONE-agy-fixture)
done_marker_scope=report
EOF2
OUT="$("$BIN" audit --log "$AGYLOG" 2>"$TMP/err")"; RC=$?
check "pruned round audits from the sentinel" [ "$RC" = 0 ]
check "pre-seal sentinel reads absent" has x "$OUT" "seal absent"
rm "$AGYLOG.rc"
"$BIN" audit --log "$AGYLOG" >/dev/null 2>"$TMP/err"; RC=$?
check "no registry row and no sentinel exits 65" [ "$RC" = 65 ]

if [ "$fail" = 0 ]; then echo "audit.test.sh: all green"; else echo "audit.test.sh: FAILED"; exit 1; fi
