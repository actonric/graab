package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mau.fi/util/random"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waAdv"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"go.mau.fi/whatsmeow/util/gcmutil"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"
)

var (
	ownPN  = types.NewJID("14155550000", types.DefaultUserServer)
	ownLID = types.NewJID("111222333444", types.HiddenUserServer)
)

// newPairedOfflineBridge is newOfflineBridge with a saved device identity, so
// whatsmeow's message-secret store (needed for votes and RSVPs) exists.
func newPairedOfflineBridge(t *testing.T) *Bridge {
	t.Helper()
	dir := t.TempDir()
	ctx := context.Background()
	container, err := sqlstore.New(ctx, "sqlite3", "file:"+filepath.Join(dir, "session.db")+"?_foreign_keys=on", waLog.Noop)
	if err != nil {
		t.Fatalf("sqlstore: %v", err)
	}
	device := container.NewDevice()
	id := ownPN
	device.ID = &id
	device.LID = ownLID
	device.Account = &waAdv.ADVSignedDeviceIdentity{Details: []byte{1}, AccountSignature: make([]byte, 64), AccountSignatureKey: make([]byte, 32), DeviceSignature: make([]byte, 64)}
	if err := device.Save(ctx); err != nil {
		t.Fatalf("save device: %v", err)
	}
	client := whatsmeow.NewClient(device, waLog.Noop)
	store, err := OpenStore(filepath.Join(dir, "messages.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	cfg := Config{StoreDir: dir, MediaRoots: []string{filepath.Join(dir, "outbox")}, SendPerMinute: 30}
	return &Bridge{client: client, store: store, storeDir: dir, log: waLog.Noop, cfg: cfg, limiter: newRateLimiter(cfg.SendPerMinute)}
}

func pollCreation(name string, options ...string) *waE2E.Message {
	opts := make([]*waE2E.PollCreationMessage_Option, len(options))
	for i, o := range options {
		opts[i] = &waE2E.PollCreationMessage_Option{OptionName: proto.String(o)}
	}
	return &waE2E.Message{
		PollCreationMessageV3: &waE2E.PollCreationMessage{Name: proto.String(name), Options: opts, SelectableOptionsCount: proto.Uint32(1)},
		MessageContextInfo:    &waE2E.MessageContextInfo{MessageSecret: random.Bytes(32)},
	}
}

// encryptAddOn encrypts an add-on payload the way a phone would, using the
// key derivation mirrored in calendar.go.
func encryptAddOn(t *testing.T, useCase string, modSender types.JID, origID string, origSender types.JID, secret []byte, m proto.Message) (payload, iv []byte) {
	t.Helper()
	plaintext, err := proto.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	key, ad := msgSecretKey(useCase, modSender, origID, origSender, secret)
	iv = random.Bytes(12)
	payload, err = gcmutil.Encrypt(key, iv, plaintext, ad)
	if err != nil {
		t.Fatal(err)
	}
	return payload, iv
}

func TestDescribePollAndEvent(t *testing.T) {
	content, _, _, ok := describeMessage("P", pollCreation("Pizza night?", "Friday", "Saturday"))
	if !ok || content != "[poll] Pizza night?\n• Friday\n• Saturday" {
		t.Errorf("poll: %q %v", content, ok)
	}
	if pm := pollMessage(&waE2E.Message{PollCreationMessageV4: &waE2E.FutureProofMessage{Message: pollCreation("v4", "a", "b")}}); pm == nil || pm.GetName() != "v4" {
		t.Errorf("v4 poll not unwrapped: %v", pm)
	}

	ev := &waE2E.Message{EventMessage: &waE2E.EventMessage{
		Name: proto.String("Picnic"), Description: proto.String("Bring snacks"),
		StartTime: proto.Int64(1758391200), EndTime: proto.Int64(1758398400),
		Location: &waE2E.LocationMessage{Name: proto.String("Dolores Park"), Address: proto.String("SF"), DegreesLatitude: proto.Float64(37.76), DegreesLongitude: proto.Float64(-122.43)},
		JoinLink: proto.String("https://call.whatsapp.com/x"),
	}}
	content, _, _, ok = describeMessage("E", ev)
	want := "[event] Picnic\nWhen: 2025-09-20T18:00:00Z to 2025-09-20T20:00:00Z\nWhere: Dolores Park, SF\nLink: https://call.whatsapp.com/x\nBring snacks"
	if !ok || content != want {
		t.Errorf("event:\n got %q\nwant %q", content, want)
	}
	e := eventFromProto(ev.GetEventMessage())
	if e.Latitude == nil || *e.Latitude != 37.76 || e.LocationName != "Dolores Park" || !e.StartTime.Equal(time.Unix(1758391200, 0)) {
		t.Errorf("eventFromProto: %+v", e)
	}
	ev.EventMessage.IsCanceled = proto.Bool(true)
	if content, _, _, _ = describeMessage("E", ev); !strings.HasPrefix(content, "[event] Picnic (canceled)") {
		t.Errorf("canceled event: %q", content)
	}

	snapshot := &waE2E.Message{PollResultSnapshotMessage: &waE2E.PollResultSnapshotMessage{
		Name:      proto.String("Pizza night?"),
		PollVotes: []*waE2E.PollResultSnapshotMessage_PollVote{{OptionName: proto.String("Friday"), OptionVoteCount: proto.Int64(3)}},
	}}
	if content, _, _, ok = describeMessage("S", snapshot); !ok || content != "[poll results] Pizza night?\n• Friday: 3" {
		t.Errorf("snapshot: %q", content)
	}
}

func TestSelectedOptionNames(t *testing.T) {
	opts := []string{"Friday", "Saturday"}
	hashes := whatsmeow.HashPollOptions([]string{"Saturday", "Sunday"})
	got := selectedOptionNames(opts, hashes)
	if len(got) != 2 || got[0] != "Saturday" || !strings.HasPrefix(got[1], "?") {
		t.Errorf("selected = %v", got)
	}
	if got := selectedOptionNames(opts, nil); len(got) != 0 {
		t.Errorf("retracted vote = %v", got)
	}
}

func TestStorePollsAndEvents(t *testing.T) {
	s := newTestStore(t)
	chat := "120363000000000001@g.us"
	_ = s.UpsertChat(chat, "Group", true, time.Time{})

	if err := s.UpsertPoll(&StoredPoll{MessageID: "P1", ChatJID: chat, SenderJID: "1@lid", Name: "Q?", Options: []string{"a", "b"}, Selectable: 1}); err != nil {
		t.Fatal(err)
	}
	// An update without the sender keeps it.
	_ = s.UpsertPoll(&StoredPoll{MessageID: "P1", ChatJID: chat, Name: "Q!", Options: []string{"a", "b", "c"}})
	p, _ := s.GetPoll("P1", chat)
	if p == nil || p.SenderJID != "1@lid" || p.Name != "Q!" || len(p.Options) != 3 || p.Selectable != 0 {
		t.Errorf("poll: %+v", p)
	}
	if missing, _ := s.GetPoll("nope", chat); missing != nil {
		t.Errorf("missing poll should be nil")
	}

	t1 := time.Date(2025, 4, 1, 10, 0, 0, 0, time.UTC)
	_ = s.UpsertPollVote(&PollVote{PollID: "P1", ChatJID: chat, Voter: "2", Selected: []string{"a"}, Timestamp: t1})
	_ = s.UpsertPollVote(&PollVote{PollID: "P1", ChatJID: chat, Voter: "2", Selected: []string{"b"}, Timestamp: t1.Add(time.Minute)})
	_ = s.UpsertPollVote(&PollVote{PollID: "P1", ChatJID: chat, Voter: "2", Selected: []string{"c"}, Timestamp: t1.Add(-time.Minute)}) // stale
	_ = s.UpsertPollVote(&PollVote{PollID: "P1", ChatJID: chat, Voter: "3", Selected: nil, Timestamp: t1})
	votes, err := s.PollVotes("P1", chat)
	if err != nil || len(votes) != 2 {
		t.Fatalf("votes: %v %v", votes, err)
	}
	if votes[0].Voter != "3" || len(votes[0].Selected) != 0 || votes[1].Voter != "2" || votes[1].Selected[0] != "b" {
		t.Errorf("votes: %+v", votes)
	}

	lat, lon := 37.76, -122.43
	ev := &StoredEvent{MessageID: "E1", ChatJID: chat, SenderJID: "1@lid", Name: "Picnic", StartTime: t1, EndTime: t1.Add(2 * time.Hour),
		LocationName: "Park", Latitude: &lat, Longitude: &lon, JoinLink: "https://x", ExtraGuestsAllowed: true}
	if err := s.UpsertEvent(ev); err != nil {
		t.Fatal(err)
	}
	_ = s.UpsertEvent(&StoredEvent{MessageID: "E1", ChatJID: chat, Name: "Picnic", StartTime: t1, Canceled: true})
	got, _ := s.GetEvent("E1", chat)
	if got == nil || got.SenderJID != "1@lid" || !got.Canceled || got.Latitude != nil || !got.EndTime.IsZero() {
		t.Errorf("event: %+v", got)
	}
	_ = s.UpsertEvent(ev)
	got, _ = s.GetEvent("E1", chat)
	if got.Latitude == nil || *got.Longitude != lon || !got.ExtraGuestsAllowed || got.Canceled {
		t.Errorf("event restored: %+v", got)
	}

	_ = s.UpsertEventResponse(&EventResponse{EventID: "E1", ChatJID: chat, Responder: "2", Response: "going", ExtraGuests: 1, Timestamp: t1})
	_ = s.UpsertEventResponse(&EventResponse{EventID: "E1", ChatJID: chat, Responder: "2", Response: "maybe", Timestamp: t1.Add(-time.Hour)}) // stale
	rs, _ := s.EventResponses("E1", chat)
	if len(rs) != 1 || rs[0].Response != "going" || rs[0].ExtraGuests != 1 {
		t.Errorf("responses: %+v", rs)
	}
}

func TestPollVoteRoundTrip(t *testing.T) {
	b := newPairedOfflineBridge(t)
	ctx := context.Background()
	group := types.NewJID("120363000000000001", types.GroupServer)
	alice := types.NewJID("14155550001", types.DefaultUserServer)
	bob := types.NewJID("14155550002", types.DefaultUserServer)
	ts := time.Date(2025, 4, 1, 10, 0, 0, 0, time.UTC)

	poll := pollCreation("Pizza night?", "Friday", "Saturday")
	secret := poll.GetMessageContextInfo().GetMessageSecret()
	b.handleEvent(&events.Message{
		Info:    types.MessageInfo{MessageSource: types.MessageSource{Chat: group, Sender: alice, IsGroup: true}, ID: "P1", Timestamp: ts},
		Message: poll,
	})
	// whatsmeow stores the secret before handing us a live message.
	if err := b.client.Store.MsgSecrets.PutMessageSecret(ctx, group, alice, "P1", secret); err != nil {
		t.Fatal(err)
	}
	if p, _ := b.store.GetPoll("P1", group.String()); p == nil || p.SenderJID != alice.String() || p.Selectable != 1 {
		t.Fatalf("poll not stored: %+v", p)
	}
	if m, _ := b.store.GetMessage("P1", group.String()); m == nil || !strings.HasPrefix(m.Content, "[poll] Pizza night?") {
		t.Errorf("poll message: %+v", m)
	}

	// Bob votes; his phone encrypts with the derivation mirrored in calendar.go.
	payload, iv := encryptAddOn(t, "Poll Vote", bob, "P1", alice, secret,
		&waE2E.PollVoteMessage{SelectedOptions: whatsmeow.HashPollOptions([]string{"Saturday"})})
	b.handleEvent(&events.Message{
		Info: types.MessageInfo{MessageSource: types.MessageSource{Chat: group, Sender: bob, IsGroup: true}, ID: "V1", Timestamp: ts.Add(time.Minute)},
		Message: &waE2E.Message{PollUpdateMessage: &waE2E.PollUpdateMessage{
			PollCreationMessageKey: &waCommon.MessageKey{RemoteJID: proto.String(group.String()), Participant: proto.String(alice.String()), ID: proto.String("P1")},
			Vote:                   &waE2E.PollEncValue{EncPayload: payload, EncIV: iv},
			SenderTimestampMS:      proto.Int64(ts.Add(time.Minute).UnixMilli()),
		}},
	})

	// Our own vote, built by whatsmeow, arriving addressed from our LID
	// (whatsmeow encrypted it under the phone number: the retry path).
	pollInfo := b.pollMessageInfo(group, &StoredPoll{MessageID: "P1", SenderJID: alice.String()})
	own, err := b.client.BuildPollVote(ctx, &pollInfo, []string{"Friday"})
	if err != nil {
		t.Fatal(err)
	}
	b.handleEvent(&events.Message{
		Info:    types.MessageInfo{MessageSource: types.MessageSource{Chat: group, Sender: ownLID, IsFromMe: true, IsGroup: true}, ID: "V2", Timestamp: ts.Add(2 * time.Minute)},
		Message: own,
	})

	votes, _ := b.store.PollVotes("P1", group.String())
	if len(votes) != 2 {
		t.Fatalf("votes: %+v", votes)
	}
	if votes[0].Voter != "14155550002" || votes[0].Selected[0] != "Saturday" {
		t.Errorf("bob's vote: %+v", votes[0])
	}
	if votes[1].Voter != "14155550000" || votes[1].Selected[0] != "Friday" {
		t.Errorf("own vote: %+v", votes[1])
	}
	if m, _ := b.store.GetMessage("V1", group.String()); m != nil {
		t.Errorf("votes should not be stored as messages")
	}

	// A vote for a poll we never saw is ignored quietly.
	b.handleEvent(&events.Message{
		Info: types.MessageInfo{MessageSource: types.MessageSource{Chat: group, Sender: bob, IsGroup: true}, ID: "V3", Timestamp: ts},
		Message: &waE2E.Message{PollUpdateMessage: &waE2E.PollUpdateMessage{
			PollCreationMessageKey: &waCommon.MessageKey{ID: proto.String("UNKNOWN")}, Vote: &waE2E.PollEncValue{EncPayload: []byte{1}, EncIV: []byte{2}},
		}},
	})
}

func TestEventResponseRoundTrip(t *testing.T) {
	b := newPairedOfflineBridge(t)
	ctx := context.Background()
	group := types.NewJID("120363000000000001", types.GroupServer)
	alice := types.NewJID("14155550001", types.DefaultUserServer)
	bob := types.NewJID("14155550002", types.DefaultUserServer)
	ts := time.Date(2025, 4, 1, 10, 0, 0, 0, time.UTC)
	secret := random.Bytes(32)

	b.handleEvent(&events.Message{
		Info: types.MessageInfo{MessageSource: types.MessageSource{Chat: group, Sender: alice, IsGroup: true}, ID: "E1", Timestamp: ts},
		Message: &waE2E.Message{
			EventMessage:       &waE2E.EventMessage{Name: proto.String("Picnic"), StartTime: proto.Int64(ts.Add(48 * time.Hour).Unix()), ExtraGuestsAllowed: proto.Bool(true)},
			MessageContextInfo: &waE2E.MessageContextInfo{MessageSecret: secret},
		},
	})
	if err := b.client.Store.MsgSecrets.PutMessageSecret(ctx, group, alice, "E1", secret); err != nil {
		t.Fatal(err)
	}
	if e, _ := b.store.GetEvent("E1", group.String()); e == nil || e.Name != "Picnic" || e.SenderJID != alice.String() {
		t.Fatalf("event not stored: %+v", e)
	}

	rsvp := func(id string, from types.JID, fromMe bool, r waE2E.EventResponseMessage_EventResponseType, guests int32, when time.Time) {
		payload, iv := encryptAddOn(t, secretEventResponse, from, "E1", alice, secret,
			&waE2E.EventResponseMessage{Response: r.Enum(), TimestampMS: proto.Int64(when.UnixMilli()), ExtraGuestCount: proto.Int32(guests)})
		b.handleEvent(&events.Message{
			Info: types.MessageInfo{MessageSource: types.MessageSource{Chat: group, Sender: from, IsFromMe: fromMe, IsGroup: true}, ID: id, Timestamp: when},
			Message: &waE2E.Message{EncEventResponseMessage: &waE2E.EncEventResponseMessage{
				EventCreationMessageKey: &waCommon.MessageKey{RemoteJID: proto.String(group.String()), Participant: proto.String(alice.String()), ID: proto.String("E1")},
				EncPayload:              payload, EncIV: iv,
			}},
		})
	}
	rsvp("R1", bob, false, waE2E.EventResponseMessage_GOING, 1, ts.Add(time.Minute))
	rsvp("R2", bob, false, waE2E.EventResponseMessage_MAYBE, 0, ts.Add(2*time.Minute))
	// Our own RSVP encrypted under the phone number but delivered from the LID.
	payload, iv := encryptAddOn(t, secretEventResponse, ownPN, "E1", alice, secret,
		&waE2E.EventResponseMessage{Response: waE2E.EventResponseMessage_NOT_GOING.Enum(), TimestampMS: proto.Int64(ts.Add(3 * time.Minute).UnixMilli())})
	b.handleEvent(&events.Message{
		Info: types.MessageInfo{MessageSource: types.MessageSource{Chat: group, Sender: ownLID, IsFromMe: true, IsGroup: true}, ID: "R3", Timestamp: ts.Add(3 * time.Minute)},
		Message: &waE2E.Message{EncEventResponseMessage: &waE2E.EncEventResponseMessage{
			EventCreationMessageKey: &waCommon.MessageKey{RemoteJID: proto.String(group.String()), Participant: proto.String(alice.String()), ID: proto.String("E1")},
			EncPayload:              payload, EncIV: iv,
		}},
	})

	rs, _ := b.store.EventResponses("E1", group.String())
	if len(rs) != 2 {
		t.Fatalf("responses: %+v", rs)
	}
	if rs[0].Responder != "14155550002" || rs[0].Response != "maybe" || rs[0].ExtraGuests != 0 {
		t.Errorf("bob's response: %+v", rs[0])
	}
	if rs[1].Responder != "14155550000" || rs[1].Response != "not_going" {
		t.Errorf("own response: %+v", rs[1])
	}

	// An edit (cancellation) arriving as an edited EventMessage updates the row and text.
	edit := types.MessageInfo{MessageSource: types.MessageSource{Chat: group, Sender: alice, IsGroup: true}, ID: "E1-EDIT", Timestamp: ts.Add(time.Hour)}
	b.handleEvent(&events.Message{
		Info: edit, IsEdit: true,
		Message:    &waE2E.Message{EventMessage: &waE2E.EventMessage{Name: proto.String("Picnic"), StartTime: proto.Int64(ts.Add(48 * time.Hour).Unix()), IsCanceled: proto.Bool(true)}},
		RawMessage: &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{Type: waE2E.ProtocolMessage_MESSAGE_EDIT.Enum(), Key: &waCommon.MessageKey{ID: proto.String("E1")}}},
	})
	e, _ := b.store.GetEvent("E1", group.String())
	if e == nil || !e.Canceled || e.SenderJID != alice.String() {
		t.Errorf("event after edit: %+v", e)
	}
	if m, _ := b.store.GetMessage("E1", group.String()); m == nil || !strings.Contains(m.Content, "(canceled)") {
		t.Errorf("message after edit: %+v", m)
	}
}

func TestHistorySyncAppliesVotesAfterPolls(t *testing.T) {
	b := newPairedOfflineBridge(t)
	group := "120363000000000009@g.us"
	alice := "14155550001@s.whatsapp.net"
	bob := "14155550002@s.whatsapp.net"
	aliceJID, _ := types.ParseJID(alice)
	bobJID, _ := types.ParseJID(bob)

	poll := pollCreation("Book?", "Dune", "Emma")
	secret := poll.GetMessageContextInfo().GetMessageSecret()
	payload, iv := encryptAddOn(t, "Poll Vote", bobJID, "HP1", aliceJID, secret,
		&waE2E.PollVoteMessage{SelectedOptions: whatsmeow.HashPollOptions([]string{"Dune"})})

	hs := &waHistorySync.HistorySync{
		SyncType: waHistorySync.HistorySync_RECENT.Enum(),
		Conversations: []*waHistorySync.Conversation{{
			ID:   proto.String(group),
			Name: proto.String("Book club"),
			Messages: []*waHistorySync.HistorySyncMsg{
				{Message: &waWeb.WebMessageInfo{ // the vote comes first in the batch
					Key:              &waCommon.MessageKey{ID: proto.String("HV1"), FromMe: proto.Bool(false), Participant: proto.String(bob)},
					MessageTimestamp: proto.Uint64(1743600100),
					Message: &waE2E.Message{PollUpdateMessage: &waE2E.PollUpdateMessage{
						PollCreationMessageKey: &waCommon.MessageKey{RemoteJID: proto.String(group), Participant: proto.String(alice), ID: proto.String("HP1")},
						Vote:                   &waE2E.PollEncValue{EncPayload: payload, EncIV: iv},
					}},
				}},
				{Message: &waWeb.WebMessageInfo{
					Key:              &waCommon.MessageKey{ID: proto.String("HP1"), FromMe: proto.Bool(false), Participant: proto.String(alice)},
					MessageTimestamp: proto.Uint64(1743600000),
					Message:          poll,
					MessageSecret:    secret,
				}},
			},
		}},
	}
	b.handleEvent(&events.HistorySync{Data: hs})

	if p, _ := b.store.GetPoll("HP1", group); p == nil || p.Name != "Book?" {
		t.Fatalf("poll from history: %+v", p)
	}
	votes, _ := b.store.PollVotes("HP1", group)
	if len(votes) != 1 || votes[0].Voter != "14155550002" || votes[0].Selected[0] != "Dune" {
		t.Errorf("votes from history: %+v", votes)
	}
	if m, _ := b.store.GetMessage("HV1", group); m != nil {
		t.Errorf("vote stored as a message")
	}
	if _, msgs, _ := b.store.Counts(); msgs != 1 {
		t.Errorf("messages = %d", msgs)
	}
}

func TestSendPollAndEventValidation(t *testing.T) {
	b := newOfflineBridge(t)
	ctx := context.Background()
	cases := []struct {
		name string
		err  string
		run  func() error
	}{
		{"empty question", "question", func() error { _, err := b.SendPoll(ctx, "123", " ", []string{"a", "b"}, 1); return err }},
		{"one option", "between 2", func() error { _, err := b.SendPoll(ctx, "123", "q", []string{"a", ""}, 1); return err }},
		{"duplicate", "duplicate", func() error { _, err := b.SendPoll(ctx, "123", "q", []string{"a", "a"}, 1); return err }},
		{"selectable", "selectable_count", func() error { _, err := b.SendPoll(ctx, "123", "q", []string{"a", "b"}, 3); return err }},
		{"not connected", "not connected", func() error { _, err := b.SendPoll(ctx, "14155550009", "q", []string{"a", "b"}, 1); return err }},
		{"event name", "name", func() error { _, err := b.SendEvent(ctx, "123", EventDraft{}); return err }},
		{"event start", "start_time", func() error { _, err := b.SendEvent(ctx, "123", EventDraft{Name: "x"}); return err }},
		{"event end", "end_time", func() error {
			_, err := b.SendEvent(ctx, "123", EventDraft{Name: "x", StartTime: time.Now(), EndTime: time.Now().Add(-time.Hour)})
			return err
		}},
		{"bad response", "going", func() error { _, err := b.RespondToEvent(ctx, "123", "E", "dunno", 0); return err }},
		{"vote not connected", "not connected", func() error { _, err := b.VotePoll(ctx, "14155550009", "P", nil); return err }},
	}
	for _, c := range cases {
		err := c.run()
		if err == nil || !strings.Contains(err.Error(), c.err) {
			t.Errorf("%s: err = %v, want containing %q", c.name, err, c.err)
		}
	}
	b.cfg.ReadOnly = true
	if _, err := b.SendPoll(ctx, "123", "q", []string{"a", "b"}, 1); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Errorf("read-only poll: %v", err)
	}
	if _, err := b.RespondToEvent(ctx, "123", "E", "going", 0); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Errorf("read-only rsvp: %v", err)
	}
}

func TestParseAPITime(t *testing.T) {
	got, err := parseAPITime("2026-09-20T18:00:00-07:00", "start_time")
	if err != nil || !got.Equal(time.Date(2026, 9, 21, 1, 0, 0, 0, time.UTC)) {
		t.Errorf("rfc3339: %v %v", got, err)
	}
	if got, err := parseAPITime("2026-09-20T18:00", "start_time"); err != nil || got.UTC().Hour() != 18 {
		t.Errorf("bare: %v %v", got, err)
	}
	if got, err := parseAPITime("", "end_time"); err != nil || !got.IsZero() {
		t.Errorf("empty: %v %v", got, err)
	}
	if _, err := parseAPITime("tomorrow", "start_time"); err == nil || !strings.Contains(err.Error(), "start_time") {
		t.Errorf("garbage: %v", err)
	}
}

func TestPollAndEventEndpoints(t *testing.T) {
	fake := &fakeMessenger{}
	h := newAPIHandler(fake)

	code, out := doJSON(t, h, "POST", "/api/poll", `{"recipient":" 123 ","question":"q","options":["a","b"],"selectable_count":1}`)
	if code != 200 || !out.Success || out.MessageID != "POLL1" || len(fake.polls) != 1 || fake.polls[0].Recipient != "123" {
		t.Errorf("poll: %d %+v %+v", code, out, fake.polls)
	}
	if code, out = doJSON(t, h, "POST", "/api/poll", `{"question":"q","options":["a","b"]}`); code != 400 || !strings.Contains(out.Message, "recipient") {
		t.Errorf("poll without recipient: %d %+v", code, out)
	}

	code, out = doJSON(t, h, "POST", "/api/poll/vote", `{"chat_jid":"1@g.us","poll_id":"P1","options":["a"]}`)
	if code != 200 || out.MessageID != "VOTE1" || len(fake.votes) != 1 || fake.votes[0].Options[0] != "a" {
		t.Errorf("vote: %d %+v", code, out)
	}
	if code, _ = doJSON(t, h, "POST", "/api/poll/vote", `{"chat_jid":"1@g.us"}`); code != 400 {
		t.Errorf("vote without poll id: %d", code)
	}

	body := `{"recipient":"1@g.us","name":"Picnic","start_time":"2026-09-20T18:00:00Z","end_time":"2026-09-20T20:00:00Z",` +
		`"location_name":"Park","latitude":37.76,"longitude":-122.43,"extra_guests_allowed":true}`
	code, out = doJSON(t, h, "POST", "/api/event", body)
	if code != 200 || out.MessageID != "EVT1" || len(fake.events) != 1 {
		t.Fatalf("event: %d %+v", code, out)
	}
	ev := fake.events[0]
	if ev.Name != "Picnic" || ev.StartTime.UTC().Hour() != 18 || ev.EndTime.IsZero() || ev.Latitude == nil || *ev.Latitude != 37.76 || !ev.ExtraGuestsAllowed {
		t.Errorf("event draft: %+v", ev)
	}
	if code, out = doJSON(t, h, "POST", "/api/event", `{"recipient":"1@g.us","name":"x","start_time":"soon"}`); code != 400 || !strings.Contains(out.Message, "start_time") {
		t.Errorf("event with bad time: %d %+v", code, out)
	}

	code, out = doJSON(t, h, "POST", "/api/event/respond", `{"chat_jid":"1@g.us","event_id":"E1","response":"going","extra_guests":2}`)
	if code != 200 || out.MessageID != "RSVP1" || len(fake.responses) != 1 || fake.responses[0].ExtraGuests != 2 {
		t.Errorf("respond: %d %+v", code, out)
	}
	if code, _ = doJSON(t, h, "GET", "/api/event/respond", ""); code != 405 {
		t.Errorf("GET respond: %d", code)
	}

	fake.sendErr = badRequest("nope")
	if code, _ = doJSON(t, h, "POST", "/api/poll", `{"recipient":"1","question":"q","options":["a","b"]}`); code != 400 {
		t.Errorf("messenger bad request: %d", code)
	}
}
