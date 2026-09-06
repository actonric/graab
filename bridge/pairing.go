package main

import (
	"errors"
	"sync"
	"time"

	"rsc.io/qr"
)

// PairingInfo is returned by GET /api/pair so a remote client (the MCP
// server's /pair page, or a tool result) can show the current QR or code.
type PairingInfo struct {
	// State is "paired", "waiting" (connecting, no code yet), "qr" or "code".
	State     string `json:"state"`
	Mode      string `json:"mode,omitempty"` // "qr" or "phone"
	Code      string `json:"code,omitempty"` // raw QR payload or the 8-character phone code
	UpdatedAt string `json:"updated_at,omitempty"`
	Message   string `json:"message"`
}

// pairState is the latest pairing code, published by the connect loop and
// read by the API.
type pairState struct {
	mu   sync.Mutex
	mode string
	code string
	at   time.Time
}

func (p *pairState) set(mode, code string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.mode, p.code, p.at = mode, code, time.Now()
}

func (p *pairState) clear() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.mode, p.code, p.at = "", "", time.Time{}
}

func (p *pairState) snapshot() (mode, code string, at time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.mode, p.code, p.at
}

// Pairing reports whether the bridge needs pairing and, if so, the current code.
func (b *Bridge) Pairing() PairingInfo {
	if b.client.Store.ID != nil {
		return PairingInfo{State: "paired", Message: "Already paired with " + b.client.Store.ID.ToNonAD().String()}
	}
	mode, code, at := b.pair.snapshot()
	if code == "" {
		return PairingInfo{State: "waiting", Message: "Not paired yet; waiting for WhatsApp to issue a pairing code"}
	}
	info := PairingInfo{Mode: mode, Code: code, UpdatedAt: at.UTC().Format(timeLayout)}
	switch mode {
	case "phone":
		info.State = "code"
		info.Message = "On your phone: WhatsApp → Settings → Linked devices → Link a device → Link with phone number instead, then enter this code"
	default:
		info.State = "qr"
		info.Message = "On your phone: WhatsApp → Settings → Linked devices → Link a device, then scan this QR code (it refreshes every ~20 seconds)"
	}
	return info
}

// qrPNG renders a QR payload as a PNG image.
func qrPNG(payload string) ([]byte, error) {
	if payload == "" {
		return nil, errors.New("empty QR payload")
	}
	code, err := qr.Encode(payload, qr.L)
	if err != nil {
		return nil, err
	}
	code.Scale = 8
	return code.PNG(), nil
}
