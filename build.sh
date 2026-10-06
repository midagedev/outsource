#!/usr/bin/env bash
# Build the outsource binaries and regenerate what ships beside them.
#
#   ./build.sh              everything below (there is no partial mode)
#
# No binary is committed to git. This script produces, in one run:
#
#   dist/outsource-<os>-<arch>          the four release binaries — what
#                                       scripts/release-assets.sh uploads;
#                                       gitignored, never committed.
#   skills/outsource/bin/outsource-<os>-<arch>
#                                       the HOST target only: the dev/clone
#                                       path, where the dispatcher finds it
#                                       beside itself before any cache or
#                                       download. Also gitignored. A host
#                                       outside the four gets its own binary
#                                       under Go's name for it.
#   skills/outsource/bin/outsource.sha256
#                                       the committed manifest: the version
#                                       from .claude-plugin/plugin.json and
#                                       the sha256 of each dist/ binary. This
#                                       is the contract the dispatcher
#                                       verifies downloads against, and
#                                       tests/reproducible-build.test.sh
#                                       holds it equal to a fresh build.
#   skills/outsource/bin/*.sh           the compatibility shims.
#
# bin/outsource itself is not a build output: it is a hand-written POSIX sh
# dispatcher that resolves this machine's binary (local build, cache, source
# build, verified download — in that order).
#
# Why the tool names are shell shims and not symlinks, measured 2026-08-19:
# a symlink to the binary breaks every caller that says `bash <path>`, with
# "cannot execute binary file". That form is not hypothetical — it is the
# documented Claude Code status-line config sitting in users' settings.json
# right now (`"command": "bash ~/.claude/skills/outsource/bin/statusline.sh"`),
# it is in grok.md's launch recipe, in glm.md's guard check, and in 2 of the 3
# runs test suites. A three-line shim satisfies `bash X`, `sh X`, `./X` and a
# direct exec alike, and costs one extra fork (~7ms, measured) on that legacy
# path only. Hooks and internal call sites point at the binary directly, so the
# hot paths do not pay it.
#
# A shim carries no logic, so it is not a second implementation and cannot
# drift; it is a name. The one exception is git-guard.sh, which wraps its call
# to translate the dispatcher's exit 69 into exit 2 — the failure-direction
# contract of a guard hook (see its own comment).
set -euo pipefail
cd "$(dirname "$0")"

# Stripped: -s drops the symbol table, -w the DWARF tables. Measured 2.9MB ->
# 1.9MB. CGO off makes it fully static, which is what lets one binary work
# across libc versions. -trimpath keeps build paths out of the artifact so the
# same source produces the same bytes on any machine.
LDFLAGS='-s -w'
# -buildvcs=false is not a detail. Go otherwise stamps the module version from
# git — commit hash, commit timestamp, and "+dirty" when the tree is not clean —
# so the binary's bytes change on every commit even when no source changed.
# The manifest's hashes would then never survive a commit, and "rebuild it and
# compare" could never work. With VCS stamping off the binary is a pure
# function of the source.
BUILDFLAGS='-trimpath -buildvcs=false'
BIN=outsource
OUT=skills/outsource/bin
DIST=dist
# The shipped platforms. install.sh, the dispatcher's manifest,
# tests/reproducible-build.test.sh and scripts/release-assets.sh keep their own
# copy of this list, written out, so a change here has to be decided in all of
# them.
TARGETS=(darwin/arm64 darwin/amd64 linux/amd64 linux/arm64)

# Every compatibility name, and the tool it dispatches to. Add a line when a
# shell script is ported, and delete that script in the same commit — a name
# that resolves to both a script and the binary is exactly the drift this port
# exists to remove.
SHIMS=(
  "audit.sh:audit"
  "credential.sh:credential"
  "git-guard.sh:guard"
  "grok-run.sh:grok-run"
  "last-report.sh:last-report"
  "outsource-run.sh:outsource-run"
  "quota.sh:quota"
  "runs.sh:runs"
  "spec-lint.sh:spec-lint"
  "statusline.sh:statusline"
  "tail.sh:tail"
  "wait.sh:wait"
)

case "${1:-}" in
  ""|--all) ;;  # --all is kept as an alias from the days it meant dist/ only
  *) echo "usage: ./build.sh   (builds dist/ for the four platforms, the host binary, the manifest and the shims)" >&2; exit 2 ;;
esac

build_one() {  # <os> <arch> <outdir>
  local out="$3/$BIN-$1-$2"
  # Built under a fresh name, then moved over the old file. go build -o leaves
  # an existing output alone when the build ID inside it still matches; it only
  # touches the mtime. Measured 2026-10-06: one flipped byte in
  # outsource-linux-amd64 survived ./build.sh with a fresh mtime, so a damaged
  # binary looked rebuilt to a staleness check.
  rm -f "$out.tmp"
  CGO_ENABLED=0 GOOS="$1" GOARCH="$2" \
    go build $BUILDFLAGS -ldflags="$LDFLAGS" -o "$out.tmp" ./cmd/outsource
  mv -f "$out.tmp" "$out"
  printf '%-14s %9d bytes  %s\n' "$1/$2" "$(wc -c < "$out")" "$out"
}

mkdir -p "$DIST" "$OUT"

for target in "${TARGETS[@]}"; do
  build_one "${target%/*}" "${target#*/}" "$DIST"
done

# The host binary, in the bin dir the dispatcher checks first. Only the host:
# a checkout on one machine has no use for the other three beside the
# dispatcher (that was the committed-four-binaries design, cancelled
# 2026-10-06), and a stale one of those would outlive its source here.
# *.sh is exempt — the shims outsource-run.sh & co. carry the same prefix.
host="$(go env GOHOSTOS)/$(go env GOHOSTARCH)"
for f in "$OUT"/$BIN-*; do
  [ -f "$f" ] || continue
  case $f in
    *.sh) continue ;;
    */$BIN-"${host%/*}"-"${host#*/}") continue ;;
    *) echo "removing a non-host local build superseded by this design: $f"; rm -f "$f" ;;
  esac
done
build_one "${host%/*}" "${host#*/}" "$OUT"

# The manifest: what the dispatcher trusts, so it is written from the dist/
# bytes that release-assets.sh uploads — not from the host copy (they are
# equal by reproducibility, but the release artifact is the source of truth).
version="$(sed -nE 's/.*"version": *"([^"]+)".*/\1/p' .claude-plugin/plugin.json | head -n 1)"
if [ -z "$version" ]; then
  echo "build: could not read .version from .claude-plugin/plugin.json" >&2
  exit 1
fi
manifest="$OUT/$BIN.sha256"
{
  echo "version $version"
  for target in "${TARGETS[@]}"; do
    f="$DIST/$BIN-${target%/*}-${target#*/}"
    # The hash tool prints the path as given ("dist/outsource-…"); the
    # manifest's lines must name the bare asset, because that is the name the
    # dispatcher and release-assets.sh grep for.
    if command -v sha256sum >/dev/null 2>&1; then sha256sum "$f"; else shasum -a 256 "$f"; fi |
      sed "s#  $DIST/#  #"
  done
} > "$manifest.tmp"
mv -f "$manifest.tmp" "$manifest"
echo "manifest $manifest (version $version)"

write_shims() {  # <dir>
  local d="$1" entry name tool
  for entry in "${SHIMS[@]}"; do
    name="${entry%%:*}"; tool="${entry#*:}"
    if [ "$name" = git-guard.sh ]; then
      cat > "$d/$name" <<SHIM
#!/usr/bin/env bash
# Compatibility name for the outsource binary; the implementation is Go, in
# cmd/outsource and internal/$tool. Generated by build.sh — do not edit.
#
# This is a shim rather than a symlink because callers say \`bash <path>\`
# (the status-line config, the launch recipes, the test suites), and bash
# cannot execute a binary. For a fork-free call, invoke: outsource $tool ...
#
# Unlike its siblings this shim carries four lines of code, and they are its
# one job: Claude Code reads a PreToolUse hook's exit 2 as "block" and any
# OTHER non-zero as a non-blocking error — the tool runs. The dispatcher's
# "no binary could be produced" is exit 69, and unwrapped that would turn a
# failed fetch into an open git guard. 69 is no binary's other meaning
# (checked against cmd/ and internal/), so translating it here cannot mask a
# guard verdict. The launcher's own hook command names the per-arch binary
# directly and never passes through the dispatcher; this wrapper is for the
# paths that reach the guard by name — glm.md's guard check, a user-wired
# hook, the codex sidecar.
"\$(dirname "\${BASH_SOURCE[0]}")/$BIN" $tool "\$@"
rc=\$?
if [ "\$rc" -eq 69 ]; then
  echo "outsource git-guard: no verified outsource binary could be produced; blocking every git call (exit 2, fail closed). Fix: install Go and run ./build.sh, or install curl/wget and check the network." >&2
  exit 2
fi
exit "\$rc"
SHIM
    else
      cat > "$d/$name" <<SHIM
#!/usr/bin/env bash
# Compatibility name for the outsource binary; the implementation is Go, in
# cmd/outsource and internal/$tool. Generated by build.sh — do not edit.
#
# This is a shim rather than a symlink because callers say \`bash <path>\`
# (the status-line config, the launch recipes, the test suites), and bash
# cannot execute a binary. For a fork-free call, invoke: outsource $tool ...
exec "\$(dirname "\${BASH_SOURCE[0]}")/$BIN" $tool "\$@"
SHIM
    fi
    chmod +x "$d/$name"
  done
}

write_shims "$OUT"
echo "built ${#TARGETS[@]} release binaries into $DIST/ + the $host binary + ${#SHIMS[@]} shim(s); $OUT/$BIN is the dispatcher ($(wc -c < "$OUT/$BIN" | tr -d ' ') bytes)"
