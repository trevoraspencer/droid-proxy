package oauth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"droid-proxy/internal/config"
)

func TestGeneratePKCE(t *testing.T) {
	pkce, err := GeneratePKCE()
	if err != nil {
		t.Fatal(err)
	}
	if pkce.CodeVerifier == "" || pkce.CodeChallenge == "" {
		t.Fatalf("pkce fields must be populated: %+v", pkce)
	}
	if pkce.CodeVerifier == pkce.CodeChallenge {
		t.Fatalf("code challenge should be derived from verifier, got identical values")
	}
}

func TestBuildAuthURL_Codex(t *testing.T) {
	pkce := &PKCE{CodeVerifier: "verifier", CodeChallenge: "challenge"}
	rawURL, err := BuildAuthURL(ProviderCodex, "http://localhost:1455/auth/callback", "state-1", "", pkce)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	q := parsed.Query()
	for key, want := range map[string]string{
		"client_id":             CodexClientID,
		"response_type":         "code",
		"redirect_uri":          "http://localhost:1455/auth/callback",
		"state":                 "state-1",
		"code_challenge":        "challenge",
		"code_challenge_method": "S256",
	} {
		if got := q.Get(key); got != want {
			t.Fatalf("%s=%q want %q in %s", key, got, want, rawURL)
		}
	}
	if !strings.Contains(q.Get("scope"), "offline_access") {
		t.Fatalf("scope should request refresh capability, got %q", q.Get("scope"))
	}
	for _, wantScope := range []string{"api.connectors.read", "api.connectors.invoke"} {
		if !strings.Contains(q.Get("scope"), wantScope) {
			t.Fatalf("scope should include %q, got %q", wantScope, q.Get("scope"))
		}
	}
}

func TestCodexRedirectURIUsesRegisteredLocalhost(t *testing.T) {
	if got, want := codexRedirectURI("127.0.0.1:1455"), "http://localhost:1455/auth/callback"; got != want {
		t.Fatalf("codexRedirectURI=%q want %q", got, want)
	}
}

func TestCallbackHandler(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		out := make(chan CallbackResult, 1)
		handler := callbackHandler("/auth/callback", "state-ok", out)
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/auth/callback?code=code-ok&state=state-ok", nil)
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		result := <-out
		if result.Code != "code-ok" || result.State != "state-ok" || result.Err != "" {
			t.Fatalf("bad callback result: %+v", result)
		}
	})

	t.Run("state mismatch", func(t *testing.T) {
		out := make(chan CallbackResult, 1)
		handler := callbackHandler("/auth/callback", "state-ok", out)
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/auth/callback?code=code-ok&state=wrong", nil)
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		result := <-out
		if !strings.Contains(result.Err, "state mismatch") {
			t.Fatalf("expected state mismatch, got %+v", result)
		}
	})
}

func TestSaveAndLoadTokenPermissionsAndAccountSelection(t *testing.T) {
	authDir := t.TempDir()
	manager := NewManager(&config.Config{OAuth: config.OAuth{AuthDir: authDir}})
	path, err := manager.SaveToken(&Token{
		Type:         string(ProviderCodex),
		AccessToken:  "access-secret",
		RefreshToken: "refresh-secret",
		Email:        "user@example.com",
		AccountID:    "account-123",
		Expired:      time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
	})
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(path) != authDir {
		t.Fatalf("token path %q not under auth dir %q", path, authDir)
	}
	assertPerm(t, authDir, 0o700)
	assertPerm(t, path, 0o600)

	token, err := manager.LoadToken(ProviderCodex, "user@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if token.AccessToken != "access-secret" || token.AccountID != "account-123" {
		t.Fatalf("loaded wrong token: %+v", token)
	}
	if _, err := manager.LoadToken(ProviderXAI, "user@example.com"); err == nil {
		t.Fatal("expected no xai token")
	}
}

func TestTokenStoreDisableEnableAndLogout(t *testing.T) {
	authDir := t.TempDir()
	manager := NewManager(&config.Config{OAuth: config.OAuth{AuthDir: authDir}})
	path, err := manager.SaveToken(&Token{
		Type:         string(ProviderXAI),
		AccessToken:  "access-secret",
		RefreshToken: "refresh-secret",
		Email:        "user@example.com",
		Subject:      "sub-123",
		AccountID:    "acct_123",
		Expired:      time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.LoadToken(ProviderXAI, "acct_123"); err != nil {
		t.Fatalf("expected account_id selector to match: %v", err)
	}

	disabled, err := manager.SetTokenDisabled(ProviderXAI, "user@example.com", true)
	if err != nil {
		t.Fatal(err)
	}
	if !disabled.Disabled {
		t.Fatalf("token not disabled: %+v", disabled)
	}
	if _, err := manager.LoadToken(ProviderXAI, "user@example.com"); err == nil {
		t.Fatal("disabled token should be filtered from LoadToken")
	}
	found, err := manager.FindToken(ProviderXAI, "sub-123", true)
	if err != nil {
		t.Fatal(err)
	}
	if !found.Disabled {
		t.Fatalf("FindToken(includeDisabled) should return disabled token: %+v", found)
	}

	enabled, err := manager.SetTokenDisabled(ProviderXAI, filepath.Base(path), false)
	if err != nil {
		t.Fatal(err)
	}
	if enabled.Disabled {
		t.Fatalf("token not enabled: %+v", enabled)
	}
	if _, err := manager.LoadToken(ProviderXAI, "user@example.com"); err != nil {
		t.Fatalf("enabled token should load: %v", err)
	}
	deletedPath, err := manager.DeleteToken(ProviderXAI, "acct_123")
	if err != nil {
		t.Fatal(err)
	}
	if deletedPath != path {
		t.Fatalf("deleted path=%q want %q", deletedPath, path)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("token file should be deleted, stat err=%v", err)
	}
}

func TestRefreshCodexSavesRefreshedToken(t *testing.T) {
	authDir := t.TempDir()
	manager := NewManager(&config.Config{OAuth: config.OAuth{AuthDir: authDir}})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		values, err := url.ParseQuery(string(body))
		if err != nil {
			t.Fatalf("parse form: %v", err)
		}
		if values.Get("grant_type") != "refresh_token" || values.Get("refresh_token") != "refresh-secret" {
			t.Fatalf("bad refresh form: %s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "new-access",
			"refresh_token": "new-refresh",
			"expires_in":    3600,
		})
	}))
	defer srv.Close()

	oldTokenURL := codexTokenURL
	codexTokenURL = srv.URL
	t.Cleanup(func() { codexTokenURL = oldTokenURL })

	refreshed, err := manager.RefreshIfNeeded(context.Background(), &Token{
		Type:         string(ProviderCodex),
		RefreshToken: "refresh-secret",
		Email:        "user@example.com",
		Expired:      time.Now().Add(-time.Hour).UTC().Format(time.RFC3339),
	})
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.AccessToken != "new-access" || refreshed.RefreshToken != "new-refresh" {
		t.Fatalf("bad refreshed token: %+v", refreshed)
	}
	loaded, err := manager.LoadToken(ProviderCodex, "user@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.AccessToken != "new-access" {
		t.Fatalf("refreshed token was not saved: %+v", loaded)
	}
}

func TestRefreshIfNeededDeduplicatesConcurrentRefresh(t *testing.T) {
	authDir := t.TempDir()
	manager := NewManager(&config.Config{OAuth: config.OAuth{AuthDir: authDir}})
	path, err := manager.SaveToken(&Token{
		Type:         string(ProviderCodex),
		AccessToken:  "old-access",
		RefreshToken: "refresh-secret",
		Email:        "user@example.com",
		Expired:      time.Now().Add(-time.Hour).UTC().Format(time.RFC3339),
	})
	if err != nil {
		t.Fatal(err)
	}
	var refreshes atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		refreshes.Add(1)
		body, _ := io.ReadAll(r.Body)
		values, err := url.ParseQuery(string(body))
		if err != nil {
			t.Fatalf("parse form: %v", err)
		}
		if values.Get("refresh_token") != "refresh-secret" {
			t.Fatalf("bad refresh token: %s", body)
		}
		time.Sleep(50 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "new-access",
			"refresh_token": "new-refresh",
			"expires_in":    3600,
		})
	}))
	defer srv.Close()

	oldTokenURL := codexTokenURL
	codexTokenURL = srv.URL
	t.Cleanup(func() { codexTokenURL = oldTokenURL })

	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			token, err := manager.loadTokenPath(path)
			if err != nil {
				errs <- err
				return
			}
			<-start
			refreshed, err := manager.RefreshIfNeeded(context.Background(), token)
			if err != nil {
				errs <- err
				return
			}
			if refreshed.AccessToken != "new-access" || refreshed.RefreshToken != "new-refresh" {
				errs <- fmt.Errorf("bad refreshed token: %+v", refreshed)
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := refreshes.Load(); got != 1 {
		t.Fatalf("refresh requests=%d want 1", got)
	}
}

func TestRefreshPreservesRefreshTokenWhenResponseOmitsIt(t *testing.T) {
	authDir := t.TempDir()
	manager := NewManager(&config.Config{OAuth: config.OAuth{AuthDir: authDir}})
	oldTokenURL := codexTokenURL
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "new-access",
			"expires_in":   3600,
		})
	}))
	defer srv.Close()
	codexTokenURL = srv.URL
	t.Cleanup(func() { codexTokenURL = oldTokenURL })
	refreshed, err := manager.RefreshIfNeeded(context.Background(), &Token{
		Type:         string(ProviderCodex),
		RefreshToken: "old-refresh",
		Expired:      time.Now().Add(-time.Hour).UTC().Format(time.RFC3339),
	})
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.RefreshToken != "old-refresh" {
		t.Fatalf("refresh token not preserved: %+v", refreshed)
	}
}

func TestLoginDeviceCodex(t *testing.T) {
	authDir := t.TempDir()
	manager := NewManager(&config.Config{OAuth: config.OAuth{AuthDir: authDir}})
	var polls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/usercode":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"device_auth_id": "device-1",
				"user_code":      "ABCD-EFGH",
				"interval":       0.001,
			})
		case "/token":
			if polls.Add(1) == 1 {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"status":"pending"}`))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"authorization_code": "auth-code",
				"code_verifier":      "device-verifier",
				"code_challenge":     "device-challenge",
			})
		case "/oauth/token":
			body, _ := io.ReadAll(r.Body)
			values, err := url.ParseQuery(string(body))
			if err != nil {
				t.Fatalf("parse token form: %v", err)
			}
			if values.Get("grant_type") != "authorization_code" ||
				values.Get("code") != "auth-code" ||
				values.Get("code_verifier") != "device-verifier" ||
				values.Get("redirect_uri") != codexDeviceRedirectURI {
				t.Fatalf("bad device token exchange: %s", body)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token":  "device-access",
				"refresh_token": "device-refresh",
				"expires_in":    3600,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	oldUserCodeURL, oldTokenURL, oldOAuthURL := codexDeviceUserCodeURL, codexDeviceTokenURL, codexTokenURL
	oldVerifyURL := codexDeviceVerifyURL
	codexDeviceUserCodeURL = srv.URL + "/usercode"
	codexDeviceTokenURL = srv.URL + "/token"
	codexTokenURL = srv.URL + "/oauth/token"
	codexDeviceVerifyURL = srv.URL + "/verify"
	t.Cleanup(func() {
		codexDeviceUserCodeURL = oldUserCodeURL
		codexDeviceTokenURL = oldTokenURL
		codexTokenURL = oldOAuthURL
		codexDeviceVerifyURL = oldVerifyURL
	})

	path, err := manager.LoginDevice(context.Background(), ProviderCodex, false)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(path) != authDir {
		t.Fatalf("device token path=%q not under %q", path, authDir)
	}
	loaded, err := manager.LoadToken(ProviderCodex, "")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.AccessToken != "device-access" || loaded.RefreshToken != "device-refresh" {
		t.Fatalf("bad saved device token: %+v", loaded)
	}
	if polls.Load() < 2 {
		t.Fatalf("expected pending poll before success, got %d", polls.Load())
	}
}

func TestLoginDeviceCodexTimeoutAndProviderValidation(t *testing.T) {
	manager := NewManager(&config.Config{OAuth: config.OAuth{AuthDir: t.TempDir()}})
	if _, err := manager.LoginDevice(context.Background(), ProviderXAI, false); err == nil || !strings.Contains(err.Error(), "only supported for codex") {
		t.Fatalf("expected non-codex device rejection, got %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/usercode":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"device_auth_id": "device-1",
				"user_code":      "ABCD-EFGH",
				"interval":       0.001,
			})
		case "/token":
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"status":"pending"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	oldUserCodeURL, oldTokenURL, oldTimeout := codexDeviceUserCodeURL, codexDeviceTokenURL, codexDeviceLoginTimeout
	codexDeviceUserCodeURL = srv.URL + "/usercode"
	codexDeviceTokenURL = srv.URL + "/token"
	codexDeviceLoginTimeout = 5 * time.Millisecond
	t.Cleanup(func() {
		codexDeviceUserCodeURL = oldUserCodeURL
		codexDeviceTokenURL = oldTokenURL
		codexDeviceLoginTimeout = oldTimeout
	})
	_, err := manager.LoginDevice(context.Background(), ProviderCodex, false)
	if err == nil {
		t.Fatal("expected device timeout")
	}
}

func TestTokenRequestErrorDoesNotLeakResponseBody(t *testing.T) {
	secret := "refresh-secret-that-must-not-leak"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"` + secret + `"}`))
	}))
	defer srv.Close()

	oldTokenURL := codexTokenURL
	codexTokenURL = srv.URL
	t.Cleanup(func() { codexTokenURL = oldTokenURL })

	manager := NewManager(&config.Config{OAuth: config.OAuth{AuthDir: t.TempDir()}})
	_, err := manager.RefreshIfNeeded(context.Background(), &Token{
		Type:         string(ProviderCodex),
		RefreshToken: secret,
		Expired:      time.Now().Add(-time.Hour).UTC().Format(time.RFC3339),
	})
	if err == nil {
		t.Fatal("expected refresh error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("refresh error leaked token/response body: %v", err)
	}
}

func TestExchangeXAICode(t *testing.T) {
	pkce := &PKCE{CodeVerifier: "verifier", CodeChallenge: "challenge"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		values, err := url.ParseQuery(string(body))
		if err != nil {
			t.Fatalf("parse form: %v", err)
		}
		if values.Get("grant_type") != "authorization_code" ||
			values.Get("client_id") != XAIClientID ||
			values.Get("code") != "code-123" ||
			values.Get("code_verifier") != "verifier" {
			t.Fatalf("bad exchange form: %s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "xai-access",
			"refresh_token": "xai-refresh",
			"expires_in":    1800,
		})
	}))
	defer srv.Close()

	token, err := exchangeXAICode(context.Background(), "code-123", "http://127.0.0.1:56121/callback", pkce, srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if token.Provider() != ProviderXAI || token.BaseURL != XAIDefaultBaseURL || token.TokenEndpoint != srv.URL {
		t.Fatalf("bad xai token metadata: %+v", token)
	}
}

func TestValidateXAIEndpoint(t *testing.T) {
	for _, good := range []string{
		"https://auth.x.ai/oauth/authorize",
		"https://x.ai/oauth/token",
	} {
		if _, err := validateXAIEndpoint(good, "endpoint"); err != nil {
			t.Fatalf("expected %q to validate: %v", good, err)
		}
	}
	for _, bad := range []string{
		"http://auth.x.ai/oauth/authorize",
		"https://example.com/oauth/token",
	} {
		if _, err := validateXAIEndpoint(bad, "endpoint"); err == nil {
			t.Fatalf("expected %q to fail", bad)
		}
	}
}

func TestParseCodexIdentityFallsBackToAccessToken(t *testing.T) {
	accessToken := testJWT(t, map[string]any{
		"sub": "sub-access",
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": "acct_access",
			"chatgpt_email":      "user@example.com",
		},
	})
	email, subject, accountID := parseCodexIdentity("", accessToken)
	if email != "user@example.com" || subject != "sub-access" || accountID != "acct_access" {
		t.Fatalf("bad codex identity email=%q subject=%q account=%q", email, subject, accountID)
	}
}

func assertPerm(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode=%#o want %#o", path, got, want)
	}
}

func testJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	header, err := json.Marshal(map[string]any{"alg": "none"})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload) + "."
}
