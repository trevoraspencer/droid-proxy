package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"

	"github.com/trevoraspencer/droid-proxy/internal/config"
	"github.com/trevoraspencer/droid-proxy/internal/oauth"
	"github.com/trevoraspencer/droid-proxy/internal/upstream"
)

// failoverTestAccount describes a Codex account for failover tests.
type failoverTestAccount struct {
	email       string
	accountID   string
	disabled    bool
	usedPercent float64 // optional Codex primary-window used_percent for quota routing
}

// failoverTestOptions configures a failover test API.
type failoverTestOptions struct {
	maxFailovers         int
	rateLimitCooldown    time.Duration
	errorCooldown        time.Duration
	strategy             config.LoadBalancingStrategy
	accounts             []failoverTestAccount
	upstreamHandler      http.HandlerFunc
	pinnedAccount        string
	modelAlias           string
	upstreamModel        string
	extraArgs            map[string]any
	responseBodyMaxBytes int64 // if > 0, override default response body limit
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
	modelAlias := opts.modelAlias
	if modelAlias == "" {
		modelAlias = "droid-oauth"
	}
	upstreamModel := opts.upstreamModel
	if upstreamModel == "" {
		upstreamModel = "codex-upstream"
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
			HTTPTimeout:          5 * time.Second,
			StreamKeepAlive:      200 * time.Millisecond,
			ResponseBodyMaxBytes: opts.responseBodyMaxBytes,
		},
		Models: []*config.Model{{
			Alias:            modelAlias,
			DisplayName:      "OAuth Test",
			FactoryProvider:  config.FactoryProviderOpenAI,
			UpstreamProtocol: config.UpstreamCodexResponses,
			OAuthProvider:    config.OAuthProviderCodex,
			BaseURL:          srv.URL,
			UpstreamModel:    upstreamModel,
			ExtraArgs:        opts.extraArgs,
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
		if acct.usedPercent > 0 {
			token.CodexQuota = &oauth.CodexQuota{
				Primary: &oauth.CodexQuotaWindow{UsedPercent: acct.usedPercent},
			}
		}
		if _, err := manager.SaveToken(token); err != nil {
			t.Fatal(err)
		}
	}

	tokens, err := manager.LoadTokens(config.OAuthProviderCodex)
	if err != nil {
		t.Fatal(err)
	}
	lb := config.LoadBalancing{
		Strategy:          strategy,
		MaxFailovers:      opts.maxFailovers,
		RateLimitCooldown: rlc,
		ErrorCooldown:     ec,
	}
	var affinity *oauth.AffinityStore
	if strategy == config.LoadBalancingSticky {
		affinityPath := filepath.Join(authDir, "conversation_affinity.json")
		affinity, err = oauth.NewAffinityStore(oauth.AffinityOptions{Path: affinityPath})
		if err != nil {
			t.Fatal(err)
		}
	}
	sel := oauth.NewSelector(strategy)
	pool := oauth.NewAccountPool(tokens, time.Now, lb, affinity, sel)

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

func TestResponsesStickyClearsAffinityOnNonRetryable4xx(t *testing.T) {
	var mu sync.Mutex
	var attempts []string

	api := newCodexFailoverTestAPI(t, failoverTestOptions{
		strategy:     config.LoadBalancingSticky,
		maxFailovers: 1,
		accounts: []failoverTestAccount{
			{email: "a@test.com", accountID: "acct_a", usedPercent: 90},
			{email: "b@test.com", accountID: "acct_b", usedPercent: 10},
		},
		upstreamHandler: func(w http.ResponseWriter, r *http.Request) {
			auth := authTokenFromRequest(r)
			mu.Lock()
			attempts = append(attempts, auth)
			mu.Unlock()
			if auth == "access-token-0" {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":{"message":"bad request"}}`))
				return
			}
			codexSuccessResponse(w)
		},
	})

	tokens, err := api.api.OAuth.LoadTokens(config.OAuthProviderCodex)
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) < 2 {
		t.Fatal("expected two codex tokens")
	}
	api.api.Pool.BindConversation("sticky-sess", tokens[0].Path())

	req1 := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"droid-oauth","input":"hi"}`))
	req1.Header.Set("session_id", "sticky-sess")
	w1 := httptest.NewRecorder()
	api.engine.ServeHTTP(w1, req1)
	if w1.Code != http.StatusBadRequest {
		t.Fatalf("request 1: expected 400, got %d body=%s", w1.Code, w1.Body.String())
	}

	attempts = nil
	req2 := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"droid-oauth","input":"hi"}`))
	req2.Header.Set("session_id", "sticky-sess")
	w2 := httptest.NewRecorder()
	api.engine.ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("request 2: expected 200, got %d body=%s", w2.Code, w2.Body.String())
	}
	if len(attempts) != 1 || attempts[0] != "access-token-1" {
		t.Fatalf("request 2: expected only access-token-1, got %v", attempts)
	}
}

func TestResponsesStickyBindsOnSuccess(t *testing.T) {
	var mu sync.Mutex
	var attempts []string

	api := newCodexFailoverTestAPI(t, failoverTestOptions{
		strategy: config.LoadBalancingSticky,
		accounts: []failoverTestAccount{
			{email: "a@test.com", accountID: "acct_a", usedPercent: 10},
			{email: "b@test.com", accountID: "acct_b", usedPercent: 90},
		},
		upstreamHandler: func(w http.ResponseWriter, r *http.Request) {
			auth := authTokenFromRequest(r)
			mu.Lock()
			attempts = append(attempts, auth)
			mu.Unlock()
			if auth != "access-token-0" {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"error":{"message":"wrong account"}}`))
				return
			}
			codexSuccessResponse(w)
		},
	})

	req1 := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"droid-oauth","input":"hi"}`))
	req1.Header.Set("session_id", "sticky-ok")
	w1 := httptest.NewRecorder()
	api.engine.ServeHTTP(w1, req1)
	if w1.Code != http.StatusOK {
		t.Fatalf("request 1: expected 200, got %d body=%s", w1.Code, w1.Body.String())
	}

	attempts = nil
	req2 := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"droid-oauth","input":"hi"}`))
	req2.Header.Set("session_id", "sticky-ok")
	w2 := httptest.NewRecorder()
	api.engine.ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("request 2: expected 200, got %d body=%s", w2.Code, w2.Body.String())
	}
	if len(attempts) != 1 || attempts[0] != "access-token-0" {
		t.Fatalf("request 2: expected sticky access-token-0, got %v", attempts)
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
	pool := oauth.NewAccountPool(tokens, time.Now, oauth.TestPoolLB(), nil, oauth.NewSelector(config.LoadBalancingFillFirst))
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

func TestResponsesCodexGPT56UnavailableDoesNotDowngradeOrFailOver(t *testing.T) {
	var attemptCount int32
	var capturedModel string

	api := newCodexFailoverTestAPI(t, failoverTestOptions{
		maxFailovers:  2,
		modelAlias:    "gpt-5.6",
		upstreamModel: "gpt-5.6-sol",
		accounts: []failoverTestAccount{
			{email: "a@test.com", accountID: "acct_a"},
			{email: "b@test.com", accountID: "acct_b"},
			{email: "c@test.com", accountID: "acct_c"},
		},
		upstreamHandler: func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&attemptCount, 1)
			body, _ := io.ReadAll(r.Body)
			var payload map[string]any
			_ = json.Unmarshal(body, &payload)
			capturedModel, _ = payload["model"].(string)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"message":"gpt-5.6-sol is unavailable for this account","type":"model_not_found"}}`))
		},
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.6","input":"hi"}`))
	api.engine.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "gpt-5.6-sol is unavailable") {
		t.Fatalf("GPT-5.6 availability error was not surfaced: status=%d body=%s", w.Code, w.Body.String())
	}
	if capturedModel != "gpt-5.6-sol" {
		t.Fatalf("forwarded model = %q, want gpt-5.6-sol", capturedModel)
	}
	if atomic.LoadInt32(&attemptCount) != 1 {
		t.Fatalf("GPT-5.6 4xx must not fail over or downgrade, got %d attempts", attemptCount)
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
	pool := oauth.NewAccountPool(tokens, time.Now, oauth.TestPoolLB(), nil, oauth.NewSelector(config.LoadBalancingFillFirst))
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
	// use the pool even when a pool is configured. A 429 gets the bounded
	// in-place capacity backoff on the same token, never pool failover.
	shrinkCapacityDelays(t)
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
	pool := oauth.NewAccountPool(tokens, time.Now, oauth.TestPoolLB(), nil, oauth.NewSelector(config.LoadBalancingFillFirst))

	router, _ := upstream.NewRouter(cfg.Models)
	logger := logrus.New()
	logger.SetOutput(io.Discard)
	api := &API{Cfg: cfg, Router: router, Client: upstream.NewClient(cfg), OAuth: manager, Pool: pool, Logger: logger}

	engine := gin.New()
	engine.POST("/v1/responses", api.Responses)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"droid-oauth","input":"hi"}`))
	engine.ServeHTTP(w, req)

	// xAI should get 429 relayed after the in-place capacity retries.
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 for xAI (no failover), got %d body=%s", w.Code, w.Body.String())
	}
	// Same-token capacity retries only — no pool failover.
	if got := atomic.LoadInt32(&attemptCount); got != int32(1+capacityRetryMaxAttempts) {
		t.Fatalf("xAI should make 1+%d same-token attempts, got %d", capacityRetryMaxAttempts, got)
	}
}

// --------------- Payload replay is stable across attempts ---------------

func TestResponsesCodexFailoverPayloadReplayStable(t *testing.T) {
	var mu sync.Mutex
	var capturedPayloads []map[string]any

	api := newCodexFailoverTestAPI(t, failoverTestOptions{
		maxFailovers:  2,
		modelAlias:    "gpt-5.6-fast",
		upstreamModel: "gpt-5.6-sol",
		extraArgs:     map[string]any{"service_tier": "priority"},
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
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.6-fast","input":"hi"}`))
	api.engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	if len(capturedPayloads) != 3 {
		t.Fatalf("expected 3 captured payloads, got %d", len(capturedPayloads))
	}
	if capturedPayloads[0]["model"] != "gpt-5.6-sol" {
		t.Fatalf("failover changed the requested model instead of only the account: %#v", capturedPayloads[0])
	}
	if capturedPayloads[0]["service_tier"] != "priority" {
		t.Fatalf("fast preset lost priority service tier before failover replay: %#v", capturedPayloads[0])
	}

	// Verify model, service tier, stream, instructions, and store are identical across attempts.
	for i := 1; i < len(capturedPayloads); i++ {
		if capturedPayloads[i]["model"] != capturedPayloads[0]["model"] {
			t.Fatalf("model mismatch attempt 0 vs %d: %v vs %v", i, capturedPayloads[0]["model"], capturedPayloads[i]["model"])
		}
		if capturedPayloads[i]["service_tier"] != capturedPayloads[0]["service_tier"] {
			t.Fatalf("service_tier mismatch attempt 0 vs %d: %v vs %v", i, capturedPayloads[0]["service_tier"], capturedPayloads[i]["service_tier"])
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

func TestCodexRateLimitCooldownUsesExhaustedWindowReset(t *testing.T) {
	fallback := 45 * time.Second
	primaryReset := time.Now().Add(time.Hour).Unix()
	secondaryReset := time.Now().Add(7 * 24 * time.Hour).Unix()
	quota := &oauth.CodexQuota{
		Primary:   &oauth.CodexQuotaWindow{UsedPercent: 100, ResetAt: &primaryReset, LimitReached: true},
		Secondary: &oauth.CodexQuotaWindow{UsedPercent: 30, ResetAt: &secondaryReset, LimitReached: false},
	}

	got := codexRateLimitCooldown(nil, quota, fallback)
	want := time.Unix(primaryReset, 0).UTC()
	if !got.Truncate(time.Second).Equal(want.Truncate(time.Second)) {
		t.Fatalf("cooldown = %v, want exhausted primary reset %v", got, want)
	}

	headers := http.Header{}
	headers.Set("Retry-After", "120")
	before := time.Now()
	got = codexRateLimitCooldown(headers, quota, fallback)
	if got.Before(before.Add(115*time.Second)) || got.After(before.Add(125*time.Second)) {
		t.Fatalf("cooldown = %v, want Retry-After around %v", got, before.Add(120*time.Second))
	}

	quota.Primary.LimitReached = false
	before = time.Now()
	got = codexRateLimitCooldown(nil, quota, fallback)
	if got.Before(before.Add(40*time.Second)) || got.After(before.Add(50*time.Second)) {
		t.Fatalf("cooldown = %v, want fallback around %v", got, before.Add(fallback))
	}

	quota.Primary.LimitReached = true
	quota.Primary.ResetAt = nil
	before = time.Now()
	got = codexRateLimitCooldown(nil, quota, fallback)
	if got.Before(before.Add(40*time.Second)) || got.After(before.Add(50*time.Second)) {
		t.Fatalf("cooldown = %v, want fallback when exhausted window has no reset around %v", got, before.Add(fallback))
	}
}

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
	// When no Retry-After and no exhausted quota reset, use configured rate_limit_cooldown.
	resetAt := time.Now().Add(7 * 24 * time.Hour).Unix()
	api := newCodexFailoverTestAPI(t, failoverTestOptions{
		maxFailovers:      0,
		rateLimitCooldown: 45 * time.Second,
		accounts: []failoverTestAccount{
			{email: "a@test.com", accountID: "acct_a"},
		},
		upstreamHandler: func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("x-codex-primary-used-percent", "30")
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

func TestResponsesCodex401RefreshCancellationDoesNotMarkUnhealthy(t *testing.T) {
	// When the client context is cancelled while force-refresh is in flight,
	// the selected account must be released without being marked unhealthy
	// and failover must not continue to another account.
	var mu sync.Mutex
	var attempts []string
	var cancelFn context.CancelFunc

	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cancelFn != nil {
			cancelFn()
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"cancelled"}`))
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

			if auth != "access-token-0" {
				codexSuccessResponse(w)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"message":"unauthorized"}}`))
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelFn = cancel

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"droid-oauth","input":"hi"}`)).WithContext(ctx)
	api.engine.ServeHTTP(w, req)

	mu.Lock()
	defer mu.Unlock()
	if len(attempts) != 1 || attempts[0] != "access-token-0" {
		t.Fatalf("expected only the cancelled account attempt, got %v", attempts)
	}

	snap := api.api.Pool.Snapshot()
	for _, acct := range snap.Accounts {
		if acct.Selector == "a@test.com" && !acct.Healthy {
			t.Fatal("cancelled force-refresh marked the account unhealthy")
		}
		if acct.InFlight != 0 {
			t.Fatalf("expected 0 in-flight for %s, got %d", acct.Selector, acct.InFlight)
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
	pool := oauth.NewAccountPool(tokens, time.Now, oauth.TestPoolLB(), nil, oauth.NewSelector(config.LoadBalancingFillFirst))

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
	// to also verify no Codex pool side effects. The 429 is retried in place
	// (capacity backoff) on the same token before being relayed.
	shrinkCapacityDelays(t)
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
	pool := oauth.NewAccountPool(tokens, time.Now, oauth.TestPoolLB(), nil, oauth.NewSelector(config.LoadBalancingFillFirst))

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
	if got := atomic.LoadInt32(&attemptCount); got != int32(1+capacityRetryMaxAttempts) {
		t.Fatalf("xAI 429 should retry in place without failover, want %d attempts, got %d", 1+capacityRetryMaxAttempts, got)
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
	pool := oauth.NewAccountPool(tokens, time.Now, oauth.TestPoolLB(), nil, oauth.NewSelector(config.LoadBalancingFillFirst))

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

// ============================================================================
// VAL-FAILOVER-007: Streaming retry boundary is before downstream commit
// ============================================================================

func TestResponsesCodexStreamingPreCommit5xxFailover(t *testing.T) {
	// Streaming request gets 500 before SSE starts → should fail over to next account.
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
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"error":{"message":"internal error"}}`))
				return
			}
			// Second attempt: streaming success.
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
		t.Fatalf("expected 200 after 5xx streaming failover, got %d body=%s", w.Code, w.Body.String())
	}
	if atomic.LoadInt32(&attemptCount) != 2 {
		t.Fatalf("expected 2 attempts, got %d", attemptCount)
	}
	if !strings.Contains(w.Body.String(), "response.completed") {
		t.Fatalf("expected streaming response, got %s", w.Body.String())
	}
}

func TestResponsesCodexStreamingPreCommitTransportErrorFailover(t *testing.T) {
	// Streaming request gets transport error on first attempt → fail over.
	var attemptCount int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(atomic.AddInt32(&attemptCount, 1))
		if n == 1 {
			// Force close the connection to simulate transport error.
			hj, ok := w.(http.Hijacker)
			if ok {
				conn, _, _ := hj.Hijack()
				_ = conn.Close()
				return
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		_, _ = w.Write([]byte("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"output\":[]}}\n\n"))
		flusher.Flush()
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
	pool := oauth.NewAccountPool(tokens, time.Now, oauth.TestPoolLB(), nil, oauth.NewSelector(config.LoadBalancingFillFirst))
	router, _ := upstream.NewRouter(cfg.Models)
	logger := logrus.New()
	logger.SetOutput(io.Discard)
	api := &API{Cfg: cfg, Router: router, Client: upstream.NewClient(cfg), OAuth: manager, Pool: pool, Logger: logger}
	engine := gin.New()
	engine.POST("/v1/responses", api.Responses)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"droid-oauth","input":"hi","stream":true}`))
	engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 after transport error failover, got %d body=%s", w.Code, w.Body.String())
	}
	if atomic.LoadInt32(&attemptCount) != 2 {
		t.Fatalf("expected 2 attempts, got %d", attemptCount)
	}
}

func TestResponsesCodexStreamingPreCommit401Failover(t *testing.T) {
	// Streaming request gets 401 → force refresh + replay. If replay also
	// returns 401, fail over to the next account.
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

			// Both original and refreshed tokens for account 0 return 401.
			if auth == "access-token-0" || auth == "refreshed-access-token-0" {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":{"message":"unauthorized"}}`))
				return
			}
			// Account 1 succeeds with streaming.
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
		t.Fatalf("expected 200 after 401 streaming failover, got %d body=%s", w.Code, w.Body.String())
	}
	mu.Lock()
	defer mu.Unlock()
	// 3 attempts: initial 401 (acct 0) + replay 401 (acct 0 refreshed) + success (acct 1)
	if len(attempts) != 3 {
		t.Fatalf("expected 3 upstream attempts, got %d: %v", len(attempts), attempts)
	}
	if !strings.Contains(w.Body.String(), "response.completed") {
		t.Fatalf("expected streaming response, got %s", w.Body.String())
	}
}

func TestResponsesCodexStreamingPostCommitNoRetryOnMidStreamFailure(t *testing.T) {
	// Once streaming begins (200 OK + SSE headers committed), later failure
	// does NOT trigger retry on another account.
	var attemptCount int32

	api := newCodexFailoverTestAPI(t, failoverTestOptions{
		maxFailovers: 1,
		accounts: []failoverTestAccount{
			{email: "a@test.com", accountID: "acct_a"},
			{email: "b@test.com", accountID: "acct_b"},
		},
		upstreamHandler: func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&attemptCount, 1)
			// Start streaming successfully, then truncate without terminal marker.
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			flusher := w.(http.Flusher)
			_, _ = w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n"))
			flusher.Flush()
			// Then close the response body to simulate truncation.
			// The stream forwarder will detect this as a non-terminal EOF.
		},
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"droid-oauth","input":"hi","stream":true}`))
	api.engine.ServeHTTP(w, req)

	// Only 1 attempt should have been made (no retry after streaming commit).
	if atomic.LoadInt32(&attemptCount) != 1 {
		t.Fatalf("expected exactly 1 upstream attempt (no retry after commit), got %d", attemptCount)
	}
	// Response should show the truncated stream error, not a second account's response.
	if !strings.Contains(w.Body.String(), "upstream stream ended before terminal marker") {
		t.Fatalf("expected truncation error in body, got %s", w.Body.String())
	}
}

// ============================================================================
// VAL-FAILOVER-008: In-flight accounting spans request and stream lifetime
// ============================================================================

func TestCodexInFlightNonStreamingReturnsToZero(t *testing.T) {
	// Non-streaming request: in-flight should be 0 before request, >0 during,
	// and 0 after completion.
	api := newCodexFailoverTestAPI(t, failoverTestOptions{
		maxFailovers: 0,
		accounts: []failoverTestAccount{
			{email: "a@test.com", accountID: "acct_a"},
		},
		upstreamHandler: func(w http.ResponseWriter, r *http.Request) {
			codexSuccessResponse(w)
		},
	})

	snapBefore := api.api.Pool.Snapshot()
	for _, acct := range snapBefore.Accounts {
		if acct.InFlight != 0 {
			t.Fatalf("expected 0 in-flight before request, got %d for %s", acct.InFlight, acct.Selector)
		}
	}

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"droid-oauth","input":"hi"}`))
	api.engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}

	snapAfter := api.api.Pool.Snapshot()
	for _, acct := range snapAfter.Accounts {
		if acct.InFlight != 0 {
			t.Fatalf("expected 0 in-flight after completion, got %d for %s", acct.InFlight, acct.Selector)
		}
	}
}

func TestCodexInFlightStreamingReturnsToZeroAfterCompletion(t *testing.T) {
	// Streaming request: in-flight should return to 0 after stream completes.
	api := newCodexFailoverTestAPI(t, failoverTestOptions{
		maxFailovers: 0,
		accounts: []failoverTestAccount{
			{email: "a@test.com", accountID: "acct_a"},
		},
		upstreamHandler: func(w http.ResponseWriter, r *http.Request) {
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

	snapAfter := api.api.Pool.Snapshot()
	for _, acct := range snapAfter.Accounts {
		if acct.InFlight != 0 {
			t.Fatalf("expected 0 in-flight after streaming completion, got %d for %s", acct.InFlight, acct.Selector)
		}
	}
}

func TestCodexInFlightReturnsToZeroOnTransportError(t *testing.T) {
	// Transport error during request: in-flight should return to 0.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if ok {
			conn, _, _ := hj.Hijack()
			_ = conn.Close()
			return
		}
	}))
	defer srv.Close()

	authDir := t.TempDir()
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
	token := &oauth.Token{
		Type:         string(config.OAuthProviderCodex),
		AccessToken:  "access-token-0",
		RefreshToken: "refresh-token-0",
		Email:        "a@test.com",
		AccountID:    "acct_a",
		Expired:      time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
	}
	if _, err := manager.SaveToken(token); err != nil {
		t.Fatal(err)
	}
	tokens, _ := manager.LoadTokens(config.OAuthProviderCodex)
	pool := oauth.NewAccountPool(tokens, time.Now, oauth.TestPoolLB(), nil, oauth.NewSelector(config.LoadBalancingFillFirst))
	router, _ := upstream.NewRouter(cfg.Models)
	logger := logrus.New()
	logger.SetOutput(io.Discard)
	api := &API{Cfg: cfg, Router: router, Client: upstream.NewClient(cfg), OAuth: manager, Pool: pool, Logger: logger}
	engine := gin.New()
	engine.POST("/v1/responses", api.Responses)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"droid-oauth","input":"hi"}`))
	engine.ServeHTTP(w, req)

	// Transport error should be relayed as 502.
	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected 502 for transport error, got %d", w.Code)
	}

	snapAfter := api.Pool.Snapshot()
	for _, acct := range snapAfter.Accounts {
		if acct.InFlight != 0 {
			t.Fatalf("expected 0 in-flight after transport error, got %d for %s", acct.InFlight, acct.Selector)
		}
	}
}

func TestCodexInFlightReturnsToZeroOnRetryable429(t *testing.T) {
	// 429 on first account with failover: in-flight should return to 0 after each attempt
	// and after final exhaustion.
	api := newCodexFailoverTestAPI(t, failoverTestOptions{
		maxFailovers: 0,
		accounts: []failoverTestAccount{
			{email: "a@test.com", accountID: "acct_a"},
		},
		upstreamHandler: func(w http.ResponseWriter, r *http.Request) {
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

	snapAfter := api.api.Pool.Snapshot()
	for _, acct := range snapAfter.Accounts {
		if acct.InFlight != 0 {
			t.Fatalf("expected 0 in-flight after 429, got %d for %s", acct.InFlight, acct.Selector)
		}
	}
}

func TestCodexInFlightReturnsToZeroOnOversizedSuccessBody(t *testing.T) {
	// When a successful non-streaming response body exceeds the size limit,
	// the pool lease should still be released.
	api := newCodexFailoverTestAPI(t, failoverTestOptions{
		maxFailovers:         0,
		responseBodyMaxBytes: 1024, // 1 KiB limit
		accounts: []failoverTestAccount{
			{email: "a@test.com", accountID: "acct_a"},
		},
		upstreamHandler: func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			// Return a response body that exceeds the configured size limit.
			largeBody := make([]byte, 11*1024) // 11 KiB > 1 KiB limit
			for i := range largeBody {
				largeBody[i] = 'a'
			}
			_, _ = w.Write(largeBody)
		},
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"droid-oauth","input":"hi"}`))
	api.engine.ServeHTTP(w, req)

	// Should get an error about oversized body.
	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected 502 for oversized body, got %d", w.Code)
	}

	snapAfter := api.api.Pool.Snapshot()
	for _, acct := range snapAfter.Accounts {
		if acct.InFlight != 0 {
			t.Fatalf("expected 0 in-flight after oversized body error, got %d for %s", acct.InFlight, acct.Selector)
		}
	}
}

func TestCodexInFlightReturnsToZeroOnRefreshFailure(t *testing.T) {
	// When token refresh fails, the pool lease should still be released.
	restore := oauth.SetTestCodexTokenURL("http://127.0.0.1:0") // unreachable
	defer restore()

	// Create an expired token that needs refresh.
	authDir := t.TempDir()
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
		Upstream: config.Upstream{
			HTTPTimeout:     2 * time.Second,
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
	token := &oauth.Token{
		Type:         string(config.OAuthProviderCodex),
		AccessToken:  "access-token-0",
		RefreshToken: "refresh-token-0",
		Email:        "a@test.com",
		AccountID:    "acct_a",
		Expired:      time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339), // expired
	}
	if _, err := manager.SaveToken(token); err != nil {
		t.Fatal(err)
	}
	tokens, _ := manager.LoadTokens(config.OAuthProviderCodex)
	pool := oauth.NewAccountPool(tokens, time.Now, oauth.TestPoolLB(), nil, oauth.NewSelector(config.LoadBalancingFillFirst))
	router, _ := upstream.NewRouter(cfg.Models)
	logger := logrus.New()
	logger.SetOutput(io.Discard)
	api := &API{Cfg: cfg, Router: router, Client: upstream.NewClient(cfg), OAuth: manager, Pool: pool, Logger: logger}
	engine := gin.New()
	engine.POST("/v1/responses", api.Responses)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"droid-oauth","input":"hi"}`))
	engine.ServeHTTP(w, req)

	snapAfter := pool.Snapshot()
	for _, acct := range snapAfter.Accounts {
		if acct.InFlight != 0 {
			t.Fatalf("expected 0 in-flight after refresh failure, got %d for %s", acct.InFlight, acct.Selector)
		}
	}
}

func TestCodexInFlightStreamingTruncationReturnsToZero(t *testing.T) {
	// When a streaming response truncates (idle timeout / no terminal marker),
	// the pool lease should still be released.
	api := newCodexFailoverTestAPI(t, failoverTestOptions{
		maxFailovers: 0,
		accounts: []failoverTestAccount{
			{email: "a@test.com", accountID: "acct_a"},
		},
		upstreamHandler: func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			flusher := w.(http.Flusher)
			// Send a non-terminal event then close.
			_, _ = w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\n"))
			flusher.Flush()
			// Body ends here without terminal marker → truncation.
		},
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"droid-oauth","input":"hi","stream":true}`))
	api.engine.ServeHTTP(w, req)

	snapAfter := api.api.Pool.Snapshot()
	for _, acct := range snapAfter.Accounts {
		if acct.InFlight != 0 {
			t.Fatalf("expected 0 in-flight after stream truncation, got %d for %s", acct.InFlight, acct.Selector)
		}
	}
}

func TestCodexInFlightStreamingFailoverReturnsToZero(t *testing.T) {
	// After streaming failover (pre-commit), all accounts should have 0 in-flight.
	api := newCodexFailoverTestAPI(t, failoverTestOptions{
		maxFailovers: 1,
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

	snapAfter := api.api.Pool.Snapshot()
	for _, acct := range snapAfter.Accounts {
		if acct.InFlight != 0 {
			t.Fatalf("expected 0 in-flight for %s after streaming failover, got %d", acct.Selector, acct.InFlight)
		}
	}
}

// ============================================================================
// VAL-FAILOVER-015: Downstream cancellation does not cause failover or cooldown
// ============================================================================

func TestCodexDownstreamCancellationDuringStreamingNoFailover(t *testing.T) {
	// When the client cancels during SSE forwarding, the proxy should NOT
	// try another account and should NOT mark the attempted account in cooldown.
	var (
		attemptCount    int32
		upstreamStarted = make(chan struct{})
		handlerDone     = make(chan struct{})
	)

	api := newCodexFailoverTestAPI(t, failoverTestOptions{
		maxFailovers: 1,
		accounts: []failoverTestAccount{
			{email: "a@test.com", accountID: "acct_a"},
			{email: "b@test.com", accountID: "acct_b"},
		},
		upstreamHandler: func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&attemptCount, 1)
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			flusher := w.(http.Flusher)
			close(upstreamStarted)
			defer close(handlerDone)
			// Write events slowly; client will cancel during streaming.
			for i := 0; i < 100; i++ {
				_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"chunk%d\"}\n\n", i)
				flusher.Flush()
				time.Sleep(50 * time.Millisecond)
			}
		},
	})

	// Use a real HTTP server so we can cancel the request context.
	srv := httptest.NewServer(api.engine)
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/v1/responses", strings.NewReader(`{"model":"droid-oauth","input":"hi","stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")

	// Start the request in a goroutine.
	done := make(chan struct{})
	go func() {
		defer close(done)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return
		}
		defer resp.Body.Close()
		// Read until error/EOF.
		buf := make([]byte, 1024)
		for {
			_, err := resp.Body.Read(buf)
			if err != nil {
				break
			}
		}
	}()

	// Wait for upstream to start streaming, then cancel the client.
	select {
	case <-upstreamStarted:
		// Give a small delay to ensure the stream has committed headers.
		time.Sleep(100 * time.Millisecond)
		cancel()
	case <-time.After(5 * time.Second):
		t.Fatal("upstream never started")
	}

	// Wait for the request goroutine to finish.
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("request goroutine did not finish")
	}

	// Wait for the handler to finish so pool state is settled.
	select {
	case <-handlerDone:
	case <-time.After(10 * time.Second):
		t.Fatal("handler goroutine did not finish")
	}

	// Only 1 upstream attempt should have been made.
	if atomic.LoadInt32(&attemptCount) != 1 {
		t.Fatalf("expected exactly 1 upstream attempt (no failover on cancel), got %d", attemptCount)
	}

	// Verify no cooldown on the attempted account.
	snap := api.api.Pool.Snapshot()
	for _, acct := range snap.Accounts {
		if acct.CooldownUntil != nil {
			t.Fatalf("account %s should not be in cooldown after cancellation, got %v", acct.Selector, acct.CooldownUntil)
		}
		if acct.InFlight != 0 {
			t.Fatalf("account %s should have 0 in-flight after cancellation, got %d", acct.Selector, acct.InFlight)
		}
	}
}

func TestCodexDownstreamCancellationBeforeUpstreamNoFailover(t *testing.T) {
	// When the client cancels before the upstream responds, the proxy should
	// NOT try another account and should NOT mark the attempted account in cooldown.
	var (
		attemptCount int32
		gotRequest   = make(chan struct{})
		handlerDone  = make(chan struct{})
	)

	api := newCodexFailoverTestAPI(t, failoverTestOptions{
		maxFailovers: 1,
		accounts: []failoverTestAccount{
			{email: "a@test.com", accountID: "acct_a"},
			{email: "b@test.com", accountID: "acct_b"},
		},
		upstreamHandler: func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&attemptCount, 1)
			close(gotRequest)
			defer close(handlerDone)
			// Wait for client or a reasonable timeout.
			select {
			case <-r.Context().Done():
			case <-time.After(10 * time.Second):
			}
		},
	})

	srv := httptest.NewServer(api.engine)
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/v1/responses", strings.NewReader(`{"model":"droid-oauth","input":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = http.DefaultClient.Do(req)
	}()

	// Wait for upstream to receive the request, then cancel.
	select {
	case <-gotRequest:
		cancel()
	case <-time.After(5 * time.Second):
		t.Fatal("upstream never received request")
	}

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("request goroutine did not finish")
	}

	// Wait for the upstream handler to finish (bounded by the select timeout).
	select {
	case <-handlerDone:
	case <-time.After(15 * time.Second):
		t.Fatal("handler goroutine did not finish")
	}

	// Only 1 upstream attempt (no failover on client cancel).
	if atomic.LoadInt32(&attemptCount) != 1 {
		t.Fatalf("expected exactly 1 upstream attempt, got %d", attemptCount)
	}

	// No cooldown should be applied.
	snap := api.api.Pool.Snapshot()
	for _, acct := range snap.Accounts {
		if acct.CooldownUntil != nil {
			t.Fatalf("account %s should not be in cooldown after cancellation, got %v", acct.Selector, acct.CooldownUntil)
		}
		if acct.InFlight != 0 {
			t.Fatalf("account %s should have 0 in-flight after cancellation, got %d", acct.Selector, acct.InFlight)
		}
	}
}

func TestCodexDownstreamCancellationDuringNonStreamBodyNoFailover(t *testing.T) {
	// When the client cancels during non-stream body reading, the proxy
	// should NOT try another account.
	var (
		attemptCount int32
		gotRequest   = make(chan struct{})
		handlerDone  = make(chan struct{})
	)

	api := newCodexFailoverTestAPI(t, failoverTestOptions{
		maxFailovers: 1,
		accounts: []failoverTestAccount{
			{email: "a@test.com", accountID: "acct_a"},
			{email: "b@test.com", accountID: "acct_b"},
		},
		upstreamHandler: func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&attemptCount, 1)
			close(gotRequest)
			defer close(handlerDone)
			// Wait for client to cancel with timeout.
			select {
			case <-r.Context().Done():
			case <-time.After(10 * time.Second):
			}
		},
	})

	srv := httptest.NewServer(api.engine)
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())

	// Non-streaming request.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/v1/responses", strings.NewReader(`{"model":"droid-oauth","input":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = http.DefaultClient.Do(req)
	}()

	select {
	case <-gotRequest:
		cancel()
	case <-time.After(5 * time.Second):
		t.Fatal("upstream never received request")
	}

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("request goroutine did not finish")
	}

	// Wait for handler to finish so pool state is settled.
	select {
	case <-handlerDone:
	case <-time.After(15 * time.Second):
		t.Fatal("handler goroutine did not finish")
	}

	if atomic.LoadInt32(&attemptCount) != 1 {
		t.Fatalf("expected exactly 1 upstream attempt, got %d", attemptCount)
	}

	snap := api.api.Pool.Snapshot()
	for _, acct := range snap.Accounts {
		if acct.CooldownUntil != nil {
			t.Fatalf("account %s should not be in cooldown, got %v", acct.Selector, acct.CooldownUntil)
		}
		if acct.InFlight != 0 {
			t.Fatalf("account %s should have 0 in-flight, got %d", acct.Selector, acct.InFlight)
		}
	}
}

// ============================================================================
// VAL-FAILOVER-016: Streaming commit boundary includes header commit
// ============================================================================

func TestCodexStreamingHeadersCommittedNoRetryBeforeFirstDataFrame(t *testing.T) {
	// Once the proxy commits downstream SSE headers/status (200), later failure
	// before any upstream data: frame has been forwarded does NOT cause retry.
	var attemptCount int32

	api := newCodexFailoverTestAPI(t, failoverTestOptions{
		maxFailovers: 1,
		accounts: []failoverTestAccount{
			{email: "a@test.com", accountID: "acct_a"},
			{email: "b@test.com", accountID: "acct_b"},
		},
		upstreamHandler: func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&attemptCount, 1)
			// Return 200 OK with SSE headers but immediately close.
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			flusher := w.(http.Flusher)
			flusher.Flush()
			// No data frames — just close. This simulates an upstream that
			// commits headers then truncates before any data.
		},
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"droid-oauth","input":"hi","stream":true}`))
	api.engine.ServeHTTP(w, req)

	// Should get 200 OK (headers committed) but with truncation error in body.
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 (headers committed before truncation), got %d", w.Code)
	}
	// Only 1 attempt — no retry after headers committed.
	if atomic.LoadInt32(&attemptCount) != 1 {
		t.Fatalf("expected exactly 1 upstream attempt (no retry after header commit), got %d", attemptCount)
	}
	// Body should contain truncation error, not a second attempt's data.
	if !strings.Contains(w.Body.String(), "upstream stream ended before terminal marker") {
		t.Fatalf("expected truncation error in SSE body, got %s", w.Body.String())
	}
}

func TestCodexStreamingIdleTimeoutAfterHeaderCommitNoRetry(t *testing.T) {
	// Idle timeout after header commit but before first data frame should not
	// trigger retry on another account. Uses a real server so the proxy can
	// get the 200 response and start streaming before the upstream stalls.
	var attemptCount int32

	api := newCodexFailoverTestAPI(t, failoverTestOptions{
		maxFailovers: 1,
		accounts: []failoverTestAccount{
			{email: "a@test.com", accountID: "acct_a"},
			{email: "b@test.com", accountID: "acct_b"},
		},
		upstreamHandler: func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&attemptCount, 1)
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			flusher := w.(http.Flusher)
			flusher.Flush()
			// Don't send any data frames — the idle timeout will fire.
			// Wait for the connection to close or a timeout.
			select {
			case <-r.Context().Done():
			case <-time.After(30 * time.Second):
			}
		},
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"droid-oauth","input":"hi","stream":true}`))
	api.engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if atomic.LoadInt32(&attemptCount) != 1 {
		t.Fatalf("expected exactly 1 upstream attempt, got %d", attemptCount)
	}
}

// ============================================================================
// VAL-FAILOVER-018: Non-streaming body-read errors have defined retry behavior
// ============================================================================

func TestCodexNonStreamingBodyReadErrorAfter2xxNotRetried(t *testing.T) {
	// For non-streaming requests, if the upstream returns 200 but the body
	// read fails (e.g., oversized body), the proxy should NOT retry on another
	// account. This preserves single-account compatibility and avoids
	// unbounded duplicate Codex work.
	var attemptCount int32

	api := newCodexFailoverTestAPI(t, failoverTestOptions{
		maxFailovers:         1,
		responseBodyMaxBytes: 1024, // 1 KiB limit
		accounts: []failoverTestAccount{
			{email: "a@test.com", accountID: "acct_a"},
			{email: "b@test.com", accountID: "acct_b"},
		},
		upstreamHandler: func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&attemptCount, 1)
			w.Header().Set("Content-Type", "text/event-stream")
			// Return a 2xx response with a body exceeding the configured limit.
			largeBody := make([]byte, 11*1024) // 11 KiB > 1 KiB limit
			for i := range largeBody {
				largeBody[i] = 'a'
			}
			_, _ = w.Write(largeBody)
		},
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"droid-oauth","input":"hi"}`))
	api.engine.ServeHTTP(w, req)

	// Should get an error about the oversized body, not a retry.
	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected 502 for oversized body, got %d body=%s", w.Code, w.Body.String())
	}
	// Only 1 attempt — body-read errors after 2xx are NOT retried.
	if atomic.LoadInt32(&attemptCount) != 1 {
		t.Fatalf("expected exactly 1 upstream attempt (no retry on body-read error), got %d", attemptCount)
	}
	// Verify the error message is about the body size.
	if !strings.Contains(w.Body.String(), "too large") {
		t.Fatalf("expected 'too large' in error, got %s", w.Body.String())
	}

	// In-flight should be 0.
	snap := api.api.Pool.Snapshot()
	for _, acct := range snap.Accounts {
		if acct.InFlight != 0 {
			t.Fatalf("expected 0 in-flight, got %d for %s", acct.InFlight, acct.Selector)
		}
	}
}

func TestCodexNonStreamingTransportErrorBeforeHeadersIsRetried(t *testing.T) {
	// Transport errors before response headers ARE retryable — this is
	// distinct from body-read errors after 2xx.
	var attemptCount int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(atomic.AddInt32(&attemptCount, 1))
		if n == 1 {
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
	pool := oauth.NewAccountPool(tokens, time.Now, oauth.TestPoolLB(), nil, oauth.NewSelector(config.LoadBalancingFillFirst))
	router, _ := upstream.NewRouter(cfg.Models)
	logger := logrus.New()
	logger.SetOutput(io.Discard)
	api := &API{Cfg: cfg, Router: router, Client: upstream.NewClient(cfg), OAuth: manager, Pool: pool, Logger: logger}
	engine := gin.New()
	engine.POST("/v1/responses", api.Responses)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"droid-oauth","input":"hi"}`))
	engine.ServeHTTP(w, req)

	// Should succeed after failover (transport error is retryable).
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 after transport error failover, got %d body=%s", w.Code, w.Body.String())
	}
	if atomic.LoadInt32(&attemptCount) != 2 {
		t.Fatalf("expected 2 attempts (transport error then success), got %d", attemptCount)
	}
}

func TestCodexNonStreamingBodyReadErrorPreservesSingleAccountBehavior(t *testing.T) {
	// With a single account, a body-read error after 2xx should produce the
	// same error shape as the multi-account case (no special single-account
	// handling needed for this edge case — it's not retried in either mode).
	var attemptCount int32

	api := newCodexFailoverTestAPI(t, failoverTestOptions{
		maxFailovers:         2,
		responseBodyMaxBytes: 1024, // 1 KiB limit
		accounts: []failoverTestAccount{
			{email: "a@test.com", accountID: "acct_a"},
		},
		upstreamHandler: func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&attemptCount, 1)
			w.Header().Set("Content-Type", "text/event-stream")
			// Return an oversized body.
			largeBody := make([]byte, 11*1024) // 11 KiB > 1 KiB limit
			for i := range largeBody {
				largeBody[i] = 'a'
			}
			_, _ = w.Write(largeBody)
		},
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"droid-oauth","input":"hi"}`))
	api.engine.ServeHTTP(w, req)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d body=%s", w.Code, w.Body.String())
	}
	// Exactly 1 attempt — body-read errors are never retried.
	if atomic.LoadInt32(&attemptCount) != 1 {
		t.Fatalf("expected exactly 1 attempt, got %d", attemptCount)
	}
}

// ============================================================================
// Code cleanups: context cancellation and status preservation in codexAuthReplay
// ============================================================================

func TestCodexAuthReplayContextCancellationDoesNotMarkUnhealthy(t *testing.T) {
	// When codexAuthReplay encounters doErr and the client context is
	// cancelled, the account must NOT be marked unhealthy. The handler
	// should return immediately without failover or cooldown marking.
	var mu sync.Mutex
	var attempts []string
	var cancelFn context.CancelFunc

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

			if auth == "access-token-0" {
				// Initial request: return 401 to trigger auth replay.
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":{"message":"unauthorized"}}`))
				return
			}
			// Replay request: cancel the client context before responding,
			// then close the connection to trigger a transport error.
			if cancelFn != nil {
				cancelFn()
			}
			hj, ok := w.(http.Hijacker)
			if ok {
				conn, _, _ := hj.Hijack()
				_ = conn.Close()
				return
			}
			w.WriteHeader(http.StatusInternalServerError)
		},
	})

	// Use a cancellable context so the replay sees cancellation.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelFn = cancel

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"droid-oauth","input":"hi"}`)).WithContext(ctx)
	api.engine.ServeHTTP(w, req)

	// The handler should have returned after cancellation without writing
	// a complete response (context was cancelled).
	mu.Lock()
	defer mu.Unlock()

	// Verify first account was NOT marked unhealthy — context cancellation
	// should suppress unhealthy marking.
	snap := api.api.Pool.Snapshot()
	for _, acct := range snap.Accounts {
		if acct.Selector == "a@test.com" && !acct.Healthy {
			t.Fatalf("account a should NOT be marked unhealthy after client cancellation during replay, got unhealthy=true")
		}
	}

	// Verify no leases are stuck.
	for _, acct := range snap.Accounts {
		if acct.InFlight != 0 {
			t.Fatalf("expected 0 in-flight for %s, got %d", acct.Selector, acct.InFlight)
		}
	}
}

func TestCodexAuthReplayTransportErrorPreservesOriginalStatus(t *testing.T) {
	// When codexAuthReplay encounters a transport error (doErr != nil) that
	// is NOT due to client cancellation, the original 401/403 status should
	// be preserved for relay rather than overwritten to 0.
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

	// Upstream handler that returns 403 on the initial request, then
	// force-closes the connection on the replay (refreshed token).
	upstreamHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := authTokenFromRequest(r)
		mu.Lock()
		attempts = append(attempts, auth)
		mu.Unlock()

		if auth == "access-token-0" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"message":"forbidden","type":"permission_error"}}`))
			return
		}
		// Replay with refreshed token: close connection to simulate transport error.
		hj, ok := w.(http.Hijacker)
		if ok {
			conn, _, _ := hj.Hijack()
			_ = conn.Close()
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	})

	api := newCodexFailoverTestAPI(t, failoverTestOptions{
		maxFailovers: 0, // No additional failovers — we want exhaustion relay
		accounts: []failoverTestAccount{
			{email: "a@test.com", accountID: "acct_a"},
			{email: "b@test.com", accountID: "acct_b"}, // second account needed for isMultiAccount=true
		},
		upstreamHandler: upstreamHandler,
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"droid-oauth","input":"hi"}`))
	api.engine.ServeHTTP(w, req)

	mu.Lock()
	defer mu.Unlock()

	// Should have seen the initial attempt and the replay attempt.
	if len(attempts) < 2 {
		t.Fatalf("expected at least 2 upstream attempts (initial + replay), got %d: %v", len(attempts), attempts)
	}

	// The response should relay the original 403, not a 0 or 502.
	// Without the fix, lastUpstreamStatus would be overwritten to 0
	// from the replay's transport error.
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 (preserved from original), got %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "forbidden") {
		t.Fatalf("expected original error body relayed, got %s", w.Body.String())
	}

	// The account should be marked unhealthy after the replay transport error.
	snap := api.api.Pool.Snapshot()
	for _, acct := range snap.Accounts {
		if acct.Selector == "a@test.com" {
			if acct.Healthy {
				t.Fatal("expected account to be marked unhealthy after replay transport error")
			}
		}
		if acct.InFlight != 0 {
			t.Fatalf("expected 0 in-flight, got %d for %s", acct.InFlight, acct.Selector)
		}
	}
}

// Mixed-model Factory threads can replay reasoning items minted by another
// provider into the Codex pool path. A 400 blaming encrypted_content must
// trigger exactly one same-pool replay with reasoning items stripped, without
// consuming the failover budget.
func TestResponsesCodexFailoverStripsUndecryptableReasoningAndReplays(t *testing.T) {
	var bodies []string
	var mu sync.Mutex

	api := newCodexFailoverTestAPI(t, failoverTestOptions{
		maxFailovers: 0, // replay must work even with no failover budget
		accounts: []failoverTestAccount{
			{email: "a@test.com", accountID: "acct_a"},
			{email: "b@test.com", accountID: "acct_b"},
		},
		upstreamHandler: func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			mu.Lock()
			bodies = append(bodies, string(body))
			n := len(bodies)
			mu.Unlock()
			if n == 1 {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":{"message":"Could not decrypt the provided encrypted_content.","type":"invalid_request_error"}}`))
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("event: response.completed\n" +
				`data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}]}}` + "\n\n"))
		},
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(
		`{"model":"droid-oauth","input":[{"id":"rs_foreign","type":"reasoning","encrypted_content":"xai-blob"},{"role":"user","content":[{"type":"input_text","text":"hi"}]}]}`))
	api.engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 after strip-replay, got %d body=%s", w.Code, w.Body.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 2 {
		t.Fatalf("upstream attempts = %d, want 2", len(bodies))
	}
	if !strings.Contains(bodies[0], `"type":"reasoning"`) {
		t.Fatalf("first attempt should carry the reasoning item: %s", bodies[0])
	}
	if strings.Contains(bodies[1], `"type":"reasoning"`) || strings.Contains(bodies[1], "xai-blob") {
		t.Fatalf("replay must not carry reasoning items: %s", bodies[1])
	}
}
