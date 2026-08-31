package store

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"protoprompt/internal/db"
)

func openTestDB(t *testing.T) (*sql.DB, error) {
	t.Helper()
	h, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err == nil {
		t.Cleanup(func() { h.Close() })
	}
	return h, err
}

func writeFileAbs(full, content string) error {
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	return os.WriteFile(full, []byte(content), 0o644)
}

func newTestRev(t *testing.T, s *Store) string {
	t.Helper()
	n++
	revID, err := s.SaveFile("p.md", "rev content "+itoaTest(n))
	if err != nil {
		t.Fatal(err)
	}
	if revID == nil {
		t.Fatal("expected new revision")
	}
	return *revID
}

var n int

func itoaTest(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}
