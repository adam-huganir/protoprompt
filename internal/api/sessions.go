package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"protoprompt/internal/llm"
	"protoprompt/internal/store"
)

// helperPreamble is the fixed instruction prefix for the prompt-helper model.
const helperPreamble = "You are an editor of the prompt file shown below. When the user asks you to change the prompt and the request is unambiguous, reply with the COMPLETE new file text inside ONE fenced code block (```markdown ... ```), and nothing else that would prevent parsing. If the request is ambiguous, or references a previous response or other context you cannot see, do NOT guess or fabricate a change: ask a single clarifying question in plain text and wait for the answer before proposing anything. If no change is requested, answer plainly."

// chatContextMaxChars / chatContextMaxMsgs bound the main-chat context block
// injected into the helper system prompt.
const (
	chatContextMaxChars = 8192
	chatContextMaxMsgs  = 40
)

// helperSystem builds the helper model's system prompt: fixed preamble, the
// current file text, and — when a chat session with messages exists for the
// file — a bounded newest-first transcript of the most recently active one.
func (s *Server) helperSystem(file, revText string) string {
	sys := helperPreamble + "\n\nCURRENT PROMPT FILE:\n" + revText
	if ctx := s.chatContext(file); ctx != "" {
		sys += "\n\n" + ctx
	}
	return sys
}

// chatContext returns a bounded newest-first transcript of the most recently
// active chat session on file ("" when none has messages). ListSessions
// orders by updated_at DESC, so the first session with a non-empty transcript
// is the most recently active one.
func (s *Server) chatContext(file string) string {
	sessions, err := s.store.ListSessions(file, "chat", 20)
	if err != nil || len(sessions) == 0 {
		return ""
	}
	for i := range sessions {
		cs := &sessions[i]
		msgs, err := s.store.Transcript(cs.ID, cs.HeadMessage)
		if err != nil || len(msgs) == 0 {
			continue
		}
		var b strings.Builder
		b.WriteString("RECENT MAIN-CHAT CONTEXT (from the most recent chat session on this file; newest first):\n")
		added := 0
		for i := len(msgs) - 1; i >= 0 && added < chatContextMaxMsgs; i-- {
			m := msgs[i]
			var entry strings.Builder
			if m.Role == "user" {
				fmt.Fprintf(&entry, "user: %s\n", m.Text)
			} else {
				fmt.Fprintf(&entry, "assistant: %s\n", m.Text)
				if m.ReasoningText != "" {
					fmt.Fprintf(&entry, "  reasoning: %s\n", m.ReasoningText)
				}
			}
			if added > 0 && b.Len()+entry.Len() > chatContextMaxChars {
				break
			}
			b.WriteString(entry.String())
			added++
		}
		return b.String()
	}
	return ""
}

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
	case sub == "proposed/accept" && r.Method == http.MethodPost:
		s.acceptProposed(w, r, id)
	case sub == "proposed" && r.Method == http.MethodDelete:
		s.discardProposed(w, r, id)
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
	kind := r.URL.Query().Get("kind")
	if kind == "" {
		kind = "chat"
	}
	sessions, err := s.store.ListSessions(file, kind, 100)
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
		Kind         string         `json:"kind"`
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
	sess, err := s.store.CreateSession(body.File, body.Title, body.Model, body.ReasoningEff, temp, body.MaxTokens, body.ExtraBody, body.Kind)
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
				renderHTML(pv)
				msgs[i].PreviewMessage = pv
			}
			pe, err := s.store.GetProposedEdit(msgs[i].ID)
			if err != nil {
				writeErr(w, http.StatusInternalServerError, err.Error())
				return
			}
			if pe != nil {
				msgs[i].ProposedEdit = pe
			}
		}
	}
	renderHTMLAll(msgs)
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
	renderHTML(msg)
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

// acceptProposed saves the proposed text as a new revision of the prompt file
// and removes the proposal, so it is gone on the next fetch.
func (s *Server) acceptProposed(w http.ResponseWriter, r *http.Request, id string) {
	pe, err := s.store.GetProposedEdit(id)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	if pe == nil {
		writeErr(w, http.StatusNotFound, "no proposal for this message")
		return
	}
	revID, err := s.store.SaveFile(pe.File, pe.Text)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	if err := s.store.DeleteProposedEdit(id); err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"revId": revID}) // revID is null when text was unchanged
}

func (s *Server) discardProposed(w http.ResponseWriter, r *http.Request, id string) {
	if err := s.store.DeleteProposedEdit(id); err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
