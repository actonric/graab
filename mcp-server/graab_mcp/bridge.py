"""HTTP client for the Go bridge's local REST API."""

from __future__ import annotations

import os
from typing import Any, Optional

import httpx

DEFAULT_BRIDGE_URL = "http://127.0.0.1:8080"


def bridge_url() -> str:
    return os.environ.get("GRAAB_BRIDGE_URL", DEFAULT_BRIDGE_URL).rstrip("/")


class BridgeClient:
    """Thin wrapper so tests can inject a transport."""

    def __init__(self, base_url: Optional[str] = None, transport: Optional[httpx.BaseTransport] = None, timeout: float = 120.0):
        self.base_url = (base_url or bridge_url()).rstrip("/")
        self._client = httpx.Client(base_url=self.base_url, transport=transport, timeout=timeout)

    def _post(self, path: str, payload: dict[str, Any]) -> dict[str, Any]:
        try:
            resp = self._client.post(path, json=payload)
        except httpx.ConnectError as exc:
            return {
                "success": False,
                "message": f"Could not reach the WhatsApp bridge at {self.base_url} ({exc}). "
                "Is it running? Start it with: cd bridge && go run .",
            }
        except httpx.HTTPError as exc:
            return {"success": False, "message": f"Bridge request failed: {exc}"}
        try:
            data = resp.json()
        except ValueError:
            return {"success": False, "message": f"Bridge returned HTTP {resp.status_code}: {resp.text[:300]}"}
        if not isinstance(data, dict):
            return {"success": False, "message": f"Unexpected bridge response: {data!r}"}
        data.setdefault("success", resp.is_success)
        data.setdefault("message", f"HTTP {resp.status_code}")
        return data

    def send(self, recipient: str, message: str = "", media_path: Optional[str] = None) -> dict[str, Any]:
        payload: dict[str, Any] = {"recipient": recipient, "message": message or ""}
        if media_path:
            payload["media_path"] = media_path
        return self._post("/api/send", payload)

    def download(self, message_id: str, chat_jid: str) -> dict[str, Any]:
        return self._post("/api/download", {"message_id": message_id, "chat_jid": chat_jid})

    def status(self) -> dict[str, Any]:
        try:
            resp = self._client.get("/api/status", timeout=5.0)
            resp.raise_for_status()
            data = resp.json()
            data["bridge_url"] = self.base_url
            data["reachable"] = True
            return data
        except httpx.HTTPError as exc:
            return {"reachable": False, "bridge_url": self.base_url, "error": str(exc)}


_default_client: Optional[BridgeClient] = None


def client() -> BridgeClient:
    global _default_client
    if _default_client is None or _default_client.base_url != bridge_url():
        _default_client = BridgeClient()
    return _default_client
