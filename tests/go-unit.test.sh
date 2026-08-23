#!/usr/bin/env bash
# The Go unit tests, wired into the one runner so they are a gate and not a
# thing someone remembers to run.
#
# Skips rather than fails when Go is absent: a user who installed the skill has
# the shipped binary and no toolchain, and telling them their checkout is broken
# would be a lie. A contributor who edits Go without Go installed will see the
# skip and know why.
set -uo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.." || exit 2

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
mkdir -p "$TMP/state"
export XDG_STATE_HOME="$TMP/state"

if ! command -v go >/dev/null 2>&1; then
  echo "go-unit: SKIP (no Go toolchain on PATH; the shipped binary is prebuilt)"
  exit 0
fi

# vet first: it catches the format-string and shadowing classes before the
# tests get a chance to pass around them.
if ! go vet ./... 2>&1; then
  echo "go-unit: go vet failed"
  exit 1
fi
if ! out="$(go test ./... 2>&1)"; then
  printf '%s\n' "$out"
  echo "go-unit: go test failed"
  exit 1
fi
printf '%s\n' "$out" | grep -vE '^\?' || true
n="$(printf '%s\n' "$out" | grep -c '^ok')"
echo "go-unit: $n package(s) ok, vet clean"
