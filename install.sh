#!/usr/bin/env sh
# install.sh — copy the outsource skill into Claude Code's skill directory.
#
# Usage:
#   ./install.sh            install into ~/.claude/skills/outsource/
#   ./install.sh --project  install into ./.claude/skills/outsource/ (cwd)
#   ./install.sh --print    show the plan without writing
#   ./install.sh --force    overwrite even if the install was hand-edited
#   ./install.sh --no-fetch skip the "produce a binary now" step (see below)
#
# Upgrades over an unmodified install proceed without --force: each install
# writes a checksum manifest (.install-checksums), and the next run refuses
# only when files changed since the LAST INSTALL (i.e. someone hand-edited
# the installed copy). references/local-overlay.md is always preserved and
# never checksummed; when the install has none, an untracked
# local-overlay*.md at the repo root seeds it. references/overlays/ — the
# declared project overlays — is preserved the same way and for the same
# reason: it is the user's content living inside a directory this script
# deletes wholesale.
#
# No binary is committed to git, so an install ends with a runnable binary in
# exactly one of two ways: with Go present, the host target is BUILT into
# DEST/bin (the dispatcher's first lookup); without Go, the dispatcher's
# verified fetch runs HERE, so a missing network fails the install rather
# than the first round five minutes later (--no-fetch skips that check).
set -eu

SRC="$(cd "$(dirname "$0")" && pwd)/skills/outsource"
ROOT="$(cd "$(dirname "$0")" && pwd)"
DEST="$HOME/.claude/skills/outsource"
MANIFEST=".install-checksums"
PRINT=0
FORCE=0
NO_FETCH=0

for arg in "$@"; do
  case "$arg" in
    --project) DEST="$(pwd)/.claude/skills/outsource" ;;
    --print)   PRINT=1 ;;
    --force)   FORCE=1 ;;
    --no-fetch) NO_FETCH=1 ;;
    -h|--help) sed -n '2,10p' "$0"; exit 0 ;;
    *) echo "unknown option: $arg" >&2; exit 2 ;;
  esac
done

checksum() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$@"; else shasum -a 256 "$@"; fi
}

GO=1
command -v go >/dev/null 2>&1 || GO=0

echo "install: $SRC -> $DEST"
[ "$PRINT" -eq 1 ] && exit 0

# Tamper check: refuse to clobber local edits unless --force.
if [ -d "$DEST" ] && [ "$FORCE" -ne 1 ]; then
  if [ -f "$DEST/$MANIFEST" ]; then
    if ! (cd "$DEST" && checksum -c "$MANIFEST" >/dev/null 2>&1); then
      echo "refusing: $DEST was modified since the last install." >&2
      echo "use --force to discard those local edits (local-overlay.md survives either way)." >&2
      exit 1
    fi
  elif ! diff -rq -x local-overlay.md -x overlays -x "$MANIFEST" "$SRC" "$DEST" >/dev/null 2>&1; then
    echo "refusing: $DEST differs and has no install manifest (pre-manifest install)." >&2
    echo "use --force once; upgrades after that won't need it." >&2
    exit 1
  fi
fi

# No binary ships in git; what ships is bin/outsource.sha256, the manifest the
# dispatcher verifies every download against. With Go present, that manifest
# must hash-match a fresh build of the four release binaries from THIS source
# — otherwise the install would hand every machine a dispatcher whose
# manifest lies about the bytes it fetches. (This replaces the old mtime
# staleness check on committed binaries: content, not clocks. The list is
# build.sh's TARGETS, written out on purpose, and the flags are kept literal
# rather than sourced so a build.sh change must be decided here too.)
if [ "$GO" -eq 1 ]; then
  if [ ! -f "$SRC/bin/outsource.sha256" ]; then
    echo "install: $SRC/bin/outsource.sha256 is missing — a checkout without a build has no manifest to install. Run ./build.sh once and commit it." >&2
    exit 1
  fi
  TMPD="$(mktemp -d)"
  trap 'rm -rf "$TMPD"' EXIT INT TERM
  for target in darwin-arm64 darwin-amd64 linux-amd64 linux-arm64; do
    os=${target%-*}; arch=${target#*-}
    if ! (cd "$ROOT" && CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" \
        go build -trimpath -buildvcs=false -ldflags="-s -w" \
        -o "$TMPD/outsource-$target" ./cmd/outsource) 2>/dev/null; then
      echo "install: the source does not build for $target — run ./build.sh, which surfaces the error." >&2
      exit 1
    fi
    fresh="$(checksum "$TMPD/outsource-$target" | cut -d' ' -f1)"
    shipped="$(grep -E "^[0-9a-f]{64}  outsource-$target\$" "$SRC/bin/outsource.sha256" | cut -d' ' -f1)"
    if [ -z "$shipped" ] || [ "$fresh" != "$shipped" ]; then
      echo "install: refusing — bin/outsource.sha256 does not match a fresh build for outsource-$target." >&2
      echo "install:   manifest: $shipped" >&2
      echo "install:   fresh   : $fresh" >&2
      echo "install: the source changed without ./build.sh (or the manifest was hand-edited); run ./build.sh and commit the manifest." >&2
      exit 1
    fi
  done
  rm -rf "$TMPD"; trap - EXIT INT TERM
fi

# Preserve a user's local overlay across upgrades (never shipped by this repo).
OVERLAY="$DEST/references/local-overlay.md"
TMP_OVERLAY=""
if [ -f "$OVERLAY" ]; then
  TMP_OVERLAY="$(mktemp)"
  cp "$OVERLAY" "$TMP_OVERLAY"
fi

# Same for declared project overlays. They are user content that happens to
# live under the installed tree, and the install below is a clean one.
DECLARED="$DEST/references/overlays"
TMP_DECLARED=""
if [ -d "$DECLARED" ]; then
  TMP_DECLARED="$(mktemp -d)"
  cp -R "$DECLARED/." "$TMP_DECLARED/"
fi

# Clean install so files removed upstream don't linger.
rm -rf "$DEST"
mkdir -p "$DEST"
cp -R "$SRC/." "$DEST/"

# A local build in the checkout's bin/ is a dev artifact of THIS machine, not
# something an install ships: the binary arrives by build (below) or by the
# dispatcher's verified fetch. Leaving a copied one in would make two
# different installs of the same source carry different bytes. The pattern
# hits only outsource-<os>-<arch>; outsource.sha256 has a dot, not a dash.
rm -f "$DEST"/bin/outsource-*

if [ -n "$TMP_OVERLAY" ]; then
  mkdir -p "$DEST/references"
  cp "$TMP_OVERLAY" "$OVERLAY"
  rm -f "$TMP_OVERLAY"
  echo "preserved local overlay: $OVERLAY"
fi

if [ -n "$TMP_DECLARED" ]; then
  mkdir -p "$DECLARED"
  cp -R "$TMP_DECLARED/." "$DECLARED/"
  rm -rf "$TMP_DECLARED"
  echo "preserved declared overlays: $DECLARED"
fi

# Seed the overlay from a personal source kept untracked at the repo root
# (`local-overlay*.md`). The repo never ships references/local-overlay.md,
# but a source file sitting here declares itself as exactly that — without
# this step it silently never reaches the install. A preserved overlay from
# the previous install wins over the seed.
if [ ! -f "$OVERLAY" ]; then
  for seed in "$ROOT"/local-overlay*.md; do
    [ -f "$seed" ] || continue
    mkdir -p "$DEST/references"
    cp "$seed" "$OVERLAY"
    echo "seeded local overlay from: $seed"
    break
  done
fi

# The install must end with a runnable binary, so the failure lands here and
# not in the first delegated round. With Go: build the host target into
# DEST/bin — the dispatcher's first lookup, no cache and no network involved.
# Without Go: run the dispatcher once (`help` resolves the binary and exits
# 0), which fetches into the user's cache and refuses anything unverified.
if [ "$GO" -eq 1 ]; then
  goos="$(go env GOOS)"; goarch="$(go env GOARCH)"
  (cd "$ROOT" && CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
    go build -trimpath -buildvcs=false -ldflags="-s -w" \
    -o "$DEST/bin/outsource-$goos-$goarch" ./cmd/outsource)
  echo "built bin/outsource-$goos-$goarch into the install"
elif [ "$NO_FETCH" -ne 1 ]; then
  if ! "$DEST/bin/outsource" help >/dev/null; then
    echo "install: the dispatcher could not produce a binary (its message is above); fix that, or pass --no-fetch to install anyway." >&2
    exit 1
  fi
  echo "dispatcher fetched a verified binary (first use)"
fi

# Record what this install shipped, so the next run can tell "upgrade over
# a clean install" (fine) from "someone hand-edited the copy" (refuse).
(
  cd "$DEST" &&
  find . -type f ! -name "$MANIFEST" ! -path "./references/local-overlay.md" \
    ! -path "./references/overlays/*" \
    | LC_ALL=C sort \
    | while IFS= read -r f; do checksum "$f"; done
) > "$DEST/$MANIFEST"

echo "installed. In Claude Code, invoke with /outsource (backends: grok CLI, and/or a z.ai key for the GLM harnesses)."
