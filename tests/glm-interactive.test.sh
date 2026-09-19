#!/usr/bin/env bash
# bin/glm.sh seats you in front of GLM, and the thing that can go wrong
# silently is WHICH model answers.
#
# The launcher has a model-identity assertion for that; an interactive session
# has nobody reading a sentinel, so the only defence is the environment it
# hands the CLI. This gate asserts that environment, the credential
# invariant (a key is never an argument), and the refusal of the one model id
# z.ai relabels.
#
# The stub is the instrument: a fake `claude` first on PATH that records its
# own argv and environment. A refused launch must leave no record at all —
# the same shape as internal/gitshim's gate, and for the same reason: a
# refusal that still runs the thing is not a refusal.
#
# FAIL-first, confirmed 2026-09-20 by breaking the script five ways:
#   - dropping ANTHROPIC_DEFAULT_OPUS_MODEL      -> "alias var ... is unset"
#   - dropping the glm-5.2 refusal               -> "REACHED claude despite being refused"
#   - handing the key to `env KEY=... claude`    -> "hands a variable to env(1) on a command line"
#   - dropping the source guard                  -> "sourcing was not refused"
#   - moving it below `set -euo pipefail`        -> "left shell options behind ($- = ehuB)"
#
# That last one caught the instrument first. The probe originally read $- after
# a refused source WITHOUT `|| true`, so a script that had leaked errexit killed
# the probe's own subshell on the refused return, printed no $-, and passed. A
# gate that breaks in exactly the case it exists to catch is not a gate; the
# empty reading is now a failure in its own right.
#
# That third one is why the credential check has two halves. The obvious
# runtime assertion — "the key is not in claude's argv" — does NOT catch it,
# measured: `env K=v claude` consumes the assignment out of its own argv and
# execs claude without it, so by the time the stub runs, the evidence is gone
# and the exposed process no longer exists. The window is real (`ps` can read
# another of your processes' arguments) and simply unobservable from inside
# the thing that replaced it. So the form is asserted at the source, where it
# is visible, and the runtime check stays for the mistakes it does catch —
# a key handed to claude itself as a flag.
set -uo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.." || exit 2

pass=0
fail=0
note() { printf '  %s\n' "$*"; }
ok()   { pass=$((pass + 1)); }
bad()  { fail=$((fail + 1)); printf 'FAIL: %s\n' "$*"; }

GLM=skills/outsource/bin/glm.sh
if [ ! -x "$GLM" ]; then
  bad "$GLM is missing or not executable"
  printf '\nglm-interactive: %d passed, %d failed\n' "$pass" "$fail"
  exit 1
fi

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

FAKE_KEY='zai-test-key-8f3c1d'
BIN="$TMP/bin"
mkdir -p "$BIN"

# glm.sh resolves credential.sh from its OWN directory, so the script under
# test is exercised beside a stub credential resolver rather than the real
# one: this suite must not need a configured z.ai account, and must never
# touch the user's actual key.
cp "$GLM" "$BIN/glm.sh"
cat > "$BIN/credential.sh" <<CRED
#!/usr/bin/env bash
[ "\$1" = "zai" ] || { echo "stub: unexpected provider \$1" >&2; exit 64; }
if [ "\${2:-}" = "--base-url" ]; then echo "https://stub.z.ai/api/anthropic"; exit 0; fi
echo "$FAKE_KEY"
CRED
chmod +x "$BIN/credential.sh"

# The fake claude. It writes what it saw and exits; a real launch produces the
# file, a refused one does not.
STUB="$TMP/stub"
mkdir -p "$STUB"
cat > "$STUB/claude" <<'CLAUDE'
#!/usr/bin/env bash
out="$GLM_TEST_RECORD"
{
  echo "ARGV<<$*>>"
  env | grep -E '^(ANTHROPIC_|CLAUDE_CODE_)' | sort
} > "$out"
CLAUDE
chmod +x "$STUB/claude"

# run <record-name> [args...] -> exit code in $rc, record path in $rec
run() {
  rec="$TMP/$1"; shift
  rm -f "$rec"
  ( export GLM_TEST_RECORD="$rec"
    export PATH="$STUB:$PATH"
    "$BIN/glm.sh" "$@" ) >"$TMP/out" 2>"$TMP/err"
  rc=$?
}

field() { sed -n "s/^$2=//p" "$1"; }

# ── The six ids, and the aliases that could route around them ────────────
# Measured 2026-09-20: with nothing set, a config dir whose settings.json says
# `"model": "opus"` produced a per-turn message.model of claude-opus-5, and
# z.ai served it without complaint. Every alias has to land on the same id or
# the session is answered by something nobody chose.
run six
if [ "$rc" -ne 0 ]; then
  bad "a plain launch exited $rc; stderr: $(cat "$TMP/err")"
elif [ ! -f "$rec" ]; then
  bad "a plain launch never reached claude"
else
  want=glm-5.3
  for v in ANTHROPIC_MODEL ANTHROPIC_DEFAULT_OPUS_MODEL ANTHROPIC_DEFAULT_SONNET_MODEL \
           ANTHROPIC_DEFAULT_HAIKU_MODEL ANTHROPIC_SMALL_FAST_MODEL CLAUDE_CODE_SUBAGENT_MODEL; do
    got="$(field "$rec" "$v")"
    if [ -z "$got" ]; then
      bad "alias var $v is unset — that path would ask z.ai for an Anthropic id"
    elif [ "$got" != "$want" ]; then
      bad "$v=$got, want $want"
    else
      ok
    fi
  done
  [ "$(field "$rec" ANTHROPIC_BASE_URL)" = "https://stub.z.ai/api/anthropic" ] \
    && ok || bad "base URL did not come from credential.sh --base-url"
  [ "$(field "$rec" ANTHROPIC_AUTH_TOKEN)" = "$FAKE_KEY" ] \
    && ok || bad "the key did not reach the CLI's environment"
  [ "$(field "$rec" CLAUDE_CODE_MAX_OUTPUT_TOKENS)" = "64000" ] \
    && ok || bad "the 32k-turn ceiling was not raised (see claudecode.go for the measurement)"
  # A session answered by z.ai should not be reporting to Anthropic.
  [ "$(field "$rec" CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC)" = "1" ] \
    && ok || bad "non-essential traffic to Anthropic was left on while pointed at z.ai"
  note "six model vars, base URL, token, output ceiling and telemetry all set"
fi

# ── The key is never an argument ──────────────────────────────────────────
# internal/cred's contract: a resolved key is printed on stdout and nowhere
# else — never written into a file this code creates, never passed on a
# command line. `env KEY=... claude` would break that for as long as the env
# process lived, and `ps` is readable.
if [ -f "$rec" ]; then
  if sed -n 's/^ARGV<<\(.*\)>>$/\1/p' "$rec" | grep -q "$FAKE_KEY"; then
    bad "the API key was handed to claude as an argument — ps would show it"
  else
    ok
  fi
fi
# The half the stub cannot see: a key given to env(1) is exposed in env's own
# argv until env execs the CLI, and that process is gone before any test can
# look at it. Asserted at the source instead, which is where the form is.
# Comments are stripped first: this file's own header explains the hazard by
# writing `env KEY=... claude`, and a gate that fires on the prose describing
# what it forbids is a gate nobody can keep.
env_on_argv="$(grep -vE '^[[:space:]]*#' "$GLM" \
  | grep -nE '(^|[^[:alnum:]_])env[[:space:]]+[A-Za-z_][A-Za-z0-9_]*=')"
if [ -n "$env_on_argv" ]; then
  bad "glm.sh hands a variable to env(1) on a command line: $env_on_argv"
else
  ok
  note "the key travels in the environment only — not in argv, not via env(1)"
fi

# ── Arguments pass through verbatim ──────────────────────────────────────
# The whole point of a launcher rather than an exported env block is that
# `glm --resume`, `glm --add-dir x`, `glm "a prompt"` keep working.
run passthrough --resume --add-dir /tmp/x "two words"
if [ ! -f "$rec" ]; then
  bad "a launch with arguments never reached claude"
else
  got="$(sed -n 's/^ARGV<<\(.*\)>>$/\1/p' "$rec")"
  if [ "$got" = "--resume --add-dir /tmp/x two words" ]; then
    ok
    note "claude flags pass through untouched"
  else
    bad "argv was altered: <<$got>>"
  fi
fi

# ── A caller's own ceiling survives ──────────────────────────────────────
rec="$TMP/ceiling"; rm -f "$rec"
( export GLM_TEST_RECORD="$rec"
  export PATH="$STUB:$PATH"
  export CLAUDE_CODE_MAX_OUTPUT_TOKENS=8000
  "$BIN/glm.sh" ) >/dev/null 2>&1
if [ "$(field "$rec" CLAUDE_CODE_MAX_OUTPUT_TOKENS)" = "8000" ]; then
  ok
  note "a caller-pinned output ceiling is not overwritten"
else
  bad "the script clobbered a caller's CLAUDE_CODE_MAX_OUTPUT_TOKENS"
fi

# ── GLM_MODEL chooses, and glm-5.2 is refused ────────────────────────────
rec="$TMP/flash"; rm -f "$rec"
( export GLM_TEST_RECORD="$rec"
  export PATH="$STUB:$PATH"
  export GLM_MODEL=glm-5.3-flash
  "$BIN/glm.sh" ) >/dev/null 2>&1
if [ "$(field "$rec" ANTHROPIC_MODEL)" = "glm-5.3-flash" ] \
   && [ "$(field "$rec" CLAUDE_CODE_SUBAGENT_MODEL)" = "glm-5.3-flash" ]; then
  ok
  note "GLM_MODEL reaches every one of the six"
else
  bad "GLM_MODEL=glm-5.3-flash did not reach all six vars"
fi

# glm-5.2's reply named a different model than the request (measured twice,
# 2026-08-27). Since 2026-09-20 the endpoint echoes every id it accepts, so
# that tell is gone and what answers glm-5.2 cannot be checked at all — which
# is a stronger reason to refuse it, not a weaker one. ~/.claude/CLAUDE.md
# forbids routing it; the headless launcher refuses it statically too.
rec="$TMP/mapped"; rm -f "$rec"
( export GLM_TEST_RECORD="$rec"
  export PATH="$STUB:$PATH"
  export GLM_MODEL=glm-5.2
  "$BIN/glm.sh" ) >"$TMP/out" 2>"$TMP/err"
rc=$?
if [ "$rc" -eq 0 ]; then
  bad "glm-5.2 launched with rc=0; it must be refused"
else
  ok
fi
# The load-bearing half: a refusal that still ran claude is not a refusal.
if [ -f "$rec" ]; then
  bad "glm-5.2 REACHED claude despite being refused"
else
  ok
fi
if grep -q "glm-5.2" "$TMP/err" && grep -qi "glm-5.3" "$TMP/err"; then
  ok
  note "glm-5.2 is refused, names itself, and points at the replacement"
else
  bad "the refusal must name the id and offer a working one: $(cat "$TMP/err")"
fi

# ── Sourcing it would leak the key into the user's shell ─────────────────
# The export-instead-of-env choice is only safe in a process of its own.
# `|| true` is load-bearing, and it was measured: without it, a script that
# HAS leaked errexit kills this subshell on the refused source's own non-zero
# return, `$-` is never printed, and the empty string passes the check below.
# The instrument broke in exactly the case it exists to catch.
out="$( set +eu; . "$BIN/glm.sh" 2>&1 || true; echo "OPTS=$-" )"
if printf '%s' "$out" | grep -q "do not source"; then
  ok
else
  bad "sourcing was not refused: $out"
fi
# And it must refuse WITHOUT having changed the caller's shell first. The
# guard sits above `set -euo pipefail` for exactly this: a refusal that has
# already turned on errexit in someone's interactive shell has done the damage
# it exists to prevent.
#
# FAIL-first: move the guard below the `set` line and this fires.
leaked="$(printf '%s' "$out" | sed -n 's/^OPTS=//p')"
case "$leaked" in
  "")      bad "the sourcing probe printed no \$- at all — the instrument failed, not the script" ;;
  *e*|*u*) bad "the refused source left shell options behind (\$- = $leaked)" ;;
  *)       ok; note "sourcing is refused, and refused before it can touch the caller's shell" ;;
esac
# zsh is where this actually happens — an alias in ~/.zshrc, a user who types
# `source` out of habit — and zsh has no BASH_SOURCE at all, so the guard has
# to refuse rather than trip over the missing array.
if command -v zsh >/dev/null 2>&1; then
  zout="$(zsh -c ". '$BIN/glm.sh'; echo ZOPTS=\$-" 2>&1)"
  if printf '%s' "$zout" | grep -q "do not source"; then
    ok
    note "zsh sources it and is refused too, with no BASH_SOURCE to read"
  else
    bad "zsh sourcing was not refused: $zout"
  fi
fi
if [ -n "${ANTHROPIC_AUTH_TOKEN:-}" ]; then
  bad "this shell now holds an ANTHROPIC_AUTH_TOKEN — the refusal leaked anyway"
else
  ok
fi

printf '\nglm-interactive: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
