"""The MCP server: tools Claude can call to read and send WhatsApp messages."""

from __future__ import annotations

import functools
import os
from typing import Any, Callable, Optional, TypeVar

from mcp.server.mcpserver import MCPServer
from mcp.server.mcpserver.exceptions import ToolError
from mcp.types import ToolAnnotations

from . import audio, bridge, db

F = TypeVar("F", bound=Callable[..., Any])


def guarded(fn: F) -> F:
    """Turn expected failures into ToolError so the model sees the reason.

    Any other exception is wrapped by the SDK as an opaque "Error executing
    tool", which hides e.g. "database not found, start the bridge".
    """

    @functools.wraps(fn)
    def wrapper(*args: Any, **kwargs: Any) -> Any:
        try:
            return fn(*args, **kwargs)
        except (db.DatabaseUnavailable, ValueError) as exc:
            raise ToolError(str(exc)) from exc

    return wrapper  # type: ignore[return-value]

INSTRUCTIONS = """\
Tools for the user's personal WhatsApp account. Messages are mirrored into a
local database by a bridge process; reads never touch the network. Phone
numbers are digits only in international format (no +). Direct chats have JIDs
like 14155551234@s.whatsapp.net; groups end in @g.us. Messages with media show
a media_type; call download_media with the message id and chat JID to fetch
the file. Sending tools act on the real account, so confirm intent first.
"""

server = MCPServer(
    "whatsapp",
    instructions=INSTRUCTIONS,
    version=__import__("graab_mcp").__version__,
)

READ_ONLY = ToolAnnotations(read_only_hint=True, destructive_hint=False, idempotent_hint=True, open_world_hint=False)
SENDS = ToolAnnotations(read_only_hint=False, destructive_hint=False, idempotent_hint=False, open_world_hint=True)


def _fail(message: str) -> dict[str, Any]:
    return {"success": False, "message": message}


# --------------------------------------------------------------------------
# Reading
# --------------------------------------------------------------------------


@server.tool(annotations=READ_ONLY)
@guarded
def search_contacts(query: str) -> list[dict[str, Any]]:
    """Search WhatsApp contacts by name or phone number.

    Args:
        query: Text to match against contact names, push names or phone digits.
    """
    return [c.to_dict() for c in db.search_contacts(query)]


@server.tool(annotations=READ_ONLY)
@guarded
def list_messages(
    after: Optional[str] = None,
    before: Optional[str] = None,
    sender_phone_number: Optional[str] = None,
    chat_jid: Optional[str] = None,
    query: Optional[str] = None,
    limit: int = 20,
    page: int = 0,
    include_context: bool = True,
    context_before: int = 1,
    context_after: int = 1,
) -> list[dict[str, Any]]:
    """Get WhatsApp messages matching the filters, newest first, with optional context.

    Args:
        after: Only messages after this ISO-8601 datetime (e.g. 2025-04-01T00:00:00Z).
        before: Only messages before this ISO-8601 datetime.
        sender_phone_number: Only messages sent by this phone number (digits only).
        chat_jid: Only messages in this chat.
        query: Case-insensitive substring to match in message text.
        limit: Maximum matches to return (default 20, max 200).
        page: Page number for pagination (default 0).
        include_context: Attach surrounding messages from the same chat to each match.
        context_before: Messages before each match to include (default 1).
        context_after: Messages after each match to include (default 1).
    """
    messages = db.list_messages(
        after=after, before=before, sender_phone_number=sender_phone_number,
        chat_jid=chat_jid, query=query, limit=limit, page=page,
    )
    out: list[dict[str, Any]] = []
    for m in messages:
        d = m.to_dict()
        if include_context and (context_before > 0 or context_after > 0):
            ctx = db.get_message_context(m.id, context_before, context_after, chat_jid=m.chat_jid)
            d["context_before"] = [x.to_dict() for x in ctx.before]
            d["context_after"] = [x.to_dict() for x in ctx.after]
        out.append(d)
    return out


@server.tool(annotations=READ_ONLY)
@guarded
def list_chats(
    query: Optional[str] = None,
    limit: int = 20,
    page: int = 0,
    include_last_message: bool = True,
    sort_by: str = "last_active",
) -> list[dict[str, Any]]:
    """List WhatsApp chats (direct and group), most recently active first.

    Args:
        query: Filter by chat name or JID substring.
        limit: Maximum chats to return (default 20, max 200).
        page: Page number for pagination (default 0).
        include_last_message: Include the last message text and sender.
        sort_by: "last_active" (default) or "name".
    """
    return [c.to_dict() for c in db.list_chats(query, limit, page, include_last_message, sort_by)]


@server.tool(annotations=READ_ONLY)
@guarded
def get_chat(chat_jid: str, include_last_message: bool = True) -> dict[str, Any]:
    """Get metadata for one chat by JID.

    Args:
        chat_jid: The chat's JID (…@s.whatsapp.net for people, …@g.us for groups).
        include_last_message: Include the last message text and sender.
    """
    chat = db.get_chat(chat_jid, include_last_message)
    return chat.to_dict() if chat else _fail(f"No chat with JID {chat_jid}")


@server.tool(annotations=READ_ONLY)
@guarded
def get_direct_chat_by_contact(sender_phone_number: str) -> dict[str, Any]:
    """Find the direct (one-to-one) chat with a phone number.

    Args:
        sender_phone_number: Phone number in international format, digits only.
    """
    chat = db.get_direct_chat_by_contact(sender_phone_number)
    return chat.to_dict() if chat else _fail(f"No direct chat with {sender_phone_number}")


@server.tool(annotations=READ_ONLY)
@guarded
def get_contact_chats(jid: str, limit: int = 20, page: int = 0) -> list[dict[str, Any]]:
    """List every chat (direct and group) a contact has participated in.

    Args:
        jid: The contact's JID, e.g. 14155551234@s.whatsapp.net.
        limit: Maximum chats to return (default 20).
        page: Page number for pagination (default 0).
    """
    return [c.to_dict() for c in db.get_contact_chats(jid, limit, page)]


@server.tool(annotations=READ_ONLY)
@guarded
def get_last_interaction(jid: str) -> dict[str, Any]:
    """Get the most recent message involving a contact.

    Args:
        jid: The contact's JID, e.g. 14155551234@s.whatsapp.net.
    """
    m = db.get_last_interaction(jid)
    return m.to_dict() if m else _fail(f"No messages involving {jid}")


@server.tool(annotations=READ_ONLY)
@guarded
def get_message_context(message_id: str, before: int = 5, after: int = 5, chat_jid: Optional[str] = None) -> dict[str, Any]:
    """Get the messages surrounding a specific message in its chat.

    Args:
        message_id: The message's id.
        before: Number of earlier messages to include (default 5).
        after: Number of later messages to include (default 5).
        chat_jid: Optional chat JID to disambiguate ids reused across chats.
    """
    try:
        return db.get_message_context(message_id, before, after, chat_jid).to_dict()
    except ValueError as exc:
        return _fail(str(exc))


@server.tool(annotations=READ_ONLY)
def bridge_status() -> dict[str, Any]:
    """Check whether the WhatsApp bridge is running and how much history is stored."""
    status = bridge.client().status()
    status["db_path"] = str(db.db_path())
    status["db_exists"] = db.db_path().exists()
    status["ffmpeg_available"] = audio.ffmpeg_available()
    return status


# --------------------------------------------------------------------------
# Sending
# --------------------------------------------------------------------------


@server.tool(annotations=SENDS)
def send_message(recipient: str, message: str) -> dict[str, Any]:
    """Send a WhatsApp text message to a person or group.

    Args:
        recipient: Phone number in international format without + (e.g. 14155551234),
            or a JID such as 14155551234@s.whatsapp.net or 1203630000@g.us for groups.
        message: The text to send.
    """
    if not recipient or not recipient.strip():
        return _fail("recipient is required")
    if not message:
        return _fail("message is required")
    return bridge.client().send(recipient.strip(), message)


@server.tool(annotations=SENDS)
def send_file(recipient: str, media_path: str, caption: str = "") -> dict[str, Any]:
    """Send an image, video, document or raw audio file via WhatsApp.

    Args:
        recipient: Phone number (digits only) or JID; use the JID for groups.
        media_path: Absolute path to the file on this machine.
        caption: Optional caption (images, videos and documents).
    """
    if not recipient or not recipient.strip():
        return _fail("recipient is required")
    if not media_path or not os.path.isfile(media_path):
        return _fail(f"Media file not found: {media_path}")
    return bridge.client().send(recipient.strip(), caption or "", media_path=os.path.abspath(media_path))


@server.tool(annotations=SENDS)
def send_audio_message(recipient: str, media_path: str) -> dict[str, Any]:
    """Send an audio file as a playable WhatsApp voice note.

    The file must be Ogg Opus. Other formats are converted with ffmpeg when it
    is installed; if conversion fails, use send_file to send the raw audio.

    Args:
        recipient: Phone number (digits only) or JID; use the JID for groups.
        media_path: Absolute path to the audio file.
    """
    if not recipient or not recipient.strip():
        return _fail("recipient is required")
    if not media_path or not os.path.isfile(media_path):
        return _fail(f"Media file not found: {media_path}")
    path = os.path.abspath(media_path)
    temp: Optional[str] = None
    if not audio.is_ogg_opus(path):
        try:
            temp = audio.convert_to_opus_ogg_temp(path)
            path = temp
        except Exception as exc:  # noqa: BLE001 - surface any conversion failure to the model
            return _fail(f"Could not convert to Ogg Opus ({exc}). Send it with send_file instead.")
    try:
        return bridge.client().send(recipient.strip(), "", media_path=path)
    finally:
        if temp and os.path.exists(temp):
            os.unlink(temp)


@server.tool(annotations=ToolAnnotations(read_only_hint=False, destructive_hint=False, idempotent_hint=True, open_world_hint=True))
def download_media(message_id: str, chat_jid: str) -> dict[str, Any]:
    """Download the media attached to a message and return its local file path.

    Args:
        message_id: The id of the message with media (see media_type in list_messages).
        chat_jid: The JID of the chat containing the message.
    """
    result = bridge.client().download(message_id, chat_jid)
    if result.get("success") and result.get("path"):
        result["file_path"] = result["path"]
    return result


def main() -> None:
    server.run(transport="stdio")


if __name__ == "__main__":
    main()
