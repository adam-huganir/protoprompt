package mcp

import (
	"context"
	"strings"
	"testing"
)

// clientForFake wraps a fakeRunner in a Client via the manager's factory seam,
// so manager tests never spawn subprocesses.
func clientForFake(command string, args []string, env []string) (*Client, error) {
	r := &fakeRunner{respond: func(method string, params map[string]any, id int64) map[string]any {
		switch method {
		case "initialize":
			return map[string]any{"result": map[string]any{"serverInfo": map[string]any{"name": command}}}
		case "tools/list":
			names := []string{"echo"}
			if command == "fake-beta" {
				names = []string{"echo", "ping"}
			}
			tools := []map[string]any{}
			for _, n := range names {
				tools = append(tools, map[string]any{
					"name":        n,
					"description": "tool of " + command,
					"inputSchema": map[string]any{"type": "object"},
				})
			}
			return map[string]any{"result": map[string]any{"tools": tools}}
		case "tools/call":
			return map[string]any{"result": map[string]any{"content": []map[string]any{{"type": "text", "text": command}}}}
		}
		return map[string]any{"error": map[string]any{"message": "unknown method"}}
	}}
	return newClient(r)
}

// TestManagerTwoServersKeyPrefixingAndRouting: tools from two servers are
// prefixed by server name (disambiguating colliding tool names) and CallTool
// routes each prefixed key back to its owning server with the original name.
func TestManagerTwoServersKeyPrefixingAndRouting(t *testing.T) {
	m := NewManager(map[string]Server{
		"alpha": {Command: "fake-alpha"},
		"beta":  {Command: "fake-beta"},
	})
	m.newClient = clientForFake
	ctx := context.Background()

	tools, err := m.Tools(ctx)
	if err != nil {
		t.Fatalf("Tools: %v", err)
	}
	keys := []string{}
	for _, tl := range tools {
		keys = append(keys, tl.Name)
	}
	want := []string{"alpha_echo", "beta_echo", "beta_ping"}
	if len(keys) != 3 {
		t.Fatalf("keys = %v want %v", keys, want)
	}
	if keys[0] != want[0] || keys[1] != want[1] || keys[2] != want[2] {
		t.Errorf("keys = %v want %v", keys, want)
	}

	cr, err := m.CallTool(ctx, "alpha_echo", map[string]any{})
	if err != nil {
		t.Fatalf("CallTool alpha_echo: %v", err)
	}
	if cr.Content != "fake-alpha" {
		t.Errorf("alpha content = %q", cr.Content)
	}
	cr, err = m.CallTool(ctx, "beta_echo", map[string]any{})
	if err != nil {
		t.Fatalf("CallTool beta_echo: %v", err)
	}
	if cr.Content != "fake-beta" {
		t.Errorf("beta content = %q", cr.Content)
	}

	if _, err := m.CallTool(ctx, "nope_echo", map[string]any{}); err == nil || !strings.Contains(err.Error(), "unknown tool") {
		t.Errorf("unknown key err = %v, want unknown tool", err)
	}

	// Tools is cached: second call returns the same set without re-initializing.
	tools2, err := m.Tools(ctx)
	if err != nil || len(tools2) != 3 {
		t.Fatalf("cached Tools: %v %d", err, len(tools2))
	}
}

// TestManagerFailureIsLoud: a server that fails to initialize/list fails the
// whole Tools call instead of being silently skipped.
func TestManagerFailureIsLoud(t *testing.T) {
	m2 := NewManager(map[string]Server{"bad": {Command: "fake-bad"}})
	m2.newClient = func(command string, args []string, env []string) (*Client, error) {
		r := &fakeRunner{respond: func(method string, params map[string]any, id int64) map[string]any {
			if method == "initialize" {
				return map[string]any{"result": map[string]any{"serverInfo": map[string]any{"name": command}}}
			}
			return map[string]any{"error": map[string]any{"message": "no such method"}}
		}}
		return newClient(r)
	}
	if _, err := m2.Tools(context.Background()); err == nil || !strings.Contains(err.Error(), `mcp server "bad"`) {
		t.Fatalf("expected loud server failure, got %v", err)
	}
	if _, err := m2.Tools(context.Background()); err == nil || !strings.Contains(err.Error(), `mcp server "bad"`) {
		t.Fatalf("failure must be remembered, got %v", err)
	}
}

// TestManagerCloseIsIdempotent guards the test-only teardown path.
func TestManagerCloseIsIdempotent(t *testing.T) {
	m := NewManager(nil)
	m.newClient = clientForFake
	if _, err := m.Tools(context.Background()); err != nil {
		t.Fatalf("Tools: %v", err)
	}
	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := m.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}
