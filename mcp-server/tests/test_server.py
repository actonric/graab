import asyncio
import json

import httpx
import pytest
from mcp.server.mcpserver.exceptions import ToolError

from graab_mcp import bridge, server
from tests.conftest import ALICE, GROUP

EXPECTED_TOOLS = {
    "search_contacts", "list_messages", "list_chats", "get_chat", "get_direct_chat_by_contact",
    "get_contact_chats", "get_last_interaction", "get_message_context", "bridge_status",
    "send_message", "send_file", "send_audio_message", "download_media",
}


def run(coro):
    return asyncio.run(coro)


def structured(coro):
    """Run a call_tool coroutine and return its structured content."""
    result = asyncio.run(coro)
    assert not result.is_error, result.content
    return result.structured_content


def test_tools_are_registered():
    tools = run(server.server.list_tools())
    names = {t.name for t in tools}
    assert names == EXPECTED_TOOLS
    by_name = {t.name: t for t in tools}
    assert by_name["list_messages"].annotations.read_only_hint is True
    assert by_name["send_message"].annotations.read_only_hint is False
    assert "chat_jid" in by_name["download_media"].input_schema["properties"]


def test_list_messages_tool_with_context(fixture_db):
    rows = structured(server.server.call_tool("list_messages", {"chat_jid": ALICE, "query": "noon", "context_after": 2}))["result"]
    assert len(rows) == 1
    row = rows[0]
    assert row["id"] == "A2" and row["sender_name"] == "Me"
    assert [m["id"] for m in row["context_before"]] == ["A1"]
    assert [m["id"] for m in row["context_after"]] == ["A3", "A4"]

    rows = structured(server.server.call_tool("list_messages", {"chat_jid": ALICE, "include_context": False, "limit": 1}))["result"]
    assert rows[0]["id"] == "A4" and "context_before" not in rows[0]


def test_read_tools(fixture_db):
    chats = structured(server.server.call_tool("list_chats", {}))["result"]
    assert chats[0]["jid"] == GROUP

    chat = structured(server.server.call_tool("get_chat", {"chat_jid": ALICE}))
    assert chat["name"] == "Alice Liddell"

    missing = structured(server.server.call_tool("get_chat", {"chat_jid": "x@s.whatsapp.net"}))
    assert missing["success"] is False

    ctx = structured(server.server.call_tool("get_message_context", {"message_id": "A3", "before": 1, "after": 1}))
    assert ctx["message"]["media_type"] == "image"
    assert [m["id"] for m in ctx["before"]] == ["A2"]

    bad = structured(server.server.call_tool("get_message_context", {"message_id": "nope"}))
    assert bad["success"] is False

    last = structured(server.server.call_tool("get_last_interaction", {"jid": ALICE}))
    assert last["id"] == "G1"

    contacts = structured(server.server.call_tool("search_contacts", {"query": "alice"}))["result"]
    assert contacts[0]["jid"] == ALICE


def test_send_tools_use_bridge(fixture_db, tmp_path, monkeypatch):
    calls = []

    def handler(request: httpx.Request) -> httpx.Response:
        calls.append((request.url.path, json.loads(request.content)))
        return httpx.Response(200, json={"success": True, "message": "sent", "message_id": "M9", "path": "/x/y.jpg", "media_type": "image"})

    fake = bridge.BridgeClient(base_url="http://bridge.test", transport=httpx.MockTransport(handler))
    monkeypatch.setattr(bridge, "client", lambda: fake)

    res = structured(server.server.call_tool("send_message", {"recipient": " 123 ", "message": "hi"}))
    assert res["success"] is True and calls[-1] == ("/api/send", {"recipient": "123", "message": "hi"})

    res = structured(server.server.call_tool("send_message", {"recipient": "", "message": "hi"}))
    assert res["success"] is False and not any(c[1].get("recipient") == "" for c in calls)

    pic = tmp_path / "pic.jpg"
    pic.write_bytes(b"\xff\xd8\xff")
    res = structured(server.server.call_tool("send_file", {"recipient": "123", "media_path": str(pic), "caption": "look"}))
    assert res["success"] is True
    assert calls[-1] == ("/api/send", {"recipient": "123", "message": "look", "media_path": str(pic)})

    res = structured(server.server.call_tool("send_file", {"recipient": "123", "media_path": str(tmp_path / "missing.jpg")}))
    assert res["success"] is False and "not found" in res["message"]

    # An existing Ogg Opus file is sent as-is without conversion.
    voice = tmp_path / "note.ogg"
    voice.write_bytes(b"OggS" + b"\x00" * 24 + b"OpusHead" + b"\x00" * 11)
    res = structured(server.server.call_tool("send_audio_message", {"recipient": "123", "media_path": str(voice)}))
    assert res["success"] is True and calls[-1][1]["media_path"] == str(voice)

    # A non-Opus file with no ffmpeg fails gracefully.
    monkeypatch.setattr(server.audio, "ffmpeg_available", lambda: False)
    mp3 = tmp_path / "song.mp3"
    mp3.write_bytes(b"ID3")
    res = structured(server.server.call_tool("send_audio_message", {"recipient": "123", "media_path": str(mp3)}))
    assert res["success"] is False and "send_file" in res["message"]

    res = structured(server.server.call_tool("download_media", {"message_id": "A3", "chat_jid": ALICE}))
    assert res["success"] is True and res["file_path"] == "/x/y.jpg"
    assert calls[-1] == ("/api/download", {"message_id": "A3", "chat_jid": ALICE})


def test_missing_database_is_reported_as_tool_error(tmp_path, monkeypatch):
    # In-process call_tool re-raises ToolError; over the wire the SDK turns it
    # into an is_error result carrying the same message.
    monkeypatch.setenv("GRAAB_DB_PATH", str(tmp_path / "missing.db"))
    with pytest.raises(ToolError, match="Start the bridge"):
        run(server.server.call_tool("list_chats", {}))


def test_bad_date_is_reported_as_tool_error(fixture_db):
    with pytest.raises(ToolError, match="ISO-8601"):
        run(server.server.call_tool("list_messages", {"after": "yesterday"}))
