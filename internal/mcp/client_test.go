package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
)

// fakeRunner is an in-memory Runner serving canned JSON-RPC responses over
// io.Pipe. respond returns the response object for a request (id is echoed
// back); notifications get no response.
type fakeRunner struct {
	mu       sync.Mutex
	requests []string
	respond  func(method string, params map[string]any, id int64) map[string]any
}

func (f *fakeRunner) Start() (io.WriteCloser, io.Reader, io.Reader, error) {
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	go func() {
		defer stdoutW.Close()
		sc := bufio.NewScanner(stdinR)
		for sc.Scan() {
			line := sc.Text()
			var m struct {
				ID     *int64          `json:"id"`
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
			}
			_ = json.Unmarshal([]byte(line), &m)
			f.mu.Lock()
			f.requests = append(f.requests, line)
			f.mu.Unlock()
			if m.ID == nil {
				continue // notification: no response
			}
			params := map[string]any{}
			_ = json.Unmarshal(m.Params, &params)
			body := f.respond(m.Method, params, *m.ID)
			if body == nil {
				continue
			}
			body["jsonrpc"] = "2.0"
			body["id"] = *m.ID
			b, _ := json.Marshal(body)
			fmt.Fprintf(stdoutW, "%s\n", b)
		}
	}()
	return stdinW, stdoutR, strings.NewReader(""), nil
}

func (f *fakeRunner) requestsCopy() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.requests...)
}

func (f *fakeRunner) Wait() error { return nil }

func fakeEchoServer() *fakeRunner {
	return &fakeRunner{respond: func(method string, params map[string]any, id int64) map[string]any {
		switch method {
		case "initialize":
			return map[string]any{"result": map[string]any{
				"protocolVersion": "2024-11-05",
				"capabilities":    map[string]any{},
				"serverInfo":      map[string]any{"name": "fake"},
			}}
		case "tools/list":
			return map[string]any{"result": map[string]any{"tools": []map[string]any{{
				"name":        "echo",
				"description": "echoes text",
				"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}},
			}}}}
		case "tools/call":
			args, _ := params["arguments"].(map[string]any)
			return map[string]any{"result": map[string]any{"content": []map[string]any{
				{"type": "text", "text": "echo: " + args["text"].(string)},
				{"type": "image", "data": "aGk=", "mimeType": "image/png"},
			}}}
		}
		return map[string]any{"error": map[string]any{"code": -32601, "message": "unknown method"}}
	}}
}

// TestInitializeListToolsCall drives the full handshake: initialize + the
// notifications/initialized notification, schema mapping from tools/list, and
// a tools/call that concatenates only text content.
func TestInitializeListToolsCall(t *testing.T) {
	r := fakeEchoServer()
	c, err := newClient(r)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer c.Close()

	tools, err := c.ListTools(ctx)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "echo" || tools[0].Description != "echoes text" {
		t.Fatalf("tools = %+v", tools)
	}
	schema, ok := tools[0].InputSchema["properties"].(map[string]any)
	if !ok || schema["text"].(map[string]any)["type"] != "string" {
		t.Errorf("InputSchema not mapped: %v", tools[0].InputSchema)
	}

	cr, err := c.CallTool(ctx, "echo", map[string]any{"text": "hi"})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if cr.Content != "echo: hi" {
		t.Errorf("Content = %q", cr.Content)
	}
	if cr.IsError {
		t.Error("IsError should be false")
	}

	reqs := r.requestsCopy()
	if len(reqs) != 4 {
		t.Fatalf("requests = %d, want 4 (init + notification + list + call)", len(reqs))
	}
	var init struct {
		Method string `json:"method"`
		Params struct {
			ProtocolVersion string `json:"protocolVersion"`
			ClientInfo      struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			} `json:"clientInfo"`
		} `json:"params"`
	}
	if err := json.Unmarshal([]byte(reqs[0]), &init); err != nil {
		t.Fatal(err)
	}
	if init.Method != "initialize" || init.Params.ProtocolVersion != "2024-11-05" || init.Params.ClientInfo.Name != "protoprompt" {
		t.Errorf("init malformed: %s", reqs[0])
	}
	var notify struct {
		Method string `json:"method"`
	}
	if err := json.Unmarshal([]byte(reqs[1]), &notify); err != nil || notify.Method != "notifications/initialized" {
		t.Errorf("expected notifications/initialized, got %s", reqs[1])
	}
}

// TestCallToolIsError propagates isError:true from the result.
func TestCallToolIsError(t *testing.T) {
	r := &fakeRunner{respond: func(method string, params map[string]any, id int64) map[string]any {
		if method == "initialize" {
			return map[string]any{"result": map[string]any{"serverInfo": map[string]any{"name": "fake"}}}
		}
		if method == "tools/call" {
			return map[string]any{"result": map[string]any{
				"content": []map[string]any{{"type": "text", "text": "boom"}},
				"isError": true,
			}}
		}
		return nil
	}}
	c, err := newClient(r)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := c.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	cr, err := c.CallTool(ctx, "boom", map[string]any{})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !cr.IsError || cr.Content != "boom" {
		t.Errorf("CallResult = %+v, want IsError with content boom", cr)
	}
}

// TestCallToolRPCError surfaces protocol-level errors.
func TestCallToolRPCError(t *testing.T) {
	r := &fakeRunner{respond: func(method string, params map[string]any, id int64) map[string]any {
		if method == "initialize" {
			return map[string]any{"result": map[string]any{"serverInfo": map[string]any{"name": "fake"}}}
		}
		if method == "tools/call" {
			return map[string]any{"error": map[string]any{"code": -32602, "message": "invalid params"}}
		}
		return nil
	}}
	c, err := newClient(r)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := c.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.CallTool(ctx, "nope", map[string]any{}); err == nil || !strings.Contains(err.Error(), "invalid params") {
		t.Errorf("err = %v, want invalid params", err)
	}
}
