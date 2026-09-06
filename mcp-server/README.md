# graab-mcp

The Python half of [Graab](../README.md): an MCP server exposing WhatsApp
tools. It reads the SQLite database written by the Go bridge in `../bridge`
and calls the bridge's local HTTP API to send messages and download media.

```sh
uv sync          # install
uv run main.py   # start (stdio transport, what MCP clients expect)
uv run pytest    # tests
```

Environment:

- `GRAAB_DB_PATH` — path to `messages.db` (default `../bridge/store/messages.db`)
- `GRAAB_BRIDGE_URL` — bridge API base URL (default `http://127.0.0.1:8080`)
- `GRAAB_BRIDGE_TOKEN` — bearer token if the bridge runs with `-token`
- `GRAAB_READ_ONLY` — `1` to omit the sending tools entirely
- `GRAAB_MCP_TRANSPORT` — `stdio` (default) or `http`
- `GRAAB_MCP_HOST`, `GRAAB_MCP_PORT` — HTTP bind address (default `127.0.0.1:8765`)
- `GRAAB_MCP_TOKEN` — bearer token clients must send; required off-loopback
- `GRAAB_MCP_ALLOWED_HOSTS` — comma-separated Host values (DNS-rebinding protection)
