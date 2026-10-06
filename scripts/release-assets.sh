#!/usr/bin/env bash
# Build, verify and upload the release binaries for a version.
#
#   scripts/release-assets.sh <X.Y.Z>          build, verify, gh release upload
#   scripts/release-assets.sh --dry-run <X.Y.Z>  everything except gh
#
# The lead runs this at release time. No binary is committed to git; what a
# machine downloads is the GitHub Release asset, and the ONLY thing that makes
# those bytes trustworthy is bin/outsource.sha256 — committed, checked by
# tests/reproducible-build.test.sh against the source — being exactly the hash
# of the uploaded asset. So this script refuses to upload anything the
# committed manifest does not already name:
#
#   1. ./build.sh rebuilds dist/ and rewrites the manifest from the source.
#   2. The requested version must equal the manifest's (and plugin.json's).
#   3. Every dist/ binary must hash to its manifest line.
#   4. In a real (non-dry) run, the manifest must be committed and clean —
#      `git status --porcelain` on it must be empty — so the upload can never
#      race ahead of the commit that vouches for it.
#
# The dry-run does everything except gh and prints the five files (four
# binaries + the manifest) with their hashes, plus the command it would run.
set -euo pipefail
cd "$(dirname "$0")/.."

DRY_RUN=0
VER=""
for arg in "$@"; do
  case "$arg" in
    --dry-run) DRY_RUN=1 ;;
    -h|--help) sed -n '2,8p' "$0"; exit 0 ;;
    *)  if [ -n "$VER" ]; then echo "usage: $0 [--dry-run] <X.Y.Z>" >&2; exit 2; fi
        VER="$arg" ;;
  esac
done
[ -n "$VER" ] || { echo "usage: $0 [--dry-run] <X.Y.Z>" >&2; exit 2; }
case $VER in
  [0-9]*.[0-9]*.[0-9]*) ;;
  *) echo "release-assets: '$VER' is not X.Y.Z" >&2; exit 2 ;;
esac

MANIFEST=skills/outsource/bin/outsource.sha256
TARGETS="darwin-arm64 darwin-amd64 linux-amd64 linux-arm64"

./build.sh

# The manifest's version — the one the dispatcher fetches under — comes from
# plugin.json, and the release tag must be exactly it.
mver="$(sed -n 's/^version //p' "$MANIFEST")"
pver="$(sed -nE 's/.*"version": *"([^"]+)".*/\1/p' .claude-plugin/plugin.json | head -n 1)"
if [ "$VER" != "$mver" ] || [ "$VER" != "$pver" ]; then
  echo "release-assets: refusing — tag v$VER, manifest $mver and plugin.json $pver do not agree." >&2
  echo "release-assets: bump .claude-plugin/plugin.json and run ./build.sh in the release commit." >&2
  exit 1
fi

sum() { if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1"; else shasum -a 256 "$1"; fi | cut -d' ' -f1; }

FILES=()
for t in $TARGETS; do
  f="dist/outsource-$t"
  [ -f "$f" ] || { echo "release-assets: $f was not built" >&2; exit 1; }
  got="$(sum "$f")"
  want="$(grep -E "^[0-9a-f]{64}  outsource-$t\$" "$MANIFEST" | cut -d' ' -f1)"
  if [ -z "$want" ] || [ "$got" != "$want" ]; then
    echo "release-assets: refusing — $f does not hash to the manifest line:" >&2
    echo "  manifest: $want" >&2
    echo "  built   : $got" >&2
    exit 1
  fi
  FILES+=("$f")
done
FILES+=("$MANIFEST")

if [ "$DRY_RUN" -eq 1 ]; then
  echo "release-assets: dry run for v$VER — these five files would be uploaded:"
  for f in "${FILES[@]}"; do printf '  %8d bytes  %s  %s\n' "$(wc -c < "$f")" "$(sum "$f")" "$f"; done
  if [ -n "$(git status --porcelain -- "$MANIFEST" 2>/dev/null)" ]; then
    echo "release-assets: NOTE — $MANIFEST is not committed and clean yet; a real run refuses until it is."
  fi
  echo "release-assets: would run: gh release upload v$VER ${FILES[*]}"
  exit 0
fi

command -v gh >/dev/null 2>&1 || { echo "release-assets: gh is not installed" >&2; exit 1; }
if [ -n "$(git status --porcelain -- "$MANIFEST" 2>/dev/null)" ]; then
  echo "release-assets: refusing — $MANIFEST must be committed and clean before the upload (it is what vouches for the assets)." >&2
  git status --porcelain -- "$MANIFEST" >&2
  exit 1
fi
exec gh release upload "v$VER" "${FILES[@]}"
