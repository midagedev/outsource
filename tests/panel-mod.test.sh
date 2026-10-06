#!/usr/bin/env bash
# Gate for the outsource-panel mod (mods/outsource-panel).
#
# Three layers:
#   1. fixture drift — tests/fixtures/runs.js (what the unit tests import)
#      must be the same rows as tests/fixtures/runs.json (what the capture
#      fake serves); the loader admits only code files, so the duplication
#      is unavoidable and this check is what keeps it honest;
#   2. claude plugin validate --strict;
#   3. claude plugin test.
#
# Each command's output goes to a file and is grepped for its verdict; a
# failing layer prints its file. Exits 1 on any failure. With no claude on
# PATH there is nothing to gate against this build's engine: SKIP, exit 0.
set -uo pipefail

cd "$(dirname "$0")/.." || exit 2
MOD=mods/outsource-panel

if ! command -v claude >/dev/null 2>&1; then
  echo "SKIP: claude is not on PATH; the panel-mod gate needs this build's engine"
  exit 0
fi

fail=0
out="$(mktemp "${TMPDIR:-/tmp}/panel-mod.XXXXXX")"
trap 'rm -f "$out"' EXIT

# 1. Fixture drift: the JS fixture and the JSON fixture are the same rows.
if command -v python3 >/dev/null 2>&1; then
  if ! python3 - "$MOD/tests/fixtures/runs.js" "$MOD/tests/fixtures/runs.json" >"$out" 2>&1 <<'PY'
import json, sys

js, json_path = sys.argv[1], sys.argv[2]
source = open(js).read()
from_js = json.loads(source[source.index("["): source.rindex("]") + 1])
from_json = json.load(open(json_path))
if from_js != from_json:
    print("runs.js and runs.json differ: the unit-test fixture and the "
          "capture fixture have drifted apart; regenerate one from the other.")
    sys.exit(1)
PY
  then
    echo "FAIL: fixture drift between runs.js and runs.json"
    cat "$out"
    exit 1
  fi
else
  echo "FAIL: python3 is required to check fixture drift"
  exit 1
fi

# 2. Strict manifest/module validation.
if claude plugin validate --strict "$MOD" >"$out" 2>&1 && grep -q 'Validation passed' "$out"; then
  grep -E 'hooks:|calls:' "$out" | sed 's/^/  /'
else
  echo "FAIL: claude plugin validate --strict $MOD"
  cat "$out"
  fail=1
fi

# 3. The behaviour tests.
if claude plugin test "$MOD" >"$out" 2>&1 && grep -Eq '^[[:space:]]*[0-9]+ pass$' "$out" && grep -q ' 0 fail' "$out"; then
  grep -E '^[[:space:]]*[0-9]+ pass$|^[[:space:]]*0 fail$' "$out" | sed 's/^/  /'
else
  echo "FAIL: claude plugin test $MOD"
  cat "$out"
  fail=1
fi

if [ "$fail" -ne 0 ]; then exit 1; fi
echo "panel-mod: drift + validate + test all green"
