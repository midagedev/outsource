#!/usr/bin/env bash
# glm.sh — your own interactive Claude Code session, answered by GLM.
#
# Everything else in this skill drives a model headlessly as a delegate. This
# is the other direction: the same z.ai coding-plan credential, pointed at the
# Claude Code CLI you already use, with you at the keyboard. No spec, no
# done-marker, no sentinel, no round record.
#
#   glm.sh                       an interactive session on glm-5.3
#   glm.sh --resume              every claude flag passes straight through
#   GLM_MODEL=glm-5.3-flash glm.sh   the model with eyes (see glm.md)
#
# THE GIT GUARD IS NOT HERE, deliberately. A delegate round gets one because
# nobody is watching it; here you are, and a guard that refuses your own
# commits would be theatre. glm.md's standalone caveat is the rule that
# applies instead: CI green is the definition of done, never push onto a red
# main, one commit at a time.
#
# ── Why six model variables and not one ───────────────────────────────────
#
# Measured 2026-09-20, against ~/.claude with its own `"model": "opus"` in
# settings.json, reading the per-turn `message.model` out of the session
# transcript (NOT `modelUsage`, which is a request echo — glm.md records that
# trap):
#
#   ANTHROPIC_MODEL=glm-5.3, settings say opus   -> message.model glm-5.3
#   nothing set, settings say opus               -> message.model claude-opus-5
#   ANTHROPIC_DEFAULT_OPUS_MODEL=glm-5.3         -> message.model glm-5.3
#
# So ANTHROPIC_MODEL does beat the config dir's `model` setting, and the
# middle line is what the other five variables exist for: every path that asks
# for a model by ALIAS rather than by id — the `model` setting, `/model opus`
# mid-session, a Task subagent asking for sonnet, the background haiku queries
# — is a path ANTHROPIC_MODEL does not cover, and z.ai accepts an Anthropic id
# without complaint. Redirecting each alias onto the same id is what makes the
# session honest about what is answering it.
#
# That matters more than it used to. Also measured 2026-09-20, straight at
# `$base/v1/messages` with curl: z.ai now ECHOES whatever model id it is given
# — `claude-opus-5`, `claude-sonnet-4-5-20250929`, `glm-5.2` and `glm-5.3` all
# came back naming themselves, and only an invented id was refused. The
# response `model` field therefore no longer discriminates on this endpoint,
# so an unpinned alias would be served by something unknowable and would look
# fine while it happened. Pin everything.
#
# ── The config dir is shared with your normal sessions, on purpose ────────
#
# No CLAUDE_CONFIG_DIR is set, so this session keeps your skills, your MCP
# servers, your history and `--resume`. The costs are real and worth knowing:
# your hooks run inside it, and ~/.claude/CLAUDE.md loads (measured: ~32k
# input tokens before you type anything). Export CLAUDE_CONFIG_DIR yourself
# for a session isolated from all of that.
# The key never reaches a command line. `env KEY=... claude` would put it in
# argv for as long as the env process lives, and internal/cred's contract is
# that a resolved key is printed on stdout and nowhere else. Exporting inside
# this process is scoped to it — which is only true if it is RUN, not sourced.
#
# This guard comes BEFORE `set`, and that ordering is the point: a refusal
# that first turns on errexit in the caller's interactive shell has already
# done the damage it exists to prevent. `${BASH_SOURCE[0]:-}` for the same
# reason — under zsh the array does not exist at all, so an unguarded
# reference under `set -u` would abort instead of refusing.
if [ "${BASH_SOURCE[0]:-}" != "$0" ]; then
  echo "glm.sh: run it, do not source it — sourcing would export a live API key into your shell" >&2
  return 2 2>/dev/null || exit 2
fi

set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
model="${GLM_MODEL:-glm-5.3}"

# glm-5.2 is refused, and today the reason is worse than it was. Measured
# twice on 2026-08-27, its reply named a DIFFERENT model than the request —
# that was the tell. Re-measured 2026-09-20, the endpoint echoes every id it
# accepts, glm-5.2 included, so the tell is gone and what answers it can no
# longer be checked at all. An unverifiable backend is not something to sit
# in front of; the launcher refuses it statically for the same reason, and
# ~/.claude/CLAUDE.md forbids routing it outright. There is no
# OUTSOURCE_ALLOW_MAPPED_MODEL escape here — that flag exists to re-measure a
# headless round, not to seat you in front of one.
if [ "$model" = "glm-5.2" ]; then
  echo "glm.sh: refusing glm-5.2 — its reply used to name a different model (measured twice," >&2
  echo "        2026-08-27); since 2026-09-20 z.ai echoes whatever id it is given, so what" >&2
  echo "        actually answers glm-5.2 can no longer be checked at all." >&2
  echo "        Use glm-5.3, or glm-5.3-flash when you need the model to see pixels." >&2
  exit 64
fi

if ! key="$("$here/credential.sh" zai)"; then
  # credential.sh already named every source it tried and how to set one.
  exit 1
fi
base="$("$here/credential.sh" zai --base-url https://api.z.ai/api/anthropic)"

export ANTHROPIC_BASE_URL="$base"
export ANTHROPIC_AUTH_TOKEN="$key"
# The id, and then every alias that could route around it.
export ANTHROPIC_MODEL="$model"
export ANTHROPIC_DEFAULT_OPUS_MODEL="$model"
export ANTHROPIC_DEFAULT_SONNET_MODEL="$model"
export ANTHROPIC_DEFAULT_HAIKU_MODEL="$model"
export ANTHROPIC_SMALL_FAST_MODEL="$model"
export CLAUDE_CODE_SUBAGENT_MODEL="$model"

# ── The context window Claude Code believes in is not the model's ────────
#
# Measured 2026-09-20, and this is a hard failure, not a cosmetic one. Claude
# Code does not know `glm-5.3`, so it applies an unknown-model default of
# **200000** — and enforces it client-side. A prompt of ~215k tokens died with
# `Prompt is too long` before a request was ever made, while the SAME content
# with this variable set went through and the endpoint reported 243868 input
# tokens. GLM-5.3 and GLM-5.3-Flash are documented at a 1M-token window, which
# is 1310720 exactly (z.ai's model page; the CLI reports that figure verbatim
# once told).
#
# So the default silently gave the session a sixth of the model it is talking
# to, and compacted six times sooner than it had to. Note the plan's quota is
# counted in PROMPTS, not tokens (`bin/quota.sh` prints "6430/28000
# consumed"), so a larger window costs requests, not budget.
#
# Not to be confused with CLAUDE_CODE_AUTO_COMPACT_WINDOW, which z.ai's own
# page recommends for this: measured, it moved nothing. The window is this
# variable.
if [ -z "${CLAUDE_CODE_MAX_CONTEXT_TOKENS:-}" ]; then
  export CLAUDE_CODE_MAX_CONTEXT_TOKENS="${GLM_CONTEXT_TOKENS:-1310720}"
fi

# Vendor-recommended for this setup (z.ai's own Claude Code page), and it is
# the one item on that page this script takes. The reasoning is not ours to
# measure: a session pointed at a third-party endpoint has no business sending
# telemetry and error reports to Anthropic, and the traffic is non-essential by
# Anthropic's own name for it. A caller who wants it keeps it.
if [ -z "${CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC:-}" ]; then
  export CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1
fi

# The same ceiling the headless launcher raises, for the same measured reason:
# the CLI caps one assistant turn at 32k output tokens and GLM writes a whole
# file plus its tests in one turn. A caller who pinned their own keeps it.
if [ -z "${CLAUDE_CODE_MAX_OUTPUT_TOKENS:-}" ]; then
  export CLAUDE_CODE_MAX_OUTPUT_TOKENS=64000
fi

exec claude "$@"
