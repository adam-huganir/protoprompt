package api

import (
	"errors"
	"net/http"
	"strings"

	"chatvolution/internal/llm"
	"chatvolution/internal/store"
)

// sessionRoutes handles /api/sessions and /api/sessions/{id}/....
func (s *Server) sessionRoutes(w http.ResponseWriter, r *http.Request) {
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/sessions"), "/")
	switch rest {
	case "":
		switch r.Method {
		case http.MethodGet:
			s.listSessions(w, r)
			return
		case http.MethodPost:
			s.createSession(w, r)
			return
		}
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	id, sub := splitSeg(rest)
	switch {
	case sub == "":
		switch r.Method {
		case http.MethodGet:
			s.getSession(w, r, id)
		case http.MethodPut:
			s.updateSession(w, r, id)
		case http.MethodDelete:
			s.deleteSession(w, r, id)
		default:
			writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	case sub == "head" && r.Method == http.MethodPost:
		s.setHead(w, r, id)
	case sub == "messages" && r.Method == http.MethodPost:
		s.addMessage(w, r, id)
	case sub == "generate" && r.Method == http.MethodPost:
		s.generate(w, r, id)
	case sub == "model" && r.Method == http.MethodPost:
		s.setSessionModel(w, r, id)
	case sub == "resend" && r.Method == http.MethodPost:
		s.resend(w, r, id)
	default:
		writeErr(w, http.StatusNotFound, "not found")
	}
}

func (s *Server) messageRoutes(w http.ResponseWriter, r *http.Request) {
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/messages"), "/")
	id, sub := splitSeg(rest)
	if id == "" {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	switch {
	case sub == "" && r.Method == http.MethodDelete:
		if err := s.store.DeleteMessage(id); err != nil {
			writeStoreErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	case sub == "accept" && r.Method == http.MethodPost:
		sess, err := s.store.AcceptPreview(id)
		if err != nil {
			writeStoreErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, sess)
	case sub == "context" && r.Method == http.MethodGet:
		s.messageContext(w, r, id)
	default:
		writeErr(w, http.StatusNotFound, "not found")
	}
}

// splitSeg splits "id/sub" into ("id", "sub"); sub may be empty.
func splitSeg(s string) (string, string) {
	if i := strings.IndexByte(s, '/'); i >= 0 {
		return s[:i], s[i+1:]
	}
	return s, ""
}

func (s *Server) listSessions(w http.ResponseWriter, r *http.Request) {
	file := r.URL.Query().Get("file")
	if file == "" {
		writeErr(w, http.StatusBadRequest, "file query parameter required")
		return
	}
	sessions, err := s.store.ListSessions(file, 100)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, sessions)
}

func (s *Server) createSession(w http.ResponseWriter, r *http.Request) {
	var body struct {
		File         string         `json:"file"`
		Title        string         `json:"title"`
		Model        string         `json:"model"`
		ReasoningEff string         `json:"reasoningEffort"`
		Temperature  *float64       `json:"temperature"`
		MaxTokens    *int           `json:"maxTokens"`
		ExtraBody    map[string]any `json:"extraBody"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if body.File == "" {
		writeErr(w, http.StatusBadRequest, "file required")
		return
	}
	if body.Model == "" {
		body.Model = s.cfg.DefaultModel
	}
	if body.ReasoningEff == "" {
		body.ReasoningEff = s.cfg.ReasoningEff
	}
	temp := 1.0
	if body.Temperature != nil {
		temp = *body.Temperature
	}
	sess, err := s.store.CreateSession(body.File, body.Title, body.Model, body.ReasoningEff, temp, body.MaxTokens, body.ExtraBody)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, sess)
}

func (s *Server) getSession(w http.ResponseWriter, r *http.Request, id string) {
	sess, err := s.store.GetSession(id)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	msgs, err := s.store.Transcript(id, sess.HeadMessage)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.store.FillSiblings(msgs); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	for i := range msgs {
		if msgs[i].Role == "assistant" {
			pv, err := s.store.GetPreviewForParent(id, msgs[i].ParentID)
			if err != nil {
				writeErr(w, http.StatusInternalServerError, err.Error())
				return
			}
			if pv != nil && pv.ID != msgs[i].ID {
				msgs[i].PreviewMessage = pv
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"session":  sess,
		"messages": msgs,
	})
}

func (s *Server) updateSession(w http.ResponseWriter, r *http.Request, id string) {
	var body struct {
		Title        string         `json:"title"`
		Model        string         `json:"model"`
		ReasoningEff string         `json:"reasoningEffort"`
		Temperature  float64        `json:"temperature"`
		MaxTokens    *int           `json:"maxTokens"`
		ExtraBody    map[string]any `json:"extraBody"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if body.Model == "" {
		body.Model = s.cfg.DefaultModel
	}
	sess, err := s.store.UpdateSession(id, body.Title, body.Model, body.ReasoningEff, body.Temperature, body.MaxTokens, body.ExtraBody)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sess)
}

// setSessionModel switches a session's model; the next generation picks it up.
// The value is not checked against any model list (see docs/api.md).
func (s *Server) setSessionModel(w http.ResponseWriter, r *http.Request, id string) {
	var body struct {
		Model string `json:"model"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	model := strings.TrimSpace(body.Model)
	if model == "" {
		writeErr(w, http.StatusBadRequest, "model required")
		return
	}
	sess, err := s.store.SetSessionModel(id, model)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sess)
}

func (s *Server) deleteSession(w http.ResponseWriter, r *http.Request, id string) {
	if err := s.store.DeleteSession(id); err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) setHead(w http.ResponseWriter, r *http.Request, id string) {
	var body struct {
		MessageID string `json:"messageId"`
		ToLeaf    *bool  `json:"toLeaf"`
	}
	if err := decodeJSON(r, &body); err != nil || body.MessageID == "" {
		writeErr(w, http.StatusBadRequest, "messageId required")
		return
	}
	toLeaf := true
	if body.ToLeaf != nil {
		toLeaf = *body.ToLeaf
	}
	sess, err := s.store.SetHead(id, body.MessageID, toLeaf)
	if err != nil {
		if errors.Is(err, store.ErrNotAccepted) {
			writeErr(w, http.StatusConflict, "message is not an accepted message of this session")
			return
		}
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sess)
}

func (s *Server) addMessage(w http.ResponseWriter, r *http.Request, id string) {
	var body struct {
		ParentID *string `json:"parentId"`
		Text     string  `json:"text"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(body.Text) == "" {
		writeErr(w, http.StatusBadRequest, "text required")
		return
	}
	has, err := s.store.HasAcceptedMessages(id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if body.ParentID == nil {
		if has {
			writeErr(w, http.StatusConflict, "parentId required for non-empty session")
			return
		}
	} else {
		p, err := s.store.GetMessage(*body.ParentID)
		if err != nil {
			writeStoreErr(w, err)
			return
		}
		if p.SessionID != id || p.Preview {
			writeErr(w, http.StatusConflict, "parentId must be an accepted message of this session")
			return
		}
	}
	msg, err := s.store.InsertUserMessage(id, body.ParentID, body.Text)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.store.SetHeadDirect(id, msg.ID); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	msg.Preview = false
	writeJSON(w, http.StatusCreated, msg)
}

func (s *Server) messageContext(w http.ResponseWriter, r *http.Request, id string) {
	m, err := s.store.GetMessage(id)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	sys, err := s.systemText(m)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	hist, err := s.store.HistoryUpTo(m.SessionID, &m.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	items := make([]llm.Item, 0, len(hist))
	for _, h := range hist {
		items = append(items, llm.Item{Role: h.Role, Content: h.Text})
	}
	writeJSON(w, http.StatusOK, map[string]any{"system": sys, "history": items})
}

// systemText resolves the prompt text to send: the message's own revision if
// recorded, else the file's latest revision.
func (s *Server) systemText(m *store.Message) (string, error) {
	if m.PromptRevID != nil {
		rev, err := s.store.GetRev(*m.PromptRevID)
		if err == nil {
			return rev.Text, nil
		}
		if !errors.Is(err, store.ErrNotFound) {
			return "", err
		}
	}
	sess, err := s.store.GetSession(m.SessionID)
	if err != nil {
		return "", err
	}
	rev, err := s.store.LatestRev(sess.File)
	if err != nil {
		return "", err
	}
	if rev == nil {
		return "", nil
	}
	return rev.Text, nil
}

// sessionModel resolves model/effort defaults against config.
func (s *Server) sessionModel(sess *store.Session) (model, effort string) {
	model = sess.Model
	if model == "" {
		model = s.cfg.DefaultModel
	}
	effort = sess.ReasoningEff
	if effort == "" {
		effort = s.cfg.ReasoningEff
	}
	return
}
