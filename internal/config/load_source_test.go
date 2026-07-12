package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

const minimalSourceConfig = `
models:
  - alias: local-tools-off
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    upstream_model: llama
    known_auth: ollama
    capabilities:
      tools: false
`

func TestLoadRecordsSourcePathAndModTime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(minimalSourceConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().Add(-42 * time.Minute).Truncate(time.Second)
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	wantPath, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SourcePath != wantPath {
		t.Fatalf("SourcePath = %q, want %q", cfg.SourcePath, wantPath)
	}
	if !cfg.SourceModTime.Equal(stamp) {
		t.Fatalf("SourceModTime = %v, want %v", cfg.SourceModTime, stamp)
	}
}

func TestParseLeavesSourceFieldsZero(t *testing.T) {
	cfg, err := parse([]byte(minimalSourceConfig))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SourcePath != "" || !cfg.SourceModTime.IsZero() {
		t.Fatalf("parse must not set source identity, got path=%q mtime=%v", cfg.SourcePath, cfg.SourceModTime)
	}
}
