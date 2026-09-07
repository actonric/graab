"""A single-user OAuth 2.1 authorization server for the MCP endpoint.

Cloud MCP clients (claude.ai, cloud Claude Code) cannot attach a static
header; they expect the server to speak OAuth. The MCP SDK provides the
protocol endpoints (discovery, dynamic client registration, PKCE, token,
revocation) and calls into this provider for the pieces that are ours:

- The "login": a page that asks for the shared secret (GRAAB_MCP_TOKEN).
  There are no user accounts. Whoever knows the secret is the owner.
- Storage for registered clients and issued tokens, as a JSON file so a
  restart does not log the client out. Tokens are stored hashed.
- Brute-force protection on the login page.
- Rotation: every token records a fingerprint of the secret it was issued
  under, so changing GRAAB_MCP_TOKEN invalidates everything at once.

The static secret is also accepted directly as a bearer token, so Claude
Code's header-based configuration keeps working against the same endpoint.
"""

from __future__ import annotations

import hashlib
import hmac
import html
import json
import logging
import os
import secrets
import tempfile
import threading
import time
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any, Optional

from mcp.server.auth.provider import (
    AccessToken,
    AuthorizationCode,
    AuthorizationParams,
    AuthorizeError,
    RefreshToken,
    RegistrationError,
    TokenError,
    construct_redirect_uri,
)
from mcp.shared.auth import OAuthClientInformationFull, OAuthToken

log = logging.getLogger("graab.oauth")

SCOPE = "whatsapp"
ACCESS_TTL = 60 * 60           # 1 hour
REFRESH_TTL = 30 * 24 * 3600   # 30 days
CODE_TTL = 5 * 60              # 5 minutes
LOGIN_TXN_TTL = 10 * 60        # 10 minutes to complete the login page
STATIC_CLIENT_ID = "static-secret"


def _hash(value: str) -> str:
    return hashlib.sha256(value.encode()).hexdigest()


def secret_fingerprint(secret: str) -> str:
    """Short, non-reversible identifier of the current secret."""
    return hashlib.sha256(b"graab-secret:" + secret.encode()).hexdigest()[:16]


# --------------------------------------------------------------------------
# Persistence
# --------------------------------------------------------------------------


class OAuthStore:
    """JSON-file store for clients, codes and tokens. Writes are atomic and
    the file is owner-only. Everything secret is stored hashed."""

    def __init__(self, path: Path) -> None:
        self.path = Path(path)
        self._lock = threading.Lock()
        self.data: dict[str, dict[str, Any]] = {"clients": {}, "codes": {}, "access": {}, "refresh": {}}
        self._load()

    def _load(self) -> None:
        try:
            with open(self.path, "r", encoding="utf-8") as fh:
                loaded = json.load(fh)
            for key in self.data:
                if isinstance(loaded.get(key), dict):
                    self.data[key] = loaded[key]
        except FileNotFoundError:
            pass
        except (OSError, ValueError) as exc:
            log.warning("could not read %s (%s); starting with an empty token store", self.path, exc)

    def save(self) -> None:
        with self._lock:
            self.path.parent.mkdir(parents=True, exist_ok=True)
            fd, tmp = tempfile.mkstemp(prefix=".oauth-", suffix=".json", dir=str(self.path.parent))
            try:
                with os.fdopen(fd, "w", encoding="utf-8") as fh:
                    json.dump(self.data, fh, indent=1, sort_keys=True)
                os.chmod(tmp, 0o600)
                os.replace(tmp, self.path)
            except Exception:
                if os.path.exists(tmp):
                    os.unlink(tmp)
                raise

    def prune(self, now: Optional[float] = None) -> None:
        now = now or time.time()
        for bucket in ("codes", "access", "refresh"):
            self.data[bucket] = {k: v for k, v in self.data[bucket].items() if not v.get("expires_at") or v["expires_at"] > now}


# --------------------------------------------------------------------------
# Login brute-force protection
# --------------------------------------------------------------------------


@dataclass
class _Attempts:
    failures: int = 0
    locked_until: float = 0.0
    history: list[float] = field(default_factory=list)


class LoginRateLimiter:
    """Per-IP exponential lockout plus a global circuit breaker.

    After `threshold` failures an IP is locked for `base_lock` seconds,
    doubling each further failure up to `max_lock`. If more than
    `global_threshold` failures arrive from anywhere within `global_window`
    seconds, every login is refused for `global_lock` seconds.
    """

    def __init__(
        self,
        threshold: int = 5,
        base_lock: float = 30.0,
        max_lock: float = 3600.0,
        global_threshold: int = 50,
        global_window: float = 600.0,
        global_lock: float = 600.0,
    ) -> None:
        self.threshold = threshold
        self.base_lock = base_lock
        self.max_lock = max_lock
        self.global_threshold = global_threshold
        self.global_window = global_window
        self.global_lock = global_lock
        self._by_ip: dict[str, _Attempts] = {}
        self._global: list[float] = []
        self._global_locked_until = 0.0
        self._lock = threading.Lock()
        self.now = time.time

    def retry_after(self, ip: str) -> float:
        """Seconds until this IP may try again; 0 if allowed now."""
        with self._lock:
            now = self.now()
            wait = max(0.0, self._global_locked_until - now)
            a = self._by_ip.get(ip)
            if a:
                wait = max(wait, a.locked_until - now)
            return wait

    def record_failure(self, ip: str) -> None:
        with self._lock:
            now = self.now()
            a = self._by_ip.setdefault(ip, _Attempts())
            a.failures += 1
            if a.failures >= self.threshold:
                lock = min(self.max_lock, self.base_lock * (2 ** (a.failures - self.threshold)))
                a.locked_until = now + lock
            self._global = [t for t in self._global if t > now - self.global_window] + [now]
            if len(self._global) >= self.global_threshold:
                self._global_locked_until = now + self.global_lock
                log.error("login circuit breaker tripped: %d failures in %ds", len(self._global), self.global_window)

    def record_success(self, ip: str) -> None:
        with self._lock:
            self._by_ip.pop(ip, None)


# --------------------------------------------------------------------------
# Provider
# --------------------------------------------------------------------------


@dataclass
class _PendingLogin:
    client_id: str
    params: AuthorizationParams
    created_at: float


class GraabOAuthProvider:
    """OAuthAuthorizationServerProvider for a single owner identified by a secret."""

    def __init__(
        self,
        secret: str,
        store_path: Path,
        issuer_url: str,
        limiter: Optional[LoginRateLimiter] = None,
        access_ttl: int = ACCESS_TTL,
        refresh_ttl: int = REFRESH_TTL,
    ) -> None:
        if not secret or len(secret) < 16:
            raise ValueError("GRAAB_MCP_TOKEN must be at least 16 characters to be used as the OAuth login secret")
        self.secret = secret
        self.fingerprint = secret_fingerprint(secret)
        self.issuer_url = issuer_url.rstrip("/")
        self.store = OAuthStore(store_path)
        self.limiter = limiter or LoginRateLimiter()
        self.access_ttl = access_ttl
        self.refresh_ttl = refresh_ttl
        self._pending: dict[str, _PendingLogin] = {}
        self._lock = threading.Lock()
        self.store.prune()
        self.store.save()

    # ---- clients ---------------------------------------------------------

    async def get_client(self, client_id: str) -> OAuthClientInformationFull | None:
        raw = self.store.data["clients"].get(client_id)
        return OAuthClientInformationFull.model_validate(raw) if raw else None

    async def register_client(self, client_info: OAuthClientInformationFull) -> None:
        for uri in client_info.redirect_uris or []:
            u = str(uri)
            host = uri.host or ""
            if uri.scheme == "https" or (uri.scheme == "http" and host in ("localhost", "127.0.0.1", "::1")):
                continue
            raise RegistrationError("invalid_redirect_uri", f"redirect_uri must be https or loopback: {u}")
        if not client_info.redirect_uris:
            raise RegistrationError("invalid_client_metadata", "at least one redirect_uri is required")
        self.store.data["clients"][client_info.client_id] = json.loads(client_info.model_dump_json(exclude_none=True))
        self.store.save()
        log.info("registered OAuth client %s (%s)", client_info.client_id, client_info.client_name or "unnamed")

    # ---- authorization ---------------------------------------------------

    async def authorize(self, client: OAuthClientInformationFull, params: AuthorizationParams) -> str:
        txn = secrets.token_urlsafe(32)
        with self._lock:
            now = time.time()
            self._pending = {k: v for k, v in self._pending.items() if v.created_at > now - LOGIN_TXN_TTL}
            self._pending[txn] = _PendingLogin(client_id=client.client_id, params=params, created_at=now)
        return f"{self.issuer_url}/login?txn={txn}"

    def pending_login(self, txn: str) -> Optional[_PendingLogin]:
        with self._lock:
            p = self._pending.get(txn)
            if p and p.created_at > time.time() - LOGIN_TXN_TTL:
                return p
            self._pending.pop(txn, None)
            return None

    def complete_login(self, txn: str, presented_secret: str, client_ip: str) -> tuple[bool, str, float]:
        """Verify the secret for a pending login.

        Returns (ok, redirect_url_or_message, retry_after_seconds).
        """
        wait = self.limiter.retry_after(client_ip)
        if wait > 0:
            return False, "Too many failed attempts. Try again later.", wait
        pending = self.pending_login(txn)
        if pending is None:
            return False, "This login link has expired. Start the connection again from your client.", 0.0
        if not hmac.compare_digest(presented_secret.strip(), self.secret):
            self.limiter.record_failure(client_ip)
            log.warning("failed OAuth login from %s", client_ip)
            return False, "Wrong secret.", self.limiter.retry_after(client_ip)
        self.limiter.record_success(client_ip)
        with self._lock:
            self._pending.pop(txn, None)

        params = pending.params
        code = secrets.token_urlsafe(32)
        record = AuthorizationCode(
            code=code,
            scopes=params.scopes or [SCOPE],
            expires_at=time.time() + CODE_TTL,
            client_id=pending.client_id,
            code_challenge=params.code_challenge,
            redirect_uri=params.redirect_uri,
            redirect_uri_provided_explicitly=params.redirect_uri_provided_explicitly,
            resource=params.resource,
            subject="owner",
        )
        self.store.data["codes"][_hash(code)] = json.loads(record.model_dump_json())
        self.store.prune()
        self.store.save()
        log.info("OAuth login succeeded for client %s from %s", pending.client_id, client_ip)
        return True, construct_redirect_uri(str(params.redirect_uri), code=code, state=params.state), 0.0

    async def load_authorization_code(self, client: OAuthClientInformationFull, authorization_code: str) -> AuthorizationCode | None:
        raw = self.store.data["codes"].get(_hash(authorization_code))
        if not raw:
            return None
        code = AuthorizationCode.model_validate(raw)
        if code.client_id != client.client_id or code.expires_at < time.time():
            return None
        return code

    async def exchange_authorization_code(self, client: OAuthClientInformationFull, authorization_code: AuthorizationCode) -> OAuthToken:
        self.store.data["codes"].pop(_hash(authorization_code.code), None)
        return self._issue(client.client_id, authorization_code.scopes, authorization_code.resource)

    # ---- tokens ----------------------------------------------------------

    def _issue(self, client_id: str, scopes: list[str], resource: Optional[str]) -> OAuthToken:
        now = int(time.time())
        access = secrets.token_urlsafe(32)
        refresh = secrets.token_urlsafe(32)
        self.store.data["access"][_hash(access)] = {
            "client_id": client_id, "scopes": scopes, "expires_at": now + self.access_ttl,
            "resource": resource, "subject": "owner", "fingerprint": self.fingerprint, "refresh": _hash(refresh),
        }
        self.store.data["refresh"][_hash(refresh)] = {
            "client_id": client_id, "scopes": scopes, "expires_at": now + self.refresh_ttl,
            "subject": "owner", "fingerprint": self.fingerprint, "access": _hash(access),
        }
        self.store.prune()
        self.store.save()
        return OAuthToken(access_token=access, token_type="Bearer", expires_in=self.access_ttl, scope=" ".join(scopes), refresh_token=refresh)

    async def load_refresh_token(self, client: OAuthClientInformationFull, refresh_token: str) -> RefreshToken | None:
        raw = self.store.data["refresh"].get(_hash(refresh_token))
        if not raw or raw.get("fingerprint") != self.fingerprint or raw["client_id"] != client.client_id:
            return None
        if raw.get("expires_at") and raw["expires_at"] < time.time():
            return None
        return RefreshToken(token=refresh_token, client_id=raw["client_id"], scopes=raw["scopes"], expires_at=raw.get("expires_at"), subject=raw.get("subject"))

    async def exchange_refresh_token(self, client: OAuthClientInformationFull, refresh_token: RefreshToken, scopes: list[str]) -> OAuthToken:
        old = self.store.data["refresh"].pop(_hash(refresh_token.token), None)
        if old is None:
            raise TokenError("invalid_grant", "refresh token is no longer valid")
        self.store.data["access"].pop(old.get("access", ""), None)
        return self._issue(client.client_id, scopes or refresh_token.scopes, None)

    async def load_access_token(self, token: str) -> AccessToken | None:
        # The static secret itself is a valid bearer token, for header-based clients.
        if hmac.compare_digest(token, self.secret):
            return AccessToken(token=token, client_id=STATIC_CLIENT_ID, scopes=[SCOPE], subject="owner")
        raw = self.store.data["access"].get(_hash(token))
        if not raw or raw.get("fingerprint") != self.fingerprint:
            return None
        if raw.get("expires_at") and raw["expires_at"] < time.time():
            return None
        return AccessToken(token=token, client_id=raw["client_id"], scopes=raw["scopes"], expires_at=raw.get("expires_at"), resource=raw.get("resource"), subject=raw.get("subject"))

    async def revoke_token(self, token: AccessToken | RefreshToken) -> None:
        h = _hash(token.token)
        access = self.store.data["access"].pop(h, None)
        refresh = self.store.data["refresh"].pop(h, None)
        if access and access.get("refresh"):
            self.store.data["refresh"].pop(access["refresh"], None)
        if refresh and refresh.get("access"):
            self.store.data["access"].pop(refresh["access"], None)
        self.store.save()

    async def exchange_identity_assertion(self, client: OAuthClientInformationFull, params: Any) -> OAuthToken:
        raise TokenError("unsupported_grant_type", "identity assertions are not supported")

    # ---- login page ------------------------------------------------------

    def login_page(self, txn: str, error: str = "", retry_after: float = 0.0) -> str:
        pending = self.pending_login(txn)
        if pending is None:
            body = "<p class=err>This login link has expired. Start the connection again from your client.</p>"
        else:
            client = self.store.data["clients"].get(pending.client_id, {})
            who = html.escape(client.get("client_name") or pending.client_id)
            msg = f'<p class=err>{html.escape(error)}</p>' if error else ""
            if retry_after > 0:
                msg += f"<p class=err>Try again in {int(retry_after) + 1} seconds.</p>"
            body = f"""
<p><b>{who}</b> wants to connect to your WhatsApp archive through Graab.</p>
<form method="post" action="/login">
  <input type="hidden" name="txn" value="{html.escape(txn, quote=True)}">
  <label>Enter the server secret (<code>GRAAB_MCP_TOKEN</code>)<br>
  <input type="password" name="secret" autocomplete="current-password" autofocus required></label>
  <button type="submit">Allow access</button>
</form>
{msg}
<p class=small>Only approve this if you started the connection yourself.</p>"""
        return _PAGE.format(body=body)


_PAGE = """<!doctype html><meta charset="utf-8"><title>Graab · sign in</title>
<meta name="viewport" content="width=device-width,initial-scale=1">
<style>body{{font:16px/1.5 system-ui,sans-serif;max-width:26rem;margin:4rem auto;padding:0 1rem}}
input[type=password]{{width:100%;padding:.6rem;font-size:1rem;margin:.4rem 0 .8rem;box-sizing:border-box}}
button{{padding:.6rem 1.2rem;font-size:1rem}}.err{{color:#b00020}}.small{{color:#666;font-size:.9rem}}
code{{font-size:.9em}}</style>
<h1>Graab</h1>{body}"""
