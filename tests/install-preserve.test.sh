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

printf 'install-preserve: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
