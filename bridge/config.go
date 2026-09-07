package main

import (
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"go.mau.fi/whatsmeow/proto/waCompanionReg"
)

// Config is everything the bridge reads from flags and environment variables.
// Every flag has a GRAAB_* environment equivalent so the same binary works
// interactively and inside a container.
type Config struct {
	StoreDir          string
	Addr              string
	Token             string
	LogLevel          string
	PairPhone         string
	DeviceName        string
	DevicePlatform    string
	MediaRoots        []string
	AllowedRecipients []string
	ReadOnly          bool
	LogMessages       bool
	SendPerMinute     int
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envBool(key string, def bool) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	switch v {
	case "":
		return def
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// parseConfig reads flags (with env defaults) and validates the result.
func parseConfig(args []string, stderr io.Writer) (Config, error) {
	fs := flag.NewFlagSet("bridge", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var cfg Config
	var mediaRoots, allowed string
	fs.StringVar(&cfg.StoreDir, "store", envOr("GRAAB_STORE_DIR", "store"), "directory for the session and message databases (env GRAAB_STORE_DIR)")
	fs.StringVar(&cfg.Addr, "addr", envOr("GRAAB_BRIDGE_ADDR", "127.0.0.1:8080"), "address for the local REST API (env GRAAB_BRIDGE_ADDR)")
	fs.StringVar(&cfg.Token, "token", envOr("GRAAB_BRIDGE_TOKEN", ""), "bearer token required on API requests; mandatory when -addr is not loopback (env GRAAB_BRIDGE_TOKEN)")
	fs.StringVar(&cfg.LogLevel, "log-level", envOr("GRAAB_LOG_LEVEL", "INFO"), "whatsmeow log level: DEBUG, INFO, WARN, ERROR (env GRAAB_LOG_LEVEL)")
	fs.StringVar(&cfg.DeviceName, "device-name", envOr("GRAAB_DEVICE_NAME", "Local MCP Bridge"), "name shown in WhatsApp's Linked devices list; applied when pairing (env GRAAB_DEVICE_NAME)")
	fs.StringVar(&cfg.DevicePlatform, "device-platform", envOr("GRAAB_DEVICE_PLATFORM", "desktop"), "platform type reported to WhatsApp, which decides how the name is shown: "+strings.Join(platformNames(), ", ")+" (env GRAAB_DEVICE_PLATFORM)")
	fs.StringVar(&cfg.PairPhone, "pair-phone", envOr("GRAAB_PAIR_PHONE", ""), "pair headlessly with a code instead of a QR: your phone number with country code (env GRAAB_PAIR_PHONE)")
	fs.StringVar(&mediaRoots, "media-roots", envOr("GRAAB_MEDIA_ROOTS", ""), "directories files may be sent from, separated by "+string(os.PathListSeparator)+" (default: <store>/media and <store>/outbox) (env GRAAB_MEDIA_ROOTS)")
	fs.StringVar(&allowed, "allow-recipients", envOr("GRAAB_ALLOWED_RECIPIENTS", ""), "comma-separated phone numbers or JIDs that may be messaged; empty allows all (env GRAAB_ALLOWED_RECIPIENTS)")
	fs.BoolVar(&cfg.ReadOnly, "read-only", envBool("GRAAB_READ_ONLY", false), "refuse all sends; the archive and downloads still work (env GRAAB_READ_ONLY)")
	fs.BoolVar(&cfg.LogMessages, "log-messages", envBool("GRAAB_LOG_MESSAGES", false), "print message contents to stdout as they arrive (env GRAAB_LOG_MESSAGES)")
	fs.IntVar(&cfg.SendPerMinute, "send-rate", envInt("GRAAB_SEND_RATE", 30), "maximum messages sent per minute; 0 disables the limit (env GRAAB_SEND_RATE)")
	if err := fs.Parse(args); err != nil {
		return cfg, err
	}

	abs, err := filepath.Abs(cfg.StoreDir)
	if err != nil {
		return cfg, err
	}
	cfg.StoreDir = abs

	if mediaRoots == "" {
		cfg.MediaRoots = []string{filepath.Join(abs, "media"), filepath.Join(abs, "outbox")}
	} else {
		for _, r := range filepath.SplitList(mediaRoots) {
			if r = strings.TrimSpace(r); r != "" {
				if ra, err := filepath.Abs(r); err == nil {
					cfg.MediaRoots = append(cfg.MediaRoots, ra)
				}
			}
		}
	}
	for _, r := range strings.Split(allowed, ",") {
		if r = strings.TrimSpace(r); r != "" {
			cfg.AllowedRecipients = append(cfg.AllowedRecipients, r)
		}
	}
	if cfg.SendPerMinute < 0 {
		cfg.SendPerMinute = 0
	}
	cfg.DeviceName = strings.TrimSpace(cfg.DeviceName)
	if cfg.DeviceName == "" {
		cfg.DeviceName = "Local MCP Bridge"
	}
	cfg.DevicePlatform = strings.ToLower(strings.TrimSpace(cfg.DevicePlatform))
	if _, ok := platformTypes[cfg.DevicePlatform]; !ok {
		return cfg, fmt.Errorf("unknown -device-platform %q; use one of %s", cfg.DevicePlatform, strings.Join(platformNames(), ", "))
	}

	if !isLoopbackAddr(cfg.Addr) && cfg.Token == "" {
		return cfg, fmt.Errorf("refusing to serve the API on %s without a token: anyone who can reach it could send messages from your account. Set -token / GRAAB_BRIDGE_TOKEN or bind to 127.0.0.1", cfg.Addr)
	}
	return cfg, nil
}

// isLoopbackAddr reports whether host:port refers to a loopback interface.
func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// platformTypes maps the -device-platform names to WhatsApp's platform enum.
// WhatsApp renders the linked device from this plus the device name.
// Verified on iOS (2026-09): DESKTOP shows the bare name ("Local MCP
// Bridge"), UNKNOWN shows "Other device" and hides the name entirely.
// Browser types render as "Chrome (name)". Keep DESKTOP as the default.
var platformTypes = map[string]waCompanionReg.DeviceProps_PlatformType{
	"desktop": waCompanionReg.DeviceProps_DESKTOP,
	"chrome":  waCompanionReg.DeviceProps_CHROME,
	"firefox": waCompanionReg.DeviceProps_FIREFOX,
	"edge":    waCompanionReg.DeviceProps_EDGE,
	"safari":  waCompanionReg.DeviceProps_SAFARI,
	"opera":   waCompanionReg.DeviceProps_OPERA,
	"unknown": waCompanionReg.DeviceProps_UNKNOWN,
}

func platformNames() []string {
	names := make([]string, 0, len(platformTypes))
	for n := range platformTypes {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// PlatformType returns the enum value for the configured platform name.
func (c Config) PlatformType() waCompanionReg.DeviceProps_PlatformType {
	return platformTypes[c.DevicePlatform]
}
