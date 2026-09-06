package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image/png"
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
	pairing   PairingInfo
}

func (f *fakeMessenger) Pairing() PairingInfo {
	if f.pairing.State == "" {
		return PairingInfo{State: "paired", Message: "Already paired"}
	}
	return f.pairing
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

func TestBearerAuth(t *testing.T) {
	h := requireBearer("s3cret", newAPIHandler(&fakeMessenger{}))

	code, res := doJSON(t, h, http.MethodPost, "/api/send", `{"recipient":"123","message":"hi"}`)
	if code != 401 || res.Success {
		t.Errorf("no token: %d %+v", code, res)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/send", strings.NewReader(`{"recipient":"123","message":"hi"}`))
	req.Header.Set("Authorization", "Bearer wrong")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Errorf("wrong token: %d", rec.Code)
	}
	req = httptest.NewRequest(http.MethodPost, "/api/send", strings.NewReader(`{"recipient":"123","message":"hi"}`))
	req.Header.Set("Authorization", "Bearer s3cret")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Errorf("right token: %d %s", rec.Code, rec.Body.String())
	}
	// Health stays open for probes.
	req = httptest.NewRequest(http.MethodGet, "/health", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Errorf("health without token: %d", rec.Code)
	}
	// Empty token disables auth entirely.
	code, _ = doJSON(t, requireBearer("", newAPIHandler(&fakeMessenger{})), http.MethodGet, "/api/status", "")
	if code != 200 {
		t.Errorf("no-auth mode: %d", code)
	}
}

func TestForbiddenMapsTo403(t *testing.T) {
	fm := &fakeMessenger{sendErr: forbidden("read-only")}
	code, res := doJSON(t, newAPIHandler(fm), http.MethodPost, "/api/send", `{"recipient":"123","message":"hi"}`)
	if code != 403 || res.Message != "read-only" {
		t.Errorf("forbidden: %d %+v", code, res)
	}
}

func TestPairingEndpoints(t *testing.T) {
	fm := &fakeMessenger{}
	h := newAPIHandler(fm)

	// Paired: JSON says so, PNG is a 404.
	req := httptest.NewRequest(http.MethodGet, "/api/pair", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var info PairingInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &info); err != nil || info.State != "paired" {
		t.Errorf("pair json: %d %s %v", rec.Code, rec.Body.String(), err)
	}
	req = httptest.NewRequest(http.MethodGet, "/api/pair/qr.png", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 404 {
		t.Errorf("qr when paired: %d", rec.Code)
	}

	// Unpaired with a QR: PNG decodes.
	fm.pairing = PairingInfo{State: "qr", Mode: "qr", Code: "2@abc,def,ghi==,jkl=", Message: "scan me"}
	req = httptest.NewRequest(http.MethodGet, "/api/pair/qr.png", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("qr png: %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
	img, err := png.Decode(bytes.NewReader(rec.Body.Bytes()))
	if err != nil || img.Bounds().Dx() < 100 {
		t.Errorf("qr png decode: %v %v", err, img)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("qr must not be cached")
	}

	// Phone code: JSON carries the code, no PNG.
	fm.pairing = PairingInfo{State: "code", Mode: "phone", Code: "ABCD-EFGH"}
	req = httptest.NewRequest(http.MethodGet, "/api/pair", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	_ = json.Unmarshal(rec.Body.Bytes(), &info)
	if info.State != "code" || info.Code != "ABCD-EFGH" {
		t.Errorf("phone code json: %+v", info)
	}
	req = httptest.NewRequest(http.MethodGet, "/api/pair/qr.png", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 404 {
		t.Errorf("qr in phone mode: %d", rec.Code)
	}

	// Auth applies to pairing endpoints too.
	req = httptest.NewRequest(http.MethodGet, "/api/pair", nil)
	rec = httptest.NewRecorder()
	requireBearer("tok", h).ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Errorf("pair without token: %d", rec.Code)
	}
}

func TestQRPNG(t *testing.T) {
	if _, err := qrPNG(""); err == nil {
		t.Errorf("empty payload should error")
	}
	data, err := qrPNG("hello")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := png.Decode(bytes.NewReader(data)); err != nil {
		t.Errorf("not a png: %v", err)
	}
}
