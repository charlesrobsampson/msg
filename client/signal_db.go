package client

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

type SignalDB struct {
	db *sql.DB
}

func OpenSignalDB() (*SignalDB, error) {
	return OpenSignalDBForProfile(GetProfile())
}

// OpenSignalDBForProfile opens the signal cache DB for a specific profile name.
// Use "" for the default profile.
func OpenSignalDBForProfile(profile string) (*SignalDB, error) {
	home, _ := os.UserHomeDir()
	dbName := "signal-cache.db"
	if profile != "" {
		dbName = "signal-cache-" + profile + ".db"
	}
	dbPath := filepath.Join(home, ".config", "msg", dbName)

	os.MkdirAll(filepath.Dir(dbPath), 0755)

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open signal cache db: %v", err)
	}

	sdb := &SignalDB{db: db}
	if err := sdb.init(); err != nil {
		db.Close()
		return nil, err
	}

	return sdb, nil
}

func (s *SignalDB) Close() error {
	return s.db.Close()
}

func (s *SignalDB) init() error {
	ddl := []string{
		`CREATE TABLE IF NOT EXISTS messages (
			id TEXT PRIMARY KEY,
			conversation_id TEXT,
			sender_name TEXT,
			body TEXT,
			is_from_me INTEGER,
			timestamp_ms INTEGER
		)`,
		`CREATE INDEX IF NOT EXISTS idx_messages_conversation_id ON messages(conversation_id)`,
		`CREATE INDEX IF NOT EXISTS idx_messages_timestamp_ms ON messages(timestamp_ms)`,
		`CREATE TABLE IF NOT EXISTS conversations (
			id TEXT PRIMARY KEY,
			name TEXT,
			is_group INTEGER,
			unread_count INTEGER,
			last_message_ts INTEGER
		)`,
	}
	for _, q := range ddl {
		if _, err := s.db.Exec(q); err != nil {
			return fmt.Errorf("failed to initialize signal cache db: %v", err)
		}
	}

	// Migrations: errors ignored because SQLite rejects ADD COLUMN on existing columns.
	migrations := []string{
		`ALTER TABLE messages ADD COLUMN attachment_id TEXT DEFAULT ''`,
		`ALTER TABLE messages ADD COLUMN mime_type TEXT DEFAULT ''`,
		`ALTER TABLE messages ADD COLUMN attachments TEXT DEFAULT ''`,
		`ALTER TABLE messages ADD COLUMN reactions TEXT DEFAULT ''`,
		`ALTER TABLE messages ADD COLUMN sender_number TEXT DEFAULT ''`,
	}
	for _, q := range migrations {
		s.db.Exec(q)
	}

	return nil
}

// SaveMessage persists a message with zero or more attachments.
// attachments is a slice of RawAttachment (ID = signal attachment ID or local path).
func (s *SignalDB) SaveMessage(m Message, conversationID string, attachments []RawAttachment) error {
	attachJSON := ""
	if len(attachments) > 0 {
		b, _ := json.Marshal(attachments)
		attachJSON = string(b)
	}

	_, err := s.db.Exec(
		"INSERT OR REPLACE INTO messages (id, conversation_id, sender_name, body, is_from_me, timestamp_ms, attachment_id, mime_type, attachments, sender_number) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		m.MessageID, conversationID, m.SenderName, m.Body, m.IsFromMe, m.TimestampMS, "", "", attachJSON, m.SenderNumber,
	)
	if err != nil {
		return err
	}

	isMe := 0
	if m.IsFromMe {
		isMe = 1
	}
	_, err = s.db.Exec(
		"INSERT INTO conversations (id, last_message_ts, unread_count) VALUES (?, ?, ?) ON CONFLICT(id) DO UPDATE SET last_message_ts = ?, unread_count = unread_count + ?",
		conversationID, m.TimestampMS, 1-isMe, m.TimestampMS, 1-isMe,
	)
	return err
}

func (s *SignalDB) FetchMessages(conversationID string, limit int, beforeTS int64, attachmentBaseURL string) ([]Message, error) {
	query := `SELECT id, sender_name, body, is_from_me, timestamp_ms,
		COALESCE(attachment_id,''), COALESCE(mime_type,''),
		COALESCE(attachments,''), COALESCE(reactions,''), COALESCE(sender_number,'')
		FROM messages WHERE conversation_id = ?`
	args := []interface{}{conversationID}

	if beforeTS > 0 {
		query += " AND timestamp_ms < ?"
		args = append(args, beforeTS)
	}

	query += " ORDER BY timestamp_ms DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var messages []Message
	for rows.Next() {
		var m Message
		var isFromMe int
		var attachmentID, mimeType, attachmentsJSON, reactionsJSON, senderNumber string
		if err := rows.Scan(&m.MessageID, &m.SenderName, &m.Body, &isFromMe, &m.TimestampMS, &attachmentID, &mimeType, &attachmentsJSON, &reactionsJSON, &senderNumber); err != nil {
			return nil, err
		}
		m.IsFromMe = (isFromMe == 1)
		m.SenderNumber = senderNumber

		if attachmentsJSON != "" {
			var raw []RawAttachment
			if json.Unmarshal([]byte(attachmentsJSON), &raw) == nil {
				for _, r := range raw {
					url := resolveAttachmentURL(r.ID, attachmentBaseURL)
					if url != "" {
						m.Attachments = append(m.Attachments, Attachment{URL: url, MimeType: r.MimeType})
					}
				}
			}
		} else if attachmentID != "" {
			url := resolveAttachmentURL(attachmentID, attachmentBaseURL)
			if url != "" {
				m.Attachments = []Attachment{{URL: url, MimeType: mimeType}}
			}
		}

		if len(m.Attachments) > 0 {
			m.AttachmentURL = m.Attachments[0].URL
			m.MimeType = m.Attachments[0].MimeType
		}

		if reactionsJSON != "" {
			json.Unmarshal([]byte(reactionsJSON), &m.Reactions)
		}

		messages = append(messages, m)
	}

	return messages, nil
}

// UpdateMessageReactions upserts a single emoji+sender on the target message
// identified by its Signal timestamp. remove=true removes the sender's reaction.
func (s *SignalDB) UpdateMessageReactions(conversationID string, targetTS int64, emoji, senderName string, remove bool) error {
	var msgID string
	var reactionsJSON string
	err := s.db.QueryRow(
		`SELECT id, COALESCE(reactions,'') FROM messages WHERE conversation_id = ? AND timestamp_ms = ? LIMIT 1`,
		conversationID, targetTS,
	).Scan(&msgID, &reactionsJSON)
	if err != nil {
		return nil // target message not in our DB yet — silently skip
	}

	var reactions []Reaction
	if reactionsJSON != "" {
		json.Unmarshal([]byte(reactionsJSON), &reactions)
	}

	if remove {
		for i, r := range reactions {
			if r.Emoji != emoji {
				continue
			}
			newSenders := make([]string, 0, len(r.Senders))
			for _, s := range r.Senders {
				if s != senderName {
					newSenders = append(newSenders, s)
				}
			}
			reactions[i].Senders = newSenders
			reactions[i].Count = len(newSenders)
			if reactions[i].Count == 0 {
				reactions = append(reactions[:i], reactions[i+1:]...)
			}
			break
		}
	} else {
		found := false
		for i, r := range reactions {
			if r.Emoji != emoji {
				continue
			}
			already := false
			for _, s := range r.Senders {
				if s == senderName {
					already = true
					break
				}
			}
			if !already {
				reactions[i].Senders = append(reactions[i].Senders, senderName)
				reactions[i].Count = len(reactions[i].Senders)
			}
			found = true
			break
		}
		if !found {
			reactions = append(reactions, Reaction{Emoji: emoji, Count: 1, Senders: []string{senderName}})
		}
	}

	b, _ := json.Marshal(reactions)
	_, err = s.db.Exec(`UPDATE messages SET reactions = ? WHERE id = ?`, string(b), msgID)
	return err
}

func resolveAttachmentURL(id, baseURL string) string {
	if id == "" {
		return ""
	}
	if strings.HasPrefix(id, "/") {
		return id
	}
	if baseURL != "" {
		return baseURL + "/v1/attachments/" + id
	}
	return ""
}

func (s *SignalDB) MarkAsRead(conversationID string) error {
	_, err := s.db.Exec("UPDATE conversations SET unread_count = 0 WHERE id = ?", conversationID)
	return err
}

// FetchUnreadReceivedTimestamps returns the Signal timestamps of unread incoming
// messages for the given conversation, most recent first, up to limit.
func (s *SignalDB) FetchUnreadReceivedTimestamps(conversationID string, limit int) ([]int64, error) {
	rows, err := s.db.Query(
		`SELECT timestamp_ms FROM messages
		 WHERE conversation_id = ? AND is_from_me = 0
		 ORDER BY timestamp_ms DESC LIMIT ?`,
		conversationID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var ts int64
		if err := rows.Scan(&ts); err != nil {
			return nil, err
		}
		out = append(out, ts)
	}
	return out, nil
}

func (s *SignalDB) FetchConversationMetadata(id string) (int, int64, error) {
	var unreadCount int
	var lastTS int64
	err := s.db.QueryRow("SELECT unread_count, last_message_ts FROM conversations WHERE id = ?", id).Scan(&unreadCount, &lastTS)
	if err == sql.ErrNoRows {
		return 0, 0, nil
	}
	return unreadCount, lastTS, err
}

func (s *SignalDB) FetchAllConversations() (map[string]struct {
	UnreadCount   int
	LastMessageTS int64
}, error) {
	rows, err := s.db.Query("SELECT id, unread_count, last_message_ts FROM conversations")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	res := make(map[string]struct {
		UnreadCount   int
		LastMessageTS int64
	})
	for rows.Next() {
		var id string
		var meta struct {
			UnreadCount   int
			LastMessageTS int64
		}
		if err := rows.Scan(&id, &meta.UnreadCount, &meta.LastMessageTS); err != nil {
			return nil, err
		}
		res[id] = meta
	}
	return res, nil
}

// UpdateReactionInAllDBs writes the reaction to every profile signal-cache DB.
// This is used when the account→profile mapping cannot be determined (e.g. when
// the profile config has no Account field). Each DB's UpdateMessageReactions is
// a no-op when the target message isn't present, so only the correct DB is
// actually modified.
func UpdateReactionInAllDBs(convID string, targetTS int64, emoji, senderName string, remove bool) {
	home, _ := os.UserHomeDir()
	configDir := filepath.Join(home, ".config", "msg")
	entries, _ := os.ReadDir(configDir)
	for _, e := range entries {
		name := e.Name()
		var profile string
		switch {
		case name == "signal-cache.db":
			profile = ""
		case strings.HasPrefix(name, "signal-cache-") && strings.HasSuffix(name, ".db"):
			profile = name[len("signal-cache-") : len(name)-len(".db")]
		default:
			continue
		}
		db, err := OpenSignalDBForProfile(profile)
		if err != nil {
			continue
		}
		db.UpdateMessageReactions(convID, targetTS, emoji, senderName, remove)
		db.Close()
	}
}
