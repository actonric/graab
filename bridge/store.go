package main

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// timeLayout is how timestamps are written to SQLite. It is ISO-8601 in UTC so
// the Python side can parse it with datetime.fromisoformat and SQLite can sort
// and compare it as plain text.
const timeLayout = "2006-01-02T15:04:05Z"

const schema = `
CREATE TABLE IF NOT EXISTS chats (
	jid               TEXT PRIMARY KEY,
	name              TEXT NOT NULL DEFAULT '',
	is_group          INTEGER NOT NULL DEFAULT 0,
	last_message_time TEXT
);

CREATE TABLE IF NOT EXISTS contacts (
	jid        TEXT PRIMARY KEY,
	phone      TEXT NOT NULL DEFAULT '',
	name       TEXT NOT NULL DEFAULT '',
	push_name  TEXT NOT NULL DEFAULT '',
	updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS messages (
	id              TEXT NOT NULL,
	chat_jid        TEXT NOT NULL,
	sender          TEXT NOT NULL DEFAULT '',
	content         TEXT NOT NULL DEFAULT '',
	timestamp       TEXT NOT NULL,
	is_from_me      INTEGER NOT NULL DEFAULT 0,
	media_type      TEXT NOT NULL DEFAULT '',
	filename        TEXT NOT NULL DEFAULT '',
	mime_type       TEXT NOT NULL DEFAULT '',
	url             TEXT NOT NULL DEFAULT '',
	direct_path     TEXT NOT NULL DEFAULT '',
	media_key       BLOB,
	file_sha256     BLOB,
	file_enc_sha256 BLOB,
	file_length     INTEGER NOT NULL DEFAULT 0,
	quoted_id       TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (id, chat_jid),
	FOREIGN KEY (chat_jid) REFERENCES chats(jid)
);

CREATE INDEX IF NOT EXISTS idx_messages_chat_time ON messages (chat_jid, timestamp);
CREATE INDEX IF NOT EXISTS idx_messages_time      ON messages (timestamp);
CREATE INDEX IF NOT EXISTS idx_messages_sender    ON messages (sender);
CREATE INDEX IF NOT EXISTS idx_chats_last         ON chats (last_message_time);
`

// Store is the message database shared with the MCP server. The bridge is the
// only writer; the Python side opens the same file read-only.
type Store struct {
	db *sql.DB
	mu sync.Mutex
}

// Chat is one row of the chats table.
type Chat struct {
	JID             string
	Name            string
	IsGroup         bool
	LastMessageTime time.Time
}

// MediaInfo is everything needed to download an attachment later.
type MediaInfo struct {
	Type          string // image, video, audio, document, sticker
	Filename      string
	MimeType      string
	URL           string
	DirectPath    string
	MediaKey      []byte
	FileSHA256    []byte
	FileEncSHA256 []byte
	FileLength    uint64
}

// StoredMessage is one row of the messages table.
type StoredMessage struct {
	ID        string
	ChatJID   string
	Sender    string
	Content   string
	Timestamp time.Time
	IsFromMe  bool
	Media     *MediaInfo
	QuotedID  string
}

// OpenStore opens (and migrates) the SQLite database at path.
func OpenStore(path string) (*Store, error) {
	dsn := fmt.Sprintf("file:%s?_foreign_keys=on&_journal_mode=WAL&_busy_timeout=5000", path)
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("open message database: %w", err)
	}
	// A single connection keeps writes serialized; WAL lets readers proceed.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("create schema: %w", err)
	}
	return &Store{db: db}, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

func formatTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC().Format(timeLayout)
}

func parseTime(v sql.NullString) time.Time {
	if !v.Valid || v.String == "" {
		return time.Time{}
	}
	t, err := time.Parse(timeLayout, v.String)
	if err != nil {
		return time.Time{}
	}
	return t
}

// UpsertChat inserts or updates a chat. An empty name never overwrites an
// existing one, and last_message_time only moves forward.
func (s *Store) UpsertChat(jid, name string, isGroup bool, last time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`
		INSERT INTO chats (jid, name, is_group, last_message_time) VALUES (?, ?, ?, ?)
		ON CONFLICT(jid) DO UPDATE SET
			name = CASE WHEN excluded.name != '' THEN excluded.name ELSE chats.name END,
			is_group = excluded.is_group,
			last_message_time = CASE
				WHEN excluded.last_message_time IS NOT NULL
				 AND (chats.last_message_time IS NULL OR excluded.last_message_time > chats.last_message_time)
				THEN excluded.last_message_time
				ELSE chats.last_message_time END`,
		jid, name, boolInt(isGroup), formatTime(last))
	return err
}

// RenameChatIfDefault sets a chat's name only when it is empty or is just the
// bare phone number / JID user part. Used when a real contact name arrives late.
func (s *Store) RenameChatIfDefault(jid, name string) error {
	if name == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`
		UPDATE chats SET name = ?
		WHERE jid = ? AND (name = '' OR name = substr(jid, 1, instr(jid, '@') - 1))`,
		name, jid)
	return err
}

// GetChat returns the chat with the given JID, or nil if it does not exist.
func (s *Store) GetChat(jid string) (*Chat, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var c Chat
	var isGroup int
	var last sql.NullString
	err := s.db.QueryRow(`SELECT jid, name, is_group, last_message_time FROM chats WHERE jid = ?`, jid).
		Scan(&c.JID, &c.Name, &isGroup, &last)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	c.IsGroup = isGroup != 0
	c.LastMessageTime = parseTime(last)
	return &c, nil
}

// UpsertContact records a contact. Empty fields never overwrite non-empty ones.
func (s *Store) UpsertContact(jid, phone, name, pushName string) error {
	if jid == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`
		INSERT INTO contacts (jid, phone, name, push_name, updated_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(jid) DO UPDATE SET
			phone = CASE WHEN excluded.phone != '' THEN excluded.phone ELSE contacts.phone END,
			name = CASE WHEN excluded.name != '' THEN excluded.name ELSE contacts.name END,
			push_name = CASE WHEN excluded.push_name != '' THEN excluded.push_name ELSE contacts.push_name END,
			updated_at = excluded.updated_at`,
		jid, phone, name, pushName, formatTime(time.Now()))
	return err
}

// ContactName returns the best known display name for a contact JID: the
// address-book name first, then the push name, then "".
func (s *Store) ContactName(jid string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var name, push string
	err := s.db.QueryRow(`SELECT name, push_name FROM contacts WHERE jid = ?`, jid).Scan(&name, &push)
	if err != nil {
		return ""
	}
	if name != "" {
		return name
	}
	return push
}

// InsertMessage stores a message, replacing any existing row with the same
// (id, chat_jid). Messages with neither text nor media are ignored.
func (s *Store) InsertMessage(m *StoredMessage) error {
	if m.Content == "" && m.Media == nil {
		return nil
	}
	if m.ID == "" || m.ChatJID == "" {
		return fmt.Errorf("message id and chat jid are required")
	}
	media := m.Media
	if media == nil {
		media = &MediaInfo{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`
		INSERT OR REPLACE INTO messages
			(id, chat_jid, sender, content, timestamp, is_from_me, media_type, filename, mime_type,
			 url, direct_path, media_key, file_sha256, file_enc_sha256, file_length, quoted_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.ChatJID, m.Sender, m.Content, formatTime(m.Timestamp), boolInt(m.IsFromMe),
		media.Type, media.Filename, media.MimeType, media.URL, media.DirectPath,
		media.MediaKey, media.FileSHA256, media.FileEncSHA256, media.FileLength, m.QuotedID)
	return err
}

// UpdateMessageContent replaces the text of an existing message (edits).
func (s *Store) UpdateMessageContent(id, chatJID, content string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.Exec(`UPDATE messages SET content = ? WHERE id = ? AND chat_jid = ?`, content, id, chatJID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// GetMessage returns a stored message, or nil if it does not exist.
func (s *Store) GetMessage(id, chatJID string) (*StoredMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var m StoredMessage
	var media MediaInfo
	var ts sql.NullString
	var fromMe int
	err := s.db.QueryRow(`
		SELECT id, chat_jid, sender, content, timestamp, is_from_me, media_type, filename, mime_type,
		       url, direct_path, media_key, file_sha256, file_enc_sha256, file_length, quoted_id
		FROM messages WHERE id = ? AND chat_jid = ?`, id, chatJID).
		Scan(&m.ID, &m.ChatJID, &m.Sender, &m.Content, &ts, &fromMe, &media.Type, &media.Filename,
			&media.MimeType, &media.URL, &media.DirectPath, &media.MediaKey, &media.FileSHA256,
			&media.FileEncSHA256, &media.FileLength, &m.QuotedID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	m.Timestamp = parseTime(ts)
	m.IsFromMe = fromMe != 0
	if media.Type != "" {
		m.Media = &media
	}
	return &m, nil
}

// Counts returns the number of chats and messages, for status reporting.
func (s *Store) Counts() (chats, messages int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM chats`).Scan(&chats); err != nil {
		return
	}
	err = s.db.QueryRow(`SELECT COUNT(*) FROM messages`).Scan(&messages)
	return
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// safeFileName strips path separators and other characters that should not
// end up in a file name derived from untrusted input.
func safeFileName(name string) string {
	name = strings.TrimSpace(name)
	replacer := strings.NewReplacer("/", "_", "\\", "_", "..", "_", ":", "_", "\x00", "")
	name = replacer.Replace(name)
	if name == "" || name == "." {
		return "file"
	}
	return name
}
