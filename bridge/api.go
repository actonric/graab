package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

// Messenger is the subset of bridge behaviour the HTTP API needs. The real
// implementation talks to WhatsApp; tests use a fake.
type Messenger interface {
	SendMessage(ctx context.Context, recipient, text, mediaPath string) (SendResult, error)
	DownloadMedia(ctx context.Context, messageID, chatJID string) (DownloadResult, error)
	Status() StatusResponse
}

// SendResult describes a successfully sent message.
type SendResult struct {
	MessageID string    `json:"message_id"`
	Recipient string    `json:"recipient"`
	Timestamp time.Time `json:"timestamp"`
}

// DownloadResult describes a downloaded attachment.
type DownloadResult struct {
	Path      string `json:"path"`
	Filename  string `json:"filename"`
	MediaType string `json:"media_type"`
	MimeType  string `json:"mime_type,omitempty"`
}

// StatusResponse is returned by GET /api/status.
type StatusResponse struct {
	Connected  bool     `json:"connected"`
	LoggedIn   bool     `json:"logged_in"`
	JID        string   `json:"jid,omitempty"`
	Chats      int      `json:"chats"`
	Messages   int      `json:"messages"`
	StoreDir   string   `json:"store_dir"`
	ReadOnly   bool     `json:"read_only"`
	MediaRoots []string `json:"media_roots"`
}

type sendRequest struct {
	Recipient string `json:"recipient"`
	Message   string `json:"message"`
	MediaPath string `json:"media_path,omitempty"`
}

type downloadRequest struct {
	MessageID string `json:"message_id"`
	ChatJID   string `json:"chat_jid"`
}

type apiResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
	// Populated on success for /api/send.
	MessageID string `json:"message_id,omitempty"`
	Timestamp string `json:"timestamp,omitempty"`
	// Populated on success for /api/download.
	Path      string `json:"path,omitempty"`
	Filename  string `json:"filename,omitempty"`
	MediaType string `json:"media_type,omitempty"`
}

// errBadRequest marks errors caused by the caller's input.
var errBadRequest = errors.New("bad request")

func badRequest(msg string) error { return &requestError{msg} }

type requestError struct{ msg string }

func (e *requestError) Error() string   { return e.msg }
func (e *requestError) Is(t error) bool { return t == errBadRequest }

// statusFor maps error kinds to HTTP status codes.
func statusFor(err error) int {
	switch {
	case errors.Is(err, errBadRequest):
		return http.StatusBadRequest
	case errors.Is(err, errForbidden):
		return http.StatusForbidden
	default:
		return http.StatusInternalServerError
	}
}

// requireBearer wraps a handler with bearer-token authentication. An empty
// token disables the check (loopback-only deployments). /health stays open so
// supervisors can probe it.
func requireBearer(token string, next http.Handler) http.Handler {
	if token == "" {
		return next
	}
	want := []byte(token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			next.ServeHTTP(w, r)
			return
		}
		got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || subtle.ConstantTimeCompare([]byte(strings.TrimSpace(got)), want) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="graab"`)
			writeJSON(w, http.StatusUnauthorized, apiResponse{Message: "missing or invalid bearer token"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// newAPIHandler builds the HTTP mux for the local REST API.
func newAPIHandler(m Messenger) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok\n"))
	})

	mux.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, http.StatusOK, m.Status())
	})

	mux.HandleFunc("/api/send", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req sendRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, apiResponse{Message: "invalid JSON body: " + err.Error()})
			return
		}
		req.Recipient = strings.TrimSpace(req.Recipient)
		if req.Recipient == "" {
			writeJSON(w, http.StatusBadRequest, apiResponse{Message: "recipient is required"})
			return
		}
		if req.Message == "" && req.MediaPath == "" {
			writeJSON(w, http.StatusBadRequest, apiResponse{Message: "message or media_path is required"})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
		defer cancel()
		res, err := m.SendMessage(ctx, req.Recipient, req.Message, req.MediaPath)
		if err != nil {
			writeJSON(w, statusFor(err), apiResponse{Message: err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, apiResponse{
			Success:   true,
			Message:   "Message sent to " + res.Recipient,
			MessageID: res.MessageID,
			Timestamp: res.Timestamp.UTC().Format(timeLayout),
		})
	})

	mux.HandleFunc("/api/download", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req downloadRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, apiResponse{Message: "invalid JSON body: " + err.Error()})
			return
		}
		if req.MessageID == "" || req.ChatJID == "" {
			writeJSON(w, http.StatusBadRequest, apiResponse{Message: "message_id and chat_jid are required"})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
		defer cancel()
		res, err := m.DownloadMedia(ctx, req.MessageID, req.ChatJID)
		if err != nil {
			writeJSON(w, statusFor(err), apiResponse{Message: err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, apiResponse{
			Success:   true,
			Message:   "Downloaded " + res.MediaType,
			Path:      res.Path,
			Filename:  res.Filename,
			MediaType: res.MediaType,
		})
	})

	return mux
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
