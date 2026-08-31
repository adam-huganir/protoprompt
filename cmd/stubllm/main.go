// Command stubllm is a dev-only OpenAI-compatible streaming chat-completions
// stub for protoprompt. It echoes the last user message with a fake
// reasoning preamble so the whole UI can be exercised without a paid endpoint.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

// request is the subset of the OpenAI chat-completions body we care about.
type request struct {
	Model    string    `json:"model"`
	Messages []message `json:"messages"`
	Stream   bool      `json:"stream"`
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// delta is a regular chunk's delta; empty fields are omitted so the finish
// chunk serializes as {}.
type delta struct {
	Role             string `json:"role,omitempty"`
	ReasoningContent string `json:"reasoning_content,omitempty"`
	Content          string `json:"content,omitempty"`
}

type choice struct {
	Index        int     `json:"index"`
	Delta        delta   `json:"delta"`
	FinishReason *string `json:"finish_reason"`
}

type chunk struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Created int64    `json:"created"`
	Model   string   `json:"model"`
	Choices []choice `json:"choices"`
	Usage   *usage   `json:"usage"`
}

// The synthetic usage chunk is shaped slightly differently from regular
// chunks: its delta always carries an (empty) content key, and it has a
// non-null usage.
type usageDelta struct {
	Content string `json:"content"`
}

type usageChoice struct {
	Index        int        `json:"index"`
	Delta        usageDelta `json:"delta"`
	FinishReason *string    `json:"finish_reason"`
}

type usageChunk struct {
	ID      string        `json:"id"`
	Object  string        `json:"object"`
	Created int64         `json:"created"`
	Model   string        `json:"model"`
	Choices []usageChoice `json:"choices"`
	Usage   usage         `json:"usage"`
}

func newChunk(model string, d delta, finishReason *string) chunk {
	return chunk{
		ID:      "chatcmpl-stub",
		Object:  "chat.completion.chunk",
		Created: time.Now().Unix(),
		Model:   model,
		Choices: []choice{{Index: 0, Delta: d, FinishReason: finishReason}},
	}
}

// send writes one SSE event (data: <json>\n\n) and flushes it.
func send(w http.ResponseWriter, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	fmt.Fprintf(w, "data: %s\n\n", b)
	flush(w)
}

func flush(w http.ResponseWriter) {
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// emitPieces streams text as delta chunks of at most n runes, sleeping delay
// after each piece. It stops early if the request context is canceled.
func emitPieces(ctx context.Context, w http.ResponseWriter, model string, reasoning bool, text string, n int, delay time.Duration) {
	runes := []rune(text)
	for i := 0; i < len(runes); i += n {
		j := min(i+n, len(runes))
		var d delta
		piece := string(runes[i:j])
		if reasoning {
			d.ReasoningContent = piece
		} else {
			d.Content = piece
		}
		send(w, newChunk(model, d, nil))
		time.Sleep(delay)
		if ctx.Err() != nil {
			return
		}
	}
}

func handleCompletions(w http.ResponseWriter, r *http.Request) {
	var req request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":{"message":"invalid request"}}`)
		return
	}

	input := ""
	systemText := ""
	totalChars := 0
	for _, m := range req.Messages {
		totalChars += utf8.RuneCountInString(m.Content)
		switch m.Role {
		case "user":
			input = strings.TrimSpace(m.Content)
		case "system":
			if systemText == "" {
				systemText = m.Content
			}
		}
	}

	reasoningText := "Thinking about " + input + "…"
	finalText := "Echo: " + input
	if strings.Contains(strings.ToLower(input), "propose") {
		finalText = "```markdown\n" + systemText + "\n- new section added by helper\n```\n"
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")

	// 1. Role announcement.
	send(w, newChunk(req.Model, delta{Role: "assistant"}, nil))

	ctx := r.Context()

	// 2. Reasoning preamble (~4 chars per chunk).
	emitPieces(ctx, w, req.Model, true, reasoningText, 4, 8*time.Millisecond)
	if ctx.Err() != nil {
		return
	}

	// 3. Final text (~2 chars per chunk).
	emitPieces(ctx, w, req.Model, false, finalText, 2, 10*time.Millisecond)
	if ctx.Err() != nil {
		return
	}

	// 4. Finish marker with empty delta.
	stop := "stop"
	send(w, newChunk(req.Model, delta{}, &stop))

	// 5. Synthetic usage chunk: non-empty choices on purpose, so index-based
	//    parsers exercise their guard against double-appending from it.
	promptTokens := max(1, totalChars/4)
	completionTokens := max(1, (utf8.RuneCountInString(reasoningText)+utf8.RuneCountInString(finalText))/4)
	send(w, usageChunk{
		ID:      "chatcmpl-stub",
		Object:  "chat.completion.chunk",
		Created: time.Now().Unix(),
		Model:   req.Model,
		Choices: []usageChoice{{Index: 0}},
		Usage: usage{
			PromptTokens:     promptTokens,
			CompletionTokens: completionTokens,
			TotalTokens:      promptTokens + completionTokens,
		},
	})

	// 6. Terminal marker.
	fmt.Fprint(w, "data: [DONE]\n\n")
	flush(w)
}

// handleModels answers the OpenAI-compatible model list. "stub" matches the
// smoke config; the -alt ids exist only upstream, so they prove a client
// really read this endpoint rather than its own config.
func handleModels(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	fmt.Fprint(w, `{"object":"list","data":[`+
		`{"id":"stub","object":"model","created":1700000000,"owned_by":"stub"},`+
		`{"id":"stub-alt","object":"model","created":1700000000,"owned_by":"stub"},`+
		`{"id":"stub-alt-2","object":"model","created":1700000000,"owned_by":"stub"}]}`)
}

func main() {
	port := flag.Int("port", 9876, "port to listen on (bound to 127.0.0.1)")
	flag.Parse()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat/completions", handleCompletions)
	mux.HandleFunc("POST /chat/completions", handleCompletions)
	mux.HandleFunc("GET /v1/models", handleModels)
	mux.HandleFunc("GET /models", handleModels)

	addr := fmt.Sprintf("127.0.0.1:%d", *port)
	log.Printf("stubllm listening on http://%s (POST /v1/chat/completions)", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatal(err)
	}
}
