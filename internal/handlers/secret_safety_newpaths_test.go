package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"

	"droid-proxy/internal/config"
	"droid-proxy/internal/logging"
	"droid-proxy/internal/oauth"
	"droid-proxy/internal/upstream"
)

// ---- VAL-CROSS-009: Logs and errors are secret-safe across new paths ----
//
// These tests verify that sentinel access tokens, refresh tokens, ID tokens,
// account IDs, authorization headers, token file contents, and upstream error
// bodies do not appear in client responses or captured logs from pool health,
// watcher invalid-file handling, selection exhaustion, refresh failure,
// upstream error-body relay, and 429/5xx failover — in both text and JSON
// logging modes.

// Sentinel secrets used across tests. These must NEVER appear in any output.
const (
	sentinelAccessToken  = "sk-CROSS009-AccessToken-Sentinel-12345"
	sentinelRefreshToken = "sk-CROSS009-RefreshToken-Sentinel-67890"
	sentinelIDToken      = "eyJ-CROSS009-IDToken-Sentinel-abcde"
	sentinelAccountID    = "acct-CROSS009-AccountID-Sentinel"
	sentinelUpstreamErr  = "sk-CROSS009-UpstreamErrorSecret-xyz"
	sentinelAuthHeader   = "Bearer sk-CROSS009-AuthHeaderSentinel-99999"
)

// allSentinels returns the full list of sentinel secrets that must never
// appear in responses or logs.
func allSentinels() []string {
	return []string{
		sentinelAccessToken,
		sentinelRefreshToken,
		sentinelIDToken,
		sentinelAccountID,
		sentinelUpstreamErr,
		sentinelAuthHeader,
	}
}

// assertNoSentinels checks that none of the sentinel secrets appear in the
// given string, reporting a fatal test error with the leaked sentinel if found.
func assertNoSentinels(t *testing.T, got string, context string) {
	t.Helper()
	for _, s := range allSentinels() {
		if strings.Contains(got, s) {
			t.Fatalf("%s leaked sentinel %q:\n%s", context, s, got)
		}
	}
}

// newSentinelToken creates a Codex token with sentinel secrets.
func newSentinelToken(email string) *oauth.Token {
	return &oauth.Token{
		Type:         string(config.OAuthProviderCodex),
		AccessToken:  sentinelAccessToken,
		RefreshToken: sentinelRefreshToken,
		IDToken:      sentinelIDToken,
		Email:        email,
		AccountID:    sentinelAccountID,
		Expired:      time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
	}
}

// TestRedact_SentinelTokenSecrets verifies that the Redact function masks
// all sentinel secret patterns used by the new pool/failover paths.
func TestRedact_SentinelTokenSecrets(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		mustNot string
	}{
		{
			name:    "sentinel access token in bearer header",
			in:      "Authorization: Bearer " + sentinelAccessToken,
			mustNot: sentinelAccessToken,
		},
		{
			name:    "sentinel refresh token as query parameter",
			in:      "refresh_token=" + sentinelRefreshToken,
			mustNot: sentinelRefreshToken,
		},
		{
			name:    "sentinel access token in JSON value",
			in:      `{"access_token":"` + sentinelAccessToken + `"}`,
			mustNot: sentinelAccessToken,
		},
		{
			name:    "sentinel upstream error as sk- token",
			in:      "error: " + sentinelUpstreamErr,
			mustNot: sentinelUpstreamErr,
		},
		{
			name:    "sentinel auth header",
			in:      "Authorization: " + sentinelAuthHeader,
			mustNot: sentinelAuthHeader,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := logging.Redact(c.in)
			if strings.Contains(got, c.mustNot) {
				t.Errorf("Redact(%q) = %q; must not contain %q", c.in, got, c.mustNot)
			}
		})
	}
}

// TestRedact_JSONLoggingModeSecrets verifies that secrets in JSON-structured
// log output are masked.
func TestRedact_JSONLoggingModeSecrets(t *testing.T) {
	jsonLog := fmt.Sprintf(`{"level":"warn","msg":"upstream error %s","token":"%s"}`,
		sentinelUpstreamErr, sentinelAccessToken)
	got := logging.Redact(jsonLog)
	for _, s := range []string{sentinelUpstreamErr, sentinelAccessToken} {
		if strings.Contains(got, s) {
			t.Fatalf("Redact did not mask sentinel %q in JSON log: %s", s, got)
		}
	}
}

// TestPoolHealthEndpoint_SecretSafeResponse verifies that the pool health
// endpoint response does not contain any sentinel token secrets.
func TestPoolHealthEndpoint_SecretSafeResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	authDir := t.TempDir()

	tok := newSentinelToken("health-user@example.com")
	raw, err := json.MarshalIndent(tok, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, '\n')
	if err := os.WriteFile(filepath.Join(authDir, "codex-health-user.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		OAuth: config.OAuth{AuthDir: authDir},
		Models: []*config.Model{{
			Alias:            "m",
			FactoryProvider:  config.FactoryProviderOpenAI,
			UpstreamProtocol: config.UpstreamCodexResponses,
			OAuthProvider:    config.OAuthProviderCodex,
			BaseURL:          "http://127.0.0.1:1",
		}},
	}
	manager := oauth.NewManager(cfg)
	tokens, err := manager.LoadTokens(config.OAuthProviderCodex)
	if err != nil {
		t.Fatal(err)
	}
	pool := oauth.NewAccountPool(tokens, time.Now, oauth.TestPoolLB(), nil)

	var logs bytes.Buffer
	logger := logrus.New()
	logger.SetOutput(&logs)

	api := &API{
		Cfg:    cfg,
		OAuth:  manager,
		Pool:   pool,
		Logger: logger,
	}
	engine := gin.New()
	engine.GET("/v1/oauth/pool-health", api.PoolHealth)
	engine.GET("/oauth/pool-health", api.PoolHealth)

	for _, path := range []string{"/v1/oauth/pool-health", "/oauth/pool-health"} {
		t.Run(path, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, path, nil)
			engine.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
			}

			body := w.Body.String()
			assertNoSentinels(t, body, "pool-health response")

			var parsed map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &parsed); err != nil {
				t.Fatal(err)
			}
			if parsed["object"] != "oauth_pool_health" {
				t.Errorf("expected object=oauth_pool_health, got %v", parsed["object"])
			}
			accounts, _ := parsed["accounts"].([]any)
			if len(accounts) != 1 {
				t.Fatalf("expected 1 account, got %d", len(accounts))
			}
			acct := accounts[0].(map[string]any)
			if acct["selector"] != "health-user@example.com" {
				t.Errorf("expected safe selector, got %v", acct["selector"])
			}
		})
	}

	assertNoSentinels(t, logs.String(), "pool-health logs")
}

// TestWatcherInvalidFile_LogsDontLeakSecrets verifies that when the watcher
// encounters an invalid JSON file containing sentinel secrets, the warning
// logs do not contain those secrets.
func TestWatcherInvalidFile_LogsDontLeakSecrets(t *testing.T) {
	dir := t.TempDir()

	invalidContent := fmt.Sprintf(`{"type":"codex","access_token":"%s","refresh_token":"%s"`,
		sentinelAccessToken, sentinelRefreshToken)
	if err := os.WriteFile(filepath.Join(dir, "broken.json"), []byte(invalidContent), 0o600); err != nil {
		t.Fatal(err)
	}

	validTok := &oauth.Token{
		Type:        string(config.OAuthProviderCodex),
		AccessToken: "valid-access-not-sentinel",
		Email:       "valid@example.com",
		Expired:     time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
	}
	validRaw, _ := json.MarshalIndent(validTok, "", "  ")
	validRaw = append(validRaw, '\n')
	if err := os.WriteFile(filepath.Join(dir, "codex-valid.json"), validRaw, 0o600); err != nil {
		t.Fatal(err)
	}

	var logs bytes.Buffer
	logger := logrus.New()
	logger.SetOutput(&logs)

	cfg := &config.Config{OAuth: config.OAuth{AuthDir: dir}}
	mgr := oauth.NewManager(cfg)

	tokens, err := oauth.LoadCodexTokensFromDir(mgr, dir, logger)
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 1 || tokens[0].Email != "valid@example.com" {
		t.Fatalf("expected 1 valid token, got %d", len(tokens))
	}

	assertNoSentinels(t, logs.String(), "watcher invalid-file log")
}

// TestSelectionExhaustion_ErrorIsSecretSafe verifies that when no accounts
// are eligible, the error response and logs do not leak sentinel secrets.
func TestSelectionExhaustion_ErrorIsSecretSafe(t *testing.T) {
	gin.SetMode(gin.TestMode)
	authDir := t.TempDir()

	tok := newSentinelToken("disabled@example.com")
	tok.Disabled = true
	cfg := &config.Config{
		OAuth:    config.OAuth{AuthDir: authDir},
		Upstream: config.Upstream{HTTPTimeout: 5 * time.Second},
		Models: []*config.Model{{
			Alias:            "m",
			FactoryProvider:  config.FactoryProviderOpenAI,
			UpstreamProtocol: config.UpstreamCodexResponses,
			OAuthProvider:    config.OAuthProviderCodex,
			BaseURL:          "http://127.0.0.1:1",
		}},
	}
	manager := oauth.NewManager(cfg)
	manager.SaveToken(tok)

	tokens, err := manager.LoadTokens(config.OAuthProviderCodex)
	if err != nil {
		t.Fatal(err)
	}
	pool := oauth.NewAccountPool(tokens, time.Now, oauth.TestPoolLB(), nil)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintln(w, "event: response.completed")
		fmt.Fprintln(w, `data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[]}}`)
		fmt.Fprintln(w)
	}))
	defer srv.Close()
	cfg.Models[0].BaseURL = srv.URL

	var logs bytes.Buffer
	logger := logrus.New()
	logger.SetOutput(&logs)

	router, err := upstream.NewRouter(cfg.Models)
	if err != nil {
		t.Fatal(err)
	}

	api := &API{
		Cfg:    cfg,
		Router: router,
		Client: upstream.NewClient(cfg),
		OAuth:  manager,
		Pool:   pool,
		Logger: logger,
	}
	engine := gin.New()
	engine.POST("/v1/responses", api.Responses)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"m","input":"hi"}`))
	engine.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d body=%s", w.Code, w.Body.String())
	}

	assertNoSentinels(t, w.Body.String(), "selection exhaustion response")
	assertNoSentinels(t, logs.String(), "selection exhaustion logs")
}

// TestUpstreamErrorRelay_FailoverExhaustion_SecretSafe verifies that when
// all failover attempts are exhausted, the relayed upstream error body
// and logs do not leak sentinel token secrets (access tokens, refresh tokens,
// ID tokens, account IDs, or auth headers).
func TestUpstreamErrorRelay_FailoverExhaustion_SecretSafe(t *testing.T) {
	gin.SetMode(gin.TestMode)
	authDir := t.TempDir()

	cfg := &config.Config{
		OAuth: config.OAuth{
			AuthDir: authDir,
			LoadBalancing: config.LoadBalancing{
				Strategy:          config.LoadBalancingFillFirst,
				MaxFailovers:      1,
				RateLimitCooldown: 60 * time.Second,
				ErrorCooldown:     30 * time.Second,
			},
		},
		Upstream: config.Upstream{HTTPTimeout: 5 * time.Second},
		Models: []*config.Model{{
			Alias:            "m",
			FactoryProvider:  config.FactoryProviderOpenAI,
			UpstreamProtocol: config.UpstreamCodexResponses,
			OAuthProvider:    config.OAuthProviderCodex,
			BaseURL:          "http://127.0.0.1:1",
		}},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limited ` + sentinelUpstreamErr + `","type":"rate_limit_exceeded"}}`))
	}))
	defer srv.Close()
	cfg.Models[0].BaseURL = srv.URL

	manager := oauth.NewManager(cfg)
	for _, email := range []string{"a@example.com", "b@example.com"} {
		tok := newSentinelToken(email)
		manager.SaveToken(tok)
	}

	tokens, _ := manager.LoadTokens(config.OAuthProviderCodex)
	pool := oauth.NewAccountPool(tokens, time.Now, oauth.TestPoolLB(), nil, oauth.NewSelector(config.LoadBalancingFillFirst))

	var logs bytes.Buffer
	logger := logrus.New()
	logger.SetOutput(&logs)

	router, _ := upstream.NewRouter(cfg.Models)
	api := &API{
		Cfg:    cfg,
		Router: router,
		Client: upstream.NewClient(cfg),
		OAuth:  manager,
		Pool:   pool,
		Logger: logger,
	}
	engine := gin.New()
	engine.POST("/v1/responses", api.Responses)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"m","input":"hi"}`))
	engine.ServeHTTP(w, req)

	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d body=%s", w.Code, w.Body.String())
	}

	// Response body may contain the upstream error body (relayed as-is).
	// But access tokens, refresh tokens, auth headers etc. must not leak.
	body := w.Body.String()
	for _, s := range []string{sentinelAccessToken, sentinelRefreshToken, sentinelIDToken, sentinelAccountID, sentinelAuthHeader} {
		if strings.Contains(body, s) {
			t.Fatalf("429 response leaked sentinel %q:\n%s", s, body)
		}
	}

	assertNoSentinels(t, logs.String(), "429 failover exhaustion logs")
}

// Test5xxFailoverResponse_SecretSafe verifies that 5xx failover responses
// do not leak sentinel token secrets.
func Test5xxFailoverResponse_SecretSafe(t *testing.T) {
	gin.SetMode(gin.TestMode)
	authDir := t.TempDir()

	cfg := &config.Config{
		OAuth: config.OAuth{
			AuthDir: authDir,
			LoadBalancing: config.LoadBalancing{
				Strategy:          config.LoadBalancingFillFirst,
				MaxFailovers:      1,
				RateLimitCooldown: 60 * time.Second,
				ErrorCooldown:     30 * time.Second,
			},
		},
		Upstream: config.Upstream{HTTPTimeout: 5 * time.Second},
		Models: []*config.Model{{
			Alias:            "m",
			FactoryProvider:  config.FactoryProviderOpenAI,
			UpstreamProtocol: config.UpstreamCodexResponses,
			OAuthProvider:    config.OAuthProviderCodex,
			BaseURL:          "http://127.0.0.1:1",
		}},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"internal error"}}`))
	}))
	defer srv.Close()
	cfg.Models[0].BaseURL = srv.URL

	manager := oauth.NewManager(cfg)
	for _, email := range []string{"a@example.com", "b@example.com"} {
		tok := newSentinelToken(email)
		manager.SaveToken(tok)
	}

	tokens, _ := manager.LoadTokens(config.OAuthProviderCodex)
	pool := oauth.NewAccountPool(tokens, time.Now, oauth.TestPoolLB(), nil, oauth.NewSelector(config.LoadBalancingFillFirst))

	var logs bytes.Buffer
	logger := logrus.New()
	logger.SetOutput(&logs)

	router, _ := upstream.NewRouter(cfg.Models)
	api := &API{
		Cfg:    cfg,
		Router: router,
		Client: upstream.NewClient(cfg),
		OAuth:  manager,
		Pool:   pool,
		Logger: logger,
	}
	engine := gin.New()
	engine.POST("/v1/responses", api.Responses)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"m","input":"hi"}`))
	engine.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d body=%s", w.Code, w.Body.String())
	}

	body := w.Body.String()
	for _, s := range []string{sentinelAccessToken, sentinelRefreshToken, sentinelIDToken, sentinelAccountID, sentinelAuthHeader} {
		if strings.Contains(body, s) {
			t.Fatalf("5xx response leaked sentinel %q:\n%s", s, body)
		}
	}

	assertNoSentinels(t, logs.String(), "5xx failover logs")
}

// TestNoEligibleAccounts_ErrorSafeWithSentinelPool verifies that the
// no-eligible-accounts error and pool snapshot do not include sentinel secrets.
func TestNoEligibleAccounts_ErrorSafeWithSentinelPool(t *testing.T) {
	tok := newSentinelToken("user@example.com")
	tok.Disabled = true
	// Assign a path using the private field workaround: save to temp dir.
	authDir := t.TempDir()
	cfg := &config.Config{OAuth: config.OAuth{AuthDir: authDir}}
	mgr := oauth.NewManager(cfg)
	path, err := mgr.SaveToken(tok)
	if err != nil {
		t.Fatal(err)
	}

	loaded, err := mgr.LoadTokenAtPath(path)
	if err != nil {
		t.Fatal(err)
	}
	loaded.Disabled = true

	pool := oauth.NewAccountPool([]*oauth.Token{loaded}, time.Now, oauth.TestPoolLB(), nil)

	_, err = pool.Select("", nil, "")
	if err == nil {
		t.Fatal("expected error for disabled-only pool")
	}

	assertNoSentinels(t, err.Error(), "pool selection error")

	snap := pool.Snapshot()
	snapJSON, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	assertNoSentinels(t, string(snapJSON), "pool snapshot JSON")
}

// TestTraceLogging_PoolHealthPathDoesNotLeakSentinels verifies that trace
// logging with a pool health request does not leak sentinel secrets when
// the request includes sentinel query parameters.
func TestTraceLogging_PoolHealthPathDoesNotLeakSentinels(t *testing.T) {
	gin.SetMode(gin.TestMode)
	authDir := t.TempDir()

	tok := newSentinelToken("trace@example.com")
	raw, _ := json.MarshalIndent(tok, "", "  ")
	raw = append(raw, '\n')
	os.WriteFile(filepath.Join(authDir, "codex-trace.json"), raw, 0o600)

	cfg := &config.Config{
		OAuth: config.OAuth{AuthDir: authDir},
		Models: []*config.Model{{
			Alias:            "m",
			FactoryProvider:  config.FactoryProviderOpenAI,
			UpstreamProtocol: config.UpstreamCodexResponses,
			OAuthProvider:    config.OAuthProviderCodex,
			BaseURL:          "http://127.0.0.1:1",
		}},
	}

	manager := oauth.NewManager(cfg)
	tokens, _ := manager.LoadTokens(config.OAuthProviderCodex)
	pool := oauth.NewAccountPool(tokens, time.Now, oauth.TestPoolLB(), nil)

	var logs bytes.Buffer
	logger := logrus.New()
	logger.SetOutput(&logs)
	logger.SetLevel(logrus.DebugLevel)

	api := &API{
		Cfg:    cfg,
		OAuth:  manager,
		Pool:   pool,
		Logger: logger,
	}
	engine := gin.New()
	engine.GET("/v1/oauth/pool-health", api.PoolHealth)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/oauth/pool-health?token="+sentinelAccessToken, nil)
	engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	assertNoSentinels(t, w.Body.String(), "trace pool-health response")
	assertNoSentinels(t, logs.String(), "trace pool-health logs")
}

// TestTraceLogging_FailoverPathDoesNotLeakSentinels verifies that trace
// logging during a failover flow does not leak sentinel secrets.
func TestTraceLogging_FailoverPathDoesNotLeakSentinels(t *testing.T) {
	gin.SetMode(gin.TestMode)
	authDir := t.TempDir()

	cfg := &config.Config{
		OAuth: config.OAuth{
			AuthDir: authDir,
			LoadBalancing: config.LoadBalancing{
				Strategy:          config.LoadBalancingFillFirst,
				MaxFailovers:      1,
				RateLimitCooldown: 60 * time.Second,
				ErrorCooldown:     30 * time.Second,
			},
		},
		Upstream: config.Upstream{HTTPTimeout: 5 * time.Second},
		Models: []*config.Model{{
			Alias:            "m",
			FactoryProvider:  config.FactoryProviderOpenAI,
			UpstreamProtocol: config.UpstreamCodexResponses,
			OAuthProvider:    config.OAuthProviderCodex,
			BaseURL:          "http://127.0.0.1:1",
		}},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limited","type":"rate_limit_exceeded"}}`))
	}))
	defer srv.Close()
	cfg.Models[0].BaseURL = srv.URL

	manager := oauth.NewManager(cfg)
	for _, email := range []string{"a@example.com", "b@example.com"} {
		tok := newSentinelToken(email)
		manager.SaveToken(tok)
	}

	tokens, _ := manager.LoadTokens(config.OAuthProviderCodex)
	pool := oauth.NewAccountPool(tokens, time.Now, oauth.TestPoolLB(), nil, oauth.NewSelector(config.LoadBalancingFillFirst))

	var logs bytes.Buffer
	logger := logrus.New()
	logger.SetOutput(&logs)
	logger.SetLevel(logrus.DebugLevel)

	router, _ := upstream.NewRouter(cfg.Models)
	api := &API{
		Cfg:    cfg,
		Router: router,
		Client: upstream.NewClient(cfg),
		OAuth:  manager,
		Pool:   pool,
		Logger: logger,
	}
	engine := gin.New()
	engine.POST("/v1/responses", api.Responses)

	reqBody := `{"model":"m","input":"hi","apiKey":"` + sentinelAccessToken + `"}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses?access_token="+sentinelAccessToken, strings.NewReader(reqBody))
	req.Header.Set("Authorization", sentinelAuthHeader)
	engine.ServeHTTP(w, req)

	assertNoSentinels(t, logs.String(), "failover trace logs")
}

// TestJSONLogFormat_PoolOperationsDoNotLeak verifies that JSON-format
// logging in pool/watcher operations does not leak sentinel secrets.
func TestJSONLogFormat_PoolOperationsDoNotLeak(t *testing.T) {
	dir := t.TempDir()

	invalidContent := fmt.Sprintf(`{"type":"codex","access_token":"%s"`, sentinelAccessToken)
	if err := os.WriteFile(filepath.Join(dir, "bad.json"), []byte(invalidContent), 0o600); err != nil {
		t.Fatal(err)
	}

	var logs bytes.Buffer
	logger := logrus.New()
	logger.SetOutput(&logs)
	logger.SetFormatter(&logrus.JSONFormatter{TimestampFormat: time.RFC3339})

	cfg := &config.Config{OAuth: config.OAuth{AuthDir: dir}}
	mgr := oauth.NewManager(cfg)

	tokens, err := oauth.LoadCodexTokensFromDir(mgr, dir, logger)
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 0 {
		t.Fatalf("expected 0 tokens from invalid file, got %d", len(tokens))
	}

	logOutput := logs.String()
	assertNoSentinels(t, logOutput, "JSON format watcher log")

	// Verify it's valid JSON
	if logOutput != "" {
		for _, line := range strings.Split(strings.TrimSpace(logOutput), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			if !json.Valid([]byte(line)) {
				t.Fatalf("invalid JSON log line: %s", line)
			}
		}
	}
}

// TestTextLogFormat_PoolOperationsDoNotLeak verifies that text-format
// logging in pool/watcher operations does not leak sentinel secrets.
func TestTextLogFormat_PoolOperationsDoNotLeak(t *testing.T) {
	dir := t.TempDir()

	invalidContent := fmt.Sprintf(`{"type":"codex","access_token":"%s"`, sentinelAccessToken)
	if err := os.WriteFile(filepath.Join(dir, "bad.json"), []byte(invalidContent), 0o600); err != nil {
		t.Fatal(err)
	}

	var logs bytes.Buffer
	logger := logrus.New()
	logger.SetOutput(&logs)
	logger.SetFormatter(&logrus.TextFormatter{FullTimestamp: true})

	cfg := &config.Config{OAuth: config.OAuth{AuthDir: dir}}
	mgr := oauth.NewManager(cfg)

	tokens, err := oauth.LoadCodexTokensFromDir(mgr, dir, logger)
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 0 {
		t.Fatalf("expected 0 tokens from invalid file, got %d", len(tokens))
	}

	logOutput := logs.String()
	assertNoSentinels(t, logOutput, "text format watcher log")
}

// TestRefreshFailure_ErrorSafeWithSentinelToken verifies that a refresh
// failure error message and response do not include sentinel token secrets.
func TestRefreshFailure_ErrorSafeWithSentinelToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	authDir := t.TempDir()

	tok := &oauth.Token{
		Type:         string(config.OAuthProviderCodex),
		AccessToken:  sentinelAccessToken,
		RefreshToken: sentinelRefreshToken,
		IDToken:      sentinelIDToken,
		Email:        "expired@example.com",
		AccountID:    sentinelAccountID,
		Expired:      time.Now().Add(-time.Hour).UTC().Format(time.RFC3339),
	}

	cfg := &config.Config{
		OAuth: config.OAuth{
			AuthDir: authDir,
			LoadBalancing: config.LoadBalancing{
				Strategy:          config.LoadBalancingFillFirst,
				MaxFailovers:      0,
				RateLimitCooldown: 60 * time.Second,
				ErrorCooldown:     30 * time.Second,
			},
		},
		Upstream: config.Upstream{HTTPTimeout: 5 * time.Second},
		Models: []*config.Model{{
			Alias:            "m",
			FactoryProvider:  config.FactoryProviderOpenAI,
			UpstreamProtocol: config.UpstreamCodexResponses,
			OAuthProvider:    config.OAuthProviderCodex,
			BaseURL:          "http://127.0.0.1:1",
		}},
	}

	manager := oauth.NewManager(cfg)
	manager.SaveToken(tok)

	tokens, _ := manager.LoadTokens(config.OAuthProviderCodex)
	pool := oauth.NewAccountPool(tokens, time.Now, oauth.TestPoolLB(), nil, oauth.NewSelector(config.LoadBalancingFillFirst))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintln(w, "event: response.completed")
		fmt.Fprintln(w, `data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[]}}`)
		fmt.Fprintln(w)
	}))
	defer srv.Close()
	cfg.Models[0].BaseURL = srv.URL

	var logs bytes.Buffer
	logger := logrus.New()
	logger.SetOutput(&logs)

	router, _ := upstream.NewRouter(cfg.Models)
	api := &API{
		Cfg:    cfg,
		Router: router,
		Client: upstream.NewClient(cfg),
		OAuth:  manager,
		Pool:   pool,
		Logger: logger,
	}
	engine := gin.New()
	engine.POST("/v1/responses", api.Responses)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"m","input":"hi"}`))
	engine.ServeHTTP(w, req)

	assertNoSentinels(t, w.Body.String(), "refresh failure response")
	assertNoSentinels(t, logs.String(), "refresh failure logs")
}

// Ensure io.Discard is used
var _ = io.Discard
