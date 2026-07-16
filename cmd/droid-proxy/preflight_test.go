package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trevoraspencer/droid-proxy/internal/config"
)

func parseTestConfig(t *testing.T, yamlBody string) *config.Config {
	t.Helper()
	cfg, err := config.ParseForTest(yamlBody)
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	return cfg
}

const validModelYAML = "models:\n  - alias: my-model\n    factory_provider: generic-chat-completion-api\n    upstream_protocol: openai-chat\n    base_url: https://api.deepseek.com/v1\n    upstream_model: m\n    api_key_env: TEST_KEY\n"

func init() {
	// Ensure TEST_KEY is non-empty for config validation.
	_ = os.Setenv("TEST_KEY", "test-value")
}

func TestOmittedPortPreflightAllowsAbsentFactory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cfg := parseTestConfig(t, "listen:\n  host: 127.0.0.1\n"+validModelYAML)

	if err := omittedPortPreflight(cfg); err != nil {
		t.Fatalf("expected no error for absent factory, got: %v", err)
	}
}

func TestOmittedPortPreflightAllowsUnrelatedEntries(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	factoryDir := filepath.Join(home, ".factory")
	os.MkdirAll(factoryDir, 0o700)
	settings := filepath.Join(factoryDir, "settings.json")
	os.WriteFile(settings, []byte(`{"customModels":[{"model":"other","provider":"openai","baseUrl":"https://api.openai.com/v1"}]}`), 0o600)

	cfg := parseTestConfig(t, "listen:\n  host: 127.0.0.1\n"+validModelYAML)

	if err := omittedPortPreflight(cfg); err != nil {
		t.Fatalf("expected no error for unrelated entries, got: %v", err)
	}
}

func TestOmittedPortPreflightRefusesOldOrigin(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	factoryDir := filepath.Join(home, ".factory")
	os.MkdirAll(factoryDir, 0o700)
	settings := filepath.Join(factoryDir, "settings.json")
	os.WriteFile(settings, []byte(`{"customModels":[{"model":"my-model","provider":"generic-chat-completion-api","baseUrl":"http://127.0.0.1:8787"}]}`), 0o600)

	cfg := parseTestConfig(t, "listen:\n  host: 127.0.0.1\n"+validModelYAML)

	err := omittedPortPreflight(cfg)
	if err == nil {
		t.Fatal("expected error for old-origin Factory entry, got nil")
	}
	if !strings.Contains(err.Error(), "8787") {
		t.Fatalf("error should mention old port 8787: %s", err.Error())
	}
}

func TestOmittedPortPreflightAllowsNewOrigin(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	factoryDir := filepath.Join(home, ".factory")
	os.MkdirAll(factoryDir, 0o700)
	settings := filepath.Join(factoryDir, "settings.json")
	os.WriteFile(settings, []byte(`{"customModels":[{"model":"my-model","provider":"generic-chat-completion-api","baseUrl":"http://127.0.0.1:9787"}]}`), 0o600)

	cfg := parseTestConfig(t, "listen:\n  host: 127.0.0.1\n"+validModelYAML)

	if err := omittedPortPreflight(cfg); err != nil {
		t.Fatalf("expected no error for new-origin entry, got: %v", err)
	}
}

func TestOmittedPortPreflightSkippedForExplicitPort(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	factoryDir := filepath.Join(home, ".factory")
	os.MkdirAll(factoryDir, 0o700)
	settings := filepath.Join(factoryDir, "settings.json")
	os.WriteFile(settings, []byte(`{"customModels":[{"model":"my-model","provider":"generic-chat-completion-api","baseUrl":"http://127.0.0.1:8787"}]}`), 0o600)

	cfg := parseTestConfig(t, "listen:\n  host: 127.0.0.1\n  port: 8787\n"+validModelYAML)

	if err := omittedPortPreflight(cfg); err != nil {
		t.Fatalf("expected no error for explicit port, got: %v", err)
	}
}

func TestOmittedPortPreflightMalformedFactoryRefuses(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	factoryDir := filepath.Join(home, ".factory")
	os.MkdirAll(factoryDir, 0o700)
	settings := filepath.Join(factoryDir, "settings.json")
	os.WriteFile(settings, []byte(`{not valid json`), 0o600)

	cfg := parseTestConfig(t, "listen:\n  host: 127.0.0.1\n"+validModelYAML)

	err := omittedPortPreflight(cfg)
	if err == nil {
		t.Fatal("expected error for malformed Factory, got nil")
	}
}

func TestOmittedPortPreflightDoesNotMutate(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	factoryDir := filepath.Join(home, ".factory")
	os.MkdirAll(factoryDir, 0o700)
	settings := filepath.Join(factoryDir, "settings.json")
	body := `{"customModels":[{"model":"my-model","provider":"generic-chat-completion-api","baseUrl":"http://127.0.0.1:8787"}]}`
	os.WriteFile(settings, []byte(body), 0o600)

	cfg := parseTestConfig(t, "listen:\n  host: 127.0.0.1\n"+validModelYAML)

	_ = omittedPortPreflight(cfg)

	after, _ := os.ReadFile(settings)
	if string(after) != body {
		t.Fatalf("preflight mutated the Factory file")
	}
}
