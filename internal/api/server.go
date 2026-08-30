// Package api wires HTTP routes onto the store, config, and LLM client.
package api

import (
	"database/sql"
	"encoding/json"
	"io/fs"
	"net/http"
	"strings"

	"chatvolution/internal/config"
	"chatvolution/internal/store"
)

type Server struct {
	cfg   config.Config
	db    *sql.DB
	store *store.Store
	web   fs.FS
}

func New(cfg config.Config, db *sql.DB, st *store.Store, web fs.FS) *Server {
	return &Server{cfg: cfg, db: db, store: st, web: web}
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
