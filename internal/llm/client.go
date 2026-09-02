// Package llm streams chat completions from an OpenAI-compatible upstream.
package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// ErrCanceled signals a canceled/aborted stream; the returned Result holds
// partial buffers.
var ErrCanceled = errors.New("stream canceled")

// Item is one history message sent to the model.
type Item struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ToolSpec describes one tool the model may call (OpenAI function shape).
type ToolSpec struct {
	Name        string
	Description string
	Parameters  map[string]any
}

// ToolCall is one tool invocation emitted by a streaming model round.
type ToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function ToolCallFunction `json:"function"`
}

type ToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ToolResult feeds one tool's outcome back to the model.
type ToolResult struct {
	ToolCallID string
	Content    string
}

// ToolRound is one model↔tool exchange: assistant text + tool calls, then
// each tool's result.
type ToolRound struct {
	AssistantContent string
	ToolCalls        []ToolCall
	Results          []ToolResult
}

// Params describes one upstream call.
type Params struct {
	Model       string
	System      string
	History     []Item
	Effort      string
	Temperature float64
	MaxTokens   *int
	ExtraBody   map[string]any
	Tools       []ToolSpec
	ToolRounds  []ToolRound
}

// Result holds the accumulated stream output.
type Result struct {
	Text         string
	Reasoning    string
	PromptTokens *int
	OutputTokens *int
	ToolCalls    []ToolCall
}

type Client struct {
	BaseURL string
	APIKey  string
	Timeout time.Duration
	HTTP    *http.Client
}

func New(baseURL, apiKey string, timeout time.Duration) *Client {
	return &Client{BaseURL: baseURL, APIKey: apiKey, Timeout: timeout, HTTP: &http.Client{}}
}

// Endpoint joins baseUrl per plan: trim trailing slashes; append
// /chat/completions unless already present.
func (c *Client) Endpoint() string {
	base := strings.TrimRight(c.BaseURL, "/")
	if strings.HasSuffix(base, "/chat/completions") {
		return base
	}
	return base + "/chat/completions"
}

// modelsEndpoint is the upstream GET /models URL: the same base as Endpoint(),
// with a trailing /chat/completions stripped and /models appended.
func (c *Client) modelsEndpoint() string {
	base := strings.TrimRight(c.BaseURL, "/")
	if trimmed := strings.TrimSuffix(base, "/chat/completions"); trimmed != base {
		base = trimmed
	}
	return base + "/models"
}

// Models returns upstream model ids from GET {base}/models, deduped and
// sorted case-insensitively. An empty result is not an error.
func (c *Client) Models(ctx context.Context) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second) // never inherit the 300s chat timeout
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.modelsEndpoint(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "protoprompt")
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		msg := extractErrMessage(body)
		if msg == "" {
			msg = strings.TrimSpace(string(body))
		}
		if msg == "" {
			msg = resp.Status
		}
		return nil, fmt.Errorf("upstream %s: %s", resp.Status, msg)
	}
	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("upstream models: malformed JSON: %w", err)
	}
	seen := map[string]bool{}
	out := []string{}
	for _, m := range payload.Data {
		id := strings.TrimSpace(m.ID)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := strings.ToLower(out[i]), strings.ToLower(out[j])
		if a != b {
			return a < b
		}
		return out[i] < out[j]
	})
	return out, nil
}

func buildBody(p Params) []byte {
	body := map[string]any{}
	for k, v := range p.ExtraBody {
		body[k] = v
	}
	msgs := make([]any, 0, len(p.History)+1)
	if p.System != "" {
		msgs = append(msgs, Item{Role: "system", Content: p.System})
	}
	for _, h := range p.History {
		msgs = append(msgs, h)
	}
	for _, round := range p.ToolRounds {
		asst := map[string]any{"role": "assistant", "content": round.AssistantContent}
		if len(round.ToolCalls) > 0 {
			tcs := make([]any, 0, len(round.ToolCalls))
			for _, tc := range round.ToolCalls {
				tcs = append(tcs, map[string]any{
					"id":       tc.ID,
					"type":     tc.Type,
					"function": map[string]any{"name": tc.Function.Name, "arguments": tc.Function.Arguments},
				})
			}
			asst["tool_calls"] = tcs
		}
		msgs = append(msgs, asst)
		for _, r := range round.Results {
			msgs = append(msgs, map[string]any{"role": "tool", "tool_call_id": r.ToolCallID, "content": r.Content})
		}
	}
	body["model"] = p.Model
	body["messages"] = msgs
	body["temperature"] = p.Temperature
	body["stream"] = true
	body["stream_options"] = map[string]any{"include_usage": true}
	if p.MaxTokens != nil {
		body["max_tokens"] = *p.MaxTokens
	}
	if p.Effort != "" {
		body["reasoning_effort"] = p.Effort
	}
	if len(p.Tools) > 0 {
		tools := make([]any, 0, len(p.Tools))
		for _, t := range p.Tools {
			tools = append(tools, map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        t.Name,
					"description": t.Description,
					"parameters":  t.Parameters,
				},
			})
		}
		body["tools"] = tools
		body["tool_choice"] = "auto"
	}
	data, _ := json.Marshal(body)
	return data
}

type chunkDelta struct {
	Content          string `json:"content"`
	ReasoningContent string `json:"reasoning_content"`
	Reasoning        string `json:"reasoning"`
	ToolCalls        []struct {
		Index    *int   `json:"index"`
		ID       string `json:"id"`
		Type     string `json:"type"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	} `json:"tool_calls"`
}

type chunkUsage struct {
	PromptTokens     *int `json:"prompt_tokens"`
	CompletionTokens *int `json:"completion_tokens"`
}

type chunk struct {
	Choices []struct {
		Delta chunkDelta `json:"delta"`
	} `json:"choices"`
	Usage *chunkUsage `json:"usage"`
}

// Stream performs the upstream streaming call. onDelta receives ("content"|"reasoning", text).
// The caller-supplied ctx bounds the request; c.Timeout additionally caps it.
func (c *Client) Stream(ctx context.Context, p Params, onDelta func(kind, text string)) (Result, error) {
	if c.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.Timeout)
		defer cancel()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Endpoint(), bytes.NewReader(buildBody(p)))
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("User-Agent", "protoprompt")
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return Result{}, ErrCanceled
		}
		return Result{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2000))
		msg := extractErrMessage(body)
		if msg == "" {
			msg = strings.TrimSpace(string(body))
		}
		if msg == "" {
			msg = resp.Status
		}
		return Result{}, fmt.Errorf("upstream %s: %s", resp.Status, msg)
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)

	var res Result
	var text, reason strings.Builder
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue // SSE comments, event:, id:, retry:
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			break
		}
		var ch chunk
		if err := json.Unmarshal([]byte(payload), &ch); err != nil {
			continue
		}
		// LiteLLM guard: usage chunks carry text we must not re-append.
		if ch.Usage != nil {
			if ch.Usage.PromptTokens != nil {
				pt := *ch.Usage.PromptTokens
				res.PromptTokens = &pt
			}
			if ch.Usage.CompletionTokens != nil {
				ct := *ch.Usage.CompletionTokens
				res.OutputTokens = &ct
			}
			continue
		}
		for _, choice := range ch.Choices {
			if choice.Delta.Content != "" {
				text.WriteString(choice.Delta.Content)
				onDelta("content", choice.Delta.Content)
			}
			rc := choice.Delta.ReasoningContent
			if rc == "" {
				rc = choice.Delta.Reasoning
			}
			if rc != "" {
				reason.WriteString(rc)
				onDelta("reasoning", rc)
			}
			for _, frag := range choice.Delta.ToolCalls {
				idx := 0
				if frag.Index != nil {
					idx = *frag.Index
				}
				for len(res.ToolCalls) <= idx {
					res.ToolCalls = append(res.ToolCalls, ToolCall{Type: "function"})
				}
				tc := &res.ToolCalls[idx]
				if tc.ID == "" && frag.ID != "" {
					tc.ID = frag.ID
				}
				if tc.Function.Name == "" && frag.Function.Name != "" {
					tc.Function.Name = frag.Function.Name
				}
				tc.Function.Arguments += frag.Function.Arguments
			}
		}
	}
	res.Text = text.String()
	res.Reasoning = reason.String()

	if ctx.Err() != nil {
		return res, ErrCanceled
	}
	if err := scanner.Err(); err != nil {
		return res, fmt.Errorf("stream read: %w", err)
	}
	return res, nil
}

func extractErrMessage(body []byte) string {
	var v struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &v) == nil && v.Error.Message != "" {
		return v.Error.Message
	}
	return ""
}
