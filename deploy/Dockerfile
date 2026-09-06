# Graab: WhatsApp bridge + MCP server in one container.
#
# Build from the repository root:
#   docker build -f deploy/Dockerfile -t graab .
#
# The container runs as an unprivileged user, keeps all state under /data
# (mount a volume there), binds the bridge API to loopback only, and serves
# MCP over HTTP with bearer-token auth on port 8765.

# ---- Go bridge ----------------------------------------------------------
FROM golang:1.26-bookworm AS bridge-build
WORKDIR /src/bridge
ENV CGO_ENABLED=1
COPY bridge/go.mod bridge/go.sum ./
RUN go mod download
COPY bridge/ ./
RUN go build -trimpath -ldflags="-s -w" -o /out/graab-bridge .

# ---- Python deps --------------------------------------------------------
FROM python:3.12-slim-bookworm AS mcp-build
COPY --from=ghcr.io/astral-sh/uv:0.8 /uv /usr/local/bin/uv
WORKDIR /app/mcp-server
ENV UV_COMPILE_BYTECODE=1 UV_LINK_MODE=copy UV_PYTHON_DOWNLOADS=never
COPY mcp-server/pyproject.toml mcp-server/uv.lock mcp-server/README.md ./
RUN uv sync --frozen --no-dev --no-install-project
COPY mcp-server/ ./
RUN uv sync --frozen --no-dev

# ---- Runtime ------------------------------------------------------------
FROM python:3.12-slim-bookworm
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates ffmpeg tini curl \
 && rm -rf /var/lib/apt/lists/* \
 && groupadd --system graab && useradd --system --gid graab --home /data --shell /usr/sbin/nologin graab \
 && mkdir -p /data && chown graab:graab /data && chmod 700 /data

COPY --from=bridge-build /out/graab-bridge /app/bin/graab-bridge
COPY --from=mcp-build /app/mcp-server /app/mcp-server
COPY deploy/entrypoint.sh /app/bin/entrypoint.sh
RUN chmod 755 /app/bin/graab-bridge /app/bin/entrypoint.sh && chown -R graab:graab /app

ENV GRAAB_STORE_DIR=/data \
    GRAAB_BRIDGE_ADDR=127.0.0.1:8080 \
    GRAAB_BRIDGE_URL=http://127.0.0.1:8080 \
    GRAAB_DB_PATH=/data/messages.db \
    GRAAB_MCP_TRANSPORT=http \
    GRAAB_MCP_HOST=0.0.0.0 \
    GRAAB_MCP_PORT=8765 \
    GRAAB_LOG_MESSAGES=0 \
    PATH=/app/mcp-server/.venv/bin:/app/bin:$PATH

USER graab
WORKDIR /data
VOLUME ["/data"]
EXPOSE 8765

HEALTHCHECK --interval=30s --timeout=5s --start-period=30s \
  CMD curl -fsS http://127.0.0.1:8765/healthz >/dev/null || exit 1

ENTRYPOINT ["/usr/bin/tini", "--", "/app/bin/entrypoint.sh"]
