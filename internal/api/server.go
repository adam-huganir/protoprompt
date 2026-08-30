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
	mux.HandleFunc("GET /api/health", s.handleHealth)
	mux.HandleFunc("GET /api/config", s.handleConfig)

	if s.web != nil {
		mux.Handle("/", http.FileServerFS(s.web))
	}
	return mux
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
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

// pathParam extracts the {name...} segment(s) from a matched request.
func pathParam(r *http.Request, name string) string {
	return strings.TrimPrefix(r.PathValue(name), "/")
}
