#!/usr/bin/env bash
# spec-lint.sh, with the to-be-created exemption as the centre of gravity.
#
# The exemption is the dangerous kind of feature: it makes the linter say
# less. Every case here therefore comes in a pair — the thing that must now
# be quiet, and a real defect placed right next to it that must still be
# loud. A creation block that swallows the rest of the document would look
# exactly like a clean spec.
#
#   SPEC_LINT=/path/to/spec-lint.sh tests/spec-lint.test.sh

set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
LINT="${SPEC_LINT:-$HERE/skills/outsource/bin/spec-lint.sh}"
[ -x "$LINT" ] || { echo "not executable: $LINT" >&2; exit 2; }

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
mkdir -p "$TMP/state"
export XDG_STATE_HOME="$TMP/state"
export OUTSOURCE_TELEMETRY_FILE="$TMP/state/outsource/telemetry.jsonl"
mkdir -p "$TMP/root/pkg"
printf 'one\ntwo\nthree\n' > "$TMP/root/pkg/exists.go"

pass=0; fail=0
out=""; rc=0

run() {  # <spec body on stdin>; sets $out and $rc
  cat > "$TMP/spec.md"
  out="$("$LINT" --root "$TMP/root" "$TMP/spec.md" 2>&1)"; rc=$?
}

ok() {  # <description> <expected-rc> <grep-pattern-that-must-appear|-> <pattern-that-must-not|->
  local desc="$1" want_rc="$2" must="$3" mustnot="$4"
  local bad=""
  [ "$rc" = "$want_rc" ] || bad="rc=$rc want=$want_rc"
  if [ "$must" != "-" ] && ! printf '%s' "$out" | grep -q -- "$must"; then
    bad="$bad; missing output: $must"
  fi
  if [ "$mustnot" != "-" ] && printf '%s' "$out" | grep -q -- "$mustnot"; then
    bad="$bad; unexpected output: $mustnot"
  fi
  if [ -z "$bad" ]; then pass=$(( pass + 1 )); else
    fail=$(( fail + 1 ))
    printf 'FAIL  %s\n      %s\n      output: %s\n' "$desc" "$bad" "$out"
  fi
}

# ---- the baseline the exemption must not erase ------------------------------

run <<'EOF'
See `pkg/exists.go` for the pattern.
EOF
ok "an existing path is clean" 0 "ok" "missing"

run <<'EOF'
See `pkg/absent.go` for the pattern.
EOF
ok "a missing path is still a finding" 1 "missing: pkg/absent.go" "-"

run <<'EOF'
See `pkg/exists.go:99` for the pattern.
EOF
ok "a line citation past EOF is still a finding" 1 "line-out-of-range" "-"

# ---- the exemption ----------------------------------------------------------

run <<'EOF'
## File whitelist

Create:
- `pkg/brandnew.go`
- `pkg/brandnew_test.go`
EOF
ok "a create block exempts its paths" 0 "2 to-be-created exempt" "missing"

run <<'EOF'
Create: `pkg/brandnew.go`
EOF
ok "the inline form exempts its path" 0 "1 to-be-created exempt" "missing"

run <<'EOF'
New files:
- `pkg/brandnew.go`
EOF
ok "New files: opens a block too" 0 "1 to-be-created exempt" "missing"

run <<'EOF'
1. Create: `pkg/brandnew.go` — the program.
2. Create: `pkg/brandnew_test.go` — its test.
EOF
ok "a numbered list item opens the inline form" 0 "2 to-be-created exempt" "missing"

run <<'EOF'
1. New files:
   - `pkg/brandnew.go`
EOF
ok "a numbered list item opens a block too" 0 "1 to-be-created exempt" "missing"

run <<'EOF'
Create (vitest files, beside their modules):
- `pkg/brandnew.go`
EOF
ok "a parenthetical before the colon still opens a block" 0 "1 to-be-created exempt" "missing"

run <<'EOF'
Create (the parser described below) `pkg/absent.go` and follow it:
read it carefully.
EOF
ok "a parenthetical inline sentence is not an opener" 1 "missing: pkg/absent.go" "-"

# ---- the exemption's own new finding ----------------------------------------

run <<'EOF'
Create:
- `pkg/exists.go`
EOF
ok "creating a file that is already there is a finding" 1 "already-exists: pkg/exists.go" "-"

# ---- the block must end -----------------------------------------------------

run <<'EOF'
Create:
- `pkg/brandnew.go`

Then read `pkg/absent.go` and follow it.
EOF
ok "prose after a block is checked again" 1 "missing: pkg/absent.go" "-"

run <<'EOF'
Create:
- `pkg/brandnew.go`

- and this list item is still part of it: `pkg/alsonew.go`
EOF
ok "a blank line does not end a list" 0 "2 to-be-created exempt" "missing"

run <<'EOF'
Create: `pkg/brandnew.go`
Also read `pkg/absent.go`.
EOF
ok "the inline form exempts one line only" 1 "missing: pkg/absent.go" "-"

run <<'EOF'
Create:

## Next section

Read `pkg/absent.go`.
EOF
ok "a heading ends the block" 1 "missing: pkg/absent.go" "-"

# ---- the exemption is by path, not by position ------------------------------
# A spec names the file it is creating more than once — in the whitelist, then
# again in the completion criteria and the test section. Exempting only the
# declaration line moved the cry-wolf defect a page down instead of fixing it.

run <<'EOF'
Create:
- `pkg/brandnew.go`

## Tests

Put the cases in `pkg/brandnew.go` and run them.
EOF
ok "a later mention of a created file is exempt" 0 "2 to-be-created exempt" "missing"

run <<'EOF'
This round builds the parser in `pkg/brandnew.go`.

Create:
- `pkg/brandnew.go`
EOF
ok "a mention before the declaration is exempt too" 0 "2 to-be-created exempt" "missing"

run <<'EOF'
Create:
- `pkg/exists.go`

Then run `pkg/exists.go` again.
EOF
ok "already-exists is reported once, not per mention" 1 "already-exists" "-"

run <<'EOF'
Create:
- `pkg/brandnew.go`

Then run `pkg/exists.go` and read `pkg/absent.go`.
EOF
ok "an unrelated missing file is still loud beside them" 1 "missing: pkg/absent.go" "missing: pkg/brandnew.go"

# The sentence that would be catastrophic to misread as a block opener: it
# ends in a colon and starts with the word, but has prose after it.
run <<'EOF'
Create the parser described below, then:
read `pkg/absent.go` for the existing convention.
EOF
ok "prose beginning with Create is not an opener" 1 "missing: pkg/absent.go" "-"

# The markers were English-only, so a spec written in Korean declared its new
# files just as clearly and got the guaranteed findings this exemption exists
# to prevent — the feature had never fired for those specs (measured
# 2026-08-18: four specs, six findings, every one a file the round was being
# sent to create).
run <<'EOF'
새 파일:
- `pkg/brandnew.go`
EOF
ok "a Korean create block exempts its paths" 0 "1 to-be-created exempt" "missing"

run <<'EOF'
신규 파일:
- `pkg/brandnew.go`

본문에서 다시 언급: `pkg/brandnew.go` 를 만든다.
EOF
ok "a later Korean mention is exempt too" 0 "2 to-be-created exempt" "missing"

# Same precision bar as the English side: a colon-ending sentence that merely
# begins with the word is prose, not an opener.
run <<'EOF'
새 파일을 만들기 전에 확인할 것:
`pkg/absent.go` 의 기존 관례를 읽어라.
EOF
ok "Korean prose beginning with the word is not an opener" 1 "missing: pkg/absent.go" "-"

# ---- a dotfile path keeps its dot -------------------------------------------
# The edge-punctuation strip took the leading `.` with it, so every spec citing
# a CI workflow reported `.github/workflows/ci.yml` as missing while the file
# sat right there (measured 2026-08-19: two of six audit specs, one finding
# each, both false). Restricted to `/`-bearing tokens, so prose keeps losing
# its dots — the two cases below pin both directions.
mkdir -p "$TMP/root/.github/workflows"
printf 'name: CI\n' > "$TMP/root/.github/workflows/ci.yml"

run <<'EOF'
Read `.github/workflows/ci.yml` for the job list.
EOF
ok "a dotfile directory keeps its leading dot" 0 "ok" "missing"

run <<'EOF'
Read `./pkg/exists.go` and `../outside/absent.go`.
EOF
ok "a relative prefix survives, and still resolves" 1 "missing: ../outside/absent.go" "missing: ./pkg/exists.go"

run <<'EOF'
Prose with an abbreviation, e.g. a note about `pkg/exists.go` behaviour.
EOF
ok "prose dots are still stripped" 0 "ok" "missing"

# ---- telemetry details: findings / exempt / missing / already-exists --------
# A missing path produces findings=1 missing=1. The dispatcher writes the row
# (Note() is not enough); this file already invokes the real binary.
run <<'EOF'
See `pkg/absent.go` for the pattern.
EOF
if python3 - "$OUTSOURCE_TELEMETRY_FILE" <<'PY'
import json, sys
last = None
for line in open(sys.argv[1]):
    line = line.strip()
    if not line:
        continue
    last = json.loads(line)
if not last or last.get("tool") != "spec-lint":
    print("last row is not spec-lint:", last)
    sys.exit(1)
d = last.get("details") or {}
for k in ("findings", "exempt", "missing", "already-exists"):
    if k not in d:
        print("missing details key", k, "in", d)
        sys.exit(1)
if d["findings"] != "1" or d["missing"] != "1":
    print("want findings=1 missing=1, got", d)
    sys.exit(1)
PY
then pass=$((pass + 1))
else fail=$((fail + 1)); echo "FAIL  spec-lint telemetry details shape" >&2
fi

printf '\nspec-lint: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
