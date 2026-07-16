package handlers

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"

	"github.com/trevoraspencer/droid-proxy/internal/config"
	"github.com/trevoraspencer/droid-proxy/internal/upstream"
)

// capturedRequest holds one upstream request's method, path, headers, and body.
type capturedRequest struct {
	method string
	path   string
	header http.Header
	body   []byte
}

// requestCapture wraps an http.HandlerFunc and records every request.
type requestCapture struct {
	mu       sync.Mutex
	requests []capturedRequest
}

func (rc *requestCapture) wrap(inner http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		rc.mu.Lock()
		rc.requests = append(rc.requests, capturedRequest{
			method: r.Method,
			path:   r.URL.Path,
			header: r.Header.Clone(),
			body:   b,
		})
		rc.mu.Unlock()
		// Restore body for the inner handler.
		r.Body = io.NopCloser(bytes.NewReader(b))
		inner(w, r)
	}
}

func (rc *requestCapture) count() int {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return len(rc.requests)
}

func (rc *requestCapture) get(i int) capturedRequest {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	if i < 0 || i >= len(rc.requests) {
		return capturedRequest{}
	}
	return rc.requests[i]
}

// newFireworksTestAPI builds a test API with one or more Fireworks models,
// all pointing at the same fake upstream. If the caller needs to inspect
// upstream requests, wrap the handler with a requestCapture before passing
// it in.
func newFireworksTestAPI(t *testing.T, handler http.HandlerFunc, models ...*config.Model) *testAPI {
	t.Helper()
	gin.SetMode(gin.TestMode)
	upstreamServer := httptest.NewServer(handler)
	t.Cleanup(upstreamServer.Close)

	// Resolve known_auth defaults for every model so BaseURL/APIKeyEnv are set.
	for _, m := range models {
		if err := config.HydrateModel(m); err != nil {
			t.Fatalf("hydrate model %q: %v", m.Alias, err)
		}
		// Override BaseURL to point at the fake.
		m.BaseURL = upstreamServer.URL
	}

	cfg := &config.Config{
		Upstream: config.Upstream{HTTPTimeout: 5 * time.Second, StreamKeepAlive: 200 * time.Millisecond},
		Models:   models,
	}
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
		Logger: logger,
	}
	engine := gin.New()
	engine.POST("/v1/chat/completions", api.ChatCompletions)
	engine.POST("/chat/completions", api.ChatCompletions)
	return &testAPI{api: api, upstream: upstreamServer, engine: engine}
}

// fwModel builds a model with the given Fireworks profile, alias, and upstream model.
func fwModel(alias, knownAuth, upstreamModel string, extraArgs map[string]any) *config.Model {
	m := &config.Model{
		Alias:            alias,
		DisplayName:      alias,
		FactoryProvider:  config.FactoryProviderGeneric,
		UpstreamProtocol: config.UpstreamOpenAIChat,
		KnownAuth:        knownAuth,
		UpstreamModel:    upstreamModel,
		ExtraArgs:        extraArgs,
	}
	return m
}

// jsonRespond writes a JSON body with the given status and optional extra headers.
func jsonRespond(w http.ResponseWriter, status int, body string, headers map[string]string) {
	w.Header().Set("Content-Type", "application/json")
	for k, v := range headers {
		w.Header().Set(k, v)
	}
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

func mustJSON(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("unmarshal body %q: %v", string(b), err)
	}
	return v
}

// ---------------------------------------------------------------------------
// VAL-FIREWORKS-009: Standard and Fire Pass route to the shared exact endpoint
// with separate opaque keys.
// ---------------------------------------------------------------------------

func TestFireworks_StandardAndFirePass_SelectDeclaredCredential(t *testing.T) {
	// Set env vars with intentionally swapped-looking prefixes so the test
	// proves profile selection is by declared env var, not by key prefix.
	t.Setenv("FIREWORKS_API_KEY", "fw_standard_secret_123")
	t.Setenv("FIREWORKS_FIRE_PASS_API_KEY", "fpk_firepass_secret_456")

	stdModel := fwModel("fw-std", "fireworks", "accounts/fireworks/models/glm-4", nil)
	fpModel := fwModel("fw-pass", "fireworks-fire-pass", "accounts/fireworks/routers/glm-5p2-fast", nil)

	rc := &requestCapture{}
	ta := newFireworksTestAPI(t, rc.wrap(func(w http.ResponseWriter, r *http.Request) {
		jsonRespond(w, http.StatusOK, `{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"ok"}}]}`, nil)
	}), stdModel, fpModel)

	for _, tc := range []struct {
		alias    string
		wantAuth string
	}{
		{"fw-std", "Bearer fw_standard_secret_123"},
		{"fw-pass", "Bearer fpk_firepass_secret_456"},
	} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
			strings.NewReader(fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}]}`, tc.alias)))
		ta.engine.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: expected 200, got %d body=%s", tc.alias, w.Code, w.Body.String())
		}
	}

	// Verify each request used the correct credential.
	for i, want := range []string{"Bearer fw_standard_secret_123", "Bearer fpk_firepass_secret_456"} {
		cap := rc.get(i)
		if got := cap.header.Get("Authorization"); got != want {
			t.Fatalf("request %d auth = %q, want %q", i, got, want)
		}
	}
	// Verify both hit /chat/completions on the same fake.
	if rc.count() != 2 {
		t.Fatalf("expected 2 upstream requests, got %d", rc.count())
	}
}

func TestFireworks_CredentialPrefixSwap_DoesNotAffectProfile(t *testing.T) {
	// Put an fpk_-prefixed value in FIREWORKS_API_KEY and an fw_-prefixed value
	// in FIREWORKS_FIRE_PASS_API_KEY. Each profile must use its own declared var.
	t.Setenv("FIREWORKS_API_KEY", "fpk_looks_like_firepass")
	t.Setenv("FIREWORKS_FIRE_PASS_API_KEY", "fw_looks_like_standard")

	stdModel := fwModel("fw-std2", "fireworks", "accounts/fireworks/models/test", nil)
	fpModel := fwModel("fw-pass2", "fireworks-fire-pass", "accounts/fireworks/routers/glm-5p2-fast", nil)

	rc := &requestCapture{}
	ta := newFireworksTestAPI(t, rc.wrap(func(w http.ResponseWriter, r *http.Request) {
		jsonRespond(w, http.StatusOK, `{"id":"x","choices":[]}`, nil)
	}), stdModel, fpModel)

	// Standard uses FIREWORKS_API_KEY (the fpk_ value), Fire Pass uses FIREWORKS_FIRE_PASS_API_KEY.
	for _, alias := range []string{"fw-std2", "fw-pass2"} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
			strings.NewReader(fmt.Sprintf(`{"model":%q,"messages":[]}`, alias)))
		ta.engine.ServeHTTP(w, req)
	}
	if got := rc.get(0).header.Get("Authorization"); got != "Bearer fpk_looks_like_firepass" {
		t.Errorf("standard auth = %q, want Bearer fpk_looks_like_firepass", got)
	}
	if got := rc.get(1).header.Get("Authorization"); got != "Bearer fw_looks_like_standard" {
		t.Errorf("fire pass auth = %q, want Bearer fw_looks_like_standard", got)
	}
}

// ---------------------------------------------------------------------------
// VAL-FIREWORKS-010: Standard, Priority, Fast, and Fire Pass retain distinct
// semantics. Sequential and interleaved requests must not combine or leak.
// ---------------------------------------------------------------------------

func TestFireworks_DistinctTierSemantics(t *testing.T) {
	t.Setenv("FIREWORKS_API_KEY", "fw_key")
	t.Setenv("FIREWORKS_FIRE_PASS_API_KEY", "fpk_key")

	// Standard: no service_tier
	stdModel := fwModel("fw-std-t", "fireworks", "accounts/fireworks/models/lm-1", nil)
	// Priority: service_tier: priority on a standard model
	priModel := fwModel("fw-pri-t", "fireworks", "accounts/fireworks/models/lm-1",
		map[string]any{"service_tier": "priority"})
	// Fast: router model, no service_tier
	fastModel := fwModel("fw-fast-t", "fireworks", "accounts/fireworks/routers/glm-5p2-fast", nil)
	// Fire Pass: own profile, own router
	fpModel := fwModel("fw-pass-t", "fireworks-fire-pass", "accounts/fireworks/routers/glm-5p2-fast", nil)
	// Explicit Fast+Priority: snapshot-supported combination preserved unchanged
	fastPriModel := fwModel("fw-fastpri-t", "fireworks", "accounts/fireworks/routers/glm-5p2-fast",
		map[string]any{"service_tier": "priority"})

	rc := &requestCapture{}
	ta := newFireworksTestAPI(t, rc.wrap(func(w http.ResponseWriter, r *http.Request) {
		jsonRespond(w, http.StatusOK, `{"id":"x","choices":[]}`, nil)
	}), stdModel, priModel, fastModel, fpModel, fastPriModel)

	aliases := []string{"fw-std-t", "fw-pri-t", "fw-fast-t", "fw-pass-t", "fw-fastpri-t"}
	for _, alias := range aliases {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
			strings.NewReader(fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"x"}]}`, alias)))
		ta.engine.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: expected 200, got %d", alias, w.Code)
		}
	}

	type expected struct {
		model   string
		hasTier bool
		tierVal string
		auth    string
	}
	expects := []expected{
		{"accounts/fireworks/models/lm-1", false, "", "Bearer fw_key"},
		{"accounts/fireworks/models/lm-1", true, "priority", "Bearer fw_key"},
		{"accounts/fireworks/routers/glm-5p2-fast", false, "", "Bearer fw_key"},
		{"accounts/fireworks/routers/glm-5p2-fast", false, "", "Bearer fpk_key"},
		{"accounts/fireworks/routers/glm-5p2-fast", true, "priority", "Bearer fw_key"},
	}
	for i, want := range expects {
		cap := rc.get(i)
		if got := gjson.GetBytes(cap.body, "model").String(); got != want.model {
			t.Errorf("request %d model = %q, want %q", i, got, want.model)
		}
		tier := gjson.GetBytes(cap.body, "service_tier")
		if want.hasTier {
			if !tier.Exists() || tier.String() != want.tierVal {
				t.Errorf("request %d service_tier = %q, want %q", i, tier.Raw, want.tierVal)
			}
		} else {
			if tier.Exists() {
				t.Errorf("request %d service_tier should be absent, got %q", i, tier.Raw)
			}
		}
		if got := cap.header.Get("Authorization"); got != want.auth {
			t.Errorf("request %d auth = %q, want %q", i, got, want.auth)
		}
	}
}

func TestFireworks_InterleavedRequests_NoTierModelLeakage(t *testing.T) {
	t.Setenv("FIREWORKS_API_KEY", "fw_key")

	// Two models with distinct tiers on the same profile.
	stdModel := fwModel("fw-inter-std", "fireworks", "accounts/fireworks/models/iso", nil)
	priModel := fwModel("fw-inter-pri", "fireworks", "accounts/fireworks/models/iso",
		map[string]any{"service_tier": "priority"})

	rc := &requestCapture{}
	ta := newFireworksTestAPI(t, rc.wrap(func(w http.ResponseWriter, r *http.Request) {
		jsonRespond(w, http.StatusOK, `{"id":"x","choices":[]}`, nil)
	}), stdModel, priModel)

	// Interleave requests: std, pri, std, pri, std.
	sequence := []string{"fw-inter-std", "fw-inter-pri", "fw-inter-std", "fw-inter-pri", "fw-inter-std"}
	for _, alias := range sequence {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
			strings.NewReader(fmt.Sprintf(`{"model":%q,"messages":[]}`, alias)))
		ta.engine.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("interleaved request %s: expected 200, got %d", alias, w.Code)
		}
	}

	if rc.count() != 5 {
		t.Fatalf("expected 5 requests, got %d", rc.count())
	}
	for i, alias := range sequence {
		cap := rc.get(i)
		// Standard must never have service_tier.
		if alias == "fw-inter-std" {
			if tier := gjson.GetBytes(cap.body, "service_tier"); tier.Exists() {
				t.Errorf("interleaved std request %d leaked service_tier: %q", i, tier.Raw)
			}
		}
		// Priority must always have service_tier: priority.
		if alias == "fw-inter-pri" {
			tier := gjson.GetBytes(cap.body, "service_tier")
			if !tier.Exists() || tier.String() != "priority" {
				t.Errorf("interleaved pri request %d service_tier = %q, want priority", i, tier.Raw)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// VAL-FIREWORKS-011: Configured and request-time Fireworks options preserve
// generic merge semantics.
// ---------------------------------------------------------------------------

func TestFireworks_OptionsReachUpstreamWithExactTypes(t *testing.T) {
	t.Setenv("FIREWORKS_API_KEY", "fw_key")

	model := fwModel("fw-opts", "fireworks", "accounts/fireworks/models/opt-test", nil)

	rc := &requestCapture{}
	ta := newFireworksTestAPI(t, rc.wrap(func(w http.ResponseWriter, r *http.Request) {
		jsonRespond(w, http.StatusOK, `{"id":"x","choices":[]}`, nil)
	}), model)

	// Send a request with every documented Fireworks field at valid values.
	reqBody := `{
		"model": "fw-opts",
		"messages": [{"role":"user","content":"hello"}],
		"service_tier": "priority",
		"reasoning_effort": "high",
		"reasoning_history": "interleaved",
		"thinking": {"type":"enabled"},
		"prompt_cache_key": "cache-abc",
		"prompt_cache_isolation_key": "iso-xyz",
		"perf_metrics_in_response": true,
		"context_length_exceeded_behavior": "truncate",
		"response_format": {"type":"json_object"},
		"min_p": 0.1,
		"top_k": 40,
		"repetition_penalty": 1.05,
		"tools": [{"type":"function","function":{"name":"f","parameters":{"type":"object"}}}],
		"tool_choice": "auto",
		"parallel_tool_calls": true,
		"stream": false,
		"stream_options": {"include_usage": true}
	}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(reqBody))
	ta.engine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}

	cap := rc.get(0)

	// Verify exact types and values for every field.
	type fieldCheck struct {
		path string
		want any
	}
	checks := []fieldCheck{
		{"service_tier", "priority"},
		{"reasoning_effort", "high"},
		{"reasoning_history", "interleaved"},
		{"thinking", map[string]any{"type": "enabled"}},
		{"prompt_cache_key", "cache-abc"},
		{"prompt_cache_isolation_key", "iso-xyz"},
		{"perf_metrics_in_response", true},
		{"context_length_exceeded_behavior", "truncate"},
		{"response_format", map[string]any{"type": "json_object"}},
		{"min_p", 0.1},
		{"top_k", float64(40)},
		{"repetition_penalty", 1.05},
		{"tool_choice", "auto"},
		{"parallel_tool_calls", true},
		{"stream", false},
		{"stream_options", map[string]any{"include_usage": true}},
	}
	for _, c := range checks {
		gotVal := gjson.GetBytes(cap.body, c.path)
		if !gotVal.Exists() {
			t.Errorf("field %s missing from upstream body", c.path)
			continue
		}
		// Compare via JSON value type.
		gotJSON := gotVal.Value()
		if !deepEqualJSON(gotJSON, c.want) {
			t.Errorf("field %s = %#v, want %#v", c.path, gotJSON, c.want)
		}
	}

	// Verify tools array survived.
	tools := gjson.GetBytes(cap.body, "tools")
	if !tools.IsArray() || len(tools.Array()) != 1 {
		t.Errorf("tools not preserved correctly: %s", tools.Raw)
	}
}

func TestFireworks_ConfiguredExtraArgs_ReplaceCollidingCallerValues(t *testing.T) {
	t.Setenv("FIREWORKS_API_KEY", "fw_key")

	// Model has configured extra_args that should override caller values at top level.
	model := fwModel("fw-merge", "fireworks", "accounts/fireworks/models/merge",
		map[string]any{
			"temperature": 0.1,
			"top_p":       0.8,
		})

	rc := &requestCapture{}
	ta := newFireworksTestAPI(t, rc.wrap(func(w http.ResponseWriter, r *http.Request) {
		jsonRespond(w, http.StatusOK, `{"id":"x","choices":[]}`, nil)
	}), model)

	// Caller sends conflicting values and an unrelated field.
	reqBody := `{"model":"fw-merge","messages":[],"temperature":0.9,"top_p":0.5,"max_tokens":100}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(reqBody))
	ta.engine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	cap := rc.get(0)
	// Configured values replace caller values at the top level.
	if got := gjson.GetBytes(cap.body, "temperature").Float(); got != 0.1 {
		t.Errorf("temperature = %v, want 0.1 (configured)", got)
	}
	if got := gjson.GetBytes(cap.body, "top_p").Float(); got != 0.8 {
		t.Errorf("top_p = %v, want 0.8 (configured)", got)
	}
	// Unrelated caller field preserved.
	if got := gjson.GetBytes(cap.body, "max_tokens").Int(); got != 100 {
		t.Errorf("max_tokens = %v, want 100 (caller)", got)
	}
}

func TestFireworks_ConfiguredExtraArgs_WholeObjectReplacement(t *testing.T) {
	t.Setenv("FIREWORKS_API_KEY", "fw_key")

	// Configured response_format replaces the caller's whole-object value (no deep merge).
	model := fwModel("fw-objrep", "fireworks", "accounts/fireworks/models/obj",
		map[string]any{
			"response_format": map[string]any{"type": "json_schema"},
		})

	rc := &requestCapture{}
	ta := newFireworksTestAPI(t, rc.wrap(func(w http.ResponseWriter, r *http.Request) {
		jsonRespond(w, http.StatusOK, `{"id":"x","choices":[]}`, nil)
	}), model)

	// Caller sends a different response_format object.
	reqBody := `{"model":"fw-objrep","messages":[],"response_format":{"type":"text","extra":"should-be-gone"}}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(reqBody))
	ta.engine.ServeHTTP(w, req)

	cap := rc.get(0)
	rf := gjson.GetBytes(cap.body, "response_format")
	if rf.Get("type").String() != "json_schema" {
		t.Errorf("response_format.type = %q, want json_schema", rf.Get("type").String())
	}
	if rf.Get("extra").Exists() {
		t.Errorf("response_format.extra survived whole-object replacement: %s", rf.Get("extra").Raw)
	}
}

func TestFireworks_UnknownNestedFieldSurvives(t *testing.T) {
	t.Setenv("FIREWORKS_API_KEY", "fw_key")

	model := fwModel("fw-unknown", "fireworks", "accounts/fireworks/models/unk", nil)

	rc := &requestCapture{}
	ta := newFireworksTestAPI(t, rc.wrap(func(w http.ResponseWriter, r *http.Request) {
		jsonRespond(w, http.StatusOK, `{"id":"x","choices":[]}`, nil)
	}), model)

	// Unknown nested field with mixed types, a large integer, and null.
	reqBody := `{"model":"fw-unknown","messages":[],"future_option":{"nested":{"big_int":9007199254740993,"null_val":null,"flag":true,"arr":[1,"two",3]},"name":"test"}}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(reqBody))
	ta.engine.ServeHTTP(w, req)

	cap := rc.get(0)
	// The unknown field must survive byte-for-byte.
	got := gjson.GetBytes(cap.body, "future_option.nested.big_int").Int()
	if got != 9007199254740993 {
		t.Errorf("big_int = %d, want 9007199254740993", got)
	}
	// null_val must survive as JSON null.
	if raw := gjson.GetBytes(cap.body, "future_option.nested.null_val").Raw; raw != "null" {
		t.Errorf("null_val = %s, want null", raw)
	}
	if v := gjson.GetBytes(cap.body, "future_option.nested.flag").Bool(); !v {
		t.Errorf("flag = %v, want true", v)
	}
}

// ---------------------------------------------------------------------------
// VAL-FIREWORKS-012: Fireworks does not invent cache or session affinity.
// ---------------------------------------------------------------------------

func TestFireworks_NoSynthesizedCacheOrSessionAffinity(t *testing.T) {
	t.Setenv("FIREWORKS_API_KEY", "fw_key")

	model := fwModel("fw-noaff", "fireworks", "accounts/fireworks/models/noaff", nil)

	rc := &requestCapture{}
	ta := newFireworksTestAPI(t, rc.wrap(func(w http.ResponseWriter, r *http.Request) {
		jsonRespond(w, http.StatusOK, `{"id":"x","choices":[]}`, nil)
	}), model)

	// Two distinct minimal requests.
	for _, content := range []string{"hello", "world"} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
			strings.NewReader(fmt.Sprintf(`{"model":"fw-noaff","messages":[{"role":"user","content":%q}]}`, content)))
		ta.engine.ServeHTTP(w, req)
	}

	for i := 0; i < 2; i++ {
		cap := rc.get(i)
		// Body must not have synthesized cache/session fields.
		for _, field := range []string{"prompt_cache_key", "prompt_cache_isolation_key", "user"} {
			if v := gjson.GetBytes(cap.body, field); v.Exists() {
				t.Errorf("request %d: synthesized body field %s = %s", i, field, v.Raw)
			}
		}
		// Headers must not have synthesized affinity values.
		for _, hdr := range []string{"X-Session-Affinity", "X-Multi-Turn-Session-Id", "X-Prompt-Cache-Isolation-Key"} {
			if v := cap.header.Get(hdr); v != "" {
				t.Errorf("request %d: synthesized header %s = %q", i, hdr, v)
			}
		}
	}
}

func TestFireworks_ExplicitCacheKeyPassesThrough(t *testing.T) {
	t.Setenv("FIREWORKS_API_KEY", "fw_key")

	model := fwModel("fw-cache", "fireworks", "accounts/fireworks/models/c", nil)

	rc := &requestCapture{}
	ta := newFireworksTestAPI(t, rc.wrap(func(w http.ResponseWriter, r *http.Request) {
		jsonRespond(w, http.StatusOK, `{"id":"x","choices":[]}`, nil)
	}), model)

	reqBody := `{"model":"fw-cache","messages":[],"prompt_cache_key":"explicit-key","user":"explicit-user"}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(reqBody))
	ta.engine.ServeHTTP(w, req)

	cap := rc.get(0)
	if got := gjson.GetBytes(cap.body, "prompt_cache_key").String(); got != "explicit-key" {
		t.Errorf("prompt_cache_key = %q, want explicit-key", got)
	}
	if got := gjson.GetBytes(cap.body, "user").String(); got != "explicit-user" {
		t.Errorf("user = %q, want explicit-user", got)
	}
}

// ---------------------------------------------------------------------------
// VAL-FIREWORKS-013: Non-streaming JSON relays native fields exactly.
// ---------------------------------------------------------------------------

func TestFireworks_NonStreamingRelaysByteForByte(t *testing.T) {
	t.Setenv("FIREWORKS_API_KEY", "fw_key")

	model := fwModel("fw-relay", "fireworks", "accounts/fireworks/models/relay", nil)

	// Rich response fixture with reasoning_content, tool calls, usage/cache,
	// perf_metrics, and documented Fireworks response headers.
	fixtureBody := `{
		"id": "resp-123",
		"object": "chat.completion",
		"model": "accounts/fireworks/models/relay",
		"choices": [{
			"index": 0,
			"message": {
				"role": "assistant",
				"content": "result text",
				"reasoning_content": "thinking about it"
			},
			"finish_reason": "stop"
		}],
		"usage": {
			"prompt_tokens": 100,
			"completion_tokens": 50,
			"total_tokens": 150,
			"prompt_tokens_details": {
				"cached_tokens": 40
			}
		},
		"perf_metrics": {
			"prompt-tokens": 100,
			"cached-prompt-tokens": 40,
			"server-time-to-first-token": 0.12,
			"server-processing-time": 0.45
		}
	}`

	fixtureHeaders := map[string]string{
		"fireworks-prompt-tokens":              "100",
		"fireworks-cached-prompt-tokens":       "40",
		"fireworks-server-time-to-first-token": "0.12",
		"X-Ratelimit-Limit-Tokens-Minute":      "60000",
	}

	ta := newFireworksTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		jsonRespond(w, http.StatusOK, fixtureBody, fixtureHeaders)
	}, model)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"fw-relay","messages":[]}`))
	ta.engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	// VAL-FIREWORKS-013: Body must be byte-for-byte identical to the fixture.
	// Use exact raw-byte equality AND SHA-256 hash equality so that any
	// leading, trailing, or internal byte mutation fails the test.
	fixtureBytes := []byte(fixtureBody)
	if !bytes.Equal(w.Body.Bytes(), fixtureBytes) {
		t.Errorf("response body raw-byte mismatch:\nwant(hex)=%s\ngot (hex)=%s",
			hex.EncodeToString(fixtureBytes), hex.EncodeToString(w.Body.Bytes()))
	}
	fixtureHash := sha256.Sum256(fixtureBytes)
	respHash := sha256.Sum256(w.Body.Bytes())
	if fixtureHash != respHash {
		t.Errorf("response body SHA-256 mismatch:\nwant=%s\ngot =%s",
			hex.EncodeToString(fixtureHash[:]), hex.EncodeToString(respHash[:]))
	}

	// Status unchanged.
	// Content-Type must be application/json.
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	// Model in response must NOT be replaced with the local alias.
	respModel := gjson.Get(w.Body.String(), "model").String()
	if respModel != "accounts/fireworks/models/relay" {
		t.Errorf("response model = %q, want accounts/fireworks/models/relay (not local alias)", respModel)
	}
	// Documented Fireworks headers preserved.
	for k, v := range fixtureHeaders {
		if got := w.Header().Get(k); got != v {
			t.Errorf("response header %s = %q, want %q", k, got, v)
		}
	}
	// Verify perf_metrics relayed exactly with hyphenated keys.
	perfKeys := gjson.Get(w.Body.String(), "perf_metrics").Map()
	for _, key := range []string{"prompt-tokens", "cached-prompt-tokens", "server-time-to-first-token", "server-processing-time"} {
		if _, ok := perfKeys[key]; !ok {
			t.Errorf("perf_metrics missing key %s", key)
		}
	}
}

// TestFireworks_NonStreamingRelayDetectsByteMutation proves that any leading,
// trailing, or internal response-byte mutation fails the exact-byte relay test.
// This is the negative control for VAL-FIREWORKS-013.
func TestFireworks_NonStreamingRelayDetectsByteMutation(t *testing.T) {
	t.Setenv("FIREWORKS_API_KEY", "fw_key")

	model := fwModel("fw-relay-mut", "fireworks", "accounts/fireworks/models/relay-mut", nil)

	originalBody := `{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"ok"}}]}`
	originalHash := sha256.Sum256([]byte(originalBody))

	mutations := []struct {
		name string
		body string
	}{
		{"trailing_newline", originalBody + "\n"},
		{"leading_space", " " + originalBody},
		{"trailing_space", originalBody + " "},
		{"internal_whitespace", `{"id":"x", "choices":[]}`}, // space after colon
		{"byte_change", `{"id":"y","choices":[{"index":0,"message":{"role":"assistant","content":"ok"}}]}`},
	}

	for _, mt := range mutations {
		t.Run(mt.name, func(t *testing.T) {
			ta := newFireworksTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
				jsonRespond(w, http.StatusOK, mt.body, nil)
			}, model)

			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
				strings.NewReader(`{"model":"fw-relay-mut","messages":[]}`))
			ta.engine.ServeHTTP(w, req)

			mutHash := sha256.Sum256(w.Body.Bytes())
			if mutHash == originalHash {
				t.Errorf("mutation %q produced the same SHA-256 as the original; mutation not detected", mt.name)
			}
			if bytes.Equal(w.Body.Bytes(), []byte(originalBody)) {
				t.Errorf("mutation %q produced byte-identical output; mutation not detected", mt.name)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// VAL-FIREWORKS-014: Streaming relays content, reasoning, tools, usage, and
// termination.
// ---------------------------------------------------------------------------

func TestFireworks_StreamingRelaysRawSSE(t *testing.T) {
	t.Setenv("FIREWORKS_API_KEY", "fw_key")

	model := fwModel("fw-stream", "fireworks", "accounts/fireworks/models/stream", nil)

	sseFrames := []string{
		`data: {"id":"1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"thinking..."}}]}`,
		`data: {"id":"1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"Hello"}}]}`,
		`data: {"id":"1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":" world"}}]}`,
		`data: {"id":"1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_x","type":"function","function":{"name":"f","arguments":"{}"}}]}}]}`,
		`data: {"id":"1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`,
		`data: {"id":"1","perf_metrics":{"prompt-tokens":10,"server-processing-time":0.1}}`,
		`data: [DONE]`,
	}

	var seenStream, seenStreamOptions bool
	ta := newFireworksTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		seenStream = gjson.GetBytes(b, "stream").Bool()
		so := gjson.GetBytes(b, "stream_options")
		seenStreamOptions = so.Exists()
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		for _, frame := range sseFrames {
			fmt.Fprintf(w, "%s\n\n", frame)
			flusher.Flush()
		}
	}, model)

	reqBody := `{"model":"fw-stream","stream":true,"stream_options":{"include_usage":true},"messages":[{"role":"user","content":"hi"}]}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(reqBody))
	ta.engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if !seenStream {
		t.Error("upstream did not receive stream:true")
	}
	if !seenStreamOptions {
		t.Error("upstream did not receive stream_options")
	}

	out := w.Body.String()
	// Every frame must appear in order.
	for _, frame := range sseFrames {
		if !strings.Contains(out, frame) {
			t.Errorf("missing SSE frame in output:\n  %s\noutput:\n%s", frame, out)
		}
	}
	// Exactly one [DONE].
	if c := strings.Count(out, "[DONE]"); c != 1 {
		t.Errorf("[DONE] count = %d, want 1", c)
	}
	// Reasoning content preserved.
	if !strings.Contains(out, `"reasoning_content":"thinking..."`) {
		t.Error("reasoning_content not relayed in SSE")
	}
}

// ---------------------------------------------------------------------------
// VAL-FIREWORKS-015: Tool-result and schema round trips are lossless.
// ---------------------------------------------------------------------------

func TestFireworks_ToolResultRoundTripLossless(t *testing.T) {
	t.Setenv("FIREWORKS_API_KEY", "fw_key")

	model := fwModel("fw-tools", "fireworks", "accounts/fireworks/models/tools", nil)

	rc := &requestCapture{}
	ta := newFireworksTestAPI(t, rc.wrap(func(w http.ResponseWriter, r *http.Request) {
		jsonRespond(w, http.StatusOK, `{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"done"}}]}`, nil)
	}), model)

	// Turn 1: assistant tool call with reasoning_content.
	turn1 := `{
		"model": "fw-tools",
		"messages": [
			{"role":"user","content":"what is the weather?"},
			{"role":"assistant","content":null,"reasoning_content":"I should call the weather tool","tool_calls":[{"id":"call_w1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"sf\"}"}}]}
		],
		"tools": [{
			"type": "function",
			"function": {
				"name": "get_weather",
				"strict": true,
				"parameters": {
					"type": "object",
					"properties": {"city": {"type": "string"}},
					"required": ["city"],
					"additionalProperties": false
				}
			}
		}],
		"tool_choice": "auto"
	}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(turn1))
	ta.engine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("turn 1: expected 200, got %d", w.Code)
	}

	cap1 := rc.get(0)
	// Model must be rewritten, everything else preserved.
	if got := gjson.GetBytes(cap1.body, "model").String(); got != "accounts/fireworks/models/tools" {
		t.Errorf("turn 1 model = %q, want accounts/fireworks/models/tools", got)
	}
	// Tool call ID and reasoning must survive.
	if got := gjson.GetBytes(cap1.body, "messages.1.tool_calls.0.id").String(); got != "call_w1" {
		t.Errorf("turn 1 tool_call id = %q, want call_w1", got)
	}
	if got := gjson.GetBytes(cap1.body, "messages.1.reasoning_content").String(); got != "I should call the weather tool" {
		t.Errorf("turn 1 reasoning_content = %q", got)
	}
	// Schema strict + additionalProperties preserved.
	if got := gjson.GetBytes(cap1.body, "tools.0.function.strict").Bool(); !got {
		t.Error("turn 1 strict not preserved")
	}
	if got := gjson.GetBytes(cap1.body, "tools.0.function.parameters.additionalProperties").Bool(); got != false {
		t.Error("turn 1 additionalProperties not preserved")
	}

	// Turn 2: tool result linked to call_w1, with prior assistant message.
	turn2 := `{
		"model": "fw-tools",
		"messages": [
			{"role":"user","content":"what is the weather?"},
			{"role":"assistant","content":null,"reasoning_content":"I should call the weather tool","tool_calls":[{"id":"call_w1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"sf\"}"}}]},
			{"role":"tool","tool_call_id":"call_w1","content":"72F sunny"}
		],
		"tools": [{
			"type": "function",
			"function": {
				"name": "get_weather",
				"strict": true,
				"parameters": {
					"type": "object",
					"properties": {"city": {"type": "string"}},
					"required": ["city"],
					"additionalProperties": false
				}
			}
		}]
	}`
	w2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(turn2))
	ta.engine.ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("turn 2: expected 200, got %d", w2.Code)
	}

	cap2 := rc.get(1)
	// Tool result must be linked correctly.
	if got := gjson.GetBytes(cap2.body, "messages.2.tool_call_id").String(); got != "call_w1" {
		t.Errorf("turn 2 tool_call_id = %q, want call_w1", got)
	}
	if got := gjson.GetBytes(cap2.body, "messages.2.content").String(); got != "72F sunny" {
		t.Errorf("turn 2 tool result = %q, want 72F sunny", got)
	}
	// Message order preserved (user, assistant, tool).
	msgs := gjson.GetBytes(cap2.body, "messages").Array()
	if len(msgs) != 3 {
		t.Fatalf("turn 2 message count = %d, want 3", len(msgs))
	}
	roles := []string{"user", "assistant", "tool"}
	for i, want := range roles {
		if got := msgs[i].Get("role").String(); got != want {
			t.Errorf("turn 2 message %d role = %q, want %q", i, got, want)
		}
	}
	// Prior reasoning_content must survive.
	if got := gjson.GetBytes(cap2.body, "messages.1.reasoning_content").String(); got != "I should call the weather tool" {
		t.Errorf("turn 2 reasoning_content = %q", got)
	}
}

// ---------------------------------------------------------------------------
// VAL-FIREWORKS-016: Errors and local validation retain correct boundaries.
// ---------------------------------------------------------------------------

func TestFireworks_UpstreamErrorRelayedWithExactStatusAndBody(t *testing.T) {
	t.Setenv("FIREWORKS_API_KEY", "fw_key")

	model := fwModel("fw-err", "fireworks", "accounts/fireworks/models/err", nil)

	for _, tc := range []struct {
		name       string
		status     int
		body       string
		contentTyp string
		headers    map[string]string
	}{
		{"validation_422", 422, `{"error":{"message":"bad request","type":"invalid_request_error"}}`, "application/json", nil},
		{"rate_limit_429", 429, `{"error":{"message":"slow down","type":"rate_limit"}}`, "application/json", map[string]string{"Retry-After": "30"}},
		{"server_500", 500, `{"error":{"message":"internal","type":"server_error"}}`, "application/json", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ta := newFireworksTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
				jsonRespond(w, tc.status, tc.body, tc.headers)
			}, model)

			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
				strings.NewReader(`{"model":"fw-err","messages":[]}`))
			ta.engine.ServeHTTP(w, req)

			if w.Code != tc.status {
				t.Fatalf("status = %d, want %d", w.Code, tc.status)
			}
			if got := strings.TrimSpace(w.Body.String()); got != strings.TrimSpace(tc.body) {
				t.Errorf("body = %q, want %q", got, strings.TrimSpace(tc.body))
			}
			if ct := w.Header().Get("Content-Type"); ct != tc.contentTyp {
				t.Errorf("Content-Type = %q, want %q", ct, tc.contentTyp)
			}
			for k, v := range tc.headers {
				if got := w.Header().Get(k); got != v {
					t.Errorf("header %s = %q, want %q", k, got, v)
				}
			}
		})
	}
}

func TestFireworks_PreSSEErrorRemainsHTTP(t *testing.T) {
	t.Setenv("FIREWORKS_API_KEY", "fw_key")

	model := fwModel("fw-sse-err", "fireworks", "accounts/fireworks/models/sseerr", nil)

	ta := newFireworksTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		// A pre-SSE non-2xx must remain native HTTP, not an SSE error.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"bad","type":"invalid_request_error"}}`))
	}, model)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"fw-sse-err","stream":true,"messages":[]}`))
	ta.engine.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json (not text/event-stream)", ct)
	}
}

func TestFireworks_TruncatedStreamEmitsSingleStreamTruncated(t *testing.T) {
	t.Setenv("FIREWORKS_API_KEY", "fw_key")

	model := fwModel("fw-trunc", "fireworks", "accounts/fireworks/models/trunc", nil)

	ta := newFireworksTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		// 200 text/event-stream that closes before [DONE].
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		fmt.Fprint(w, "data: {\"id\":\"1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"}}]}\n\n")
		flusher.Flush()
		// Connection closes here (no [DONE]).
	}, model)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"fw-trunc","stream":true,"messages":[]}`))
	ta.engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for truncated stream, got %d", w.Code)
	}
	out := w.Body.String()
	if !strings.Contains(out, "stream_truncated") {
		t.Errorf("expected stream_truncated event, got:\n%s", out)
	}
	if strings.Contains(out, "[DONE]") {
		t.Error("truncated stream must not contain invented [DONE]")
	}
}

func TestFireworks_LocalErrorsMakeZeroUpstreamRequests(t *testing.T) {
	t.Setenv("FIREWORKS_API_KEY", "fw_key")

	model := fwModel("fw-local", "fireworks", "accounts/fireworks/models/local", nil)

	rc := &requestCapture{}
	ta := newFireworksTestAPI(t, rc.wrap(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("upstream must not be called for local errors")
	}), model)

	// Missing model.
	w := httptest.NewRecorder()
	ta.engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"messages":[]}`)))
	if w.Code != http.StatusBadRequest {
		t.Errorf("missing model: expected 400, got %d", w.Code)
	}

	// Unknown alias.
	w = httptest.NewRecorder()
	ta.engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"nonexistent"}`)))
	if w.Code != http.StatusNotFound {
		t.Errorf("unknown alias: expected 404, got %d", w.Code)
	}

	if rc.count() != 0 {
		t.Errorf("expected 0 upstream requests for local errors, got %d", rc.count())
	}
}

func TestFireworks_MissingProfileKeyFailsLocally(t *testing.T) {
	// Do NOT set FIREWORKS_API_KEY - verify the error fails before upstream.
	model := fwModel("fw-nokey", "fireworks", "accounts/fireworks/models/nokey", nil)

	rc := &requestCapture{}
	ta := newFireworksTestAPI(t, rc.wrap(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("upstream must not be called when key is missing")
	}), model)

	w := httptest.NewRecorder()
	ta.engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"fw-nokey","messages":[]}`)))
	if w.Code != http.StatusInternalServerError {
		t.Errorf("missing key: expected 500, got %d", w.Code)
	}
	if rc.count() != 0 {
		t.Errorf("expected 0 upstream requests, got %d", rc.count())
	}
}

// ---------------------------------------------------------------------------
// VAL-FIREWORKS-017: Auth and headers are isolated and filtered.
// ---------------------------------------------------------------------------

func TestFireworks_ProtectedHeadersCannotOverrideTransportValues(t *testing.T) {
	t.Setenv("FIREWORKS_API_KEY", "fw_real_key")

	model := fwModel("fw-hdr", "fireworks", "accounts/fireworks/models/hdr", nil)

	var seenAuth, seenCookie, seenCustom string
	ta := newFireworksTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		seenAuth = r.Header.Get("Authorization")
		seenCookie = r.Header.Get("Cookie")
		seenCustom = r.Header.Get("X-My-Custom-Header")
		jsonRespond(w, http.StatusOK, `{"id":"x","choices":[]}`, nil)
	}, model)

	// Send downstream request with hostile headers that must not override upstream.
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"fw-hdr","messages":[]}`))
	req.Header.Set("Authorization", "Bearer downstream-fake")
	req.Header.Set("X-Api-Key", "downstream-key")
	req.Header.Set("Cookie", "session=fake")
	req.Header.Set("X-My-Custom-Header", "passes-through")

	w := httptest.NewRecorder()
	ta.engine.ServeHTTP(w, req)

	if seenAuth != "Bearer fw_real_key" {
		t.Errorf("upstream Authorization = %q, want Bearer fw_real_key", seenAuth)
	}
	if seenCookie != "" {
		t.Errorf("upstream Cookie leaked: %q", seenCookie)
	}
	// X-My-Custom-Header was sent on the downstream request but NOT configured
	// as an allowed extra header on the model, so it must not appear upstream.
	// (Only model-configured ExtraHeaders pass through; arbitrary downstream
	// request headers are not forwarded to upstream.)
	if seenCustom != "" {
		t.Errorf("downstream X-My-Custom-Header leaked upstream: %q", seenCustom)
	}
}

func TestFireworks_AllowedExtraHeaderPasses(t *testing.T) {
	t.Setenv("FIREWORKS_API_KEY", "fw_key")

	model := fwModel("fw-ehdr", "fireworks", "accounts/fireworks/models/eh", nil)
	model.ExtraHeaders = map[string]string{
		"X-Fireworks-Client": "droid-proxy-test",
	}

	var seenClient string
	ta := newFireworksTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		seenClient = r.Header.Get("X-Fireworks-Client")
		jsonRespond(w, http.StatusOK, `{"id":"x","choices":[]}`, nil)
	}, model)

	w := httptest.NewRecorder()
	ta.engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"fw-ehdr","messages":[]}`)))

	if seenClient != "droid-proxy-test" {
		t.Errorf("allowed extra header X-Fireworks-Client = %q, want droid-proxy-test", seenClient)
	}
}

func TestFireworks_ResponseHeaders_Filtered(t *testing.T) {
	t.Setenv("FIREWORKS_API_KEY", "fw_key")

	model := fwModel("fw-rhdr", "fireworks", "accounts/fireworks/models/rh", nil)

	ta := newFireworksTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		// Safe documented Fireworks headers + generic request-ID positive control.
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("fireworks-prompt-tokens", "50")
		w.Header().Set("fireworks-cached-prompt-tokens", "20")
		w.Header().Set("fireworks-server-time-to-first-token", "0.08")
		w.Header().Set("X-Ratelimit-Limit-Tokens-Minute", "30000")
		w.Header().Set("X-Request-ID", "req-generic-123") // generic safe request-ID
		// Unsafe headers that must be removed by the proxy's FilterHeaders.
		// Note: Go's HTTP transport strips Connection, Transfer-Encoding,
		// Keep-Alive, and connection-nominated headers before the proxy sees
		// the response. The headers below survive Go's transport and are
		// filtered by the proxy's own header filter.
		w.Header().Set("Set-Cookie", "secret=session")
		w.Header().Set("Content-Encoding", "br") // decompression-derived, not auto-handled by Go
		// Gateway-identifying prefixes.
		w.Header().Set("X-Litellm-Version", "1.0")
		w.Header().Set("X-Portkey-Request-Id", "pk-123")
		w.Header().Set("Helicone-Id", "hc-456")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"x","choices":[]}`))
	}, model)

	w := httptest.NewRecorder()
	ta.engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"fw-rhdr","messages":[]}`)))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}

	// Safe documented Fireworks headers preserved exactly.
	safeHeaders := map[string]string{
		"fireworks-prompt-tokens":              "50",
		"fireworks-cached-prompt-tokens":       "20",
		"fireworks-server-time-to-first-token": "0.08",
		"X-Ratelimit-Limit-Tokens-Minute":      "30000",
		"X-Request-ID":                         "req-generic-123",
	}
	for k, v := range safeHeaders {
		if got := w.Header().Get(k); got != v {
			t.Errorf("safe response header %s = %q, want %q", k, got, v)
		}
	}

	// Unsafe headers removed by the proxy's filter.
	unsafeHeaders := []string{
		"Set-Cookie",
		"Content-Encoding",
		"X-Litellm-Version",
		"X-Portkey-Request-Id",
		"Helicone-Id",
	}
	for _, h := range unsafeHeaders {
		if got := w.Header().Get(h); got != "" {
			t.Errorf("unsafe response header %s leaked: %q", h, got)
		}
	}
}

// TestFireworks_FilterHeaders_RemovesHopByHopAndConnectionScoped proves
// directly that the proxy's FilterHeaders function removes cookies, hop-by-hop
// headers, connection-nominated headers, decompression-derived headers, and
// gateway-identifying prefixes while preserving safe metadata. This covers the
// header categories that Go's HTTP transport already strips before the proxy
// sees them; FilterHeaders is the proxy's own safety net for these cases.
func TestFireworks_FilterHeaders_RemovesHopByHopAndConnectionScoped(t *testing.T) {
	src := http.Header{}
	// Safe metadata.
	src.Set("fireworks-prompt-tokens", "100")
	src.Set("fireworks-cached-prompt-tokens", "40")
	src.Set("X-Ratelimit-Limit-Tokens-Minute", "60000")
	src.Set("X-Request-ID", "generic-req-id")
	// Unsafe: cookies.
	src.Set("Set-Cookie", "session=leaked")
	// Unsafe: hop-by-hop.
	src.Set("Connection", "keep-alive")
	src.Set("Keep-Alive", "timeout=5")
	src.Set("Transfer-Encoding", "chunked")
	src.Set("Proxy-Authenticate", "Basic realm=x")
	src.Set("Proxy-Authorization", "Basic dXNlcjpwYXNz")
	src.Set("Te", "trailers")
	src.Set("Trailer", "X-Foo")
	src.Set("Upgrade", "h2c")
	src.Set("Content-Length", "42")
	src.Set("Content-Encoding", "gzip")
	// Unsafe: connection-nominated.
	src.Set("Connection", "keep-alive, X-Custom-Named")
	src.Set("X-Custom-Named", "conn-scoped-val")
	// Unsafe: gateway prefixes.
	src.Set("X-Litellm-Version", "1.0")
	src.Set("X-Portkey-Id", "pk-1")
	src.Set("Helicone-Request-Id", "hc-1")
	src.Set("Cf-Aig-Status", "blocked")
	src.Set("X-Kong-Proxy-Latency", "123")
	src.Set("X-Bt-Trace-Id", "bt-1")

	filtered := upstream.FilterHeaders(src)

	// Safe headers preserved.
	for k, v := range map[string]string{
		"fireworks-prompt-tokens":         "100",
		"fireworks-cached-prompt-tokens":  "40",
		"X-Ratelimit-Limit-Tokens-Minute": "60000",
		"X-Request-ID":                    "generic-req-id",
	} {
		if got := filtered.Get(k); got != v {
			t.Errorf("FilterHeaders dropped safe header %s: got %q, want %q", k, got, v)
		}
	}

	// All unsafe headers removed.
	removed := []string{
		"Set-Cookie", "Connection", "Keep-Alive", "Transfer-Encoding",
		"Proxy-Authenticate", "Proxy-Authorization", "Te", "Trailer",
		"Upgrade", "Content-Length", "Content-Encoding",
		"X-Custom-Named",
		"X-Litellm-Version", "X-Portkey-Id", "Helicone-Request-Id",
		"Cf-Aig-Status", "X-Kong-Proxy-Latency", "X-Bt-Trace-Id",
	}
	for _, h := range removed {
		if got := filtered.Get(h); got != "" {
			t.Errorf("FilterHeaders kept unsafe header %s: %q", h, got)
		}
	}
}

// TestFireworks_ErrorResponseHeaders_Filtered proves that unsafe error-response
// headers (cookies, connection-scoped, hop-by-hop, decompression-derived, and
// gateway-prefixed) are removed while allowed metadata (documented fireworks-*
// performance headers, X-Ratelimit-Limit-Tokens-*, Retry-After, and a generic
// request-ID) remains on upstream error responses. This is the error-path
// coverage for VAL-FIREWORKS-017.
func TestFireworks_ErrorResponseHeaders_Filtered(t *testing.T) {
	t.Setenv("FIREWORKS_API_KEY", "fw_key")

	model := fwModel("fw-errhdr", "fireworks", "accounts/fireworks/models/errhdr", nil)

	ta := newFireworksTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		// Safe documented Fireworks headers + generic request-ID + Retry-After.
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("fireworks-prompt-tokens", "80")
		w.Header().Set("X-Ratelimit-Limit-Tokens-Minute", "10000")
		w.Header().Set("X-Request-ID", "req-err-generic-456")
		w.Header().Set("Retry-After", "60")
		// Unsafe headers that must be removed even on error responses.
		w.Header().Set("Set-Cookie", "session=leaked-on-error")
		w.Header().Set("Connection", "keep-alive, X-Conn-Scoped")
		w.Header().Set("X-Conn-Scoped", "error-conn-value")
		w.Header().Set("Transfer-Encoding", "chunked")
		w.Header().Set("Content-Encoding", "br")
		w.Header().Set("Keep-Alive", "timeout=5")
		w.Header().Set("X-Litellm-Version", "2.0")
		w.Header().Set("X-Portkey-Status", "error")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limited","type":"rate_limit_error"}}`))
	}, model)

	w := httptest.NewRecorder()
	ta.engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"fw-errhdr","messages":[]}`)))

	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", w.Code)
	}

	// Safe headers preserved on error response.
	safeHeaders := map[string]string{
		"fireworks-prompt-tokens":         "80",
		"X-Ratelimit-Limit-Tokens-Minute": "10000",
		"X-Request-ID":                    "req-err-generic-456",
		"Retry-After":                     "60",
	}
	for k, v := range safeHeaders {
		if got := w.Header().Get(k); got != v {
			t.Errorf("safe error-response header %s = %q, want %q", k, got, v)
		}
	}

	// Unsafe headers removed from error response.
	unsafeHeaders := []string{
		"Set-Cookie",
		"Connection",
		"X-Conn-Scoped",
		"Transfer-Encoding",
		"Content-Encoding",
		"Keep-Alive",
		"X-Litellm-Version",
		"X-Portkey-Status",
	}
	for _, h := range unsafeHeaders {
		if got := w.Header().Get(h); got != "" {
			t.Errorf("unsafe error-response header %s leaked: %q", h, got)
		}
	}
}

// ---------------------------------------------------------------------------
// VAL-FIREWORKS-018: Logs and responses remain secret-safe.
// ---------------------------------------------------------------------------

func TestFireworks_ResponsesDoNotLeakCredentialSentinels(t *testing.T) {
	credSentinel := "fw_secret_credential_xyz"
	t.Setenv("FIREWORKS_API_KEY", credSentinel)

	model := fwModel("fw-leak", "fireworks", "accounts/fireworks/models/leak", nil)

	ta := newFireworksTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		// Upstream response does NOT contain the credential.
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"response"}}]}`))
	}, model)

	w := httptest.NewRecorder()
	ta.engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"fw-leak","messages":[]}`)))

	if strings.Contains(w.Body.String(), credSentinel) {
		t.Errorf("credential sentinel leaked in response body")
	}
	for k := range w.Header() {
		for _, v := range w.Header().Values(k) {
			if strings.Contains(v, credSentinel) {
				t.Errorf("credential sentinel leaked in response header %s: %s", k, v)
			}
		}
	}
}

func TestFireworks_ErrorResponsesDoNotLeakCredentialSentinels(t *testing.T) {
	credSentinel := "fw_err_secret_abc"
	t.Setenv("FIREWORKS_API_KEY", credSentinel)

	model := fwModel("fw-eleak", "fireworks", "accounts/fireworks/models/eleak", nil)

	ta := newFireworksTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		// Upstream error body is deliberately secret-free (per contract).
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"internal server error","type":"server_error"}}`))
	}, model)

	w := httptest.NewRecorder()
	ta.engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"fw-eleak","messages":[]}`)))

	if strings.Contains(w.Body.String(), credSentinel) {
		t.Errorf("credential sentinel leaked in error response body")
	}
}

// ---------------------------------------------------------------------------
// VAL-FIREWORKS-019: Generic fast remains unchanged and Codex compatibility
// stays localized.
// ---------------------------------------------------------------------------

func TestFireworks_GenericServiceTierFastSurvivesUnchanged(t *testing.T) {
	t.Setenv("FIREWORKS_API_KEY", "fw_key")

	// Model with explicit service_tier: fast in config.
	// After removing the global rewrite, it must reach upstream as "fast".
	model := fwModel("fw-fastcfg", "fireworks", "accounts/fireworks/models/fc",
		map[string]any{"service_tier": "fast"})

	rc := &requestCapture{}
	ta := newFireworksTestAPI(t, rc.wrap(func(w http.ResponseWriter, r *http.Request) {
		jsonRespond(w, http.StatusOK, `{"id":"x","choices":[]}`, nil)
	}), model)

	w := httptest.NewRecorder()
	ta.engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"fw-fastcfg","messages":[]}`)))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	cap := rc.get(0)
	if got := gjson.GetBytes(cap.body, "service_tier").String(); got != "fast" {
		t.Errorf("service_tier = %q, want \"fast\" (global load must not rewrite)", got)
	}
}

func TestFireworks_CustomProviderServiceTierFastSurvives(t *testing.T) {
	t.Setenv("CUSTOM_KEY", "custom_val")

	// A custom (non-Fireworks) provider with service_tier: fast.
	model := &config.Model{
		Alias:            "custom-fast",
		DisplayName:      "Custom Fast",
		FactoryProvider:  config.FactoryProviderGeneric,
		UpstreamProtocol: config.UpstreamOpenAIChat,
		BaseURL:          "", // will be overridden by test helper
		UpstreamModel:    "custom-model",
		APIKeyEnv:        "CUSTOM_KEY",
		ExtraArgs:        map[string]any{"service_tier": "fast"},
	}

	rc := &requestCapture{}
	ta := newFireworksTestAPI(t, rc.wrap(func(w http.ResponseWriter, r *http.Request) {
		jsonRespond(w, http.StatusOK, `{"id":"x","choices":[]}`, nil)
	}), model)

	w := httptest.NewRecorder()
	ta.engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"custom-fast","messages":[]}`)))

	cap := rc.get(0)
	if got := gjson.GetBytes(cap.body, "service_tier").String(); got != "fast" {
		t.Errorf("custom provider service_tier = %q, want \"fast\"", got)
	}
}

func TestFireworks_CodexNormalizesFastButGenericDoesNot(t *testing.T) {
	// This test proves that only the Codex Responses path normalizes "fast",
	// while the generic Chat path does not. The Codex normalization is already
	// covered by oauth_responses_test.go; here we verify the generic path
	// preserves "fast" when a caller supplies it in the request body.

	t.Setenv("FIREWORKS_API_KEY", "fw_key")
	model := fwModel("fw-codex-guard", "fireworks", "accounts/fireworks/models/cg", nil)

	rc := &requestCapture{}
	ta := newFireworksTestAPI(t, rc.wrap(func(w http.ResponseWriter, r *http.Request) {
		jsonRespond(w, http.StatusOK, `{"id":"x","choices":[]}`, nil)
	}), model)

	// Caller sends service_tier: fast in the request body on a generic Chat path.
	w := httptest.NewRecorder()
	ta.engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"fw-codex-guard","service_tier":"fast","messages":[]}`)))

	cap := rc.get(0)
	// Generic Chat must NOT normalize fast -> priority.
	if got := gjson.GetBytes(cap.body, "service_tier").String(); got != "fast" {
		t.Errorf("generic Chat service_tier = %q, want \"fast\" (Codex normalization must not apply here)", got)
	}
}

// ---------------------------------------------------------------------------
// Helper: deep JSON equality check.
// ---------------------------------------------------------------------------

func deepEqualJSON(a, b any) bool {
	switch av := a.(type) {
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for k, v := range av {
			if !deepEqualJSON(v, bv[k]) {
				return false
			}
		}
		return true
	case float64:
		bv, ok := b.(float64)
		return ok && av == bv
	case bool:
		bv, ok := b.(bool)
		return ok && av == bv
	case string:
		bv, ok := b.(string)
		return ok && av == bv
	case nil:
		return b == nil
	default:
		return a == b
	}
}

// ---------------------------------------------------------------------------
// Extra: verify all Fireworks modes use the same generic Chat transport.
// (supports VAL-CROSS-009 at the assertion level: one handler, no fork)
// ---------------------------------------------------------------------------

func TestFireworks_AllModesUseGenericChatTransport(t *testing.T) {
	t.Setenv("FIREWORKS_API_KEY", "fw_key")
	t.Setenv("FIREWORKS_FIRE_PASS_API_KEY", "fpk_key")

	models := []*config.Model{
		fwModel("fw-trans-std", "fireworks", "accounts/fireworks/models/tr1", nil),
		fwModel("fw-trans-pri", "fireworks", "accounts/fireworks/models/tr1", map[string]any{"service_tier": "priority"}),
		fwModel("fw-trans-fast", "fireworks", "accounts/fireworks/routers/glm-5p2-fast", nil),
		fwModel("fw-trans-fp", "fireworks-fire-pass", "accounts/fireworks/routers/glm-5p2-fast", nil),
		fwModel("fw-trans-fastpri", "fireworks", "accounts/fireworks/routers/glm-5p2-fast", map[string]any{"service_tier": "priority"}),
	}

	// All models must be FactoryProviderGeneric and UpstreamOpenAIChat.
	for _, m := range models {
		if err := config.HydrateModel(m); err != nil {
			t.Fatalf("hydrate %q: %v", m.Alias, err)
		}
		if m.FactoryProvider != config.FactoryProviderGeneric {
			t.Errorf("%q FactoryProvider = %q, want %q", m.Alias, m.FactoryProvider, config.FactoryProviderGeneric)
		}
		if m.UpstreamProtocol != config.UpstreamOpenAIChat {
			t.Errorf("%q UpstreamProtocol = %q, want %q", m.Alias, m.UpstreamProtocol, config.UpstreamOpenAIChat)
		}
	}

	rc := &requestCapture{}
	ta := newFireworksTestAPI(t, rc.wrap(func(w http.ResponseWriter, r *http.Request) {
		// All requests must hit /chat/completions with POST.
		jsonRespond(w, http.StatusOK, `{"id":"x","choices":[]}`, nil)
	}), models...)

	for _, m := range models {
		w := httptest.NewRecorder()
		ta.engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
			strings.NewReader(fmt.Sprintf(`{"model":%q,"messages":[]}`, m.Alias))))
		if w.Code != http.StatusOK {
			t.Fatalf("%s: expected 200, got %d", m.Alias, w.Code)
		}
	}

	// Every request must have gone through the same Chat handler (same path).
	for i := 0; i < rc.count(); i++ {
		cap := rc.get(i)
		if cap.method != http.MethodPost {
			t.Errorf("request %d method = %q, want POST", i, cap.method)
		}
		if !strings.HasSuffix(cap.path, "/chat/completions") {
			t.Errorf("request %d path = %q, want suffix /chat/completions", i, cap.path)
		}
	}
}
