package main

import (
	"path/filepath"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := OpenStore(filepath.Join(t.TempDir(), "messages.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestUpsertChatKeepsNameAndMovesTimeForward(t *testing.T) {
	s := newTestStore(t)
	t1 := time.Date(2025, 4, 1, 10, 0, 0, 0, time.UTC)
	t0 := t1.Add(-time.Hour)

	if err := s.UpsertChat("123@s.whatsapp.net", "Alice", false, t1); err != nil {
		t.Fatal(err)
	}
	// Empty name and an older timestamp must not regress the row.
	if err := s.UpsertChat("123@s.whatsapp.net", "", false, t0); err != nil {
		t.Fatal(err)
	}
	c, err := s.GetChat("123@s.whatsapp.net")
	if err != nil || c == nil {
		t.Fatalf("GetChat: %v %v", c, err)
	}
	if c.Name != "Alice" {
		t.Errorf("name = %q, want Alice", c.Name)
	}
	if !c.LastMessageTime.Equal(t1) {
		t.Errorf("last = %v, want %v", c.LastMessageTime, t1)
	}
	// Zero time must not clear an existing timestamp either.
	if err := s.UpsertChat("123@s.whatsapp.net", "Alice B", false, time.Time{}); err != nil {
		t.Fatal(err)
	}
	c, _ = s.GetChat("123@s.whatsapp.net")
	if c.Name != "Alice B" || !c.LastMessageTime.Equal(t1) {
		t.Errorf("after zero-time upsert: %+v", c)
	}
}

func TestRenameChatIfDefault(t *testing.T) {
	s := newTestStore(t)
	_ = s.UpsertChat("123@s.whatsapp.net", "123", false, time.Now())
	_ = s.UpsertChat("456@s.whatsapp.net", "Bob", false, time.Now())
	_ = s.RenameChatIfDefault("123@s.whatsapp.net", "Alice")
	_ = s.RenameChatIfDefault("456@s.whatsapp.net", "Robert")
	a, _ := s.GetChat("123@s.whatsapp.net")
	b, _ := s.GetChat("456@s.whatsapp.net")
	if a.Name != "Alice" {
		t.Errorf("bare-number chat should be renamed, got %q", a.Name)
	}
	if b.Name != "Bob" {
		t.Errorf("named chat should keep its name, got %q", b.Name)
	}
}

func TestContacts(t *testing.T) {
	s := newTestStore(t)
	jid := "123@s.whatsapp.net"
	_ = s.UpsertContact(jid, "123", "", "alice-push")
	if got := s.ContactName(jid); got != "alice-push" {
		t.Errorf("push name fallback = %q", got)
	}
	_ = s.UpsertContact(jid, "", "Alice Liddell", "")
	if got := s.ContactName(jid); got != "Alice Liddell" {
		t.Errorf("address-book name should win, got %q", got)
	}
	_ = s.UpsertContact(jid, "", "", "newer-push")
	if got := s.ContactName(jid); got != "Alice Liddell" {
		t.Errorf("push name update must not clobber name, got %q", got)
	}
	if got := s.ContactName("nobody@s.whatsapp.net"); got != "" {
		t.Errorf("unknown contact = %q", got)
	}
}

func TestMessageRoundTrip(t *testing.T) {
	s := newTestStore(t)
	chat := "123@s.whatsapp.net"
	ts := time.Date(2025, 4, 1, 12, 34, 56, 0, time.UTC)
	_ = s.UpsertChat(chat, "Alice", false, ts)

	media := &MediaInfo{
		Type: "image", Filename: "image_ABC.jpg", MimeType: "image/jpeg",
		URL: "https://mmg.whatsapp.net/v/t62/x.enc?ccb=1", DirectPath: "/v/t62/x.enc",
		MediaKey: []byte{1, 2, 3}, FileSHA256: []byte{4}, FileEncSHA256: []byte{5}, FileLength: 42,
	}
	in := &StoredMessage{ID: "ABC", ChatJID: chat, Sender: "123", Content: "look", Timestamp: ts, Media: media, QuotedID: "Q1"}
	if err := s.InsertMessage(in); err != nil {
		t.Fatal(err)
	}
	out, err := s.GetMessage("ABC", chat)
	if err != nil || out == nil {
		t.Fatalf("GetMessage: %v %v", out, err)
	}
	if out.Content != "look" || !out.Timestamp.Equal(ts) || out.IsFromMe || out.QuotedID != "Q1" {
		t.Errorf("unexpected message: %+v", out)
	}
	if out.Media == nil || out.Media.Type != "image" || out.Media.FileLength != 42 || string(out.Media.MediaKey) != "\x01\x02\x03" {
		t.Errorf("unexpected media: %+v", out.Media)
	}

	// Text-only message has nil Media.
	_ = s.InsertMessage(&StoredMessage{ID: "DEF", ChatJID: chat, Sender: "123", Content: "hi", Timestamp: ts})
	txt, _ := s.GetMessage("DEF", chat)
	if txt.Media != nil {
		t.Errorf("text message should have nil media")
	}

	// Empty messages are ignored, not errors.
	if err := s.InsertMessage(&StoredMessage{ID: "GHI", ChatJID: chat, Timestamp: ts}); err != nil {
		t.Errorf("empty insert: %v", err)
	}
	if m, _ := s.GetMessage("GHI", chat); m != nil {
		t.Errorf("empty message was stored: %+v", m)
	}

	ok, err := s.UpdateMessageContent("DEF", chat, "edited")
	if err != nil || !ok {
		t.Fatalf("UpdateMessageContent: %v %v", ok, err)
	}
	txt, _ = s.GetMessage("DEF", chat)
	if txt.Content != "edited" {
		t.Errorf("content after edit = %q", txt.Content)
	}
	if ok, _ := s.UpdateMessageContent("missing", chat, "x"); ok {
		t.Errorf("update of missing message reported success")
	}

	chats, msgs, err := s.Counts()
	if err != nil || chats != 1 || msgs != 2 {
		t.Errorf("Counts = %d %d %v", chats, msgs, err)
	}

	if missing, _ := s.GetMessage("nope", chat); missing != nil {
		t.Errorf("missing message should be nil")
	}
}

func TestTimestampsAreISO8601UTC(t *testing.T) {
	s := newTestStore(t)
	ts := time.Date(2025, 4, 1, 12, 0, 0, 0, time.FixedZone("X", 3600))
	_ = s.UpsertChat("c@g.us", "G", true, ts)
	_ = s.InsertMessage(&StoredMessage{ID: "1", ChatJID: "c@g.us", Sender: "1", Content: "x", Timestamp: ts})
	var raw string
	if err := s.db.QueryRow(`SELECT timestamp FROM messages WHERE id = '1'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if raw != "2025-04-01T11:00:00Z" {
		t.Errorf("stored timestamp = %q, want UTC ISO-8601", raw)
	}
}

func TestSafeFileName(t *testing.T) {
	cases := map[string]string{
		"report.pdf":         "report.pdf",
		"../../etc/passwd":   "____etc_passwd",
		"a/b\\c:d":           "a_b_c_d",
		"":                   "file",
		"  spaced name.txt ": "spaced name.txt",
	}
	for in, want := range cases {
		if got := safeFileName(in); got != want {
			t.Errorf("safeFileName(%q) = %q, want %q", in, got, want)
		}
	}
}
