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
#   config list --json | set <key> <value> | unset <key>
#               — the setup pane's verbs over a state file seeded from
#                 config-list.json, with the confirmation lines the real
#                 `outsource config` prints. Without a state file the list
#                 serves the fixture and every write is refused (exit 1).
#   models --provider <p> … --json
#               — models-<p>.json; no fixture for <p> is a catalogue that did
#                 not load: exit 1, like the real binary.
#
# Environment:
#   OUTSOURCE_PANEL_FIXTURE_DIR   fixtures (default: this script's directory)
#   OUTSOURCE_PANEL_OWNER         the lead session id that owns the own rows
#   OUTSOURCE_PANEL_LIVE_SOCK     when set, also serve an own running row
#                                 `live-recv` carrying this messaging socket
#   OUTSOURCE_PANEL_LIVE_SOCK_FILE  same, but the socket path is read from
#                                 this file at every call (so a capture can
#                                 add the row after a receiver starts)
#   OUTSOURCE_PANEL_CONFIG_STATE  the file `config set|unset` writes (a
#                                 capture's temp dir; never a real config)
#
# It reads only the committed synthetic fixtures — never a real registry or
# a real config.
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
  config)
    shift
    exec python3 - "$FIXTURE_DIR/config-list.json" "${OUTSOURCE_PANEL_CONFIG_STATE:-}" "$@" <<'PY'
import json, os, sys

fixture, state, args = sys.argv[1], sys.argv[2], sys.argv[3:]
base = json.load(open(fixture))

def load():
    if state and os.path.exists(state):
        return state, json.load(open(state))
    return (state or base["path"]), dict(base["values"])

def fail(code, text):
    sys.stderr.write(text + "\n")
    sys.exit(code)

verb = args[0] if args else ""
if verb == "list" and args[1:] == ["--json"]:
    path, values = load()
    # As encoding/json prints it: one compact line, the map's keys sorted.
    out = {"path": path, "values": dict(sorted(values.items())), "unknown": []}
    sys.stdout.write(json.dumps(out, separators=(",", ":"), ensure_ascii=False) + "\n")
    sys.exit(0)
if (verb == "set" and len(args) == 3) or (verb == "unset" and len(args) == 2):
    if not state:
        fail(1, "fake-outsource: config writes need OUTSOURCE_PANEL_CONFIG_STATE (a temp file); refused")
    path, values = load()
    key = args[1]
    if key not in values:
        fail(64, 'outsource config: unknown key "%s"' % key)
    if verb == "unset":
        if values[key] is None:
            print("%s was not set (%s)" % (key, path))
            sys.exit(0)
        values[key] = None
        line = "%s unset (%s)" % (key, path)
    else:
        raw = args[2]
        if key.endswith(".enabled"):
            if raw not in ("true", "false"):
                fail(64, "outsource config: %s takes true or false, got: %s" % (key, raw))
            values[key] = raw == "true"
        else:
            values[key] = raw
        line = "%s = %s (%s)" % (key, raw, path)
    with open(state, "w") as f:
        json.dump(values, f, ensure_ascii=False)
    print(line)
    sys.exit(0)
fail(64, "fake-outsource: unsupported config call: %s" % " ".join(args))
PY
    ;;
  models)
    shift
    prov=""
    json=0
    while [ $# -gt 0 ]; do
      case "$1" in
        --provider) prov="${2:-}"; shift; [ $# -gt 0 ] && shift ;;
        --json) json=1; shift ;;
        --free|--tools|--policy|--refresh) shift ;;
        *) echo "models: unknown argument: $1" >&2; exit 64 ;;
      esac
    done
    if [ "$json" -ne 1 ]; then
      echo "fake-outsource: models serves --json only" >&2
      exit 64
    fi
    f="$FIXTURE_DIR/models-$prov.json"
    if [ -z "$prov" ] || [ ! -f "$f" ]; then
      echo "models: no catalogue loaded for ${prov:-any provider} (no fixture)" >&2
      exit 1
    fi
    cat "$f"
    ;;
  *)
    echo "fake-outsource: unsupported command: $cmd" >&2
    exit 64
    ;;
esac
