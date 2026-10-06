#!/usr/bin/env bash
# A fake `outsource` binary for the panel's capture harness. It serves:
#
#   runs json   — the synthetic rows from runs.json, with timestamps rebuilt
#                 relative to the real now (so the visibility windows hold at
#                 capture time) and the own rows' ownerSession set from
#                 $OUTSOURCE_PANEL_OWNER.
#   tail <id>   — the fixture trail tail-<id>.txt, emulating the real
#                 renderer: the `── ` header, the `… N earlier entries` note
#                 when there are more than -n, the last -n entries, each
#                 clipped to -w characters with `…`. No fixture for the id
#                 means "no trail yet": exit 65, like the real binary.
#
# Environment:
#   OUTSOURCE_PANEL_FIXTURE_DIR   fixtures (default: this script's directory)
#   OUTSOURCE_PANEL_OWNER         the lead session id that owns the own rows
#   OUTSOURCE_PANEL_LIVE_SOCK     when set, also serve an own running row
#                                 `live-recv` carrying this messaging socket
#   OUTSOURCE_PANEL_LIVE_SOCK_FILE  same, but the socket path is read from
#                                 this file at every call (so a capture can
#                                 add the row after a receiver starts)
#
# It reads only the committed synthetic fixtures — never a real registry.
set -uo pipefail

FIXTURE_DIR="${OUTSOURCE_PANEL_FIXTURE_DIR:-$(cd "$(dirname "$0")" && pwd)}"
OWNER="${OUTSOURCE_PANEL_OWNER:-11111111-2222-4333-8444-555555555555}"
LIVE_SOCK="${OUTSOURCE_PANEL_LIVE_SOCK:-}"
LIVE_SOCK_FILE="${OUTSOURCE_PANEL_LIVE_SOCK_FILE:-}"

cmd="${1:-}"
case "$cmd" in
  runs)
    if [ -n "$LIVE_SOCK_FILE" ] && [ -f "$LIVE_SOCK_FILE" ]; then
      LIVE_SOCK="$(head -n 1 "$LIVE_SOCK_FILE" | tr -d '[:space:]')"
    fi
    exec python3 - "$FIXTURE_DIR/runs.json" "$OWNER" "$LIVE_SOCK" <<'PY'
import json, sys, time

path, owner, live_sock = sys.argv[1], sys.argv[2], sys.argv[3]
rows = json.load(open(path))
# The fixture was built against NOW0; every started/finished stamp keeps its
# offset from it, so the visibility windows (30 min, 1 h, 2 h, 5 h) hold at
# whatever moment the capture runs.
NOW0 = 1791300000
now = int(time.time())
own_anchor = "11111111-2222-4333-8444-555555555555"
out = []
for r in rows:
    r = dict(r)
    if r.get("ownerSession") == own_anchor:
        r["ownerSession"] = owner
    for field in ("startedAt", "finishedAt"):
        if isinstance(r.get(field), int):
            r[field] = now + (r[field] - NOW0)
    out.append(r)
if live_sock:
    out.append({
        "id": "rlive", "pid": 4999, "label": "live-recv",
        "provider": "zai", "harness": "claude-code", "model": "glm-5.3",
        "cwd": "/tmp/panel-fixtures/wt-live",
        "spec": "/tmp/panel-fixtures/specs/live-recv.md",
        "log": "/tmp/panel-fixtures/logs/rlive.log",
        "progressDir": "/tmp/panel-fixtures/progress/rlive",
        "trail": "/tmp/panel-fixtures/trails/rlive.jsonl",
        "trailFormat": "claude-transcript",
        "ownerSession": owner, "ownerClaudePid": "41000",
        "startedAt": now - 900, "rc": None, "finishedAt": None,
        "session": "cccc3333-0000-4000-8000-000000000015",
        "modelActual": "glm-5.3", "state": "running",
        "elapsedSeconds": 900, "idleSeconds": 5, "stalled": False,
        "messagingSocket": live_sock,
    })
json.dump(out, sys.stdout, ensure_ascii=False)
sys.stdout.write("\n")
PY
    ;;
  tail)
    id=""
    n=40
    w=160
    shift
    while [ $# -gt 0 ]; do
      case "$1" in
        -n) n="$2"; shift 2 ;;
        -w) w="$2"; shift 2 ;;
        --all|--raw|-f|--follow) shift ;;
        --max-seconds) shift 2 ;;
        -*) echo "tail: unknown flag: $1" >&2; exit 64 ;;
        *) id="$1"; shift ;;
      esac
    done
    f="$FIXTURE_DIR/tail-$id.txt"
    if [ -z "$id" ] || [ ! -f "$f" ]; then
      echo "tail: run $id has not revealed a trail yet" >&2
      exit 65
    fi
    header="$(sed -n '1p' "$f")"
    entries="$(sed -n '2,$p' "$f")"
    count="$(printf '%s\n' "$entries" | grep -c . || true)"
    echo "$header"
    if [ "$n" -gt 0 ] && [ "$count" -gt "$n" ]; then
      echo "… $((count - n)) earlier entries not shown (-n 0 for all)"
      entries="$(printf '%s\n' "$entries" | tail -n "$n")"
    fi
    printf '%s\n' "$entries" | awk -v w="$w" '{
      if (w > 0 && length($0) > w) print substr($0, 1, w) "…"; else print
    }'
    ;;
  *)
    echo "fake-outsource: unsupported command: $cmd" >&2
    exit 64
    ;;
esac
