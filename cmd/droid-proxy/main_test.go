package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"droid-proxy/internal/config"
	"droid-proxy/internal/daemon"
	"droid-proxy/internal/oauth"
)

func TestResolveDefaultConfigPathPrefersCurrentDirectory(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("models: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	got := resolveDefaultConfigPath(dir, "", daemon.RuntimeMetadata{
		ConfigPath: filepath.Join(t.TempDir(), "config.local.yaml"),
	}, true, regularFileExists)
	if got != configPath {
		t.Fatalf("default config path = %q, want %q", got, configPath)
	}
}

func TestResolveDefaultConfigPathUsesRuntimeMetadata(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.local.yaml")
	if err := os.WriteFile(configPath, []byte("models: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	got := resolveDefaultConfigPath(t.TempDir(), "", daemon.RuntimeMetadata{ConfigPath: configPath}, true, regularFileExists)
	if got != configPath {
		t.Fatalf("default config path = %q, want %q", got, configPath)
	}
}

func TestResolveDefaultConfigPathUsesExecutableDirectory(t *testing.T) {
	exeDir := t.TempDir()
	configPath := filepath.Join(exeDir, "config.local.yaml")
	if err := os.WriteFile(configPath, []byte("models: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	got := resolveDefaultConfigPath(t.TempDir(), filepath.Join(exeDir, "droid-proxy"), daemon.RuntimeMetadata{
		ConfigPath: filepath.Join(t.TempDir(), "missing.yaml"),
	}, true, regularFileExists)
	if got != configPath {
		t.Fatalf("default config path = %q, want %q", got, configPath)
	}
}

func TestResolveDefaultConfigPathFallsBackToConfigYAML(t *testing.T) {
	got := resolveDefaultConfigPath(t.TempDir(), "", daemon.RuntimeMetadata{}, false, regularFileExists)
	if got != "config.yaml" {
		t.Fatalf("default config path = %q, want config.yaml", got)
	}
}

func TestFormatAuthStatusDoesNotExposeSecrets(t *testing.T) {
	manager := oauth.NewManager(&config.Config{OAuth: config.OAuth{AuthDir: t.TempDir()}})
	expires := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	if _, err := manager.SaveToken(&oauth.Token{
		Type:         string(config.OAuthProviderXAI),
		AccessToken:  "access-secret",
		RefreshToken: "refresh-secret",
		IDToken:      "id-secret",
		Email:        "user@example.com",
		Subject:      "sub-123",
		AccountID:    "acct_123",
		Expired:      expires,
		LastRefresh:  "2026-05-29T10:00:00Z",
		Disabled:     true,
	}); err != nil {
		t.Fatal(err)
	}
	out, err := formatAuthStatus(manager, []config.OAuthProvider{config.OAuthProviderXAI})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"provider: xai",
		"account: user@example.com",
		"email: user@example.com",
		"sub: sub-123",
		"account_id: acct_123",
		"expires: " + expires,
		"last_refresh: 2026-05-29T10:00:00Z",
		"disabled: true",
		"path:",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("status output missing %q:\n%s", want, out)
		}
	}
	for _, secret := range []string{"access-secret", "refresh-secret", "id-secret"} {
		if strings.Contains(out, secret) {
			t.Fatalf("status output leaked %q:\n%s", secret, out)
		}
	}
}
