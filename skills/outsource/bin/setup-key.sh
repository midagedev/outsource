#!/usr/bin/env bash
# The interactive half of credential handling: find a provider's key, or ask
# for one, then store it where bin/credential.sh will find it next time.
#
#   setup-key.sh <provider>            # zai | xai | openrouter
#   setup-key.sh <provider> --force    # re-ask even if a key is already found
#
# Run this yourself in a terminal. The launcher never prompts — it runs
# headless in the background, where a prompt hangs a round instead of
# failing it, so it points here instead.
#
# The key is read with echo off, stored at ~/.config/outsource/credentials
# with mode 0600, and never printed, logged, or passed on a command line.
set -euo pipefail

BIN="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROVIDER="${1:-}"
FORCE=0
[ "${2:-}" = "--force" ] && FORCE=1

case "$PROVIDER" in
  zai|xai|openrouter) ;;
  *) echo "usage: setup-key.sh <zai|xai|openrouter> [--force]" >&2; exit 64 ;;
esac

# CHECKER names the host `outsource verify-key` checks the key against, for
# the messages below; xai has no free endpoint (verify-key exits 3 for it).
case "$PROVIDER" in
  zai) ENV_VAR="ZAI_API_KEY"; WHERE="https://z.ai — API keys in your account settings"; CHECKER="z.ai" ;;
  xai) ENV_VAR="XAI_API_KEY"; WHERE="https://console.x.ai — API keys"; CHECKER="x.ai" ;;
  openrouter) ENV_VAR="OPENROUTER_API_KEY"; WHERE="https://openrouter.ai/settings/keys"; CHECKER="openrouter.ai" ;;
esac

CONFIG_HOME="${XDG_CONFIG_HOME:-$HOME/.config}"
STORE="$CONFIG_HOME/outsource/credentials"

# Already resolvable? Say where from, without printing it.
if [ "$FORCE" -eq 0 ] && SRC="$("$BIN/credential.sh" "$PROVIDER" --where 2>/dev/null)"; then
  echo "outsource: '$PROVIDER' already resolves from: $SRC"
  echo "nothing to do. Re-run with --force to replace it."
  exit 0
fi

echo "outsource: no key found for '$PROVIDER'."
echo "Get one at: $WHERE"
echo

[ -t 0 ] || { echo "setup-key.sh needs a terminal — run it yourself, not from a script." >&2; exit 1; }

printf 'Paste the %s key (input hidden): ' "$ENV_VAR" >&2
IFS= read -rs KEY </dev/tty
printf '\n' >&2
[ -n "$KEY" ] || { echo "setup-key.sh: nothing entered; no change made." >&2; exit 1; }

# Validate before storing, so a typo fails here instead of mid-round. The check
# itself lives in the binary (`outsource verify-key`): it is an HTTP call and a
# JSON verdict, which is exactly the work this file should not be doing in shell,
# and it is where the endpoint's quirk is documented — a bad credential comes
# back as HTTP 200 with success:false, so the body decides, not the status line.
#
# The key goes over a pipe, never an argument: an argument would show up in a
# process listing. This file's remaining job is the part shell is actually right
# for — a TTY check, a prompt with echo off, /dev/tty, and an atomic 0600 write.
#
# `|| RC=$?`, not `; RC=$?`: under set -e an assignment whose command
# substitution fails ends the script on the spot, so every non-zero verdict
# exited before the case below could say why — a rejected key left silently,
# and xai's exit 3 never reached the "stored unverified" branch (reproduced
# 2026-10-09 on bash 3.2.57).
RC=0
VERDICT="$(printf '%s' "$KEY" | "$BIN/outsource" verify-key "$PROVIDER" 2>&1)" || RC=$?
case "$RC" in
  0) printf 'checked against %s: accepted.\n' "$CHECKER" >&2 ;;
  3) echo "note: '$PROVIDER' has no free endpoint to verify against, so this key is stored unverified." >&2 ;;
  2) echo "setup-key.sh: could not reach $CHECKER to check the key. Nothing stored — re-run when you are online, or export \$$ENV_VAR to bypass the store." >&2
     exit 1 ;;
  *) echo "setup-key.sh: $CHECKER did not accept that key${VERDICT:+ ($VERDICT)}. Nothing stored." >&2
     exit 1 ;;
esac

umask 077
mkdir -p "$(dirname "$STORE")"
TMP="$(mktemp "$STORE.XXXXXX")"
# Rewrite without this provider's line, then append the new one, so a
# partial write can never leave a half-file where the key used to be.
[ ! -f "$STORE" ] || grep -v "^${ENV_VAR}=" "$STORE" > "$TMP" || true
printf '%s=%s\n' "$ENV_VAR" "$KEY" >> "$TMP"
chmod 600 "$TMP"
mv "$TMP" "$STORE"

echo "stored in $STORE (mode 600)."
echo "bin/credential.sh $PROVIDER will find it from now on; exporting \$$ENV_VAR still takes precedence."
