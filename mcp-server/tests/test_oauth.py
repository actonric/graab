"""End-to-end tests for the OAuth 2.1 mode used by cloud clients."""

import asyncio
import base64
import hashlib
import secrets
import socket
import threading
import time
from pathlib import Path
from urllib.parse import parse_qs, urlparse

import httpx
import pytest
import uvicorn

from graab_mcp import server
from graab_mcp.oauth import GraabOAuthProvider, LoginRateLimiter, OAuthStore
from tests.conftest import ALICE

SECRET = "correct-horse-battery-staple-42"
INIT = {
    "jsonrpc": "2.0", "id": 1, "method": "initialize",
    "params": {"protocolVersion": "2025-06-18", "capabilities": {}, "clientInfo": {"name": "t", "version": "0"}},
}
ACCEPT = {"Accept": "application/json, text/event-stream", "Content-Type": "application/json"}


def _free_port() -> int:
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


@pytest.fixture
def oauth_server(fixture_db, tmp_path):
    port = _free_port()
    base = f"http://127.0.0.1:{port}"
    # Generous limiter so the happy-path tests never trip it; lockout has its own unit test.
    app = server.build_http_app(
        token=SECRET, host="127.0.0.1", public_url=base, state_dir=str(tmp_path), read_only=False,
        limiter=LoginRateLimiter(threshold=100),
    )
    srv = uvicorn.Server(uvicorn.Config(app, host="127.0.0.1", port=port, log_level="warning"))
    thread = threading.Thread(target=srv.run, daemon=True)
    thread.start()
    deadline = time.time() + 10
    while not srv.started and time.time() < deadline:
        time.sleep(0.05)
    assert srv.started
    yield base, tmp_path / "oauth.json"
    srv.should_exit = True
    thread.join(timeout=5)


def _pkce():
    verifier = secrets.token_urlsafe(48)
    challenge = base64.urlsafe_b64encode(hashlib.sha256(verifier.encode()).digest()).decode().rstrip("=")
    return verifier, challenge


def _register(base: str, redirect: str = "http://127.0.0.1:9/callback") -> dict:
    r = httpx.post(f"{base}/register", json={
        "redirect_uris": [redirect], "client_name": "Test client",
        "token_endpoint_auth_method": "none", "grant_types": ["authorization_code", "refresh_token"],
        "response_types": ["code"],
    })
    assert r.status_code == 201, r.text
    return r.json()


def _login_for_code(base: str, client: dict, challenge: str, secret: str = SECRET) -> str:
    """Drive /authorize → /login → redirect and return the authorization code."""
    r = httpx.get(f"{base}/authorize", params={
        "response_type": "code", "client_id": client["client_id"], "redirect_uri": client["redirect_uris"][0],
        "code_challenge": challenge, "code_challenge_method": "S256", "state": "xyz", "scope": "whatsapp",
    }, follow_redirects=False)
    assert r.status_code in (302, 307), r.text
    login_url = r.headers["location"]
    assert login_url.startswith(f"{base}/login?txn=")
    page = httpx.get(login_url)
    assert page.status_code == 200 and "Test client" in page.text and "wants to connect" in page.text
    txn = parse_qs(urlparse(login_url).query)["txn"][0]
    r = httpx.post(f"{base}/login", data={"txn": txn, "secret": secret}, follow_redirects=False)
    assert r.status_code == 302, r.text
    target = urlparse(r.headers["location"])
    assert target.netloc == "127.0.0.1:9" and target.path == "/callback"
    q = parse_qs(target.query)
    assert q["state"] == ["xyz"]
    return q["code"][0]


def _exchange(base: str, client: dict, code: str, verifier: str) -> dict:
    r = httpx.post(f"{base}/token", data={
        "grant_type": "authorization_code", "code": code, "code_verifier": verifier,
        "client_id": client["client_id"], "redirect_uri": client["redirect_uris"][0],
    })
    assert r.status_code == 200, r.text
    return r.json()


def _mcp(base: str, token: str | None) -> int:
    headers = dict(ACCEPT)
    if token:
        headers["Authorization"] = f"Bearer {token}"
    return httpx.post(f"{base}/mcp", json=INIT, headers=headers).status_code


def test_discovery_and_registration(oauth_server):
    base, _ = oauth_server
    meta = httpx.get(f"{base}/.well-known/oauth-authorization-server").json()
    assert meta["issuer"] == base
    assert meta["authorization_endpoint"] == f"{base}/authorize"
    assert meta["token_endpoint"] == f"{base}/token"
    assert meta["registration_endpoint"] == f"{base}/register"
    assert "S256" in meta["code_challenge_methods_supported"]

    prm = httpx.get(f"{base}/.well-known/oauth-protected-resource/mcp")
    assert prm.status_code == 200 and prm.json()["authorization_servers"] == [base]

    client = _register(base)
    assert client["client_id"]

    bad = httpx.post(f"{base}/register", json={"redirect_uris": ["http://evil.example/cb"], "client_name": "x"})
    assert bad.status_code == 400 and bad.json()["error"] == "invalid_redirect_uri"


def test_full_authorization_code_flow(oauth_server):
    base, state_file = oauth_server
    client = _register(base)
    verifier, challenge = _pkce()

    # Unauthenticated /mcp is refused and points at the resource metadata.
    r = httpx.post(f"{base}/mcp", json=INIT, headers=ACCEPT)
    assert r.status_code == 401 and "resource_metadata" in r.headers.get("www-authenticate", "")

    code = _login_for_code(base, client, challenge)

    # Wrong verifier is refused; the code survives for the right one.
    r = httpx.post(f"{base}/token", data={
        "grant_type": "authorization_code", "code": code, "code_verifier": "nope",
        "client_id": client["client_id"], "redirect_uri": client["redirect_uris"][0],
    })
    assert r.status_code == 400

    tokens = _exchange(base, client, code, verifier)
    assert tokens["token_type"].lower() == "bearer" and tokens["refresh_token"] and tokens["expires_in"] == 3600
    access = tokens["access_token"]

    # A code is single-use.
    assert httpx.post(f"{base}/token", data={
        "grant_type": "authorization_code", "code": code, "code_verifier": verifier,
        "client_id": client["client_id"], "redirect_uri": client["redirect_uris"][0],
    }).status_code == 400

    assert _mcp(base, access) == 200
    assert _mcp(base, SECRET) == 200          # static secret still works for header clients
    assert _mcp(base, "garbage") == 401
    assert _mcp(base, None) == 401

    # Tokens are stored hashed and the store is owner-only.
    raw = state_file.read_text()
    assert access not in raw and tokens["refresh_token"] not in raw and code not in raw
    assert (state_file.stat().st_mode & 0o777) == 0o600

    # Refresh rotates both tokens; the old pair dies.
    r = httpx.post(f"{base}/token", data={"grant_type": "refresh_token", "refresh_token": tokens["refresh_token"], "client_id": client["client_id"]})
    assert r.status_code == 200, r.text
    new = r.json()
    assert new["access_token"] != access
    assert _mcp(base, new["access_token"]) == 200
    assert _mcp(base, access) == 401
    r = httpx.post(f"{base}/token", data={"grant_type": "refresh_token", "refresh_token": tokens["refresh_token"], "client_id": client["client_id"]})
    assert r.status_code == 400 and r.json()["error"] == "invalid_grant"

    # Revocation. (The SDK's endpoint requires the client_secret field to be
    # present even for public clients; an empty value is accepted.)
    assert httpx.post(f"{base}/revoke", data={"token": new["access_token"], "client_id": client["client_id"], "client_secret": ""}).status_code == 200
    assert _mcp(base, new["access_token"]) == 401
    r = httpx.post(f"{base}/token", data={"grant_type": "refresh_token", "refresh_token": new["refresh_token"], "client_id": client["client_id"]})
    assert r.status_code == 400


def test_login_rejects_wrong_secret_and_expired_txn(oauth_server):
    base, _ = oauth_server
    client = _register(base)
    _, challenge = _pkce()
    r = httpx.get(f"{base}/authorize", params={
        "response_type": "code", "client_id": client["client_id"], "redirect_uri": client["redirect_uris"][0],
        "code_challenge": challenge, "code_challenge_method": "S256", "state": "s",
    }, follow_redirects=False)
    txn = parse_qs(urlparse(r.headers["location"]).query)["txn"][0]

    r = httpx.post(f"{base}/login", data={"txn": txn, "secret": "wrong"}, follow_redirects=False)
    assert r.status_code == 401 and "Wrong secret" in r.text

    r = httpx.post(f"{base}/login", data={"txn": "bogus", "secret": SECRET}, follow_redirects=False)
    assert r.status_code == 401 and "expired" in r.text

    page = httpx.get(f"{base}/login?txn=bogus")
    assert "expired" in page.text and "<form" not in page.text


def test_oauth_token_does_not_open_pairing_pages(oauth_server):
    base, _ = oauth_server
    client = _register(base)
    verifier, challenge = _pkce()
    tokens = _exchange(base, client, _login_for_code(base, client, challenge), verifier)
    # Pairing pages need the static secret specifically (owner-only, not any OAuth client).
    assert httpx.get(f"{base}/pair", headers={"Authorization": f"Bearer {tokens['access_token']}"}).status_code == 401
    assert httpx.get(f"{base}/pair?token={SECRET}").status_code == 200
    assert httpx.get(f"{base}/healthz").status_code == 200
    # The OAuth endpoints themselves are public, as the spec requires.
    assert httpx.get(f"{base}/.well-known/oauth-authorization-server").status_code == 200


def test_mcp_client_end_to_end_with_oauth_token(oauth_server):
    from mcp import ClientSession
    from mcp.client.streamable_http import streamable_http_client

    base, _ = oauth_server
    client = _register(base)
    verifier, challenge = _pkce()
    tokens = _exchange(base, client, _login_for_code(base, client, challenge), verifier)

    async def go():
        async with httpx.AsyncClient(headers={"Authorization": f"Bearer {tokens['access_token']}"}, timeout=10) as hc:
            async with streamable_http_client(f"{base}/mcp", http_client=hc) as (r, w):
                async with ClientSession(r, w) as s:
                    await s.initialize()
                    res = await s.call_tool("get_chat", {"chat_jid": ALICE})
                    return res

    res = asyncio.run(go())
    assert not res.is_error and res.structured_content["name"] == "Alice Liddell"


def test_rate_limiter_lockout():
    clock = [1000.0]
    lim = LoginRateLimiter(threshold=3, base_lock=30, max_lock=120, global_threshold=100)
    lim.now = lambda: clock[0]
    assert lim.retry_after("1.1.1.1") == 0
    lim.record_failure("1.1.1.1")
    lim.record_failure("1.1.1.1")
    assert lim.retry_after("1.1.1.1") == 0
    lim.record_failure("1.1.1.1")
    assert 0 < lim.retry_after("1.1.1.1") <= 30
    assert lim.retry_after("2.2.2.2") == 0          # other IPs unaffected
    clock[0] += 31
    assert lim.retry_after("1.1.1.1") == 0
    lim.record_failure("1.1.1.1")                    # doubles
    assert 30 < lim.retry_after("1.1.1.1") <= 60
    lim.record_success("1.1.1.1")
    assert lim.retry_after("1.1.1.1") == 0

    # Global circuit breaker.
    lim = LoginRateLimiter(threshold=100, global_threshold=5, global_window=60, global_lock=600)
    lim.now = lambda: clock[0]
    for i in range(5):
        lim.record_failure(f"10.0.0.{i}")
    assert lim.retry_after("9.9.9.9") > 0


def test_provider_lockout_and_rotation(tmp_path):
    from mcp.server.auth.provider import AuthorizationParams
    from mcp.shared.auth import OAuthClientInformationFull
    from pydantic import AnyUrl

    store = tmp_path / "oauth.json"
    lim = LoginRateLimiter(threshold=2, base_lock=60)
    prov = GraabOAuthProvider(SECRET, store, "https://graab.example", limiter=lim)
    client = OAuthClientInformationFull(client_id="c1", redirect_uris=[AnyUrl("https://claude.ai/api/mcp/auth_callback")])
    asyncio.run(prov.register_client(client))
    params = AuthorizationParams(state="st", scopes=["whatsapp"], code_challenge="ch", redirect_uri=AnyUrl("https://claude.ai/api/mcp/auth_callback"), redirect_uri_provided_explicitly=True)

    url = asyncio.run(prov.authorize(client, params))
    txn = parse_qs(urlparse(url).query)["txn"][0]
    assert url.startswith("https://graab.example/login?txn=")

    ok, msg, wait = prov.complete_login(txn, "wrong", "5.5.5.5")
    assert not ok and wait == 0
    ok, msg, wait = prov.complete_login(txn, "wrong again", "5.5.5.5")
    assert not ok and wait > 0                       # locked after the 2nd failure
    ok, msg, wait = prov.complete_login(txn, SECRET, "5.5.5.5")
    assert not ok and "Too many" in msg and wait > 0  # even the right secret is refused while locked
    ok, redirect, _ = prov.complete_login(txn, SECRET, "6.6.6.6")
    assert ok and redirect.startswith("https://claude.ai/api/mcp/auth_callback?")
    code = parse_qs(urlparse(redirect).query)["code"][0]

    ac = asyncio.run(prov.load_authorization_code(client, code))
    assert ac is not None and ac.code_challenge == "ch"
    tokens = asyncio.run(prov.exchange_authorization_code(client, ac))
    assert asyncio.run(prov.load_access_token(tokens.access_token)) is not None

    # A new process with the same secret keeps the session; a rotated secret drops it.
    same = GraabOAuthProvider(SECRET, store, "https://graab.example")
    assert asyncio.run(same.load_access_token(tokens.access_token)) is not None
    assert asyncio.run(same.get_client("c1")) is not None
    rotated = GraabOAuthProvider(SECRET + "-new", store, "https://graab.example")
    assert asyncio.run(rotated.load_access_token(tokens.access_token)) is None
    assert asyncio.run(rotated.load_refresh_token(client, tokens.refresh_token)) is None

    with pytest.raises(ValueError, match="at least 16"):
        GraabOAuthProvider("short", store, "https://graab.example")


def test_store_survives_corruption(tmp_path):
    p = tmp_path / "oauth.json"
    p.write_text("{not json")
    s = OAuthStore(p)
    assert s.data["clients"] == {}
    s.data["clients"]["x"] = {"client_id": "x"}
    s.save()
    assert OAuthStore(p).data["clients"]["x"]["client_id"] == "x"
