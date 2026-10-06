#!/usr/bin/env bash
# bin/outsource, the sh dispatcher that turns the committed manifest into a
# running binary — without a binary ever being committed.
#
# The failure modes this blocks, one section each:
#
#   - the wrong binary for the machine: every uname spelling of the four
#     targets reaches its binary (aarch64 is Linux's name for arm64, x86_64
#     is everyone's for amd64);
#   - altered arguments: "$@" arrives unchanged, spaces and empty strings
#     included, because the shims and the panel pass user text through;
#   - UNVERIFIED BYTES RUNNING: a download whose sha256 differs from
#     bin/outsource.sha256 by one byte is deleted, never exec'd, and both
#     hashes are named;
#   - a silent wrong source: the go-build path builds with build.sh's exact
#     flags into the versioned cache, not somewhere ad hoc;
#   - a lost skill dir: a binary run from the cache still finds the skill's
#     files, because the dispatcher exports OUTSOURCE_SKILL_DIR (only when
#     unset) on every exec from outside the bin dir;
#   - an open git guard: the dispatcher's exit 69 is a NON-BLOCKING error to
#     Claude Code (the tool runs), so git-guard.sh translates it to exit 2
#     and blocks instead;
#   - a download storm or a half-written binary: concurrent first calls share
#     one mkdir lock, waiters pick up the winner's file, and a killed
#     fetcher's stale lock does not wedge the next one;
#   - an unbounded cache: pruning keeps the manifest's version and the most
#     recently used older one (a round launched before an update execes its
#     hook from the old version's path), and deletes the rest;
#   - a binary committed to git by mistake: none may be tracked;
#   - the shell: the core logic runs under every POSIX shell found here
#     (sh, dash, busybox), because Alpine has no bash and macOS's sh IS bash,
#     where a bashism would pass unnoticed.
#
# The mapping and refusal cases run against fake binaries under a stubbed
# uname, so they prove the logic on any host; the fetch cases use stub
# curl/wget/go. Only the last sections touch the real repo files.
set -uo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.." || exit 2
ROOT="$(pwd)"
# shellcheck source=hermetic-env.sh
. tests/hermetic-env.sh
hermetic_scrub_env
DISPATCHER="$ROOT/skills/outsource/bin/outsource"
GUARD_SHIM="$ROOT/skills/outsource/bin/git-guard.sh"
TARGETS="darwin-arm64 darwin-amd64 linux-amd64 linux-arm64"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
mkdir -p "$TMP/state"
export XDG_STATE_HOME="$TMP/state"

pass=0; fail=0
ok()  { pass=$((pass+1)); }
bad() { fail=$((fail+1)); printf 'FAIL  %s\n' "$1" >&2; }

[ -f "$DISPATCHER" ] || { echo "dispatcher: $DISPATCHER is missing" >&2; exit 2; }

# ── fixture: <root with a space>/skills/outsource/bin/{dispatcher,manifest} ──
FX="$TMP/with space"
BIN="$FX/skills/outsource/bin"
mkdir -p "$BIN"
VER=9.9.9

fake() {  # <path>: reports its name and argv (the mapping/arguments cases)
  cat >"$1" <<'FAKE'
#!/bin/sh
printf 'ran=%s argc=%s\n' "${0##*/}" "$#"
for a in "$@"; do printf '<%s>\n' "$a"; done
case "${1:-}" in
  stdin) cat ;;
  rc) exit "$2" ;;
esac
FAKE
  chmod +x "$1"
}
envfake() {  # <path>: reports OUTSOURCE_SKILL_DIR and exits FAKE_RC
  cat >"$1" <<'FAKE'
#!/bin/sh
echo "SKILL=${OUTSOURCE_SKILL_DIR:-unset}"
exit "${FAKE_RC:-0}"
FAKE
  chmod +x "$1"
}
sum() { if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1"; else shasum -a 256 "$1"; fi | cut -d' ' -f1; }

# A manifest for the fixture: version line plus four hash lines. ASSET is the
# "release binary" the stubs serve; its hash is the manifest truth for
# darwin-arm64 (the arch all fetch cases stub uname to). The other three get
# well-formed placeholder hashes: this suite never fetches them.
ASSET="$TMP/asset"
envfake "$ASSET"
zeros=0000000000000000000000000000000000000000000000000000000000000000
write_manifest() {  # <extra hash for darwin-arm64, or '' for ASSET's real one
  local h="$1"; [ -n "$h" ] || h="$(sum "$ASSET")"
  { echo "version $VER"
    echo "$h  outsource-darwin-arm64"
    echo "$zeros  outsource-darwin-amd64"
    echo "$zeros  outsource-linux-amd64"
    echo "$zeros  outsource-linux-arm64"
  } > "$BIN/outsource.sha256"
}
reset_fixture() {  # dispatcher + manifest, no local builds
  cp "$DISPATCHER" "$BIN/outsource"; chmod +x "$BIN/outsource"
  write_manifest ""
}

# ── stubs, in two directories so a case can hide the fetchers alone ─────────
# STUB: uname (from STUB_S/STUB_M) and a readlink with no -f, as on macOS
# before 12.3 — the dispatcher must not need it.
STUB="$TMP/stub"; mkdir -p "$STUB"
cat >"$STUB/uname" <<'UNAME'
#!/bin/sh
[ -n "${STUB_FAIL:-}" ] && exit 1
s=0; m=0
for f in "$@"; do
  case $f in
    -s) s=1 ;;
    -m) m=1 ;;
    -sm|-ms) s=1; m=1 ;;
    *) echo "uname stub: unexpected flag $f" >&2; exit 2 ;;
  esac
done
out=""
[ "$s" = 1 ] && out="$STUB_S"
[ "$m" = 1 ] && out="${out:+$out }$STUB_M"
printf '%s\n' "$out"
UNAME
REAL_READLINK="$(command -v readlink)"
cat >"$STUB/readlink" <<READLINK
#!/bin/sh
for a in "\$@"; do
  case \$a in -f|-e|-m) echo "readlink: illegal option -- \${a#-}" >&2; exit 1 ;; esac
done
exec "$REAL_READLINK" "\$@"
READLINK
chmod +x "$STUB/uname" "$STUB/readlink"

# STOOLS: curl/wget/go. Each logs its argv and copies/builds the fake binary;
# FAIL_CURL/FAIL_WGET make the download fail. The log/asset paths come in on
# the environment (CURL_LOG etc.) because the stubs live outside $TMP's env.
CURLLOG="$TMP/curl.log"; WGETLOG="$TMP/wget.log"; GOLOG="$TMP/go.log"
STOOLS="$TMP/stools"; mkdir -p "$STOOLS"
cat >"$STOOLS/curl" <<'CURL'
#!/bin/sh
echo "curl $*" >>"$CURL_LOG"
[ -n "${FAIL_CURL:-}" ] && exit 22
out=""
while [ $# -gt 0 ]; do case $1 in -o) out=$2 ;; esac; shift; done
[ -n "$out" ] && cat "$CURL_ASSET" >"$out"
CURL
cat >"$STOOLS/wget" <<'WGET'
#!/bin/sh
echo "wget $*" >>"$WGET_LOG"
[ -n "${FAIL_WGET:-}" ] && exit 4
out=""
while [ $# -gt 0 ]; do
  case $1 in --*) ;; *O*) out=$2 ;; esac   # -O and combined -qO, as invoked
  shift
done
[ -n "$out" ] && cat "$WGET_ASSET" >"$out"
WGET
cat >"$STOOLS/go" <<'GO'
#!/bin/sh
echo "go $*" >>"$GO_LOG"
echo "GOOS=$GOOS GOARCH=$GOARCH CGO_ENABLED=$CGO_ENABLED" >>"$GO_LOG"
while [ $# -gt 0 ]; do
  [ "$1" = "-o" ] && { printf '#!/bin/sh\necho SKILL=${OUTSOURCE_SKILL_DIR:-unset}\nexit "${FAKE_RC:-0}"\n' >"$2"; chmod +x "$2"; }
  shift
done
GO
chmod +x "$STOOLS/curl" "$STOOLS/wget" "$STOOLS/go"

# TOOLBOX: everything the dispatcher itself needs, and NOT go/curl/wget —
# the real /usr/bin has curl, so "hide the fetcher" needs a curated PATH.
TOOLBOX="$TMP/toolbox"; mkdir -p "$TOOLBOX"
for t in sed grep head cut tr readlink find sleep chmod mv rm cat mkdir rmdir shasum sha256sum uname dirname; do
  p="$(command -v "$t" 2>/dev/null)" && ln -s "$p" "$TOOLBOX/$t"
done
# TOOLBOX's uname is shadowed by STUB's whenever STUB is on PATH.

# The fetch env shared by every download case (the base URL varies per case).
FETCHENV="PATH=$TOOLBOX:$STUB:$STOOLS CURL_LOG=$TMP/curl.log CURL_ASSET=$ASSET WGET_LOG=$TMP/wget.log WGET_ASSET=$ASSET GO_LOG=$TMP/go.log"

# run <env-assignments> <script> [args...]: stdout/stderr captured, rc in RC.
# /bin/sh is named absolutely because env resolves the interpreter AFTER
# replacing PATH, and the curated PATHs here deliberately lack it.
run() {
  local envs="$1"; shift
  eval "env $envs /bin/sh -c '\"\$@\"' sh \"\$@\"" >"$TMP/out" 2>"$TMP/err"
  RC=$?
}

reset_fixture

# ══ the core, under every shell found ══════════════════════════════════════
SHELLS="sh"
command -v dash >/dev/null 2>&1 && SHELLS="$SHELLS dash"
command -v busybox >/dev/null 2>&1 && SHELLS="$SHELLS busybox"
echo "dispatcher: shells under test: $SHELLS"

for SH in $SHELLS; do
  interp=("$SH"); [ "$SH" = busybox ] && interp=(busybox sh)
  # local builds for all four, so the mapping cases take path (a)
  for t in $TARGETS; do fake "$BIN/outsource-$t"; done
  runc() {  # <S> <M> [args...] — like run(), but under $interp with stubbed uname
    local s="$1" m="$2"; shift 2
    PATH="$STUB:$PATH" STUB_S="$s" STUB_M="$m" XDG_CACHE_HOME="$TMP/cache.core" \
      "${interp[@]}" "$BIN/outsource" "$@" >"$TMP/out" 2>"$TMP/err"
    RC=$?
  }

  # the four targets, under every uname spelling of them
  for row in Darwin:arm64:darwin-arm64 Darwin:x86_64:darwin-amd64 \
             Linux:x86_64:linux-amd64 Linux:amd64:linux-amd64 \
             Linux:aarch64:linux-arm64 Linux:arm64:linux-arm64; do
    IFS=: read -r S M want <<<"$row"
    runc "$S" "$M"
    got="$(head -1 "$TMP/out")"
    if [ "$RC" -eq 0 ] && [ "$got" = "ran=outsource-$want argc=0" ]; then ok
    else bad "[$SH] uname $S $M: want outsource-$want, got rc=$RC '$got' $(cat "$TMP/err")"; fi
  done

  # "$@" unchanged: spaces, an empty argument, a glob, a flag, quotes
  runc Linux aarch64 runs 'two words' '' '*' -n 'say "hi"' '  lead'
  expected="$(printf '%s\n' 'ran=outsource-linux-arm64 argc=7' '<runs>' '<two words>' '<>' '<*>' '<-n>' '<say "hi">' '<  lead>')"
  if [ "$RC" -eq 0 ] && [ "$(cat "$TMP/out")" = "$expected" ]; then ok
  else bad "[$SH] arguments were altered on the way through: rc=$RC, got:
$(cat "$TMP/out")"; fi

  # stdin and the exit code pass through: exec, not a child whose rc is lost.
  printf 'payload\n' >"$TMP/in"
  runc Darwin arm64 stdin <"$TMP/in"
  if grep -qx payload "$TMP/out"; then ok; else bad "[$SH] stdin did not reach the binary"; fi
  runc Darwin arm64 rc 42
  if [ "$RC" -eq 42 ]; then ok; else bad "[$SH] the binary's exit code 42 came back as $RC"; fi

  # an unknown os/arch with no local build: exit 69, ONE line, naming what was
  # detected, the four targets and ./build.sh (the old rule, kept): there is
  # no release asset for it, so nothing is fetched for it.
  runc Linux riscv64 runs json
  lines="$(grep -c . "$TMP/err")"
  if [ "$RC" -eq 69 ]; then ok; else bad "[$SH] linux/riscv64: exit $RC, want 69"; fi
  if [ "$lines" -eq 1 ] && [ ! -s "$TMP/out" ]; then ok
  else bad "[$SH] linux/riscv64: want exactly one stderr line and no stdout, got $lines line(s): $(cat "$TMP/err" "$TMP/out")"; fi
  for need in linux/riscv64 $TARGETS ./build.sh; do
    if grep -qF -- "$need" "$TMP/err"; then ok
    else bad "[$SH] the refusal does not name '$need': $(cat "$TMP/err")"; fi
  done
  runc FreeBSD amd64
  if [ "$RC" -eq 69 ] && grep -qF freebsd/amd64 "$TMP/err"; then ok
  else bad "[$SH] FreeBSD amd64: want 69 naming freebsd/amd64, got rc=$RC $(cat "$TMP/err")"; fi
  STUB_FAIL=1 runc Linux x86_64
  if [ "$RC" -eq 69 ] && grep -qF unknown/unknown "$TMP/err"; then ok
  else bad "[$SH] a failing uname: want 69 naming unknown/unknown, got rc=$RC $(cat "$TMP/err")"; fi

  # a shipped name without its exec bit is not silently run
  chmod -x "$BIN/outsource-linux-amd64"
  runc Linux x86_64
  chmod +x "$BIN/outsource-linux-amd64"
  if [ "$RC" -eq 69 ]; then ok
  else bad "[$SH] a non-executable outsource-linux-amd64: want 69, got rc=$RC $(cat "$TMP/err")"; fi

  # what ./build.sh promises a machine outside the four: a local build under
  # Go's name for it runs (the dispatcher maps uname onto GOARCH names).
  fake "$BIN/outsource-linux-riscv64"; fake "$BIN/outsource-linux-arm"; fake "$BIN/outsource-freebsd-amd64"
  for row in Linux:riscv64:linux-riscv64 Linux:armv7l:linux-arm FreeBSD:amd64:freebsd-amd64; do
    IFS=: read -r S M want <<<"$row"
    runc "$S" "$M" x
    if [ "$RC" -eq 0 ] && [ "$(head -1 "$TMP/out")" = "ran=outsource-$want argc=1" ]; then ok
    else bad "[$SH] uname $S $M with a local build present: want outsource-$want, got rc=$RC $(cat "$TMP/out" "$TMP/err")"; fi
  done
  rm -f "$BIN"/outsource-*

  # reached through symlinks, with a readlink that has no -f. A link named
  # after a tool does NOT select that tool: sh cannot set argv[0] on exec
  # (cmd/outsource/main.go). Pinned so the behaviour is a decision, not drift.
  L="$TMP/links/deeper"; mkdir -p "$L"
  fake "$BIN/outsource-darwin-arm64"
  ln -s "$BIN/outsource" "$TMP/links/runs.sh"
  ln -s ../runs.sh "$L/rel"
  for link in "$TMP/links/runs.sh" "$L/rel"; do
    PATH="$STUB:$PATH" STUB_S=Darwin STUB_M=arm64 XDG_CACHE_HOME="$TMP/cache.core" \
      "${interp[@]}" "$link" list >"$TMP/out" 2>"$TMP/err"
    RC=$?
    if [ "$RC" -eq 0 ] && [ "$(cat "$TMP/out")" = "$(printf '%s\n' 'ran=outsource-darwin-arm64 argc=1' '<list>')" ]; then ok
    else bad "[$SH] via symlink ${link#"$TMP"/}: want outsource-darwin-arm64 with only <list>, got rc=$RC $(cat "$TMP/out" "$TMP/err")"; fi
  done
  rm -rf "$TMP/links" "$BIN/outsource-darwin-arm64"
done

# by bare name from PATH, through the kernel's shebang: $0 has a directory
fake "$BIN/outsource-linux-amd64"
PATH="$BIN:$STUB:$PATH" STUB_S=Linux STUB_M=x86_64 XDG_CACHE_HOME="$TMP/cache.path" \
  outsource a >"$TMP/out" 2>"$TMP/err"; RC=$?
if [ "$RC" -eq 0 ] && [ "$(head -1 "$TMP/out")" = "ran=outsource-linux-amd64 argc=1" ]; then ok
else bad "invoked by name from PATH: got rc=$RC $(cat "$TMP/out" "$TMP/err")"; fi
rm -f "$BIN"/outsource-*

# ══ path (b): the cache ════════════════════════════════════════════════════
CACHE="$TMP/cache.b"
mkdir -p "$CACHE/outsource/$VER"
envfake "$CACHE/outsource/$VER/outsource-darwin-arm64"
: >"$CURLLOG"; : >"$GOLOG"
run "PATH=$TOOLBOX:$STUB:$STOOLS CURL_LOG=$CURLLOG CURL_ASSET=$ASSET GO_LOG=$GOLOG \
XDG_CACHE_HOME=$CACHE STUB_S=Darwin STUB_M=arm64 OUTSOURCE_RELEASE_URL=http://x/" "$BIN/outsource"
if [ "$RC" -eq 0 ] && grep -q "^SKILL=$FX/skills/outsource$" "$TMP/out"; then ok
else bad "cache hit did not exec the cached binary with the skill dir exported: rc=$RC $(cat "$TMP/out") $(cat "$TMP/err")"; fi
if [ ! -s "$CURLLOG" ] && [ ! -s "$GOLOG" ]; then ok
else bad "a cache hit still fetched or built (curl: $(cat "$CURLLOG") go: $(cat "$GOLOG"))"; fi
# an explicit OUTSOURCE_SKILL_DIR from the caller wins over the export
run "PATH=$TOOLBOX:$STUB:$STOOLS CURL_LOG=$CURLLOG CURL_ASSET=$ASSET \
XDG_CACHE_HOME=$CACHE STUB_S=Darwin STUB_M=arm64 OUTSOURCE_SKILL_DIR=/elsewhere" "$BIN/outsource"
if [ "$RC" -eq 0 ] && grep -q '^SKILL=/elsewhere$' "$TMP/out"; then ok
else bad "an explicit OUTSOURCE_SKILL_DIR was overridden: $(cat "$TMP/out")"; fi
# a cached entry without the exec bit is repaired by a verified re-fetch,
# not silently run and not left broken
chmod -x "$CACHE/outsource/$VER/outsource-darwin-arm64"
: >"$CURLLOG"
run "PATH=$TOOLBOX:$STUB:$STOOLS CURL_LOG=$CURLLOG CURL_ASSET=$ASSET \
XDG_CACHE_HOME=$CACHE STUB_S=Darwin STUB_M=arm64" "$BIN/outsource"
if [ "$RC" -eq 0 ] && [ "$(grep -c . "$CURLLOG")" -eq 1 ] && [ -x "$CACHE/outsource/$VER/outsource-darwin-arm64" ]; then ok
else bad "a non-executable cache entry was not repaired by one fetch: rc=$RC curl=$(cat "$CURLLOG")"; fi

# ══ path (c): go + source beside the skill ═════════════════════════════════
CACHE="$TMP/cache.c"; mkdir -p "$CACHE"
mkdir -p "$FX/cmd/outsource"           # the source marker the dispatcher looks for
: >"$GOLOG"; : >"$CURLLOG"
run "PATH=$TOOLBOX:$STUB:$STOOLS GO_LOG=$GOLOG CURL_LOG=$CURLLOG CURL_ASSET=$ASSET \
XDG_CACHE_HOME=$CACHE STUB_S=Darwin STUB_M=arm64" "$BIN/outsource"
if [ "$RC" -eq 0 ] && grep -q "^SKILL=$FX/skills/outsource$" "$TMP/out"; then ok
else bad "the go path did not build-and-exec from the cache with the skill dir: rc=$RC $(cat "$TMP/out") $(cat "$TMP/err")"; fi
if grep -q '^go build -trimpath -buildvcs=false -ldflags=-s -w -o .* ./cmd/outsource$' "$GOLOG" && \
   grep -q '^GOOS=darwin GOARCH=arm64 CGO_ENABLED=0$' "$GOLOG"; then ok
else bad "the go build did not use build.sh's flags: $(cat "$GOLOG")"; fi
if [ -x "$CACHE/outsource/$VER/outsource-darwin-arm64" ]; then ok
else bad "the built binary did not land executable at $CACHE/outsource/$VER/"; fi
if grep -q 'building outsource-darwin-arm64 from source' "$TMP/err" && [ "$(grep -c building "$TMP/err")" -eq 1 ]; then ok
else bad "the go path did not print exactly one building line: $(cat "$TMP/err")"; fi
if [ ! -s "$CURLLOG" ]; then ok; else bad "the go path also downloaded: $(cat "$CURLLOG")"; fi
# no source beside the skill (an install.sh copy): go present, still fetches
rm -rf "$FX/cmd"
: >"$CURLLOG"
run "PATH=$TOOLBOX:$STUB:$STOOLS CURL_LOG=$CURLLOG CURL_ASSET=$ASSET \
XDG_CACHE_HOME=$TMP/cache.c2 STUB_S=Darwin STUB_M=arm64 OUTSOURCE_RELEASE_URL=http://srv/" "$BIN/outsource"
if [ "$RC" -eq 0 ] && grep -q "curl .*http://srv/v$VER/outsource-darwin-arm64\$" "$CURLLOG"; then ok
else bad "go without source should fall through to the download: rc=$RC curl: $(cat "$CURLLOG") err: $(cat "$TMP/err")"; fi

# ══ path (d): the verified download ════════════════════════════════════════
CACHE="$TMP/cache.d"; mkdir -p "$CACHE"
: >"$CURLLOG"
run "PATH=$TOOLBOX:$STUB:$STOOLS CURL_LOG=$CURLLOG CURL_ASSET=$ASSET \
XDG_CACHE_HOME=$CACHE STUB_S=Darwin STUB_M=arm64" "$BIN/outsource"
if [ "$RC" -eq 0 ] && grep -q "^SKILL=$FX/skills/outsource$" "$TMP/out"; then ok
else bad "the download path did not verify-and-exec: rc=$RC $(cat "$TMP/out") $(cat "$TMP/err")"; fi
if grep -q -- "-fsSL -o .* https://github.com/midagedev/outsource/releases/download/v$VER/outsource-darwin-arm64\$" "$CURLLOG"; then ok
else bad "the download URL is not the default release URL: $(cat "$CURLLOG")"; fi
if [ -x "$CACHE/outsource/$VER/outsource-darwin-arm64" ]; then ok
else bad "the verified download did not land executable in the cache"; fi
if find "$CACHE" -name '.tmp*' | grep -q .; then bad "a temp file was left in the cache: $(find "$CACHE" -name '.tmp*')"; else ok; fi
# the second call does not download again
n="$(grep -c . "$CURLLOG")"
run "PATH=$TOOLBOX:$STUB:$STOOLS CURL_LOG=$CURLLOG CURL_ASSET=$ASSET \
XDG_CACHE_HOME=$CACHE STUB_S=Darwin STUB_M=arm64" "$BIN/outsource"
if [ "$RC" -eq 0 ] && [ "$(grep -c . "$CURLLOG")" -eq "$n" ]; then ok
else bad "the second call downloaded again ($(grep -c . "$CURLLOG") vs $n)"; fi
# OUTSOURCE_RELEASE_URL only moves the bytes; verification stays on
: >"$CURLLOG"
run "PATH=$TOOLBOX:$STUB:$STOOLS CURL_LOG=$CURLLOG CURL_ASSET=$ASSET \
XDG_CACHE_HOME=$TMP/cache.d2 STUB_S=Darwin STUB_M=arm64 OUTSOURCE_RELEASE_URL=http://mirror/v" "$BIN/outsource"
if [ "$RC" -eq 0 ] && grep -q "curl .*http://mirror/v/v$VER/outsource-darwin-arm64" "$CURLLOG"; then ok
else bad "OUTSOURCE_RELEASE_URL was not honored as the base: $(cat "$CURLLOG")"; fi
# wget when curl is absent (busybox alpine has only wget)
WGETBIN="$TMP/wgetbin"; mkdir -p "$WGETBIN"; ln -s "$STOOLS/wget" "$WGETBIN/wget"
: >"$WGETLOG"
run "PATH=$TOOLBOX:$STUB:$WGETBIN WGET_LOG=$WGETLOG WGET_ASSET=$ASSET \
XDG_CACHE_HOME=$TMP/cache.d3 STUB_S=Darwin STUB_M=arm64" "$BIN/outsource"
if [ "$RC" -eq 0 ] && grep -q -- "wget .*-qO .* https://github.com/midagedev/outsource/releases/download/v$VER/outsource-darwin-arm64\$" "$WGETLOG"; then ok
else bad "the wget path failed: rc=$RC $(cat "$WGETLOG") $(cat "$TMP/err")"; fi
# curl failing is named in the everything-failed block, and leaves no files
run "PATH=$TOOLBOX:$STUB:$STOOLS CURL_LOG=$CURLLOG CURL_ASSET=$ASSET FAIL_CURL=1 \
XDG_CACHE_HOME=$TMP/cache.d4 STUB_S=Darwin STUB_M=arm64" "$BIN/outsource"
if [ "$RC" -eq 69 ] && grep -q 'curl failed (rc=22)' "$TMP/err"; then ok
else bad "a failed download was not reported as such: rc=$RC $(cat "$TMP/err")"; fi
if find "$TMP/cache.d4" -type f 2>/dev/null | grep -q .; then bad "a failed download left files: $(find "$TMP/cache.d4" -type f)"; else ok; fi

# ══ the mismatch: unverified bytes never run ═══════════════════════════════
CACHE="$TMP/cache.m"; mkdir -p "$CACHE"
TAMPERED="$TMP/tampered"
cat "$ASSET" <(printf 'X') >"$TAMPERED"   # one extra byte changes the hash
: >"$CURLLOG"
want_hash="$(sum "$ASSET")"; got_hash="$(sum "$TAMPERED")"
run "PATH=$TOOLBOX:$STUB:$STOOLS CURL_LOG=$CURLLOG CURL_ASSET=$TAMPERED \
XDG_CACHE_HOME=$CACHE STUB_S=Darwin STUB_M=arm64" "$BIN/outsource"
if [ "$RC" -eq 69 ]; then ok; else bad "a tampered asset ran (rc=$RC)"; fi
if grep -qF "$want_hash" "$TMP/err" && grep -qF "$got_hash" "$TMP/err"; then ok
else bad "the mismatch refusal does not name both hashes: $(cat "$TMP/err")"; fi
if [ ! -e "$CACHE/outsource/$VER/outsource-darwin-arm64" ] && ! find "$CACHE" -type f 2>/dev/null | grep -q .; then ok
else bad "the tampered download left a file behind: $(find "$CACHE" -type f 2>/dev/null)"; fi
if [ ! -s "$TMP/out" ]; then ok; else bad "the tampered asset produced stdout: $(cat "$TMP/out")"; fi
# and through the guard shim that 69 is a block (exit 2), never a non-blocking error
cp "$GUARD_SHIM" "$BIN/git-guard.sh"
run "PATH=$TOOLBOX:$STUB:$STOOLS CURL_LOG=$CURLLOG CURL_ASSET=$TAMPERED \
XDG_CACHE_HOME=$TMP/cache.m2 STUB_S=Darwin STUB_M=arm64" /bin/bash "$BIN/git-guard.sh"
if [ "$RC" -eq 2 ] && grep -q 'fail closed' "$TMP/err"; then ok
else bad "the guard shim did not translate 69 into exit 2: rc=$RC $(cat "$TMP/err")"; fi
rm -f "$BIN/git-guard.sh"

# ══ the guard shim's other directions ══════════════════════════════════════
cp "$GUARD_SHIM" "$BIN/git-guard.sh"
# everything missing (no local, no cache, no fetcher, no source): 2, never 69
run "PATH=$TOOLBOX:$STUB XDG_CACHE_HOME=$TMP/cache.g STUB_S=Darwin STUB_M=arm64" /bin/bash "$BIN/git-guard.sh" status
if [ "$RC" -eq 2 ]; then ok; else bad "a binary-less guard path exited $RC, want 2 (block)"; fi
if grep -q 'could not produce a verified binary' "$TMP/err" && grep -q 'git-guard' "$TMP/err"; then ok
else bad "the exit-2 message does not explain itself: $(cat "$TMP/err")"; fi
# the dispatcher alone keeps exit 69 — the translation is the shim's, so the
# panel and the status line keep their non-block exit code
run "PATH=$TOOLBOX:$STUB XDG_CACHE_HOME=$TMP/cache.g2 STUB_S=Darwin STUB_M=arm64" "$BIN/outsource" runs
if [ "$RC" -eq 69 ]; then ok; else bad "the bare dispatcher should keep exit 69, got $RC"; fi
# a working binary's own exit code passes through untouched (0, 2 and 42)
envfake "$BIN/outsource-darwin-arm64"
run "PATH=$TOOLBOX:$STUB:$STOOLS CURL_LOG=$CURLLOG CURL_ASSET=$ASSET \
XDG_CACHE_HOME=$TMP/cache.g3 STUB_S=Darwin STUB_M=arm64 FAKE_RC=0" /bin/bash "$BIN/git-guard.sh"
[ "$RC" -eq 0 ] && ok || bad "guard shim broke rc=0 (got $RC)"
run "PATH=$TOOLBOX:$STUB:$STOOLS CURL_LOG=$CURLLOG CURL_ASSET=$ASSET \
XDG_CACHE_HOME=$TMP/cache.g3 STUB_S=Darwin STUB_M=arm64 FAKE_RC=42" /bin/bash "$BIN/git-guard.sh"
[ "$RC" -eq 42 ] && ok || bad "guard shim broke rc=42 (got $RC)"
run "PATH=$TOOLBOX:$STUB:$STOOLS CURL_LOG=$CURLLOG CURL_ASSET=$ASSET \
XDG_CACHE_HOME=$TMP/cache.g3 STUB_S=Darwin STUB_M=arm64 FAKE_RC=2" /bin/bash "$BIN/git-guard.sh"
[ "$RC" -eq 2 ] && ok || bad "guard shim broke rc=2 (got $RC)"
rm -f "$BIN/git-guard.sh" "$BIN/outsource-darwin-arm64"

# ══ neither curl nor wget: the block names the missing tools ═══════════════
run "PATH=$TOOLBOX:$STUB XDG_CACHE_HOME=$TMP/cache.n STUB_S=Darwin STUB_M=arm64" "$BIN/outsource" runs
if [ "$RC" -eq 69 ] && grep -q 'neither curl nor wget is installed' "$TMP/err"; then ok
else bad "the missing-fetcher case does not name the tools: rc=$RC $(cat "$TMP/err")"; fi
# the block names every attempt and the fix
for need in 'local build' 'cache' 'go build' 'download' 'curl' './build.sh' 'check the network'; do
  if grep -qiF -- "$need" "$TMP/err"; then ok; else bad "the everything-failed block does not mention '$need': $(cat "$TMP/err")"; fi
done

# ══ the lock: one fetcher, waiters take the result ═════════════════════════
CACHE="$TMP/outsource"; mkdir -p "$CACHE/$VER"
lockdir="$CACHE/.fetch-lock"
mkdir "$lockdir"
( sleep 1; cp "$ASSET" "$CACHE/$VER/outsource-darwin-arm64"; \
  chmod +x "$CACHE/$VER/outsource-darwin-arm64"; rmdir "$lockdir" ) &
holder=$!
: >"$CURLLOG"
run "PATH=$TOOLBOX:$STUB:$STOOLS CURL_LOG=$CURLLOG CURL_ASSET=$ASSET \
XDG_CACHE_HOME=$TMP STUB_S=Darwin STUB_M=arm64" "$BIN/outsource"
wait "$holder"
if [ "$RC" -eq 0 ] && grep -q "^SKILL=$FX/skills/outsource$" "$TMP/out"; then ok
else bad "a waiter did not pick up the lock holder's binary: rc=$RC $(cat "$TMP/out") $(cat "$TMP/err")"; fi
if [ ! -s "$CURLLOG" ]; then ok; else bad "a waiter downloaded anyway: $(cat "$CURLLOG")"; fi
# a stale lock (a killed fetcher's) is broken by the next taker, not waited out
rm -rf "$CACHE"
mkdir -p "$CACHE"
mkdir "$lockdir"; touch -t 200001010000 "$lockdir"
: >"$CURLLOG"
run "PATH=$TOOLBOX:$STUB:$STOOLS CURL_LOG=$CURLLOG CURL_ASSET=$ASSET \
XDG_CACHE_HOME=$TMP STUB_S=Darwin STUB_M=arm64" "$BIN/outsource"
if [ "$RC" -eq 0 ] && [ "$(grep -c . "$CURLLOG")" -eq 1 ] && [ ! -d "$lockdir" ]; then ok
else bad "a stale lock was not broken: rc=$RC curl=$(cat "$CURLLOG") lock=$([ -d "$lockdir" ] && echo present || echo gone)"; fi

# ══ pruning: current + most recent other, nothing older ════════════════════
CACHE="$TMP/cache.p"; mkdir -p "$CACHE/outsource"
mkdir -p "$CACHE/outsource/$VER" "$CACHE/outsource/9.8.0" "$CACHE/outsource/9.7.0" "$CACHE/outsource/9.6.0"
envfake "$CACHE/outsource/9.8.0/outsource-darwin-arm64"
envfake "$CACHE/outsource/9.7.0/outsource-darwin-arm64"
envfake "$CACHE/outsource/9.6.0/outsource-darwin-arm64"
touch -t 202601010300 "$CACHE/outsource/9.7.0"; touch -t 202601010400 "$CACHE/outsource/9.8.0"; touch -t 202601010200 "$CACHE/outsource/9.6.0"
: >"$CURLLOG"
run "PATH=$TOOLBOX:$STUB:$STOOLS CURL_LOG=$CURLLOG CURL_ASSET=$ASSET \
XDG_CACHE_HOME=$CACHE STUB_S=Darwin STUB_M=arm64" "$BIN/outsource"
if [ -d "$CACHE/outsource/$VER" ] && [ -x "$CACHE/outsource/$VER/outsource-darwin-arm64" ]; then ok
else bad "pruning ran before the fetch landed"; fi
if [ -d "$CACHE/outsource/9.8.0" ]; then ok; else bad "pruning deleted the previous version (9.8.0)"; fi
if [ ! -d "$CACHE/outsource/9.7.0" ] && [ ! -d "$CACHE/outsource/9.6.0" ]; then ok
else bad "pruning kept versions older than the previous one"; fi
# the current version is never deleted, even when it is the oldest
rm -rf "$CACHE"; mkdir -p "$CACHE/outsource/$VER" "$CACHE/outsource/9.8.0" "$CACHE/outsource/9.7.0"
touch -t 202001010000 "$CACHE/outsource/$VER"
envfake "$CACHE/outsource/9.8.0/outsource-darwin-arm64"; touch -t 202601010500 "$CACHE/outsource/9.8.0"
envfake "$CACHE/outsource/9.7.0/outsource-darwin-arm64"; touch -t 202601010400 "$CACHE/outsource/9.7.0"
: >"$CURLLOG"
run "PATH=$TOOLBOX:$STUB:$STOOLS CURL_LOG=$CURLLOG CURL_ASSET=$ASSET \
XDG_CACHE_HOME=$CACHE STUB_S=Darwin STUB_M=arm64" "$BIN/outsource"
if [ -d "$CACHE/outsource/$VER" ] && [ -d "$CACHE/outsource/9.8.0" ] && [ ! -d "$CACHE/outsource/9.7.0" ]; then ok
else bad "prune picked the wrong survivors: $(ls "$CACHE")"; fi
# a cache hit does not write to the cache (no prune on the hot path)
before="$(ls "$CACHE")"
run "PATH=$TOOLBOX:$STUB XDG_CACHE_HOME=$CACHE STUB_S=Darwin STUB_M=arm64" "$BIN/outsource"
if [ "$RC" -eq 0 ] && [ "$(ls "$CACHE")" = "$before" ]; then ok
else bad "a cache hit wrote to the cache"; fi

# ══ a broken manifest refuses everything, saying so ═════════════════════════
rm -f "$BIN/outsource.sha256"
run "PATH=$TOOLBOX:$STUB:$STOOLS CURL_LOG=$CURLLOG CURL_ASSET=$ASSET \
XDG_CACHE_HOME=$TMP/cache.x STUB_S=Darwin STUB_M=arm64" "$BIN/outsource"
if [ "$RC" -eq 69 ] && grep -qF 'no manifest at' "$TMP/err"; then ok
else bad "a missing manifest was not named: rc=$RC $(cat "$TMP/err")"; fi
printf 'version not-a-version\n' > "$BIN/outsource.sha256"
run "PATH=$TOOLBOX:$STUB:$STOOLS CURL_LOG=$CURLLOG CURL_ASSET=$ASSET \
XDG_CACHE_HOME=$TMP/cache.x STUB_S=Darwin STUB_M=arm64" "$BIN/outsource"
if [ "$RC" -eq 69 ] && grep -qF "no 'version X.Y.Z' line" "$TMP/err"; then ok
else bad "a manifest without a version line was not named: rc=$RC $(cat "$TMP/err")"; fi
write_manifest ""

# ══ no binary is committed to git ══════════════════════════════════════════
# (outsource-run.sh & co. share the prefix; only non-.sh names are binaries)
tracked="$(git ls-files -- 'skills/outsource/bin/outsource-*' | grep -v '\.sh$' || true)"
if [ -z "$tracked" ]; then ok
else bad "binaries are tracked in git (the design says never): $tracked"; fi
if [ -f "$ROOT/skills/outsource/bin/outsource.sha256" ]; then ok
else bad "bin/outsource.sha256 is missing — the dispatcher has nothing to verify against"; fi
if head -1 "$DISPATCHER" | grep -qx '#!/bin/sh'; then ok
else bad "bin/outsource is not the #!/bin/sh dispatcher"; fi

# ══ the real repo binary, from a cache-like dir, resolves the overlay ══════
# (the skill-dir export: without it, a cached binary derives its skill dir
# from the cache and the overlays silently stop applying)
host_t=""
case "$(uname -s)/$(uname -m)" in
  Darwin/arm64) host_t=darwin-arm64 ;;
  Darwin/x86_64) host_t=darwin-amd64 ;;
  Linux/x86_64) host_t=linux-amd64 ;;
  Linux/aarch64|Linux/arm64) host_t=linux-arm64 ;;
esac
if [ -n "$host_t" ] && [ -f "$ROOT/skills/outsource/bin/outsource-$host_t" ]; then
  SK="$TMP/realskill"; mkdir -p "$SK/skills/outsource/bin" "$SK/skills/outsource/references/overlays"
  cp "$DISPATCHER" "$SK/skills/outsource/bin/outsource"
  { echo "version $VER"; echo "$(sum "$ROOT/skills/outsource/bin/outsource-$host_t")  outsource-$host_t"; } \
    > "$SK/skills/outsource/bin/outsource.sha256"
  printf -- '---\npaths:\n  - /tmp/x\n---\ndeclared\n' > "$SK/skills/outsource/references/overlays/acme.md"
  RCACHE="$TMP/cache.real"; mkdir -p "$RCACHE/outsource/$VER"
  cp "$ROOT/skills/outsource/bin/outsource-$host_t" "$RCACHE/outsource/$VER/outsource-$host_t"
  chmod +x "$RCACHE/outsource/$VER/outsource-$host_t"
  run "PATH=$TOOLBOX:$STUB STUB_S=$(uname -s) STUB_M=$(uname -m) XDG_CACHE_HOME=$RCACHE" \
    "$SK/skills/outsource/bin/outsource" overlays --root /tmp/x
  if [ "$RC" -eq 0 ] && grep -q 'overlays/acme.md' "$TMP/out"; then ok
  else bad "a cached real binary did not resolve the skill's overlay: rc=$RC $(cat "$TMP/out") $(cat "$TMP/err")"; fi
  # the same case with the export stripped must NOT resolve it — this is what
  # the export is for, so the green above is proven against a red here
  sed '/export OUTSOURCE_SKILL_DIR/d; /skill=\$(cd "\$dir\/\.\." /d' "$DISPATCHER" > "$SK/skills/outsource/bin/outsource"
  chmod +x "$SK/skills/outsource/bin/outsource"
  run "PATH=$TOOLBOX:$STUB STUB_S=$(uname -s) STUB_M=$(uname -m) XDG_CACHE_HOME=$RCACHE" \
    "$SK/skills/outsource/bin/outsource" overlays --root /tmp/x
  if [ "$RC" -eq 0 ] && grep -q 'overlays/acme.md' "$TMP/out"; then
    bad "the overlay resolved WITHOUT the skill-dir export — the export case above proves nothing"
  else ok; fi
  # and this machine's binary runs through the real dispatcher
  if out="$("$DISPATCHER" help 2>&1)" && printf '%s' "$out" | grep -q '^usage: outsource <'; then ok
  else bad "this machine's binary did not run through bin/outsource: $out"; fi
else
  echo "dispatcher: no local $host_t build — the real-binary cases did not run (run ./build.sh first)" >&2
fi

# ══ static check, when the tool is here ════════════════════════════════════
if command -v shellcheck >/dev/null 2>&1; then
  if shellcheck --shell=sh "$DISPATCHER" >"$TMP/sc" 2>&1; then ok
  else bad "shellcheck --shell=sh found problems in bin/outsource: $(cat "$TMP/sc")"; fi
fi

printf '\ndispatcher: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
