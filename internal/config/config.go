// Package config loads protoprompt settings: defaults < protoprompt.json < env < CLI.
package config

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"time"
)

type Config struct {
	BaseURL      string         `json:"baseUrl"`
	APIKey       string         `json:"apiKey"`
	Models       []string       `json:"models"`
	DefaultModel string         `json:"defaultModel"`
	ReasoningEff string         `json:"reasoningEffort"`
	ExtraBody    map[string]any `json:"extraBody"`
	Timeout      time.Duration  `json:"-"`
	Port         int            `json:"port"`
	Host         string         `json:"host"`
	Dir          string         `json:"-"` // prompt workspace dir (from --dir)
}

type fileConfig struct {
	BaseURL               *string        `json:"baseUrl"`
	APIKey                *string        `json:"apiKey"`
	Models                []string       `json:"models"`
	DefaultModel          *string        `json:"defaultModel"`
	ReasoningEff          *string        `json:"reasoningEffort"`
	ExtraBody             map[string]any `json:"extraBody"`
	RequestTimeoutSeconds *int           `json:"requestTimeoutSeconds"`
	Port                  *int           `json:"port"`
	Host                  *string        `json:"host"`
}

// Load reads config from path (default "protoprompt.json" when empty). A
// missing file is not an error. Env overrides file; caller applies CLI flags.
func Load(path string) (*Config, error) {
	cfg := &Config{
		ExtraBody: map[string]any{},
		Timeout:   300 * time.Second,
		Port:      8420,
		Host:      "127.0.0.1",
	}
	if path == "" {
		path = "protoprompt.json"
	}
	if data, err := os.ReadFile(path); err == nil {
		var fc fileConfig
		if err := json.Unmarshal(data, &fc); err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
		if fc.BaseURL != nil {
			cfg.BaseURL = *fc.BaseURL
		}
		if fc.APIKey != nil {
			cfg.APIKey = *fc.APIKey
		}
		if len(fc.Models) > 0 {
			cfg.Models = fc.Models
		}
		if fc.DefaultModel != nil {
			cfg.DefaultModel = *fc.DefaultModel
		}
		if fc.ReasoningEff != nil {
			cfg.ReasoningEff = *fc.ReasoningEff
		}
		if fc.ExtraBody != nil {
			cfg.ExtraBody = fc.ExtraBody
		}
		if fc.RequestTimeoutSeconds != nil && *fc.RequestTimeoutSeconds > 0 {
			cfg.Timeout = time.Duration(*fc.RequestTimeoutSeconds) * time.Second
		}
		if fc.Port != nil && *fc.Port > 0 {
			cfg.Port = *fc.Port
		}
		if fc.Host != nil && *fc.Host != "" {
			cfg.Host = *fc.Host
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	// Env: PROTOPROMPT_* preferred, OPENAI_* fallback.
	if v := envFallback("PROTOPROMPT_BASE_URL", "OPENAI_BASE_URL"); v != "" {
		cfg.BaseURL = v
	}
	if v := envFallback("PROTOPROMPT_API_KEY", "OPENAI_API_KEY"); v != "" {
		cfg.APIKey = v
	}
	if v := os.Getenv("PROTOPROMPT_MODEL"); v != "" {
		cfg.DefaultModel = v
	}
	if len(cfg.Models) == 0 && cfg.DefaultModel != "" {
		cfg.Models = []string{cfg.DefaultModel}
	}
	if cfg.ExtraBody == nil {
		cfg.ExtraBody = map[string]any{}
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func envFallback(primary, fallback string) string {
	if v := os.Getenv(primary); v != "" {
		return v
	}
	return os.Getenv(fallback)
}

func (c *Config) validate() error {
	if c.BaseURL == "" {
		return nil // not configured; generate reports it
	}
	u, err := url.Parse(c.BaseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("baseUrl must parse with scheme http/https, got %q", c.BaseURL)
	}
	return nil
}
