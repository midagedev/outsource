#!/usr/bin/env bash
# What an upgrade is allowed to delete.
#
# install.sh is a *clean* install — it `rm -rf`s the destination so files removed
# upstream do not linger. That is correct for shipped files and destructive for
# the two things under that tree the user owns: references/local-overlay.md and
# references/overlays/ (declared project overlays). Both are content this repo
# never ships, so nothing else would notice them disappearing; the loss shows up
# later as delegations that silently stopped carrying the repo's rules.
#
# The overlays/ half was a real gap: the declared-overlay mode shipped in 0.12.0
# pointing users at a directory the installer deleted on the next upgrade.
set -uo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.." || exit 2

pass=0; fail=0
ok()  { pass=$((pass+1)); }
bad() { fail=$((fail+1)); printf 'FAIL  %s\n' "$1" >&2; }

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
export HOME="$TMP/home"
mkdir -p "$HOME"
DEST="$HOME/.claude/skills/outsource"

./install.sh >/dev/null 2>&1 || { echo "install.sh failed on a clean HOME" >&2; exit 2; }

# User content, written the way a user would after installing.
mkdir -p "$DEST/references/overlays"
printf 'user overlay\n' > "$DEST/references/local-overlay.md"
printf -- '---\npaths:\n  - /tmp/x\n---\ndeclared\n' > "$DEST/references/overlays/acme.md"

# An upgrade over an unmodified install must not need --force...
if ./install.sh >/dev/null 2>&1; then ok; else
  bad "upgrade refused after only user-owned files were added — those must not count as tampering"
fi

# ...and must not have eaten either of them.
if [ -f "$DEST/references/local-overlay.md" ]; then ok; else bad "local-overlay.md was deleted by an upgrade"; fi
if [ -f "$DEST/references/overlays/acme.md" ]; then ok; else bad "references/overlays/ was deleted by an upgrade"; fi
if grep -q 'declared' "$DEST/references/overlays/acme.md" 2>/dev/null; then ok; else bad "declared overlay survived as an empty file"; fi

# Neither may enter the manifest: they are not shipped, so checksumming them
# turns the user's own next edit into "someone hand-edited the install".
if grep -q 'references/overlays/' "$DEST/.install-checksums"; then
  bad "declared overlays are checksummed — editing one would make the next upgrade refuse"
else ok; fi

# The resolver has to see them where the installer puts them, or the whole
# arrangement is documentation only.
out="$("$DEST/bin/outsource" overlays --root /tmp/x --skill-dir "$DEST" 2>/dev/null)"
if printf '%s' "$out" | grep -q 'overlays/acme.md'; then ok; else
  bad "installed resolver did not find the declared overlay (got: $out)"
fi

# The stale-binary refusal keys on mtimes, so a *_test.go newer than
# bin/outsource must NOT refuse (tracker STD-11: a test-only edit made
# install.sh refuse and this suite red until a content-identical rebuild —
# hit twice on 2026-10-06). A non-test .go newer than the binary must still
# refuse: that is the check doing its one job.
# The suite already cd'd to the repo root at the top. Recomputing it from a
# relative BASH_SOURCE here resolved one level too high under run-all.sh
# (invoked as ./install-preserve.test.sh from tests/), so both cases below
# were skipped silently there (lead review, 2026-10-06).
ROOT="$(pwd)"
BIN="$ROOT/skills/outsource/bin/outsource"
NEWER_TEST="$ROOT/internal/install_probe_test.go"
NEWER_SRC="$ROOT/internal/install_probe.go"
cleanup_probe() { rm -f "$NEWER_TEST" "$NEWER_SRC"; }
trap 'cleanup_probe; rm -rf "$TMP"' EXIT

if [ -e "$BIN" ]; then
  : >"$NEWER_TEST"
  touch "$BIN" "$NEWER_TEST"; sleep 1; touch "$NEWER_TEST" # strictly newer
  if ./install.sh >/dev/null 2>&1; then ok; else
    bad "install refused on a *_test.go newer than the binary — a test-only edit must not need a rebuild (STD-11)"
  fi

  : >"$NEWER_SRC"
  touch "$BIN" "$NEWER_SRC"; sleep 1; touch "$NEWER_SRC" # strictly newer
  if ./install.sh >/dev/null 2>&1; then
    bad "install did not refuse on a non-test .go newer than the binary — it would ship the previous binary"
  else ok; fi
  cleanup_probe
else
  bad "no committed binary at $BIN — the STD-11 cases cannot run, and a skip would read as a pass"
fi

printf 'install-preserve: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
