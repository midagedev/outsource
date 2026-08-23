#!/usr/bin/env bash
# The committed binary must be exactly what the committed source builds.
#
# This repo ships a prebuilt binary because neither install path has a build step,
# which creates two hazards this closes at once.
#
# The first is trust: "rebuild it and compare the hash" is only an honest offer if
# it actually works. It did not, at first — Go stamps the commit hash, the commit
# timestamp and a "+dirty" marker into the module version, so the bytes changed on
# every commit and no two builds ever agreed. `-buildvcs=false` in build.sh is what
# makes the binary a pure function of the source.
#
# The second is staler and more likely: someone edits Go source, runs the tests
# against a binary built from the PREVIOUS source, sees green, and commits. Every
# black-box suite here invokes the binary, so that mistake is invisible to all of
# them. This is the one check that would catch it.
set -uo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.." || exit 2

if ! command -v go >/dev/null 2>&1; then
  echo "reproducible-build: SKIP (no Go toolchain; an installed copy has the prebuilt binary)"
  exit 0
fi

BIN=skills/outsource/bin/outsource
[ -f "$BIN" ] || { echo "reproducible-build: $BIN is missing — run ./build.sh" >&2; exit 1; }

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
mkdir -p "$TMP/state"
export XDG_STATE_HOME="$TMP/state"

# The same flags build.sh uses. Kept literal rather than sourced, so this test
# fails loudly if build.sh changes them without a decision.
if ! CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags="-s -w" \
     -o "$TMP/outsource" ./cmd/outsource 2>"$TMP/err"; then
  echo "reproducible-build: the source does not build" >&2
  cat "$TMP/err" >&2
  exit 1
fi

sum() { if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1"; else shasum -a 256 "$1"; fi | cut -d' ' -f1; }
fresh="$(sum "$TMP/outsource")"
shipped="$(sum "$BIN")"

if [ "$fresh" = "$shipped" ]; then
  echo "reproducible-build: the committed binary matches its source ($fresh)"
  exit 0
fi

cat >&2 <<MSG
reproducible-build: the committed binary does NOT match the committed source.
  from source: $fresh
  committed:   $shipped

Either the source changed without ./build.sh being run — in which case run it and
amend — or the build is no longer reproducible, in which case find out what got
stamped in before trusting the artifact.
MSG
exit 1
