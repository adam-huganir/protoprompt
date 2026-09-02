package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"protoprompt/internal/version"
)

const (
	protocolVersion = "2024-11-05"
	requestTimeout  = 60 * time.Second
)

// Tool is one tool exposed by an MCP server.
type Tool struct {
	Name        string
	Description string
	InputSchema map[string]any
}

// CallResult is the outcome of one tool call.
type CallResult struct {
	Content string
	IsError bool
}

// Client speaks newline-delimited JSON-RPC (the MCP stdio transport) over a
// Runner's stdin/stdout. Requests are serialized with a mutex; notifications
// (e.g. server→client requests, tools/list_changed) are ignored.
type Client struct {
	runner  Runner
	stdin   io.WriteCloser
	scanner *bufio.Scanner

	reqMu sync.Mutex // serializes request round-trips
	mu    sync.Mutex // guards id + pends
	id    int64
	pends map[int64]chan *rpcMessage

	done chan struct{}
}

// NewStdioClient wraps an exec runner for `command`; it does NOT start the
// process — call Start before ListTools/CallTool.
func NewStdioClient(command string, args []string, env []string) (*Client, error) {
	return newClient(NewStdioRunner(command, args, env))
}

// newClient wraps an arbitrary Runner (the test seam).
func newClient(r Runner) (*Client, error) {
	return &Client{runner: r}, nil
}

// rpcMessage is a parsed JSON-RPC frame. Result and Error are mutually
// exclusive; both are raw so ListTools/CallTool shape them.
type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
	Result  json.RawMessage `json:"result"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// Start launches the runner, sends the initialize handshake, and begins
// reading responses. The caller's ctx bounds the whole handshake, capped at
// requestTimeout.
func (c *Client) Start(ctx context.Context) error {
	stdin, stdout, _, err := c.runner.Start()
	if err != nil {
		return err
	}
	c.stdin = stdin
	c.scanner = bufio.NewScanner(stdout)
	c.scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	c.pends = map[int64]chan *rpcMessage{}
	c.done = make(chan struct{})
	go c.readLoop()

	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	if _, err := c.request(ctx, "initialize", map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "protoprompt", "version": version.Version},
	}); err != nil {
		return err
	}
	return c.notify("notifications/initialized", map[string]any{})
}

// ListTools fetches the server's tool list, mapping each result's inputSchema
// into Tool.InputSchema.
func (c *Client) ListTools(ctx context.Context) ([]Tool, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	resp, err := c.request(ctx, "tools/list", map[string]any{})
	if err != nil {
		return nil, err
	}
	var payload struct {
		Tools []struct {
			Name        string         `json:"name"`
			Description string         `json:"description"`
			InputSchema map[string]any `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(resp.Result, &payload); err != nil {
		return nil, fmt.Errorf("tools/list: malformed result: %w", err)
	}
	out := make([]Tool, 0, len(payload.Tools))
	for _, t := range payload.Tools {
		out = append(out, Tool{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema})
	}
	return out, nil
}

// CallTool invokes `name` with `args`, concatenating text content items and
// propagating isError. Transport failures return an error; isError:true is a
// successful call whose content describes the tool's own failure.
func (c *Client) CallTool(ctx context.Context, name string, args map[string]any) (CallResult, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	resp, err := c.request(ctx, "tools/call", map[string]any{"name": name, "arguments": args})
	if err != nil {
		return CallResult{}, err
	}
	var payload struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(resp.Result, &payload); err != nil {
		return CallResult{}, fmt.Errorf("tools/call: malformed result: %w", err)
	}
	parts := make([]string, 0, len(payload.Content))
	for _, item := range payload.Content {
		if item.Type == "text" {
			parts = append(parts, item.Text)
		}
	}
	return CallResult{Content: strings.Join(parts, "\n"), IsError: payload.IsError}, nil
}

// Close closes stdin (graceful EOF for many servers) and reaps the process
// best-effort.
func (c *Client) Close() error {
	if c.stdin != nil {
		_ = c.stdin.Close()
	}
	if w, ok := c.runner.(waiter); ok {
		_ = w.Wait()
	}
	return nil
}

func (c *Client) readLoop() {
	defer close(c.done)
	for c.scanner.Scan() {
		line := strings.TrimSpace(c.scanner.Text())
		if line == "" {
			continue
		}
		var m rpcMessage
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			continue
		}
		// Only responses (which carry an id) are deliverable; notifications
		// and server→client requests carry no id and are dropped.
		if m.ID == nil {
			continue
		}
		c.mu.Lock()
		ch := c.pends[*m.ID]
		c.mu.Unlock()
		if ch != nil {
			ch <- &m
		}
	}
}

// request sends a request and waits for its matching response. Request
// round-trips are serialized: many MCP servers are not safe for concurrent
// requests.
func (c *Client) request(ctx context.Context, method string, params map[string]any) (*rpcMessage, error) {
	c.reqMu.Lock()
	defer c.reqMu.Unlock()

	c.mu.Lock()
	c.id++
	id := c.id
	ch := make(chan *rpcMessage, 1)
	c.pends[id] = ch
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		delete(c.pends, id)
		c.mu.Unlock()
	}()

	data, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	})
	if err != nil {
		return nil, err
	}
	if _, err := c.stdin.Write(append(data, '\n')); err != nil {
		return nil, err
	}

	select {
	case m, ok := <-ch:
		if !ok {
			return nil, io.EOF
		}
		if m.Error != nil {
			return nil, fmt.Errorf("%s: %s", method, m.Error.Message)
		}
		return m, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// notify sends a JSON-RPC notification (no response expected).
func (c *Client) notify(method string, params map[string]any) error {
	data, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
	})
	if err != nil {
		return err
	}
	_, err = c.stdin.Write(append(data, '\n'))
	return err
}
