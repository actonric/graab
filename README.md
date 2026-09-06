# Graab · WhatsApp MCP server

Graab connects Claude (or any MCP client) to your **personal WhatsApp account**.
Claude can search your contacts, read and search message history, fetch
attachments, and send messages, images, documents and voice notes, all through
[Model Context Protocol](https://modelcontextprotocol.io) tools you control.

It is an independent implementation of the design popularised by
[lharries/whatsapp-mcp](https://github.com/lharries/whatsapp-mcp), which was
discussed on Hacker News as [item 43532967](https://news.ycombinator.com/item?id=43532967).

**How it works.** A small Go program (the *bridge*) links to WhatsApp as a
companion device, exactly like WhatsApp Web, using the
[whatsmeow](https://github.com/tulir/whatsmeow) library. It mirrors your chats
into a local SQLite file and exposes a loopback-only HTTP API for sending.
A Python MCP server reads that file and calls that API. Your messages stay on
your machine; they only reach a model when it calls a tool.

```
 phone ──(WhatsApp multi-device)──▶ bridge (Go) ──▶ store/messages.db
                                      ▲ 127.0.0.1:8080        │ read-only
                                      └───── mcp-server (Python) ◀──── Claude
```

> **Caution.** Like every MCP server that reads private data and can act on the
> world, this one is exposed to prompt injection: a message someone sends you
> could try to make the model forward your data somewhere. Read
> [the lethal trifecta](https://simonwillison.net/2025/Jun/16/the-lethal-trifecta/)
> and keep the sending tools behind approval prompts.

## Requirements

- Go 1.24 or newer (the module asks for a newer toolchain and downloads it automatically when `GOTOOLCHAIN=auto`, the default), plus a C compiler for `go-sqlite3` (`gcc` on Linux, Xcode tools on macOS, MSYS2 on Windows).
- Python 3.11+ and [uv](https://docs.astral.sh/uv/).
- Optional: `ffmpeg`, only to send non-Opus audio as voice notes.
- An MCP client: Claude Desktop, Claude Code, Cursor, etc.

## Setup

### 1. Run the bridge and pair your phone

```sh
cd bridge
go run .
```

The first run prints a QR code. On your phone: WhatsApp → Settings →
Linked devices → Link a device, and scan it. History then syncs from the
phone over the next few minutes; you will see messages scroll by. Leave the
bridge running while you use the MCP server. Sessions last about 20 days
before WhatsApp asks you to pair again.

Flags and environment variables:

| Flag | Env | Default | Meaning |
|------|-----|---------|---------|
| `-store` | `GRAAB_STORE_DIR` | `store` | Directory holding `whatsapp.db` (session keys), `messages.db` and downloaded media |
| `-addr` | `GRAAB_BRIDGE_ADDR` | `127.0.0.1:8080` | Address of the local REST API. Keep it on loopback. |
| `-log-level` | `GRAAB_LOG_LEVEL` | `INFO` | `DEBUG`, `INFO`, `WARN`, `ERROR` |

### 2. Point your MCP client at the server

Find the absolute path of `uv` (`which uv`) and of this repository, then add
this to your client's MCP config:

```json
{
  "mcpServers": {
    "whatsapp": {
      "command": "/absolute/path/to/uv",
      "args": ["--directory", "/absolute/path/to/graab/mcp-server", "run", "main.py"]
    }
  }
}
```

- **Claude Desktop:** `~/Library/Application Support/Claude/claude_desktop_config.json` (macOS) or `%APPDATA%\Claude\claude_desktop_config.json` (Windows), then restart Claude Desktop.
- **Cursor:** `~/.cursor/mcp.json`.
- **Claude Code:** `claude mcp add whatsapp -- /absolute/path/to/uv --directory /absolute/path/to/graab/mcp-server run main.py`

If the bridge stores data somewhere other than `bridge/store`, or listens on a
different port, pass `GRAAB_DB_PATH` and `GRAAB_BRIDGE_URL` in the config's
`env` block.

### 3. Try it

Ask Claude things like "what did Alice say about lunch?", "summarise the book
club group this week", or "send Bob the PDF I just downloaded". The
`bridge_status` tool tells you whether everything is wired up.

## Tools

| Tool | What it does |
|------|--------------|
| `search_contacts` | Find contacts by name, push name or phone digits |
| `list_messages` | Search messages by text, chat, sender or date, with surrounding context |
| `list_chats` | List chats by recent activity or name, with the last message |
| `get_chat` | Metadata for one chat by JID |
| `get_direct_chat_by_contact` | The one-to-one chat for a phone number |
| `get_contact_chats` | Every chat a contact has taken part in |
| `get_last_interaction` | Most recent message involving a contact |
| `get_message_context` | Messages before and after a given message |
| `bridge_status` | Whether the bridge is up, how much history is stored, ffmpeg availability |
| `send_message` | Send text to a phone number or group JID |
| `send_file` | Send an image, video, document or raw audio file, with an optional caption |
| `send_audio_message` | Send audio as a playable voice note (Ogg Opus; converts with ffmpeg) |
| `download_media` | Download a message's attachment and return the local path |

Phone numbers are digits only in international format (`14155551234`). Direct
chats have JIDs like `14155551234@s.whatsapp.net`; groups end in `@g.us`.
Messages with attachments carry a `media_type` and `filename`; pass the
message id and chat JID to `download_media` to fetch the file.

## What the bridge stores

`bridge/store/messages.db` has three tables:

- `chats` — JID, display name, whether it is a group, time of the last message.
- `contacts` — JID, phone, address-book name, push name.
- `messages` — id, chat, sender, text, timestamp (UTC ISO-8601), direction, and for attachments the type, filename, MIME type and the encrypted-media keys needed to download later.

Text, captions, locations, contacts cards, polls and events are stored as
text. Attachments store metadata only until you call `download_media`, which
saves the file under `bridge/store/media/<chat>/`. Reactions and read receipts
are not stored. Edits update the original row; deletions replace the text with
`[message deleted]`. Messages you send through the tools are recorded too.

`bridge/store/whatsapp.db` holds the session keys. Delete both files to unlink
and start over (also remove the device from Linked devices on your phone).

## Development

```sh
# Go bridge
cd bridge && go vet ./... && go test ./...

# Python MCP server
cd mcp-server && uv sync && uv run pytest
```

The Go tests exercise the store, message parsing, the Ogg Opus analyser, the
HTTP API, and the event handlers against an offline whatsmeow client. The
Python tests run every tool against a fixture database and a mocked bridge.
Nothing in the test suites contacts WhatsApp.

The bridge's REST API, should you want to script it directly:

| Method | Path | Body |
|--------|------|------|
| `GET` | `/api/status` | — |
| `POST` | `/api/send` | `{"recipient": "…", "message": "…", "media_path": "/abs/path"}` |
| `POST` | `/api/download` | `{"message_id": "…", "chat_jid": "…"}` |

## Troubleshooting

- **No QR code / pairing times out:** restart the bridge; make sure the terminal is wide enough for the code.
- **"Device limit reached":** remove an old linked device on your phone.
- **Tools say the database is missing:** the bridge has not run yet, or it is using a different `-store` directory. Set `GRAAB_DB_PATH` for the MCP server.
- **Contact names show as numbers:** names come from your phone's address book via app-state sync, which can take a minute after pairing. Group names and push names fill in as messages arrive.
- **`go-sqlite3 requires cgo`:** install a C compiler and build with `CGO_ENABLED=1`.
- **Messages out of sync:** stop the bridge, delete `bridge/store/messages.db` and `bridge/store/whatsapp.db`, and pair again.

## License

MIT. whatsmeow is MPL-2.0. WhatsApp is a trademark of Meta; this project is
not affiliated with or endorsed by Meta, and using unofficial clients may be
against WhatsApp's terms of service. Use it with your own account, at your own
risk.
