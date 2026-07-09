package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/trevoraspencer/droid-proxy/internal/config"
	"github.com/trevoraspencer/droid-proxy/internal/daemon"
	"github.com/trevoraspencer/droid-proxy/internal/oauth"
)

func TestPrintAuthUsageIncludesSubcommands(t *testing.T) {
	var out bytes.Buffer
	printAuthUsage(&out)
	text := out.String()
	for _, want := range []string{
		"droid-proxy auth <codex|xai>",
		"droid-proxy auth status",
		"droid-proxy auth pool",
		"droid-proxy auth <enable|disable|logout>",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("auth usage missing %q:\n%s", want, text)
		}
	}
}

func TestPrintServiceUsage(t *testing.T) {
	var out bytes.Buffer
	printServiceUsage(&out)
	if !strings.Contains(out.String(), "droid-proxy service <install|uninstall>") {
		t.Fatalf("service usage missing command form:\n%s", out.String())
	}
}

func TestResolveDefaultConfigPathPrefersCurrentDirectory(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("models: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	got := resolveDefaultConfigPath(dir, "", filepath.Join(t.TempDir(), "config.yaml"), daemon.RuntimeMetadata{
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

	got := resolveDefaultConfigPath(t.TempDir(), "", filepath.Join(t.TempDir(), "config.yaml"), daemon.RuntimeMetadata{ConfigPath: configPath}, true, regularFileExists)
	if got != configPath {
		t.Fatalf("default config path = %q, want %q", got, configPath)
	}
}

func TestResolveDefaultConfigPathUsesPerUserConfig(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "droid-proxy", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte("models: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	got := resolveDefaultConfigPath(t.TempDir(), "", configPath, daemon.RuntimeMetadata{
		ConfigPath: filepath.Join(t.TempDir(), "missing.yaml"),
	}, true, regularFileExists)
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

	got := resolveDefaultConfigPath(t.TempDir(), filepath.Join(exeDir, "droid-proxy"), filepath.Join(t.TempDir(), "config.yaml"), daemon.RuntimeMetadata{
		ConfigPath: filepath.Join(t.TempDir(), "missing.yaml"),
	}, true, regularFileExists)
	if got != configPath {
		t.Fatalf("default config path = %q, want %q", got, configPath)
	}
}

func TestResolveDefaultConfigPathFallsBackToConfigYAML(t *testing.T) {
	got := resolveDefaultConfigPath(t.TempDir(), "", filepath.Join(t.TempDir(), "config.yaml"), daemon.RuntimeMetadata{}, false, regularFileExists)
	if got != "config.yaml" {
		t.Fatalf("default config path = %q, want config.yaml", got)
	}
}

func TestTailLinesReturnsLastNLines(t *testing.T) {
	got, err := tailLines(strings.NewReader("one\ntwo\nthree\nfour\n"), 2)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, "|") != "three|four" {
		t.Fatalf("tailLines = %#v", got)
	}
}

func TestTailLinesPreservesInteriorBlankLines(t *testing.T) {
	got, err := tailLines(strings.NewReader("\n\none\n\ntwo\n\n"), 3)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, "|") != "one||two" {
		t.Fatalf("tailLines = %#v", got)
	}
}

func TestTailLinesNonPositiveShowsAllContentLines(t *testing.T) {
	got, err := tailLines(strings.NewReader("\none\ntwo\n"), 0)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, "|") != "one|two" {
		t.Fatalf("tailLines = %#v", got)
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

func TestFormatPoolHealthJSONShowsUnhealthyUntil(t *testing.T) {
	raw := []byte(`{
		"strategy": "round_robin",
		"codex_account_count": 1,
		"eligible_count": 0,
		"accounts": [
			{
				"selector": "user@example.com",
				"healthy": false,
				"unhealthy_until": "2026-06-12T17:30:00Z",
				"in_flight": 0,
				"bound_conversation_count": 0
			}
		]
	}`)
	out, err := formatPoolHealthJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"unhealthy", "unhealthy:2026-06-12T17:30:00Z"} {
		if !strings.Contains(out, want) {
			t.Fatalf("pool health output missing %q:\n%s", want, out)
		}
	}
}

func TestFormatPoolHealthJSONShowsRemovedTokenFile(t *testing.T) {
	raw := []byte(`{
		"strategy": "round_robin",
		"codex_account_count": 1,
		"eligible_count": 0,
		"accounts": [
			{
				"selector": "removed@example.com",
				"token_file_present": false,
				"healthy": true,
				"in_flight": 1,
				"bound_conversation_count": 0
			}
		]
	}`)
	out, err := formatPoolHealthJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "removed@example.com") || !strings.Contains(out, "removed") {
		t.Fatalf("pool health output should show removed token file:\n%s", out)
	}
}

func TestFormatPoolHealthJSONShowsEligibilityReasons(t *testing.T) {
	raw := []byte(`{
		"strategy": "sticky",
		"codex_account_count": 1,
		"eligible_count": 0,
		"accounts": [
			{
				"selector": "user@example.com",
				"eligible": false,
				"eligibility_status": "disabled",
				"eligibility_reasons": ["disabled", "expired_no_refresh"],
				"healthy": true,
				"disabled": true,
				"in_flight": 0,
				"bound_conversation_count": 0
			}
		]
	}`)
	out, err := formatPoolHealthJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"STATUS", "disabled,expired_no_refresh"} {
		if !strings.Contains(out, want) {
			t.Fatalf("pool health output missing %q:\n%s", want, out)
		}
	}
}
