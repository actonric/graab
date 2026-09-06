import json

import httpx

from graab_mcp.bridge import BridgeClient


def make_client(handler):
    return BridgeClient(base_url="http://bridge.test", transport=httpx.MockTransport(handler))


def test_send_success():
    seen = {}

    def handler(request: httpx.Request) -> httpx.Response:
        seen["path"] = request.url.path
        seen["body"] = json.loads(request.content)
        return httpx.Response(200, json={"success": True, "message": "Message sent to 123@s.whatsapp.net", "message_id": "M1"})

    res = make_client(handler).send("123", "hello")
    assert res["success"] is True and res["message_id"] == "M1"
    assert seen["path"] == "/api/send"
    assert seen["body"] == {"recipient": "123", "message": "hello"}


def test_send_with_media_and_error_status():
    def handler(request: httpx.Request) -> httpx.Response:
        body = json.loads(request.content)
        assert body["media_path"] == "/tmp/pic.jpg"
        return httpx.Response(400, json={"success": False, "message": "invalid phone number"})

    res = make_client(handler).send("abc", "", media_path="/tmp/pic.jpg")
    assert res == {"success": False, "message": "invalid phone number"}


def test_non_json_response():
    def handler(request: httpx.Request) -> httpx.Response:
        return httpx.Response(502, text="<html>bad gateway</html>")

    res = make_client(handler).download("M", "C")
    assert res["success"] is False and "HTTP 502" in res["message"]


def test_connection_refused_is_explained():
    def handler(request: httpx.Request) -> httpx.Response:
        raise httpx.ConnectError("connection refused", request=request)

    res = make_client(handler).send("123", "hi")
    assert res["success"] is False
    assert "Is it running?" in res["message"]


def test_status():
    def handler(request: httpx.Request) -> httpx.Response:
        assert request.url.path == "/api/status"
        return httpx.Response(200, json={"connected": True, "logged_in": True, "chats": 3, "messages": 40})

    res = make_client(handler).status()
    assert res["reachable"] is True and res["connected"] is True and res["messages"] == 40

    def down(request: httpx.Request) -> httpx.Response:
        raise httpx.ConnectError("refused", request=request)

    res = make_client(down).status()
    assert res["reachable"] is False and "refused" in res["error"]
