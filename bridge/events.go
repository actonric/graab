package main

import (
	"context"
	"fmt"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
)

// Bridge glues the whatsmeow client to the message store.
type Bridge struct {
	client   *whatsmeow.Client
	store    *Store
	storeDir string
	log      waLog.Logger
}

func (b *Bridge) ownUser() string {
	if b.client.Store.ID == nil {
		return ""
	}
	return b.client.Store.ID.User
}

// handleEvent is registered with the client and dispatches every event.
func (b *Bridge) handleEvent(evt any) {
	ctx := context.Background()
	switch v := evt.(type) {
	case *events.Message:
		b.handleMessage(ctx, v)
	case *events.HistorySync:
		b.handleHistorySync(ctx, v)
	case *events.PushName:
		jid := b.phoneJID(ctx, v.JID)
		_ = b.store.UpsertContact(jid.ToNonAD().String(), phoneOf(jid), "", v.NewPushName)
	case *events.Contact:
		jid := b.phoneJID(ctx, v.JID)
		name := v.Action.GetFullName()
		if name == "" {
			name = v.Action.GetFirstName()
		}
		key := jid.ToNonAD().String()
		_ = b.store.UpsertContact(key, phoneOf(jid), name, "")
		_ = b.store.RenameChatIfDefault(key, name)
	case *events.AppStateSyncComplete:
		if v.Name == appstate.WAPatchCriticalUnblockLow {
			b.refreshContacts(ctx)
		}
	case *events.OfflineSyncCompleted:
		b.log.Infof("Offline sync completed")
		b.refreshContacts(ctx)
		b.refreshGroups(ctx)
	case *events.Connected:
		b.log.Infof("Connected to WhatsApp")
	case *events.Disconnected:
		b.log.Warnf("Disconnected from WhatsApp; whatsmeow will reconnect automatically")
	case *events.LoggedOut:
		b.log.Errorf("Logged out of WhatsApp (reason %v). Delete the store and restart to pair again.", v.Reason)
	case *events.StreamReplaced:
		b.log.Errorf("Stream replaced: another instance of this bridge connected with the same session")
	}
}

// phoneJID maps a LID (privacy) address to the phone-number JID when known.
func (b *Bridge) phoneJID(ctx context.Context, jid types.JID) types.JID {
	if jid.Server != types.HiddenUserServer {
		return jid
	}
	if lids := b.client.Store.LIDs; lids != nil {
		if pn, err := lids.GetPNForLID(ctx, jid); err == nil && !pn.IsEmpty() {
			return pn
		}
	}
	return jid
}

func phoneOf(jid types.JID) string {
	if jid.Server == types.DefaultUserServer {
		return jid.User
	}
	return ""
}

// canonicalChat picks the chat JID we key on: phone-number JIDs for direct
// chats even when the event arrived addressed by LID.
func (b *Bridge) canonicalChat(ctx context.Context, info types.MessageInfo) types.JID {
	chat := info.Chat
	if chat.Server == types.HiddenUserServer {
		switch {
		case info.IsFromMe && !info.RecipientAlt.IsEmpty():
			chat = info.RecipientAlt
		case !info.IsFromMe && !info.SenderAlt.IsEmpty():
			chat = info.SenderAlt
		default:
			chat = b.phoneJID(ctx, chat)
		}
	}
	return chat.ToNonAD()
}

// senderJID returns the phone-number JID of a message's sender when known.
func (b *Bridge) senderJID(ctx context.Context, info types.MessageInfo) types.JID {
	if info.Sender.Server == types.HiddenUserServer && !info.SenderAlt.IsEmpty() {
		return info.SenderAlt.ToNonAD()
	}
	return b.phoneJID(ctx, info.Sender).ToNonAD()
}

// chatName resolves a display name: existing store name, then contacts /
// group metadata, then the push name, then the bare identifier.
func (b *Bridge) chatName(ctx context.Context, jid types.JID, pushName string, isGroup bool) string {
	key := jid.String()
	if c, err := b.store.GetChat(key); err == nil && c != nil && c.Name != "" && c.Name != jid.User {
		return c.Name
	}
	if isGroup {
		if gi, err := b.client.GetGroupInfo(ctx, jid); err == nil && gi.Name != "" {
			return gi.Name
		}
		return "Group " + jid.User
	}
	if name := b.store.ContactName(key); name != "" {
		return name
	}
	if contacts := b.client.Store.Contacts; contacts != nil {
		if ci, err := contacts.GetContact(ctx, jid); err == nil && ci.Found {
			switch {
			case ci.FullName != "":
				return ci.FullName
			case ci.FirstName != "":
				return ci.FirstName
			case ci.BusinessName != "":
				return ci.BusinessName
			case ci.PushName != "":
				return ci.PushName
			}
		}
	}
	if pushName != "" {
		return pushName
	}
	return jid.User
}

// handleMessage stores a live message.
func (b *Bridge) handleMessage(ctx context.Context, evt *events.Message) {
	info := evt.Info
	chat := b.canonicalChat(ctx, info)
	sender := b.senderJID(ctx, info)

	if !info.IsFromMe && info.PushName != "" {
		_ = b.store.UpsertContact(sender.String(), phoneOf(sender), "", info.PushName)
	}

	// Edits arrive as a ProtocolMessage wrapping the new content and the
	// original key; whatsmeow unwraps the content but marks the event.
	if evt.IsEdit {
		if content, _, _, ok := describeMessage(info.ID, evt.Message); ok {
			targetID := info.ID
			if pm := evt.RawMessage.GetProtocolMessage(); pm != nil && pm.GetKey().GetID() != "" {
				targetID = pm.GetKey().GetID()
			}
			if updated, _ := b.store.UpdateMessageContent(targetID, chat.String(), content); updated {
				b.log.Infof("[%s] edited message %s in %s", info.Timestamp.Format(time.DateTime), targetID, chat)
				return
			}
		}
	}
	if pm := evt.Message.GetProtocolMessage(); pm != nil && pm.GetType() == waE2E.ProtocolMessage_REVOKE {
		if id := pm.GetKey().GetID(); id != "" {
			_, _ = b.store.UpdateMessageContent(id, chat.String(), "[message deleted]")
		}
		return
	}

	content, media, quoted, ok := describeMessage(info.ID, evt.Message)
	if !ok {
		return
	}

	pushName := ""
	if !info.IsFromMe {
		pushName = info.PushName
	}
	name := b.chatName(ctx, chat, pushName, info.IsGroup)
	if err := b.store.UpsertChat(chat.String(), name, info.IsGroup, info.Timestamp); err != nil {
		b.log.Warnf("store chat: %v", err)
	}
	err := b.store.InsertMessage(&StoredMessage{
		ID: info.ID, ChatJID: chat.String(), Sender: sender.User, Content: content,
		Timestamp: info.Timestamp, IsFromMe: info.IsFromMe, Media: media, QuotedID: quoted,
	})
	if err != nil {
		b.log.Warnf("store message: %v", err)
		return
	}
	b.logMessage(info.Timestamp, info.IsFromMe, name, sender.User, content, media)
}

func (b *Bridge) logMessage(ts time.Time, fromMe bool, chatName, sender, content string, media *MediaInfo) {
	dir := "←"
	if fromMe {
		dir = "→"
	}
	if media != nil {
		fmt.Printf("[%s] %s %s (%s): [%s %s] %s\n", ts.Local().Format(time.DateTime), dir, chatName, sender, media.Type, media.Filename, content)
	} else {
		fmt.Printf("[%s] %s %s (%s): %s\n", ts.Local().Format(time.DateTime), dir, chatName, sender, content)
	}
}

// handleHistorySync imports the conversation backlog the phone pushes after
// pairing (and periodically afterwards).
func (b *Bridge) handleHistorySync(ctx context.Context, evt *events.HistorySync) {
	data := evt.Data
	b.log.Infof("History sync (%s): %d conversations, %d push names",
		data.GetSyncType(), len(data.GetConversations()), len(data.GetPushnames()))

	for _, pn := range data.GetPushnames() {
		if jid, err := types.ParseJID(pn.GetID()); err == nil {
			jid = b.phoneJID(ctx, jid).ToNonAD()
			_ = b.store.UpsertContact(jid.String(), phoneOf(jid), "", pn.GetPushname())
		}
	}

	stored := 0
	for _, conv := range data.GetConversations() {
		if conv.GetID() == "" {
			continue
		}
		jid, err := types.ParseJID(conv.GetID())
		if err != nil {
			b.log.Warnf("history sync: bad JID %q: %v", conv.GetID(), err)
			continue
		}
		if jid.Server == types.HiddenUserServer && conv.GetPnJID() != "" {
			if pn, err := types.ParseJID(conv.GetPnJID()); err == nil {
				jid = pn
			}
		}
		jid = b.phoneJID(ctx, jid).ToNonAD()
		if jid.Server == types.BroadcastServer || jid.Server == types.NewsletterServer {
			continue
		}
		isGroup := jid.Server == types.GroupServer

		name := conv.GetDisplayName()
		if name == "" {
			name = conv.GetName()
		}
		if name == "" {
			name = b.chatName(ctx, jid, "", isGroup)
		}

		var last time.Time
		if ts := conv.GetLastMsgTimestamp(); ts != 0 {
			last = time.Unix(int64(ts), 0)
		}

		for _, hm := range conv.GetMessages() {
			wmi := hm.GetMessage()
			if wmi == nil || wmi.GetMessage() == nil || wmi.GetKey() == nil {
				continue
			}
			key := wmi.GetKey()
			id := key.GetID()
			ts := wmi.GetMessageTimestamp()
			if id == "" || ts == 0 {
				continue
			}
			when := time.Unix(int64(ts), 0)
			fromMe := key.GetFromMe()

			sender := jid.User
			if fromMe {
				sender = b.ownUser()
			} else if p := key.GetParticipant(); p != "" {
				if pj, err := types.ParseJID(p); err == nil {
					sender = b.phoneJID(ctx, pj).User
				}
			} else if p := wmi.GetParticipant(); p != "" {
				if pj, err := types.ParseJID(p); err == nil {
					sender = b.phoneJID(ctx, pj).User
				}
			}
			if !fromMe && wmi.GetPushName() != "" {
				sj := types.NewJID(sender, types.DefaultUserServer)
				_ = b.store.UpsertContact(sj.String(), sender, "", wmi.GetPushName())
			}

			content, media, quoted, ok := describeMessage(id, wmi.GetMessage())
			if !ok {
				continue
			}
			if when.After(last) {
				last = when
			}
			if err := b.store.UpsertChat(jid.String(), name, isGroup, when); err != nil {
				b.log.Warnf("history sync: store chat: %v", err)
				break
			}
			if err := b.store.InsertMessage(&StoredMessage{
				ID: id, ChatJID: jid.String(), Sender: sender, Content: content,
				Timestamp: when, IsFromMe: fromMe, Media: media, QuotedID: quoted,
			}); err != nil {
				b.log.Warnf("history sync: store message: %v", err)
				continue
			}
			stored++
		}
		if !last.IsZero() {
			_ = b.store.UpsertChat(jid.String(), name, isGroup, last)
		}
	}
	b.log.Infof("History sync complete: stored %d messages", stored)
}

// refreshContacts copies the address book whatsmeow has synced into our
// contacts table and upgrades bare-number chat names.
func (b *Bridge) refreshContacts(ctx context.Context) {
	contacts := b.client.Store.Contacts
	if contacts == nil {
		return
	}
	all, err := contacts.GetAllContacts(ctx)
	if err != nil {
		b.log.Warnf("load contacts: %v", err)
		return
	}
	n := 0
	for jid, ci := range all {
		jid = b.phoneJID(ctx, jid).ToNonAD()
		name := ci.FullName
		if name == "" {
			name = ci.FirstName
		}
		if name == "" {
			name = ci.BusinessName
		}
		key := jid.String()
		if err := b.store.UpsertContact(key, phoneOf(jid), name, ci.PushName); err == nil {
			n++
		}
		if name != "" {
			_ = b.store.RenameChatIfDefault(key, name)
		}
	}
	b.log.Infof("Synced %d contacts", n)
}

// refreshGroups makes sure every joined group has a row with its real name.
func (b *Bridge) refreshGroups(ctx context.Context) {
	groups, err := b.client.GetJoinedGroups(ctx)
	if err != nil {
		b.log.Warnf("load groups: %v", err)
		return
	}
	for _, g := range groups {
		_ = b.store.UpsertChat(g.JID.String(), g.Name, true, time.Time{})
	}
	b.log.Infof("Synced %d groups", len(groups))
}
