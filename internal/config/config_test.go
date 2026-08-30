package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeFile(t *testing.T, path, content string) string {
	t.Helper()
	full := filepath.Join(t.TempDir(), path)
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return full
}

func TestMissingFileDefaults(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Port != 8420 || cfg.Host != "127.0.0.1" {
		t.Errorf("port/host = %d/%q", cfg.Port, cfg.Host)
	}
	if cfg.Timeout != 300*time.Second {
		t.Errorf("timeout = %v", cfg.Timeout)
	}
	if cfg.BaseURL != "" || cfg.APIKey != "" {
		t.Errorf("expected empty base/key, got %q %q", cfg.BaseURL, cfg.APIKey)
	}
}

func TestEnvBeatsFile(t *testing.T) {
	path := writeFile(t, "c.json", `{"baseUrl":"https://file.example/v1","apiKey":"filekey"}`)
	t.Setenv("CHATVOLUTION_BASE_URL", "https://env.example/v1")
	t.Setenv("CHATVOLUTION_API_KEY", "envkey")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.BaseURL != "https://env.example/v1" {
		t.Errorf("BaseURL = %q", cfg.BaseURL)
	}
	if cfg.APIKey != "envkey" {
		t.Errorf("APIKey = %q", cfg.APIKey)
	}
}

func TestChatvolutionBeatsOpenAI(t *testing.T) {
	t.Setenv("OPENAI_BASE_URL", "https://openai.example/v1")
	t.Setenv("OPENAI_API_KEY", "openaikey")
	t.Setenv("CHATVOLUTION_API_KEY", "cvkey")
	cfg, err := Load(filepath.Join(t.TempDir(), "none.json"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.BaseURL != "https://openai.example/v1" {
		t.Errorf("BaseURL fallback to OPENAI_* = %q", cfg.BaseURL)
	}
	if cfg.APIKey != "cvkey" {
		t.Errorf("CHATVOLUTION_API_KEY should beat OPENAI_API_KEY, got %q", cfg.APIKey)
	}
}

func TestModelEnvPopulatesModels(t *testing.T) {
	t.Setenv("CHATVOLUTION_MODEL", "qwen3vl-2b")
	cfg, err := Load(filepath.Join(t.TempDir(), "none.json"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DefaultModel != "qwen3vl-2b" {
		t.Errorf("DefaultModel = %q", cfg.DefaultModel)
	}
	if len(cfg.Models) != 1 || cfg.Models[0] != "qwen3vl-2b" {
		t.Errorf("Models = %v", cfg.Models)
	}
}

func TestInvalidBaseURLErrors(t *testing.T) {
	path := writeFile(t, "c.json", `{"baseUrl":"ftp://nope"}`)
	if _, err := Load(path); err == nil {
		t.Fatal("expected error for ftp scheme")
	}
	path = writeFile(t, "c2.json", `{"baseUrl":"not a url"}`)
	if _, err := Load(path); err == nil {
		t.Fatal("expected error for unparseable base url")
	}
}
