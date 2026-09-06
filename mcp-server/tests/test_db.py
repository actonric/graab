import pytest

from graab_mcp import db
from tests.conftest import ALICE, BOB, GROUP


def test_missing_db_gives_helpful_error(tmp_path, monkeypatch):
    monkeypatch.setenv("GRAAB_DB_PATH", str(tmp_path / "nope.db"))
    with pytest.raises(db.DatabaseUnavailable, match="Start the bridge"):
        db.search_contacts("x")


def test_search_contacts(fixture_db):
    by_name = db.search_contacts("alice")
    assert [c.jid for c in by_name] == [ALICE]
    assert by_name[0].phone_number == "14155550001"
    assert by_name[0].name == "Alice Liddell"

    by_push = db.search_contacts("bobby")
    assert [c.jid for c in by_push] == [BOB]

    by_phone = db.search_contacts("+1 415 555 0002")
    assert [c.jid for c in by_phone] == [BOB]

    everything = db.search_contacts("")
    assert {c.jid for c in everything} == {ALICE, BOB}
    assert everything[0].name == "Alice Liddell"  # named contacts sort first


def test_list_messages_filters(fixture_db):
    newest = db.list_messages(limit=3)
    assert [m.id for m in newest] == ["G2", "G1", "A4"]

    lunch = db.list_messages(query="LUNCH")
    assert {m.id for m in lunch} == {"A1", "G2"}

    alice_only = db.list_messages(chat_jid=ALICE)
    assert [m.id for m in alice_only] == ["A4", "A3", "A2", "A1"]

    from_alice = db.list_messages(sender_phone_number="+1 (415) 555-0001")
    assert {m.id for m in from_alice} == {"A1", "A3", "A4", "G1"}

    window = db.list_messages(after="2025-04-01T10:00:30Z", before="2025-04-01T10:04:00+00:00")
    assert [m.id for m in window] == ["A3", "A2"]

    paged = db.list_messages(chat_jid=ALICE, limit=2, page=1)
    assert [m.id for m in paged] == ["A2", "A1"]

    with pytest.raises(ValueError, match="after"):
        db.list_messages(after="yesterday")


def test_message_names_and_media(fixture_db):
    msgs = {m.id: m for m in db.list_messages(limit=50)}
    assert msgs["A1"].sender_name == "Alice Liddell"
    assert msgs["A1"].chat_name == "Alice Liddell"
    assert msgs["A2"].sender_name == "Me"
    assert msgs["A2"].is_from_me is True
    assert msgs["A2"].quoted_id == "A1"
    assert msgs["G2"].sender_name == "bobby"  # push name fallback in a group
    assert msgs["A3"].media_type == "image"
    assert msgs["A3"].filename == "image_A3.jpg"
    assert msgs["A3"].timestamp.isoformat() == "2025-04-01T10:02:00+00:00"

    d = msgs["A1"].to_dict()
    assert "media_type" not in d and "quoted_id" not in d
    assert d["timestamp"] == "2025-04-01T10:00:00+00:00"
    assert "filename" in msgs["A3"].to_dict()


def test_message_context(fixture_db):
    ctx = db.get_message_context("A2", before=1, after=2)
    assert ctx.message.id == "A2"
    assert [m.id for m in ctx.before] == ["A1"]
    assert [m.id for m in ctx.after] == ["A3", "A4"]

    wide = db.get_message_context("A3", before=10, after=10)
    assert [m.id for m in wide.before] == ["A1", "A2"]  # oldest first
    assert [m.id for m in wide.after] == ["A4"]

    with pytest.raises(ValueError, match="not found"):
        db.get_message_context("ZZZ")

    assert db.get_message_context("A1", chat_jid=ALICE).message.chat_jid == ALICE
    with pytest.raises(ValueError):
        db.get_message_context("A1", chat_jid=GROUP)


def test_list_chats(fixture_db):
    chats = db.list_chats()
    assert [c.jid for c in chats] == [GROUP, ALICE, BOB]
    group = chats[0]
    assert group.is_group and group.name == "Book club"
    assert group.last_message == "lunch is on me"
    assert group.last_sender == "14155550002"
    assert group.last_is_from_me is False
    assert group.last_message_time.isoformat() == "2025-04-02T18:00:00+00:00"

    by_name = db.list_chats(sort_by="name")
    assert [c.name for c in by_name] == ["14155550002", "Alice Liddell", "Book club"]

    assert [c.jid for c in db.list_chats(query="book")] == [GROUP]
    assert [c.jid for c in db.list_chats(query="0002")] == [BOB]

    plain = db.list_chats(include_last_message=False)
    assert plain[0].last_message is None

    assert [c.jid for c in db.list_chats(limit=1, page=1)] == [ALICE]


def test_get_chat_and_direct_chat(fixture_db):
    alice = db.get_chat(ALICE)
    assert alice and alice.name == "Alice Liddell" and alice.last_message == "that place"
    assert db.get_chat("nobody@s.whatsapp.net") is None

    direct = db.get_direct_chat_by_contact("+1 (415) 555-0001")
    assert direct and direct.jid == ALICE
    assert db.get_direct_chat_by_contact("555-0002").jid == BOB  # suffix match
    assert db.get_direct_chat_by_contact("") is None
    assert db.get_direct_chat_by_contact("000000") is None


def test_contact_chats_and_last_interaction(fixture_db):
    chats = db.get_contact_chats(ALICE)
    assert [c.jid for c in chats] == [GROUP, ALICE]

    last = db.get_last_interaction(ALICE)
    assert last and last.id == "G1"  # Alice's group message is newer than her DM

    last_bob = db.get_last_interaction(BOB)
    assert last_bob and last_bob.id == "G2"

    assert db.get_last_interaction("nobody@s.whatsapp.net") is None
