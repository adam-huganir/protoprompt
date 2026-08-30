package api

import (
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"chatvolution/internal/config"
	"chatvolution/internal/db"
	"chatvolution/internal/store"
)

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	h, err := db.Open(filepath.Join(t.TempDir(), "api.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close() })
	cfg := config.Config{Port: 8420, Host: "127.0.0.1", Dir: t.TempDir(), ExtraBody: map[string]any{}}
	st := store.New(cfg.Dir, h)
	srv := httptest.NewServer(New(cfg, h, st, nil).Handler())
	t.Cleanup(srv.Close)
	return srv
}

func do(t *testing.T, srv *httptest.Server, method, path string, body any) (int, []byte) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = strings.NewReader(string(b))
	}
	req, err := http.NewRequest(method, srv.URL+path, rdr)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, data
}

func TestFilesRoundTrip(t *testing.T) {
	srv := newTestServer(t)

	code, body := do(t, srv, "POST", "/api/files", map[string]string{"path": "system-prompt.md"})
	if code != http.StatusCreated {
		t.Fatalf("create file: %d %s", code, body)
	}

	// Duplicate → 409.
	if code, _ := do(t, srv, "POST", "/api/files", map[string]string{"path": "system-prompt.md"}); code != http.StatusConflict {
		t.Fatalf("duplicate create: got %d", code)
	}

	// Bad suffix → 400.
	if code, _ := do(t, srv, "POST", "/api/files", map[string]string{"path": "notes.txt"}); code != http.StatusBadRequest {
		t.Fatalf("bad suffix: got %d", code)
	}

	// Save v1.
	code, body = do(t, srv, "POST", "/api/files/system-prompt.md/save", map[string]string{"text": "hello world"})
	if code != http.StatusOK {
		t.Fatalf("save: %d %s", code, body)
	}
	var saved map[string]any
	if err := json.Unmarshal(body, &saved); err != nil {
		t.Fatal(err)
	}
	rev1, _ := saved["revId"].(string)
	if rev1 == "" {
		t.Fatalf("expected revId, got %s", body)
	}

	// Identical save → null revId.
	if _, body = do(t, srv, "POST", "/api/files/system-prompt.md/save", map[string]string{"text": "hello world"}); !strings.Contains(string(body), `"revId":null`) {
		t.Fatalf("identical save should return null revId: %s", body)
	}

	// Save v2.
	_, body = do(t, srv, "POST", "/api/files/system-prompt.md/save", map[string]string{"text": "hello big world"})
	if err := json.Unmarshal(body, &saved); err != nil {
		t.Fatal(err)
	}
	rev2, _ := saved["revId"].(string)
	if rev2 == "" || rev2 == rev1 {
		t.Fatalf("expected new rev, got %s", body)
	}

	// Get file: text, revs newest-first, latest id.
	code, body = do(t, srv, "GET", "/api/files/system-prompt.md", nil)
	if code != http.StatusOK {
		t.Fatalf("get file: %d %s", code, body)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got["text"] != "hello big world" {
		t.Errorf("text = %v", got["text"])
	}
	if got["latestRevId"] != rev2 {
		t.Errorf("latestRevId = %v want %v", got["latestRevId"], rev2)
	}
	revs := got["revs"].([]any)
	if len(revs) != 2 {
		t.Fatalf("revs = %v", got["revs"])
	}
	if revs[0].(map[string]any)["id"] != rev2 {
		t.Error("revs should be newest-first")
	}
	if got["diskChangedSinceLatestRev"] != false {
		t.Error("disk should not be changed right after save")
	}

	// Diff from rev1 → latest: one hunk with del/add.
	code, body = do(t, srv, "GET", "/api/files/system-prompt.md/diff?from="+rev1, nil)
	if code != http.StatusOK {
		t.Fatalf("diff: %d %s", code, body)
	}
	if !strings.Contains(string(body), `"t":"add"`) {
		t.Errorf("diff should contain add line: %s", body)
	}

	// Rev fetch.
	code, body = do(t, srv, "GET", "/api/revs/"+rev1, nil)
	if code != http.StatusOK || !strings.Contains(string(body), `"file":"system-prompt.md"`) {
		t.Errorf("get rev: %d %s", code, body)
	}

	// Subdirectory path with slashes.
	if code, _ = do(t, srv, "POST", "/api/files", map[string]string{"path": "sub/deep.md"}); code != http.StatusCreated {
		t.Fatalf("create nested: %d", code)
	}
	if code, body = do(t, srv, "POST", "/api/files/sub/deep.md/save", map[string]string{"text": "nested"}); code != http.StatusOK {
		t.Fatalf("save nested: %d %s", code, body)
	}
	if code, body = do(t, srv, "GET", "/api/files/sub/deep.md", nil); code != http.StatusOK {
		t.Fatalf("get nested: %d", code)
	}

	// Files list.
	_, body = do(t, srv, "GET", "/api/files", nil)
	var files []map[string]any
	if err := json.Unmarshal(body, &files); err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("expected 2 files, got %s", body)
	}
}

var _ = sql.ErrNoRows
