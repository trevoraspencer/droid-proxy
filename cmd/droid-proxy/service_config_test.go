package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateServiceInstallConfigLoadsEnvFileAndRestoresEnv(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	key := "DROID_PROXY_SERVICE_CONFIG_TEST_KEY"
	_ = os.Unsetenv(key)
	t.Cleanup(func() { _ = os.Unsetenv(key) })

	raw := `models:
  - alias: test-model
    display_name: Test Model
    factory_provider: openai
    upstream_protocol: openai-chat
    upstream_model: test-model
    base_url: https://api.example.test/v1
    api_key_env: ` + key + `
`
	if err := os.WriteFile(configPath, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".env.local"), []byte(key+"=secret-from-env-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := validateServiceInstallConfig(configPath); err != nil {
		t.Fatalf("validateServiceInstallConfig: %v", err)
	}
	if val, ok := os.LookupEnv(key); ok {
		t.Fatalf("env key %s leaked into process env with value %q", key, val)
	}
}

func TestValidateServiceInstallConfigIgnoresAmbientAPIKey(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	key := "DROID_PROXY_SERVICE_CONFIG_AMBIENT_TEST_KEY"
	t.Setenv(key, "ambient-secret")

	raw := `models:
  - alias: test-model
    display_name: Test Model
    factory_provider: openai
    upstream_protocol: openai-chat
    upstream_model: test-model
    base_url: https://api.example.test/v1
    api_key_env: ` + key + `
`
	if err := os.WriteFile(configPath, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}

	err := validateServiceInstallConfig(configPath)
	if err == nil {
		t.Fatal("validateServiceInstallConfig error = nil, want missing service env key error")
	}
	if !strings.Contains(err.Error(), "env var "+key+" is empty") {
		t.Fatalf("error missing service env key failure:\n%v", err)
	}
	if got := os.Getenv(key); got != "ambient-secret" {
		t.Fatalf("ambient env was not restored: %q", got)
	}
}

func TestDoctorServiceConfigIssuesRejectsInvalidExistingConfig(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	envPath := filepath.Join(dir, ".env.local")
	if err := os.WriteFile(configPath, []byte("models: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(envPath, []byte("DROID_SENTINEL=doctor-redacted-sentinel\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	issues := doctorServiceConfigIssues([]string{
		"/tmp/droid-proxy", "start", "--foreground",
		"--config", configPath,
		"--env-file", envPath,
	})
	if len(issues) != 1 {
		t.Fatalf("issues = %#v, want one config issue", issues)
	}
	text := strings.Join(issues, "\n")
	for _, want := range []string{"service config is not runnable", "at least one model", "droid-proxy config"} {
		if !strings.Contains(text, want) {
			t.Fatalf("issue missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "doctor-redacted-sentinel") || strings.Contains(text, "DROID_SENTINEL=") {
		t.Fatalf("doctor service config issue leaked env contents:\n%s", text)
	}
}

func TestDoctorServiceConfigIssuesSkipsMissingConfigPath(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.yaml")

	issues := doctorServiceConfigIssues([]string{
		"/tmp/droid-proxy", "start", "--foreground", "--config", missing,
	})
	if len(issues) != 0 {
		t.Fatalf("issues = %#v, want missing path handled by daemon service checks", issues)
	}
}
