package main

import (
	"strings"
	"testing"
	"time"

	"droid-proxy/internal/config"
	"droid-proxy/internal/oauth"
)

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
