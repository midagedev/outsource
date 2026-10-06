#!/usr/bin/env bash
# The committed manifest must be exactly what the committed source builds.
#
# No binary is committed to git; what commits the BYTES to a hash is
# bin/outsource.sha256, and everything downstream trusts it: the dispatcher
# runs a download only if it hashes to the manifest line, and
# scripts/release-assets.sh uploads only manifest-named bytes. Two hazards
# this closes at once.
#
# The first is trust: "rebuild it and compare the hash" is only an honest
# offer if it actually works. It did not, at first — Go stamps the commit
# hash, the commit timestamp and a "+dirty" marker into the module version,
# so the bytes changed on every commit and no two builds ever agreed.
# `-buildvcs=false` in build.sh is what makes the binary a pure function of
# the source.
#
# The second is staler and more likely: someone edits Go source, runs the
# tests against a binary built from the PREVIOUS source, sees green, and
# commits. Every black-box suite here invokes the binary, so that mistake is
# invisible to all of them. This is the one check that would catch it — a
# source edit without ./build.sh leaves the manifest naming yesterday's
# bytes. FAIL-first: add a function to any .go file (a comment is NOT enough —
# the compiler strips comments, so the bytes do not change) and this fails
# naming every target.
#
# The host's local build (bin/outsource-<os>-<arch>, gitignored, what the
# suites actually execute through the dispatcher) is checked too: a stale
# local binary is exactly the "tests ran yesterday's code" failure wearing a
# green suite.
set -uo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.." || exit 2

if ! command -v go >/dev/null 2>&1; then
  echo "reproducible-build: SKIP (no Go toolchain; an installed copy fetches the release binaries)"
  exit 0
fi

# The shipped platforms, written out like build.sh's TARGETS: a target added
# there and not here is a decision this test should be told about.
TARGETS="darwin/arm64 darwin/amd64 linux/amd64 linux/arm64"
MANIFEST=skills/outsource/bin/outsource.sha256

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
mkdir -p "$TMP/state"
export XDG_STATE_HOME="$TMP/state"

sum() { if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1"; else shasum -a 256 "$1"; fi | cut -d' ' -f1; }

if [ ! -f "$MANIFEST" ]; then
  echo "reproducible-build: $MANIFEST is missing — run ./build.sh once and commit it." >&2
  exit 1
fi
if [ -z "$(sed -n 's/^version [0-9]*\.[0-9]*\.[0-9]*$/&/p' "$MANIFEST")" ]; then
  echo "reproducible-build: $MANIFEST has no 'version X.Y.Z' line." >&2
  exit 1
fi

bad=0
for target in $TARGETS; do
  os="${target%/*}"; arch="${target#*/}"
  # The same flags build.sh uses. Kept literal rather than sourced, so this
  # test fails loudly if build.sh changes them without a decision.
  if ! CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath -buildvcs=false -ldflags="-s -w" \
       -o "$TMP/outsource-$os-$arch" ./cmd/outsource 2>"$TMP/err"; then
    echo "reproducible-build: the source does not build for $target" >&2
    cat "$TMP/err" >&2
    exit 1
  fi
  fresh="$(sum "$TMP/outsource-$os-$arch")"
  shipped="$(grep -E "^[0-9a-f]{64}  outsource-$os-$arch\$" "$MANIFEST" | cut -d' ' -f1)"
  if [ -z "$shipped" ]; then
    echo "reproducible-build: $MANIFEST has no hash line for outsource-$os-$arch — run ./build.sh" >&2
    bad=1; continue
  fi
  if [ "$fresh" != "$shipped" ]; then
    bad=1
    cat >&2 <<MSG
reproducible-build: the manifest does NOT match the committed source for outsource-$os-$arch.
  from source: $fresh
  manifest:    $shipped
MSG
  else
    echo "reproducible-build: outsource-$os-$arch matches its manifest line ($fresh)"
  fi
  # A local build for this target, if one is present, must be those same
  # bytes — it is what the test suites execute through the dispatcher.
  local_bin="skills/outsource/bin/outsource-$os-$arch"
  if [ -f "$local_bin" ] && [ "$(sum "$local_bin")" != "$fresh" ]; then
    bad=1
    echo "reproducible-build: $local_bin is stale (the suites would run yesterday's binary) — run ./build.sh or delete it." >&2
  fi
done

if [ "$bad" -ne 0 ]; then
  cat >&2 <<MSG

Either the source changed without ./build.sh being run — in which case run it
and commit the manifest it writes — or the build is no longer reproducible,
in which case find out what got stamped in before trusting the artifact.
MSG
  exit 1
fi
echo "reproducible-build: all four manifest lines match their source"
