import asyncio
import json
import os
import socket
import threading
import time

import httpx
import pytest
import uvicorn

from graab_mcp import bridge, server
from tests.conftest import ALICE

# A 1x1 PNG, enough to stand in for a rendered QR.
TINY_PNG = bytes.fromhex(
    "89504e470d0a1a0a0000000d49484452000000010000000108060000001f15c4890000000d4944415478da63f8cfc0f01f0005000101"
    "0e8fb2b00000000049454e44ae426082"
)


def fake_bridge(state: str):
    """A BridgeClient whose /api/pair reports the given state."""

    def handler(request: httpx.Request) -> httpx.Response:
        if request.url.path == "/api/pair":
            if state == "qr":
                return httpx.Response(200, json={"state": "qr", "mode": "qr", "code": "2@abc", "message": "scan this"})
            if state == "code":
                return httpx.Response(200, json={"state": "code", "mode": "phone", "code": "ABCD-EFGH", "message": "enter this code"})
            return httpx.Response(200, json={"state": state, "message": f"state is {state}"})
        if request.url.path == "/api/pair/qr.png":
            if state == "qr":
                return httpx.Response(200, content=TINY_PNG, headers={"content-type": "image/png"})
            return httpx.Response(404, json={"message": "no qr"})
        return httpx.Response(404)

    return bridge.BridgeClient(base_url="http://bridge.test", transport=httpx.MockTransport(handler), token="")

SEND_TOOLS = {"send_message", "send_file", "send_audio_message"}


def run(coro):
    return asyncio.run(coro)


def test_bridge_client_sends_bearer_token():
    seen = {}

    def handler(request: httpx.Request) -> httpx.Response:
        seen["auth"] = request.headers.get("authorization")
        return httpx.Response(200, json={"success": True, "message": "ok"})

    c = bridge.BridgeClient(base_url="http://bridge.test", transport=httpx.MockTransport(handler), token="tok-123")
    assert c.send("123", "hi")["success"] is True
    assert seen["auth"] == "Bearer tok-123"

    c = bridge.BridgeClient(base_url="http://bridge.test", transport=httpx.MockTransport(handler), token="")
    c.send("123", "hi")
    assert seen["auth"] is None


def test_bridge_401_is_explained():
    def handler(request: httpx.Request) -> httpx.Response:
        return httpx.Response(401, json={"success": False, "message": "missing or invalid bearer token"})

    c = bridge.BridgeClient(base_url="http://bridge.test", transport=httpx.MockTransport(handler), token="wrong")
    res = c.send("123", "hi")
    assert res["success"] is False and "GRAAB_BRIDGE_TOKEN" in res["message"]


def test_read_only_server_has_no_send_tools():
    ro = server.create_server(read_only=True)
    names = {t.name for t in asyncio.run(ro.list_tools())}
    assert not (names & SEND_TOOLS)
    assert {"list_messages", "download_media", "bridge_status"} <= names

    rw = server.create_server(read_only=False)
    assert SEND_TOOLS <= {t.name for t in asyncio.run(rw.list_tools())}


def test_read_only_env(monkeypatch):
    monkeypatch.setenv("GRAAB_READ_ONLY", "1")
    assert server.read_only_mode() is True
    monkeypatch.setenv("GRAAB_READ_ONLY", "no")
    assert server.read_only_mode() is False


def test_http_app_refuses_public_bind_without_token():
    with pytest.raises(ValueError, match="GRAAB_MCP_TOKEN"):
        server.build_http_app(server.server, token="", host="0.0.0.0")
    # Loopback without a token is allowed (local development).
    assert server.build_http_app(server.server, token="", host="127.0.0.1") is not None


def _free_port() -> int:
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


@pytest.fixture
def http_server(fixture_db):
    """Run the authenticated HTTP transport in a background thread."""
    port = _free_port()
    app = server.build_http_app(server.create_server(read_only=False), token="s3cret", host="127.0.0.1")
    config = uvicorn.Config(app, host="127.0.0.1", port=port, log_level="warning")
    srv = uvicorn.Server(config)
    thread = threading.Thread(target=srv.run, daemon=True)
    thread.start()
    deadline = time.time() + 10
    while not srv.started and time.time() < deadline:
        time.sleep(0.05)
    assert srv.started, "uvicorn did not start"
    yield f"http://127.0.0.1:{port}"
    srv.should_exit = True
    thread.join(timeout=5)


def test_http_requires_bearer_token(http_server):
    base = http_server
    assert httpx.get(f"{base}/healthz").status_code == 200

    body = {"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {"protocolVersion": "2025-06-18", "capabilities": {}, "clientInfo": {"name": "t", "version": "0"}}}
    accept = {"Accept": "application/json, text/event-stream", "Content-Type": "application/json"}

    r = httpx.post(f"{base}/mcp", json=body, headers=accept)
    assert r.status_code == 401
    assert r.headers.get("www-authenticate", "").startswith("Bearer")

    r = httpx.post(f"{base}/mcp", json=body, headers={**accept, "Authorization": "Bearer nope"})
    assert r.status_code == 401

    r = httpx.post(f"{base}/mcp", json=body, headers={**accept, "Authorization": "Bearer s3cret"})
    assert r.status_code == 200, r.text


def test_http_end_to_end_with_mcp_client(http_server):
    from mcp import ClientSession
    from mcp.client.streamable_http import streamable_http_client

    async def go():
        async with httpx.AsyncClient(headers={"Authorization": "Bearer s3cret"}, timeout=10) as hc:
            async with streamable_http_client(f"{http_server}/mcp", http_client=hc) as (r, w):
                async with ClientSession(r, w) as s:
                    await s.initialize()
                    tools = await s.list_tools()
                    res = await s.call_tool("get_chat", {"chat_jid": ALICE})
                    return {t.name for t in tools.tools}, res

    names, res = asyncio.run(go())
    assert "list_messages" in names and "send_message" in names
    assert not res.is_error
    assert res.structured_content["name"] == "Alice Liddell"


def test_pairing_tool_returns_image(monkeypatch):
    monkeypatch.setattr(bridge, "client", lambda: fake_bridge("qr"))
    res = run(server.server.call_tool("pairing_qr_code", {}))
    assert not res.is_error
    kinds = [c.type for c in res.content]
    assert kinds == ["image", "text"]
    assert res.content[0].mime_type == "image/png"
    import base64
    assert base64.b64decode(res.content[0].data) == TINY_PNG

    monkeypatch.setattr(bridge, "client", lambda: fake_bridge("code"))
    res = run(server.server.call_tool("pairing_qr_code", {}))
    assert res.content[0].type == "text" and "ABCD-EFGH" in res.content[0].text

    monkeypatch.setattr(bridge, "client", lambda: fake_bridge("paired"))
    res = run(server.server.call_tool("pairing_qr_code", {}))
    assert res.content[0].type == "text" and "paired" in res.content[0].text


def test_pair_page_requires_token_and_shows_qr(http_server, monkeypatch):
    monkeypatch.setattr(bridge, "client", lambda: fake_bridge("qr"))
    base = http_server

    assert httpx.get(f"{base}/pair").status_code == 401
    assert httpx.get(f"{base}/pair?token=wrong").status_code == 401
    # The query-token shortcut is only for the pairing pages, never for /mcp.
    r = httpx.post(f"{base}/mcp?token=s3cret", json={}, headers={"Accept": "application/json, text/event-stream"})
    assert r.status_code == 401

    page = httpx.get(f"{base}/pair?token=s3cret")
    assert page.status_code == 200
    assert "scan this" in page.text and '/pair/qr.png?ts=' in page.text and "token=s3cret" in page.text
    assert page.headers["cache-control"] == "no-store"

    img = httpx.get(f"{base}/pair/qr.png?token=s3cret")
    assert img.status_code == 200 and img.headers["content-type"] == "image/png" and img.content == TINY_PNG

    # Header auth works for the page too.
    assert httpx.get(f"{base}/pair", headers={"Authorization": "Bearer s3cret"}).status_code == 200

    monkeypatch.setattr(bridge, "client", lambda: fake_bridge("paired"))
    page = httpx.get(f"{base}/pair?token=s3cret")
    assert "✓" in page.text and "<img" not in page.text
    assert httpx.get(f"{base}/pair/qr.png?token=s3cret").status_code == 404
