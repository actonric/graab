package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.mau.fi/util/random"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"go.mau.fi/whatsmeow/util/gcmutil"
	"go.mau.fi/whatsmeow/util/hkdfutil"
	"google.golang.org/protobuf/proto"
)

// Calendar events. An EventMessage carries the details in the clear; RSVPs
// arrive as EncEventResponseMessage, encrypted with the same per-message
// secret scheme as poll votes. whatsmeow has no public helper for event
// responses, so the key derivation is mirrored here (see msgsecret.go in
// whatsmeow: generateMsgSecretKey and decryptMsgSecret).

const (
	responseGoing       = "going"
	responseNotGoing    = "not_going"
	responseMaybe       = "maybe"
	secretEventResponse = "Event Response"
)

// EventDraft is what a caller supplies to create an event.
type EventDraft struct {
	Name               string
	Description        string
	StartTime          time.Time
	EndTime            time.Time
	LocationName       string
	LocationAddress    string
	Latitude           *float64
	Longitude          *float64
	JoinLink           string
	ExtraGuestsAllowed bool
}

// eventFromProto copies the event fields out of the protobuf (ids unset).
func eventFromProto(em *waE2E.EventMessage) StoredEvent {
	e := StoredEvent{
		Name: em.GetName(), Description: em.GetDescription(), JoinLink: em.GetJoinLink(),
		Canceled: em.GetIsCanceled(), ExtraGuestsAllowed: em.GetExtraGuestsAllowed(),
	}
	if ts := em.GetStartTime(); ts > 0 {
		e.StartTime = time.Unix(ts, 0)
	}
	if ts := em.GetEndTime(); ts > 0 {
		e.EndTime = time.Unix(ts, 0)
	}
	if loc := em.GetLocation(); loc != nil {
		e.LocationName, e.LocationAddress = loc.GetName(), loc.GetAddress()
		if loc.DegreesLatitude != nil && loc.DegreesLongitude != nil {
			lat, lon := loc.GetDegreesLatitude(), loc.GetDegreesLongitude()
			e.Latitude, e.Longitude = &lat, &lon
		}
	}
	return e
}

// describeEvent is the text stored in the messages table for an event.
func describeEvent(e *StoredEvent) string {
	var b strings.Builder
	b.WriteString("[event] " + e.Name)
	if e.Canceled {
		b.WriteString(" (canceled)")
	}
	if !e.StartTime.IsZero() {
		b.WriteString("\nWhen: " + e.StartTime.UTC().Format(timeLayout))
		if !e.EndTime.IsZero() {
			b.WriteString(" to " + e.EndTime.UTC().Format(timeLayout))
		}
	}
	where := e.LocationName
	if e.LocationAddress != "" {
		if where != "" {
			where += ", "
		}
		where += e.LocationAddress
	}
	if where != "" {
		b.WriteString("\nWhere: " + where)
	}
	if e.JoinLink != "" {
		b.WriteString("\nLink: " + e.JoinLink)
	}
	if e.Description != "" {
		b.WriteString("\n" + e.Description)
	}
	return b.String()
}

// storeEvent records the event details for a stored message.
func (b *Bridge) storeEvent(id, chatJID, senderJID string, fromMe bool, em *waE2E.EventMessage) {
	e := eventFromProto(em)
	e.MessageID, e.ChatJID, e.SenderJID, e.IsFromMe = id, chatJID, senderJID, fromMe
	if err := b.store.UpsertEvent(&e); err != nil {
		b.log.Warnf("store event %s: %v", id, err)
	}
}

func responseName(r waE2E.EventResponseMessage_EventResponseType) string {
	switch r {
	case waE2E.EventResponseMessage_GOING:
		return responseGoing
	case waE2E.EventResponseMessage_NOT_GOING:
		return responseNotGoing
	case waE2E.EventResponseMessage_MAYBE:
		return responseMaybe
	}
	return ""
}

func responseType(name string) (waE2E.EventResponseMessage_EventResponseType, bool) {
	switch strings.ToLower(strings.TrimSpace(strings.ReplaceAll(name, " ", "_"))) {
	case responseGoing, "yes":
		return waE2E.EventResponseMessage_GOING, true
	case responseNotGoing, "no", "not-going", "notgoing":
		return waE2E.EventResponseMessage_NOT_GOING, true
	case responseMaybe:
		return waE2E.EventResponseMessage_MAYBE, true
	}
	return waE2E.EventResponseMessage_UNKNOWN, false
}

// handleEventResponse decrypts an RSVP and records it.
func (b *Bridge) handleEventResponse(ctx context.Context, evt *events.Message, chat, responder types.JID) {
	enc := evt.Message.GetEncEventResponseMessage()
	eventID := enc.GetEventCreationMessageKey().GetID()
	if ev, err := b.store.GetEvent(eventID, chat.String()); err != nil || ev == nil {
		b.log.Debugf("response to unknown event %s in %s ignored", eventID, chat)
		return
	}
	resp, err := b.decryptEventResponse(ctx, evt)
	if err != nil {
		b.log.Warnf("decrypt response to event %s in %s: %v", eventID, chat, err)
		return
	}
	name := responseName(resp.GetResponse())
	if name == "" {
		return
	}
	ts := evt.Info.Timestamp
	if ms := resp.GetTimestampMS(); ms > 0 {
		ts = time.UnixMilli(ms)
	}
	err = b.store.UpsertEventResponse(&EventResponse{
		EventID: eventID, ChatJID: chat.String(), Responder: responder.User,
		Response: name, ExtraGuests: int(resp.GetExtraGuestCount()), Timestamp: ts,
	})
	if err != nil {
		b.log.Warnf("store response to event %s: %v", eventID, err)
	}
}

// msgSecretKey derives the AES-GCM key and additional data for a message
// add-on (vote, RSVP) from the original message's secret. Mirrors whatsmeow.
func msgSecretKey(useCase string, modSender types.JID, origID string, origSender types.JID, secret []byte) (key, additionalData []byte) {
	origStr := origSender.ToNonAD().String()
	modStr := modSender.ToNonAD().String()
	info := make([]byte, 0, len(origID)+len(origStr)+len(modStr)+len(useCase))
	info = append(info, origID...)
	info = append(info, origStr...)
	info = append(info, modStr...)
	info = append(info, useCase...)
	key = hkdfutil.SHA256(secret, nil, info, 32)
	additionalData = fmt.Appendf(nil, "%s\x00%s", origID, modStr)
	return key, additionalData
}

// origSenderFromKey works out who sent the original message an add-on refers
// to, from the add-on's message key. Mirrors whatsmeow.
func origSenderFromKey(evt *events.Message, key *waCommon.MessageKey) (types.JID, error) {
	if key.GetFromMe() {
		return evt.Info.Sender, nil
	}
	if evt.Info.Chat.Server == types.DefaultUserServer || evt.Info.Chat.Server == types.HiddenUserServer {
		return types.ParseJID(key.GetRemoteJID())
	}
	sender, err := types.ParseJID(key.GetParticipant())
	if err == nil && sender.Server != types.DefaultUserServer && sender.Server != types.HiddenUserServer {
		err = fmt.Errorf("unexpected server in participant %q", key.GetParticipant())
	}
	return sender, err
}

// decryptEventResponse decrypts an RSVP using the event's stored secret.
func (b *Bridge) decryptEventResponse(ctx context.Context, evt *events.Message) (*waE2E.EventResponseMessage, error) {
	enc := evt.Message.GetEncEventResponseMessage()
	key := enc.GetEventCreationMessageKey()
	origSender, err := origSenderFromKey(evt, key)
	if err != nil {
		return nil, err
	}
	if b.client.Store.MsgSecrets == nil {
		return nil, fmt.Errorf("not paired yet: no message secret store")
	}
	secret, storedSender, err := b.client.Store.MsgSecrets.GetMessageSecret(ctx, evt.Info.Chat, origSender, key.GetID())
	if err != nil {
		return nil, fmt.Errorf("look up event secret: %w", err)
	}
	if secret == nil {
		return nil, fmt.Errorf("no secret stored for event %s", key.GetID())
	}
	senders := []types.JID{evt.Info.Sender}
	if evt.Info.IsFromMe {
		if alt := b.otherOwnJID(evt.Info.Sender); !alt.IsEmpty() {
			senders = append(senders, alt)
		}
	}
	var lastErr error
	for _, modSender := range senders {
		for _, orig := range []types.JID{origSender, storedSender} {
			if orig.IsEmpty() {
				continue
			}
			k, ad := msgSecretKey(secretEventResponse, modSender, key.GetID(), orig, secret)
			plaintext, err := gcmutil.Decrypt(k, enc.GetEncIV(), enc.GetEncPayload(), ad)
			if err != nil {
				lastErr = err
				continue
			}
			var msg waE2E.EventResponseMessage
			if err := proto.Unmarshal(plaintext, &msg); err != nil {
				return nil, fmt.Errorf("decode event response: %w", err)
			}
			return &msg, nil
		}
	}
	return nil, lastErr
}

// SendEvent creates a calendar event in a chat.
func (b *Bridge) SendEvent(ctx context.Context, recipient string, d EventDraft) (SendResult, error) {
	d.Name = strings.TrimSpace(d.Name)
	if d.Name == "" {
		return SendResult{}, badRequest("event name is required")
	}
	if d.StartTime.IsZero() {
		return SendResult{}, badRequest("event start_time is required")
	}
	if !d.EndTime.IsZero() && d.EndTime.Before(d.StartTime) {
		return SendResult{}, badRequest("event end_time is before start_time")
	}
	if (d.Latitude == nil) != (d.Longitude == nil) {
		return SendResult{}, badRequest("latitude and longitude must be given together")
	}
	to, err := b.prepareSend(recipient)
	if err != nil {
		return SendResult{}, err
	}
	em := &waE2E.EventMessage{
		Name: proto.String(d.Name), StartTime: proto.Int64(d.StartTime.Unix()),
		IsCanceled: proto.Bool(false), ExtraGuestsAllowed: proto.Bool(d.ExtraGuestsAllowed),
	}
	if d.Description != "" {
		em.Description = proto.String(d.Description)
	}
	if !d.EndTime.IsZero() {
		em.EndTime = proto.Int64(d.EndTime.Unix())
	}
	if d.JoinLink != "" {
		em.JoinLink = proto.String(d.JoinLink)
	}
	if d.LocationName != "" || d.LocationAddress != "" || d.Latitude != nil {
		loc := &waE2E.LocationMessage{}
		if d.LocationName != "" {
			loc.Name = proto.String(d.LocationName)
		}
		if d.LocationAddress != "" {
			loc.Address = proto.String(d.LocationAddress)
		}
		if d.Latitude != nil {
			loc.DegreesLatitude, loc.DegreesLongitude = proto.Float64(*d.Latitude), proto.Float64(*d.Longitude)
		}
		em.Location = loc
	}
	msg := &waE2E.Message{
		EventMessage:       em,
		MessageContextInfo: &waE2E.MessageContextInfo{MessageSecret: random.Bytes(32)},
	}
	resp, err := b.client.SendMessage(ctx, to, msg)
	if err != nil {
		return SendResult{}, fmt.Errorf("send event: %w", err)
	}
	e := eventFromProto(em)
	res := b.recordSent(ctx, to, resp, describeEvent(&e), nil)
	e.MessageID, e.ChatJID, e.SenderJID, e.IsFromMe = resp.ID, res.Recipient, resp.Sender.String(), true
	if err := b.store.UpsertEvent(&e); err != nil {
		b.log.Warnf("store sent event: %v", err)
	}
	return res, nil
}

// RespondToEvent sends our RSVP (going, not_going or maybe) to a stored event.
func (b *Bridge) RespondToEvent(ctx context.Context, chatJID, eventID, response string, extraGuests int) (SendResult, error) {
	rt, ok := responseType(response)
	if !ok {
		return SendResult{}, badRequest("response must be going, not_going or maybe")
	}
	if extraGuests < 0 {
		return SendResult{}, badRequest("extra_guests cannot be negative")
	}
	chat, err := b.prepareSend(chatJID)
	if err != nil {
		return SendResult{}, err
	}
	ev, err := b.store.GetEvent(eventID, chat.ToNonAD().String())
	if err != nil {
		return SendResult{}, err
	}
	if ev == nil {
		return SendResult{}, badRequest(fmt.Sprintf("no event with id %s in %s", eventID, chat.ToNonAD()))
	}
	if ev.Canceled {
		return SendResult{}, badRequest("this event has been canceled")
	}
	if extraGuests > 0 && !ev.ExtraGuestsAllowed {
		return SendResult{}, badRequest("this event does not allow extra guests")
	}
	creator, _ := types.ParseJID(ev.SenderJID)
	if creator.IsEmpty() {
		if ev.IsFromMe {
			creator = b.client.Store.GetJID()
		} else {
			creator = chat
		}
	}
	plaintext, err := proto.Marshal(&waE2E.EventResponseMessage{
		Response: rt.Enum(), TimestampMS: proto.Int64(time.Now().UnixMilli()), ExtraGuestCount: proto.Int32(int32(extraGuests)),
	})
	if err != nil {
		return SendResult{}, err
	}
	ownID := b.client.Store.GetLID()
	if creator.Server == types.DefaultUserServer || ownID.IsEmpty() {
		ownID = b.client.Store.GetJID()
	}
	if b.client.Store.MsgSecrets == nil {
		return SendResult{}, fmt.Errorf("not paired yet: no message secret store")
	}
	secret, origSender, err := b.client.Store.MsgSecrets.GetMessageSecret(ctx, chat, creator, eventID)
	if err != nil {
		return SendResult{}, fmt.Errorf("look up event secret: %w", err)
	}
	if secret == nil {
		return SendResult{}, badRequest("the secret key for this event is not available, so it cannot be answered from here")
	}
	key, ad := msgSecretKey(secretEventResponse, ownID, eventID, origSender, secret)
	iv := random.Bytes(12)
	ciphertext, err := gcmutil.Encrypt(key, iv, plaintext, ad)
	if err != nil {
		return SendResult{}, fmt.Errorf("encrypt event response: %w", err)
	}
	msgKey := &waCommon.MessageKey{
		RemoteJID: proto.String(chat.String()), FromMe: proto.Bool(ev.IsFromMe), ID: proto.String(eventID),
	}
	if chat.Server == types.GroupServer {
		msgKey.Participant = proto.String(creator.ToNonAD().String())
	}
	msg := &waE2E.Message{EncEventResponseMessage: &waE2E.EncEventResponseMessage{
		EventCreationMessageKey: msgKey, EncPayload: ciphertext, EncIV: iv,
	}}
	resp, err := b.client.SendMessage(ctx, chat, msg)
	if err != nil {
		return SendResult{}, fmt.Errorf("send event response: %w", err)
	}
	if err := b.store.UpsertEventResponse(&EventResponse{
		EventID: eventID, ChatJID: chat.ToNonAD().String(), Responder: b.ownUser(),
		Response: responseName(rt), ExtraGuests: extraGuests, Timestamp: resp.Timestamp,
	}); err != nil {
		b.log.Warnf("store own event response: %v", err)
	}
	return SendResult{MessageID: resp.ID, Recipient: chat.ToNonAD().String(), Timestamp: resp.Timestamp}, nil
}
