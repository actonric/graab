// Command bridge links a WhatsApp account to a local SQLite database and
// exposes a small HTTP API for sending messages and downloading media.
//
// It is one half of Graab: the other half is the Python MCP server in
// ../mcp-server, which reads the database and calls this API.
package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/mdp/qrterminal/v3"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"
)

func main() {
	cfg, err := parseConfig(os.Args[1:], os.Stderr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(2)
	}
	if err := run(cfg); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(cfg Config) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger := waLog.Stdout("Bridge", cfg.LogLevel, true)

	// Only this user should be able to read session keys and messages.
	if err := os.MkdirAll(cfg.StoreDir, 0o700); err != nil {
		return fmt.Errorf("create store directory: %w", err)
	}
	_ = os.Chmod(cfg.StoreDir, 0o700)
	for _, sub := range []string{"media", "outbox"} {
		_ = os.MkdirAll(filepath.Join(cfg.StoreDir, sub), 0o700)
	}

	// Session (keys, device identity) lives in whatsapp.db, managed by whatsmeow.
	sessionPath := filepath.Join(cfg.StoreDir, "whatsapp.db")
	sessionDSN := fmt.Sprintf("file:%s?_foreign_keys=on&_journal_mode=WAL&_busy_timeout=5000", sessionPath)
	container, err := sqlstore.New(ctx, "sqlite3", sessionDSN, waLog.Stdout("Session", "WARN", true))
	if err != nil {
		return fmt.Errorf("open session database: %w", err)
	}
	device, err := container.GetFirstDevice(ctx)
	if err != nil {
		return fmt.Errorf("load device: %w", err)
	}
	store.DeviceProps.Os = proto.String("Graab")

	// Messages live in messages.db, shared read-only with the MCP server.
	msgPath := filepath.Join(cfg.StoreDir, "messages.db")
	msgStore, err := OpenStore(msgPath)
	if err != nil {
		return err
	}
	defer msgStore.Close()
	restrictPerms(sessionPath, msgPath)

	client := whatsmeow.NewClient(device, waLog.Stdout("WhatsApp", cfg.LogLevel, true))
	bridge := &Bridge{
		client: client, store: msgStore, storeDir: cfg.StoreDir, log: logger,
		cfg: cfg, limiter: newRateLimiter(cfg.SendPerMinute),
	}
	client.AddEventHandler(bridge.handleEvent)

	// Serve the API before pairing so status is observable on headless hosts
	// (sends fail cleanly with "not connected" until pairing completes).
	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           requireBearer(cfg.Token, newAPIHandler(bridge)),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      6 * time.Minute, // downloads can be slow
		IdleTimeout:       2 * time.Minute,
	}
	ln, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.Addr, err)
	}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Errorf("API server: %v", err)
		}
	}()
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	auth := "no token (loopback only)"
	if cfg.Token != "" {
		auth = "bearer token required"
	}
	logger.Infof("REST API listening on http://%s (%s)", ln.Addr(), auth)
	logger.Infof("Store: %s · read-only: %v · files may be sent from: %v · rate limit: %d/min",
		cfg.StoreDir, cfg.ReadOnly, cfg.MediaRoots, cfg.SendPerMinute)
	if len(cfg.AllowedRecipients) > 0 {
		logger.Infof("Allowed recipients: %v", cfg.AllowedRecipients)
	}

	if err := connect(ctx, client, cfg, logger); err != nil {
		return err
	}
	defer client.Disconnect()
	restrictPerms(sessionPath, msgPath)

	fmt.Println("\n✓ Bridge is running. Messages are being mirrored to", msgPath)
	if !cfg.LogMessages {
		fmt.Println("  (message contents are not logged; pass -log-messages to see them)")
	}
	fmt.Println("  Press Ctrl+C to stop.")

	<-ctx.Done()
	fmt.Println("\nShutting down…")
	return nil
}

// connectWithRetry opens the WhatsApp websocket, retrying with backoff so a
// network blip at boot does not crash-loop a server. Once connected,
// whatsmeow's own auto-reconnect takes over.
func connectWithRetry(ctx context.Context, client *whatsmeow.Client, logger waLog.Logger) error {
	delay := 2 * time.Second
	for attempt := 1; ; attempt++ {
		err := client.Connect()
		if err == nil {
			return nil
		}
		if attempt >= 8 {
			return fmt.Errorf("connect: %w (gave up after %d attempts)", err, attempt)
		}
		logger.Warnf("Connect attempt %d failed: %v; retrying in %s", attempt, err, delay)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
		if delay < 30*time.Second {
			delay *= 2
		}
	}
}

// restrictPerms makes the databases (and their WAL side files) owner-only.
func restrictPerms(paths ...string) {
	for _, p := range paths {
		for _, suffix := range []string{"", "-wal", "-shm"} {
			if _, err := os.Stat(p + suffix); err == nil {
				_ = os.Chmod(p+suffix, 0o600)
			}
		}
	}
}

// connect logs in, pairing first if there is no session: by phone-number
// code when -pair-phone is set (headless servers), otherwise with a QR code.
func connect(ctx context.Context, client *whatsmeow.Client, cfg Config, logger waLog.Logger) error {
	if client.Store.ID != nil {
		if err := connectWithRetry(ctx, client, logger); err != nil {
			return err
		}
		logger.Infof("Reusing existing session for %s", client.Store.ID.ToNonAD())
		return nil
	}

	qrChan, err := client.GetQRChannel(ctx)
	if err != nil {
		return fmt.Errorf("get QR channel: %w", err)
	}
	if err := connectWithRetry(ctx, client, logger); err != nil {
		return err
	}

	usingCode := cfg.PairPhone != ""
	if usingCode {
		fmt.Println("\nNo session found. Pairing by code: waiting for the WhatsApp server…")
	} else {
		fmt.Println("\nNo session found. On your phone open WhatsApp → Settings → Linked devices → Link a device,")
		fmt.Println("then scan the QR code below.")
	}

	codeShown := false
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case item, ok := <-qrChan:
			if !ok {
				return errors.New("QR channel closed before pairing completed")
			}
			switch item.Event {
			case "code":
				if usingCode {
					if codeShown {
						continue
					}
					codeShown = true
					phone := nonDigits.ReplaceAllString(cfg.PairPhone, "")
					code, err := client.PairPhone(ctx, phone, true, whatsmeow.PairClientChrome, "Chrome (Linux)")
					if err != nil {
						return fmt.Errorf("request pairing code: %w", err)
					}
					fmt.Printf("\nOn the phone with number +%s open WhatsApp → Settings → Linked devices → Link a device →\n", phone)
					fmt.Printf("\"Link with phone number instead\", and enter this code:\n\n    %s\n\n", code)
					fmt.Println("(The code expires in about two minutes.)")
					continue
				}
				fmt.Println()
				qrterminal.GenerateHalfBlock(item.Code, qrterminal.L, os.Stdout)
				fmt.Println("\n(The code refreshes every few seconds; keep this window open.)")
			case "success":
				fmt.Println("\n✓ Paired successfully. History will sync from your phone over the next minutes.")
				return nil
			case "timeout":
				return errors.New("pairing timed out; restart the bridge to try again")
			case "error":
				return fmt.Errorf("pairing error: %v", item.Error)
			default:
				logger.Infof("Pairing event: %s", item.Event)
			}
		}
	}
}
