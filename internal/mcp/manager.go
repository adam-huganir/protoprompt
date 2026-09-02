package mcp

import (
	"context"
	"fmt"
	"sort"
	"sync"
)

// Server describes one configured stdio MCP server.
type Server struct {
	Command string
	Args    []string
	Env     map[string]string
}

// toolRoute remembers which client owns a key and the server's original tool
// name (the llm-facing name is prefixed, so the route must strip it back).
type toolRoute struct {
	client *Client
	name   string
}

// Manager aggregates configured servers and maps tools to OpenAI-style specs.
// Initialization is lazy and cached under a mutex: the first Tools call starts
// and lists every server once. A failed server fails the whole call (it is not
// silently skipped).
type Manager struct {
	servers   map[string]Server
	newClient func(command string, args []string, env []string) (*Client, error)

	mu      sync.Mutex
	done    bool
	initErr error
	tools   []Tool
	byKey   map[string]*toolRoute
	clients []*Client
}

// NewManager returns a Manager for the given configured servers.
func NewManager(servers map[string]Server) *Manager {
	return &Manager{
		servers:   servers,
		newClient: NewStdioClient,
		byKey:     map[string]*toolRoute{},
	}
}

// Tools starts and lists every configured server (once), returning all tools
// with keys prefixed "<server>_<tool>" to disambiguate cross-server name
// collisions. It reuses the cached list on later calls.
func (m *Manager) Tools(ctx context.Context) ([]Tool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.initErr != nil {
		return nil, m.initErr
	}
	if m.done {
		out := make([]Tool, len(m.tools))
		copy(out, m.tools)
		return out, nil
	}

	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	// Iterate servers in a stable order so the tool list (and thus the
	// llm-facing key ordering) is deterministic across runs.
	names := make([]string, 0, len(m.servers))
	for name := range m.servers {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		srv := m.servers[name]
		cl, err := m.newClient(srv.Command, srv.Args, envPairs(srv.Env))
		if err != nil {
			m.initErr = fmt.Errorf("mcp server %q: %w", name, err)
			m.closeStarted()
			return nil, m.initErr
		}
		if err := cl.Start(ctx); err != nil {
			m.initErr = fmt.Errorf("mcp server %q: %w", name, err)
			m.closeStarted()
			return nil, m.initErr
		}
		tools, err := cl.ListTools(ctx)
		if err != nil {
			m.initErr = fmt.Errorf("mcp server %q: %w", name, err)
			m.closeStarted()
			return nil, m.initErr
		}
		m.clients = append(m.clients, cl)
		for _, t := range tools {
			key := name + "_" + t.Name
			t.Name = key // llm-facing name is the prefixed key
			m.tools = append(m.tools, t)
			m.byKey[key] = &toolRoute{client: cl, name: t.Name[len(name)+1:]}
		}
	}
	m.done = true
	out := make([]Tool, len(m.tools))
	copy(out, m.tools)
	return out, nil
}

// CallTool routes by the prefixed key to its owning client, calling the
// server's original tool name. An unknown key is an error.
func (m *Manager) CallTool(ctx context.Context, key string, args map[string]any) (CallResult, error) {
	m.mu.Lock()
	route := m.byKey[key]
	m.mu.Unlock()
	if route == nil {
		return CallResult{}, fmt.Errorf("unknown tool %q", key)
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	return route.client.CallTool(ctx, route.name, args)
}

// Close closes every started client. Not wired into server teardown — the
// process lives for the app's lifetime — but implemented for tests.
func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, cl := range m.clients {
		_ = cl.Close()
	}
	return nil
}

func (m *Manager) closeStarted() {
	for _, cl := range m.clients {
		_ = cl.Close()
	}
	m.clients = nil
}

func envPairs(env map[string]string) []string {
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}
