#!/usr/bin/env bash
# One-shot local setup for Graab: installs missing tools, prepares the MCP
# server's environment, and registers it with Claude Code.
#
#   ./scripts/setup-local.sh
#
# Safe to re-run. Does not start the bridge (that needs your terminal for
# the QR code): run `cd bridge && go run .` separately.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MCP_DIR="$ROOT/mcp-server"

say()  { printf '\n\033[1m%s\033[0m\n' "$*"; }
ok()   { printf '  ✓ %s\n' "$*"; }
warn() { printf '  ! %s\n' "$*"; }

need_brew() {
  if ! command -v brew >/dev/null 2>&1; then
    warn "Homebrew is not installed, so I can't install $1 for you."
    warn "Install it from https://brew.sh, or install $1 by hand, then re-run this script."
    exit 1
  fi
}

say "Checking tools"
if command -v go >/dev/null 2>&1; then
  ok "go $(go version | awk '{print $3}')"
else
  need_brew go
  echo "  installing go with Homebrew…"
  brew install go
  ok "go installed"
fi

if command -v uv >/dev/null 2>&1; then
  ok "uv $(uv --version | awk '{print $2}')"
else
  need_brew uv
  echo "  installing uv with Homebrew…"
  brew install uv
  ok "uv installed"
fi

if command -v ffmpeg >/dev/null 2>&1; then
  ok "ffmpeg (voice-note conversion available)"
else
  warn "ffmpeg not found; voice notes must already be Ogg Opus. Optional: brew install ffmpeg"
fi

say "Preparing the MCP server environment"
( cd "$MCP_DIR" && uv sync --quiet )
ok "dependencies installed in $MCP_DIR/.venv"

say "Building the bridge"
( cd "$ROOT/bridge" && go build -o /dev/null . )
ok "bridge compiles"

UV_PATH="$(command -v uv)"
if command -v claude >/dev/null 2>&1; then
  say "Registering with Claude Code"
  claude mcp remove whatsapp >/dev/null 2>&1 || true
  claude mcp add whatsapp -- "$UV_PATH" --directory "$MCP_DIR" run main.py
  ok "registered as 'whatsapp' (claude mcp list to confirm)"
else
  say "Claude Code CLI not found; add this to your MCP client by hand:"
  cat <<EOF
  {
    "mcpServers": {
      "whatsapp": {
        "command": "$UV_PATH",
        "args": ["--directory", "$MCP_DIR", "run", "main.py"]
      }
    }
  }
EOF
fi

say "Done. Next:"
cat <<EOF
  1. Start the bridge (leave it running):   cd $ROOT/bridge && go run .
  2. In Claude Code, ask for "bridge_status", then "list my recent chats".
EOF
