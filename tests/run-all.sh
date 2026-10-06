#!/usr/bin/env bash
# Every test in this directory, one command, one exit code.
#
# This exists because the alternative had already started happening: two test
# files, each documenting its own invocation in a header comment, and nothing
# that ran either of them. A test nobody runs is not a gate — it is a record
# of what someone once checked. The point of a single entry point is that
# adding a file to tests/ is enough to make it part of the suite; nobody has
# to remember to register it anywhere.
#
#   tests/run-all.sh          exit 0 = every suite passed
#
# Each suite prints its own detail. This prints only the roll-up, so a green
# run is quiet and a red one names exactly which file to open.

set -uo pipefail

# This runner poisons its own environment with the delegate-session markers
# (OUTSOURCE_ROUND=1, OUTSOURCE_DETACHED=1, an obviously-non-key
# ANTHROPIC_AUTH_TOKEN) before any suite runs. Why: a suite that is green in
# the lead's shell and red inside every delegated round is exactly the failure
# mode this directory spent 2026-10-06 reverting (five suites red in every
# round, each round's first minutes spent proving "this red is not mine").
# With the poison present, a future suite that forgets to source
# hermetic-env.sh and inherits a marker goes red in the lead's ordinary run —
# at the front door, not only inside rounds where it reads as "not my red".
cd "$(dirname "${BASH_SOURCE[0]}")" || exit 2

# shellcheck source=hermetic-env.sh
. ./hermetic-env.sh
for n in "${hermetic_poison_names[@]}"; do
  # A name may be poisoned only if the scrub cleans it; otherwise every
  # sourcing suite would still see it and the gate would gate nothing.
  case " ${hermetic_env_names[*]} " in
    *" $n "*) ;;
    *) echo "run-all: hermetic_poison_names names $n, which hermetic_scrub_env does not unset — fix tests/hermetic-env.sh" >&2; exit 2 ;;
  esac
  case "$n" in
    # Never poison the nesting permission: handing every suite
    # OUTSOURCE_ALLOW_NESTED=1 would silence the refusal the poison exists to
    # exercise.
    OUTSOURCE_ALLOW_NESTED) echo "run-all: refusing to poison $n — a suite must never inherit permission to nest" >&2; exit 2 ;;
    ANTHROPIC_AUTH_TOKEN) export "$n=run-all-poison-not-a-key" ;;
    *) export "$n=1" ;;
  esac
done

suites=()
while IFS= read -r f; do suites+=("$f"); done < <(ls -1 ./*.test.sh 2>/dev/null | sort)

if [ "${#suites[@]}" -eq 0 ]; then
  echo "tests/run-all.sh: no *.test.sh found — that is a broken checkout, not a pass" >&2
  exit 2
fi

failed=()
for s in "${suites[@]}"; do
  printf '\n──── %s ────\n' "${s#./}"
  if bash "$s"; then :; else failed+=("${s#./}"); fi
done

printf '\n════ %d suite(s) ════\n' "${#suites[@]}"
if [ "${#failed[@]}" -eq 0 ]; then
  echo "all green"
  exit 0
fi
printf 'FAILED: %s\n' "${failed[*]}"
exit 1
