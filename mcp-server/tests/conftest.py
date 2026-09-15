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
CREATE TABLE polls (
    message_id TEXT NOT NULL, chat_jid TEXT NOT NULL, sender_jid TEXT NOT NULL DEFAULT '', is_from_me INTEGER NOT NULL DEFAULT 0,
    name TEXT NOT NULL DEFAULT '', options TEXT NOT NULL DEFAULT '[]', selectable INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (message_id, chat_jid)
);
CREATE TABLE poll_votes (
    poll_id TEXT NOT NULL, chat_jid TEXT NOT NULL, voter TEXT NOT NULL, selected TEXT NOT NULL DEFAULT '[]', timestamp TEXT NOT NULL,
    PRIMARY KEY (poll_id, chat_jid, voter)
);
CREATE TABLE events (
    message_id TEXT NOT NULL, chat_jid TEXT NOT NULL, sender_jid TEXT NOT NULL DEFAULT '', is_from_me INTEGER NOT NULL DEFAULT 0,
    name TEXT NOT NULL DEFAULT '', description TEXT NOT NULL DEFAULT '', start_time TEXT, end_time TEXT,
    location_name TEXT NOT NULL DEFAULT '', location_address TEXT NOT NULL DEFAULT '', latitude REAL, longitude REAL,
    join_link TEXT NOT NULL DEFAULT '', is_canceled INTEGER NOT NULL DEFAULT 0, extra_guests_allowed INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (message_id, chat_jid)
);
CREATE TABLE event_responses (
    event_id TEXT NOT NULL, chat_jid TEXT NOT NULL, responder TEXT NOT NULL, response TEXT NOT NULL,
    extra_guests INTEGER NOT NULL DEFAULT 0, timestamp TEXT NOT NULL,
    PRIMARY KEY (event_id, chat_jid, responder)
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
        ("G3", GROUP, "14155550001", "[poll] Next meeting?\n• Tuesday\n• Thursday", "2025-03-20T18:30:00Z", 0, "", "", ""),
        ("G4", GROUP, ME, "[event] Book swap\nWhen: 2025-04-12T17:00:00Z\nWhere: Alice's place", "2025-03-20T19:00:00Z", 1, "", "", ""),
        ("G5", GROUP, "14155550002", "[event] Old picnic\nWhen: 2025-03-01T12:00:00Z", "2025-02-20T09:00:00Z", 0, "", "", ""),
    ]
    conn.executemany(
        "INSERT INTO messages (id, chat_jid, sender, content, timestamp, is_from_me, media_type, filename, quoted_id)"
        " VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
        msgs,
    )
    conn.execute(
        "INSERT INTO polls VALUES (?, ?, ?, ?, ?, ?, ?)",
        ("G3", GROUP, "14155550001@s.whatsapp.net", 0, "Next meeting?", '["Tuesday", "Thursday"]', 1),
    )
    conn.executemany(
        "INSERT INTO poll_votes VALUES (?, ?, ?, ?, ?)",
        [
            ("G3", GROUP, "14155550002", '["Thursday"]', "2025-03-20T18:31:00Z"),
            ("G3", GROUP, ME, '["Tuesday"]', "2025-03-20T18:32:00Z"),
            ("G3", GROUP, "14155550009", "[]", "2025-03-20T18:33:00Z"),  # retracted
        ],
    )
    conn.executemany(
        "INSERT INTO events VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
        [
            ("G4", GROUP, "14155550000@s.whatsapp.net", 1, "Book swap", "Bring one, take one", "2025-04-12T17:00:00Z", "2025-04-12T19:00:00Z",
             "Alice's place", "1 Rabbit Hole", 51.5, -0.1, "", 0, 1),
            ("G5", GROUP, "14155550002@s.whatsapp.net", 0, "Old picnic", "", "2025-03-01T12:00:00Z", None, "", "", None, None, "", 1, 0),
        ],
    )
    conn.executemany(
        "INSERT INTO event_responses VALUES (?, ?, ?, ?, ?, ?)",
        [
            ("G4", GROUP, "14155550001", "going", 1, "2025-03-20T19:05:00Z"),
            ("G4", GROUP, "14155550002", "maybe", 0, "2025-03-20T19:06:00Z"),
        ],
    )
    conn.commit()
    conn.close()
    monkeypatch.setenv("GRAAB_DB_PATH", str(path))
    return path
