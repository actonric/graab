package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type fakeMessenger struct {
	sent      []sendRequest
	sendErr   error
	downloads []downloadRequest
	dlErr     error
}

func (f *fakeMessenger) SendMessage(_ context.Context, recipient, text, mediaPath string) (SendResult, error) {
	f.sent = append(f.sent, sendRequest{Recipient: recipient, Message: text, MediaPath: mediaPath})
	if f.sendErr != nil {
		return SendResult{}, f.sendErr
	}
	return SendResult{MessageID: "MSG1", Recipient: recipient, Timestamp: time.Date(2025, 4, 1, 0, 0, 0, 0, time.UTC)}, nil
}

func (f *fakeMessenger) DownloadMedia(_ context.Context, messageID, chatJID string) (DownloadResult, error) {
	f.downloads = append(f.downloads, downloadRequest{MessageID: messageID, ChatJID: chatJID})
	if f.dlErr != nil {
		return DownloadResult{}, f.dlErr
	}
	return DownloadResult{Path: "/store/media/x/image_1.jpg", Filename: "image_1.jpg", MediaType: "image", MimeType: "image/jpeg"}, nil
}

func (f *fakeMessenger) Status() StatusResponse {
	return StatusResponse{Connected: true, LoggedIn: true, JID: "1@s.whatsapp.net", Chats: 2, Messages: 5}
}

func doJSON(t *testing.T, h http.Handler, method, path, body string) (int, apiResponse) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out apiResponse
	if rec.Body.Len() > 0 && strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode response %q: %v", rec.Body.String(), err)
		}
	}
	return rec.Code, out
}

func TestSendEndpoint(t *testing.T) {
	fm := &fakeMessenger{}
	h := newAPIHandler(fm)

	code, res := doJSON(t, h, http.MethodPost, "/api/send", `{"recipient":"123","message":"hi"}`)
	if code != 200 || !res.Success || res.MessageID != "MSG1" || res.Timestamp != "2025-04-01T00:00:00Z" {
		t.Errorf("send: %d %+v", code, res)
	}
	if len(fm.sent) != 1 || fm.sent[0].Message != "hi" {
		t.Errorf("messenger not called correctly: %+v", fm.sent)
	}

	code, res = doJSON(t, h, http.MethodPost, "/api/send", `{"recipient":"","message":"hi"}`)
	if code != 400 || res.Success {
		t.Errorf("missing recipient: %d %+v", code, res)
	}
	code, res = doJSON(t, h, http.MethodPost, "/api/send", `{"recipient":"123"}`)
	if code != 400 || !strings.Contains(res.Message, "media_path") {
		t.Errorf("missing body: %d %+v", code, res)
	}
	code, _ = doJSON(t, h, http.MethodPost, "/api/send", `{not json`)
	if code != 400 {
		t.Errorf("bad json: %d", code)
	}
	code, _ = doJSON(t, h, http.MethodGet, "/api/send", "")
	if code != 405 {
		t.Errorf("GET send: %d", code)
	}

	fm.sendErr = badRequest("invalid phone number")
	code, res = doJSON(t, h, http.MethodPost, "/api/send", `{"recipient":"abc","message":"hi"}`)
	if code != 400 || res.Message != "invalid phone number" {
		t.Errorf("bad request from messenger: %d %+v", code, res)
	}
	fm.sendErr = errors.New("not connected")
	code, res = doJSON(t, h, http.MethodPost, "/api/send", `{"recipient":"123","message":"hi"}`)
	if code != 500 || res.Success {
		t.Errorf("internal error: %d %+v", code, res)
	}
}

func TestDownloadEndpoint(t *testing.T) {
	fm := &fakeMessenger{}
	h := newAPIHandler(fm)

	code, res := doJSON(t, h, http.MethodPost, "/api/download", `{"message_id":"M","chat_jid":"1@s.whatsapp.net"}`)
	if code != 200 || !res.Success || res.Path == "" || res.MediaType != "image" {
		t.Errorf("download: %d %+v", code, res)
	}
	code, _ = doJSON(t, h, http.MethodPost, "/api/download", `{"message_id":"M"}`)
	if code != 400 {
		t.Errorf("missing chat: %d", code)
	}
	fm.dlErr = badRequest("message has no media attachment")
	code, res = doJSON(t, h, http.MethodPost, "/api/download", `{"message_id":"M","chat_jid":"1@s.whatsapp.net"}`)
	if code != 400 || res.Success {
		t.Errorf("no media: %d %+v", code, res)
	}
}

func TestStatusAndHealth(t *testing.T) {
	h := newAPIHandler(&fakeMessenger{})
	req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var st StatusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil || !st.Connected || st.Messages != 5 {
		t.Errorf("status: %d %s %v", rec.Code, rec.Body.String(), err)
	}
	req = httptest.NewRequest(http.MethodGet, "/health", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 || rec.Body.String() != "ok\n" {
		t.Errorf("health: %d %q", rec.Code, rec.Body.String())
	}
}
