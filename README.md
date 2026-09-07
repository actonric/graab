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

**Shortcut:** `./scripts/setup-local.sh` installs Go and uv with Homebrew if
they are missing, prepares the Python environment, checks the bridge builds,
and registers the server with Claude Code. Then only step 1 below remains.

### 1. Run the bridge and pair your phone

```sh
cd bridge
go run .
```

The first run prints a QR code. On your phone: WhatsApp → Settings →
Linked devices → Link a device, and scan it. (Can't scan a terminal? The same
QR is available as an image at `http://127.0.0.1:8080/api/pair/qr.png`, or
pass `-pair-phone <your number>` to get a code to type instead.) History then syncs from the
phone over the next few minutes; you will see messages scroll by. Leave the
bridge running while you use the MCP server. Sessions last about 20 days
before WhatsApp asks you to pair again.

Flags and environment variables:

| Flag | Env | Default | Meaning |
|------|-----|---------|---------|
| `-store` | `GRAAB_STORE_DIR` | `store` | Directory holding `whatsapp.db` (session keys), `messages.db`, downloaded media and the `outbox` folder |
| `-addr` | `GRAAB_BRIDGE_ADDR` | `127.0.0.1:8080` | Address of the local REST API. Binding anywhere but loopback requires `-token`. |
| `-token` | `GRAAB_BRIDGE_TOKEN` | none | Bearer token the MCP server must present. Optional on loopback. |
| `-device-name` | `GRAAB_DEVICE_NAME` | `Local MCP Bridge` | Name shown under Linked devices on your phone; applied when pairing |
| `-device-platform` | `GRAAB_DEVICE_PLATFORM` | `desktop` | How WhatsApp presents the device: `desktop` shows the name alone, browsers (`chrome`, `firefox`, …) show as "Chrome (name)", `unknown` shows "Other device" |
| `-pair-phone` | `GRAAB_PAIR_PHONE` | none | Pair with a code typed into the phone instead of a QR (for headless servers) |
| `-media-roots` | `GRAAB_MEDIA_ROOTS` | `<store>/media`, `<store>/outbox` | Only files inside these directories can be sent |
| `-allow-recipients` | `GRAAB_ALLOWED_RECIPIENTS` | everyone | Comma-separated numbers or JIDs the model may message |
| `-read-only` | `GRAAB_READ_ONLY` | off | Refuse all sends |
| `-send-rate` | `GRAAB_SEND_RATE` | `30` | Maximum sends per minute (0 disables) |
| `-log-messages` | `GRAAB_LOG_MESSAGES` | off | Echo message contents to stdout as they arrive |
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
| `bridge_status` | Whether the bridge is up and paired, how much history is stored, ffmpeg availability |
| `pairing_qr_code` | The pairing QR as an image (or the phone code) while the bridge is not yet linked |
| `send_message` | Send text to a phone number or group JID |
| `send_file` | Send an image, video, document or raw audio file from an allowed directory, with an optional caption |
| `send_audio_message` | Send audio as a playable voice note (Ogg Opus; converts with ffmpeg) |
| `download_media` | Download a message's attachment and return the local path |

Phone numbers are digits only in international format (`14155551234`). Direct
chats have JIDs like `14155551234@s.whatsapp.net`; groups end in `@g.us`.
Messages with attachments carry a `media_type` and `filename`; pass the
message id and chat JID to `download_media` to fetch the file. Files can only
be sent from the bridge's `store/media` and `store/outbox` directories (or
whatever `-media-roots` names), so drop a file into `outbox` to send it.

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

`bridge/store/outbox/` is where you put files you want the model to be able
to send. `bridge/store/whatsapp.db` holds the session keys. Delete both files to unlink
and start over (also remove the device from Linked devices on your phone).

## Security model

Graab holds two things worth protecting: the WhatsApp session keys (whoever has
them *is* you on WhatsApp until you unlink the device) and your message
archive. It also gives a language model the ability to act on your account,
and language models can be manipulated by text in the messages they read.
The defaults are chosen with that in mind.

- **Nothing listens beyond your machine by default.** The bridge API binds to
  loopback and refuses to bind anywhere else without a token. The MCP server
  speaks stdio unless you opt into HTTP, and HTTP off-loopback requires
  `GRAAB_MCP_TOKEN`. A public deployment adds an OAuth 2.1 login in front
  (see "Hosting on a server → Public").
- **Files leave only from allowed directories.** `send_file` is confined to
  `store/media` and `store/outbox` (symlinks are resolved first), so a hijacked
  session cannot mail out `whatsapp.db`, `~/.ssh`, or `/etc/passwd`.
- **Blast radius can be capped.** `-allow-recipients` limits who the model may
  message, `-send-rate` limits how fast, and `-read-only` removes sending
  entirely (the MCP server then doesn't even register the sending tools).
- **Secrets stay out of logs.** Message contents are not printed unless you
  ask; tokens are never logged; databases are created owner-only (`0600`).
- **Prompt injection is still your problem.** The server instructions tell
  the model not to follow instructions found in messages, but that is advice,
  not enforcement. Keep sending tools behind your client's approval prompt,
  and prefer read-only mode when you only need answers.

## Hosting on a server

You can run Graab on a small always-on machine (the examples use
[Fly.io](https://fly.io)) so your phone doesn't need to be near a laptop.
`deploy/` contains a Dockerfile that runs both processes as an unprivileged
user, an entrypoint, and an annotated `fly.toml.example`. CI builds the image
and checks that the MCP endpoint comes up behind bearer auth.

The MCP server then runs over HTTP with a bearer token, and your client
connects to it remotely. Two shapes are supported; pick the private one unless
you specifically need a public URL.

### Private (recommended)

The app has no public address. You reach it through Fly's private network
from your own machine.

```sh
cp deploy/fly.toml.example fly.toml       # edit app name and region
fly launch --no-deploy --copy-config
fly volumes create graab_data --size 1 --region iad
fly secrets set GRAAB_MCP_TOKEN="$(openssl rand -hex 32)"
fly deploy
```

Open a tunnel from your laptop and point your client at it:

```sh
fly proxy 8765:8765 -a graab-whatsapp          # keep running
claude mcp add --transport http whatsapp-fly http://127.0.0.1:8765/mcp \
  --header "Authorization: Bearer <your GRAAB_MCP_TOKEN>"
```

Then pair the phone, in whichever of three ways suits you:

- **QR in the browser.** With the tunnel open, visit
  `http://127.0.0.1:8765/pair?token=<your GRAAB_MCP_TOKEN>` and scan the QR
  with WhatsApp → Settings → Linked devices → Link a device. The page
  refreshes itself as the code rotates.
- **QR in the chat.** Ask Claude to "show the WhatsApp pairing QR code". The
  `pairing_qr_code` tool returns the QR as an image you can scan from the
  screen.
- **Phone code.** Set `fly secrets set GRAAB_PAIR_PHONE=14155551234` (your
  number, digits only) before deploying; the bridge prints an eight-character
  code to `fly logs`. On the phone choose "Link with phone number instead" and
  type it. Unset the secret afterwards: `fly secrets unset GRAAB_PAIR_PHONE`.

Pairing codes expire after about two minutes; the bridge keeps requesting
fresh ones until you pair, so there is no rush to catch a particular one.

The `fly.toml.example` ships with `GRAAB_READ_ONLY=1`. Flip it to `0` when
you want sending, and consider setting `GRAAB_ALLOWED_RECIPIENTS` at the
same time.

### Public (reachable from claude.ai and cloud Claude Code)

To use Graab without your laptop in the loop, give it a public HTTPS address.
The MCP server then runs its own OAuth 2.1 authorization server, which is
what claude.ai's custom connectors and cloud Claude Code expect. There are no
user accounts: the login page asks for `GRAAB_MCP_TOKEN` once, and the client
receives its own short-lived tokens from then on.

```sh
# fly.toml in this repository is already set up for the app "graab"
# (copy deploy/fly.public.toml.example instead for a different app name).
fly launch --no-deploy --copy-config
fly volumes create graab_data --size 1 --region iad
fly secrets set GRAAB_MCP_TOKEN="$(openssl rand -hex 32)"
fly deploy
```

Pair the phone from the browser at `https://<app>.fly.dev/pair?token=<GRAAB_MCP_TOKEN>`.

Then connect a client:

- **claude.ai (web and phone):** Settings → Connectors → Add custom connector,
  URL `https://<app>.fly.dev/mcp`. Your browser is sent to the Graab login
  page; enter the secret and you are returned to claude.ai connected.
- **Claude Code (local or cloud):** `claude mcp add --transport http whatsapp https://<app>.fly.dev/mcp`,
  then `/mcp` inside a session to complete the browser login. Passing the
  secret as a header (`--header "Authorization: Bearer …"`) also still works.

How the OAuth mode protects you:

- Clients register dynamically but may only use `https://` or loopback
  redirect URLs, and every authorization requires the secret to be typed into
  the login page (PKCE is enforced by the SDK).
- The login page locks an IP out after five wrong attempts, doubling the wait
  each time, and trips a global breaker if failures arrive from many addresses.
- Access tokens last an hour, refresh tokens thirty days, and both rotate on
  refresh. Tokens are stored hashed in `oauth.json` on the volume, owner-only.
- Rotating `GRAAB_MCP_TOKEN` (`fly secrets set …`) invalidates every token
  ever issued, so that is the "log everyone out" switch.
- The `Host` header is checked against the public URL to defeat DNS rebinding.
- The pairing pages accept only the secret itself, never an OAuth token, so a
  connected client cannot re-pair the bridge.

Environment for this mode: `GRAAB_MCP_PUBLIC_URL` (turns OAuth on; must be
the exact public origin) and optionally `GRAAB_MCP_STATE_DIR` for where
`oauth.json` lives (defaults next to `messages.db`).

### What changes when hosted

- `send_file` and `send_audio_message` take paths on the server. Put files in
  `/data/outbox` (for example with `fly ssh sftp`) or send media the bridge
  downloaded into `/data/media`.
- `download_media` returns a server path; fetch it with `fly ssh sftp get`.
- Run exactly one machine. WhatsApp permits one live connection per linked
  device, and two bridges sharing a volume will fight over the session.
- Fly's logs are stored off-machine; message contents are not logged by
  default, so keep `GRAAB_LOG_MESSAGES=0`.

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

The bridge's REST API, should you want to script it directly (add
`Authorization: Bearer <token>` when the bridge runs with `-token`):

| Method | Path | Body |
|--------|------|------|
| `GET` | `/api/status` | — |
| `GET` | `/api/pair` | — (current pairing state, QR payload or phone code) |
| `GET` | `/api/pair/qr.png` | — (the QR as an image, 404 when not applicable) |
| `POST` | `/api/send` | `{"recipient": "…", "message": "…", "media_path": "/abs/path"}` |
| `POST` | `/api/download` | `{"message_id": "…", "chat_jid": "…"}` |

MCP server environment: `GRAAB_DB_PATH`, `GRAAB_BRIDGE_URL`,
`GRAAB_BRIDGE_TOKEN`, `GRAAB_READ_ONLY`, and for HTTP mode
`GRAAB_MCP_TRANSPORT=http`, `GRAAB_MCP_HOST`, `GRAAB_MCP_PORT`,
`GRAAB_MCP_TOKEN`, `GRAAB_MCP_ALLOWED_HOSTS`, `GRAAB_MCP_PUBLIC_URL` (OAuth
mode), `GRAAB_MCP_STATE_DIR`.

## Troubleshooting

- **No QR code / pairing times out:** restart the bridge; make sure the terminal is wide enough for the code.
- **"Device limit reached":** remove an old linked device on your phone.
- **Tools say the database is missing:** the bridge has not run yet, or it is using a different `-store` directory. Set `GRAAB_DB_PATH` for the MCP server.
- **Contact names show as numbers:** names come from your phone's address book via app-state sync, which can take a minute after pairing. Group names and push names fill in as messages arrive.
- **`go-sqlite3 requires cgo`:** install a C compiler and build with `CGO_ENABLED=1`.
- **Linked device shows "Other device":** WhatsApp ignores the name unless the platform type is one it knows. The default `-device-platform desktop` is verified to show the bare name (e.g. "Local MCP Bridge"); browser types show "Chrome (name)". Re-pair for a change to apply.
- **Renaming the linked device:** the name and platform are registered when you pair, so changing `-device-name` on a running bridge does nothing. Stop the bridge, remove the old entry under Linked devices on your phone, delete `bridge/store/whatsapp.db` (keep `messages.db`), and start the bridge again to pair under the new name.
- **Messages out of sync:** stop the bridge, delete `bridge/store/messages.db` and `bridge/store/whatsapp.db`, and pair again.

## License

MIT. whatsmeow is MPL-2.0. WhatsApp is a trademark of Meta; this project is
not affiliated with or endorsed by Meta, and using unofficial clients may be
against WhatsApp's terms of service. Use it with your own account, at your own
risk.
