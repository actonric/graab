package main

import (
	"database/sql"
	"encoding/json"
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

CREATE TABLE IF NOT EXISTS polls (
	message_id  TEXT NOT NULL,
	chat_jid    TEXT NOT NULL,
	sender_jid  TEXT NOT NULL DEFAULT '',
	is_from_me  INTEGER NOT NULL DEFAULT 0,
	name        TEXT NOT NULL DEFAULT '',
	options     TEXT NOT NULL DEFAULT '[]',
	selectable  INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (message_id, chat_jid)
);

CREATE TABLE IF NOT EXISTS poll_votes (
	poll_id    TEXT NOT NULL,
	chat_jid   TEXT NOT NULL,
	voter      TEXT NOT NULL,
	selected   TEXT NOT NULL DEFAULT '[]',
	timestamp  TEXT NOT NULL,
	PRIMARY KEY (poll_id, chat_jid, voter)
);

CREATE TABLE IF NOT EXISTS events (
	message_id           TEXT NOT NULL,
	chat_jid             TEXT NOT NULL,
	sender_jid           TEXT NOT NULL DEFAULT '',
	is_from_me           INTEGER NOT NULL DEFAULT 0,
	name                 TEXT NOT NULL DEFAULT '',
	description          TEXT NOT NULL DEFAULT '',
	start_time           TEXT,
	end_time             TEXT,
	location_name        TEXT NOT NULL DEFAULT '',
	location_address     TEXT NOT NULL DEFAULT '',
	latitude             REAL,
	longitude            REAL,
	join_link            TEXT NOT NULL DEFAULT '',
	is_canceled          INTEGER NOT NULL DEFAULT 0,
	extra_guests_allowed INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (message_id, chat_jid)
);

CREATE TABLE IF NOT EXISTS event_responses (
	event_id     TEXT NOT NULL,
	chat_jid     TEXT NOT NULL,
	responder    TEXT NOT NULL,
	response     TEXT NOT NULL,
	extra_guests INTEGER NOT NULL DEFAULT 0,
	timestamp    TEXT NOT NULL,
	PRIMARY KEY (event_id, chat_jid, responder)
);

CREATE INDEX IF NOT EXISTS idx_messages_chat_time ON messages (chat_jid, timestamp);
CREATE INDEX IF NOT EXISTS idx_events_start       ON events (start_time);
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

// StoredPoll is one row of the polls table. SenderJID is the exact address
// whatsmeow reported for the creator (it may be a @lid address); voting needs
// it to find the poll's secret key.
type StoredPoll struct {
	MessageID  string
	ChatJID    string
	SenderJID  string
	IsFromMe   bool
	Name       string
	Options    []string
	Selectable int // maximum selections; 0 means any number
}

// PollVote is one voter's current selection in a poll. An empty Selected
// means the vote was retracted.
type PollVote struct {
	PollID    string
	ChatJID   string
	Voter     string
	Selected  []string
	Timestamp time.Time
}

// StoredEvent is one row of the events table (a WhatsApp calendar event).
type StoredEvent struct {
	MessageID          string
	ChatJID            string
	SenderJID          string
	IsFromMe           bool
	Name               string
	Description        string
	StartTime          time.Time
	EndTime            time.Time
	LocationName       string
	LocationAddress    string
	Latitude           *float64
	Longitude          *float64
	JoinLink           string
	Canceled           bool
	ExtraGuestsAllowed bool
}

// EventResponse is one person's RSVP to an event.
type EventResponse struct {
	EventID     string
	ChatJID     string
	Responder   string
	Response    string // going, not_going, maybe
	ExtraGuests int
	Timestamp   time.Time
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

func jsonStrings(v []string) string {
	if v == nil {
		v = []string{}
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func parseJSONStrings(s string) []string {
	var out []string
	if err := json.Unmarshal([]byte(s), &out); err != nil || out == nil {
		return []string{}
	}
	return out
}

// UpsertPoll records a poll's question and options. The creator's address is
// kept from the first insert unless the new one is non-empty.
func (s *Store) UpsertPoll(p *StoredPoll) error {
	if p.MessageID == "" || p.ChatJID == "" {
		return fmt.Errorf("poll message id and chat jid are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`
		INSERT INTO polls (message_id, chat_jid, sender_jid, is_from_me, name, options, selectable)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(message_id, chat_jid) DO UPDATE SET
			sender_jid = CASE WHEN excluded.sender_jid != '' THEN excluded.sender_jid ELSE polls.sender_jid END,
			is_from_me = excluded.is_from_me,
			name = excluded.name, options = excluded.options, selectable = excluded.selectable`,
		p.MessageID, p.ChatJID, p.SenderJID, boolInt(p.IsFromMe), p.Name, jsonStrings(p.Options), p.Selectable)
	return err
}

// GetPoll returns a stored poll, or nil if it does not exist.
func (s *Store) GetPoll(messageID, chatJID string) (*StoredPoll, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var p StoredPoll
	var fromMe int
	var options string
	err := s.db.QueryRow(`SELECT message_id, chat_jid, sender_jid, is_from_me, name, options, selectable
		FROM polls WHERE message_id = ? AND chat_jid = ?`, messageID, chatJID).
		Scan(&p.MessageID, &p.ChatJID, &p.SenderJID, &fromMe, &p.Name, &options, &p.Selectable)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	p.IsFromMe = fromMe != 0
	p.Options = parseJSONStrings(options)
	return &p, nil
}

// UpsertPollVote stores a voter's latest selection. An older vote never
// overwrites a newer one.
func (s *Store) UpsertPollVote(v *PollVote) error {
	if v.PollID == "" || v.ChatJID == "" || v.Voter == "" {
		return fmt.Errorf("poll id, chat jid and voter are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`
		INSERT INTO poll_votes (poll_id, chat_jid, voter, selected, timestamp) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(poll_id, chat_jid, voter) DO UPDATE SET
			selected = excluded.selected, timestamp = excluded.timestamp
		WHERE excluded.timestamp >= poll_votes.timestamp`,
		v.PollID, v.ChatJID, v.Voter, jsonStrings(v.Selected), formatTime(v.Timestamp))
	return err
}

// PollVotes lists the current votes for a poll, oldest first.
func (s *Store) PollVotes(pollID, chatJID string) ([]PollVote, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT poll_id, chat_jid, voter, selected, timestamp FROM poll_votes
		WHERE poll_id = ? AND chat_jid = ? ORDER BY timestamp`, pollID, chatJID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PollVote
	for rows.Next() {
		var v PollVote
		var selected string
		var ts sql.NullString
		if err := rows.Scan(&v.PollID, &v.ChatJID, &v.Voter, &selected, &ts); err != nil {
			return nil, err
		}
		v.Selected = parseJSONStrings(selected)
		v.Timestamp = parseTime(ts)
		out = append(out, v)
	}
	return out, rows.Err()
}

// UpsertEvent records or updates a calendar event. As with polls, the
// creator's address survives updates that do not know it.
func (s *Store) UpsertEvent(e *StoredEvent) error {
	if e.MessageID == "" || e.ChatJID == "" {
		return fmt.Errorf("event message id and chat jid are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`
		INSERT INTO events (message_id, chat_jid, sender_jid, is_from_me, name, description, start_time, end_time,
			location_name, location_address, latitude, longitude, join_link, is_canceled, extra_guests_allowed)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(message_id, chat_jid) DO UPDATE SET
			sender_jid = CASE WHEN excluded.sender_jid != '' THEN excluded.sender_jid ELSE events.sender_jid END,
			is_from_me = excluded.is_from_me,
			name = excluded.name, description = excluded.description,
			start_time = excluded.start_time, end_time = excluded.end_time,
			location_name = excluded.location_name, location_address = excluded.location_address,
			latitude = excluded.latitude, longitude = excluded.longitude,
			join_link = excluded.join_link, is_canceled = excluded.is_canceled,
			extra_guests_allowed = excluded.extra_guests_allowed`,
		e.MessageID, e.ChatJID, e.SenderJID, boolInt(e.IsFromMe), e.Name, e.Description,
		formatTime(e.StartTime), formatTime(e.EndTime), e.LocationName, e.LocationAddress,
		e.Latitude, e.Longitude, e.JoinLink, boolInt(e.Canceled), boolInt(e.ExtraGuestsAllowed))
	return err
}

// GetEvent returns a stored event, or nil if it does not exist.
func (s *Store) GetEvent(messageID, chatJID string) (*StoredEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var e StoredEvent
	var fromMe, canceled, extra int
	var start, end sql.NullString
	var lat, lon sql.NullFloat64
	err := s.db.QueryRow(`SELECT message_id, chat_jid, sender_jid, is_from_me, name, description, start_time, end_time,
			location_name, location_address, latitude, longitude, join_link, is_canceled, extra_guests_allowed
		FROM events WHERE message_id = ? AND chat_jid = ?`, messageID, chatJID).
		Scan(&e.MessageID, &e.ChatJID, &e.SenderJID, &fromMe, &e.Name, &e.Description, &start, &end,
			&e.LocationName, &e.LocationAddress, &lat, &lon, &e.JoinLink, &canceled, &extra)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	e.IsFromMe, e.Canceled, e.ExtraGuestsAllowed = fromMe != 0, canceled != 0, extra != 0
	e.StartTime, e.EndTime = parseTime(start), parseTime(end)
	if lat.Valid {
		e.Latitude = &lat.Float64
	}
	if lon.Valid {
		e.Longitude = &lon.Float64
	}
	return &e, nil
}

// UpsertEventResponse stores a person's latest RSVP; older ones never win.
func (s *Store) UpsertEventResponse(r *EventResponse) error {
	if r.EventID == "" || r.ChatJID == "" || r.Responder == "" {
		return fmt.Errorf("event id, chat jid and responder are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`
		INSERT INTO event_responses (event_id, chat_jid, responder, response, extra_guests, timestamp)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(event_id, chat_jid, responder) DO UPDATE SET
			response = excluded.response, extra_guests = excluded.extra_guests, timestamp = excluded.timestamp
		WHERE excluded.timestamp >= event_responses.timestamp`,
		r.EventID, r.ChatJID, r.Responder, r.Response, r.ExtraGuests, formatTime(r.Timestamp))
	return err
}

// EventResponses lists the current RSVPs for an event, oldest first.
func (s *Store) EventResponses(eventID, chatJID string) ([]EventResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT event_id, chat_jid, responder, response, extra_guests, timestamp
		FROM event_responses WHERE event_id = ? AND chat_jid = ? ORDER BY timestamp`, eventID, chatJID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EventResponse
	for rows.Next() {
		var r EventResponse
		var ts sql.NullString
		if err := rows.Scan(&r.EventID, &r.ChatJID, &r.Responder, &r.Response, &r.ExtraGuests, &ts); err != nil {
			return nil, err
		}
		r.Timestamp = parseTime(ts)
		out = append(out, r)
	}
	return out, rows.Err()
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
