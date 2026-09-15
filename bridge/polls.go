package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// Polls. WhatsApp sends a poll as a PollCreationMessage (several protobuf
// versions exist) and each vote as a PollUpdateMessage whose payload is
// encrypted with a secret the creator attached to the poll. whatsmeow keeps
// those secrets and decrypts votes for us; the decrypted vote lists SHA-256
// hashes of the chosen option names, which we map back to the names.

const maxPollOptions = 12

// pollMessage returns the poll payload from any of its protobuf versions.
func pollMessage(msg *waE2E.Message) *waE2E.PollCreationMessage {
	msg = unwrap(msg)
	switch {
	case msg == nil:
		return nil
	case msg.GetPollCreationMessage() != nil:
		return msg.GetPollCreationMessage()
	case msg.GetPollCreationMessageV2() != nil:
		return msg.GetPollCreationMessageV2()
	case msg.GetPollCreationMessageV3() != nil:
		return msg.GetPollCreationMessageV3()
	case msg.GetPollCreationMessageV4() != nil:
		return pollMessage(msg.GetPollCreationMessageV4().GetMessage())
	case msg.GetPollCreationMessageV5() != nil:
		return msg.GetPollCreationMessageV5()
	case msg.GetPollCreationMessageV6() != nil:
		return msg.GetPollCreationMessageV6()
	}
	return nil
}

func pollOptionNames(pm *waE2E.PollCreationMessage) []string {
	out := make([]string, 0, len(pm.GetOptions()))
	for _, o := range pm.GetOptions() {
		out = append(out, o.GetOptionName())
	}
	return out
}

// describePoll is the text stored in the messages table for a poll.
func describePoll(name string, options []string) string {
	var b strings.Builder
	b.WriteString("[poll] " + name)
	for _, o := range options {
		b.WriteString("\n• " + o)
	}
	return b.String()
}

func hashOption(name string) string {
	sum := sha256.Sum256([]byte(name))
	return hex.EncodeToString(sum[:])
}

// selectedOptionNames maps the hashes in a decrypted vote to option names.
// Unknown hashes (an option added after we stored the poll) are kept as
// "?<hash prefix>" so the vote is not silently lost.
func selectedOptionNames(options []string, hashes [][]byte) []string {
	byHash := make(map[string]string, len(options))
	for _, o := range options {
		byHash[hashOption(o)] = o
	}
	out := make([]string, 0, len(hashes))
	for _, h := range hashes {
		key := hex.EncodeToString(h)
		if name, ok := byHash[key]; ok {
			out = append(out, name)
		} else if len(key) >= 8 {
			out = append(out, "?"+key[:8])
		}
	}
	return out
}

// storePoll records the poll details for a stored message.
func (b *Bridge) storePoll(id, chatJID, senderJID string, fromMe bool, pm *waE2E.PollCreationMessage) {
	err := b.store.UpsertPoll(&StoredPoll{
		MessageID: id, ChatJID: chatJID, SenderJID: senderJID, IsFromMe: fromMe,
		Name: pm.GetName(), Options: pollOptionNames(pm), Selectable: int(pm.GetSelectableOptionsCount()),
	})
	if err != nil {
		b.log.Warnf("store poll %s: %v", id, err)
	}
}

// handlePollVote decrypts a vote and records the voter's current selection.
// chat is the canonical chat JID we key on; voter is the phone-number JID.
func (b *Bridge) handlePollVote(ctx context.Context, evt *events.Message, chat, voter types.JID) {
	pu := evt.Message.GetPollUpdateMessage()
	pollID := pu.GetPollCreationMessageKey().GetID()
	poll, err := b.store.GetPoll(pollID, chat.String())
	if err != nil || poll == nil {
		b.log.Debugf("vote for unknown poll %s in %s ignored", pollID, chat)
		return
	}
	vote, err := b.decryptPollVote(ctx, evt)
	if err != nil {
		b.log.Warnf("decrypt vote for poll %s in %s: %v", pollID, chat, err)
		return
	}
	ts := evt.Info.Timestamp
	if ms := pu.GetSenderTimestampMS(); ms > 0 {
		ts = time.UnixMilli(ms)
	}
	err = b.store.UpsertPollVote(&PollVote{
		PollID: pollID, ChatJID: chat.String(), Voter: voter.User,
		Selected: selectedOptionNames(poll.Options, vote.GetSelectedOptions()), Timestamp: ts,
	})
	if err != nil {
		b.log.Warnf("store vote for poll %s: %v", pollID, err)
	}
}

// decryptPollVote decrypts a vote, retrying under our other address for our
// own votes (the phone may have used either the phone-number or LID form).
func (b *Bridge) decryptPollVote(ctx context.Context, evt *events.Message) (*waE2E.PollVoteMessage, error) {
	if b.client.Store.MsgSecrets == nil {
		return nil, fmt.Errorf("not paired yet: no message secret store")
	}
	vote, err := b.client.DecryptPollVote(ctx, evt)
	if err == nil || !evt.Info.IsFromMe {
		return vote, err
	}
	if alt := b.otherOwnJID(evt.Info.Sender); !alt.IsEmpty() {
		retry := *evt
		retry.Info.Sender = alt
		if vote2, err2 := b.client.DecryptPollVote(ctx, &retry); err2 == nil {
			return vote2, nil
		}
	}
	return vote, err
}

// otherOwnJID returns our LID when given our phone-number JID and vice versa.
func (b *Bridge) otherOwnJID(jid types.JID) types.JID {
	pn, lid := b.client.Store.GetJID(), b.client.Store.GetLID()
	if jid.Server == types.HiddenUserServer {
		return pn
	}
	return lid
}

// pollMessageInfo rebuilds the MessageInfo whatsmeow needs to encrypt a vote
// for a stored poll.
func (b *Bridge) pollMessageInfo(chat types.JID, p *StoredPoll) types.MessageInfo {
	sender, _ := types.ParseJID(p.SenderJID)
	if sender.IsEmpty() {
		if p.IsFromMe {
			sender = b.client.Store.GetJID()
		} else {
			sender = chat
		}
	}
	return types.MessageInfo{
		MessageSource: types.MessageSource{Chat: chat, Sender: sender, IsFromMe: p.IsFromMe, IsGroup: chat.Server == types.GroupServer},
		ID:            p.MessageID,
	}
}

// SendPoll creates a poll in a chat.
func (b *Bridge) SendPoll(ctx context.Context, recipient, name string, options []string, selectable int) (SendResult, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return SendResult{}, badRequest("poll question is required")
	}
	clean := make([]string, 0, len(options))
	seen := map[string]bool{}
	for _, o := range options {
		o = strings.TrimSpace(o)
		if o == "" {
			continue
		}
		if seen[o] {
			return SendResult{}, badRequest(fmt.Sprintf("duplicate poll option %q", o))
		}
		seen[o] = true
		clean = append(clean, o)
	}
	if len(clean) < 2 || len(clean) > maxPollOptions {
		return SendResult{}, badRequest(fmt.Sprintf("a poll needs between 2 and %d options", maxPollOptions))
	}
	if selectable < 0 || selectable > len(clean) {
		return SendResult{}, badRequest("selectable_count must be between 0 (any number) and the number of options")
	}
	to, err := b.prepareSend(recipient)
	if err != nil {
		return SendResult{}, err
	}
	resp, err := b.client.SendMessage(ctx, to, b.client.BuildPollCreation(name, clean, selectable))
	if err != nil {
		return SendResult{}, fmt.Errorf("send poll: %w", err)
	}
	res := b.recordSent(ctx, to, resp, describePoll(name, clean), nil)
	if err := b.store.UpsertPoll(&StoredPoll{
		MessageID: resp.ID, ChatJID: res.Recipient, SenderJID: resp.Sender.String(), IsFromMe: true,
		Name: name, Options: clean, Selectable: selectable,
	}); err != nil {
		b.log.Warnf("store sent poll: %v", err)
	}
	return res, nil
}

// VotePoll casts (or, with no options, retracts) our vote in a stored poll.
func (b *Bridge) VotePoll(ctx context.Context, chatJID, pollID string, options []string) (SendResult, error) {
	chat, err := b.prepareSend(chatJID)
	if err != nil {
		return SendResult{}, err
	}
	poll, err := b.store.GetPoll(pollID, chat.ToNonAD().String())
	if err != nil {
		return SendResult{}, err
	}
	if poll == nil {
		return SendResult{}, badRequest(fmt.Sprintf("no poll with id %s in %s", pollID, chat.ToNonAD()))
	}
	known := map[string]bool{}
	for _, o := range poll.Options {
		known[o] = true
	}
	chosen := make([]string, 0, len(options))
	for _, o := range options {
		o = strings.TrimSpace(o)
		if !known[o] {
			return SendResult{}, badRequest(fmt.Sprintf("%q is not an option of this poll; options are: %s", o, strings.Join(poll.Options, ", ")))
		}
		chosen = append(chosen, o)
	}
	if poll.Selectable > 0 && len(chosen) > poll.Selectable {
		return SendResult{}, badRequest(fmt.Sprintf("this poll allows at most %d selection(s)", poll.Selectable))
	}
	info := b.pollMessageInfo(chat, poll)
	msg, err := b.client.BuildPollVote(ctx, &info, chosen)
	if err != nil {
		return SendResult{}, fmt.Errorf("build vote: %w", err)
	}
	resp, err := b.client.SendMessage(ctx, chat, msg)
	if err != nil {
		return SendResult{}, fmt.Errorf("send vote: %w", err)
	}
	if err := b.store.UpsertPollVote(&PollVote{
		PollID: pollID, ChatJID: chat.ToNonAD().String(), Voter: b.ownUser(), Selected: chosen, Timestamp: resp.Timestamp,
	}); err != nil {
		b.log.Warnf("store own vote: %v", err)
	}
	return SendResult{MessageID: resp.ID, Recipient: chat.ToNonAD().String(), Timestamp: resp.Timestamp}, nil
}
