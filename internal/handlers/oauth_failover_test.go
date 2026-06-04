package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
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
