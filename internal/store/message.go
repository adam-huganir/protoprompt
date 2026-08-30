package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Message is one node in the session's message tree.
type Message struct {
	ID             string  `json:"id"`
	SessionID      string  `json:"-"`
	ParentID       *string `json:"parentId"`
	Role           string  `json:"role"`
	Text           string  `json:"text"`
	ReasoningText  string  `json:"reasoningText"`
	Status         string  `json:"status"`
	Preview        bool    `json:"preview"`
	PromptRevID    *string `json:"promptRevId"`
	Model          string  `json:"model"`
	ReasoningEff   string  `json:"reasoningEffort"`
	Temperature    float64 `json:"temperature"`
	MaxTokens      *int    `json:"maxTokens"`
	Error          string  `json:"error"`
	PromptTokens   *int    `json:"promptTokens"`
	OutputTokens   *int    `json:"outputTokens"`
	LatencyMS      *int64  `json:"latencyMs"`
	CreatedAt      int64   `json:"createdAt"`
	FinishedAt     *int64  `json:"finishedAt"`
	SiblingCount   int     `json:"siblingCount"`
	SiblingIndex   int     `json:"siblingIndex"`
	PreviewMessage *any    `json:"previewMessage"`
}

var ErrNotAccepted = errors.New("not an accepted message of this session")

const messageCols = `id, session_id, parent_id, role, text, reasoning_text, status, preview, prompt_rev_id, model, reasoning_effort, temperature, max_tokens, error, prompt_tokens, output_tokens, latency_ms, created_at, finished_at`

func scanMessage(row interface{ Scan(...any) error }) (*Message, error) {
	var m Message
	err := row.Scan(&m.ID, &m.SessionID, &m.ParentID, &m.Role, &m.Text, &m.ReasoningText,
		&m.Status, &m.Preview, &m.PromptRevID, &m.Model, &m.ReasoningEff,
		&m.Temperature, &m.MaxTokens, &m.Error, &m.PromptTokens, &m.OutputTokens,
		&m.LatencyMS, &m.CreatedAt, &m.FinishedAt)
	return &m, err
}

func (s *Store) GetMessage(id string) (*Message, error) {
	row := s.DB.QueryRow(`SELECT `+messageCols+` FROM message WHERE id = ?`, id)
	m, err := scanMessage(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return m, nil
}

// execInsertMessage runs an INSERT, retrying once on an id collision.
func (s *Store) execInsertMessage(m *Message, withPreview bool) error {
	insert := func(id string) error {
		_, err := s.DB.Exec(
			`INSERT INTO message (id, session_id, parent_id, role, text, reasoning_text, status, preview, prompt_rev_id, model, reasoning_effort, temperature, max_tokens, created_at)
			 VALUES (?, ?, ?, ?, ?, '', ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, m.SessionID, m.ParentID, m.Role, m.Text, m.Status, boolInt(m.Preview && withPreview),
			m.PromptRevID, m.Model, m.ReasoningEff, m.Temperature, m.MaxTokens, m.CreatedAt,
		)
		return err
	}
	err := insert(m.ID)
	if err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed: message.id") {
		m.ID = NewID()
		err = insert(m.ID)
	}
	return err
}

// InsertUserMessage appends a done user message under parentID (nil = root).
func (s *Store) InsertUserMessage(sessionID string, parentID *string, text string) (*Message, error) {
	m := &Message{
		ID: NewID(), SessionID: sessionID, ParentID: parentID, Role: "user",
		Text: text, Status: "done", CreatedAt: nowMS(),
	}
	if err := s.execInsertMessage(m, true); err != nil {
		return nil, err
	}
	return m, nil
}

// AssistantParams captures the generation snapshot stored on the row.
type AssistantParams struct {
	Model        string
	ReasoningEff string
	Temperature  float64
	MaxTokens    *int
	PromptRevID  string
}

// InsertAssistantRow creates a streaming assistant row under parentID.
func (s *Store) InsertAssistantRow(sessionID string, parentID *string, preview bool, p AssistantParams) (*Message, error) {
	var revID *string
	if p.PromptRevID != "" {
		revID = &p.PromptRevID
	}
	m := &Message{
		ID: NewID(), SessionID: sessionID, ParentID: parentID, Role: "assistant",
		Status: "streaming", Preview: preview && true, PromptRevID: revID,
		Model: p.Model, ReasoningEff: p.ReasoningEff, Temperature: p.Temperature,
		MaxTokens: p.MaxTokens, CreatedAt: nowMS(),
	}
	if err := s.execInsertMessage(m, true); err != nil {
		return nil, err
	}
	m.Preview = preview
	return m, nil
}

// FinishMessage writes terminal content to a row.
func (s *Store) FinishMessage(id, text, reasoning string, promptToks, outToks *int, status, errMsg string) error {
	now := nowMS()
	lat := now - func() int64 {
		var created int64
		if err := s.DB.QueryRow(`SELECT created_at FROM message WHERE id = ?`, id).Scan(&created); err != nil {
			return now
		}
		return created
	}()
	_, err := s.DB.Exec(
		`UPDATE message SET text = ?, reasoning_text = ?, status = ?, error = ?, prompt_tokens = ?, output_tokens = ?, latency_ms = ?, finished_at = ? WHERE id = ?`,
		text, reasoning, status, errMsg, promptToks, outToks, lat, now, id,
	)
	return err
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// Transcript walks from head back through parents (accepted rows only) and
// returns oldest→newest.
func (s *Store) Transcript(sessionID string, headID *string) ([]Message, error) {
	if headID == nil {
		return []Message{}, nil
	}
	var out []Message
	cur := *headID
	for range 100000 {
		row := s.DB.QueryRow(
			`SELECT `+messageCols+` FROM message WHERE id = ?`, cur,
		)
		m, err := scanMessage(row)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				break
			}
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
	// Reverse to oldest-first.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

// FillSiblings computes siblingCount/siblingIndex on each message: accepted,
// same-parent, same-role rows ordered by creation.
func (s *Store) FillSiblings(msgs []Message) error {
	for i := range msgs {
		m := &msgs[i]
		rows, err := s.DB.Query(
			`SELECT id FROM message WHERE session_id = ? AND role = ? AND preview = 0 AND
			        ((?) IS NULL AND parent_id IS NULL OR parent_id = ?)
			 ORDER BY created_at, id`,
			m.SessionID, m.Role, m.ParentID, m.ParentID,
		)
		if err != nil {
			return err
		}
		count := 0
		index := 0
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			count++
			if id == m.ID && index == 0 {
				index = count
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		m.SiblingCount = count
		if index == 0 {
			index = count
		}
		m.SiblingIndex = index
	}
	return nil
}

// Preview availability.

// PreviewConflict checks whether a preview for parentID is already streaming.
func (s *Store) PreviewConflict(sessionID string, parentID *string) (bool, error) {
	var id string
	err := s.DB.QueryRow(
		`SELECT id FROM message WHERE session_id = ? AND preview = 1 AND status = 'streaming' AND
		        ((?) IS NULL AND parent_id IS NULL OR parent_id = ?) LIMIT 1`,
		sessionID, parentID, parentID,
	).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return id != "", err
}

// GetPreviewForParent returns the newest preview row for parentID, or nil.
func (s *Store) GetPreviewForParent(sessionID string, parentID *string) (*Message, error) {
	row := s.DB.QueryRow(
		`SELECT `+messageCols+` FROM message WHERE session_id = ? AND preview = 1 AND
		        ((?) IS NULL AND parent_id IS NULL OR parent_id = ?)
		 ORDER BY created_at DESC, id LIMIT 1`,
		sessionID, parentID, parentID,
	)
	m, err := scanMessage(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return m, err
}

// DeletePreviewsForParent removes all preview rows sharing parentID.
func (s *Store) DeletePreviewsForParent(sessionID string, parentID *string) error {
	_, err := s.DB.Exec(
		`DELETE FROM message WHERE session_id = ? AND preview = 1 AND
		        ((?) IS NULL AND parent_id IS NULL OR parent_id = ?)`,
		sessionID, parentID, parentID,
	)
	return err
}

// AcceptPreview flips a done preview row to accepted and sets it as head.
func (s *Store) AcceptPreview(id string) (*Session, error) {
	m, err := s.GetMessage(id)
	if err != nil {
		return nil, err
	}
	if !m.Preview || m.Status != "done" {
		return nil, fmt.Errorf("only a completed preview can be accepted")
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`UPDATE message SET preview = 0 WHERE id = ?`, id); err != nil {
		tx.Rollback()
		return nil, err
	}
	if _, err := tx.Exec(`UPDATE session SET head_message_id = ?, updated_at = ? WHERE id = ?`,
		id, nowMS(), m.SessionID); err != nil {
		tx.Rollback()
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetSession(m.SessionID)
}

// DeleteMessage removes a message and its whole subtree (FK cascade).
func (s *Store) DeleteMessage(id string) error {
	m, err := s.GetMessage(id)
	if err != nil {
		return err
	}
	res, err := s.DB.Exec(`DELETE FROM message WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	// If head pointed at the deleted root, FK SET NULL already cleared it; bump session.
	return s.TouchSession(m.SessionID)
}

// PrimaryPath follows the newest accepted child chain from startID,
// returning the descendants (excluding startID itself), top→bottom.
func (s *Store) PrimaryPath(sessionID, startID string) ([]Message, error) {
	var out []Message
	cur := startID
	for range 100000 {
		next, err := s.newestChild(sessionID, cur)
		if err != nil {
			return nil, err
		}
		if next == "" {
			break
		}
		m, err := s.GetMessage(next)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
		cur = next
	}
	return out, nil
}

// HasAcceptedMessages reports whether the session has any accepted row.
func (s *Store) HasAcceptedMessages(sessionID string) (bool, error) {
	var n int
	err := s.DB.QueryRow(`SELECT COUNT(*) FROM message WHERE session_id = ? AND preview = 0`, sessionID).Scan(&n)
	return n > 0, err
}
