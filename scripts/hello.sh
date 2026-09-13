#!/bin/bash
# Hello-world smoke test for a running horde server.
# Requires HORDE_TOKEN in the environment: dotenvx run -- scripts/hello.sh
# (or just `make hello`).
set -euo pipefail

URL=${HORDE_URL:-http://127.0.0.1:8787}
: "${HORDE_TOKEN:?HORDE_TOKEN not set — run via: dotenvx run -- scripts/hello.sh}"
AUTH="Authorization: Bearer $HORDE_TOKEN"
ACC="Accept: application/json, text/event-stream"
CT="Content-Type: application/json"
NOTE=_scratch/hello.md
SHOT=_scratch/hello.png

body() { grep '^data:' | sed 's/^data: //'; }

pretty() {
  python3 -c '
import json, sys
msg = json.load(sys.stdin)
if "error" in msg:
    sys.exit("RPC error: %s" % msg["error"])
r = msg["result"]
if r.get("isError"):
    sys.exit("tool error: %s" % r["content"][0]["text"])
print(json.dumps(r.get("structuredContent", r), indent=2))
'
}

echo "==> healthz"
curl -sf "$URL/healthz"; echo

echo "==> initialize MCP session"
HDRS=$(mktemp)
curl -sf -D "$HDRS" -o /dev/null -X POST "$URL/mcp" -H "$AUTH" -H "$ACC" -H "$CT" -d '{
  "jsonrpc":"2.0","id":1,"method":"initialize",
  "params":{"protocolVersion":"2025-03-26","capabilities":{},
            "clientInfo":{"name":"hello","version":"0"}}}'
SID=$(grep -i '^mcp-session-id:' "$HDRS" | tr -d '\r' | awk '{print $2}')
rm -f "$HDRS"
SH="Mcp-Session-Id: $SID"
curl -sf -o /dev/null -X POST "$URL/mcp" -H "$AUTH" -H "$ACC" -H "$CT" -H "$SH" \
  -d '{"jsonrpc":"2.0","method":"notifications/initialized"}'
echo "session ok"

call() { # call <id> <tool> <args-json>
  curl -sf -X POST "$URL/mcp" -H "$AUTH" -H "$ACC" -H "$CT" -H "$SH" \
    -d "{\"jsonrpc\":\"2.0\",\"id\":$1,\"method\":\"tools/call\",
         \"params\":{\"name\":\"$2\",\"arguments\":$3}}" | body | pretty
}

echo "==> create_note $NOTE"
call 2 create_note "{\"path\":\"$NOTE\",\"content\":\"# Hello, world!\\n\\nWritten via MCP at $(date -Is).\\n\",\"overwrite\":true}"

echo "==> read_note $NOTE"
call 3 read_note "{\"path\":\"$NOTE\"}"

echo "==> upload $SHOT (PUT /attachments)"
PNG=$(mktemp)
# Smallest valid 1x1 PNG, so the upload exercises the real allowlist/MIME path.
base64 -d > "$PNG" <<'B64'
iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==
B64
UP=$(curl -sf -X PUT "$URL/attachments/$SHOT?overwrite=true" -H "$AUTH" --data-binary "@$PNG")
echo "$UP"

echo "==> download it back (signed link, no auth header)"
DL=$(printf '%s' "$UP" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("download_url",""))')
if [ -n "$DL" ]; then
  GOT=$(mktemp)
  # -J would honour Content-Disposition; here we just check the bytes survive.
  curl -sf -o "$GOT" "$DL"
  cmp -s "$PNG" "$GOT" && echo "download matches upload ✔" || { echo "DOWNLOAD MISMATCH"; exit 1; }
  rm -f "$GOT"
else
  echo "(no download_url — HORDE_PUBLIC_URL is not set)"
fi
rm -f "$PNG"

echo "==> list_attachments _scratch"
call 4 list_attachments "{\"path_prefix\":\"_scratch\"}"

echo "==> delete_attachment $SHOT"
call 5 delete_attachment "{\"path\":\"$SHOT\",\"confirm\":true}"

echo "==> delete_note $NOTE"
call 6 delete_note "{\"path\":\"$NOTE\",\"confirm\":true}"

echo
echo "hello world round-trip OK ✔"
