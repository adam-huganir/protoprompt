package api

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"protoprompt/internal/config"
	"protoprompt/internal/db"
	"protoprompt/internal/store"
)

// echoUpstream streams "Echo: <last user message>" with reasoning, then a
// usage chunk, mirroring the stub server's shape.
func echoUpstream() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"object":"list","data":[{"id":"stub"},{"id":"stub-alt"}]}`)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		json.Unmarshal(body, &req)
		last := ""
		for _, m := range req.Messages {
			if m.Role == "user" {
				last = m.Content
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		f := w.(http.Flusher)
		send := func(s string) { fmt.Fprint(w, s); f.Flush() }
		send("data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"Thinking about " + last + "\"}}]}\n\n")
		send("data: {\"choices\":[{\"delta\":{\"content\":\"Echo: " + last + "\"}}]}\n\n")
		send("data: {\"choices\":[{\"delta\":{}}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":5,\"total_tokens\":15}}\n\n")
		send("data: [DONE]\n\n")
	}))
}

type liveServer struct {
	t   *testing.T
	srv *httptest.Server
}

func newLiveServer(t *testing.T) *liveServer {
	t.Helper()
	up := echoUpstream()
	t.Cleanup(up.Close)
	h, err := db.Open(filepath.Join(t.TempDir(), "live.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close() })
	cfg := config.Config{
		BaseURL:      up.URL,
		APIKey:       "testkey",
		Models:       []string{"stub"},
		DefaultModel: "stub",
		ExtraBody:    map[string]any{},
		Dir:          t.TempDir(),
		Port:         8420,
		Host:         "127.0.0.1",
	}
	st := store.New(cfg.Dir, h)
	srv := httptest.NewServer(New(cfg, h, st, nil).Handler())
	t.Cleanup(srv.Close)
	return &liveServer{t: t, srv: srv}
}

func (ls *liveServer) req(method, path string, body any) (int, []byte) {
	ls.t.Helper()
	return do(ls.t, ls.srv, method, path, body)
}

// streamSSETest POSTs url with body and collects SSE events until EOF.
func (ls *liveServer) streamSSETest(url string, body any) []sseEvent {
	ls.t.Helper()
	b, _ := json.Marshal(body)
	resp, err := http.Post(ls.srv.URL+url, "application/json", strings.NewReader(string(b)))
	if err != nil {
		ls.t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		ls.t.Fatalf("stream %s: status %d body %s", url, resp.StatusCode, data)
	}
	return parseSSE(ls.t, resp.Body)
}

type sseEvent struct {
	Event string
	Data  map[string]any
}

func parseSSE(t *testing.T, r io.Reader) []sseEvent {
	t.Helper()
	sc := bufio.NewScanner(r)
	var out []sseEvent
	event := ""
	data := ""
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			if event != "" || data != "" {
				var m map[string]any
				if data != "" {
					if err := json.Unmarshal([]byte(data), &m); err != nil {
						t.Fatalf("bad SSE data %q: %v", data, err)
					}
				}
				out = append(out, sseEvent{Event: event, Data: m})
				event, data = "", ""
			}
			continue
		}
		if strings.HasPrefix(line, "event:") {
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		} else if strings.HasPrefix(line, "data:") {
			data = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("SSE read: %v", err)
	}
	return out
}

func mustJSON[T any](t *testing.T, data []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatalf("unmarshal %s: %v", data, err)
	}
	return v
}

func seedFile(t *testing.T, ls *liveServer, path, text string) string {
	t.Helper()
	code, body := ls.req("POST", "/api/files", map[string]string{"path": path})
	if code != http.StatusCreated {
		t.Fatalf("seed create: %d %s", code, body)
	}
	code, body = ls.req("POST", "/api/files/"+path+"/save", map[string]string{"text": text})
	if code != http.StatusOK {
		t.Fatalf("seed save: %d %s", code, body)
	}
	saved := mustJSON[map[string]any](t, body)
	rev, _ := saved["revId"].(string)
	if rev == "" {
		t.Fatalf("no rev id in %s", body)
	}
	return rev
}

func createSession(t *testing.T, ls *liveServer, file string) map[string]any {
	t.Helper()
	code, body := ls.req("POST", "/api/sessions", map[string]any{"file": file})
	if code != http.StatusCreated {
		t.Fatalf("create session: %d %s", code, body)
	}
	return mustJSON[map[string]any](t, body)
}

func addUserMessage(t *testing.T, ls *liveServer, sid string, parent *string, text string) map[string]any {
	t.Helper()
	body := map[string]any{"text": text}
	if parent != nil {
		body["parentId"] = *parent
	}
	code, resp := ls.req("POST", "/api/sessions/"+sid+"/messages", body)
	if code != http.StatusCreated {
		t.Fatalf("add message: %d %s", code, resp)
	}
	return mustJSON[map[string]any](t, resp)
}

func TestGenerateFlow(t *testing.T) {
	ls := newLiveServer(t)
	rev1 := seedFile(t, ls, "p.md", "be nice")
	sess := createSession(t, ls, "p.md")
	sid := sess["id"].(string)

	u1 := addUserMessage(t, ls, sid, nil, "hello")
	uid := u1["id"].(string)

	events := ls.streamSSETest("/api/sessions/"+sid+"/generate", map[string]any{})
	var done map[string]any
	deltas := 0
	for _, ev := range events {
		switch ev.Event {
		case "meta":
		case "delta":
			deltas++
		case "done":
			done = ev.Data
		case "error":
			t.Fatalf("stream error: %v", ev.Data)
		}
	}
	if done == nil {
		t.Fatalf("no done event; events: %+v", events)
	}
	if deltas < 1 {
		t.Error("expected at least one delta")
	}
	msg := done["message"].(map[string]any)
	if msg["text"] != "Echo: hello" {
		t.Errorf("text = %v", msg["text"])
	}
	if msg["reasoningText"] != "Thinking about hello" {
		t.Errorf("reasoning = %v", msg["reasoningText"])
	}
	pt, _ := msg["promptTokens"].(float64)
	if pt != 10 {
		t.Errorf("promptTokens = %v", msg["promptTokens"])
	}
	if msg["promptRevId"] != rev1 {
		t.Errorf("promptRevId = %v want %v", msg["promptRevId"], rev1)
	}
	if uid == "" {
		t.Fatal("user message missing")
	}

	// Session payload: two messages, head at assistant.
	code, body := ls.req("GET", "/api/sessions/"+sid, nil)
	if code != http.StatusOK {
		t.Fatalf("get session: %d %s", code, body)
	}
	payload := mustJSON[map[string]any](t, body)
	sessBack := payload["session"].(map[string]any)
	if sessBack["headMessageId"] != done["messageId"] {
		t.Error("head did not advance")
	}
	msgs := payload["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("transcript len = %d", len(msgs))
	}
}

func TestPromptDriftUsesLatestRev(t *testing.T) {
	ls := newLiveServer(t)
	rev1 := seedFile(t, ls, "p.md", "v1")
	sess := createSession(t, ls, "p.md")
	sid := sess["id"].(string)
	addUserMessage(t, ls, sid, nil, "q1")
	events := ls.streamSSETest("/api/sessions/"+sid+"/generate", map[string]any{})
	if len(events) == 0 {
		t.Fatal("no events")
	}
	// Save a new revision; next generate must carry the newer rev id.
	code, body := ls.req("POST", "/api/files/p.md/save", map[string]string{"text": "v2"})
	if code != http.StatusOK {
		t.Fatal(code, string(body))
	}
	rev2 := mustJSON[map[string]any](t, body)["revId"].(string)
	if rev2 == rev1 {
		t.Fatal("expected new rev")
	}
	// Use proper parent = current head via session payload
	code, body = ls.req("GET", "/api/sessions/"+sid, nil)
	sessBack := mustJSON[map[string]any](t, body)["session"].(map[string]any)
	head := sessBack["headMessageId"].(string)
	addUserMessage(t, ls, sid, &head, "q3")
	events = ls.streamSSETest("/api/sessions/"+sid+"/generate", map[string]any{})
	var last map[string]any
	for _, ev := range events {
		if ev.Event == "done" {
			last = ev.Data
		}
	}
	if last == nil {
		t.Fatal("no done")
	}
	msg := last["message"].(map[string]any)
	if msg["promptRevId"] != rev2 {
		t.Errorf("latest rev not used: %v", msg["promptRevId"])
	}
}

func TestPreviewAcceptFlow(t *testing.T) {
	ls := newLiveServer(t)
	seedFile(t, ls, "p.md", "base prompt")
	sess := createSession(t, ls, "p.md")
	sid := sess["id"].(string)
	u1 := addUserMessage(t, ls, sid, nil, "hello")

	gen := ls.streamSSETest("/api/sessions/"+sid+"/generate", map[string]any{})
	var a1 map[string]any
	_ = u1
	for _, ev := range gen {
		if ev.Event == "done" {
			a1 = ev.Data["message"].(map[string]any)
		}
	}
	if a1 == nil {
		t.Fatal("no a1")
	}

	// Preview a1.
	prevEvents := ls.streamSSETest("/api/sessions/"+sid+"/generate", map[string]any{"previewFor": a1["id"]})
	var pvID string
	for _, ev := range prevEvents {
		if ev.Event == "done" {
			pvID = ev.Data["messageId"].(string)
		}
	}
	if pvID == "" {
		t.Fatalf("no preview done event: %+v", prevEvents)
	}

	// Session payload shows previewMessage on a1.
	code, body := ls.req("GET", "/api/sessions/"+sid, nil)
	payload := mustJSON[map[string]any](t, body)
	msgs := payload["messages"].([]any)
	a1Row := msgs[1].(map[string]any)
	pv := a1Row["previewMessage"].(map[string]any)
	if pv == nil || pv["id"] != pvID {
		t.Fatalf("previewMessage not attached: %v", a1Row["previewMessage"])
	}
	if pv["preview"] != true {
		t.Error("preview flag should be true")
	}

	// Accept.
	code, body = ls.req("POST", "/api/messages/"+pvID+"/accept", nil)
	if code != http.StatusOK {
		t.Fatalf("accept: %d %s", code, body)
	}
	// Head moved to the preview; a1's siblingCount is 2.
	code, body = ls.req("GET", "/api/sessions/"+sid, nil)
	payload = mustJSON[map[string]any](t, body)
	sessBack := payload["session"].(map[string]any)
	if sessBack["headMessageId"] != pvID {
		t.Errorf("head after accept = %v want %v", sessBack["headMessageId"], pvID)
	}
	msgs = payload["messages"].([]any)
	found := false
	for _, m := range msgs {
		row := m.(map[string]any)
		if row["id"] == pvID {
			found = true
			if row["siblingCount"].(float64) != 2 {
				t.Errorf("siblingCount = %v want 2", row["siblingCount"])
			}
		}
	}
	if !found {
		t.Error("accepted preview missing from transcript")
	}

	// Accept again → 409.
	if code, _ = ls.req("POST", "/api/messages/"+pvID+"/accept", nil); code != http.StatusConflict {
		t.Errorf("double accept: got %d", code)
	}
}

func TestResendFlow(t *testing.T) {
	ls := newLiveServer(t)
	seedFile(t, ls, "p.md", "prompt")
	sess := createSession(t, ls, "p.md")
	sid := sess["id"].(string)
	u1 := addUserMessage(t, ls, sid, nil, "first")
	uid := u1["id"].(string)
	gen := ls.streamSSETest("/api/sessions/"+sid+"/generate", map[string]any{})
	var a1ID string
	for _, ev := range gen {
		if ev.Event == "done" {
			a1ID = ev.Data["messageId"].(string)
		}
	}
	if a1ID == "" {
		t.Fatal("no a1")
	}
	u2 := addUserMessage(t, ls, sid, &a1ID, "second")
	_ = u2
	gen = ls.streamSSETest("/api/sessions/"+sid+"/generate", map[string]any{})
	var a2ID string
	for _, ev := range gen {
		if ev.Event == "done" {
			a2ID = ev.Data["messageId"].(string)
		}
	}
	if a2ID == "" {
		t.Fatal("no a2")
	}

	// Resend editing u1 → chain of U2 copy + assistant + user "second" copy + assistant.
	events := ls.streamSSETest("/api/sessions/"+sid+"/resend",
		map[string]string{"messageId": uid, "text": "first EDITED"})
	var gotMeta int
	var userTexts []string
	var assistantDones int
	var completeHead string
	sawError := false
	for _, ev := range events {
		if ev.Event == "meta" {
			gotMeta++
			if m, ok := ev.Data["message"].(map[string]any); ok {
				if m["role"] == "user" {
					userTexts = append(userTexts, m["text"].(string))
				}
			}
		}
		if ev.Event == "done" {
			if m, ok := ev.Data["message"].(map[string]any); ok {
				if m["role"] == "assistant" {
					assistantDones++
				}
			}
		}
		if ev.Event == "error" {
			sawError = true
			t.Logf("resend error event: %v", ev.Data)
		}
		if ev.Event == "complete" {
			completeHead, _ = ev.Data["headMessageId"].(string)
		}
	}
	if sawError {
		t.Fatalf("resend produced error; userTexts=%v metas=%d", userTexts, gotMeta)
	}
	if completeHead == "" {
		t.Fatalf("no complete frame; metas=%d", gotMeta)
	}
	if len(userTexts) != 2 {
		t.Errorf("expected 2 user nodes (edited + copied), got %v", userTexts)
	}
	if userTexts[0] != "first EDITED" {
		t.Errorf("edited text = %q", userTexts[0])
	}
	if assistantDones != 2 {
		t.Errorf("assistant regenerations = %d want 2", assistantDones)
	}

	// Head at last regenerated assistant; original branch intact via siblings.
	_, body := ls.req("GET", "/api/sessions/"+sid, nil)
	payload := mustJSON[map[string]any](t, body)
	sessBack := payload["session"].(map[string]any)
	if sessBack["headMessageId"] != completeHead {
		t.Errorf("head = %v want %v", sessBack["headMessageId"], completeHead)
	}
	msgs := payload["messages"].([]any)
	if len(msgs) != 4 {
		t.Fatalf("new chain transcript len = %d want 4", len(msgs))
	}
	first := msgs[0].(map[string]any)
	if first["text"] != "first EDITED" {
		t.Errorf("first transcript text = %v", first["text"])
	}
	if first["siblingCount"].(float64) != 2 {
		t.Errorf("edited root should have 2 siblings, got %v", first["siblingCount"])
	}
}

func TestGenerateNoRev(t *testing.T) {
	ls := newLiveServer(t)
	code, body := ls.req("POST", "/api/files", map[string]string{"path": "empty.md"})
	if code != http.StatusCreated {
		t.Fatal(code, string(body))
	}
	sess := createSession(t, ls, "empty.md")
	sid := sess["id"].(string)
	resp, err := http.Post(ls.srv.URL+"/api/sessions/"+sid+"/generate", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409, got %d", resp.StatusCode)
	}
	data, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(data), "save the editor first") {
		t.Errorf("body = %s", data)
	}
}

func TestContextEndpoint(t *testing.T) {
	ls := newLiveServer(t)
	seedFile(t, ls, "p.md", "sys prompt")
	sess := createSession(t, ls, "p.md")
	sid := sess["id"].(string)
	u1 := addUserMessage(t, ls, sid, nil, "hi")
	uid := u1["id"].(string)
	code, body := ls.req("GET", "/api/messages/"+uid+"/context", nil)
	if code != http.StatusOK {
		t.Fatalf("context: %d %s", code, body)
	}
	ctxData := mustJSON[map[string]any](t, body)
	if ctxData["system"] != "sys prompt" {
		t.Errorf("system = %v", ctxData["system"])
	}
	hist := ctxData["history"].([]any)
	if len(hist) != 1 || hist[0].(map[string]any)["content"] != "hi" {
		t.Errorf("history = %v", hist)
	}
}

func TestNoKey501(t *testing.T) {
	h, err := db.Open(filepath.Join(t.TempDir(), "nokey.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	cfg := config.Config{ExtraBody: map[string]any{}, Dir: t.TempDir()}
	srv := httptest.NewServer(New(cfg, h, store.New(cfg.Dir, h), nil).Handler())
	defer srv.Close()

	// seed file+rev+session
	b, _ := json.Marshal(map[string]string{"path": "p.md"})
	http.Post(srv.URL+"/api/files", "application/json", strings.NewReader(string(b)))
	b, _ = json.Marshal(map[string]string{"text": "x"})
	http.Post(srv.URL+"/api/files/p.md/save", "application/json", strings.NewReader(string(b)))
	b, _ = json.Marshal(map[string]any{"file": "p.md"})
	resp, err := http.Post(srv.URL+"/api/sessions", "application/json", strings.NewReader(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var sess map[string]any
	json.Unmarshal(data, &sess)

	resp, err = http.Post(srv.URL+"/api/sessions/"+sess["id"].(string)+"/generate", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 501, got %d %s", resp.StatusCode, data)
	}
}

func TestModelsEndpoint(t *testing.T) {
	ls := newLiveServer(t)
	code, body := ls.req("GET", "/api/models", nil)
	if code != http.StatusOK {
		t.Fatalf("models: %d %s", code, body)
	}
	got := mustJSON[map[string]any](t, body)["models"]
	list, _ := got.([]any)
	if len(list) != 2 || list[0] != "stub" || list[1] != "stub-alt" {
		t.Fatalf("models = %v", got)
	}
}

func TestModelsNoKey501(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("upstream must not be called without an API key")
	}))
	defer up.Close()
	h, err := db.Open(filepath.Join(t.TempDir(), "nomodels.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	cfg := config.Config{BaseURL: up.URL, ExtraBody: map[string]any{}, Dir: t.TempDir()}
	srv := httptest.NewServer(New(cfg, h, store.New(cfg.Dir, h), nil).Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/models")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 501, got %d %s", resp.StatusCode, data)
	}
}

func TestSwitchModelAppliesToNextReply(t *testing.T) {
	ls := newLiveServer(t)
	seedFile(t, ls, "m.md", "be nice")
	sess := createSession(t, ls, "m.md")
	sid := sess["id"].(string)
	if sess["model"] != "stub" {
		t.Fatalf("session model = %v", sess["model"])
	}

	if code, body := ls.req("POST", "/api/sessions/"+sid+"/model", map[string]string{"model": "  "}); code != http.StatusBadRequest {
		t.Fatalf("blank model: want 400, got %d %s", code, body)
	}
	if code, body := ls.req("POST", "/api/sessions/nope/model", map[string]string{"model": "x"}); code != http.StatusNotFound {
		t.Fatalf("unknown session: want 404, got %d %s", code, body)
	}

	code, body := ls.req("POST", "/api/sessions/"+sid+"/model", map[string]string{"model": "stub-alt"})
	if code != http.StatusOK {
		t.Fatalf("switch model: %d %s", code, body)
	}
	updated := mustJSON[map[string]any](t, body)
	if updated["model"] != "stub-alt" {
		t.Fatalf("updated model = %v", updated["model"])
	}

	addUserMessage(t, ls, sid, nil, "hello")
	events := ls.streamSSETest("/api/sessions/"+sid+"/generate", map[string]any{})
	var done map[string]any
	for _, ev := range events {
		if ev.Event == "done" {
			done = ev.Data
		}
		if ev.Event == "error" {
			t.Fatalf("stream error: %v", ev.Data)
		}
	}
	if done == nil {
		t.Fatalf("no done event; events: %+v", events)
	}
	msg := done["message"].(map[string]any)
	if msg["model"] != "stub-alt" {
		t.Errorf("reply model = %v, want stub-alt", msg["model"])
	}
}
