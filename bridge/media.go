package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

var nonDigits = regexp.MustCompile(`[^0-9]`)

// parseRecipient accepts a full JID ("123@s.whatsapp.net", "123-456@g.us") or
// a phone number in international format with or without "+" and punctuation.
func parseRecipient(recipient string) (types.JID, error) {
	recipient = strings.TrimSpace(recipient)
	if recipient == "" {
		return types.JID{}, badRequest("recipient is required")
	}
	if strings.Contains(recipient, "@") {
		jid, err := types.ParseJID(recipient)
		if err != nil {
			return types.JID{}, badRequest(fmt.Sprintf("invalid JID %q: %v", recipient, err))
		}
		if jid.User == "" {
			return types.JID{}, badRequest(fmt.Sprintf("invalid JID %q", recipient))
		}
		return jid, nil
	}
	digits := nonDigits.ReplaceAllString(recipient, "")
	if len(digits) < 5 {
		return types.JID{}, badRequest(fmt.Sprintf("invalid phone number %q: use the international format without the +, e.g. 14155551234", recipient))
	}
	return types.NewJID(digits, types.DefaultUserServer), nil
}

// SendMessage sends text and/or a media file to a recipient and records the
// sent message in the local store.
func (b *Bridge) SendMessage(ctx context.Context, recipient, text, mediaPath string) (SendResult, error) {
	if !b.client.IsConnected() {
		return SendResult{}, fmt.Errorf("not connected to WhatsApp")
	}
	to, err := parseRecipient(recipient)
	if err != nil {
		return SendResult{}, err
	}

	var msg *waE2E.Message
	var media *MediaInfo
	if mediaPath != "" {
		msg, media, err = b.buildMediaMessage(ctx, mediaPath, text)
		if err != nil {
			return SendResult{}, err
		}
	} else {
		msg = &waE2E.Message{Conversation: proto.String(text)}
	}

	resp, err := b.client.SendMessage(ctx, to, msg)
	if err != nil {
		return SendResult{}, fmt.Errorf("send message: %w", err)
	}

	// whatsmeow does not echo our own sends as events, so record it here.
	chatJID := to.ToNonAD().String()
	name := b.chatName(ctx, to.ToNonAD(), "", to.Server == types.GroupServer)
	if err := b.store.UpsertChat(chatJID, name, to.Server == types.GroupServer, resp.Timestamp); err != nil {
		b.log.Warnf("store chat after send: %v", err)
	}
	if media != nil {
		media.Filename = "sent_" + safeFileName(filepath.Base(mediaPath))
	}
	if err := b.store.InsertMessage(&StoredMessage{
		ID: resp.ID, ChatJID: chatJID, Sender: b.ownUser(), Content: text,
		Timestamp: resp.Timestamp, IsFromMe: true, Media: media,
	}); err != nil {
		b.log.Warnf("store sent message: %v", err)
	}
	return SendResult{MessageID: resp.ID, Recipient: chatJID, Timestamp: resp.Timestamp}, nil
}

// buildMediaMessage uploads a local file and wraps it in the right message type.
func (b *Bridge) buildMediaMessage(ctx context.Context, mediaPath, caption string) (*waE2E.Message, *MediaInfo, error) {
	data, err := os.ReadFile(mediaPath)
	if err != nil {
		return nil, nil, badRequest(fmt.Sprintf("read media file: %v", err))
	}
	if len(data) == 0 {
		return nil, nil, badRequest("media file is empty")
	}
	mimeType := mimeTypeForPath(mediaPath)
	base := strings.ToLower(strings.TrimPrefix(mimeType, " "))

	var kind whatsmeow.MediaType
	var mediaType string
	switch {
	case strings.HasPrefix(base, "image/") && base != "image/gif":
		kind, mediaType = whatsmeow.MediaImage, "image"
	case strings.HasPrefix(base, "video/") || base == "image/gif":
		kind, mediaType = whatsmeow.MediaVideo, "video"
	case strings.HasPrefix(base, "audio/"):
		kind, mediaType = whatsmeow.MediaAudio, "audio"
	default:
		kind, mediaType = whatsmeow.MediaDocument, "document"
	}

	up, err := b.client.Upload(ctx, data, kind)
	if err != nil {
		return nil, nil, fmt.Errorf("upload media: %w", err)
	}

	info := &MediaInfo{
		Type: mediaType, MimeType: mimeType, URL: up.URL, DirectPath: up.DirectPath,
		MediaKey: up.MediaKey, FileSHA256: up.FileSHA256, FileEncSHA256: up.FileEncSHA256, FileLength: up.FileLength,
	}
	msg := &waE2E.Message{}
	switch kind {
	case whatsmeow.MediaImage:
		msg.ImageMessage = &waE2E.ImageMessage{
			Caption: optString(caption), Mimetype: proto.String(mimeType),
			URL: proto.String(up.URL), DirectPath: proto.String(up.DirectPath), MediaKey: up.MediaKey,
			FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256, FileLength: proto.Uint64(up.FileLength),
		}
	case whatsmeow.MediaVideo:
		msg.VideoMessage = &waE2E.VideoMessage{
			Caption: optString(caption), Mimetype: proto.String(mimeType),
			URL: proto.String(up.URL), DirectPath: proto.String(up.DirectPath), MediaKey: up.MediaKey,
			FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256, FileLength: proto.Uint64(up.FileLength),
			GifPlayback: proto.Bool(base == "image/gif"),
		}
	case whatsmeow.MediaAudio:
		audio := &waE2E.AudioMessage{
			Mimetype: proto.String(mimeType),
			URL:      proto.String(up.URL), DirectPath: proto.String(up.DirectPath), MediaKey: up.MediaKey,
			FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256, FileLength: proto.Uint64(up.FileLength),
		}
		// Ogg Opus is sent as a push-to-talk voice note with duration + waveform
		// so it renders as a playable bubble. Other audio is a plain audio file.
		if strings.Contains(base, "ogg") {
			oi, err := analyzeOggOpus(data)
			if err != nil {
				return nil, nil, badRequest(fmt.Sprintf("%s is not a valid Ogg Opus file: %v", filepath.Base(mediaPath), err))
			}
			audio.PTT = proto.Bool(true)
			audio.Seconds = proto.Uint32(oi.Seconds)
			audio.Waveform = oi.Waveform
		}
		msg.AudioMessage = audio
	default:
		msg.DocumentMessage = &waE2E.DocumentMessage{
			Title: proto.String(filepath.Base(mediaPath)), FileName: proto.String(filepath.Base(mediaPath)),
			Caption: optString(caption), Mimetype: proto.String(mimeType),
			URL: proto.String(up.URL), DirectPath: proto.String(up.DirectPath), MediaKey: up.MediaKey,
			FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256, FileLength: proto.Uint64(up.FileLength),
		}
	}
	return msg, info, nil
}

func optString(s string) *string {
	if s == "" {
		return nil
	}
	return proto.String(s)
}

// mediaDownloader adapts stored media metadata to whatsmeow's download API.
type mediaDownloader struct {
	info *MediaInfo
	kind whatsmeow.MediaType
}

func (d *mediaDownloader) GetDirectPath() string             { return d.info.DirectPath }
func (d *mediaDownloader) GetURL() string                    { return d.info.URL }
func (d *mediaDownloader) GetMediaKey() []byte               { return d.info.MediaKey }
func (d *mediaDownloader) GetFileSHA256() []byte             { return d.info.FileSHA256 }
func (d *mediaDownloader) GetFileEncSHA256() []byte          { return d.info.FileEncSHA256 }
func (d *mediaDownloader) GetFileLength() uint64             { return d.info.FileLength }
func (d *mediaDownloader) GetMediaType() whatsmeow.MediaType { return d.kind }

// DownloadMedia fetches and decrypts a message's attachment into the store's
// media directory, returning the absolute path. Already-downloaded files are
// returned without contacting WhatsApp.
func (b *Bridge) DownloadMedia(ctx context.Context, messageID, chatJID string) (DownloadResult, error) {
	msg, err := b.store.GetMessage(messageID, chatJID)
	if err != nil {
		return DownloadResult{}, fmt.Errorf("look up message: %w", err)
	}
	if msg == nil {
		return DownloadResult{}, badRequest(fmt.Sprintf("message %s not found in chat %s", messageID, chatJID))
	}
	if msg.Media == nil {
		return DownloadResult{}, badRequest("message has no media attachment")
	}
	info := msg.Media

	dir := filepath.Join(b.storeDir, "media", safeFileName(strings.ReplaceAll(chatJID, ":", "_")))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return DownloadResult{}, fmt.Errorf("create media directory: %w", err)
	}
	local := filepath.Join(dir, safeFileName(info.Filename))
	abs, _ := filepath.Abs(local)
	result := DownloadResult{Path: abs, Filename: filepath.Base(local), MediaType: info.Type, MimeType: info.MimeType}

	if st, err := os.Stat(local); err == nil && st.Size() > 0 {
		return result, nil
	}

	if info.DirectPath == "" && info.URL == "" || len(info.MediaKey) == 0 || len(info.FileSHA256) == 0 || len(info.FileEncSHA256) == 0 {
		return DownloadResult{}, fmt.Errorf("stored media metadata is incomplete; the attachment cannot be downloaded")
	}
	if info.DirectPath == "" {
		info.DirectPath = directPathFromURL(info.URL)
	}

	var kind whatsmeow.MediaType
	switch info.Type {
	case "image", "sticker":
		kind = whatsmeow.MediaImage
	case "video":
		kind = whatsmeow.MediaVideo
	case "audio":
		kind = whatsmeow.MediaAudio
	case "document":
		kind = whatsmeow.MediaDocument
	default:
		return DownloadResult{}, fmt.Errorf("unsupported media type %q", info.Type)
	}
	if !b.client.IsConnected() {
		return DownloadResult{}, fmt.Errorf("not connected to WhatsApp")
	}

	data, err := b.client.Download(ctx, &mediaDownloader{info: info, kind: kind})
	if err != nil {
		return DownloadResult{}, fmt.Errorf("download media: %w", err)
	}
	if err := os.WriteFile(local, data, 0o644); err != nil {
		return DownloadResult{}, fmt.Errorf("write media file: %w", err)
	}
	b.log.Infof("Downloaded %s (%d bytes) to %s", info.Type, len(data), abs)
	return result, nil
}

// directPathFromURL recovers the CDN direct path from a full media URL
// (older stored rows may only have the URL).
func directPathFromURL(url string) string {
	idx := strings.Index(url, ".net/")
	if idx < 0 {
		return ""
	}
	p := url[idx+len(".net/"):]
	if q := strings.IndexByte(p, '?'); q >= 0 {
		p = p[:q]
	}
	return "/" + p
}

// Status reports connection state and store size.
func (b *Bridge) Status() StatusResponse {
	chats, messages, _ := b.store.Counts()
	s := StatusResponse{
		Connected: b.client.IsConnected(),
		LoggedIn:  b.client.IsLoggedIn(),
		Chats:     chats,
		Messages:  messages,
		StoreDir:  b.storeDir,
	}
	if b.client.Store.ID != nil {
		s.JID = b.client.Store.ID.ToNonAD().String()
	}
	return s
}

var _ = time.Second
