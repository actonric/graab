package main

import (
	"fmt"
	"mime"
	"path"
	"strings"

	"go.mau.fi/whatsmeow/proto/waE2E"
)

// unwrap peels the wrapper message types (ephemeral, view-once, document with
// caption, edited) so the inner content can be inspected uniformly. Live
// events are already unwrapped by whatsmeow; history-sync messages are not.
func unwrap(msg *waE2E.Message) *waE2E.Message {
	for i := 0; i < 8 && msg != nil; i++ {
		switch {
		case msg.GetEphemeralMessage() != nil:
			msg = msg.GetEphemeralMessage().GetMessage()
		case msg.GetViewOnceMessage() != nil:
			msg = msg.GetViewOnceMessage().GetMessage()
		case msg.GetViewOnceMessageV2() != nil:
			msg = msg.GetViewOnceMessageV2().GetMessage()
		case msg.GetViewOnceMessageV2Extension() != nil:
			msg = msg.GetViewOnceMessageV2Extension().GetMessage()
		case msg.GetDocumentWithCaptionMessage() != nil:
			msg = msg.GetDocumentWithCaptionMessage().GetMessage()
		case msg.GetEditedMessage() != nil:
			msg = msg.GetEditedMessage().GetMessage()
		case msg.GetDeviceSentMessage() != nil:
			msg = msg.GetDeviceSentMessage().GetMessage()
		default:
			return msg
		}
	}
	return msg
}

// describeMessage turns a WhatsApp message into the text and media metadata
// we persist. ok is false for message kinds we deliberately do not store
// (reactions, protocol messages, key distribution, etc.).
func describeMessage(id string, msg *waE2E.Message) (content string, media *MediaInfo, quotedID string, ok bool) {
	msg = unwrap(msg)
	if msg == nil {
		return "", nil, "", false
	}

	switch {
	case msg.GetConversation() != "":
		return msg.GetConversation(), nil, "", true

	case msg.GetExtendedTextMessage() != nil:
		ext := msg.GetExtendedTextMessage()
		return ext.GetText(), nil, ext.GetContextInfo().GetStanzaID(), ext.GetText() != ""

	case msg.GetImageMessage() != nil:
		m := msg.GetImageMessage()
		media = &MediaInfo{
			Type: "image", MimeType: m.GetMimetype(), URL: m.GetURL(), DirectPath: m.GetDirectPath(),
			MediaKey: m.GetMediaKey(), FileSHA256: m.GetFileSHA256(), FileEncSHA256: m.GetFileEncSHA256(),
			FileLength: m.GetFileLength(),
		}
		media.Filename = "image_" + id + extensionFor(m.GetMimetype(), ".jpg")
		return m.GetCaption(), media, m.GetContextInfo().GetStanzaID(), true

	case msg.GetVideoMessage() != nil || msg.GetPtvMessage() != nil:
		m := msg.GetVideoMessage()
		if m == nil {
			m = msg.GetPtvMessage()
		}
		media = &MediaInfo{
			Type: "video", MimeType: m.GetMimetype(), URL: m.GetURL(), DirectPath: m.GetDirectPath(),
			MediaKey: m.GetMediaKey(), FileSHA256: m.GetFileSHA256(), FileEncSHA256: m.GetFileEncSHA256(),
			FileLength: m.GetFileLength(),
		}
		media.Filename = "video_" + id + extensionFor(m.GetMimetype(), ".mp4")
		return m.GetCaption(), media, m.GetContextInfo().GetStanzaID(), true

	case msg.GetAudioMessage() != nil:
		m := msg.GetAudioMessage()
		media = &MediaInfo{
			Type: "audio", MimeType: m.GetMimetype(), URL: m.GetURL(), DirectPath: m.GetDirectPath(),
			MediaKey: m.GetMediaKey(), FileSHA256: m.GetFileSHA256(), FileEncSHA256: m.GetFileEncSHA256(),
			FileLength: m.GetFileLength(),
		}
		prefix := "audio_"
		if m.GetPTT() {
			prefix = "voice_"
		}
		media.Filename = prefix + id + extensionFor(m.GetMimetype(), ".ogg")
		return "", media, m.GetContextInfo().GetStanzaID(), true

	case msg.GetDocumentMessage() != nil:
		m := msg.GetDocumentMessage()
		media = &MediaInfo{
			Type: "document", MimeType: m.GetMimetype(), URL: m.GetURL(), DirectPath: m.GetDirectPath(),
			MediaKey: m.GetMediaKey(), FileSHA256: m.GetFileSHA256(), FileEncSHA256: m.GetFileEncSHA256(),
			FileLength: m.GetFileLength(),
		}
		name := m.GetFileName()
		if name == "" {
			name = m.GetTitle()
		}
		if name == "" {
			name = "document_" + id + extensionFor(m.GetMimetype(), "")
		}
		media.Filename = safeFileName(name)
		return m.GetCaption(), media, m.GetContextInfo().GetStanzaID(), true

	case msg.GetStickerMessage() != nil:
		m := msg.GetStickerMessage()
		media = &MediaInfo{
			Type: "sticker", MimeType: m.GetMimetype(), URL: m.GetURL(), DirectPath: m.GetDirectPath(),
			MediaKey: m.GetMediaKey(), FileSHA256: m.GetFileSHA256(), FileEncSHA256: m.GetFileEncSHA256(),
			FileLength: m.GetFileLength(),
		}
		media.Filename = "sticker_" + id + extensionFor(m.GetMimetype(), ".webp")
		return "", media, m.GetContextInfo().GetStanzaID(), true

	case msg.GetLocationMessage() != nil:
		m := msg.GetLocationMessage()
		return formatLocation(m.GetName(), m.GetAddress(), m.GetDegreesLatitude(), m.GetDegreesLongitude()),
			nil, m.GetContextInfo().GetStanzaID(), true

	case msg.GetLiveLocationMessage() != nil:
		m := msg.GetLiveLocationMessage()
		return "[live location] " + formatLocation(m.GetCaption(), "", m.GetDegreesLatitude(), m.GetDegreesLongitude()),
			nil, m.GetContextInfo().GetStanzaID(), true

	case msg.GetContactMessage() != nil:
		m := msg.GetContactMessage()
		return "[contact] " + m.GetDisplayName(), nil, m.GetContextInfo().GetStanzaID(), true

	case msg.GetContactsArrayMessage() != nil:
		m := msg.GetContactsArrayMessage()
		var names []string
		for _, c := range m.GetContacts() {
			if n := c.GetDisplayName(); n != "" {
				names = append(names, n)
			}
		}
		return "[contacts] " + strings.Join(names, ", "), nil, m.GetContextInfo().GetStanzaID(), true

	case msg.GetPollCreationMessage() != nil:
		return "[poll] " + msg.GetPollCreationMessage().GetName(), nil, "", true
	case msg.GetPollCreationMessageV2() != nil:
		return "[poll] " + msg.GetPollCreationMessageV2().GetName(), nil, "", true
	case msg.GetPollCreationMessageV3() != nil:
		return "[poll] " + msg.GetPollCreationMessageV3().GetName(), nil, "", true

	case msg.GetEventMessage() != nil:
		return "[event] " + msg.GetEventMessage().GetName(), nil, "", true

	case msg.GetGroupInviteMessage() != nil:
		m := msg.GetGroupInviteMessage()
		return "[group invite] " + m.GetGroupName(), nil, "", true
	}

	// Reactions, protocol messages (deletes/edits carry their own handling),
	// sender-key distribution, payments, etc. are not stored.
	return "", nil, "", false
}

func formatLocation(name, address string, lat, lon float64) string {
	var b strings.Builder
	b.WriteString("[location]")
	if name != "" {
		b.WriteString(" " + name)
	}
	if address != "" {
		b.WriteString(", " + address)
	}
	fmt.Fprintf(&b, " (%.6f, %.6f)", lat, lon)
	return b.String()
}

// extensionFor picks a file extension for a MIME type, falling back to def.
func extensionFor(mimeType, def string) string {
	mt := strings.ToLower(strings.TrimSpace(strings.SplitN(mimeType, ";", 2)[0]))
	switch mt {
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "video/mp4":
		return ".mp4"
	case "video/quicktime":
		return ".mov"
	case "video/3gpp":
		return ".3gp"
	case "audio/ogg":
		return ".ogg"
	case "audio/mpeg", "audio/mp3":
		return ".mp3"
	case "audio/mp4", "audio/m4a", "audio/x-m4a":
		return ".m4a"
	case "audio/aac":
		return ".aac"
	case "audio/wav", "audio/x-wav":
		return ".wav"
	case "application/pdf":
		return ".pdf"
	case "":
		return def
	}
	if exts, err := mime.ExtensionsByType(mt); err == nil && len(exts) > 0 {
		return exts[0]
	}
	return def
}

// mimeTypeForPath guesses a MIME type from a file extension, defaulting to
// application/octet-stream.
func mimeTypeForPath(p string) string {
	switch strings.ToLower(path.Ext(p)) {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".mp4":
		return "video/mp4"
	case ".mov":
		return "video/quicktime"
	case ".3gp":
		return "video/3gpp"
	case ".ogg", ".opus":
		return "audio/ogg; codecs=opus"
	case ".mp3":
		return "audio/mpeg"
	case ".m4a":
		return "audio/mp4"
	case ".aac":
		return "audio/aac"
	case ".wav":
		return "audio/wav"
	case ".pdf":
		return "application/pdf"
	case ".txt":
		return "text/plain"
	case ".csv":
		return "text/csv"
	case ".zip":
		return "application/zip"
	case ".docx":
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	case ".xlsx":
		return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	case ".pptx":
		return "application/vnd.openxmlformats-officedocument.presentationml.presentation"
	}
	if t := mime.TypeByExtension(strings.ToLower(path.Ext(p))); t != "" {
		return t
	}
	return "application/octet-stream"
}
