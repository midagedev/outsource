#!/usr/bin/env bash
# What an upgrade is allowed to delete, and what an install must not lie about.
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
#
# Since no binary is committed to git, the suite also holds the installer to the
# new binary contract, in a skeleton repo so the cases are cheap and hermetic:
#   - with Go, bin/outsource.sha256 must match a fresh build of the four, or the
#     install refuses naming the target (an install must never ship a manifest
#     that lies about the bytes machines will verify downloads against);
#   - without Go, the dispatcher's verified fetch runs AT INSTALL TIME, so a
#     missing network fails the install, not the first round (--no-fetch skips).
set -uo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.." || exit 2

pass=0; fail=0
ok()  { pass=$((pass+1)); }
bad() { fail=$((fail+1)); printf 'FAIL  %s\n' "$1" >&2; }

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
export HOME="$TMP/home"
mkdir -p "$HOME" "$TMP/state"
export XDG_STATE_HOME="$TMP/state"
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
# arrangement is documentation only. (A Go install built the host binary into
# DEST/bin, so this goes through the dispatcher's first lookup.)
out="$("$DEST/bin/outsource" overlays --root /tmp/x --skill-dir "$DEST" 2>/dev/null)"
if printf '%s' "$out" | grep -q 'overlays/acme.md'; then ok; else
  bad "installed resolver did not find the declared overlay (got: $out)"
fi

# An install with Go ends with a runnable binary in DEST/bin — the dispatcher's
# first lookup — and no other machine's artifacts snuck in from the checkout.
host_t="$(uname -s | tr '[:upper:]' '[:lower:]')-$(uname -m | tr '[:upper:]' '[:lower:]' | sed 's/aarch64/arm64/;s/x86_64/amd64/')"
if [ -x "$DEST/bin/outsource-$host_t" ]; then ok
else bad "a Go install left no executable bin/outsource-$host_t in the install"; fi
strays="$(find "$DEST/bin" -name 'outsource-*' ! -name '*.sh' ! -name "outsource-$host_t" 2>/dev/null)"
if [ -z "$strays" ]; then ok
else bad "the install carried non-host local builds into DEST/bin: $strays"; fi

# Every shipped bin/*.sh must come out of the install present and executable:
# the docs launch rounds through bin/outsource-run.sh, and the cleanup glob
# outsource-* used to eat exactly that shim (measured 2026-10-09: the
# installed copy had every other *.sh but not that one). Keyed on git
# ls-files rather than a hand list, so a newly tracked shim is covered the
# moment it exists.
tracked_shims="$(git ls-files 'skills/outsource/bin/*.sh' 2>/dev/null)"
if [ -z "$tracked_shims" ]; then
  bad "git ls-files listed no skills/outsource/bin/*.sh — the shim check has nothing to check"
else
  missing_shims=""
  while IFS= read -r tracked; do
    name="${tracked##*/}"
    [ -x "$DEST/bin/$name" ] || missing_shims="$missing_shims $name"
  done <<< "$tracked_shims"
  if [ -z "$missing_shims" ]; then ok
  else bad "install dropped shipped shims from DEST/bin:$missing_shims"; fi
fi

# ── skeleton: the Go-path manifest check, cheap and per-target ──────────────
# A real repo check would rebuild the real binary four times per case; the
# skeleton builds a two-line main, so every case is sub-second and the
# assertion is about install.sh's comparison, not Go's speed.
SK="$TMP/skel"
mkdir -p "$SK/cmd/outsource" "$SK/skills/outsource/bin"
cp ./install.sh "$SK/install.sh"
cp skills/outsource/bin/outsource "$SK/skills/outsource/bin/outsource"
printf 'module probe\n\ngo %s\n' "$(sed -n 's/^go //p' go.mod | head -n1)" > "$SK/go.mod"
printf 'package main\n\nfunc main() {}\n' > "$SK/cmd/outsource/main.go"
sum() { if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1"; else shasum -a 256 "$1"; fi | cut -d' ' -f1; }
SKVER=9.9.9
write_skel_manifest() {  # rewrites the skeleton manifest from fresh builds,
  # invoked exactly the way install.sh invokes its own builds (cd $SK &&
  # go build ./cmd/outsource): the same package built by absolute path from
  # another module's directory produced different bytes, and the comparison
  # must be about install.sh's logic, not Go's path handling.
  { echo "version $SKVER"
    for t in darwin-arm64 darwin-amd64 linux-amd64 linux-arm64; do
      (cd "$SK" && CGO_ENABLED=0 GOOS="${t%-*}" GOARCH="${t#*-}" \
        go build -trimpath -buildvcs=false -ldflags="-s -w" -o "$TMP/skel-$t" ./cmd/outsource) 2>/dev/null
      echo "$(sum "$TMP/skel-$t")  outsource-$t"
    done
  } > "$SK/skills/outsource/bin/outsource.sha256"
}
if command -v go >/dev/null 2>&1; then
  write_skel_manifest
  # every target's line, tampered one at a time: each refuses, naming its target
  aaa="$(printf 'a%.0s' {1..64})"
  for stale in darwin-arm64 darwin-amd64 linux-amd64 linux-arm64; do
    write_skel_manifest
    awk -v t="outsource-$stale" -v h="$aaa" 'BEGIN{FS=OFS="  "} $2==t {$1=h} {print}' \
      "$SK/skills/outsource/bin/outsource.sha256" > "$TMP/m.new" && mv "$TMP/m.new" "$SK/skills/outsource/bin/outsource.sha256"
    if "$SK/install.sh" --force >/dev/null 2>"$TMP/skel.err"; then
      bad "install did not refuse with outsource-$stale's manifest line tampered — it would vouch for bytes that are not the source's"
    elif grep -q "outsource-$stale" "$TMP/skel.err"; then ok
    else bad "install refused for $stale but did not name it: $(cat "$TMP/skel.err")"; fi
  done
  # all four matching: the skeleton installs and builds its host binary
  write_skel_manifest
  SKDEST="$TMP/skelhome/.claude/skills/outsource"
  if HOME="$TMP/skelhome" "$SK/install.sh" --force >/dev/null 2>"$TMP/skel.err"; then ok
  else bad "the skeleton with a matching manifest did not install: $(cat "$TMP/skel.err")"; fi
  if [ -x "$SKDEST/bin/outsource-$host_t" ]; then ok
  else bad "the skeleton Go install built no host binary into DEST/bin"; fi
  # the cleanup still bites: sparing *.sh from the removal must not become
  # removing nothing — a stray foreign-arch build sitting in the source's
  # bin/ must not survive into the install
  printf 'not a real build\n' > "$SK/skills/outsource/bin/outsource-plan9-386"
  if HOME="$TMP/skelhome" "$SK/install.sh" --force >/dev/null 2>"$TMP/skel.err"; then
    if [ ! -e "$SKDEST/bin/outsource-plan9-386" ]; then ok
    else bad "the install carried the fake outsource-plan9-386 local build into DEST/bin"; fi
  else
    bad "the skeleton install failed with the stray binary present: $(cat "$TMP/skel.err")"
  fi
  rm -f "$SK/skills/outsource/bin/outsource-plan9-386"
  # a missing manifest refuses rather than installing a dispatcher with nothing
  # to verify against
  mv "$SK/skills/outsource/bin/outsource.sha256" "$TMP/hidden-manifest"
  if HOME="$TMP/skelhome" "$SK/install.sh" --force >/dev/null 2>"$TMP/skel.err"; then
    bad "install went ahead with bin/outsource.sha256 missing"
  elif grep -q 'outsource.sha256 is missing' "$TMP/skel.err"; then ok
  else bad "install refused a missing manifest without saying so: $(cat "$TMP/skel.err")"; fi
  mv "$TMP/hidden-manifest" "$SK/skills/outsource/bin/outsource.sha256"
else
  echo "install-preserve: no Go — the manifest-check cases did not run" >&2
fi

# ── skeleton without Go: the binary arrives by verified fetch, or not at all ─
# PATH=/usr/bin:/bin has no go on every machine this runs on (checked, and the
# case is skipped loudly if not); it still has curl, so the fetch is exercised
# against a file:// URL — no server, no network.
NOGO_PATH=/usr/bin:/bin
# command -v in a FRESH shell: this test has already run go above, and bash's
# command hash would hand the restricted lookup the cached /opt/homebrew path
# (measured on this machine: "go not on PATH" became /opt/homebrew/bin/go).
if PATH="$NOGO_PATH" bash -c 'command -v go' >/dev/null 2>&1; then
  echo "install-preserve: $NOGO_PATH still has go on this machine — the no-Go cases cannot run here" >&2
else
  SK2="$TMP/skel2"
  mkdir -p "$SK2/skills/outsource/bin" "$TMP/srv/v$SKVER" "$TMP/h2"
  cp ./install.sh "$SK2/install.sh"
  cp skills/outsource/bin/outsource "$SK2/skills/outsource/bin/outsource"
  ASSET="$TMP/srv/v$SKVER/outsource-darwin-arm64"
  printf '#!/bin/sh\nexit 0\n' > "$ASSET"
  { echo "version $SKVER"; echo "$(sum "$ASSET")  outsource-darwin-arm64"
    echo "$(sum "$ASSET")  outsource-darwin-amd64"
    echo "$(sum "$ASSET")  outsource-linux-amd64"
    echo "$(sum "$ASSET")  outsource-linux-arm64"; } > "$SK2/skills/outsource/bin/outsource.sha256"
  # the fetch works: the install succeeds and the cache, not DEST/bin, holds it
  if env PATH="$NOGO_PATH" HOME="$TMP/h2" XDG_CACHE_HOME="$TMP/c2" \
       OUTSOURCE_RELEASE_URL="file://$TMP/srv" sh "$SK2/install.sh" --force >/dev/null 2>"$TMP/nogo.err"; then ok
  else bad "the no-Go install failed on a working fetch: $(cat "$TMP/nogo.err")"; fi
  if [ -x "$TMP/c2/outsource/$SKVER/outsource-darwin-arm64" ]; then ok
  else bad "the verified fetch did not land in the cache: $(find "$TMP/c2" -type f 2>/dev/null)"; fi
  if [ ! -e "$TMP/h2/.claude/skills/outsource/bin/outsource-darwin-arm64" ]; then ok
  else bad "the no-Go install left a binary in DEST/bin some other way"; fi
  # a dead release URL fails THE INSTALL, not the first round
  rm -rf "$TMP/h2/.claude"
  if env PATH="$NOGO_PATH" HOME="$TMP/h2" XDG_CACHE_HOME="$TMP/c3" \
       OUTSOURCE_RELEASE_URL="http://127.0.0.1:1" sh "$SK2/install.sh" --force >/dev/null 2>"$TMP/nogo.err"; then
    bad "the install succeeded although no binary could be fetched — the failure moved into the first round"
  elif grep -q 'could not produce' "$TMP/nogo.err"; then ok
  else bad "the failed fetch did not say so: $(cat "$TMP/nogo.err")"; fi
  # --no-fetch opts out explicitly
  if env PATH="$NOGO_PATH" HOME="$TMP/h2" XDG_CACHE_HOME="$TMP/c4" \
       OUTSOURCE_RELEASE_URL="http://127.0.0.1:1" sh "$SK2/install.sh" --force --no-fetch >/dev/null 2>"$TMP/nogo.err"; then ok
  else bad "--no-fetch did not skip the fetch: $(cat "$TMP/nogo.err")"; fi
fi

printf '\ninstall-preserve: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
