package main

import (
	"strings"
	"testing"

	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

func TestDescribeText(t *testing.T) {
	content, media, quoted, ok := describeMessage("ID1", &waE2E.Message{Conversation: proto.String("hello")})
	if !ok || content != "hello" || media != nil || quoted != "" {
		t.Errorf("conversation: %q %v %q %v", content, media, quoted, ok)
	}

	ext := &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{
		Text:        proto.String("reply"),
		ContextInfo: &waE2E.ContextInfo{StanzaID: proto.String("ORIG")},
	}}
	content, _, quoted, ok = describeMessage("ID2", ext)
	if !ok || content != "reply" || quoted != "ORIG" {
		t.Errorf("extended text: %q %q %v", content, quoted, ok)
	}
}

func TestDescribeMedia(t *testing.T) {
	img := &waE2E.Message{ImageMessage: &waE2E.ImageMessage{
		Caption: proto.String("cap"), Mimetype: proto.String("image/png"),
		URL: proto.String("https://mmg.whatsapp.net/v/x.enc"), DirectPath: proto.String("/v/x.enc"),
		MediaKey: []byte{1}, FileSHA256: []byte{2}, FileEncSHA256: []byte{3}, FileLength: proto.Uint64(10),
	}}
	content, media, _, ok := describeMessage("ID3", img)
	if !ok || content != "cap" || media == nil {
		t.Fatalf("image: %q %v %v", content, media, ok)
	}
	if media.Type != "image" || media.Filename != "image_ID3.png" || media.FileLength != 10 || media.DirectPath != "/v/x.enc" {
		t.Errorf("image media: %+v", media)
	}

	voice := &waE2E.Message{AudioMessage: &waE2E.AudioMessage{
		Mimetype: proto.String("audio/ogg; codecs=opus"), PTT: proto.Bool(true), MediaKey: []byte{1},
	}}
	_, media, _, ok = describeMessage("ID4", voice)
	if !ok || media.Type != "audio" || media.Filename != "voice_ID4.ogg" {
		t.Errorf("voice: %+v %v", media, ok)
	}

	doc := &waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{
		FileName: proto.String("../../secret.pdf"), Mimetype: proto.String("application/pdf"), Caption: proto.String("see attached"),
	}}
	content, media, _, ok = describeMessage("ID5", doc)
	if !ok || content != "see attached" || media.Type != "document" {
		t.Fatalf("document: %q %+v %v", content, media, ok)
	}
	if strings.Contains(media.Filename, "/") || strings.Contains(media.Filename, "..") {
		t.Errorf("document filename not sanitized: %q", media.Filename)
	}

	sticker := &waE2E.Message{StickerMessage: &waE2E.StickerMessage{Mimetype: proto.String("image/webp")}}
	_, media, _, ok = describeMessage("ID6", sticker)
	if !ok || media.Type != "sticker" || media.Filename != "sticker_ID6.webp" {
		t.Errorf("sticker: %+v %v", media, ok)
	}
}

func TestDescribeUnwrapsWrappers(t *testing.T) {
	inner := &waE2E.Message{Conversation: proto.String("secret")}
	wrapped := &waE2E.Message{EphemeralMessage: &waE2E.FutureProofMessage{Message: &waE2E.Message{
		ViewOnceMessageV2: &waE2E.FutureProofMessage{Message: inner},
	}}}
	content, _, _, ok := describeMessage("ID7", wrapped)
	if !ok || content != "secret" {
		t.Errorf("wrapped: %q %v", content, ok)
	}

	dwc := &waE2E.Message{DocumentWithCaptionMessage: &waE2E.FutureProofMessage{Message: &waE2E.Message{
		DocumentMessage: &waE2E.DocumentMessage{FileName: proto.String("a.txt"), Caption: proto.String("c")},
	}}}
	content, media, _, ok := describeMessage("ID8", dwc)
	if !ok || content != "c" || media == nil || media.Filename != "a.txt" {
		t.Errorf("document with caption: %q %+v %v", content, media, ok)
	}
}

func TestDescribeSkipsNonContent(t *testing.T) {
	reaction := &waE2E.Message{ReactionMessage: &waE2E.ReactionMessage{
		Text: proto.String("👍"), Key: &waCommon.MessageKey{ID: proto.String("X")},
	}}
	if _, _, _, ok := describeMessage("ID9", reaction); ok {
		t.Errorf("reactions should not be stored")
	}
	revoke := &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{Type: waE2E.ProtocolMessage_REVOKE.Enum()}}
	if _, _, _, ok := describeMessage("ID10", revoke); ok {
		t.Errorf("protocol messages should not be stored")
	}
	if _, _, _, ok := describeMessage("ID11", nil); ok {
		t.Errorf("nil message should not be stored")
	}
	if _, _, _, ok := describeMessage("ID12", &waE2E.Message{}); ok {
		t.Errorf("empty message should not be stored")
	}
}

func TestDescribeLocationAndContact(t *testing.T) {
	loc := &waE2E.Message{LocationMessage: &waE2E.LocationMessage{
		Name: proto.String("Cafe"), Address: proto.String("1 Main St"),
		DegreesLatitude: proto.Float64(51.5), DegreesLongitude: proto.Float64(-0.1),
	}}
	content, _, _, ok := describeMessage("L", loc)
	if !ok || content != "[location] Cafe, 1 Main St (51.500000, -0.100000)" {
		t.Errorf("location: %q", content)
	}
	c := &waE2E.Message{ContactMessage: &waE2E.ContactMessage{DisplayName: proto.String("Bob")}}
	content, _, _, ok = describeMessage("C", c)
	if !ok || content != "[contact] Bob" {
		t.Errorf("contact: %q", content)
	}
}

func TestExtensionAndMime(t *testing.T) {
	if got := extensionFor("image/jpeg", ".bin"); got != ".jpg" {
		t.Errorf("jpeg ext = %q", got)
	}
	if got := extensionFor("audio/ogg; codecs=opus", ".bin"); got != ".ogg" {
		t.Errorf("ogg ext = %q", got)
	}
	if got := extensionFor("", ".bin"); got != ".bin" {
		t.Errorf("empty mime ext = %q", got)
	}
	if got := mimeTypeForPath("/tmp/x.PNG"); got != "image/png" {
		t.Errorf("png mime = %q", got)
	}
	if got := mimeTypeForPath("/tmp/x.unknownext"); got != "application/octet-stream" {
		t.Errorf("unknown mime = %q", got)
	}
}

func TestDirectPathFromURL(t *testing.T) {
	got := directPathFromURL("https://mmg.whatsapp.net/v/t62.7118-24/abc_n.enc?ccb=11-4&oh=x")
	if got != "/v/t62.7118-24/abc_n.enc" {
		t.Errorf("direct path = %q", got)
	}
	if got := directPathFromURL("garbage"); got != "" {
		t.Errorf("garbage url = %q", got)
	}
}

func TestParseRecipient(t *testing.T) {
	jid, err := parseRecipient("+1 (415) 555-1234")
	if err != nil || jid.String() != "14155551234@s.whatsapp.net" {
		t.Errorf("phone: %v %v", jid, err)
	}
	jid, err = parseRecipient("120363000000000000@g.us")
	if err != nil || jid.Server != "g.us" {
		t.Errorf("group: %v %v", jid, err)
	}
	if _, err := parseRecipient("abc"); err == nil {
		t.Errorf("garbage recipient accepted")
	}
	if _, err := parseRecipient(""); err == nil {
		t.Errorf("empty recipient accepted")
	}
	if _, err := parseRecipient("@g.us"); err == nil {
		t.Errorf("empty user JID accepted")
	}
}
