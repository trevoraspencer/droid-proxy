package oauth

import (
	"context"
	"errors"
	"github.com/trevoraspencer/droid-proxy/internal/config"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestTokenLockWaitCanBeCancelled(t *testing.T) {
	m := NewManager(&config.Config{OAuth: config.OAuth{AuthDir: t.TempDir()}})
	release, err := m.lockToken(t.Context(), "account")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	for _, manager := range []*Manager{m, NewManager(m.cfg)} {
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
		_, err := manager.lockToken(ctx, "account")
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("wait did not cancel: %v", err)
		}
	}
}

func TestTokenLockCoordinatesDirectoryAliases(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	if err := os.Mkdir(real, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(dir, "alias")
	if err := os.Symlink(real, alias); err != nil {
		t.Skipf("directory symlinks unavailable: %v", err)
	}
	m := NewManager(&config.Config{OAuth: config.OAuth{AuthDir: real}})
	other := NewManager(&config.Config{OAuth: config.OAuth{AuthDir: alias}})
	key := refreshLockKey(&Token{path: filepath.Join(real, "account.json")})
	aliasKey := refreshLockKey(&Token{path: filepath.Join(alias, "account.json")})
	if key != aliasKey {
		t.Fatalf("directory aliases use different locks: %q, %q", key, aliasKey)
	}
	release, err := m.lockToken(t.Context(), key)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	if _, err := other.lockToken(ctx, aliasKey); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("alias bypassed file lock: %v", err)
	}
}

func TestDisableWaitsForRefreshAndPreservesNewCredentials(t *testing.T) {
	m := NewManager(&config.Config{OAuth: config.OAuth{AuthDir: t.TempDir()}})
	path, err := m.SaveToken(&Token{Type: string(ProviderCodex), Email: "test@example.com", AccessToken: "old", RefreshToken: "old-refresh"})
	if err != nil {
		t.Fatal(err)
	}
	token, err := m.loadTokenPath(path)
	if err != nil {
		t.Fatal(err)
	}
	key := refreshLockKey(token)
	release, err := m.lockToken(t.Context(), key)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if release != nil {
			release()
		}
	}()
	other := NewManager(m.cfg)
	result := make(chan error, 1)
	go func() { _, err := other.SetTokenDisabled(ProviderCodex, token.Email, true); result <- err }()
	deadline := time.Now().Add(time.Second)
	for {
		other.mu.Lock()
		waiting := other.refreshLocks[key] != nil
		other.mu.Unlock()
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("disable did not reach lock")
		}
		time.Sleep(time.Millisecond)
	}
	token.AccessToken, token.RefreshToken = "new", "new-refresh"
	if _, err := m.SaveToken(token); err != nil {
		t.Fatal(err)
	}
	release()
	release = nil
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	latest, err := m.loadTokenPath(path)
	if err != nil {
		t.Fatal(err)
	}
	if !latest.Disabled || latest.AccessToken != "new" || latest.RefreshToken != "new-refresh" {
		t.Fatal("disable lost refreshed credentials")
	}
}

func TestForceRefreshDeduplicatesAcrossManagers(t *testing.T) {
	m := NewManager(&config.Config{OAuth: config.OAuth{AuthDir: t.TempDir()}})
	path, err := m.SaveToken(&Token{Type: string(ProviderCodex), Email: "test@example.com", AccessToken: "old", RefreshToken: "refresh", Expired: time.Now().Add(time.Hour).Format(time.RFC3339)})
	if err != nil {
		t.Fatal(err)
	}
	other := NewManager(m.cfg)
	first, err := m.loadTokenPath(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := other.loadTokenPath(path)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		time.Sleep(50 * time.Millisecond)
		_, _ = io.WriteString(w, `{"access_token":"new","refresh_token":"rotated","expires_in":3600}`)
	}))
	defer srv.Close()
	old := codexTokenURL
	codexTokenURL = srv.URL
	defer func() { codexTokenURL = old }()
	results := make(chan error, 2)
	for _, pair := range []struct {
		m     *Manager
		token *Token
	}{{m, first}, {other, second}} {
		go func() {
			token, err := pair.m.ForceRefresh(t.Context(), pair.token)
			if err == nil && (token.AccessToken != "new" || token.RefreshToken != "rotated") {
				err = errors.New("lost refreshed credentials")
			}
			results <- err
		}()
	}
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("rotated refresh token %d times", calls.Load())
	}
}

func TestLateQuotaUpdateDoesNotRecreateLoggedOutToken(t *testing.T) {
	m := NewManager(&config.Config{OAuth: config.OAuth{AuthDir: t.TempDir()}})
	path, err := m.SaveToken(&Token{Type: string(ProviderCodex), Email: "test@example.com", AccessToken: "access"})
	if err != nil {
		t.Fatal(err)
	}
	token, err := m.loadTokenPath(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.DeleteToken(ProviderCodex, token.Email); err != nil {
		t.Fatal(err)
	}
	if err = m.RecordCodexUsage(token, &CodexQuota{}, nil); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected missing token: %v", err)
	}
	if _, err = os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("logout was undone: %v", err)
	}
}

func TestCodexUsageLimitScalarReset(t *testing.T) {
	now := time.Unix(1000, 0)
	for _, test := range []struct {
		body string
		want int64
	}{
		{`{"error":{"type":"usage_limit_reached","resets_at":1100}}`, 1100},
		{`{"error":{"type":"usage_limit_reached","resets_in_seconds":60}}`, 1060},
		{`{"error":{"type":"invalid_request","resets_at":1100}}`, 0},
		{`{"error":{"type":"usage_limit_reached","resets_at":900}}`, 0},
		{`{"error":{"type":"usage_limit_reached","resets_in_seconds":1e30}}`, 0},
	} {
		got := ParseCodexUsageLimitReset([]byte(test.body), now)
		if test.want == 0 {
			if got != nil {
				t.Errorf("unexpected reset for %s", test.body)
			}
			continue
		}
		if got == nil || got.Unix() != test.want {
			t.Errorf("reset for %s = %v", test.body, got)
		}
	}
}
