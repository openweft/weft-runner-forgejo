// runner config persistence. Mirrors the github/gitlab siblings. The
// persisted token here is the runner UUID + secret returned by
// RunnerService.Register, NOT the one-shot registration token.

package runner

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// PersistedConfig is the on-disk shape of `weft-runner-forgejo register`.
type PersistedConfig struct {
	URL    string   `json:"url"`        // https://codeberg.org or self-hosted
	UUID   string   `json:"uuid"`       // runner identity
	Token  string   `json:"token"`      // long-lived runner token
	Name   string   `json:"name,omitempty"`
	Labels []string `json:"labels,omitempty"`
}

func writeConfig(path string, cfg PersistedConfig) error {
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func readConfig(path string) (PersistedConfig, error) {
	var cfg PersistedConfig
	b, err := os.ReadFile(path)
	if err != nil {
		return cfg, fmt.Errorf("read %s: %w (run `weft-runner-forgejo register` first)", path, err)
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return cfg, fmt.Errorf("decode %s: %w", path, err)
	}
	if v := os.Getenv("WEFT_RUNNER_FORGEJO_TOKEN"); v != "" {
		cfg.Token = v
	}
	cfg.URL = strings.TrimRight(cfg.URL, "/")
	if cfg.URL == "" {
		return cfg, fmt.Errorf("config %s missing url", path)
	}
	if cfg.UUID == "" || cfg.Token == "" {
		return cfg, fmt.Errorf("config %s missing uuid/token", path)
	}
	return cfg, nil
}
