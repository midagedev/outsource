#!/usr/bin/env sh
# install.sh — copy the outsource skill into Claude Code's skill directory.
#
# Usage:
#   ./install.sh            install into ~/.claude/skills/outsource/
#   ./install.sh --project  install into ./.claude/skills/outsource/ (cwd)
#   ./install.sh --print    show the plan without writing
#   ./install.sh --force    overwrite even if the install was hand-edited
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
set -eu

SRC="$(cd "$(dirname "$0")" && pwd)/skills/outsource"
DEST="$HOME/.claude/skills/outsource"
MANIFEST=".install-checksums"
PRINT=0
FORCE=0

for arg in "$@"; do
  case "$arg" in
    --project) DEST="$(pwd)/.claude/skills/outsource" ;;
    --print)   PRINT=1 ;;
    --force)   FORCE=1 ;;
    -h|--help) sed -n '2,15p' "$0"; exit 0 ;;
    *) echo "unknown option: $arg" >&2; exit 2 ;;
  esac
done

checksum() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$@"; else shasum -a 256 "$@"; fi
}

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
  for seed in "$(cd "$(dirname "$0")" && pwd)"/local-overlay*.md; do
    [ -f "$seed" ] || continue
    mkdir -p "$DEST/references"
    cp "$seed" "$OVERLAY"
    echo "seeded local overlay from: $seed"
    break
  done
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
