package api

import (
	"fmt"
	"net/http"
	"strings"
)

// resend handles POST /api/sessions/{id}/resend {messageId, text}: replaces
// the target user message with an edited sibling and regenerates the primary
// path beneath it. Streams meta/delta/done per node, then complete.
func (s *Server) resend(w http.ResponseWriter, r *http.Request, id string) {
	sess, err := s.store.GetSession(id)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	var body struct {
		MessageID string `json:"messageId"`
		Text      string `json:"text"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if s.cfg.APIKey == "" {
		writeErr(w, http.StatusNotImplemented, "no API key configured")
		return
	}
	if strings.TrimSpace(body.Text) == "" {
		writeErr(w, http.StatusBadRequest, "text required")
		return
	}
	rev, err := s.store.LatestRev(sess.File)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if rev == nil {
		writeErr(w, http.StatusConflict, fmt.Sprintf("no prompt revision saved for file %q; save the editor first", sess.File))
		return
	}
	target, err := s.store.GetMessage(body.MessageID)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	if target.SessionID != id || target.Preview || target.Role != "user" {
		writeErr(w, http.StatusConflict, "messageId must be an accepted user message of this session")
		return
	}

	u2, err := s.store.InsertUserMessage(id, target.ParentID, body.Text)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	str, ok := newSSE(w, r)
	if !ok {
		return
	}
	str.event("meta", map[string]any{"messageId": u2.ID, "message": u2})
	str.event("done", map[string]any{"messageId": u2.ID, "message": u2})

	path, err := s.store.PrimaryPath(id, target.ID)
	if err != nil {
		str.event("error", map[string]string{"message": err.Error()})
		return
	}

	prevID := &u2.ID
	failed := false
	reported := false
	for idx := range path {
		node := path[idx]
		if node.Role == "user" {
			cp, err := s.store.CopyMessageToParent(&node, prevID)
			if err != nil {
				failed = true
				break
			}
			str.event("meta", map[string]any{"messageId": cp.ID, "message": cp})
			str.event("done", map[string]any{"messageId": cp.ID, "message": cp})
			prevID = &cp.ID
			continue
		}
		params := s.snapshotParams(sess, rev.ID)
		row, err := s.store.InsertAssistantRow(id, prevID, false, params)
		if err != nil {
			failed = true
			break
		}
		str.event("meta", map[string]any{"messageId": row.ID, "message": row})
		if serr := s.streamAssistant(r.Context(), str, sess, row, rev.Text, false); serr != nil {
			failed = true
			reported = true
			break
		}
		prevID = &row.ID
	}

	if headErr := s.store.SetHeadDirect(id, *prevID); headErr != nil {
		str.event("error", map[string]string{"message": headErr.Error()})
		return
	}
	if failed {
		if !reported {
			str.event("error", map[string]string{"message": "resend aborted; partial chain kept"})
		}
		return
	}
	str.event("complete", map[string]any{"headMessageId": *prevID})
}
