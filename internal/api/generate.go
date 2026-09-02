package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"protoprompt/internal/diffx"
	"protoprompt/internal/llm"
	"protoprompt/internal/store"
)

var fenceRe = regexp.MustCompile("(?s)```(?:markdown|md)?\n(.*?)```")

// extractFenced returns the first fenced code block's content, or "" when absent.
func extractFenced(s string) string {
	m := fenceRe.FindStringSubmatch(s)
	if m == nil {
		return ""
	}
	return strings.TrimRight(m[1], "\n")
}

// sseStream wraps the response writer for server-sent events.
type sseStream struct {
	w    http.ResponseWriter
	f    http.Flusher
	live bool
}

func newSSE(w http.ResponseWriter, r *http.Request) (*sseStream, bool) {
	f, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "streaming unsupported")
		return nil, false
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("Connection", "keep-alive")
	s := &sseStream{w: w, f: f, live: true}
	f.Flush()
	return s, true
}

func (s *sseStream) event(name string, data any) {
	if !s.live {
		return
	}
	b, err := json.Marshal(data)
	if err != nil {
		s.live = false
		return
	}
	if _, err := fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", name, b); err != nil {
		s.live = false
		return
	}
	s.f.Flush()
}

// generate handles POST /api/sessions/{id}/generate {previewFor?}.
func (s *Server) generate(w http.ResponseWriter, r *http.Request, id string) {
	sess, err := s.store.GetSession(id)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	var body struct {
		PreviewFor string `json:"previewFor"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if s.cfg.APIKey == "" {
		writeErr(w, http.StatusNotImplemented, "no API key configured")
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

	var (
		parentID *string
		preview  bool
	)
	if body.PreviewFor != "" {
		target, err := s.store.GetMessage(body.PreviewFor)
		if err != nil {
			writeStoreErr(w, err)
			return
		}
		if target.SessionID != id || target.Preview {
			writeErr(w, http.StatusConflict, "previewFor must be an accepted message of this session")
			return
		}
		conflict, err := s.store.PreviewConflict(id, target.ParentID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if conflict {
			writeErr(w, http.StatusConflict, "preview already running")
			return
		}
		if err := s.store.DeletePreviewsForParent(id, target.ParentID); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		parentID = target.ParentID
		preview = true
	} else {
		parentID = sess.HeadMessage
	}

	params := s.snapshotParams(sess, rev.ID)
	row, err := s.store.InsertAssistantRow(id, parentID, preview, params)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	str, ok := newSSE(w, r)
	if !ok {
		return
	}
	renderHTML(row)
	str.event("meta", map[string]any{"messageId": row.ID, "message": row})
	sys := rev.Text
	if sess.Kind == "helper" {
		sys = s.helperSystem(sess.File, rev.Text)
	}
	s.streamAssistant(r.Context(), str, sess, row, sys, preview)

	if sess.Kind == "helper" {
		s.saveHelperProposal(row.ID, sess.File, rev.Text)
	}
}

// saveHelperProposal extracts a fenced block from a finished assistant row and
// stores it as a proposal for the file. Best-effort: a failed save just means
// no proposal panel; the reply is already delivered.
func (s *Server) saveHelperProposal(rowID, file, baseText string) {
	fresh, err := s.store.GetMessage(rowID)
	if err != nil || fresh.Status != "done" {
		return
	}
	if prop := extractFenced(fresh.Text); prop != "" {
		hunks := diffx.Diff(baseText, prop)
		_ = s.store.SaveProposedEdit(fresh.ID, file, prop, hunks)
	}
}

// streamAssistant runs one upstream call for a pre-inserted assistant row and
// emits delta/done/error frames. Returns nil on success.
func (s *Server) streamAssistant(ctx context.Context, str *sseStream, sess *store.Session, row *store.Message, system string, preview bool) error {
	hist, err := s.store.HistoryUpTo(sess.ID, row.ParentID)
	if err != nil {
		str.event("error", map[string]string{"message": err.Error()})
		return err
	}
	items := make([]llm.Item, 0, len(hist))
	for _, h := range hist {
		items = append(items, llm.Item{Role: h.Role, Content: h.Text})
	}

	model, effort := s.sessionModel(sess)

	// List configured MCP tools once; a misconfigured server fails the whole
	// generation loudly rather than silently disabling tools.
	var toolSpecs []llm.ToolSpec
	tools, err := s.mcpMgr.Tools(ctx)
	if err != nil {
		s.finalizeRow(row, llm.Result{}, "error", err.Error())
		str.event("error", map[string]string{"message": err.Error()})
		return err
	}
	for _, t := range tools {
		toolSpecs = append(toolSpecs, llm.ToolSpec{Name: t.Name, Description: t.Description, Parameters: t.InputSchema})
	}

	relayDelta := func(kind, text string) {
		str.event("delta", map[string]any{"kind": kind, "text": text})
	}

	// Accumulate text/reasoning/tokens across tool rounds; finalize once.
	acc := llm.Result{}
	var rounds []llm.ToolRound
	finished := false
	for attempt := 0; attempt < 10 && !finished; attempt++ {
		p := llm.Params{
			Model:       model,
			System:      system,
			History:     items,
			Effort:      effort,
			Temperature: sess.Temperature,
			MaxTokens:   sess.MaxTokens,
			ExtraBody:   sess.ExtraBody,
			Tools:       toolSpecs,
			ToolRounds:  rounds,
		}
		res, streamErr := s.llm.Stream(ctx, p, relayDelta)

		acc.Text += res.Text
		acc.Reasoning += res.Reasoning
		if res.PromptTokens != nil {
			if acc.PromptTokens == nil {
				pt := *res.PromptTokens
				acc.PromptTokens = &pt
			} else {
				*acc.PromptTokens += *res.PromptTokens
			}
		}
		if res.OutputTokens != nil {
			if acc.OutputTokens == nil {
				ot := *res.OutputTokens
				acc.OutputTokens = &ot
			} else {
				*acc.OutputTokens += *res.OutputTokens
			}
		}

		switch {
		case errors.Is(streamErr, llm.ErrCanceled):
			s.finalizeRow(row, acc, "canceled", "stream canceled")
			return streamErr
		case streamErr != nil:
			s.finalizeRow(row, acc, "error", streamErr.Error())
			str.event("error", map[string]string{"message": streamErr.Error()})
			return streamErr
		}

		if len(res.ToolCalls) == 0 {
			finished = true
			break
		}

		results := []llm.ToolResult{}
		for _, call := range res.ToolCalls {
			args := map[string]any{}
			if call.Function.Arguments != "" {
				_ = json.Unmarshal([]byte(call.Function.Arguments), &args)
			}
			cr, err := s.mcpMgr.CallTool(ctx, call.Function.Name, args)
			if err != nil {
				s.finalizeRow(row, acc, "error", err.Error())
				str.event("error", map[string]string{"message": err.Error()})
				return err
			}
			results = append(results, llm.ToolResult{ToolCallID: call.ID, Content: cr.Content})
		}
		rounds = append(rounds, llm.ToolRound{AssistantContent: res.Text, ToolCalls: res.ToolCalls, Results: results})
	}
	if !finished {
		msg := "tool loop exceeded 10 rounds"
		s.finalizeRow(row, acc, "error", msg)
		str.event("error", map[string]string{"message": msg})
		return fmt.Errorf("%s", msg)
	}

	fresh := s.finalizeRow(row, acc, "done", "")
	if !preview {
		if err := s.store.SetHeadDirect(sess.ID, row.ID); err != nil {
			str.event("error", map[string]string{"message": err.Error()})
			return err
		}
	}
	str.event("done", map[string]any{"messageId": row.ID, "message": fresh})
	return nil
}

// finalizeRow writes terminal content and returns the fresh row.
func (s *Server) finalizeRow(row *store.Message, res llm.Result, status, errMsg string) *store.Message {
	if err := s.store.FinishMessage(row.ID, res.Text, res.Reasoning, res.PromptTokens, res.OutputTokens, status, errMsg); err != nil {
		return row
	}
	fresh, err := s.store.GetMessage(row.ID)
	if err != nil {
		return row
	}
	renderHTML(fresh)
	return fresh
}

// snapshotParams captures generation parameters plus the recorded revision.
func (s *Server) snapshotParams(sess *store.Session, revID string) store.AssistantParams {
	model, effort := s.sessionModel(sess)
	return store.AssistantParams{
		Model:        model,
		ReasoningEff: effort,
		Temperature:  sess.Temperature,
		MaxTokens:    sess.MaxTokens,
		PromptRevID:  revID,
	}
}
