package store

import (
	"path/filepath"
	"strings"
	"testing"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	db, err := openTestDB(t)
	if err != nil {
		t.Fatal(err)
	}
	return New(dir, db)
}

func TestSaveCreatesFileAndRev(t *testing.T) {
	s := newTestStore(t)
	created, err := s.CreateFile("system-prompt.md")
	if err != nil {
		t.Fatalf("CreateFile: %v", err)
	}
	if !strings.HasSuffix(created.Path, "system-prompt.md") {
		t.Errorf("path = %q", created.Path)
	}
	revID, err := s.SaveFile("system-prompt.md", "v1 text")
	if err != nil {
		t.Fatalf("SaveFile: %v", err)
	}
	if revID == nil {
		t.Fatal("first save should create a revision")
	}

	// Identical re-save creates no new revision.
	revID2, err := s.SaveFile("system-prompt.md", "v1 text")
	if err != nil {
		t.Fatalf("SaveFile identical: %v", err)
	}
	if revID2 != nil {
		t.Fatalf("identical save should not insert a rev, got %v", *revID2)
	}

	// Different text inserts a new revision.
	revID3, err := s.SaveFile("system-prompt.md", "v2 text")
	if err != nil {
		t.Fatalf("SaveFile v2: %v", err)
	}
	if revID3 == nil || *revID3 == *revID {
		t.Fatalf("expected new rev id, got %v vs %v", revID, revID3)
	}

	revs, err := s.ListRevs("system-prompt.md", 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(revs) != 2 {
		t.Fatalf("expected 2 revs, got %d", len(revs))
	}
	if revs[0].Text != "v2 text" {
		t.Errorf("revs should be newest-first, got %q first", revs[0].Text)
	}

	files, err := s.ListFiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].RevCount != 2 {
		t.Errorf("ListFiles = %+v", files)
	}

	changed, err := s.DiskChanged("system-prompt.md")
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("disk should match latest rev right after save")
	}
}

func TestDotFileHidden(t *testing.T) {
	s := newTestStore(t)
	if err := writeFile(s.Dir, ".hidden.md", "x"); err != nil {
		t.Fatal(err)
	}
	files, err := s.ListFiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Errorf("dot files should be hidden, got %+v", files)
	}
}

func TestPathValidation(t *testing.T) {
	s := newTestStore(t)
	for _, bad := range []string{"../evil.md", "sub/../../evil.md", "notes.txt", "sub/x.txt", "/abs/path.md", ""} {
		if _, err := s.CreateFile(bad); err == nil {
			t.Errorf("expected error for %q", bad)
		}
	}
	if _, err := s.CreateFile("sub/dir/x.md"); err != nil {
		t.Errorf("nested path should work: %v", err)
	}
}

func TestValidateName(t *testing.T) {
	if got, err := ValidateName("a/b.md"); err != nil || got != "a/b.md" {
		t.Errorf("ValidateName(a/b.md) = %q, %v", got, err)
	}
	if got, err := ValidateName("./a.md"); err != nil || got != "a.md" {
		t.Errorf("ValidateName(./a.md) = %q, %v", got, err)
	}
	if got, err := ValidateName(`win\path.md`); err != nil || got != "win/path.md" {
		t.Errorf("ValidateName backslash = %q, %v", got, err)
	}
}

func writeFile(dir, name, content string) error {
	full := filepath.Join(dir, name)
	return writeFileAbs(full, content)
}
