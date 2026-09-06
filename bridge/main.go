// Command bridge links a WhatsApp account to a local SQLite database and
// exposes a small HTTP API for sending messages and downloading media.
//
// It is one half of Graab: the other half is the Python MCP server in
// ../mcp-server, which reads the database and calls this API.
package main

import (
	"context"
	"errors"
	"flag"
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

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	storeDir := flag.String("store", envOr("GRAAB_STORE_DIR", "store"), "directory for the session and message databases (env GRAAB_STORE_DIR)")
	addr := flag.String("addr", envOr("GRAAB_BRIDGE_ADDR", "127.0.0.1:8080"), "address for the local REST API (env GRAAB_BRIDGE_ADDR)")
	logLevel := flag.String("log-level", envOr("GRAAB_LOG_LEVEL", "INFO"), "whatsmeow log level: DEBUG, INFO, WARN, ERROR")
	flag.Parse()

	if err := run(*storeDir, *addr, *logLevel); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(storeDir, addr, logLevel string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger := waLog.Stdout("Bridge", logLevel, true)

	absStore, err := filepath.Abs(storeDir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(absStore, 0o755); err != nil {
		return fmt.Errorf("create store directory: %w", err)
	}

	// Session (keys, device identity) lives in whatsapp.db, managed by whatsmeow.
	sessionDSN := fmt.Sprintf("file:%s?_foreign_keys=on&_journal_mode=WAL&_busy_timeout=5000", filepath.Join(absStore, "whatsapp.db"))
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
	msgStore, err := OpenStore(filepath.Join(absStore, "messages.db"))
	if err != nil {
		return err
	}
	defer msgStore.Close()

	client := whatsmeow.NewClient(device, waLog.Stdout("WhatsApp", logLevel, true))
	bridge := &Bridge{client: client, store: msgStore, storeDir: absStore, log: logger}
	client.AddEventHandler(bridge.handleEvent)

	if err := connect(ctx, client, logger); err != nil {
		return err
	}
	defer client.Disconnect()

	// Serve the local API. Default bind is loopback only: anyone who can
	// reach this port can send messages from your account.
	srv := &http.Server{
		Addr:              addr,
		Handler:           newAPIHandler(bridge),
		ReadHeaderTimeout: 10 * time.Second,
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", addr, err)
	}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Errorf("API server: %v", err)
		}
	}()
	logger.Infof("REST API listening on http://%s (store: %s)", ln.Addr(), absStore)
	fmt.Println("\n✓ Bridge is running. Messages are being mirrored to", filepath.Join(absStore, "messages.db"))
	fmt.Println("  Press Ctrl+C to stop.")

	<-ctx.Done()
	fmt.Println("\nShutting down…")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	return nil
}

// connect logs in, showing a QR code for first-time pairing.
func connect(ctx context.Context, client *whatsmeow.Client, logger waLog.Logger) error {
	if client.Store.ID != nil {
		if err := client.Connect(); err != nil {
			return fmt.Errorf("connect: %w", err)
		}
		logger.Infof("Reusing existing session for %s", client.Store.ID.ToNonAD())
		return nil
	}

	qrChan, err := client.GetQRChannel(ctx)
	if err != nil {
		return fmt.Errorf("get QR channel: %w", err)
	}
	if err := client.Connect(); err != nil {
		return fmt.Errorf("connect: %w", err)
	}

	fmt.Println("\nNo session found. On your phone open WhatsApp → Settings → Linked devices → Link a device,")
	fmt.Println("then scan the QR code below.")
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
				fmt.Println()
				qrterminal.GenerateHalfBlock(item.Code, qrterminal.L, os.Stdout)
				fmt.Println("\n(The code refreshes every few seconds; keep this window open.)")
			case "success":
				fmt.Println("\n✓ Paired successfully. History will sync from your phone over the next minutes.")
				return nil
			case "timeout":
				return errors.New("QR code timed out; restart the bridge to try again")
			case "error":
				return fmt.Errorf("pairing error: %v", item.Error)
			default:
				logger.Infof("QR event: %s", item.Event)
			}
		}
	}
}
