package api

import "net/http"

// sessionRoutes and messageRoutes are implemented in later steps alongside the
// session/message store.
func (s *Server) sessionRoutes(w http.ResponseWriter, r *http.Request) {
	writeErr(w, http.StatusNotFound, "not found")
}

func (s *Server) messageRoutes(w http.ResponseWriter, r *http.Request) {
	writeErr(w, http.StatusNotFound, "not found")
}
