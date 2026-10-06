#!/usr/bin/env bash
# The plugin manifest's version must be the version the changelog's newest entry
# announces.
#
# This closes a defect that shipped three releases in a row, and it is a
# tagging problem, not a cosmetic one. The pattern each time: a commit adds a
# `## X.Y.Z` changelog entry and the feature it describes, but leaves
# .claude-plugin/plugin.json reading the PREVIOUS version, which then catches up
# silently at some later bump. The result is that no commit in history has a
# manifest saying X.Y.Z, so `vX.Y.Z` has no honest home — every one of those
# releases had to be tagged at the changelog commit with a footnote explaining
# that the manifest disagreed there.
#
#   0.13.0  entry at 8489f39, manifest went 0.12.0 -> 0.13.5, skipping it
#   0.14.1  entry at 20476b5, manifest still 0.14.0; caught up at 0.15.0
#   0.16.1  entry at 44098ec, manifest still 0.16.0; caught up at 0.17.0
#
# Three instances is a class. A release is cheap to get right at commit time and
# expensive to reconstruct afterwards, so the check lives here rather than in
# anyone's memory.
#
# Since the no-binaries-in-git design, the same door has a second lock on it:
# skills/outsource/bin/outsource.sha256 — the manifest the dispatcher verifies
# downloads against — must carry the plugin's version and one well-formed hash
# line per shipped platform. A version mismatch there means a machine verifies
# the wrong release's bytes; a missing line means a machine of that kind
# downloads with nothing to check against.
#
# FAIL-first: set the manifest one version behind the changelog head and this
# fails naming both values.
set -uo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.." || exit 2

pass=0
fail=0
note() { printf '  %s\n' "$*"; }
ok()   { pass=$((pass + 1)); }
bad()  { fail=$((fail + 1)); printf 'FAIL: %s\n' "$*"; }

MANIFEST=.claude-plugin/plugin.json
CHANGELOG=CHANGELOG.md
HASHES=skills/outsource/bin/outsource.sha256

for f in "$MANIFEST" "$CHANGELOG" "$HASHES"; do
  if [ ! -f "$f" ]; then
    bad "$f is missing"
  fi
done

if [ "$fail" -eq 0 ]; then
  # The manifest is JSON, so it is read as JSON rather than grepped.
  manifest_version=$(python3 -c '
import json,sys
print(json.load(open(sys.argv[1]))["version"])' "$MANIFEST" 2>/dev/null)

  # The changelog head is the first "## <version> — ..." heading in the file.
  changelog_version=$(grep -m1 -E '^## [0-9]+\.[0-9]+\.[0-9]+' "$CHANGELOG" \
    | sed -E 's/^## ([0-9]+\.[0-9]+\.[0-9]+).*/\1/')

  if [ -z "$manifest_version" ]; then
    bad "could not read .version from $MANIFEST"
  elif [ -z "$changelog_version" ]; then
    bad "no '## <version>' heading found at the top of $CHANGELOG"
  elif [ "$manifest_version" != "$changelog_version" ]; then
    bad "version drift: $MANIFEST says '$manifest_version' but $CHANGELOG's newest entry announces '$changelog_version'"
    note "Bump the manifest in the same commit as the changelog entry, or the"
    note "release for '$changelog_version' has no commit to be tagged at."
  else
    ok
    note "manifest and changelog agree on $manifest_version"
  fi

  # The hash manifest must carry that same version: the dispatcher caches and
  # fetches under it, so a manifest one release behind would verify a binary
  # the new skill never shipped. FAIL-first: set its version one behind and
  # this fails naming both values.
  hashes_version=$(sed -n 's/^version //p' "$HASHES")
  if [ -z "$hashes_version" ]; then
    bad "no 'version X.Y.Z' line at the top of $HASHES"
  elif [ "$hashes_version" != "$manifest_version" ]; then
    bad "version drift: $HASHES says '$hashes_version' but $MANIFEST says '$manifest_version'"
    note "Run ./build.sh after bumping the plugin version; it reads the"
    note "version from plugin.json into the manifest."
  else
    ok
    note "$HASHES agrees on $hashes_version"
  fi

  # One well-formed hash line per shipped platform, or a machine of that kind
  # fetches with nothing to verify against.
  for t in darwin-arm64 darwin-amd64 linux-amd64 linux-arm64; do
    if grep -qE "^[0-9a-f]{64}  outsource-$t\$" "$HASHES"; then
      ok
    else
      bad "$HASHES has no '<sha256>  outsource-$t' line — that machine cannot verify its download"
    fi
  done
fi

printf '\nversion-manifest: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
