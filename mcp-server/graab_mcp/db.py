"""Read-only access to the SQLite database written by the Go bridge.

The bridge owns the schema (see ../../bridge/store.go). Everything here is a
query; nothing is ever written from the MCP side.
"""

from __future__ import annotations

import os
import sqlite3
from dataclasses import asdict, dataclass, field
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Iterator, Optional

_HERE = Path(__file__).resolve().parent
DEFAULT_DB_PATH = _HERE.parent.parent / "bridge" / "store" / "messages.db"


def db_path() -> Path:
    """Location of messages.db (override with GRAAB_DB_PATH)."""
    return Path(os.environ.get("GRAAB_DB_PATH", str(DEFAULT_DB_PATH))).expanduser()


class DatabaseUnavailable(RuntimeError):
    """Raised when messages.db does not exist yet (bridge never ran)."""


def connect() -> sqlite3.Connection:
    path = db_path()
    if not path.exists():
        raise DatabaseUnavailable(
            f"WhatsApp database not found at {path}. Start the bridge first "
            "(cd bridge && go run .) and pair it with your phone."
        )
    conn = sqlite3.connect(f"file:{path}?mode=ro", uri=True, timeout=5)
    conn.row_factory = sqlite3.Row
    return conn


# --------------------------------------------------------------------------
# Models
# --------------------------------------------------------------------------


def _parse_ts(value: Optional[str]) -> Optional[datetime]:
    if not value:
        return None
    try:
        dt = datetime.fromisoformat(value.replace("Z", "+00:00"))
    except ValueError:
        return None
    if dt.tzinfo is None:
        dt = dt.replace(tzinfo=timezone.utc)
    return dt


def _iso(dt: Optional[datetime]) -> Optional[str]:
    return dt.isoformat() if dt else None


@dataclass
class Message:
    id: str
    chat_jid: str
    chat_name: str
    sender: str
    sender_name: str
    timestamp: Optional[datetime]
    is_from_me: bool
    content: str
    media_type: str = ""
    filename: str = ""
    quoted_id: str = ""

    def to_dict(self) -> dict[str, Any]:
        d = asdict(self)
        d["timestamp"] = _iso(self.timestamp)
        if not self.media_type:
            d.pop("media_type")
            d.pop("filename")
        if not self.quoted_id:
            d.pop("quoted_id")
        return d


@dataclass
class Chat:
    jid: str
    name: str
    is_group: bool
    last_message_time: Optional[datetime]
    last_message: Optional[str] = None
    last_sender: Optional[str] = None
    last_is_from_me: Optional[bool] = None

    def to_dict(self) -> dict[str, Any]:
        d = asdict(self)
        d["last_message_time"] = _iso(self.last_message_time)
        return d


@dataclass
class Contact:
    jid: str
    phone_number: str
    name: str
    push_name: str = ""

    def to_dict(self) -> dict[str, Any]:
        return asdict(self)


@dataclass
class MessageContext:
    message: Message
    before: list[Message] = field(default_factory=list)
    after: list[Message] = field(default_factory=list)

    def to_dict(self) -> dict[str, Any]:
        return {
            "message": self.message.to_dict(),
            "before": [m.to_dict() for m in self.before],
            "after": [m.to_dict() for m in self.after],
        }


# --------------------------------------------------------------------------
# Helpers
# --------------------------------------------------------------------------

_MESSAGE_SELECT = """
    SELECT m.id, m.chat_jid, COALESCE(c.name, '') AS chat_name, m.sender, m.content,
           m.timestamp, m.is_from_me, m.media_type, m.filename, m.quoted_id,
           COALESCE(NULLIF(ct.name, ''), NULLIF(ct.push_name, ''), '') AS sender_name
    FROM messages m
    LEFT JOIN chats c ON c.jid = m.chat_jid
    LEFT JOIN contacts ct ON ct.jid = m.sender || '@s.whatsapp.net'
"""


def _row_to_message(row: sqlite3.Row) -> Message:
    sender_name = row["sender_name"] or ""
    if row["is_from_me"]:
        sender_name = "Me"
    elif not sender_name and not row["chat_jid"].endswith("@g.us"):
        # Direct chat: the chat name is the other party's name.
        sender_name = row["chat_name"] or ""
    return Message(
        id=row["id"],
        chat_jid=row["chat_jid"],
        chat_name=row["chat_name"] or row["chat_jid"].split("@")[0],
        sender=row["sender"],
        sender_name=sender_name or row["sender"],
        timestamp=_parse_ts(row["timestamp"]),
        is_from_me=bool(row["is_from_me"]),
        content=row["content"] or "",
        media_type=row["media_type"] or "",
        filename=row["filename"] or "",
        quoted_id=row["quoted_id"] or "",
    )


def _row_to_chat(row: sqlite3.Row) -> Chat:
    keys = row.keys()
    return Chat(
        jid=row["jid"],
        name=row["name"] or row["jid"].split("@")[0],
        is_group=bool(row["is_group"]) if "is_group" in keys else row["jid"].endswith("@g.us"),
        last_message_time=_parse_ts(row["last_message_time"]),
        last_message=row["last_message"] if "last_message" in keys else None,
        last_sender=row["last_sender"] if "last_sender" in keys else None,
        last_is_from_me=(bool(row["last_is_from_me"]) if "last_is_from_me" in keys and row["last_is_from_me"] is not None else None),
    )


def _to_utc_iso(value: str, name: str) -> str:
    """Normalise a user-supplied ISO-8601 date to the DB's UTC text format."""
    try:
        dt = datetime.fromisoformat(value.replace("Z", "+00:00"))
    except ValueError as exc:
        raise ValueError(f"Invalid ISO-8601 date for '{name}': {value!r}") from exc
    if dt.tzinfo is None:
        dt = dt.replace(tzinfo=timezone.utc)
    return dt.astimezone(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def _digits(value: str) -> str:
    return "".join(ch for ch in value if ch.isdigit())


def _clamp(limit: int, page: int) -> tuple[int, int]:
    limit = max(1, min(int(limit), 200))
    page = max(0, int(page))
    return limit, page * limit


# --------------------------------------------------------------------------
# Queries
# --------------------------------------------------------------------------


def search_contacts(query: str, limit: int = 50) -> list[Contact]:
    """Match contacts and direct chats by name, push name or phone number."""
    q = (query or "").strip()
    like = f"%{q}%"
    digits = _digits(q)
    with connect() as conn:
        rows = conn.execute(
            """
            SELECT jid, phone, name, push_name FROM (
                SELECT ct.jid AS jid, ct.phone AS phone, ct.name AS name, ct.push_name AS push_name
                FROM contacts ct
                UNION
                SELECT c.jid, substr(c.jid, 1, instr(c.jid, '@') - 1), c.name, ''
                FROM chats c
                WHERE c.is_group = 0 AND c.jid NOT IN (SELECT jid FROM contacts)
            )
            WHERE jid LIKE '%@s.whatsapp.net'
              AND (LOWER(name) LIKE LOWER(?) OR LOWER(push_name) LIKE LOWER(?)
                   OR (? != '' AND phone LIKE ?) OR (? = '' AND ? = ''))
            ORDER BY CASE WHEN name != '' THEN 0 ELSE 1 END, LOWER(name), LOWER(push_name), phone
            LIMIT ?
            """,
            (like, like, digits, f"%{digits}%", q, digits, max(1, min(int(limit), 200))),
        ).fetchall()
    return [
        Contact(jid=r["jid"], phone_number=r["phone"] or r["jid"].split("@")[0], name=r["name"] or "", push_name=r["push_name"] or "")
        for r in rows
    ]


def list_messages(
    after: Optional[str] = None,
    before: Optional[str] = None,
    sender_phone_number: Optional[str] = None,
    chat_jid: Optional[str] = None,
    query: Optional[str] = None,
    limit: int = 20,
    page: int = 0,
) -> list[Message]:
    """Newest-first messages matching the filters."""
    where: list[str] = []
    params: list[Any] = []
    if after:
        where.append("m.timestamp > ?")
        params.append(_to_utc_iso(after, "after"))
    if before:
        where.append("m.timestamp < ?")
        params.append(_to_utc_iso(before, "before"))
    if sender_phone_number:
        where.append("m.sender = ?")
        params.append(_digits(sender_phone_number) or sender_phone_number)
    if chat_jid:
        where.append("m.chat_jid = ?")
        params.append(chat_jid)
    if query:
        where.append("LOWER(m.content) LIKE LOWER(?)")
        params.append(f"%{query}%")
    limit, offset = _clamp(limit, page)
    sql = _MESSAGE_SELECT
    if where:
        sql += " WHERE " + " AND ".join(where)
    sql += " ORDER BY m.timestamp DESC, m.rowid DESC LIMIT ? OFFSET ?"
    params.extend([limit, offset])
    with connect() as conn:
        rows = conn.execute(sql, params).fetchall()
    return [_row_to_message(r) for r in rows]


def get_message(message_id: str, chat_jid: Optional[str] = None) -> Optional[Message]:
    sql = _MESSAGE_SELECT + " WHERE m.id = ?"
    params: list[Any] = [message_id]
    if chat_jid:
        sql += " AND m.chat_jid = ?"
        params.append(chat_jid)
    sql += " LIMIT 1"
    with connect() as conn:
        row = conn.execute(sql, params).fetchone()
    return _row_to_message(row) if row else None


def get_message_context(message_id: str, before: int = 5, after: int = 5, chat_jid: Optional[str] = None) -> MessageContext:
    """The message plus the surrounding messages from the same chat, oldest first."""
    target = get_message(message_id, chat_jid)
    if target is None:
        raise ValueError(f"Message {message_id!r} not found")
    ts = target.timestamp.astimezone(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ") if target.timestamp else ""
    before = max(0, min(int(before), 100))
    after = max(0, min(int(after), 100))
    with connect() as conn:
        rows_before = conn.execute(
            _MESSAGE_SELECT + " WHERE m.chat_jid = ? AND m.timestamp < ? ORDER BY m.timestamp DESC, m.rowid DESC LIMIT ?",
            (target.chat_jid, ts, before),
        ).fetchall()
        rows_after = conn.execute(
            _MESSAGE_SELECT + " WHERE m.chat_jid = ? AND m.timestamp > ? ORDER BY m.timestamp ASC, m.rowid ASC LIMIT ?",
            (target.chat_jid, ts, after),
        ).fetchall()
    return MessageContext(
        message=target,
        before=[_row_to_message(r) for r in reversed(rows_before)],
        after=[_row_to_message(r) for r in rows_after],
    )


_CHAT_SELECT_WITH_LAST = """
    SELECT c.jid, c.name, c.is_group, c.last_message_time,
           lm.content AS last_message, lm.sender AS last_sender, lm.is_from_me AS last_is_from_me
    FROM chats c
    LEFT JOIN messages lm ON lm.rowid = (
        SELECT m.rowid FROM messages m WHERE m.chat_jid = c.jid
        ORDER BY m.timestamp DESC, m.rowid DESC LIMIT 1
    )
"""

_CHAT_SELECT_PLAIN = "SELECT c.jid, c.name, c.is_group, c.last_message_time FROM chats c"


def list_chats(
    query: Optional[str] = None,
    limit: int = 20,
    page: int = 0,
    include_last_message: bool = True,
    sort_by: str = "last_active",
) -> list[Chat]:
    sql = _CHAT_SELECT_WITH_LAST if include_last_message else _CHAT_SELECT_PLAIN
    params: list[Any] = []
    if query:
        sql += " WHERE (LOWER(c.name) LIKE LOWER(?) OR c.jid LIKE ?)"
        params.extend([f"%{query}%", f"%{query}%"])
    if sort_by == "name":
        sql += " ORDER BY LOWER(c.name) ASC, c.jid ASC"
    else:
        sql += " ORDER BY c.last_message_time IS NULL, c.last_message_time DESC"
    limit, offset = _clamp(limit, page)
    sql += " LIMIT ? OFFSET ?"
    params.extend([limit, offset])
    with connect() as conn:
        rows = conn.execute(sql, params).fetchall()
    return [_row_to_chat(r) for r in rows]


def get_chat(chat_jid: str, include_last_message: bool = True) -> Optional[Chat]:
    sql = (_CHAT_SELECT_WITH_LAST if include_last_message else _CHAT_SELECT_PLAIN) + " WHERE c.jid = ?"
    with connect() as conn:
        row = conn.execute(sql, (chat_jid,)).fetchone()
    return _row_to_chat(row) if row else None


def get_direct_chat_by_contact(phone_number: str) -> Optional[Chat]:
    """Direct chat for a phone number (digits only; a trailing match is enough)."""
    digits = _digits(phone_number)
    if not digits:
        return None
    with connect() as conn:
        row = conn.execute(
            _CHAT_SELECT_WITH_LAST
            + " WHERE c.is_group = 0 AND c.jid LIKE '%@s.whatsapp.net' AND substr(c.jid, 1, instr(c.jid, '@') - 1) LIKE ?"
            " ORDER BY CASE WHEN c.jid = ? THEN 0 ELSE 1 END, c.last_message_time DESC LIMIT 1",
            (f"%{digits}", f"{digits}@s.whatsapp.net"),
        ).fetchone()
    return _row_to_chat(row) if row else None


def get_contact_chats(jid: str, limit: int = 20, page: int = 0) -> list[Chat]:
    """Every chat (direct or group) where the contact has sent a message."""
    user = jid.split("@")[0]
    limit, offset = _clamp(limit, page)
    with connect() as conn:
        rows = conn.execute(
            _CHAT_SELECT_WITH_LAST
            + """
            WHERE c.jid = ? OR c.jid IN (SELECT DISTINCT chat_jid FROM messages WHERE sender = ?)
            ORDER BY c.last_message_time IS NULL, c.last_message_time DESC
            LIMIT ? OFFSET ?
            """,
            (jid, user, limit, offset),
        ).fetchall()
    return [_row_to_chat(r) for r in rows]


def get_last_interaction(jid: str) -> Optional[Message]:
    """Most recent message sent by, or in the direct chat with, a contact."""
    user = jid.split("@")[0]
    with connect() as conn:
        row = conn.execute(
            _MESSAGE_SELECT + " WHERE m.sender = ? OR m.chat_jid = ? ORDER BY m.timestamp DESC, m.rowid DESC LIMIT 1",
            (user, jid),
        ).fetchone()
    return _row_to_message(row) if row else None


def iter_all_chats() -> Iterator[Chat]:
    with connect() as conn:
        for row in conn.execute(_CHAT_SELECT_PLAIN):
            yield _row_to_chat(row)
