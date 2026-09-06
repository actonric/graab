package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"
)

// newOfflineBridge builds a Bridge around a whatsmeow client that is never
// connected. Store lookups work; network calls fail and fall back.
func newOfflineBridge(t *testing.T) *Bridge {
	t.Helper()
	dir := t.TempDir()
	ctx := context.Background()
	container, err := sqlstore.New(ctx, "sqlite3", "file:"+filepath.Join(dir, "session.db")+"?_foreign_keys=on", waLog.Noop)
	if err != nil {
		t.Fatalf("sqlstore: %v", err)
	}
	device := container.NewDevice()
	client := whatsmeow.NewClient(device, waLog.Noop)
	store, err := OpenStore(filepath.Join(dir, "messages.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return &Bridge{client: client, store: store, storeDir: dir, log: waLog.Noop}
}

func TestHandleMessageStoresTextAndPushName(t *testing.T) {
	b := newOfflineBridge(t)
	alice := types.NewJID("14155550001", types.DefaultUserServer)
	ts := time.Date(2025, 4, 1, 10, 0, 0, 0, time.UTC)

	b.handleEvent(&events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: alice, Sender: alice},
			ID:            "MSG1", PushName: "alice", Timestamp: ts,
		},
		Message: &waE2E.Message{Conversation: proto.String("hello there")},
	})

	m, err := b.store.GetMessage("MSG1", alice.String())
	if err != nil || m == nil {
		t.Fatalf("message not stored: %v %v", m, err)
	}
	if m.Content != "hello there" || m.Sender != "14155550001" || m.IsFromMe {
		t.Errorf("unexpected message: %+v", m)
	}
	c, _ := b.store.GetChat(alice.String())
	if c == nil || c.Name != "alice" || c.IsGroup || !c.LastMessageTime.Equal(ts) {
		t.Errorf("unexpected chat: %+v", c)
	}
	if got := b.store.ContactName(alice.String()); got != "alice" {
		t.Errorf("push name not recorded: %q", got)
	}
}

func TestHandleMessageResolvesLIDViaAlt(t *testing.T) {
	b := newOfflineBridge(t)
	lid := types.NewJID("99887766", types.HiddenUserServer)
	pn := types.NewJID("14155550002", types.DefaultUserServer)

	b.handleEvent(&events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: lid, Sender: lid, SenderAlt: pn},
			ID:            "MSG2", Timestamp: time.Now(),
		},
		Message: &waE2E.Message{Conversation: proto.String("from a lid")},
	})
	if m, _ := b.store.GetMessage("MSG2", pn.String()); m == nil || m.Sender != "14155550002" {
		t.Errorf("LID message should be keyed by phone JID, got %+v", m)
	}
	if m, _ := b.store.GetMessage("MSG2", lid.String()); m != nil {
		t.Errorf("message should not be stored under the LID")
	}
}

func TestHandleMessageGroupEditAndRevoke(t *testing.T) {
	b := newOfflineBridge(t)
	group := types.NewJID("120363000000000001", types.GroupServer)
	bob := types.NewJID("14155550003", types.DefaultUserServer)
	base := types.MessageInfo{
		MessageSource: types.MessageSource{Chat: group, Sender: bob, IsGroup: true},
		ID:            "G1", PushName: "bob", Timestamp: time.Now(),
	}
	b.handleEvent(&events.Message{Info: base, Message: &waE2E.Message{Conversation: proto.String("draft")}})

	c, _ := b.store.GetChat(group.String())
	if c == nil || !c.IsGroup || c.Name != "Group 120363000000000001" {
		t.Errorf("group chat fallback name: %+v", c)
	}

	// Edit: whatsmeow hands us the new content with IsEdit and the raw protocol message.
	edit := base
	edit.ID = "G1-EDIT"
	b.handleEvent(&events.Message{
		Info:    edit,
		IsEdit:  true,
		Message: &waE2E.Message{Conversation: proto.String("final")},
		RawMessage: &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{
			Type: waE2E.ProtocolMessage_MESSAGE_EDIT.Enum(),
			Key:  &waCommon.MessageKey{ID: proto.String("G1")},
		}},
	})
	if m, _ := b.store.GetMessage("G1", group.String()); m == nil || m.Content != "final" {
		t.Errorf("edit not applied: %+v", m)
	}
	if m, _ := b.store.GetMessage("G1-EDIT", group.String()); m != nil {
		t.Errorf("edit should not create a second row")
	}

	// Revoke marks the original as deleted.
	rev := base
	rev.ID = "G1-REV"
	b.handleEvent(&events.Message{Info: rev, Message: &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{
		Type: waE2E.ProtocolMessage_REVOKE.Enum(),
		Key:  &waCommon.MessageKey{ID: proto.String("G1")},
	}}})
	if m, _ := b.store.GetMessage("G1", group.String()); m == nil || m.Content != "[message deleted]" {
		t.Errorf("revoke not applied: %+v", m)
	}
	if m, _ := b.store.GetMessage("G1-REV", group.String()); m != nil {
		t.Errorf("protocol message should not be stored")
	}
}

func TestHandleHistorySync(t *testing.T) {
	b := newOfflineBridge(t)
	alice := "14155550001@s.whatsapp.net"
	group := "120363000000000009@g.us"

	hs := &waHistorySync.HistorySync{
		SyncType: waHistorySync.HistorySync_RECENT.Enum(),
		Pushnames: []*waHistorySync.Pushname{
			{ID: proto.String(alice), Pushname: proto.String("Alice")},
		},
		Conversations: []*waHistorySync.Conversation{
			{
				ID:               proto.String(alice),
				LastMsgTimestamp: proto.Uint64(1743501000),
				Messages: []*waHistorySync.HistorySyncMsg{
					{Message: &waWeb.WebMessageInfo{
						Key:              &waCommon.MessageKey{ID: proto.String("H1"), FromMe: proto.Bool(false), RemoteJID: proto.String(alice)},
						MessageTimestamp: proto.Uint64(1743500000),
						PushName:         proto.String("Alice"),
						Message:          &waE2E.Message{Conversation: proto.String("old hello")},
					}},
					{Message: &waWeb.WebMessageInfo{
						Key:              &waCommon.MessageKey{ID: proto.String("H2"), FromMe: proto.Bool(true), RemoteJID: proto.String(alice)},
						MessageTimestamp: proto.Uint64(1743501000),
						Message: &waE2E.Message{ImageMessage: &waE2E.ImageMessage{
							Caption: proto.String("pic"), Mimetype: proto.String("image/jpeg"), MediaKey: []byte{1},
						}},
					}},
					{Message: &waWeb.WebMessageInfo{ // reaction: skipped
						Key:              &waCommon.MessageKey{ID: proto.String("H3"), FromMe: proto.Bool(false)},
						MessageTimestamp: proto.Uint64(1743501500),
						Message:          &waE2E.Message{ReactionMessage: &waE2E.ReactionMessage{Text: proto.String("👍")}},
					}},
				},
			},
			{
				ID:   proto.String(group),
				Name: proto.String("Book club"),
				Messages: []*waHistorySync.HistorySyncMsg{
					{Message: &waWeb.WebMessageInfo{
						Key:              &waCommon.MessageKey{ID: proto.String("H4"), FromMe: proto.Bool(false), Participant: proto.String("14155550003@s.whatsapp.net")},
						MessageTimestamp: proto.Uint64(1743600000),
						Message:          &waE2E.Message{Conversation: proto.String("next book")},
					}},
				},
			},
			{ID: proto.String("status@broadcast")}, // ignored
		},
	}
	b.handleEvent(&events.HistorySync{Data: hs})

	if m, _ := b.store.GetMessage("H1", alice); m == nil || m.Content != "old hello" || m.IsFromMe {
		t.Errorf("H1: %+v", m)
	}
	if m, _ := b.store.GetMessage("H2", alice); m == nil || !m.IsFromMe || m.Media == nil || m.Media.Filename != "image_H2.jpg" {
		t.Errorf("H2: %+v", m)
	}
	if m, _ := b.store.GetMessage("H3", alice); m != nil {
		t.Errorf("reaction should be skipped")
	}
	if m, _ := b.store.GetMessage("H4", group); m == nil || m.Sender != "14155550003" {
		t.Errorf("H4 participant sender: %+v", m)
	}
	c, _ := b.store.GetChat(alice)
	if c == nil || c.Name != "Alice" || !c.LastMessageTime.Equal(time.Unix(1743501000, 0)) {
		t.Errorf("alice chat: %+v", c)
	}
	g, _ := b.store.GetChat(group)
	if g == nil || g.Name != "Book club" || !g.IsGroup {
		t.Errorf("group chat: %+v", g)
	}
	if bc, _ := b.store.GetChat("status@broadcast"); bc != nil {
		t.Errorf("broadcast should be ignored")
	}
	chats, msgs, _ := b.store.Counts()
	if chats != 2 || msgs != 3 {
		t.Errorf("counts = %d chats, %d messages", chats, msgs)
	}
}

func TestDownloadMediaValidation(t *testing.T) {
	b := newOfflineBridge(t)
	ctx := context.Background()
	chat := "14155550001@s.whatsapp.net"
	_ = b.store.UpsertChat(chat, "A", false, time.Now())
	_ = b.store.InsertMessage(&StoredMessage{ID: "T", ChatJID: chat, Sender: "1", Content: "text only", Timestamp: time.Now()})

	if _, err := b.DownloadMedia(ctx, "missing", chat); err == nil {
		t.Errorf("expected error for unknown message")
	}
	if _, err := b.DownloadMedia(ctx, "T", chat); err == nil {
		t.Errorf("expected error for text message")
	}
	if _, err := b.SendMessage(ctx, "14155550001", "hi", ""); err == nil {
		t.Errorf("expected error when not connected")
	}
	if st := b.Status(); st.Connected || st.Chats != 1 || st.Messages != 1 {
		t.Errorf("status: %+v", st)
	}
}
