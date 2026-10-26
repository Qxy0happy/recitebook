package store

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

// ChatMessage is one turn of a question's conversation.
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ErrEmptyChatID means a chat was addressed without a question id.
var ErrEmptyChatID = errors.New("store: empty question id")

// ChatStore reads and writes the per-question chat history. It is separate
// from Store, which only exposes recitation state, and it never takes part in
// backups or sync.
type ChatStore interface {
	LoadChat(questionID string) ([]ChatMessage, error)
	SaveChat(questionID string, messages []ChatMessage) error
}

var _ ChatStore = (*SQLite)(nil)

// LoadChat returns the saved conversation for a question. A question without
// history yields an empty slice, never an error.
func (s *SQLite) LoadChat(questionID string) ([]ChatMessage, error) {
	if strings.TrimSpace(questionID) == "" {
		return nil, ErrEmptyChatID
	}
	var raw string
	err := s.db.QueryRow(`SELECT messages FROM chats WHERE question_id = ?`, questionID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return []ChatMessage{}, nil
	}
	if err != nil {
		return nil, classify(err)
	}
	messages := []ChatMessage{}
	if err := decode(raw, &messages); err != nil {
		return nil, corrupt(err)
	}
	if messages == nil {
		return []ChatMessage{}, nil
	}
	return messages, nil
}

// SaveChat replaces the whole conversation for a question.
func (s *SQLite) SaveChat(questionID string, messages []ChatMessage) error {
	if strings.TrimSpace(questionID) == "" {
		return ErrEmptyChatID
	}
	if messages == nil {
		messages = []ChatMessage{}
	}
	encoded, err := encode(messages)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(
		`INSERT INTO chats (question_id, messages, updated_at) VALUES (?, ?, ?)
		 ON CONFLICT(question_id) DO UPDATE SET
			messages = excluded.messages, updated_at = excluded.updated_at`,
		questionID, string(encoded), time.Now().UTC().Format(time.RFC3339),
	)
	return classify(err)
}
