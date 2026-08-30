package store

import (
	"database/sql"
	"encoding/json"
	"errors"
)

// Session is a conversation bound to one prompt file.
type Session struct {
	ID           string         `json:"id"`
	File         string         `json:"file"`
	Title        string         `json:"title"`
	Model        string         `json:"model"`
	ReasoningEff string         `json:"reasoningEffort"`
	Temperature  float64        `json:"temperature"`
	MaxTokens    *int           `json:"maxTokens"`
	ExtraBody    map[string]any `json:"extraBody"`
	HeadMessage  *string        `json:"headMessageId"`
	CreatedAt    int64          `json:"createdAt"`
	UpdatedAt    int64          `json:"updatedAt"`

	extraJSON string // raw column value
}

// CreateSession inserts a session row; empty model defaults stay empty (the
// handler applies config defaults before calling).
func (s *Store) CreateSession(file, title, model, effort string, temperature float64, maxTokens *int, extraBody map[string]any) (*Session, error) {
	if extraBody == nil {
		extraBody = map[string]any{}
	}
	if temperature == 0 {
		temperature = 1
	}
	exj, err := json.Marshal(extraBody)
	if err != nil {
		return nil, err
	}
	now := nowMS()
	sess := &Session{
		ID: NewID(), File: file, Title: title, Model: model,
		ReasoningEff: effort, Temperature: temperature, MaxTokens: maxTokens,
		ExtraBody: extraBody, CreatedAt: now, UpdatedAt: now,
	}
	_, err = s.DB.Exec(
		`INSERT INTO session (id, file, title, model, reasoning_effort, temperature, max_tokens, extra_json, head_message_id, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, NULL, ?, ?)`,
		sess.ID, sess.File, sess.Title, sess.Model, sess.ReasoningEff,
		sess.Temperature, sess.MaxTokens, string(exj), sess.CreatedAt, sess.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return sess, nil
}

func scanSession(row interface{ Scan(...any) error }) (*Session, error) {
	var s Session
	var exj string
	err := row.Scan(&s.ID, &s.File, &s.Title, &s.Model, &s.ReasoningEff,
		&s.Temperature, &s.MaxTokens, &exj, &s.HeadMessage, &s.CreatedAt, &s.UpdatedAt)
	if err != nil {
		return nil, err
	}
	s.extraJSON = exj
	if err := json.Unmarshal([]byte(exj), &s.ExtraBody); err != nil {
		s.ExtraBody = map[string]any{}
	}
	return &s, nil
}

const sessionCols = `id, file, title, model, reasoning_effort, temperature, max_tokens, extra_json, head_message_id, created_at, updated_at`

func (s *Store) GetSession(id string) (*Session, error) {
	row := s.DB.QueryRow(`SELECT `+sessionCols+` FROM session WHERE id = ?`, id)
	sess, err := scanSession(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return sess, err
}

// ListSessions returns sessions for a prompt file, newest first.
func (s *Store) ListSessions(file string, limit int) ([]Session, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.DB.Query(
		`SELECT `+sessionCols+` FROM session WHERE file = ? ORDER BY updated_at DESC LIMIT ?`,
		file, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Session{}
	for rows.Next() {
		sess, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *sess)
	}
	return out, rows.Err()
}

// UpdateSession replaces the editable fields of a session.
func (s *Store) UpdateSession(id, title, model, effort string, temperature float64, maxTokens *int, extraBody map[string]any) (*Session, error) {
	if extraBody == nil {
		extraBody = map[string]any{}
	}
	exj, err := json.Marshal(extraBody)
	if err != nil {
		return nil, err
	}
	now := nowMS()
	res, err := s.DB.Exec(
		`UPDATE session SET title = ?, model = ?, reasoning_effort = ?, temperature = ?, max_tokens = ?, extra_json = ?, updated_at = ? WHERE id = ?`,
		title, model, effort, temperature, maxTokens, string(exj), now, id,
	)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	return s.GetSession(id)
}

func (s *Store) DeleteSession(id string) error {
	res, err := s.DB.Exec(`DELETE FROM session WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) TouchSession(id string) error {
	_, err := s.DB.Exec(`UPDATE session SET updated_at = ? WHERE id = ?`, nowMS(), id)
	return err
}

// SetHead points the session at an accepted message. toLeaf=true walks the
// newest-child chain to a leaf. The message must belong to the session and be
// accepted (not a preview).
func (s *Store) SetHead(sessionID, messageID string, toLeaf bool) (*Session, error) {
	msg, err := s.GetMessage(messageID)
	if err != nil {
		return nil, err
	}
	if msg.SessionID != sessionID || msg.Preview {
		return nil, ErrNotAccepted
	}
	leaf := messageID
	if toLeaf {
		cur := messageID
		for range 10000 {
			next, err := s.newestChild(sessionID, cur)
			if err != nil {
				return nil, err
			}
			if next == "" {
				break
			}
			cur = next
		}
		leaf = cur
	}
	_, err = s.DB.Exec(`UPDATE session SET head_message_id = ?, updated_at = ? WHERE id = ?`,
		leaf, nowMS(), sessionID)
	if err != nil {
		return nil, err
	}
	return s.GetSession(sessionID)
}

// newestChild returns the id of the newest accepted child of parent, or "".
func (s *Store) newestChild(sessionID, parentID string) (string, error) {
	var id string
	err := s.DB.QueryRow(
		`SELECT id FROM message WHERE session_id = ? AND parent_id = ? AND preview = 0
		 ORDER BY created_at DESC, id LIMIT 1`,
		sessionID, parentID,
	).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// SetHeadDirect points head at an accepted message id (no leaf walk).
func (s *Store) SetHeadDirect(sessionID, messageID string) error {
	_, err := s.DB.Exec(`UPDATE session SET head_message_id = ?, updated_at = ? WHERE id = ?`,
		messageID, nowMS(), sessionID)
	return err
}

// HistoryUpTo walks parents from msgID upward and returns accepted rows
// oldest→newest ending at msgID (inclusive). nil returns empty.
func (s *Store) HistoryUpTo(sessionID string, msgID *string) ([]Message, error) {
	if msgID == nil {
		return []Message{}, nil
	}
	var out []Message
	cur := *msgID
	for range 100000 {
		m, err := s.GetMessage(cur)
		if err != nil {
			return nil, err
		}
		if m.SessionID != sessionID {
			return nil, ErrNotAccepted
		}
		if !m.Preview {
			out = append(out, *m)
		}
		if m.ParentID == nil {
			break
		}
		cur = *m.ParentID
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}
