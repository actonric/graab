import sqlite3
from pathlib import Path

import pytest

# Mirror of the schema in bridge/store.go. Keep the two in sync.
SCHEMA = """
CREATE TABLE chats (
    jid TEXT PRIMARY KEY, name TEXT NOT NULL DEFAULT '', is_group INTEGER NOT NULL DEFAULT 0, last_message_time TEXT
);
CREATE TABLE contacts (
    jid TEXT PRIMARY KEY, phone TEXT NOT NULL DEFAULT '', name TEXT NOT NULL DEFAULT '',
    push_name TEXT NOT NULL DEFAULT '', updated_at TEXT NOT NULL
);
CREATE TABLE messages (
    id TEXT NOT NULL, chat_jid TEXT NOT NULL, sender TEXT NOT NULL DEFAULT '', content TEXT NOT NULL DEFAULT '',
    timestamp TEXT NOT NULL, is_from_me INTEGER NOT NULL DEFAULT 0, media_type TEXT NOT NULL DEFAULT '',
    filename TEXT NOT NULL DEFAULT '', mime_type TEXT NOT NULL DEFAULT '', url TEXT NOT NULL DEFAULT '',
    direct_path TEXT NOT NULL DEFAULT '', media_key BLOB, file_sha256 BLOB, file_enc_sha256 BLOB,
    file_length INTEGER NOT NULL DEFAULT 0, quoted_id TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (id, chat_jid), FOREIGN KEY (chat_jid) REFERENCES chats(jid)
);
"""

ALICE = "14155550001@s.whatsapp.net"
BOB = "14155550002@s.whatsapp.net"
GROUP = "120363000000000001@g.us"
ME = "14155550000"


@pytest.fixture
def fixture_db(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> Path:
    path = tmp_path / "messages.db"
    conn = sqlite3.connect(path)
    conn.executescript(SCHEMA)
    conn.executemany(
        "INSERT INTO chats VALUES (?, ?, ?, ?)",
        [
            (ALICE, "Alice Liddell", 0, "2025-04-01T10:05:00Z"),
            (BOB, "14155550002", 0, "2025-03-30T09:00:00Z"),
            (GROUP, "Book club", 1, "2025-04-02T18:00:00Z"),
        ],
    )
    conn.executemany(
        "INSERT INTO contacts VALUES (?, ?, ?, ?, ?)",
        [
            (ALICE, "14155550001", "Alice Liddell", "alice", "2025-04-01T00:00:00Z"),
            (BOB, "14155550002", "", "bobby", "2025-04-01T00:00:00Z"),
        ],
    )
    msgs = [
        # id, chat, sender, content, ts, from_me, media_type, filename, quoted
        ("A1", ALICE, "14155550001", "hey, lunch tomorrow?", "2025-04-01T10:00:00Z", 0, "", "", ""),
        ("A2", ALICE, ME, "sure, noon?", "2025-04-01T10:01:00Z", 1, "", "", "A1"),
        ("A3", ALICE, "14155550001", "", "2025-04-01T10:02:00Z", 0, "image", "image_A3.jpg", ""),
        ("A4", ALICE, "14155550001", "that place", "2025-04-01T10:05:00Z", 0, "", "", ""),
        ("B1", BOB, "14155550002", "invoice attached", "2025-03-30T09:00:00Z", 0, "document", "invoice.pdf", ""),
        ("G1", GROUP, "14155550001", "next book: Dune", "2025-04-02T17:00:00Z", 0, "", "", ""),
        ("G2", GROUP, "14155550002", "lunch is on me", "2025-04-02T18:00:00Z", 0, "", "", ""),
    ]
    conn.executemany(
        "INSERT INTO messages (id, chat_jid, sender, content, timestamp, is_from_me, media_type, filename, quoted_id)"
        " VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
        msgs,
    )
    conn.commit()
    conn.close()
    monkeypatch.setenv("GRAAB_DB_PATH", str(path))
    return path
