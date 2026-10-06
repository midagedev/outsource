#!/usr/bin/env bash
# Gate for the outsource-panel mod (mods/outsource-panel).
#
# The module ships twice: as the standalone plugin `outsource-panel` and
# inside the root plugin `outsource` (hooks/hooks.json at the repo root), which
# is what `/plugin install outsource@outsource` brings. Every layer below that
# loads the module runs for both.
#
# Layers:
#   1. fixture drift — tests/fixtures/runs.js (what the unit tests import)
#      must be the same rows as tests/fixtures/runs.json (what the capture
#      fake serves); the loader admits only code files, so the duplication
#      is unavoidable and this check is what keeps it honest;
#   2. claude plugin validate --strict, the standalone and the root (the root
#      validation must reach the root's hooks module, not only the
#      marketplace file beside it);
#   3. one log site: register.js writes `$.ui.log` in exactly one place, and
#      that place sends to the debug sink — the class it closes is a log line
#      landing in the person's transcript (measured 2026-10-06:
#      `⏺ outsource-panel: outsource-panel: no watermark …`);
#   4. claude plugin test, the standalone (plugin `outsource-panel`) and the
#      root (plugin `outsource`: the same suite plus tests/panel-bundle.test.ts).
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

# 2. Strict manifest/module validation, standalone then root.
if claude plugin validate --strict "$MOD" >"$out" 2>&1 && grep -q 'Validation passed' "$out"; then
  grep -E 'hooks:|calls:' "$out" | sed 's/^/  /'
else
  echo "FAIL: claude plugin validate --strict $MOD"
  cat "$out"
  fail=1
fi
# With a marketplace.json beside plugin.json, validate targets the marketplace
# file; it reaches the root's hooks module only through hooks/hooks.json, so
# the module's own report line is required, not just the verdict.
if claude plugin validate --strict . >"$out" 2>&1 && grep -q 'Validation passed' "$out" \
  && grep -q '^Validating hooks: .*/hooks/hooks\.json$' "$out" \
  && grep -q '\.\./mods/outsource-panel/hooks/register\.js hooks: ' "$out"; then
  echo "  root: hooks/hooks.json -> ../mods/outsource-panel/hooks/register.js validated"
else
  echo "FAIL: claude plugin validate --strict . (the root plugin must carry the panel module)"
  cat "$out"
  fail=1
fi

# 3. One log site, on the debug sink.
if python3 - "$MOD/hooks/register.js" "$MOD/hooks/view.js" >"$out" 2>&1 <<'PY'
import re, sys

register, view = (open(p).read() for p in sys.argv[1:3])
sites = [m.start() for m in re.finditer(r"\$\.ui\.log\(", register)]
bad = []
if len(sites) != 1:
    bad.append(f"register.js has {len(sites)} $.ui.log( call sites; the panel logs through its one log() helper")
else:
    line = register[sites[0]:register.index("\n", sites[0])]
    if "to: 'debug'" not in line:
        bad.append("register.js's one $.ui.log( does not send to the debug sink: " + line.strip())
if "$.ui.log(" in view:
    bad.append("view.js calls $.ui.log(: it is pure and must not touch $")
for b in bad:
    print(b)
sys.exit(1 if bad else 0)
PY
then
  echo "  log: one \$.ui.log site, debug sink"
else
  echo "FAIL: panel log sites"
  cat "$out"
  fail=1
fi

# 4. The behaviour tests, under each copy's plugin name. The root run must
# include the bundled-first pair test, which lives outside the mod's folder.
for target in "$MOD" .; do
  if claude plugin test "$target" >"$out" 2>&1 && grep -Eq '^[[:space:]]*[0-9]+ pass$' "$out" && grep -q ' 0 fail' "$out" \
    && { [ "$target" != . ] || grep -q '^tests/panel-bundle\.test\.ts:$' "$out"; }; then
    echo "  test $target: $(grep -E '^[[:space:]]*[0-9]+ pass$' "$out" | sed 's/^ *//'), 0 fail"
  else
    echo "FAIL: claude plugin test $target"
    cat "$out"
    fail=1
  fi
done

if [ "$fail" -ne 0 ]; then exit 1; fi
echo "panel-mod: drift + validate (standalone, root) + log site + test (standalone, root) all green"
