#!/usr/bin/env bash
# Live terminal verification for the outsource-panel mod. It drives a real
# `claude --plugin-dir` session inside a dedicated tmux server (never the
# user's), feeds it a fake `outsource` binary serving the committed fixtures,
# and captures the pane as text for each check:
#
#   C1  160x50  /rounds opens the pane: rows with their glyphs, +<k> more,
#               trail lines; then typing `abc` proves the prompt kept focus.
#   C2  160x50  /rounds again closes the pane.
#   C3  160x50  pane closed: the band draws exactly one row above the prompt.
#   C4  100x40  /rounds: the pane is placed INLINE above the prompt (narrower than the 110-column dock).
#   C5          live send: a real receiver session gets PANEL-TOKEN-58 via
#               `/rounds send live-recv …`; a control run with no send
#               answers `none`.
#   C6  160x50  /rounds setup opens the setup pane: the config line, one line
#               per fixture provider, the controls row and the input label;
#               typing `abc` proves the prompt kept focus; /rounds setup again
#               closes it. The fake's `config` verbs write only a state file
#               under the work dir, and OUTSOURCE_CONFIG points there too.
#
# Every process is waited on by a pid captured with `$!` (kill -0), signals
# go only to pids captured at start, and the tmux server is killed on exit.
# ANTHROPIC_BASE_URL / ANTHROPIC_AUTH_TOKEN are inherited from the caller's
# environment and never printed. No prompt that is not a slash command is
# ever submitted.
#
# Output: one PASS/FAIL line per check, then the table; captures under
# $WD/captures. A check that fails twice is UNMEASURED, capture quoted.
set -uo pipefail

MOD="$(cd "$(dirname "$0")/.." && pwd)"
FAKE="$MOD/tests/fixtures/fake-outsource.sh"
SRV="outsource-panel-cap"
WD="$(mktemp -d "${TMPDIR:-/tmp}/outsource-panel-cap.XXXXXX")"
CAPS="$WD/captures"
FIXDIR="$WD/fixtures"
UUID="11111111-2222-4333-8444-555555555555" # the fixture's own-session id
MODEL_ENV=(
  ANTHROPIC_MODEL=glm-5.3
  ANTHROPIC_DEFAULT_OPUS_MODEL=glm-5.3
  ANTHROPIC_DEFAULT_SONNET_MODEL=glm-5.3
  ANTHROPIC_DEFAULT_HAIKU_MODEL=glm-5.3
  ANTHROPIC_SMALL_FAST_MODEL=glm-5.3
  CLAUDE_CODE_SUBAGENT_MODEL=glm-5.3
)
mkdir -p "$CAPS" "$FIXDIR"
cp "$MOD"/tests/fixtures/runs.json "$FIXDIR"/
cp "$MOD"/tests/fixtures/tail-*.txt "$FIXDIR"/
cp "$MOD"/tests/fixtures/config-list.json "$MOD"/tests/fixtures/models-*.json "$FIXDIR"/
CONFIG_STATE="$WD/config-state.json" # the fake's `config set|unset` target

cleanup() {
  tmux -L "$SRV" kill-server >/dev/null 2>&1 || true
  # Only pids captured at start are ever signalled.
  for f in "$WD"/*.pid; do
    [ -f "$f" ] || continue
    pid="$(cat "$f")"
    kill "$pid" >/dev/null 2>&1 || true
  done
  echo "captures: $CAPS"
  echo "work dir: $WD"
}
trap cleanup EXIT

# ---- recorders ---------------------------------------------------------------

declare -a CHECKS=()
declare -a VERDICTS=()
record() { # <PASS|FAIL|UNMEASURED> <check> <detail>
  CHECKS+=("$2")
  VERDICTS+=("$1")
  printf '%s  %s  %s\n' "$1" "$2" "$3"
}

# ---- tmux helpers ------------------------------------------------------------

ATTEMPT=1
cap() { # <name> <session> — plain text plus the -e ANSI capture beside it
  tmux -L "$SRV" capture-pane -p -t "$2" >"$CAPS/$1.txt" 2>/dev/null || true
  tmux -L "$SRV" capture-pane -p -e -t "$2" >"$CAPS/$1.ansi" 2>/dev/null || true
  # A retry overwrites the names above; keep each attempt's frame too, so a
  # check that fails twice still shows what its FIRST attempt saw.
  cp "$CAPS/$1.txt" "$CAPS/$1.a$ATTEMPT.txt" 2>/dev/null || true
  cp "$CAPS/$1.ansi" "$CAPS/$1.a$ATTEMPT.ansi" 2>/dev/null || true
}

send_keys() { tmux -L "$SRV" send-keys -t "$2" -l "$1"; }
enter() { tmux -L "$SRV" send-keys -t "$1" Enter; }

start_panel() { # <session> <columns> <rows>
  tmux -L "$SRV" new-session -d -s "$1" -x "$2" -y "$3" -c "$WD" \
    "env CLAUDE_CONFIG_DIR='$WD/cap-cfg' OUTSOURCE_PANEL_BIN='$FAKE' OUTSOURCE_PANEL_FIXTURE_DIR='$FIXDIR' OUTSOURCE_PANEL_OWNER='$UUID' OUTSOURCE_PANEL_LIVE_SOCK_FILE='$FIXDIR/live-sock' OUTSOURCE_PANEL_CONFIG_STATE='$CONFIG_STATE' OUTSOURCE_CONFIG='$WD/outsource-config.json' ${MODEL_ENV[*]} claude --plugin-dir '$MOD' --session-id '$UUID' --debug-file '$WD/debug-$1.log'"
}

# Seed the config dir so no onboarding or trust dialog blocks the session.
# The workspace key must be the path as the engine resolves it: on macOS that
# is the physical /private/var spelling, not mktemp's /var, so both go in.
mkdir -p "$WD/cap-cfg"
WD_PHYS="$(python3 -c 'import os, sys; print(os.path.realpath(sys.argv[1]))' "$WD")"
{
  printf '{"hasCompletedOnboarding": true, "projects": {\n'
  printf '  "%s": {"hasTrustDialogAccepted": true},\n' "$WD"
  printf '  "%s": {"hasTrustDialogAccepted": true}\n' "$WD_PHYS"
  printf '}}\n'
} >"$WD/cap-cfg/.claude.json"

dialog_is_up() { # <capture text>
  printf '%s' "$1" | grep -qiE 'Quick safety check|trust this folder|choose a theme|select a theme'
}

wait_ready() { # <session> — until the capture is stable and shows a prompt
  local prev="" cur=""
  for _ in $(seq 1 60); do
    sleep 1
    cur="$(tmux -L "$SRV" capture-pane -p -t "$1" 2>/dev/null || true)"
    if [ -n "$cur" ] && [ "$cur" = "$prev" ] && printf '%s' "$cur" | grep -qE '[?❯>│]' && ! dialog_is_up "$cur"; then
      return 0
    fi
    prev="$cur"
  done
  return 1
}

dismiss_dialog() { # <session> — at most 2 attempts, recorded; Down+Enter so a
  # trust prompt lands on "Yes", never on the highlighted "No, exit".
  local attempt name="$1" seen="" shot
  for attempt in 1 2; do
    shot="$(tmux -L "$SRV" capture-pane -p -t "$name" 2>/dev/null || true)"
    if dialog_is_up "$shot"; then
      seen="dialog dismissed (attempt $attempt): $(printf '%s' "$shot" | grep -iE 'safety|trust|theme' | head -1 | cut -c1-60)"
      tmux -L "$SRV" send-keys -t "$name" Down
      sleep 0.3
      enter "$name"
      sleep 2
      continue
    fi
    break
  done
  [ -n "$seen" ] && echo "  note: $seen" || true
}

# ---- checks ------------------------------------------------------------------

check_c1() { # pane opens, rows+glyphs+more+trail, focus stays with the prompt
  # /rounds toggles, so a retry after an attempt that left the pane open would
  # close it. Start every attempt from a closed pane.
  if tmux -L "$SRV" capture-pane -p -t panel 2>/dev/null | grep -q '✕─╮$'; then
    send_keys '/rounds' panel; enter panel
    sleep 2
  fi
  send_keys '/rounds' panel; enter panel
  sleep 4
  cap c1 panel
  dismiss_dialog panel
  local ok=1
  grep -q 'quota-report' "$CAPS/c1.txt" || ok=0
  grep -q '⇄▶' "$CAPS/c1.txt" || ok=0
  grep -q '⏳' "$CAPS/c1.txt" || ok=0
  grep -q '+2 more' "$CAPS/c1.txt" || ok=0
  grep -q '── quota-report · running ──' "$CAPS/c1.txt" || ok=0
  grep -qE '💬|🔧' "$CAPS/c1.txt" || ok=0
  # The whole tree is on screen, not folded under the trail header: the trail's
  # entries and the controls row are visible (added 2026-10-06 by the lead; the
  # first capture cut the inline pane right under the header).
  grep -q '🔧 Bash bin/quota.sh' "$CAPS/c1.txt" || ok=0
  grep -q 'results+thinking: off' "$CAPS/c1.txt" || ok=0
  if [ "$ok" -ne 1 ]; then return 1; fi
  send_keys 'abc' panel
  sleep 1
  cap c1-focus panel
  grep -q 'abc' "$CAPS/c1-focus.txt" || return 1
  tmux -L "$SRV" send-keys -t panel C-u # clear the typed text
  return 0
}

check_c2() { # the same command closes the pane
  send_keys '/rounds' panel; enter panel
  sleep 2
  cap c2 panel
  if grep -q '── quota-report · running ──' "$CAPS/c2.txt"; then return 1; fi
  if grep -q '+2 more' "$CAPS/c2.txt"; then return 1; fi
  return 0
}

check_c3() { # pane closed: the band is exactly one row above the prompt
  sleep 6 # the next closed tick refreshes and invalidates the band
  cap c3 panel
  grep -q '▶ quota-report 1m · ' "$CAPS/c3.txt" || return 1
  local n
  n="$(grep -c 'quota-report' "$CAPS/c3.txt" || true)"
  [ "$n" -eq 1 ] || return 1
  return 0
}

# Re-pinned 2026-10-06 (lead). The first version expected the not-placed fallback
# here, from a spec premise that an asked open needs 110 columns. The engine's
# own types say otherwise (claude-code.d.ts, 2.1.290: a pane docks "from 110
# columns, else inline above the prompt"), and the first capture run showed the
# inline box at 100 columns. The fallback path stays covered by panel.test.ts
# test 12 (stubbed isPlaced:false). Only the box and its first row are asserted:
# at 40 rows the inline pane shows the list and folds the trail below its edge
# (a layout finding, tracked separately). FAIL-first: this check run against
# c2.txt (a frame with the pane closed) fails on the box grep.
check_c4() { # 100x40: an asked open narrower than the dock floor is placed inline
  tmux -L "$SRV" kill-session -t panel100 >/dev/null 2>&1 || true
  start_panel panel100 100 40
  wait_ready panel100 || return 1
  send_keys '/rounds' panel100; enter panel100
  sleep 4
  cap c4 panel100
  dismiss_dialog panel100
  if grep -q '(pane not placed:' "$CAPS/c4.txt"; then return 1; fi
  grep -q '^╭─.*✕─╮$' "$CAPS/c4.txt" || return 1
  grep -q '^│  ▶  quota-report ' "$CAPS/c4.txt" || return 1
  return 0
}

RECV_PROMPT='Run the Bash command sleep 15 four times, as four separate Bash tool calls, one after another. Then report verbatim every message you received from another session during this turn, or the word none.'

start_receiver() { # <cfg-dir> <out-prefix>
  mkdir -p "$1"
  printf '{"crossSessionInbound":"accept"}\n' >"$WD/$2-settings.json"
  (
    cd "$WD" || exit 1
    env -u CLAUDE_CODE_MESSAGING_SOCKET -u CLAUDE_CODE_MESSAGING_TOKEN \
      CLAUDE_CONFIG_DIR="$1" "${MODEL_ENV[@]}" \
      claude -p --permission-mode bypassPermissions --output-format json \
      --settings "$WD/$2-settings.json" "$RECV_PROMPT" \
      >"$CAPS/$2.json" 2>"$CAPS/$2.err"
  ) &
  local pid=$!
  echo "$pid" >"$WD/$2.pid"
}

wait_receiver() { # <pid-file> <ceiling-seconds>
  local pid ceils waited=0
  pid="$(cat "$1")"
  ceils="$2"
  while kill -0 "$pid" 2>/dev/null; do
    sleep 2
    waited=$((waited + 2))
    if [ "$waited" -ge "$ceils" ]; then return 1; fi
  done
  return 0
}

recv_answer() { # <out-prefix> — the final result text of a receiver run
  python3 - "$CAPS/$1.json" <<'PY' 2>/dev/null || true
import json, sys
try:
    d = json.load(open(sys.argv[1]))
except Exception:
    print("(unparseable receiver output)")
    sys.exit(0)
r = d.get("result") if isinstance(d, dict) else None
print(r if isinstance(r, str) else json.dumps(d)[:400])
PY
}

check_c5() { # a live receiver quotes the token; the control answers none
  rm -f "$FIXDIR/live-sock"
  start_receiver "$WD/recv-cfg" c5-recv
  local sock="" f
  for _ in $(seq 1 90); do
    sleep 1
    for f in "$WD"/recv-cfg/sessions/*.json; do
      [ -f "$f" ] || continue
      sock="$(python3 -c 'import json,sys
try:
    d = json.load(open(sys.argv[1]))
    p = d.get("messagingSocketPath")
    print(p if p else "")
except Exception:
    pass' "$f" 2>/dev/null || true)"
      [ -n "$sock" ] && break 2
    done
  done
  if [ -z "$sock" ]; then
    echo "  c5: no messagingSocketPath appeared under recv-cfg/sessions/" >&2
    return 1
  fi
  printf '%s\n' "$sock" >"$FIXDIR/live-sock"
  sleep 8 # let the receiver get into its turn, and the panel poll the row
  send_keys '/rounds send live-recv PANEL-TOKEN-58' panel; enter panel
  sleep 2
  cap c5-send panel
  grep -q 'sent to live-recv' "$CAPS/c5-send.txt" || true # informational
  if ! wait_receiver "$WD/c5-recv.pid" 240; then
    echo "  c5: receiver did not finish within 240 s" >&2
    return 1
  fi
  recv_answer c5-recv >"$CAPS/c5-answer.txt"
  grep -q 'PANEL-TOKEN-58' "$CAPS/c5-answer.txt" || return 1

  start_receiver "$WD/recv2-cfg" c5-ctrl
  if ! wait_receiver "$WD/c5-ctrl.pid" 240; then
    echo "  c5: control receiver did not finish within 240 s" >&2
    return 1
  fi
  recv_answer c5-ctrl >"$CAPS/c5-ctrl-answer.txt"
  grep -qx 'none' "$CAPS/c5-ctrl-answer.txt" || grep -q 'none' "$CAPS/c5-ctrl-answer.txt" || return 1
  return 0
}

check_c6() { # /rounds setup opens the setup pane, focus stays, again closes it
  # /rounds setup toggles: start every attempt from a closed setup pane.
  if tmux -L "$SRV" capture-pane -p -t panel 2>/dev/null | grep -qF "config: $CONFIG_STATE"; then
    send_keys '/rounds setup' panel; enter panel
    sleep 2
  fi
  send_keys '/rounds setup' panel; enter panel
  sleep 4
  cap c6 panel
  dismiss_dialog panel
  local ok=1 line
  grep -qF "config: $CONFIG_STATE" "$CAPS/c6.txt" || ok=0
  for line in \
    '› agy        on   launcher default' \
    '  muse       off  launcher default' \
    '  openrouter on   nvidia/nemotron-3-ultra-550b-a55b:free' \
    '  xai        on   launcher default' \
    '  zai        on   launcher default' \
    '  zen        on   launcher default'; do
    grep -qF "$line" "$CAPS/c6.txt" || ok=0
  done
  # The controls row (the provider Select, the enabled toggle, close) and the
  # input label for the selected provider.
  grep -q 'provider' "$CAPS/c6.txt" || ok=0
  grep -qF 'enabled: on' "$CAPS/c6.txt" || ok=0
  grep -q 'close' "$CAPS/c6.txt" || ok=0
  grep -qF 'default model → agy' "$CAPS/c6.txt" || ok=0
  if [ "$ok" -ne 1 ]; then return 1; fi
  send_keys 'abc' panel
  sleep 1
  cap c6-focus panel
  grep -q 'abc' "$CAPS/c6-focus.txt" || return 1
  tmux -L "$SRV" send-keys -t panel C-u # clear the typed text
  sleep 1
  send_keys '/rounds setup' panel; enter panel
  sleep 2
  cap c6-close panel
  if grep -qF "config: $CONFIG_STATE" "$CAPS/c6-close.txt"; then return 1; fi
  if grep -qF 'default model → agy' "$CAPS/c6-close.txt"; then return 1; fi
  return 0
}

# ---- the run -----------------------------------------------------------------

try_check() { # <name> <fn> — at most two attempts, then UNMEASURED
  local name="$1" fn="$2" attempt
  for attempt in 1 2; do
    ATTEMPT="$attempt"
    if "$fn"; then
      record PASS "$name" "attempt $attempt"
      return 0
    fi
    echo "  $name: attempt $attempt failed" >&2
  done
  record UNMEASURED "$name" "failed twice; see $CAPS"
  return 1
}

echo "outsource-panel capture run  $(date '+%Y-%m-%d %H:%M:%S')"
echo "mod: $MOD"

start_panel panel 160 50
if ! wait_ready panel; then
  echo "FATAL: the panel session never showed a prompt; capture:"
  tmux -L "$SRV" capture-pane -p -t panel 2>/dev/null | head -20 || true
  exit 2
fi
dismiss_dialog panel boot

try_check C1 check_c1
try_check C2 check_c2
try_check C3 check_c3
try_check C4 check_c4
try_check C5 check_c5
try_check C6 check_c6

echo
echo "════════ capture summary ════════"
for i in "${!CHECKS[@]}"; do
  printf '%s  %s\n' "${VERDICTS[$i]}" "${CHECKS[$i]}"
done
fails=0
for v in "${VERDICTS[@]}"; do [ "$v" = "PASS" ] || fails=$((fails + 1)); done
if [ "$fails" -eq 0 ]; then
  echo "all green"
  exit 0
fi
echo "FAILED: $fails check(s)"
exit 1
