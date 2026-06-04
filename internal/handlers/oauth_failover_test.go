package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"

	"droid-proxy/internal/config"
	"droid-proxy/internal/oauth"
	"droid-proxy/internal/upstream"
)

// failoverTestAccount describes a Codex account for failover tests.
type failoverTestAccount struct {
	email     string
	accountID string
	disabled  bool
}

// failoverTestOptions configures a failover test API.
type failoverTestOptions struct {
	maxFailovers      int
	rateLimitCooldown time.Duration
	errorCooldown     time.Duration
	strategy          config.LoadBalancingStrategy
	accounts          []failoverTestAccount
	upstreamHandler   http.HandlerFunc
	pinnedAccount     string
}

// newCodexFailoverTestAPI creates a test API with a Codex account pool
// configured for failover testing using fake upstreams and temp token files.
func newCodexFailoverTestAPI(t *testing.T, opts failoverTestOptions) *testAPI {
	t.Helper()
	gin.SetMode(gin.TestMode)
	srv := httptest.NewServer(opts.upstreamHandler)
	t.Cleanup(srv.Close)

	authDir := t.TempDir()
	strategy := opts.strategy
	if strategy == "" {
		strategy = config.LoadBalancingFillFirst
	}
	rlc := opts.rateLimitCooldown
	if rlc == 0 {
		rlc = 60 * time.Second
	}
	ec := opts.errorCooldown
	if ec == 0 {
		ec = 30 * time.Second
	}

	cfg := &config.Config{
		OAuth: config.OAuth{
			AuthDir: authDir,
			LoadBalancing: config.LoadBalancing{
				Strategy:          strategy,
				MaxFailovers:      opts.maxFailovers,
				RateLimitCooldown: rlc,
				ErrorCooldown:     ec,
			},
		},
		Upstream: config.Upstream{
			HTTPTimeout:     5 * time.Second,
			StreamKeepAlive: 200 * time.Millisecond,
		},
		Models: []*config.Model{{
			Alias:            "droid-oauth",
			DisplayName:      "OAuth Test",
			FactoryProvider:  config.FactoryProviderOpenAI,
			UpstreamProtocol: config.UpstreamCodexResponses,
			OAuthProvider:    config.OAuthProviderCodex,
			BaseURL:          srv.URL,
			UpstreamModel:    "codex-upstream",
			OAuthAccount:     opts.pinnedAccount,
		}},
	}

	manager := oauth.NewManager(cfg)
	for i, acct := range opts.accounts {
		token := &oauth.Token{
			Type:         string(config.OAuthProviderCodex),
			AccessToken:  fmt.Sprintf("access-token-%d", i),
			RefreshToken: fmt.Sprintf("refresh-token-%d", i),
			Email:        acct.email,
			AccountID:    acct.accountID,
			Disabled:     acct.disabled,
			Expired:      time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		}
		if _, err := manager.SaveToken(token); err != nil {
			t.Fatal(err)
		}
	}

	tokens, err := manager.LoadTokens(config.OAuthProviderCodex)
	if err != nil {
		t.Fatal(err)
	}
	sel := oauth.NewSelector(strategy)
	pool := oauth.NewAccountPool(tokens, time.Now, sel)

	router, err := upstream.NewRouter(cfg.Models)
	if err != nil {
		t.Fatal(err)
	}
	logger := logrus.New()
	logger.SetOutput(io.Discard)

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
	engine.POST("/responses", api.Responses)
	return &testAPI{api: api, upstream: srv, engine: engine}
}

// codexSuccessResponse writes a minimal successful Codex SSE completion.
func codexSuccessResponse(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	fmt.Fprintln(w, "event: response.completed")
	fmt.Fprintln(w, `data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[]}}`)
	fmt.Fprintln(w)
}

// authTokenFromRequest extracts the bearer token from an HTTP request.
func authTokenFromRequest(r *http.Request) string {
	return strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
}

// --------------- VAL-FAILOVER-001: Codex 429 failover is bounded and replay-safe ---------------

func TestResponsesCodexFailover429BoundedReplay(t *testing.T) {
	var attempts []string
	var capturedBodies [][]byte
	var mu sync.Mutex

	api := newCodexFailoverTestAPI(t, failoverTestOptions{
		maxFailovers: 2,
		accounts: []failoverTestAccount{
			{email: "a@test.com", accountID: "acct_a"},
			{email: "b@test.com", accountID: "acct_b"},
			{email: "c@test.com", accountID: "acct_c"},
		},
		upstreamHandler: func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			mu.Lock()
			attempts = append(attempts, authTokenFromRequest(r))
			capturedBodies = append(capturedBodies, body)
			mu.Unlock()

			auth := authTokenFromRequest(r)
			if auth == "access-token-2" {
				codexSuccessResponse(w)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"rate limited","type":"rate_limit_exceeded"}}`))
		},
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"droid-oauth","input":"hi"}`))
	api.engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 after failover, got %d body=%s", w.Code, w.Body.String())
	}
	if len(attempts) != 3 {
		t.Fatalf("expected 3 upstream attempts, got %d", len(attempts))
	}
	// Verify each attempt used a different account token.
	if attempts[0] == attempts[1] || attempts[1] == attempts[2] || attempts[0] == attempts[2] {
		t.Fatalf("expected distinct account tokens per attempt, got %v", attempts)
	}
	// Verify payload is replay-safe: all request bodies should be identical.
	for i := 1; i < len(capturedBodies); i++ {
		if string(capturedBodies[i]) != string(capturedBodies[0]) {
			t.Fatalf("payload mismatch between attempt 0 and %d:\n  %s\n  %s", i, capturedBodies[0], capturedBodies[i])
		}
	}
}

// --------------- VAL-FAILOVER-003: 5xx and transport timeout use error cooldown and alternate account ---------------

func TestResponsesCodexFailover5xxCooldownAndAlternate(t *testing.T) {
	var attemptCount int32

	api := newCodexFailoverTestAPI(t, failoverTestOptions{
		maxFailovers: 1,
		accounts: []failoverTestAccount{
			{email: "a@test.com", accountID: "acct_a"},
			{email: "b@test.com", accountID: "acct_b"},
		},
		upstreamHandler: func(w http.ResponseWriter, r *http.Request) {
			n := int(attemptCount)
			attemptCount++
			if n == 0 {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"error":{"message":"internal error"}}`))
				return
			}
			codexSuccessResponse(w)
		},
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"droid-oauth","input":"hi"}`))
	api.engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 after 5xx failover, got %d body=%s", w.Code, w.Body.String())
	}
	if atomic.LoadInt32(&attemptCount) != 2 {
		t.Fatalf("expected 2 upstream attempts, got %d", attemptCount)
	}

	// Verify first account is in cooldown.
	snap := api.api.Pool.Snapshot()
	for _, acct := range snap.Accounts {
		if acct.Selector == "a@test.com" {
			if acct.CooldownUntil == nil {
				t.Fatal("expected cooldown on first account")
			}
		}
	}
}

func TestResponsesCodexFailoverTransportErrorCooldown(t *testing.T) {
	var attemptCount int32

	// Create a server that immediately closes the connection on the first attempt.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(attemptCount)
		attemptCount++
		if n == 0 {
			// Force close the connection to simulate a transport error.
			hj, ok := w.(http.Hijacker)
			if ok {
				conn, _, _ := hj.Hijack()
				_ = conn.Close()
				return
			}
		}
		codexSuccessResponse(w)
	}))
	defer srv.Close()

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
		Upstream: config.Upstream{
			HTTPTimeout:     5 * time.Second,
			StreamKeepAlive: 200 * time.Millisecond,
		},
		Models: []*config.Model{{
			Alias:            "droid-oauth",
			DisplayName:      "OAuth Test",
			FactoryProvider:  config.FactoryProviderOpenAI,
			UpstreamProtocol: config.UpstreamCodexResponses,
			OAuthProvider:    config.OAuthProviderCodex,
			BaseURL:          srv.URL,
			UpstreamModel:    "codex-upstream",
		}},
	}

	manager := oauth.NewManager(cfg)
	for i, email := range []string{"a@test.com", "b@test.com"} {
		token := &oauth.Token{
			Type:         string(config.OAuthProviderCodex),
			AccessToken:  fmt.Sprintf("access-token-%d", i),
			RefreshToken: fmt.Sprintf("refresh-token-%d", i),
			Email:        email,
			AccountID:    fmt.Sprintf("acct_%d", i),
			Expired:      time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		}
		if _, err := manager.SaveToken(token); err != nil {
			t.Fatal(err)
		}
	}
	tokens, _ := manager.LoadTokens(config.OAuthProviderCodex)
	pool := oauth.NewAccountPool(tokens, time.Now, oauth.NewSelector(config.LoadBalancingFillFirst))
	router, _ := upstream.NewRouter(cfg.Models)
	logger := logrus.New()
	logger.SetOutput(io.Discard)

	api := &API{Cfg: cfg, Router: router, Client: upstream.NewClient(cfg), OAuth: manager, Pool: pool, Logger: logger}
	engine := gin.New()
	engine.POST("/v1/responses", api.Responses)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"droid-oauth","input":"hi"}`))
	engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 after transport error failover, got %d body=%s", w.Code, w.Body.String())
	}
	if atomic.LoadInt32(&attemptCount) != 2 {
		t.Fatalf("expected 2 upstream attempts, got %d", attemptCount)
	}
}

// --------------- VAL-FAILOVER-005: Non-retryable 4xx does not fail over ---------------

func TestResponsesCodexFailoverNonRetryable4xxDoesNotFailover(t *testing.T) {
	var attemptCount int32

	api := newCodexFailoverTestAPI(t, failoverTestOptions{
		maxFailovers: 2,
		accounts: []failoverTestAccount{
			{email: "a@test.com", accountID: "acct_a"},
			{email: "b@test.com", accountID: "acct_b"},
			{email: "c@test.com", accountID: "acct_c"},
		},
		upstreamHandler: func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&attemptCount, 1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"bad request","type":"invalid_request_error"}}`))
		},
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"droid-oauth","input":"hi"}`))
	api.engine.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "bad request") {
		t.Fatalf("expected upstream error body preserved, got %s", w.Body.String())
	}
	if atomic.LoadInt32(&attemptCount) != 1 {
		t.Fatalf("non-retryable 4xx should not fail over, got %d attempts", attemptCount)
	}
}

func TestResponsesCodexFailover422DoesNotFailover(t *testing.T) {
	var attemptCount int32

	api := newCodexFailoverTestAPI(t, failoverTestOptions{
		maxFailovers: 2,
		accounts: []failoverTestAccount{
			{email: "a@test.com", accountID: "acct_a"},
			{email: "b@test.com", accountID: "acct_b"},
		},
		upstreamHandler: func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&attemptCount, 1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"error":{"message":"unprocessable","type":"invalid_request_error"}}`))
		},
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"droid-oauth","input":"hi"}`))
	api.engine.ServeHTTP(w, req)

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d body=%s", w.Code, w.Body.String())
	}
	if atomic.LoadInt32(&attemptCount) != 1 {
		t.Fatalf("422 should not fail over, got %d attempts", attemptCount)
	}
}

// --------------- VAL-FAILOVER-006: Exhaustion relays the last upstream error ---------------

func TestResponsesCodexFailoverExhaustionRelaysLastError(t *testing.T) {
	var attemptCount int32

	api := newCodexFailoverTestAPI(t, failoverTestOptions{
		maxFailovers: 1,
		accounts: []failoverTestAccount{
			{email: "a@test.com", accountID: "acct_a"},
			{email: "b@test.com", accountID: "acct_b"},
		},
		upstreamHandler: func(w http.ResponseWriter, r *http.Request) {
			n := atomic.AddInt32(&attemptCount, 1)
			w.Header().Set("Content-Type", "application/json")
			if n == 1 {
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"error":{"message":"first rate limited","type":"rate_limit_exceeded"}}`))
			} else {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte(`{"error":{"message":"second unavailable","type":"server_error"}}`))
			}
		},
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"droid-oauth","input":"hi"}`))
	api.engine.ServeHTTP(w, req)

	// Should relay the LAST upstream error (503 from second attempt).
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 (last error), got %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "second unavailable") {
		t.Fatalf("expected last error body relayed, got %s", w.Body.String())
	}
	if atomic.LoadInt32(&attemptCount) != 2 {
		t.Fatalf("expected 2 attempts, got %d", attemptCount)
	}
}

func TestResponsesCodexFailoverExhaustion5xxRelaysLastError(t *testing.T) {
	var attemptCount int32

	api := newCodexFailoverTestAPI(t, failoverTestOptions{
		maxFailovers: 1,
		accounts: []failoverTestAccount{
			{email: "a@test.com", accountID: "acct_a"},
			{email: "b@test.com", accountID: "acct_b"},
		},
		upstreamHandler: func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&attemptCount, 1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"error":{"message":"bad gateway","type":"server_error"}}`))
		},
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"droid-oauth","input":"hi"}`))
	api.engine.ServeHTTP(w, req)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected 502 (last error), got %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "bad gateway") {
		t.Fatalf("expected last error body relayed, got %s", w.Body.String())
	}
}

// --------------- VAL-FAILOVER-010: Eligibility excludes invalid candidates ---------------

func TestResponsesCodexFailoverExcludesDisabledAccounts(t *testing.T) {
	var attemptCount int32

	api := newCodexFailoverTestAPI(t, failoverTestOptions{
		maxFailovers: 1,
		accounts: []failoverTestAccount{
			{email: "a@test.com", accountID: "acct_a", disabled: true},
			{email: "b@test.com", accountID: "acct_b"},
		},
		upstreamHandler: func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&attemptCount, 1)
			codexSuccessResponse(w)
		},
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"droid-oauth","input":"hi"}`))
	api.engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	if atomic.LoadInt32(&attemptCount) != 1 {
		t.Fatalf("expected 1 attempt (only non-disabled account), got %d", attemptCount)
	}
}

func TestResponsesCodexFailoverExcludesNonCodexTokens(t *testing.T) {
	// Verify that xAI tokens are not included in the Codex pool.
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
		Upstream: config.Upstream{
			HTTPTimeout:     5 * time.Second,
			StreamKeepAlive: 200 * time.Millisecond,
		},
		Models: []*config.Model{{
			Alias:            "droid-oauth",
			DisplayName:      "OAuth Test",
			FactoryProvider:  config.FactoryProviderOpenAI,
			UpstreamProtocol: config.UpstreamCodexResponses,
			OAuthProvider:    config.OAuthProviderCodex,
			BaseURL:          "http://unused",
			UpstreamModel:    "codex-upstream",
		}},
	}

	manager := oauth.NewManager(cfg)
	// Save a Codex token and an xAI token.
	codexToken := &oauth.Token{
		Type:        string(config.OAuthProviderCodex),
		AccessToken: "codex-access",
		Email:       "codex@test.com",
		Expired:     time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
	}
	if _, err := manager.SaveToken(codexToken); err != nil {
		t.Fatal(err)
	}
	xaiToken := &oauth.Token{
		Type:        string(config.OAuthProviderXAI),
		AccessToken: "xai-access",
		Email:       "xai@test.com",
		Expired:     time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
	}
	if _, err := manager.SaveToken(xaiToken); err != nil {
		t.Fatal(err)
	}

	tokens, _ := manager.LoadTokens(config.OAuthProviderCodex)
	pool := oauth.NewAccountPool(tokens, time.Now, oauth.NewSelector(config.LoadBalancingFillFirst))
	snap := pool.Snapshot()

	if len(snap.Accounts) != 1 {
		t.Fatalf("expected 1 Codex account in pool (xAI excluded), got %d", len(snap.Accounts))
	}
	if snap.Accounts[0].Selector != "codex@test.com" {
		t.Fatalf("expected codex account, got %s", snap.Accounts[0].Selector)
	}
}

func TestResponsesCodexFailoverExcludesAlreadyTriedAccounts(t *testing.T) {
	// Verify that accounts already tried in the failover loop are excluded.
	var mu sync.Mutex
	var seenTokens []string

	api := newCodexFailoverTestAPI(t, failoverTestOptions{
		maxFailovers: 1,
		accounts: []failoverTestAccount{
			{email: "a@test.com", accountID: "acct_a"},
			{email: "b@test.com", accountID: "acct_b"},
		},
		upstreamHandler: func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			seenTokens = append(seenTokens, authTokenFromRequest(r))
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"rate limited"}}`))
		},
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"droid-oauth","input":"hi"}`))
	api.engine.ServeHTTP(w, req)

	// Both accounts should be tried, no account tried twice.
	if len(seenTokens) != 2 {
		t.Fatalf("expected 2 attempts, got %d", len(seenTokens))
	}
	if seenTokens[0] == seenTokens[1] {
		t.Fatalf("same account was tried twice: %v", seenTokens)
	}
}

func TestResponsesCodexFailoverPinnedAccountExclusion(t *testing.T) {
	// Pinned accounts that don't match should be excluded.
	var attemptCount int32

	api := newCodexFailoverTestAPI(t, failoverTestOptions{
		maxFailovers:  1,
		pinnedAccount: "nonexistent@test.com",
		accounts: []failoverTestAccount{
			{email: "a@test.com", accountID: "acct_a"},
			{email: "b@test.com", accountID: "acct_b"},
		},
		upstreamHandler: func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&attemptCount, 1)
			codexSuccessResponse(w)
		},
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"droid-oauth","input":"hi"}`))
	api.engine.ServeHTTP(w, req)

	// No account matches the pin, so we should get a safe error.
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 for pinned-out accounts, got %d body=%s", w.Code, w.Body.String())
	}
	if atomic.LoadInt32(&attemptCount) != 0 {
		t.Fatalf("expected 0 upstream attempts (pinned out), got %d", attemptCount)
	}
	// Verify no secrets in response.
	if strings.Contains(w.Body.String(), "access-token") || strings.Contains(w.Body.String(), "token") {
		t.Fatalf("response should not contain secrets: %s", w.Body.String())
	}
}

// --------------- VAL-FAILOVER-013: Failover budget semantics are explicit ---------------

func TestResponsesCodexFailoverBudgetZeroMakesOneAttempt(t *testing.T) {
	var attemptCount int32

	api := newCodexFailoverTestAPI(t, failoverTestOptions{
		maxFailovers: 0,
		accounts: []failoverTestAccount{
			{email: "a@test.com", accountID: "acct_a"},
			{email: "b@test.com", accountID: "acct_b"},
		},
		upstreamHandler: func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&attemptCount, 1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"rate limited"}}`))
		},
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"droid-oauth","input":"hi"}`))
	api.engine.ServeHTTP(w, req)

	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d body=%s", w.Code, w.Body.String())
	}
	if atomic.LoadInt32(&attemptCount) != 1 {
		t.Fatalf("max_failovers=0 should make exactly 1 attempt, got %d", attemptCount)
	}
}

func TestResponsesCodexFailoverBudgetOneMakesTwoAttempts(t *testing.T) {
	var attemptCount int32

	api := newCodexFailoverTestAPI(t, failoverTestOptions{
		maxFailovers: 1,
		accounts: []failoverTestAccount{
			{email: "a@test.com", accountID: "acct_a"},
			{email: "b@test.com", accountID: "acct_b"},
		},
		upstreamHandler: func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&attemptCount, 1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"rate limited"}}`))
		},
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"droid-oauth","input":"hi"}`))
	api.engine.ServeHTTP(w, req)

	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d body=%s", w.Code, w.Body.String())
	}
	if atomic.LoadInt32(&attemptCount) != 2 {
		t.Fatalf("max_failovers=1 should make at most 2 attempts, got %d", attemptCount)
	}
}

func TestResponsesCodexFailoverBudgetTwoMakesThreeAttempts(t *testing.T) {
	var attemptCount int32

	api := newCodexFailoverTestAPI(t, failoverTestOptions{
		maxFailovers: 2,
		accounts: []failoverTestAccount{
			{email: "a@test.com", accountID: "acct_a"},
			{email: "b@test.com", accountID: "acct_b"},
			{email: "c@test.com", accountID: "acct_c"},
		},
		upstreamHandler: func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&attemptCount, 1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"rate limited"}}`))
		},
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"droid-oauth","input":"hi"}`))
	api.engine.ServeHTTP(w, req)

	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d body=%s", w.Code, w.Body.String())
	}
	if atomic.LoadInt32(&attemptCount) != 3 {
		t.Fatalf("max_failovers=2 should make at most 3 attempts, got %d", attemptCount)
	}
}

func TestResponsesCodexFailoverMoreAccountsThanBudget(t *testing.T) {
	var attemptCount int32

	api := newCodexFailoverTestAPI(t, failoverTestOptions{
		maxFailovers: 1, // budget allows 2 attempts total
		accounts: []failoverTestAccount{
			{email: "a@test.com", accountID: "acct_a"},
			{email: "b@test.com", accountID: "acct_b"},
			{email: "c@test.com", accountID: "acct_c"},
		},
		upstreamHandler: func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&attemptCount, 1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"rate limited"}}`))
		},
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"droid-oauth","input":"hi"}`))
	api.engine.ServeHTTP(w, req)

	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d body=%s", w.Code, w.Body.String())
	}
	// Only 2 attempts despite 3 accounts, because budget is 1 failover.
	if atomic.LoadInt32(&attemptCount) != 2 {
		t.Fatalf("more accounts than budget should not exceed budget, got %d attempts", attemptCount)
	}
}

// --------------- VAL-FAILOVER-014: No-eligible-account errors are deterministic and safe ---------------

func TestResponsesCodexFailoverNoEligibleAccountsSafe(t *testing.T) {
	api := newCodexFailoverTestAPI(t, failoverTestOptions{
		maxFailovers: 1,
		accounts: []failoverTestAccount{
			{email: "a@test.com", accountID: "acct_a", disabled: true},
			{email: "b@test.com", accountID: "acct_b", disabled: true},
		},
		upstreamHandler: func(w http.ResponseWriter, r *http.Request) {
			t.Fatal("should not reach upstream with all accounts disabled")
		},
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"droid-oauth","input":"hi"}`))
	api.engine.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "no eligible codex accounts available") {
		t.Fatalf("expected safe error message, got %s", body)
	}
	// Verify no secrets or file paths in response.
	if strings.Contains(body, "access-token") || strings.Contains(body, "refresh-token") || strings.Contains(body, ".json") {
		t.Fatalf("response should not contain secrets or paths: %s", body)
	}
}

func TestResponsesCodexFailoverEmptyPoolSafe(t *testing.T) {
	api := newCodexFailoverTestAPI(t, failoverTestOptions{
		maxFailovers: 1,
		accounts:     []failoverTestAccount{}, // empty pool
		upstreamHandler: func(w http.ResponseWriter, r *http.Request) {
			t.Fatal("should not reach upstream with empty pool")
		},
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"droid-oauth","input":"hi"}`))
	api.engine.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "no eligible codex accounts available") {
		t.Fatalf("expected safe error message, got %s", body)
	}
}

// --------------- VAL-CROSS-006: Empty or unusable pools preserve startup and safe request errors ---------------

func TestResponsesCodexFailoverNoMutationOnNoEligible(t *testing.T) {
	api := newCodexFailoverTestAPI(t, failoverTestOptions{
		maxFailovers: 1,
		accounts: []failoverTestAccount{
			{email: "a@test.com", accountID: "acct_a", disabled: true},
		},
		upstreamHandler: func(w http.ResponseWriter, r *http.Request) {
			t.Fatal("should not reach upstream")
		},
	})

	// Capture pool state before the request.
	snapBefore := api.api.Pool.Snapshot()

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"droid-oauth","input":"hi"}`))
	api.engine.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d body=%s", w.Code, w.Body.String())
	}

	// Verify pool state was not mutated.
	snapAfter := api.api.Pool.Snapshot()
	if len(snapAfter.Accounts) != len(snapBefore.Accounts) {
		t.Fatalf("pool was mutated: before=%d after=%d accounts", len(snapBefore.Accounts), len(snapAfter.Accounts))
	}
	for i, before := range snapBefore.Accounts {
		after := snapAfter.Accounts[i]
		if after.InFlight != before.InFlight {
			t.Fatalf("in_flight was mutated for %s: before=%d after=%d", after.Selector, before.InFlight, after.InFlight)
		}
	}
}

// --------------- xAI remains unchanged ---------------

func TestResponsesCodexFailoverXAIUsesSingleTokenPath(t *testing.T) {
	// Verify that xAI requests still use the single-token path and do not
	// use the pool even when a pool is configured.
	var attemptCount int32

	authDir := t.TempDir()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attemptCount, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limited"}}`))
	}))
	defer srv.Close()

	cfg := &config.Config{
		OAuth: config.OAuth{
			AuthDir: authDir,
			LoadBalancing: config.LoadBalancing{
				Strategy:          config.LoadBalancingFillFirst,
				MaxFailovers:      2,
				RateLimitCooldown: 60 * time.Second,
				ErrorCooldown:     30 * time.Second,
			},
		},
		Upstream: config.Upstream{
			HTTPTimeout:     5 * time.Second,
			StreamKeepAlive: 200 * time.Millisecond,
		},
		Models: []*config.Model{{
			Alias:            "droid-oauth",
			DisplayName:      "OAuth Test",
			FactoryProvider:  config.FactoryProviderOpenAI,
			UpstreamProtocol: config.UpstreamXAIResponses,
			OAuthProvider:    config.OAuthProviderXAI,
			BaseURL:          srv.URL,
			UpstreamModel:    "xai-upstream",
		}},
	}

	manager := oauth.NewManager(cfg)
	token := &oauth.Token{
		Type:         string(config.OAuthProviderXAI),
		AccessToken:  "xai-access-token",
		RefreshToken: "xai-refresh-token",
		Email:        "xai@test.com",
		Expired:      time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
	}
	if _, err := manager.SaveToken(token); err != nil {
		t.Fatal(err)
	}

	// Create a pool (even though xAI shouldn't use it).
	codexToken := &oauth.Token{
		Type:        string(config.OAuthProviderCodex),
		AccessToken: "codex-access-token",
		Email:       "codex@test.com",
		Expired:     time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
	}
	if _, err := manager.SaveToken(codexToken); err != nil {
		t.Fatal(err)
	}
	tokens, _ := manager.LoadTokens(config.OAuthProviderCodex)
	pool := oauth.NewAccountPool(tokens, time.Now, oauth.NewSelector(config.LoadBalancingFillFirst))

	router, _ := upstream.NewRouter(cfg.Models)
	logger := logrus.New()
	logger.SetOutput(io.Discard)
	api := &API{Cfg: cfg, Router: router, Client: upstream.NewClient(cfg), OAuth: manager, Pool: pool, Logger: logger}

	engine := gin.New()
	engine.POST("/v1/responses", api.Responses)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"droid-oauth","input":"hi"}`))
	engine.ServeHTTP(w, req)

	// xAI should get 429 directly, no failover.
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 for xAI (no failover), got %d body=%s", w.Code, w.Body.String())
	}
	// xAI should make exactly 1 attempt (no pool failover).
	if atomic.LoadInt32(&attemptCount) != 1 {
		t.Fatalf("xAI should not fail over, got %d attempts", attemptCount)
	}
}

// --------------- Payload replay is stable across attempts ---------------

func TestResponsesCodexFailoverPayloadReplayStable(t *testing.T) {
	var mu sync.Mutex
	var capturedPayloads []map[string]any

	api := newCodexFailoverTestAPI(t, failoverTestOptions{
		maxFailovers: 2,
		accounts: []failoverTestAccount{
			{email: "a@test.com", accountID: "acct_a"},
			{email: "b@test.com", accountID: "acct_b"},
			{email: "c@test.com", accountID: "acct_c"},
		},
		upstreamHandler: func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			var parsed map[string]any
			_ = json.Unmarshal(body, &parsed)
			mu.Lock()
			capturedPayloads = append(capturedPayloads, parsed)
			mu.Unlock()

			auth := authTokenFromRequest(r)
			if auth == "access-token-2" {
				codexSuccessResponse(w)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"rate limited"}}`))
		},
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"droid-oauth","input":"hi"}`))
	api.engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	if len(capturedPayloads) != 3 {
		t.Fatalf("expected 3 captured payloads, got %d", len(capturedPayloads))
	}

	// Verify model, stream, instructions, store are identical across attempts.
	for i := 1; i < len(capturedPayloads); i++ {
		if capturedPayloads[i]["model"] != capturedPayloads[0]["model"] {
			t.Fatalf("model mismatch attempt 0 vs %d: %v vs %v", i, capturedPayloads[0]["model"], capturedPayloads[i]["model"])
		}
		if capturedPayloads[i]["stream"] != capturedPayloads[0]["stream"] {
			t.Fatalf("stream mismatch attempt 0 vs %d", i)
		}
		if capturedPayloads[i]["instructions"] != capturedPayloads[0]["instructions"] {
			t.Fatalf("instructions mismatch attempt 0 vs %d", i)
		}
		if capturedPayloads[i]["store"] != capturedPayloads[0]["store"] {
			t.Fatalf("store mismatch attempt 0 vs %d", i)
		}
	}
}

// --------------- Streaming 429 fails over before downstream commit ---------------

func TestResponsesCodexFailoverStreaming429FailsOver(t *testing.T) {
	var attemptCount int32

	api := newCodexFailoverTestAPI(t, failoverTestOptions{
		maxFailovers: 1,
		accounts: []failoverTestAccount{
			{email: "a@test.com", accountID: "acct_a"},
			{email: "b@test.com", accountID: "acct_b"},
		},
		upstreamHandler: func(w http.ResponseWriter, r *http.Request) {
			n := int(atomic.AddInt32(&attemptCount, 1))
			if n == 1 {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"error":{"message":"rate limited"}}`))
				return
			}
			// Second attempt: success with streaming.
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			flusher := w.(http.Flusher)
			_, _ = w.Write([]byte("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"output\":[]}}\n\n"))
			flusher.Flush()
		},
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"droid-oauth","input":"hi","stream":true}`))
	api.engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	if atomic.LoadInt32(&attemptCount) != 2 {
		t.Fatalf("expected 2 attempts (429 then success), got %d", attemptCount)
	}
	if !strings.Contains(w.Body.String(), "response.completed") {
		t.Fatalf("expected streaming response, got %s", w.Body.String())
	}
}

// --------------- VAL-FAILOVER-002: 429 cooldown uses Retry-After, quota reset, then fallback ---------------

func TestCodexRateLimitCooldownUsesRetryAfterNumeric(t *testing.T) {
	// When a 429 has Retry-After: 120 (numeric seconds), the pool's
	// rate-limited timestamp should be approximately 120s from now.
	api := newCodexFailoverTestAPI(t, failoverTestOptions{
		maxFailovers:      0,
		rateLimitCooldown: 60 * time.Second,
		accounts: []failoverTestAccount{
			{email: "a@test.com", accountID: "acct_a"},
		},
		upstreamHandler: func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Retry-After", "120")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"rate limited"}}`))
		},
	})

	before := time.Now()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"droid-oauth","input":"hi"}`))
	api.engine.ServeHTTP(w, req)

	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", w.Code)
	}

	snap := api.api.Pool.Snapshot()
	if len(snap.Accounts) != 1 {
		t.Fatalf("expected 1 account, got %d", len(snap.Accounts))
	}
	rl := snap.Accounts[0].RateLimitedUntil
	if rl == nil {
		t.Fatal("expected rate-limited timestamp on account")
	}
	// Should be approximately 120s from now (allow 5s tolerance).
	expectedMin := before.Add(115 * time.Second)
	expectedMax := before.Add(125 * time.Second)
	if rl.Before(expectedMin) || rl.After(expectedMax) {
		t.Fatalf("expected rate-limited until ~%v (120s from request), got %v", before.Add(120*time.Second), rl)
	}
}

func TestCodexRateLimitCooldownUsesRetryAfterHTTPDate(t *testing.T) {
	// When a 429 has Retry-After with an HTTP-date, the pool's rate-limited
	// timestamp should match that date.
	futureTime := time.Now().Add(5 * time.Minute).UTC().Format("Mon, 02 Jan 2006 15:04:05 GMT")
	api := newCodexFailoverTestAPI(t, failoverTestOptions{
		maxFailovers:      0,
		rateLimitCooldown: 60 * time.Second,
		accounts: []failoverTestAccount{
			{email: "a@test.com", accountID: "acct_a"},
		},
		upstreamHandler: func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Retry-After", futureTime)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"rate limited"}}`))
		},
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"droid-oauth","input":"hi"}`))
	api.engine.ServeHTTP(w, req)

	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", w.Code)
	}

	snap := api.api.Pool.Snapshot()
	rl := snap.Accounts[0].RateLimitedUntil
	if rl == nil {
		t.Fatal("expected rate-limited timestamp on account")
	}
	parsed, err := http.ParseTime(futureTime)
	if err != nil {
		t.Fatalf("could not parse test future time: %v", err)
	}
	if !rl.Truncate(time.Second).Equal(parsed.Truncate(time.Second)) {
		t.Fatalf("expected rate-limited until %v, got %v", parsed, rl)
	}
}

func TestCodexRateLimitCooldownUsesQuotaResetWhenNoRetryAfter(t *testing.T) {
	// When a 429 has quota headers with a future reset-at but no Retry-After,
	// the pool's rate-limited timestamp should use the deterministic quota reset.
	resetAt := time.Now().Add(10 * time.Minute).Unix() // future unix timestamp
	api := newCodexFailoverTestAPI(t, failoverTestOptions{
		maxFailovers:      0,
		rateLimitCooldown: 60 * time.Second,
		accounts: []failoverTestAccount{
			{email: "a@test.com", accountID: "acct_a"},
		},
		upstreamHandler: func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("x-codex-primary-used-percent", "100")
			w.Header().Set("x-codex-primary-reset-at", fmt.Sprintf("%d", resetAt))
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"rate limited"}}`))
		},
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"droid-oauth","input":"hi"}`))
	api.engine.ServeHTTP(w, req)

	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", w.Code)
	}

	snap := api.api.Pool.Snapshot()
	rl := snap.Accounts[0].RateLimitedUntil
	if rl == nil {
		t.Fatal("expected rate-limited timestamp on account")
	}
	expected := time.Unix(resetAt, 0).UTC()
	if !rl.Truncate(time.Second).Equal(expected.Truncate(time.Second)) {
		t.Fatalf("expected rate-limited until %v (quota reset), got %v", expected, rl)
	}
}

func TestCodexRateLimitCooldownRetryAfterOverridesQuotaReset(t *testing.T) {
	// When both Retry-After and quota reset are present, Retry-After wins.
	resetAt := time.Now().Add(10 * time.Minute).Unix()
	api := newCodexFailoverTestAPI(t, failoverTestOptions{
		maxFailovers:      0,
		rateLimitCooldown: 60 * time.Second,
		accounts: []failoverTestAccount{
			{email: "a@test.com", accountID: "acct_a"},
		},
		upstreamHandler: func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Retry-After", "30")
			w.Header().Set("x-codex-primary-used-percent", "100")
			w.Header().Set("x-codex-primary-reset-at", fmt.Sprintf("%d", resetAt))
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"rate limited"}}`))
		},
	})

	before := time.Now()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"droid-oauth","input":"hi"}`))
	api.engine.ServeHTTP(w, req)

	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", w.Code)
	}

	snap := api.api.Pool.Snapshot()
	rl := snap.Accounts[0].RateLimitedUntil
	if rl == nil {
		t.Fatal("expected rate-limited timestamp on account")
	}
	// Should be ~30s (Retry-After), not 10min (quota reset).
	expectedMin := before.Add(25 * time.Second)
	expectedMax := before.Add(35 * time.Second)
	if rl.Before(expectedMin) || rl.After(expectedMax) {
		t.Fatalf("expected rate-limited until ~30s (Retry-After), got %v", rl)
	}
}

func TestCodexRateLimitCooldownFallsBackToConfiguredDefault(t *testing.T) {
	// When no Retry-After and no quota reset, use configured rate_limit_cooldown.
	api := newCodexFailoverTestAPI(t, failoverTestOptions{
		maxFailovers:      0,
		rateLimitCooldown: 45 * time.Second,
		accounts: []failoverTestAccount{
			{email: "a@test.com", accountID: "acct_a"},
		},
		upstreamHandler: func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"rate limited"}}`))
		},
	})

	before := time.Now()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"droid-oauth","input":"hi"}`))
	api.engine.ServeHTTP(w, req)

	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", w.Code)
	}

	snap := api.api.Pool.Snapshot()
	rl := snap.Accounts[0].RateLimitedUntil
	if rl == nil {
		t.Fatal("expected rate-limited timestamp on account")
	}
	// Should be approximately 45s from now (configured fallback).
	expectedMin := before.Add(40 * time.Second)
	expectedMax := before.Add(50 * time.Second)
	if rl.Before(expectedMin) || rl.After(expectedMax) {
		t.Fatalf("expected rate-limited until ~%v (45s fallback), got %v", before.Add(45*time.Second), rl)
	}
}

func TestCodexRateLimitCooldownInvalidRetryAfterFallsThrough(t *testing.T) {
	// Invalid Retry-After should fall through to quota reset or fallback.
	resetAt := time.Now().Add(5 * time.Minute).Unix()
	for _, badHeader := range []string{"not-a-number", "-10", "0", "abc"} {
		t.Run("retry_after_"+badHeader, func(t *testing.T) {
			api := newCodexFailoverTestAPI(t, failoverTestOptions{
				maxFailovers:      0,
				rateLimitCooldown: 60 * time.Second,
				accounts: []failoverTestAccount{
					{email: "a@test.com", accountID: "acct_a"},
				},
				upstreamHandler: func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Retry-After", badHeader)
					w.Header().Set("x-codex-primary-used-percent", "100")
					w.Header().Set("x-codex-primary-reset-at", fmt.Sprintf("%d", resetAt))
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusTooManyRequests)
					_, _ = w.Write([]byte(`{"error":{"message":"rate limited"}}`))
				},
			})

			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"droid-oauth","input":"hi"}`))
			api.engine.ServeHTTP(w, req)

			snap := api.api.Pool.Snapshot()
			rl := snap.Accounts[0].RateLimitedUntil
			if rl == nil {
				t.Fatal("expected rate-limited timestamp from quota reset fallback")
			}
			expected := time.Unix(resetAt, 0).UTC()
			if !rl.Truncate(time.Second).Equal(expected.Truncate(time.Second)) {
				t.Fatalf("expected quota reset %v, got %v", expected, rl)
			}
		})
	}
}

func TestCodexRateLimitCooldownMultipleWindowsPicksLatest(t *testing.T) {
	// When multiple quota windows have reset-at, the latest (maximum) is used.
	earlierReset := time.Now().Add(3 * time.Minute).Unix()
	laterReset := time.Now().Add(8 * time.Minute).Unix()
	api := newCodexFailoverTestAPI(t, failoverTestOptions{
		maxFailovers:      0,
		rateLimitCooldown: 60 * time.Second,
		accounts: []failoverTestAccount{
			{email: "a@test.com", accountID: "acct_a"},
		},
		upstreamHandler: func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("x-codex-primary-used-percent", "100")
			w.Header().Set("x-codex-primary-reset-at", fmt.Sprintf("%d", earlierReset))
			w.Header().Set("x-codex-secondary-used-percent", "100")
			w.Header().Set("x-codex-secondary-reset-at", fmt.Sprintf("%d", laterReset))
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"rate limited"}}`))
		},
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"droid-oauth","input":"hi"}`))
	api.engine.ServeHTTP(w, req)

	snap := api.api.Pool.Snapshot()
	rl := snap.Accounts[0].RateLimitedUntil
	if rl == nil {
		t.Fatal("expected rate-limited timestamp")
	}
	expected := time.Unix(laterReset, 0).UTC()
	if !rl.Truncate(time.Second).Equal(expected.Truncate(time.Second)) {
		t.Fatalf("expected latest quota reset %v, got %v", expected, rl)
	}
}

func TestCodexRateLimitCooldownOnlyMarksAttemptedAccount(t *testing.T) {
	// Only the account that got 429 is marked rate-limited, not other accounts.
	var mu sync.Mutex
	var firstToken string

	api := newCodexFailoverTestAPI(t, failoverTestOptions{
		maxFailovers:      1,
		rateLimitCooldown: 60 * time.Second,
		strategy:          config.LoadBalancingFillFirst,
		accounts: []failoverTestAccount{
			{email: "a@test.com", accountID: "acct_a"},
			{email: "b@test.com", accountID: "acct_b"},
		},
		upstreamHandler: func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			if firstToken == "" {
				firstToken = authTokenFromRequest(r)
			}
			mu.Unlock()

			auth := authTokenFromRequest(r)
			if auth == firstToken {
				w.Header().Set("Retry-After", "120")
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"error":{"message":"rate limited"}}`))
				return
			}
			codexSuccessResponse(w)
		},
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"droid-oauth","input":"hi"}`))
	api.engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 after failover, got %d", w.Code)
	}

	snap := api.api.Pool.Snapshot()
	for _, acct := range snap.Accounts {
		if acct.Selector == "a@test.com" {
			if acct.RateLimitedUntil == nil {
				t.Fatal("expected first account to be rate-limited")
			}
		}
		if acct.Selector == "b@test.com" {
			if acct.RateLimitedUntil != nil {
				t.Fatalf("second account should NOT be rate-limited, got %v", acct.RateLimitedUntil)
			}
		}
	}
}

// --------------- VAL-FAILOVER-009: Codex quota recording remains per selected account ---------------

func TestCodexQuotaRecordingRetryableErrorsPersistToAttemptedAccount(t *testing.T) {
	// Quota headers from a 429 response should persist to the failed account's token file.
	api := newCodexFailoverTestAPI(t, failoverTestOptions{
		maxFailovers:      1,
		rateLimitCooldown: 60 * time.Second,
		strategy:          config.LoadBalancingFillFirst,
		accounts: []failoverTestAccount{
			{email: "a@test.com", accountID: "acct_a"},
			{email: "b@test.com", accountID: "acct_b"},
		},
		upstreamHandler: func(w http.ResponseWriter, r *http.Request) {
			auth := authTokenFromRequest(r)
			if auth == "access-token-0" {
				w.Header().Set("x-codex-primary-used-percent", "100")
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"error":{"message":"rate limited"}}`))
				return
			}
			codexSuccessResponse(w)
		},
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"droid-oauth","input":"hi"}`))
	api.engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}

	// Verify quota was persisted to account a's token file by reloading tokens.
	tokens, err := api.api.OAuth.LoadTokens(config.OAuthProviderCodex)
	if err != nil {
		t.Fatal(err)
	}
	for _, tok := range tokens {
		if tok.Email == "a@test.com" {
			if tok.CodexQuota == nil || tok.CodexQuota.Primary == nil {
				t.Fatalf("expected quota on first account token (the one that got 429), got nil")
			}
			if tok.CodexQuota.Primary.UsedPercent != 100 {
				t.Fatalf("expected 100%% used on first account quota, got %v", tok.CodexQuota.Primary.UsedPercent)
			}
		}
	}
}

func TestCodexQuotaRecordingSSEQuotaPersistsToSuccessAccount(t *testing.T) {
	// SSE quota events from a successful response should persist to the
	// successful account's token file, not to previously failed accounts.
	api := newCodexFailoverTestAPI(t, failoverTestOptions{
		maxFailovers:      1,
		rateLimitCooldown: 60 * time.Second,
		strategy:          config.LoadBalancingFillFirst,
		accounts: []failoverTestAccount{
			{email: "a@test.com", accountID: "acct_a"},
			{email: "b@test.com", accountID: "acct_b"},
		},
		upstreamHandler: func(w http.ResponseWriter, r *http.Request) {
			auth := authTokenFromRequest(r)
			if auth == "access-token-0" {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"error":{"message":"rate limited"}}`))
				return
			}
			// Success response with SSE quota event in non-streaming body.
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintln(w, `data: {"type":"codex.rate_limits","rate_limits":{"primary":{"used_percent":55,"window_minutes":60}}}`)
			fmt.Fprintln(w)
			fmt.Fprintln(w, "event: response.completed")
			fmt.Fprintln(w, `data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[]}}`)
			fmt.Fprintln(w)
		},
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"droid-oauth","input":"hi"}`))
	api.engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}

	// Verify SSE quota was persisted to account b's token file (the successful one).
	tokens, err := api.api.OAuth.LoadTokens(config.OAuthProviderCodex)
	if err != nil {
		t.Fatal(err)
	}
	for _, tok := range tokens {
		if tok.Email == "b@test.com" {
			if tok.CodexQuota == nil || tok.CodexQuota.Primary == nil {
				t.Fatalf("expected quota on second account token (the one that succeeded), got nil")
			}
			if tok.CodexQuota.Primary.UsedPercent != 55 {
				t.Fatalf("expected 55%% used on second account quota, got %v", tok.CodexQuota.Primary.UsedPercent)
			}
		}
	}
}

func TestCodexQuotaRecordingNoCrossAccountOverwrite(t *testing.T) {
	// Verify that quota metadata is never cross-account overwritten.
	// Account A gets 429 with quota → its token gets quota.
	// Account B succeeds with different quota → its token gets its own quota.
	// Account A's quota should still reflect the 429 values, not B's.
	api := newCodexFailoverTestAPI(t, failoverTestOptions{
		maxFailovers:      1,
		rateLimitCooldown: 60 * time.Second,
		strategy:          config.LoadBalancingFillFirst,
		accounts: []failoverTestAccount{
			{email: "a@test.com", accountID: "acct_a"},
			{email: "b@test.com", accountID: "acct_b"},
		},
		upstreamHandler: func(w http.ResponseWriter, r *http.Request) {
			auth := authTokenFromRequest(r)
			if auth == "access-token-0" {
				w.Header().Set("x-codex-primary-used-percent", "100")
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"error":{"message":"rate limited"}}`))
				return
			}
			// Account B succeeds with different quota.
			w.Header().Set("x-codex-primary-used-percent", "30")
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintln(w, `data: {"type":"codex.rate_limits","rate_limits":{"primary":{"used_percent":30}}}`)
			fmt.Fprintln(w)
			fmt.Fprintln(w, "event: response.completed")
			fmt.Fprintln(w, `data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[]}}`)
			fmt.Fprintln(w)
		},
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"droid-oauth","input":"hi"}`))
	api.engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}

	// Verify quota was persisted per account without cross-overwrite.
	tokens, err := api.api.OAuth.LoadTokens(config.OAuthProviderCodex)
	if err != nil {
		t.Fatal(err)
	}
	for _, tok := range tokens {
		switch tok.Email {
		case "a@test.com":
			if tok.CodexQuota == nil || tok.CodexQuota.Primary == nil {
				t.Fatal("expected quota on account A token")
			}
			// Should be 100 from the 429 headers, not overwritten by B's 30.
			if tok.CodexQuota.Primary.UsedPercent != 100 {
				t.Fatalf("account A quota should be 100 (from 429), got %v", tok.CodexQuota.Primary.UsedPercent)
			}
		case "b@test.com":
			if tok.CodexQuota == nil || tok.CodexQuota.Primary == nil {
				t.Fatal("expected quota on account B token")
			}
			// SSE quota updates: the SSE quota event said 30.
			if tok.CodexQuota.Primary.UsedPercent != 30 {
				t.Fatalf("account B quota should be 30 (from success), got %v", tok.CodexQuota.Primary.UsedPercent)
			}
		}
	}
}

func TestCodexQuotaRecordingNoSecretLeakage(t *testing.T) {
	// Verify that quota recording does not leak secrets into pool snapshots
	// or token files.
	api := newCodexFailoverTestAPI(t, failoverTestOptions{
		maxFailovers:      1,
		rateLimitCooldown: 60 * time.Second,
		strategy:          config.LoadBalancingFillFirst,
		accounts: []failoverTestAccount{
			{email: "a@test.com", accountID: "acct_a"},
			{email: "b@test.com", accountID: "acct_b"},
		},
		upstreamHandler: func(w http.ResponseWriter, r *http.Request) {
			auth := authTokenFromRequest(r)
			if auth == "access-token-0" {
				w.Header().Set("x-codex-primary-used-percent", "100")
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"error":{"message":"rate limited"}}`))
				return
			}
			codexSuccessResponse(w)
		},
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"droid-oauth","input":"hi"}`))
	api.engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}

	// Check pool snapshot has no secrets.
	snap := api.api.Pool.Snapshot()
	snapJSON, _ := json.Marshal(snap)
	snapStr := string(snapJSON)
	for _, secret := range []string{"access-token-", "refresh-token-", "Bearer "} {
		if strings.Contains(snapStr, secret) {
			t.Fatalf("pool snapshot contains secret %q: %s", secret, snapStr)
		}
	}

	// Check response body has no secrets.
	respBody := w.Body.String()
	for _, secret := range []string{"access-token-", "refresh-token-"} {
		if strings.Contains(respBody, secret) {
			t.Fatalf("response body contains secret %q: %s", secret, respBody)
		}
	}
}

// --------------- VAL-FAILOVER-017: Quota headers persist for non-retryable and exhausted errors ---------------

func TestCodexQuotaRecordingNonRetryable4xxPersistsQuota(t *testing.T) {
	// A non-retryable 4xx (e.g. 400) with quota headers should still record
	// quota to the attempted account's token.
	api := newCodexFailoverTestAPI(t, failoverTestOptions{
		maxFailovers:      2,
		rateLimitCooldown: 60 * time.Second,
		strategy:          config.LoadBalancingFillFirst,
		accounts: []failoverTestAccount{
			{email: "a@test.com", accountID: "acct_a"},
			{email: "b@test.com", accountID: "acct_b"},
		},
		upstreamHandler: func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("x-codex-primary-used-percent", "75")
			w.Header().Set("x-codex-primary-window-minutes", "60")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"bad request","type":"invalid_request_error"}}`))
		},
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"droid-oauth","input":"hi"}`))
	api.engine.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", w.Code, w.Body.String())
	}

	// Verify quota was persisted to account A's token file (the one that was attempted).
	tokens, err := api.api.OAuth.LoadTokens(config.OAuthProviderCodex)
	if err != nil {
		t.Fatal(err)
	}
	for _, tok := range tokens {
		if tok.Email == "a@test.com" {
			if tok.CodexQuota == nil || tok.CodexQuota.Primary == nil {
				t.Fatalf("expected quota on first account token despite non-retryable 4xx, got nil")
			}
			if tok.CodexQuota.Primary.UsedPercent != 75 {
				t.Fatalf("expected 75%% used, got %v", tok.CodexQuota.Primary.UsedPercent)
			}
		}
		// Account B should NOT have quota (was never attempted).
		if tok.Email == "b@test.com" {
			if tok.CodexQuota != nil {
				t.Fatalf("account B should have no quota (never attempted), got %v", tok.CodexQuota)
			}
		}
	}
}

func TestCodexQuotaRecordingExhaustedErrorsPersistQuotaPerAccount(t *testing.T) {
	// When all attempts fail with 429, each account should have its own
	// quota headers persisted. The last attempted account's quota should
	// be the one visible in the pool snapshot.
	var mu sync.Mutex
	var attemptTokens []string

	api := newCodexFailoverTestAPI(t, failoverTestOptions{
		maxFailovers:      1,
		rateLimitCooldown: 60 * time.Second,
		strategy:          config.LoadBalancingFillFirst,
		accounts: []failoverTestAccount{
			{email: "a@test.com", accountID: "acct_a"},
			{email: "b@test.com", accountID: "acct_b"},
		},
		upstreamHandler: func(w http.ResponseWriter, r *http.Request) {
			auth := authTokenFromRequest(r)
			mu.Lock()
			attemptTokens = append(attemptTokens, auth)
			mu.Unlock()

			if auth == "access-token-0" {
				w.Header().Set("x-codex-primary-used-percent", "80")
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"error":{"message":"rate limited"}}`))
				return
			}
			w.Header().Set("x-codex-primary-used-percent", "95")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"rate limited"}}`))
		},
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"droid-oauth","input":"hi"}`))
	api.engine.ServeHTTP(w, req)

	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 (exhausted), got %d body=%s", w.Code, w.Body.String())
	}

	// Both accounts should have been attempted.
	if len(attemptTokens) != 2 {
		t.Fatalf("expected 2 attempts, got %d", len(attemptTokens))
	}

	// Verify quota was persisted per account via token files.
	tokens, err := api.api.OAuth.LoadTokens(config.OAuthProviderCodex)
	if err != nil {
		t.Fatal(err)
	}
	for _, tok := range tokens {
		switch tok.Email {
		case "a@test.com":
			if tok.CodexQuota == nil || tok.CodexQuota.Primary == nil {
				t.Fatal("expected quota on account A token")
			}
			if tok.CodexQuota.Primary.UsedPercent != 80 {
				t.Fatalf("account A quota should be 80, got %v", tok.CodexQuota.Primary.UsedPercent)
			}
		case "b@test.com":
			if tok.CodexQuota == nil || tok.CodexQuota.Primary == nil {
				t.Fatal("expected quota on account B token")
			}
			if tok.CodexQuota.Primary.UsedPercent != 95 {
				t.Fatalf("account B quota should be 95, got %v", tok.CodexQuota.Primary.UsedPercent)
			}
		}
	}
}

func TestCodexQuotaRecordingExhaustedFinalErrorPreservesLastQuota(t *testing.T) {
	// When budget is exhausted, the last upstream error is relayed.
	// Quota from the final attempt should be recorded to the correct account.
	var attemptCount int32

	api := newCodexFailoverTestAPI(t, failoverTestOptions{
		maxFailovers:      0, // only 1 attempt
		rateLimitCooldown: 60 * time.Second,
		strategy:          config.LoadBalancingFillFirst,
		accounts: []failoverTestAccount{
			{email: "a@test.com", accountID: "acct_a"},
		},
		upstreamHandler: func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&attemptCount, 1)
			w.Header().Set("x-codex-primary-used-percent", "99")
			w.Header().Set("x-codex-primary-window-minutes", "60")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"error":{"message":"bad gateway"}}`))
		},
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"droid-oauth","input":"hi"}`))
	api.engine.ServeHTTP(w, req)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "bad gateway") {
		t.Fatalf("expected error body preserved, got %s", w.Body.String())
	}

	// Verify quota was persisted despite the exhausted error via token file.
	tokens, err := api.api.OAuth.LoadTokens(config.OAuthProviderCodex)
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 1 {
		t.Fatalf("expected 1 token, got %d", len(tokens))
	}
	tok := tokens[0]
	if tok.CodexQuota == nil || tok.CodexQuota.Primary == nil {
		t.Fatal("expected quota persisted even for exhausted error")
	}
	if tok.CodexQuota.Primary.UsedPercent != 99 {
		t.Fatalf("expected 99%% used, got %v", tok.CodexQuota.Primary.UsedPercent)
	}
}

// --------------- VAL-FAILOVER-004: 401/403 refresh before dropping account ---------------

func TestResponsesCodex401ForceRefreshReplaySucceeds(t *testing.T) {
	// When the first account gets 401, force-refresh should be attempted
	// and the replay request should succeed on the same account.
	var mu sync.Mutex
	var attempts []string // track access tokens used
	var refreshTokens []string

	// Set up a fake token endpoint for force refresh.
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		values, _ := url.ParseQuery(string(body))
		rt := values.Get("refresh_token")
		mu.Lock()
		refreshTokens = append(refreshTokens, rt)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "refreshed-access-token-0",
			"refresh_token": rt,
			"expires_in":    3600,
		})
	}))
	defer tokenSrv.Close()
	restore := oauth.SetTestCodexTokenURL(tokenSrv.URL)
	defer restore()

	api := newCodexFailoverTestAPI(t, failoverTestOptions{
		maxFailovers: 1,
		accounts: []failoverTestAccount{
			{email: "a@test.com", accountID: "acct_a"},
			{email: "b@test.com", accountID: "acct_b"},
		},
		upstreamHandler: func(w http.ResponseWriter, r *http.Request) {
			auth := authTokenFromRequest(r)
			mu.Lock()
			attempts = append(attempts, auth)
			mu.Unlock()

			if auth == "access-token-0" {
				// Initial request with original token returns 401.
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":{"message":"unauthorized","type":"authentication_error"}}`))
				return
			}
			// Refreshed token request succeeds.
			codexSuccessResponse(w)
		},
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"droid-oauth","input":"hi"}`))
	api.engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 after force-refresh replay, got %d body=%s", w.Code, w.Body.String())
	}
	// Should have: initial attempt (401) + replay with refreshed token (success)
	mu.Lock()
	defer mu.Unlock()
	if len(attempts) != 2 {
		t.Fatalf("expected 2 upstream attempts, got %d: %v", len(attempts), attempts)
	}
	if attempts[0] != "access-token-0" {
		t.Fatalf("first attempt should use original token, got %s", attempts[0])
	}
	if attempts[1] != "refreshed-access-token-0" {
		t.Fatalf("replay should use refreshed token, got %s", attempts[1])
	}
	if len(refreshTokens) != 1 {
		t.Fatalf("expected 1 refresh call, got %d", len(refreshTokens))
	}
}

func TestResponsesCodex403ForceRefreshStillFailsThenFailover(t *testing.T) {
	// When the first account gets 403, force-refresh is attempted,
	// but the replay still gets 403. The account should be marked unhealthy
	// and failover should continue to the next account.
	var mu sync.Mutex
	var attempts []string

	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "refreshed-access-token-0",
			"refresh_token": "refresh-token-0",
			"expires_in":    3600,
		})
	}))
	defer tokenSrv.Close()
	restore := oauth.SetTestCodexTokenURL(tokenSrv.URL)
	defer restore()

	api := newCodexFailoverTestAPI(t, failoverTestOptions{
		maxFailovers: 1,
		accounts: []failoverTestAccount{
			{email: "a@test.com", accountID: "acct_a"},
			{email: "b@test.com", accountID: "acct_b"},
		},
		upstreamHandler: func(w http.ResponseWriter, r *http.Request) {
			auth := authTokenFromRequest(r)
			mu.Lock()
			attempts = append(attempts, auth)
			mu.Unlock()

			// Both original and refreshed tokens for account 0 return 403.
			// Account 1 succeeds.
			if auth == "access-token-0" || auth == "refreshed-access-token-0" {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"error":{"message":"forbidden","type":"permission_error"}}`))
				return
			}
			codexSuccessResponse(w)
		},
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"droid-oauth","input":"hi"}`))
	api.engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 after failover to second account, got %d body=%s", w.Code, w.Body.String())
	}
	mu.Lock()
	defer mu.Unlock()
	// 3 attempts: initial 403 (acct 0) + replay 403 (acct 0 refreshed) + success (acct 1)
	if len(attempts) != 3 {
		t.Fatalf("expected 3 upstream attempts, got %d: %v", len(attempts), attempts)
	}

	// Verify first account is marked unhealthy.
	snap := api.api.Pool.Snapshot()
	for _, acct := range snap.Accounts {
		if acct.Selector == "a@test.com" && acct.Healthy {
			t.Fatal("expected first account to be marked unhealthy after failed replay")
		}
	}
}

func TestResponsesCodex401RefreshFailsThenFailover(t *testing.T) {
	// When the first account gets 401 and the force-refresh itself fails,
	// the account should be marked unhealthy and failover should continue.
	var mu sync.Mutex
	var attempts []string

	// Token endpoint returns error (refresh fails).
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
	}))
	defer tokenSrv.Close()
	restore := oauth.SetTestCodexTokenURL(tokenSrv.URL)
	defer restore()

	api := newCodexFailoverTestAPI(t, failoverTestOptions{
		maxFailovers: 1,
		accounts: []failoverTestAccount{
			{email: "a@test.com", accountID: "acct_a"},
			{email: "b@test.com", accountID: "acct_b"},
		},
		upstreamHandler: func(w http.ResponseWriter, r *http.Request) {
			auth := authTokenFromRequest(r)
			mu.Lock()
			attempts = append(attempts, auth)
			mu.Unlock()

			if auth == "access-token-0" {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":{"message":"unauthorized"}}`))
				return
			}
			codexSuccessResponse(w)
		},
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"droid-oauth","input":"hi"}`))
	api.engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 after failover (refresh failed), got %d body=%s", w.Code, w.Body.String())
	}
	mu.Lock()
	defer mu.Unlock()
	// 2 attempts: initial 401 (acct 0) + success (acct 1, after refresh failed for acct 0)
	if len(attempts) != 2 {
		t.Fatalf("expected 2 upstream attempts, got %d: %v", len(attempts), attempts)
	}

	// Verify first account is marked unhealthy.
	snap := api.api.Pool.Snapshot()
	for _, acct := range snap.Accounts {
		if acct.Selector == "a@test.com" && acct.Healthy {
			t.Fatal("expected first account to be marked unhealthy after failed refresh")
		}
	}
}

func TestResponsesCodex401ReplayDoesNotConsumeFailoverBudget(t *testing.T) {
	// The same-account force-refresh replay should NOT consume the
	// alternate-account failover budget. With max_failovers=2 (3 total attempts)
	// and 3 accounts, the 401 replay on account A doesn't count against the budget,
	// so both B and C should still be tried if needed.
	var mu sync.Mutex
	var attemptTokens []string

	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "refreshed-access-token-0",
			"refresh_token": "refresh-token-0",
			"expires_in":    3600,
		})
	}))
	defer tokenSrv.Close()
	restore := oauth.SetTestCodexTokenURL(tokenSrv.URL)
	defer restore()

	api := newCodexFailoverTestAPI(t, failoverTestOptions{
		maxFailovers: 2, // budget allows 3 total alternate-account attempts
		accounts: []failoverTestAccount{
			{email: "a@test.com", accountID: "acct_a"},
			{email: "b@test.com", accountID: "acct_b"},
			{email: "c@test.com", accountID: "acct_c"},
		},
		upstreamHandler: func(w http.ResponseWriter, r *http.Request) {
			auth := authTokenFromRequest(r)
			mu.Lock()
			attemptTokens = append(attemptTokens, auth)
			mu.Unlock()

			// Account A: 401 on initial, 401 on replay
			if auth == "access-token-0" || auth == "refreshed-access-token-0" {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":{"message":"unauthorized"}}`))
				return
			}
			// Account B: 429
			if auth == "access-token-1" {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"error":{"message":"rate limited"}}`))
				return
			}
			// Account C: success
			codexSuccessResponse(w)
		},
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"droid-oauth","input":"hi"}`))
	api.engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 after budget-preserving failover, got %d body=%s", w.Code, w.Body.String())
	}
	mu.Lock()
	defer mu.Unlock()
	// 4 attempts:
	//   1. acct A initial (401) - counts as main attempt (iteration 0)
	//   2. acct A replay (401) - same-account, doesn't consume budget
	//   3. acct B (429) - first failover attempt (iteration 1)
	//   4. acct C (success) - second failover attempt (iteration 2)
	if len(attemptTokens) != 4 {
		t.Fatalf("expected 4 upstream attempts, got %d: %v", len(attemptTokens), attemptTokens)
	}
}

func TestResponsesCodex401SameAccountReplayAtMostOnce(t *testing.T) {
	// Verify that the same-account replay is attempted at most once per account.
	// After the replay, if it still fails, the account is marked unhealthy
	// and excluded from further selection.
	var mu sync.Mutex
	var attemptTokens []string

	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "refreshed-access-token-0",
			"refresh_token": "refresh-token-0",
			"expires_in":    3600,
		})
	}))
	defer tokenSrv.Close()
	restore := oauth.SetTestCodexTokenURL(tokenSrv.URL)
	defer restore()

	api := newCodexFailoverTestAPI(t, failoverTestOptions{
		maxFailovers: 2,
		accounts: []failoverTestAccount{
			{email: "a@test.com", accountID: "acct_a"},
			{email: "b@test.com", accountID: "acct_b"},
		},
		upstreamHandler: func(w http.ResponseWriter, r *http.Request) {
			auth := authTokenFromRequest(r)
			mu.Lock()
			attemptTokens = append(attemptTokens, auth)
			mu.Unlock()

			// All requests return 401.
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"message":"unauthorized"}}`))
		},
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"droid-oauth","input":"hi"}`))
	api.engine.ServeHTTP(w, req)

	// Should get 401 (last error relayed)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d body=%s", w.Code, w.Body.String())
	}

	mu.Lock()
	defer mu.Unlock()
	// Expected attempts:
	//   1. acct A initial (401)
	//   2. acct A replay (401) - same-account, at most once
	//   3. acct B initial (401)
	//   4. acct B replay (401) - same-account, at most once
	// Total: 4 attempts (2 main + 2 replays, within max_failovers=2 budget for alternates)
	if len(attemptTokens) != 4 {
		t.Fatalf("expected 4 attempts (each account tried once with replay), got %d: %v", len(attemptTokens), attemptTokens)
	}

	// Verify no account was tried more than twice (once initial + once replay)
	tokenCounts := map[string]int{}
	for _, tok := range attemptTokens {
		tokenCounts[tok]++
	}
	for tok, count := range tokenCounts {
		if count > 2 {
			t.Fatalf("token %s was used %d times, expected at most 2 (initial + replay)", tok, count)
		}
	}
}

// --------------- VAL-FAILOVER-011: Single-account Codex remains equivalent to current behavior ---------------

func TestResponsesCodexSingleAccount401NoReplay(t *testing.T) {
	// With exactly one enabled Codex account, 401 should be relayed directly
	// without any force-refresh+replay attempt.
	var attemptCount int32

	restore := oauth.SetTestCodexTokenURL("http://unused-invalid-url")
	defer restore()

	api := newCodexFailoverTestAPI(t, failoverTestOptions{
		maxFailovers: 2,
		accounts: []failoverTestAccount{
			{email: "a@test.com", accountID: "acct_a"},
		},
		upstreamHandler: func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&attemptCount, 1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"message":"unauthorized","type":"authentication_error"}}`))
		},
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"droid-oauth","input":"hi"}`))
	api.engine.ServeHTTP(w, req)

	// Should relay the 401 directly
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 relayed directly, got %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "unauthorized") {
		t.Fatalf("expected upstream error body preserved, got %s", w.Body.String())
	}
	// Exactly one upstream attempt, no replay
	if atomic.LoadInt32(&attemptCount) != 1 {
		t.Fatalf("single-account 401 should make exactly 1 attempt, got %d", attemptCount)
	}
}

func TestResponsesCodexSingleAccount403NoReplay(t *testing.T) {
	// With exactly one enabled Codex account, 403 should be relayed directly.
	var attemptCount int32

	restore := oauth.SetTestCodexTokenURL("http://unused-invalid-url")
	defer restore()

	api := newCodexFailoverTestAPI(t, failoverTestOptions{
		maxFailovers: 2,
		accounts: []failoverTestAccount{
			{email: "a@test.com", accountID: "acct_a"},
		},
		upstreamHandler: func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&attemptCount, 1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"message":"forbidden","type":"permission_error"}}`))
		},
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"droid-oauth","input":"hi"}`))
	api.engine.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 relayed directly, got %d body=%s", w.Code, w.Body.String())
	}
	if atomic.LoadInt32(&attemptCount) != 1 {
		t.Fatalf("single-account 403 should make exactly 1 attempt, got %d", attemptCount)
	}
}

func TestResponsesCodexSingleAccountSuccessSameAsBefore(t *testing.T) {
	// Verify that a successful single-account request behaves exactly as before:
	// one token selection/refresh, one upstream request, same downstream response.
	var capturedAuth string
	var attemptCount int32

	api := newCodexFailoverTestAPI(t, failoverTestOptions{
		maxFailovers: 2,
		accounts: []failoverTestAccount{
			{email: "a@test.com", accountID: "acct_a"},
		},
		upstreamHandler: func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&attemptCount, 1)
			capturedAuth = r.Header.Get("Authorization")
			codexSuccessResponse(w)
		},
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"droid-oauth","input":"hi"}`))
	api.engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	if atomic.LoadInt32(&attemptCount) != 1 {
		t.Fatalf("single-account success should make exactly 1 attempt, got %d", attemptCount)
	}
	if capturedAuth != "Bearer access-token-0" {
		t.Fatalf("expected single-account auth header, got %s", capturedAuth)
	}
}

func TestResponsesCodexSingleAccount429NoFailover(t *testing.T) {
	// Single-account 429 should relay directly, no failover.
	var attemptCount int32

	api := newCodexFailoverTestAPI(t, failoverTestOptions{
		maxFailovers: 2,
		accounts: []failoverTestAccount{
			{email: "a@test.com", accountID: "acct_a"},
		},
		upstreamHandler: func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&attemptCount, 1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"rate limited"}}`))
		},
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"droid-oauth","input":"hi"}`))
	api.engine.ServeHTTP(w, req)

	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d body=%s", w.Code, w.Body.String())
	}
	if atomic.LoadInt32(&attemptCount) != 1 {
		t.Fatalf("single-account 429 should make exactly 1 attempt, got %d", attemptCount)
	}
}

// --------------- VAL-FAILOVER-012: xAI OAuth remains unchanged ---------------

func TestResponsesCodexFailoverXAI500DoesNotFailover(t *testing.T) {
	// xAI requests should NOT fail over even when the upstream returns 500.
	var attemptCount int32

	authDir := t.TempDir()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attemptCount, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"internal error"}}`))
	}))
	defer srv.Close()

	cfg := &config.Config{
		OAuth: config.OAuth{
			AuthDir: authDir,
			LoadBalancing: config.LoadBalancing{
				Strategy:          config.LoadBalancingFillFirst,
				MaxFailovers:      2,
				RateLimitCooldown: 60 * time.Second,
				ErrorCooldown:     30 * time.Second,
			},
		},
		Upstream: config.Upstream{
			HTTPTimeout:     5 * time.Second,
			StreamKeepAlive: 200 * time.Millisecond,
		},
		Models: []*config.Model{{
			Alias:            "droid-oauth",
			DisplayName:      "OAuth Test",
			FactoryProvider:  config.FactoryProviderOpenAI,
			UpstreamProtocol: config.UpstreamXAIResponses,
			OAuthProvider:    config.OAuthProviderXAI,
			BaseURL:          srv.URL,
			UpstreamModel:    "xai-upstream",
		}},
	}

	manager := oauth.NewManager(cfg)
	token := &oauth.Token{
		Type:         string(config.OAuthProviderXAI),
		AccessToken:  "xai-access-token",
		RefreshToken: "xai-refresh-token",
		Email:        "xai@test.com",
		Expired:      time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
	}
	if _, err := manager.SaveToken(token); err != nil {
		t.Fatal(err)
	}

	// Create a Codex pool even though xAI shouldn't use it.
	codexToken := &oauth.Token{
		Type:        string(config.OAuthProviderCodex),
		AccessToken: "codex-access-token",
		Email:       "codex@test.com",
		Expired:     time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
	}
	if _, err := manager.SaveToken(codexToken); err != nil {
		t.Fatal(err)
	}
	tokens, _ := manager.LoadTokens(config.OAuthProviderCodex)
	pool := oauth.NewAccountPool(tokens, time.Now, oauth.NewSelector(config.LoadBalancingFillFirst))

	router, _ := upstream.NewRouter(cfg.Models)
	logger := logrus.New()
	logger.SetOutput(io.Discard)
	api := &API{Cfg: cfg, Router: router, Client: upstream.NewClient(cfg), OAuth: manager, Pool: pool, Logger: logger}
	engine := gin.New()
	engine.POST("/v1/responses", api.Responses)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"droid-oauth","input":"hi"}`))
	engine.ServeHTTP(w, req)

	// xAI should get 500 directly, no failover.
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 for xAI (no failover), got %d body=%s", w.Code, w.Body.String())
	}
	if atomic.LoadInt32(&attemptCount) != 1 {
		t.Fatalf("xAI 500 should not fail over, got %d attempts", attemptCount)
	}

	// Verify Codex pool was not affected (no cooldowns, no unhealthy marks).
	snap := pool.Snapshot()
	for _, acct := range snap.Accounts {
		if acct.CooldownUntil != nil {
			t.Fatalf("Codex pool should have no cooldowns from xAI request, got %v for %s", acct.CooldownUntil, acct.Selector)
		}
		if !acct.Healthy {
			t.Fatalf("Codex pool should have no unhealthy marks from xAI request, got unhealthy for %s", acct.Selector)
		}
	}
}

func TestResponsesCodexFailoverXAI429DoesNotFailover(t *testing.T) {
	// xAI requests should NOT fail over even when the upstream returns 429.
	// This extends the existing TestResponsesCodexFailoverXAIUsesSingleTokenPath
	// to also verify no Codex pool side effects.
	var attemptCount int32

	authDir := t.TempDir()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attemptCount, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limited"}}`))
	}))
	defer srv.Close()

	cfg := &config.Config{
		OAuth: config.OAuth{
			AuthDir: authDir,
			LoadBalancing: config.LoadBalancing{
				Strategy:          config.LoadBalancingFillFirst,
				MaxFailovers:      2,
				RateLimitCooldown: 60 * time.Second,
				ErrorCooldown:     30 * time.Second,
			},
		},
		Upstream: config.Upstream{
			HTTPTimeout:     5 * time.Second,
			StreamKeepAlive: 200 * time.Millisecond,
		},
		Models: []*config.Model{{
			Alias:            "droid-oauth",
			DisplayName:      "OAuth Test",
			FactoryProvider:  config.FactoryProviderOpenAI,
			UpstreamProtocol: config.UpstreamXAIResponses,
			OAuthProvider:    config.OAuthProviderXAI,
			BaseURL:          srv.URL,
			UpstreamModel:    "xai-upstream",
		}},
	}

	manager := oauth.NewManager(cfg)
	token := &oauth.Token{
		Type:         string(config.OAuthProviderXAI),
		AccessToken:  "xai-access-token",
		RefreshToken: "xai-refresh-token",
		Email:        "xai@test.com",
		Expired:      time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
	}
	if _, err := manager.SaveToken(token); err != nil {
		t.Fatal(err)
	}

	codexToken := &oauth.Token{
		Type:        string(config.OAuthProviderCodex),
		AccessToken: "codex-access-token",
		Email:       "codex@test.com",
		Expired:     time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
	}
	if _, err := manager.SaveToken(codexToken); err != nil {
		t.Fatal(err)
	}
	tokens, _ := manager.LoadTokens(config.OAuthProviderCodex)
	pool := oauth.NewAccountPool(tokens, time.Now, oauth.NewSelector(config.LoadBalancingFillFirst))

	router, _ := upstream.NewRouter(cfg.Models)
	logger := logrus.New()
	logger.SetOutput(io.Discard)
	api := &API{Cfg: cfg, Router: router, Client: upstream.NewClient(cfg), OAuth: manager, Pool: pool, Logger: logger}
	engine := gin.New()
	engine.POST("/v1/responses", api.Responses)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"droid-oauth","input":"hi"}`))
	engine.ServeHTTP(w, req)

	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 for xAI (no failover), got %d body=%s", w.Code, w.Body.String())
	}
	if atomic.LoadInt32(&attemptCount) != 1 {
		t.Fatalf("xAI 429 should not fail over, got %d attempts", attemptCount)
	}

	// Verify Codex pool has no rate-limit marks from xAI request.
	snap := pool.Snapshot()
	for _, acct := range snap.Accounts {
		if acct.RateLimitedUntil != nil {
			t.Fatalf("Codex pool should have no rate-limit marks from xAI request, got %v for %s", acct.RateLimitedUntil, acct.Selector)
		}
		if !acct.Healthy {
			t.Fatalf("Codex pool should have no unhealthy marks from xAI request, got unhealthy for %s", acct.Selector)
		}
	}
}

func TestResponsesCodexFailoverXAI401DoesNotUseCodexPool(t *testing.T) {
	// xAI 401 should use the single-token path, not the Codex pool
	// force-refresh mechanism.
	var attemptCount int32

	authDir := t.TempDir()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attemptCount, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"unauthorized"}}`))
	}))
	defer srv.Close()

	cfg := &config.Config{
		OAuth: config.OAuth{
			AuthDir: authDir,
			LoadBalancing: config.LoadBalancing{
				Strategy:          config.LoadBalancingFillFirst,
				MaxFailovers:      2,
				RateLimitCooldown: 60 * time.Second,
				ErrorCooldown:     30 * time.Second,
			},
		},
		Upstream: config.Upstream{
			HTTPTimeout:     5 * time.Second,
			StreamKeepAlive: 200 * time.Millisecond,
		},
		Models: []*config.Model{{
			Alias:            "droid-oauth",
			DisplayName:      "OAuth Test",
			FactoryProvider:  config.FactoryProviderOpenAI,
			UpstreamProtocol: config.UpstreamXAIResponses,
			OAuthProvider:    config.OAuthProviderXAI,
			BaseURL:          srv.URL,
			UpstreamModel:    "xai-upstream",
		}},
	}

	manager := oauth.NewManager(cfg)
	token := &oauth.Token{
		Type:         string(config.OAuthProviderXAI),
		AccessToken:  "xai-access-token",
		RefreshToken: "xai-refresh-token",
		Email:        "xai@test.com",
		Expired:      time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
	}
	if _, err := manager.SaveToken(token); err != nil {
		t.Fatal(err)
	}

	codexToken := &oauth.Token{
		Type:        string(config.OAuthProviderCodex),
		AccessToken: "codex-access-token",
		Email:       "codex@test.com",
		Expired:     time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
	}
	if _, err := manager.SaveToken(codexToken); err != nil {
		t.Fatal(err)
	}
	tokens, _ := manager.LoadTokens(config.OAuthProviderCodex)
	pool := oauth.NewAccountPool(tokens, time.Now, oauth.NewSelector(config.LoadBalancingFillFirst))

	router, _ := upstream.NewRouter(cfg.Models)
	logger := logrus.New()
	logger.SetOutput(io.Discard)
	api := &API{Cfg: cfg, Router: router, Client: upstream.NewClient(cfg), OAuth: manager, Pool: pool, Logger: logger}
	engine := gin.New()
	engine.POST("/v1/responses", api.Responses)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"droid-oauth","input":"hi"}`))
	engine.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for xAI (single-token path), got %d body=%s", w.Code, w.Body.String())
	}
	if atomic.LoadInt32(&attemptCount) != 1 {
		t.Fatalf("xAI 401 should not fail over or refresh+replay, got %d attempts", attemptCount)
	}

	// Verify Codex pool state is completely untouched.
	snap := pool.Snapshot()
	for _, acct := range snap.Accounts {
		if !acct.Healthy {
			t.Fatalf("Codex pool should not be affected by xAI 401, got unhealthy for %s", acct.Selector)
		}
		if acct.InFlight != 0 {
			t.Fatalf("Codex pool should have no in-flight from xAI request, got %d for %s", acct.InFlight, acct.Selector)
		}
	}
}
