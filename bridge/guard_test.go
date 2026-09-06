package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/types"
)

func TestResolveOutgoingPath(t *testing.T) {
	dir := t.TempDir()
	outbox := filepath.Join(dir, "outbox")
	secret := filepath.Join(dir, "whatsapp.db")
	if err := os.MkdirAll(outbox, 0o755); err != nil {
		t.Fatal(err)
	}
	ok := filepath.Join(outbox, "pic.jpg")
	_ = os.WriteFile(ok, []byte("x"), 0o644)
	_ = os.WriteFile(secret, []byte("keys"), 0o600)
	link := filepath.Join(outbox, "sneaky.db")
	_ = os.Symlink(secret, link)
	roots := []string{outbox, filepath.Join(dir, "missing-root")}

	if got, err := resolveOutgoingPath(ok, roots); err != nil || got != ok {
		t.Errorf("allowed file: %q %v", got, err)
	}
	if _, err := resolveOutgoingPath(secret, roots); !errors.Is(err, errForbidden) {
		t.Errorf("file outside roots should be forbidden, got %v", err)
	}
	if _, err := resolveOutgoingPath(link, roots); !errors.Is(err, errForbidden) {
		t.Errorf("symlink escaping roots should be forbidden, got %v", err)
	}
	if _, err := resolveOutgoingPath(filepath.Join(outbox, "..", "whatsapp.db"), roots); !errors.Is(err, errForbidden) {
		t.Errorf("dot-dot escape should be forbidden, got %v", err)
	}
	if _, err := resolveOutgoingPath("relative.jpg", roots); !errors.Is(err, errBadRequest) {
		t.Errorf("relative path should be a bad request, got %v", err)
	}
	if _, err := resolveOutgoingPath(filepath.Join(outbox, "nope.jpg"), roots); !errors.Is(err, errBadRequest) {
		t.Errorf("missing file should be a bad request, got %v", err)
	}
	if _, err := resolveOutgoingPath(outbox, roots); !errors.Is(err, errBadRequest) {
		t.Errorf("directory should be a bad request, got %v", err)
	}
	if _, err := resolveOutgoingPath(ok, nil); !errors.Is(err, errForbidden) {
		t.Errorf("no roots means nothing is allowed, got %v", err)
	}
}

func TestRecipientAllowed(t *testing.T) {
	alice := types.NewJID("14155550001", types.DefaultUserServer)
	group := types.NewJID("120363000000000001", types.GroupServer)
	if !recipientAllowed(alice, nil) {
		t.Errorf("empty allowlist should allow everyone")
	}
	allowed := []string{"+1 (415) 555-0001", "120363000000000001@g.us", "garbage"}
	if !recipientAllowed(alice, allowed) || !recipientAllowed(group, allowed) {
		t.Errorf("listed recipients should be allowed")
	}
	if recipientAllowed(types.NewJID("14155550002", types.DefaultUserServer), allowed) {
		t.Errorf("unlisted recipient should be blocked")
	}
	// Device-suffixed JIDs match their base user.
	ad := types.NewADJID("14155550001", 0, 3)
	if !recipientAllowed(ad, allowed) {
		t.Errorf("AD JID should match base user")
	}
}

func TestRateLimiter(t *testing.T) {
	now := time.Date(2025, 4, 1, 0, 0, 0, 0, time.UTC)
	r := newRateLimiter(3)
	r.now = func() time.Time { return now }
	for i := 0; i < 3; i++ {
		if !r.allow() {
			t.Fatalf("attempt %d should be allowed", i)
		}
	}
	if r.allow() {
		t.Errorf("4th attempt within a minute should be blocked")
	}
	now = now.Add(61 * time.Second)
	if !r.allow() {
		t.Errorf("attempt after the window should be allowed")
	}
	if !newRateLimiter(0).allow() {
		t.Errorf("limit 0 disables limiting")
	}
	var nilLimiter *rateLimiter
	if !nilLimiter.allow() {
		t.Errorf("nil limiter allows")
	}
}

func TestParseConfig(t *testing.T) {
	t.Setenv("GRAAB_BRIDGE_TOKEN", "")
	t.Setenv("GRAAB_BRIDGE_ADDR", "")
	t.Setenv("GRAAB_MEDIA_ROOTS", "")
	t.Setenv("GRAAB_ALLOWED_RECIPIENTS", "")
	t.Setenv("GRAAB_READ_ONLY", "")
	t.Setenv("GRAAB_DEVICE_NAME", "")

	cfg, err := parseConfig([]string{"-store", "/tmp/graab-test"}, os.Stderr)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Addr != "127.0.0.1:8080" || cfg.SendPerMinute != 30 || cfg.ReadOnly || cfg.LogMessages || cfg.DeviceName != "Local MCP Bridge" {
		t.Errorf("defaults: %+v", cfg)
	}
	if c, _ := parseConfig([]string{"-device-name", "  "}, os.Stderr); c.DeviceName != "Local MCP Bridge" {
		t.Errorf("blank device name should fall back, got %q", c.DeviceName)
	}
	if c, _ := parseConfig([]string{"-device-name", "Fly bridge"}, os.Stderr); c.DeviceName != "Fly bridge" {
		t.Errorf("device name flag: %q", c.DeviceName)
	}
	if len(cfg.MediaRoots) != 2 || cfg.MediaRoots[0] != "/tmp/graab-test/media" {
		t.Errorf("default media roots: %v", cfg.MediaRoots)
	}

	if _, err := parseConfig([]string{"-addr", "0.0.0.0:8080"}, os.Stderr); err == nil {
		t.Errorf("non-loopback bind without token must be refused")
	}
	cfg, err = parseConfig([]string{"-addr", "0.0.0.0:8080", "-token", "s3cret", "-allow-recipients", " 123 , 456@g.us", "-read-only"}, os.Stderr)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.ReadOnly || len(cfg.AllowedRecipients) != 2 || cfg.AllowedRecipients[1] != "456@g.us" {
		t.Errorf("parsed: %+v", cfg)
	}

	t.Setenv("GRAAB_READ_ONLY", "true")
	t.Setenv("GRAAB_SEND_RATE", "5")
	cfg, _ = parseConfig(nil, os.Stderr)
	if !cfg.ReadOnly || cfg.SendPerMinute != 5 {
		t.Errorf("env overrides: %+v", cfg)
	}

	for _, a := range []string{"127.0.0.1:1", "[::1]:1", "localhost:1"} {
		if !isLoopbackAddr(a) {
			t.Errorf("%s should be loopback", a)
		}
	}
	for _, a := range []string{"0.0.0.0:1", "[::]:1", "10.0.0.1:1", "fly-local-6pn:1", "bad"} {
		if isLoopbackAddr(a) {
			t.Errorf("%s should not be loopback", a)
		}
	}
}
