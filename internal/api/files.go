package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"chatvolution/internal/diffx"
	"chatvolution/internal/store"
)

type revBrief struct {
	ID      string `json:"id"`
	SavedAt int64  `json:"savedAt"`
}

func (s *Server) listFiles(w http.ResponseWriter, r *http.Request) {
	files, err := s.store.ListFiles()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, files)
}

func (s *Server) createFile(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path string `json:"path"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	f, err := s.store.CreateFile(body.Path)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, f)
}

func (s *Server) getFile(w http.ResponseWriter, r *http.Request, name string) {
	text, err := s.store.ReadFile(name)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	revs, err := s.store.ListRevs(name, 200)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	briefs := make([]revBrief, 0, len(revs))
	for _, rv := range revs {
		briefs = append(briefs, revBrief{ID: rv.ID, SavedAt: rv.SavedAt})
	}
	var latestID *string
	if len(briefs) > 0 {
		latestID = &briefs[0].ID
	}
	changed, err := s.store.DiskChanged(name)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"path":                      name,
		"text":                      text,
		"latestRevId":               latestID,
		"revs":                      briefs,
		"diskChangedSinceLatestRev": changed,
	})
}

func (s *Server) saveFile(w http.ResponseWriter, r *http.Request, name string) {
	var body struct {
		Text string `json:"text"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	revID, err := s.store.SaveFile(name, body.Text)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"revId": revID})
}

func (s *Server) diffFile(w http.ResponseWriter, r *http.Request, name string) {
	from := r.URL.Query().Get("from")
	to := r.URL.Query().Get("to")
	if from == "" {
		writeErr(w, http.StatusBadRequest, "from revision required")
		return
	}
	fromRev, err := s.store.GetRev(from)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	if fromRev.File != name {
		writeErr(w, http.StatusBadRequest, "from revision belongs to a different file")
		return
	}
	var toText string
	if to != "" {
		toRev, err := s.store.GetRev(to)
		if err != nil {
			writeStoreErr(w, err)
			return
		}
		if toRev.File != name {
			writeErr(w, http.StatusBadRequest, "to revision belongs to a different file")
			return
		}
		toText = toRev.Text
	} else {
		latest, err := s.store.LatestRev(name)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if latest == nil {
			writeErr(w, http.StatusNotFound, "no revisions for file")
			return
		}
		toText = latest.Text
	}
	hunks := diffx.Diff(fromRev.Text, toText)
	writeJSON(w, http.StatusOK, map[string]any{"hunks": hunks})
}

func (s *Server) getRev(w http.ResponseWriter, r *http.Request, id string) {
	rev, err := s.store.GetRev(id)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rev)
}

// fileRoutes handles /api/files and /api/files/{name...} including the
// /save and /diff suffixes (ServeMux cannot match mid-path wildcards).
func (s *Server) fileRoutes(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	if p == "/api/files" {
		switch r.Method {
		case http.MethodGet:
			s.listFiles(w, r)
		case http.MethodPost:
			s.createFile(w, r)
		default:
			writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		}
		return
	}
	if p == "/api/revs" || strings.HasPrefix(p, "/api/revs/") {
		if r.Method != http.MethodGet {
			writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		s.getRev(w, r, strings.TrimPrefix(p, "/api/revs/"))
		return
	}
	rest := strings.TrimPrefix(p, "/api/files/")
	if rest == p {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	rest = strings.Trim(rest, "/")
	switch r.Method {
	case http.MethodPost:
		if strings.HasSuffix(rest, "/save") {
			name := strings.TrimSuffix(rest, "/save")
			s.saveFile(w, r, name)
			return
		}
		writeErr(w, http.StatusNotFound, "not found")
	case http.MethodGet:
		if strings.HasSuffix(rest, "/diff") {
			name := strings.TrimSuffix(rest, "/diff")
			s.diffFile(w, r, name)
			return
		}
		s.getFile(w, r, rest)
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func decodeJSON(r *http.Request, v any) error {
	defer r.Body.Close()
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<24)).Decode(v); err != nil {
		return errors.New("invalid JSON body")
	}
	return nil
}

func writeStoreErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrExists):
		writeErr(w, http.StatusConflict, err.Error())
	case errors.Is(err, store.ErrNotFound):
		writeErr(w, http.StatusNotFound, err.Error())
	case errors.Is(err, store.ErrName):
		writeErr(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, store.ErrAccept):
		writeErr(w, http.StatusConflict, err.Error())
	default:
		writeErr(w, http.StatusInternalServerError, err.Error())
	}
}
