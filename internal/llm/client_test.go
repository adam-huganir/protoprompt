package llm

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func sseBody(chunks ...string) string {
	var b strings.Builder
	for _, c := range chunks {
		b.WriteString("data: ")
		b.WriteString(c)
		b.WriteString("\n\n")
	}
	b.WriteString("data: [DONE]\n\n")
	return b.String()
}

func TestStreamContentReasoningUsageGuard(t *testing.T) {
	// The usage chunk also carries non-empty delta text; it must NOT be
	// appended (LiteLLM synthetic usage chunk shape).
	body := sseBody(
		`{"choices":[{"delta":{"reasoning_content":"Let me "}}]}`,
		`{"choices":[{"delta":{"reasoning_content":"think"}}]}`,
		`{"choices":[{"delta":{"content":"Hello"}}]}`,
		`{"choices":[{"delta":{"content":" world"}}]}`,
		`{"choices":[{"delta":{"content":"SHOULD_NOT_APPEARS"}}],"usage":{"prompt_tokens":7,"completion_tokens":3}}`,
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer k1" {
			t.Errorf("missing auth header: %q", r.Header.Get("Authorization"))
		}
		if got := r.Header.Get("Accept"); got != "text/event-stream" {
			t.Errorf("accept = %q", got)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, body)
	}))
	defer srv.Close()

	c := New(srv.URL, "k1", 5*time.Second)
	var got []string
	res, err := c.Stream(context.Background(), Params{Model: "m", System: "sys", History: []Item{{Role: "user", Content: "hi"}}, Temperature: 1},
		func(kind, text string) { got = append(got, kind+":"+text) })
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if res.Text != "Hello world" {
		t.Errorf("Text = %q", res.Text)
	}
	if res.Reasoning != "Let me think" {
		t.Errorf("Reasoning = %q", res.Reasoning)
	}
	if res.PromptTokens == nil || *res.PromptTokens != 7 {
		t.Errorf("PromptTokens = %v", res.PromptTokens)
	}
	if res.OutputTokens == nil || *res.OutputTokens != 3 {
		t.Errorf("OutputTokens = %v", res.OutputTokens)
	}
	for _, g := range got {
		if strings.Contains(g, "SHOULD_NOT_APPEARS") {
			t.Errorf("usage chunk text leaked into stream: %v", got)
		}
	}
}

func TestStreamReasoningAlias(t *testing.T) {
	body := sseBody(`{"choices":[{"delta":{"reasoning":"alt-reason"}}]}`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, body)
	}))
	defer srv.Close()
	c := New(srv.URL, "", time.Second)
	res, err := c.Stream(context.Background(), Params{Model: "m"}, func(string, string) {})
	if err != nil {
		t.Fatal(err)
	}
	if res.Reasoning != "alt-reason" {
		t.Errorf("reasoning alias = %q", res.Reasoning)
	}
}

func TestStreamNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":{"message":"model not found"}}`)
	}))
	defer srv.Close()
	c := New(srv.URL, "x", time.Second)
	_, err := c.Stream(context.Background(), Params{Model: "m"}, func(string, string) {})
	if err == nil || !strings.Contains(err.Error(), "model not found") {
		t.Fatalf("expected upstream error with message, got %v", err)
	}
}

func TestStreamCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"partial"}}]}`+"\n\n")
		w.(http.Flusher).Flush()
		// Keep writing until the client's disconnect makes writes fail.
		for range 2000 {
			if _, err := fmt.Fprint(w, ": keep\n\n\n"); err != nil {
				return
			}
			w.(http.Flusher).Flush()
			time.Sleep(5 * time.Millisecond)
		}
	}))
	defer srv.Close()
	c := New(srv.URL, "x", 150*time.Millisecond)
	res, err := c.Stream(context.Background(), Params{Model: "m"}, func(string, string) {})
	if err != ErrCanceled {
		t.Fatalf("expected ErrCanceled, got %v", err)
	}
	if res.Text != "partial" {
		t.Errorf("partial text = %q", res.Text)
	}
}

func TestEndpointJoin(t *testing.T) {
	c := &Client{BaseURL: "https://api.openai.com/v1/"}
	if got := c.Endpoint(); got != "https://api.openai.com/v1/chat/completions" {
		t.Errorf("Endpoint = %q", got)
	}
	c = &Client{BaseURL: "https://litellm.local/v1/chat/completions"}
	if got := c.Endpoint(); got != "https://litellm.local/v1/chat/completions" {
		t.Errorf("Endpoint = %q", got)
	}
}

func TestBodyMergeExtraFirst(t *testing.T) {
	mt := 99
	body := buildBody(Params{
		Model: "m", System: "s", Temperature: 0.5, MaxTokens: &mt,
		ExtraBody: map[string]any{"model": "ignored", "foo": "bar"},
	})
	s := string(body)
	if !strings.Contains(s, `"model":"m"`) {
		t.Errorf("core model should win: %s", s)
	}
	if !strings.Contains(s, `"foo":"bar"`) {
		t.Errorf("extra key missing: %s", s)
	}
	if !strings.Contains(s, `"max_tokens":99`) {
		t.Errorf("max_tokens missing: %s", s)
	}
}

func TestModelsParsesDedupesSorts(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Errorf("path = %q, want /models", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"object":"list","data":[{"id":"zeta"},{"id":"alpha"},{"id":"alpha"},{"id":"  "}]}`)
	}))
	defer srv.Close()

	c := New(srv.URL, "k1", 5*time.Second)
	got, err := c.Models(context.Background())
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	if len(got) != 2 || got[0] != "alpha" || got[1] != "zeta" {
		t.Errorf("Models = %v, want [alpha zeta]", got)
	}
	if gotAuth != "Bearer k1" {
		t.Errorf("Authorization = %q", gotAuth)
	}
}

func TestModelsStripsChatCompletionsSuffix(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		fmt.Fprint(w, `{"data":[{"id":"m1"}]}`)
	}))
	defer srv.Close()

	c := New(srv.URL+"/v1/chat/completions", "k1", 5*time.Second)
	got, err := c.Models(context.Background())
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	if gotPath != "/v1/models" {
		t.Errorf("path = %q, want /v1/models", gotPath)
	}
	if len(got) != 1 || got[0] != "m1" {
		t.Errorf("Models = %v", got)
	}
}

func TestModelsEmptyListIsNotAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"object":"list","data":[]}`)
	}))
	defer srv.Close()
	c := New(srv.URL, "k1", 5*time.Second)
	got, err := c.Models(context.Background())
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("Models = %v, want empty", got)
	}
}

func TestModelsNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":{"message":"bad key"}}`)
	}))
	defer srv.Close()
	c := New(srv.URL, "nope", 5*time.Second)
	_, err := c.Models(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "upstream 401") || !strings.Contains(err.Error(), "bad key") {
		t.Errorf("err = %v", err)
	}
}
