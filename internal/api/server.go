// Package api wires HTTP routes onto the store, config, and LLM client.
package api

import (
	"database/sql"
	"encoding/json"
	"io/fs"
	"net/http"
	"strings"
	"time"

	"protoprompt/internal/config"
	"protoprompt/internal/llm"
	"protoprompt/internal/mcp"
	"protoprompt/internal/store"
)

type Server struct {
	cfg    config.Config
	db     *sql.DB
	store  *store.Store
	web    fs.FS
	llm    *llm.Client
	mcpMgr *mcp.Manager
}

func New(cfg config.Config, db *sql.DB, st *store.Store, web fs.FS) *Server {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 300 * time.Second
	}
	return &Server{
		cfg:    cfg,
		db:     db,
		store:  st,
		web:    web,
		llm:    llm.New(cfg.BaseURL, cfg.APIKey, timeout),
		mcpMgr: mcp.NewManager(convertServers(cfg.MCPServers)),
	}
}

// convertServers maps configured MCPServers onto mcp.Server specs for the
// tool manager.
func convertServers(servers map[string]config.MCPServer) map[string]mcp.Server {
	if servers == nil {
		return nil
	}
	out := make(map[string]mcp.Server, len(servers))
	for name, s := range servers {
		out[name] = mcp.Server{Command: s.Command, Args: s.Args, Env: s.Env}
	}
	return out
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/", s.dispatchAPI)
	if s.web != nil {
		mux.Handle("/", http.FileServerFS(s.web))
	}
	return mux
}

// dispatchAPI routes the whole /api tree in code: prompt-file paths embed
// slashes, which ServeMux patterns cannot express alongside suffixes like
// /save.
func (s *Server) dispatchAPI(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	switch {
	case p == "/api/health" && r.Method == http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	case p == "/api/config" && r.Method == http.MethodGet:
		s.handleConfig(w, r)
	case p == "/api/models" && r.Method == http.MethodGet:
		s.handleModels(w, r)
	case p == "/api/files" || strings.HasPrefix(p, "/api/files/") ||
		strings.HasPrefix(p, "/api/revs/"):
		s.fileRoutes(w, r)
	case p == "/api/sessions" || strings.HasPrefix(p, "/api/sessions/"):
		s.sessionRoutes(w, r)
	case strings.HasPrefix(p, "/api/messages/"):
		s.messageRoutes(w, r)
	default:
		writeErr(w, http.StatusNotFound, "not found")
	}
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"baseUrl":         s.cfg.BaseURL,
		"models":          s.cfg.Models,
		"defaultModel":    s.cfg.DefaultModel,
		"reasoningEffort": s.cfg.ReasoningEff,
		"hasKey":          s.cfg.APIKey != "",
		"dir":             s.cfg.Dir,
	})
}

// handleModels lists upstream model ids via the LLM client.
func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	if s.cfg.BaseURL == "" {
		writeErr(w, http.StatusNotImplemented, "no baseUrl configured")
		return
	}
	if s.cfg.APIKey == "" {
		// Same condition + wording as generate/resend.
		writeErr(w, http.StatusNotImplemented, "no API key configured")
		return
	}
	ids, err := s.llm.Models(r.Context())
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": ids})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
