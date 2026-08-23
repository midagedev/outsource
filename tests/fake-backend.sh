#!/usr/bin/env bash
# Stub grok / crush / claude CLIs for launcher tests. The REAL grok-run /
# outsource-run binary is under test; only the model CLI is fake.
#
# Source after TMP is a writable directory. Installs $TMP/bin/{grok,crush,claude}
# and prepends that dir to PATH.
#
# Env the stubs honour (optional):
#   FAKE_GROK_TEXT         payload of one grok {"type":"text","data":...} event
#   FAKE_GROK_NDJSON       whole grok stream (overrides TEXT)
#   FAKE_CRUSH_OUTPUT      crush stdout (the launcher log)
#   FAKE_PROVIDER_CANARY   path touched if a stub actually execs
#
# Log shapes match internal/report/report_test.go (grok text-after-tool,
# claude-code result+modelUsage, crush plain text).
set -uo pipefail

if [ -z "${TMP:-}" ]; then
  echo "fake-backend.sh: TMP is not set" >&2
  return 2 2>/dev/null || exit 2
fi

mkdir -p "$TMP/bin"

cat > "$TMP/bin/grok" <<'FAKE'
#!/usr/bin/env bash
if [ -n "${FAKE_PROVIDER_CANARY:-}" ]; then
  : > "$FAKE_PROVIDER_CANARY"
fi
if [ -n "${FAKE_GROK_NDJSON:-}" ]; then
  printf '%s\n' "$FAKE_GROK_NDJSON"
else
  printf '{"type":"text","data":%s}\n' "$(python3 -c 'import json,os; print(json.dumps(os.environ.get("FAKE_GROK_TEXT","working, no marker")))')"
fi
printf '{"type":"end","stopReason":"end_turn"}\n'
exit 0
FAKE
chmod +x "$TMP/bin/grok"

cat > "$TMP/bin/crush" <<'FAKE'
#!/usr/bin/env bash
if [ "${1:-}" = "session" ]; then
  printf '%s\n' '{}'
  exit 0
fi
if [ -n "${FAKE_PROVIDER_CANARY:-}" ]; then
  : > "$FAKE_PROVIDER_CANARY"
fi
cat >/dev/null
printf '%s\n' "${FAKE_CRUSH_OUTPUT:-working, no marker}"
exit 0
FAKE
chmod +x "$TMP/bin/crush"

cat > "$TMP/bin/claude" <<'FAKE'
#!/usr/bin/env bash
if [ -n "${FAKE_PROVIDER_CANARY:-}" ]; then
  : > "$FAKE_PROVIDER_CANARY"
fi
cat >/dev/null
printf '%s\n' '{"session_id":"sess-identity","usage":{"input_tokens":1},"total_cost_usd":0,"modelUsage":{"glm-5.3":{"inputTokens":1}}}'
exit 0
FAKE
chmod +x "$TMP/bin/claude"

export PATH="$TMP/bin:$PATH"
