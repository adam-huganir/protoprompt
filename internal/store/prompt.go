package store

import (
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// Rev is one saved revision of a prompt file.
type Rev struct {
	ID      string `json:"id"`
	File    string `json:"file"`
	Text    string `json:"text"`
	SavedAt int64  `json:"savedAt"`
}

// FileEntry describes one prompt file on disk plus revision stats.
type FileEntry struct {
	Name          string `json:"name"`
	Path          string `json:"path"`
	RevCount      int    `json:"revCount"`
	LatestSavedAt int64  `json:"latestSavedAt"`
}

var (
	ErrName      = errors.New("path must end in .md or .markdown")
	ErrExists    = errors.New("file already exists")
	ErrNotFound  = errors.New("not found")
	ErrRevStream = errors.New("preview already running")
)

// ValidateName cleans a user-supplied workspace-relative path: rejects
// absolute paths, ".." segments, and non-.md/.markdown names.
func ValidateName(name string) (string, error) {
	if name == "" {
		return "", ErrName
	}
	if strings.HasPrefix(name, "/") || filepath.IsAbs(name) {
		return "", fmt.Errorf("absolute paths not allowed")
	}
	clean := path.Clean(strings.ReplaceAll(name, "\\", "/"))
	if clean == ".." || strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, "/") {
		return "", fmt.Errorf("path escapes workspace")
	}
	if !strings.HasSuffix(clean, ".md") && !strings.HasSuffix(clean, ".markdown") {
		return "", ErrName
	}
	for _, seg := range strings.Split(clean, "/") {
		if seg == ".." || seg == "" {
			return "", fmt.Errorf("invalid path %q", name)
		}
	}
	return clean, nil
}

// ListFiles recursively scans the workspace for .md/.markdown files, skipping
// dot-directories, and joins revision stats.
func (s *Store) ListFiles() ([]FileEntry, error) {
	stats := map[string]struct {
		count  int
		latest int64
	}{}
	rows, err := s.DB.Query(`SELECT file, COUNT(*), MAX(saved_at) FROM prompt_rev GROUP BY file`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var f string
		var c int
		var l int64
		if err := rows.Scan(&f, &c, &l); err != nil {
			return nil, err
		}
		stats[f] = struct {
			count  int
			latest int64
		}{c, l}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := []FileEntry{}
	base := s.Dir
	err = filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			if p != base && strings.HasPrefix(d.Name(), ".") {
				return fs.SkipDir
			}
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".md") && !strings.HasSuffix(name, ".markdown") {
			return nil
		}
		rel, err := filepath.Rel(base, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(name, ".") {
			return nil
		}
		e := FileEntry{Name: rel, Path: p}
		if st, ok := stats[rel]; ok {
			e.RevCount = st.count
			e.LatestSavedAt = st.latest
		}
		out = append(out, e)
		return nil
	})
	if err != nil {
		return nil, err
	}
	// Deterministic order.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].Name < out[j-1].Name; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out, nil
}

// CreateFile makes an empty file (and parent dirs); 409-style error if exists.
func (s *Store) CreateFile(name string) (*FileEntry, error) {
	clean, err := ValidateName(name)
	if err != nil {
		return nil, err
	}
	full := filepath.Join(s.Dir, filepath.FromSlash(clean))
	if _, err := os.Stat(full); err == nil {
		return nil, ErrExists
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(full, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return nil, ErrExists
		}
		return nil, err
	}
	f.Close()
	return &FileEntry{Name: clean, Path: full}, nil
}

// ReadFile returns current disk content of a workspace file.
func (s *Store) ReadFile(name string) (string, error) {
	clean, err := ValidateName(name)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(filepath.Join(s.Dir, filepath.FromSlash(clean)))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", ErrNotFound
		}
		return "", err
	}
	return string(data), nil
}

// SaveFile atomically writes text to disk and inserts a prompt_rev row only
// when text differs from the file's latest revision. nil revID = unchanged.
func (s *Store) SaveFile(name, text string) (*string, error) {
	clean, err := ValidateName(name)
	if err != nil {
		return nil, err
	}
	full := filepath.Join(s.Dir, filepath.FromSlash(clean))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return nil, err
	}

	latest, err := s.LatestRev(clean)
	if err != nil {
		return nil, err
	}

	// Atomic write: temp file in same dir, then rename over target.
	tmp := full + ".tmp-" + NewID()
	if err := os.WriteFile(tmp, []byte(text), 0o644); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, full); err != nil {
		os.Remove(tmp)
		return nil, err
	}

	if latest != nil && latest.Text == text {
		return nil, nil // unchanged
	}
	rev := Rev{ID: NewID(), File: clean, Text: text, SavedAt: nowMS()}
	if _, err := s.DB.Exec(
		`INSERT INTO prompt_rev (id, file, text, saved_at) VALUES (?, ?, ?, ?)`,
		rev.ID, rev.File, rev.Text, rev.SavedAt,
	); err != nil {
		return nil, err
	}
	return &rev.ID, nil
}

// LatestRev returns the newest revision for file, or nil when none exists.
func (s *Store) LatestRev(file string) (*Rev, error) {
	var r Rev
	err := s.DB.QueryRow(
		`SELECT id, file, text, saved_at FROM prompt_rev WHERE file = ? ORDER BY saved_at DESC, id LIMIT 1`,
		file,
	).Scan(&r.ID, &r.File, &r.Text, &r.SavedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// ListRevs returns revisions newest-first, limited.
func (s *Store) ListRevs(file string, limit int) ([]Rev, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := s.DB.Query(
		`SELECT id, file, text, saved_at FROM prompt_rev WHERE file = ? ORDER BY saved_at DESC, id DESC LIMIT ?`,
		file, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Rev{}
	for rows.Next() {
		var r Rev
		if err := rows.Scan(&r.ID, &r.File, &r.Text, &r.SavedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetRev fetches one revision by id.
func (s *Store) GetRev(id string) (*Rev, error) {
	var r Rev
	err := s.DB.QueryRow(
		`SELECT id, file, text, saved_at FROM prompt_rev WHERE id = ?`, id,
	).Scan(&r.ID, &r.File, &r.Text, &r.SavedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// DiskChanged reports whether a file's current disk content differs from its
// latest revision text (false when no revisions exist yet).
func (s *Store) DiskChanged(file string) (bool, error) {
	latest, err := s.LatestRev(file)
	if err != nil || latest == nil {
		return false, err
	}
	disk, err := s.ReadFile(file)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return true, nil
		}
		return false, err
	}
	return disk != latest.Text, nil
}

func nowMS() int64 {
	return time.Now().UnixMilli()
}
