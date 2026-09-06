#!/usr/bin/env bash
# Runs the WhatsApp bridge and the MCP server together and exits if either dies.
set -euo pipefail

: "${GRAAB_STORE_DIR:=/data}"
: "${GRAAB_BRIDGE_ADDR:=127.0.0.1:8080}"
: "${GRAAB_MCP_TOKEN:?GRAAB_MCP_TOKEN must be set (fly secrets set GRAAB_MCP_TOKEN=...)}"

# The bridge API only listens on loopback inside the container, but a token
# still costs nothing and protects against anything else running here.
if [ -z "${GRAAB_BRIDGE_TOKEN:-}" ]; then
  GRAAB_BRIDGE_TOKEN="$(python3 -c 'import secrets; print(secrets.token_urlsafe(32))')"
  export GRAAB_BRIDGE_TOKEN
fi

umask 077
mkdir -p "$GRAAB_STORE_DIR"

echo "[entrypoint] starting bridge (store: $GRAAB_STORE_DIR, api: $GRAAB_BRIDGE_ADDR)"
/app/bin/graab-bridge &
BRIDGE_PID=$!

cleanup() {
  echo "[entrypoint] shutting down"
  kill -TERM "$BRIDGE_PID" 2>/dev/null || true
  [ -n "${MCP_PID:-}" ] && kill -TERM "$MCP_PID" 2>/dev/null || true
  wait 2>/dev/null || true
}
trap cleanup TERM INT

# Wait for the bridge's health endpoint, but don't block forever on first-run
# pairing: the MCP server is useful (bridge_status) even before pairing.
for _ in $(seq 1 30); do
  if curl -fsS "http://${GRAAB_BRIDGE_ADDR}/health" >/dev/null 2>&1; then break; fi
  if ! kill -0 "$BRIDGE_PID" 2>/dev/null; then
    echo "[entrypoint] bridge exited during startup" >&2
    wait "$BRIDGE_PID" || exit $?
  fi
  sleep 1
done

echo "[entrypoint] starting MCP server (transport: ${GRAAB_MCP_TRANSPORT:-http}, read-only: ${GRAAB_READ_ONLY:-0})"
cd /app/mcp-server
python -m graab_mcp.server &
MCP_PID=$!

# Exit when either process exits so the platform restarts the machine.
wait -n "$BRIDGE_PID" "$MCP_PID"
STATUS=$?
echo "[entrypoint] a process exited with status $STATUS; stopping the other"
cleanup
exit "$STATUS"
