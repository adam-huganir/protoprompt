package store

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"chatvolution/internal/db"
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
