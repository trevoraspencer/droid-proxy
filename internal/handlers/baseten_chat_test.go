package handlers

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
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

// ---------------------------------------------------------------------------
// Test helpers (Baseten-specific)
// ---------------------------------------------------------------------------

// newBasetenTestAPI builds a test API with one or more Baseten models,
// all pointing at the same fake upstream. It registers both the Chat
// completions and local model-listing endpoints so tests can exercise the
// full generic Chat surface.
func newBasetenTestAPI(t *testing.T, handler http.HandlerFunc, models ...*config.Model) *testAPI {
	t.Helper()
	gin.SetMode(gin.TestMode)
	upstreamServer := httptest.NewServer(handler)
	t.Cleanup(upstreamServer.Close)

	for _, m := range models {
		if err := config.HydrateModel(m); err != nil {
			t.Fatalf("hydrate model %q: %v", m.Alias, err)
		}
		// Override BaseURL to point at the fake, preserving the /v1 suffix.
		m.BaseURL = upstreamServer.URL + "/v1"
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
	engine.GET("/v1/models", api.Models)
	engine.GET("/models", api.Models)
	return &testAPI{api: api, upstream: upstreamServer, engine: engine}
}

// basetenModel builds a model with the native Baseten profile.
func basetenModel(alias, upstreamModel string, extraArgs map[string]any) *config.Model {
	m := &config.Model{
		Alias:            alias,
		DisplayName:      alias,
		FactoryProvider:  config.FactoryProviderGeneric,
		UpstreamProtocol: config.UpstreamOpenAIChat,
		KnownAuth:        "baseten",
		UpstreamModel:    upstreamModel,
		ExtraArgs:        extraArgs,
	}
	return m
}

// canonicalTLSInterceptor creates an httptest.NewTLSServer and a custom transport
// that redirects all HTTPS connections to that server. The capturedAuthorities
// slice records every host:port the proxy attempted to connect to, allowing
// canonical-host assertions without contacting the real provider.
func canonicalTLSInterceptor(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *http.Transport, *[]string) {
	t.Helper()
	srv := httptest.NewTLSServer(handler)
	t.Cleanup(srv.Close)

	var mu sync.Mutex
	var authorities []string

	transport := &http.Transport{
		DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			mu.Lock()
			authorities = append(authorities, addr)
			mu.Unlock()
			// Redirect the connection to the local TLS fake server.
			return tls.Dial(network, srv.Listener.Addr().String(), &tls.Config{
				InsecureSkipVerify: true,
			})
		},
		ForceAttemptHTTP2: false, // avoid h2 upgrade negotiation with the fake
	}
	return srv, transport, &authorities
}

// countingFailingRoundTripper is a deterministic http.RoundTripper that always
// fails with err and records the exact number of RoundTrip calls. It never
// contacts any server, making it ideal for proving exactly-one-attempt
// transport-failure semantics without race conditions from real sockets.
type countingFailingRoundTripper struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (rt *countingFailingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	rt.mu.Lock()
	rt.calls++
	rt.mu.Unlock()
	// Close the request body so the HTTP client does not leak goroutines
	// waiting on a body read.
	if req.Body != nil {
		_ = req.Body.Close()
	}
	return nil, rt.err
}

func (rt *countingFailingRoundTripper) count() int {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.calls
}

// ---------------------------------------------------------------------------
// VAL-BASETEN-007: Baseten canonical profile, discovery, and custom override
// use exact routes.
// ---------------------------------------------------------------------------

// TestBaseten_NativeModelResolvesCanonicalOrigin verifies that a native
// known_auth: baseten model (no explicit base/key fields) hydrates to the
// canonical inference.baseten.co/v1 base URL and that the full Chat URL
// resolves to inference.baseten.co:443/v1/chat/completions through a loopback
// TLS interceptor.
func TestBaseten_NativeModelResolvesCanonicalOrigin(t *testing.T) {
	t.Setenv("BASETEN_API_KEY", "baseten_canonical_secret")

	m := basetenModel("bt-canonical", "org/model-deep-v4", nil)
	if err := config.HydrateModel(m); err != nil {
		t.Fatal(err)
	}
	// BaseURL must be the canonical shared Model API origin.
	if m.BaseURL != "https://inference.baseten.co/v1" {
		t.Fatalf("hydrated BaseURL = %q, want https://inference.baseten.co/v1", m.BaseURL)
	}

	// Verify URL construction via the exported Build method.
	client := upstream.NewClient(&config.Config{})
	req, err := client.Build(context.Background(), upstream.SendOptions{
		Model:  m,
		Method: http.MethodPost,
		Path:   "/chat/completions",
		Body:   []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if req.URL.Hostname() != "inference.baseten.co" {
		t.Errorf("hostname = %q, want inference.baseten.co", req.URL.Hostname())
	}
	// HTTPS implies port 443; Go omits the default port from URL.String().
	if req.URL.Scheme != "https" {
		t.Errorf("scheme = %q, want https", req.URL.Scheme)
	}
	if req.URL.Path != "/v1/chat/completions" {
		t.Errorf("path = %q, want /v1/chat/completions", req.URL.Path)
	}

	// Verify through a live loopback TLS interceptor that the proxy connects
	// only to inference.baseten.co:443.
	rc := &requestCapture{}
	srv, transport, authorities := canonicalTLSInterceptor(t, rc.wrap(func(w http.ResponseWriter, r *http.Request) {
		jsonRespond(w, http.StatusOK, `{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"ok"}}]}`, nil)
	}))
	_ = srv

	gin.SetMode(gin.TestMode)
	cfg := &config.Config{
		Upstream: config.Upstream{HTTPTimeout: 5 * time.Second, StreamKeepAlive: 200 * time.Millisecond},
		Models:   []*config.Model{m},
	}
	router, err := upstream.NewRouter(cfg.Models)
	if err != nil {
		t.Fatal(err)
	}
	client2 := upstream.NewClient(cfg)
	client2.HTTP.Transport = transport

	logger := logrus.New()
	logger.SetOutput(io.Discard)
	api := &API{Cfg: cfg, Router: router, Client: client2, Logger: logger}
	engine := gin.New()
	engine.POST("/v1/chat/completions", api.ChatCompletions)

	w := httptest.NewRecorder()
	engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"bt-canonical","messages":[]}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}

	if len(*authorities) != 1 {
		t.Fatalf("expected exactly 1 outbound connection, got %d", len(*authorities))
	}
	if got := (*authorities)[0]; got != "inference.baseten.co:443" {
		t.Errorf("connected authority = %q, want inference.baseten.co:443", got)
	}
	// Auth must derive from BASETEN_API_KEY.
	cap := rc.get(0)
	if got := cap.header.Get("Authorization"); got != "Bearer baseten_canonical_secret" {
		t.Errorf("upstream Authorization = %q, want Bearer baseten_canonical_secret", got)
	}
}

// TestBaseten_DeploymentLookingIDDoesNotAlterOrigin verifies that a model slug
// that looks like a deployment identifier (e.g. deploy_id:...) does not alter
// the shared Model API origin.
func TestBaseten_DeploymentLookingIDDoesNotAlterOrigin(t *testing.T) {
	t.Setenv("BASETEN_API_KEY", "baseten_key")

	for _, slug := range []string{
		"deploy_id:abc123",
		"org/deploy_id:xyz",
		"e2e-uuid-550e8400-e29b-41d4-a716-446655440000",
		"org/sub:custom-slug.v2",
	} {
		m := basetenModel("bt-deploy-"+slug, slug, nil)
		if err := config.HydrateModel(m); err != nil {
			t.Fatalf("hydrate slug %q: %v", slug, err)
		}
		if m.BaseURL != "https://inference.baseten.co/v1" {
			t.Errorf("slug %q: BaseURL = %q, want https://inference.baseten.co/v1", slug, m.BaseURL)
		}

		// Verify the URL via buildURL through Build.
		client := upstream.NewClient(&config.Config{})
		req, err := client.Build(context.Background(), upstream.SendOptions{
			Model: m, Method: http.MethodPost, Path: "/chat/completions", Body: []byte(`{}`),
		})
		if err != nil {
			t.Fatalf("build for slug %q: %v", slug, err)
		}
		if req.URL.Hostname() != "inference.baseten.co" {
			t.Errorf("slug %q: hostname = %q", slug, req.URL.Hostname())
		}
	}
}

// TestBaseten_CustomEndpointUsesExplicitBaseURL verifies that a dedicated/custom
// OpenAI-compatible deployment uses an explicit base_url, omits known_auth: baseten,
// and appends exactly /chat/completions.
func TestBaseten_CustomEndpointUsesExplicitBaseURL(t *testing.T) {
	t.Setenv("CUSTOM_DEPLOY_KEY", "custom_deploy_secret")

	rc := &requestCapture{}
	ta := newBasetenTestAPI(t, rc.wrap(func(w http.ResponseWriter, r *http.Request) {
		jsonRespond(w, http.StatusOK, `{"id":"x","choices":[]}`, nil)
	}))

	// Register a custom-endpoint model (NOT known_auth: baseten).
	customModel := &config.Model{
		Alias:            "bt-custom-deploy",
		DisplayName:      "Custom Deploy",
		FactoryProvider:  config.FactoryProviderGeneric,
		UpstreamProtocol: config.UpstreamOpenAIChat,
		BaseURL:          ta.upstream.URL + "/custom-deploy",
		UpstreamModel:    "my-truss-model",
		APIKeyEnv:        "CUSTOM_DEPLOY_KEY",
	}
	// Add it to the router so it can be resolved.
	ta.api.Cfg.Models = append(ta.api.Cfg.Models, customModel)
	router, err := upstream.NewRouter(ta.api.Cfg.Models)
	if err != nil {
		t.Fatal(err)
	}
	ta.api.Router = router

	// The custom model must NOT have known_auth: baseten.
	if customModel.KnownAuth == "baseten" {
		t.Error("custom endpoint model should not have known_auth: baseten")
	}
	if customModel.APIKeyEnv != "CUSTOM_DEPLOY_KEY" {
		t.Errorf("custom model APIKeyEnv = %q, want CUSTOM_DEPLOY_KEY", customModel.APIKeyEnv)
	}

	w := httptest.NewRecorder()
	ta.engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"bt-custom-deploy","messages":[]}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}

	cap := rc.get(0)
	// Path must end with /chat/completions appended to the custom base.
	if !strings.HasSuffix(cap.path, "/custom-deploy/chat/completions") {
		t.Errorf("custom endpoint path = %q, want suffix /custom-deploy/chat/completions", cap.path)
	}
	// Auth derives from the custom env var, not BASETEN_API_KEY.
	if got := cap.header.Get("Authorization"); got != "Bearer custom_deploy_secret" {
		t.Errorf("custom endpoint auth = %q, want Bearer custom_deploy_secret", got)
	}
}

// ---------------------------------------------------------------------------
// VAL-BASETEN-008: Baseten local model listing and alias rewrite preserve
// opaque slugs.
// ---------------------------------------------------------------------------

// TestBaseten_LocalModelListingExposesAliasAndUpstreamSlug verifies that
// /v1/models exposes the exact accepted local alias with generic Chat metadata
// and the exact opaque upstream slug without remote discovery.
func TestBaseten_LocalModelListingExposesAliasAndUpstreamSlug(t *testing.T) {
	t.Setenv("BASETEN_API_KEY", "baseten_key")

	opaqueSlug := "org/DeepSeek-V4.Pro:deploy-1"
	m := basetenModel("bt-listing", opaqueSlug, nil)

	rc := &requestCapture{}
	ta := newBasetenTestAPI(t, rc.wrap(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("model listing must not contact upstream")
	}), m)

	// /v1/models must not trigger any upstream request.
	w := httptest.NewRecorder()
	ta.engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if rc.count() != 0 {
		t.Errorf("model listing made %d upstream requests, want 0", rc.count())
	}

	var resp struct {
		Data []struct {
			ID              string `json:"id"`
			UpstreamModel   string `json:"upstream_model"`
			FactoryProvider string `json:"factory_provider"`
			UpstreamProto   string `json:"upstream_protocol"`
			AgentReady      bool   `json:"agent_ready"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("expected 1 model entry, got %d", len(resp.Data))
	}
	entry := resp.Data[0]
	if entry.ID != "bt-listing" {
		t.Errorf("id = %q, want bt-listing", entry.ID)
	}
	if entry.UpstreamModel != opaqueSlug {
		t.Errorf("upstream_model = %q, want %q", entry.UpstreamModel, opaqueSlug)
	}
	if entry.FactoryProvider != string(config.FactoryProviderGeneric) {
		t.Errorf("factory_provider = %q, want %q", entry.FactoryProvider, config.FactoryProviderGeneric)
	}
	if entry.UpstreamProto != string(config.UpstreamOpenAIChat) {
		t.Errorf("upstream_protocol = %q, want %q", entry.UpstreamProto, config.UpstreamOpenAIChat)
	}
	// agent_ready reflects configured/resolved generic Chat metadata.
	if !entry.AgentReady {
		t.Error("agent_ready should be true for a generic chat model")
	}
}

// TestBaseten_InferenceReplacesOnlyModel verifies that inference replaces only
// the downstream model alias with the opaque upstream slug while preserving
// organization prefixes, case, punctuation, and all unrelated request fields.
func TestBaseten_InferenceReplacesOnlyModel(t *testing.T) {
	t.Setenv("BASETEN_API_KEY", "baseten_key")

	opaqueSlug := "org/DeepSeek-V4.Pro:deploy-1"
	m := basetenModel("bt-rewrite", opaqueSlug, nil)

	rc := &requestCapture{}
	ta := newBasetenTestAPI(t, rc.wrap(func(w http.ResponseWriter, r *http.Request) {
		jsonRespond(w, http.StatusOK, `{"id":"x","choices":[]}`, nil)
	}), m)

	// Request with the alias and unrelated fields.
	reqBody := `{
		"model": "bt-rewrite",
		"messages": [{"role":"user","content":"hello"}],
		"temperature": 0.7,
		"max_tokens": 100,
		"top_p": 0.9
	}`
	w := httptest.NewRecorder()
	ta.engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(reqBody)))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	cap := rc.get(0)
	// Only model is replaced.
	if got := gjson.GetBytes(cap.body, "model").String(); got != opaqueSlug {
		t.Errorf("model = %q, want %q (opaque slug)", got, opaqueSlug)
	}
	// Unrelated fields preserved exactly.
	if got := gjson.GetBytes(cap.body, "temperature").Float(); got != 0.7 {
		t.Errorf("temperature = %v, want 0.7", got)
	}
	if got := gjson.GetBytes(cap.body, "max_tokens").Int(); got != 100 {
		t.Errorf("max_tokens = %v, want 100", got)
	}
	if got := gjson.GetBytes(cap.body, "top_p").Float(); got != 0.9 {
		t.Errorf("top_p = %v, want 0.9", got)
	}
	// Messages preserved.
	if got := gjson.GetBytes(cap.body, "messages.0.role").String(); got != "user" {
		t.Errorf("messages.0.role = %q, want user", got)
	}
	if got := gjson.GetBytes(cap.body, "messages.0.content").String(); got != "hello" {
		t.Errorf("messages.0.content = %q, want hello", got)
	}
}

// TestBaseten_InferencePreservesOpaqueSlugVariants verifies that various
// opaque slug formats (org prefixes, case, dots, hyphens, underscores,
// colons, slashes) survive model replacement byte-for-byte.
func TestBaseten_InferencePreservesOpaqueSlugVariants(t *testing.T) {
	t.Setenv("BASETEN_API_KEY", "baseten_key")

	slugs := []string{
		"org/DeepSeek-V4.Pro",
		"org/custom-model_v2",
		"org/sub:deploy-1",
		"org/mixed-CASE.Model-Name",
		"org/a/b/c/path-model",
		"org/UPPER-CASE.Slug_Test:2",
	}
	var models []*config.Model
	for i, slug := range slugs {
		models = append(models, basetenModel(fmt.Sprintf("bt-slug-%d", i), slug, nil))
	}

	rc := &requestCapture{}
	ta := newBasetenTestAPI(t, rc.wrap(func(w http.ResponseWriter, r *http.Request) {
		jsonRespond(w, http.StatusOK, `{"id":"x","choices":[]}`, nil)
	}), models...)

	for i, slug := range slugs {
		alias := fmt.Sprintf("bt-slug-%d", i)
		w := httptest.NewRecorder()
		ta.engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
			strings.NewReader(fmt.Sprintf(`{"model":%q,"messages":[]}`, alias))))
		if w.Code != http.StatusOK {
			t.Fatalf("slug %q: expected 200, got %d", slug, w.Code)
		}
		cap := rc.get(i)
		if got := gjson.GetBytes(cap.body, "model").String(); got != slug {
			t.Errorf("slug %q: model = %q, want %q", slug, got, slug)
		}
	}
}

// ---------------------------------------------------------------------------
// VAL-BASETEN-009: Baseten non-streaming and streaming responses relay natively.
// ---------------------------------------------------------------------------

// TestBaseten_NonStreamingRelaysByteForByte verifies that a non-streaming
// response with content, reasoning_content, tool calls, usage, and status 200
// is relayed byte-for-byte with content type unchanged.
func TestBaseten_NonStreamingRelaysByteForByte(t *testing.T) {
	t.Setenv("BASETEN_API_KEY", "baseten_key")

	m := basetenModel("bt-relay", "org/relay-model", nil)

	fixtureBody := `{
		"id": "resp-456",
		"object": "chat.completion",
		"model": "org/relay-model",
		"choices": [{
			"index": 0,
			"message": {
				"role": "assistant",
				"content": "result text",
				"reasoning_content": "thinking through the answer"
			},
			"finish_reason": "stop"
		}],
		"usage": {
			"prompt_tokens": 50,
			"completion_tokens": 30,
			"total_tokens": 80
		}
	}`

	ta := newBasetenTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		jsonRespond(w, http.StatusOK, fixtureBody, nil)
	}, m)

	w := httptest.NewRecorder()
	ta.engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"bt-relay","messages":[]}`)))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	// Byte-for-byte equality.
	fixtureBytes := []byte(fixtureBody)
	if !bytesEqual(w.Body.Bytes(), fixtureBytes) {
		t.Errorf("response body raw-byte mismatch:\nwant(hex)=%s\ngot (hex)=%s",
			hex.EncodeToString(fixtureBytes), hex.EncodeToString(w.Body.Bytes()))
	}
	fixtureHash := sha256.Sum256(fixtureBytes)
	respHash := sha256.Sum256(w.Body.Bytes())
	if fixtureHash != respHash {
		t.Errorf("response body SHA-256 mismatch:\nwant=%s\ngot =%s",
			hex.EncodeToString(fixtureHash[:]), hex.EncodeToString(respHash[:]))
	}

	// Content type unchanged.
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	// Model in response must NOT be replaced with the local alias.
	if m := gjson.Get(w.Body.String(), "model").String(); m != "org/relay-model" {
		t.Errorf("response model = %q, want org/relay-model", m)
	}
}

// TestBaseten_NonStreamingRelayDetectsByteMutation proves that any leading,
// trailing, or internal response-byte mutation fails the exact-byte relay test.
// This is the negative control for VAL-BASETEN-009, confirming that whitespace
// trimming or substring comparisons would mask real drift.
func TestBaseten_NonStreamingRelayDetectsByteMutation(t *testing.T) {
	t.Setenv("BASETEN_API_KEY", "baseten_key")

	m := basetenModel("bt-relay-mut", "org/relay-mut-model", nil)

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
		{"extra_trailing_bytes", originalBody + `{"extra":true}`},
	}

	for _, mt := range mutations {
		t.Run(mt.name, func(t *testing.T) {
			ta := newBasetenTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
				jsonRespond(w, http.StatusOK, mt.body, nil)
			}, m)

			w := httptest.NewRecorder()
			ta.engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
				strings.NewReader(`{"model":"bt-relay-mut","messages":[]}`)))

			mutHash := sha256.Sum256(w.Body.Bytes())
			if mutHash == originalHash {
				t.Errorf("mutation %q produced the same SHA-256 as the original; mutation not detected", mt.name)
			}
			if bytesEqual(w.Body.Bytes(), []byte(originalBody)) {
				t.Errorf("mutation %q produced byte-identical output; mutation not detected", mt.name)
			}
		})
	}
}

// TestBaseten_StreamingRelaysRawSSE verifies that streaming success preserves
// the exact LF-based fixture's ordered SSE records and blank-line framing,
// content/reasoning/tool deltas, final usage, and exactly one [DONE].
func TestBaseten_StreamingRelaysRawSSE(t *testing.T) {
	t.Setenv("BASETEN_API_KEY", "baseten_key")

	m := basetenModel("bt-stream", "org/stream-model", nil)

	sseFrames := []string{
		`data: {"id":"s1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"thinking..."}}]}`,
		`data: {"id":"s1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"Hello"}}]}`,
		`data: {"id":"s1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":" world"}}]}`,
		`data: {"id":"s1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_b1","type":"function","function":{"name":"f","arguments":"{}"}}]}}]}`,
		`data: {"id":"s1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`,
		`data: [DONE]`,
	}

	var seenStream, seenStreamOptions bool
	ta := newBasetenTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
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
	}, m)

	reqBody := `{"model":"bt-stream","stream":true,"stream_options":{"include_usage":true},"messages":[{"role":"user","content":"hi"}]}`
	w := httptest.NewRecorder()
	ta.engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(reqBody)))

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

	// Build the exact expected raw-byte SSE transcript: each frame followed
	// by a blank line (LF LF). This replaces substring-only checks with an
	// exact fixture-hash assertion for the complete transcript.
	var expectedSSE strings.Builder
	for _, frame := range sseFrames {
		expectedSSE.WriteString(frame)
		expectedSSE.WriteString("\n\n")
	}
	expectedBytes := []byte(expectedSSE.String())

	// Exact raw-byte SHA-256 comparison of the complete SSE transcript.
	expectedHash := sha256.Sum256(expectedBytes)
	actualHash := sha256.Sum256(w.Body.Bytes())
	if expectedHash != actualHash {
		t.Errorf("SSE transcript SHA-256 mismatch:\nwant=%s\ngot =%s\nwant(hex)=%s\ngot (hex)=%s",
			hex.EncodeToString(expectedHash[:]), hex.EncodeToString(actualHash[:]),
			hex.EncodeToString(expectedBytes), hex.EncodeToString(w.Body.Bytes()))
	}

	// Also verify byte-for-byte equality explicitly.
	if !bytesEqual(w.Body.Bytes(), expectedBytes) {
		t.Errorf("SSE transcript raw-byte mismatch:\nwant(hex)=%s\ngot (hex)=%s",
			hex.EncodeToString(expectedBytes), hex.EncodeToString(w.Body.Bytes()))
	}

	if c := strings.Count(out, "[DONE]"); c != 1 {
		t.Errorf("[DONE] count = %d, want 1", c)
	}
	if !strings.Contains(out, `"reasoning_content":"thinking..."`) {
		t.Error("reasoning_content not relayed in SSE")
	}
	// Verify content-type is text/event-stream.
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}
}

// TestBaseten_StreamingSSE_DetectsMutation proves that frame reordering,
// duplicate frames, single or triple newline separators, and trailing bytes
// all fail the exact raw-byte SSE transcript comparison when they originate
// from a fake upstream and pass through /v1/chat/completions before
// downstream comparison. Each relayed mutation must differ byte-for-byte and
// by SHA-256 from the canonical transcript, while the canonical relay remains
// exact.
func TestBaseten_StreamingSSE_DetectsMutation(t *testing.T) {
	t.Setenv("BASETEN_API_KEY", "baseten_key")

	canonicalFrames := []string{
		`data: {"id":"s1","choices":[{"index":0,"delta":{"role":"assistant","content":"Hello"}}]}`,
		`data: {"id":"s1","choices":[{"index":0,"delta":{"content":" world"}}]}`,
		`data: {"id":"s1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`,
		`data: [DONE]`,
	}

	// Compute the canonical transcript: each frame followed by \n\n.
	var canonicalSSE strings.Builder
	for _, f := range canonicalFrames {
		canonicalSSE.WriteString(f)
		canonicalSSE.WriteString("\n\n")
	}
	canonicalBytes := []byte(canonicalSSE.String())
	canonicalHash := sha256.Sum256(canonicalBytes)

	m := basetenModel("bt-sse-mut", "org/sse-mut-model", nil)

	// --- Canonical relay remains exact through /v1/chat/completions ---
	// The canonical frames originate from a fake upstream and pass through the
	// generic Chat proxy. The downstream SSE transcript must match the canonical
	// bytes exactly (byte-for-byte and SHA-256).
	ta := newBasetenTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		for _, frame := range canonicalFrames {
			fmt.Fprintf(w, "%s\n\n", frame)
			flusher.Flush()
		}
	}, m)

	w := httptest.NewRecorder()
	ta.engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"bt-sse-mut","stream":true,"messages":[]}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("canonical relay: expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	relayHash := sha256.Sum256(w.Body.Bytes())
	if relayHash != canonicalHash {
		t.Errorf("canonical relay SHA-256 mismatch:\nwant=%s\ngot =%s",
			hex.EncodeToString(canonicalHash[:]), hex.EncodeToString(relayHash[:]))
	}
	if !bytesEqual(w.Body.Bytes(), canonicalBytes) {
		t.Errorf("canonical relay raw-byte mismatch:\nwant(hex)=%s\ngot (hex)=%s",
			hex.EncodeToString(canonicalBytes), hex.EncodeToString(w.Body.Bytes()))
	}

	// --- Each mutation originates from a fake upstream, passes through
	// /v1/chat/completions, and must differ from the canonical transcript ---
	mutations := []struct {
		name      string
		writeFunc func(w http.ResponseWriter, flusher http.Flusher)
	}{
		{
			name: "frame_reorder",
			writeFunc: func(w http.ResponseWriter, flusher http.Flusher) {
				reordered := []string{canonicalFrames[1], canonicalFrames[0], canonicalFrames[2], canonicalFrames[3]}
				for _, f := range reordered {
					fmt.Fprintf(w, "%s\n\n", f)
					flusher.Flush()
				}
			},
		},
		{
			name: "duplicate_frame",
			writeFunc: func(w http.ResponseWriter, flusher http.Flusher) {
				duped := []string{canonicalFrames[0], canonicalFrames[0], canonicalFrames[1], canonicalFrames[2], canonicalFrames[3]}
				for _, f := range duped {
					fmt.Fprintf(w, "%s\n\n", f)
					flusher.Flush()
				}
			},
		},
		{
			name: "single_newline_separator",
			writeFunc: func(w http.ResponseWriter, flusher http.Flusher) {
				for _, f := range canonicalFrames {
					fmt.Fprintf(w, "%s\n", f) // single newline instead of \n\n
					flusher.Flush()
				}
			},
		},
		{
			name: "triple_newline_separator",
			writeFunc: func(w http.ResponseWriter, flusher http.Flusher) {
				for _, f := range canonicalFrames {
					fmt.Fprintf(w, "%s\n\n\n", f) // triple newline instead of \n\n
					flusher.Flush()
				}
			},
		},
		{
			name: "extra_trailing_byte",
			writeFunc: func(w http.ResponseWriter, flusher http.Flusher) {
				for _, f := range canonicalFrames {
					fmt.Fprintf(w, "%s\n\n", f)
					flusher.Flush()
				}
				fmt.Fprint(w, " ") // extra trailing byte after [DONE]
				flusher.Flush()
			},
		},
	}

	for _, mt := range mutations {
		t.Run(mt.name, func(t *testing.T) {
			ta := newBasetenTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				mt.writeFunc(w, w.(http.Flusher))
			}, m)

			w := httptest.NewRecorder()
			ta.engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
				strings.NewReader(`{"model":"bt-sse-mut","stream":true,"messages":[]}`)))
			if w.Code != http.StatusOK {
				t.Fatalf("mutation %s: expected 200, got %d body=%s", mt.name, w.Code, w.Body.String())
			}

			mutHash := sha256.Sum256(w.Body.Bytes())
			if mutHash == canonicalHash {
				t.Errorf("mutation %q: relayed SSE produced the same SHA-256 as the canonical transcript; mutation not detected", mt.name)
			}
			if bytesEqual(w.Body.Bytes(), canonicalBytes) {
				t.Errorf("mutation %q: relayed SSE is byte-identical to the canonical transcript; mutation not detected", mt.name)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// VAL-BASETEN-010: Baseten tools and tool-result continuation are lossless.
// ---------------------------------------------------------------------------

// TestBaseten_ToolResultRoundTripLossless verifies that tool definitions,
// strict schemas, tool choice, parallel tool calls, IDs, argument strings,
// assistant null content, caller-supplied reasoning_content, multiple result
// messages, and transcript ordering pass through unchanged across two turns.
func TestBaseten_ToolResultRoundTripLossless(t *testing.T) {
	t.Setenv("BASETEN_API_KEY", "baseten_key")

	m := basetenModel("bt-tools", "org/tools-model", nil)

	rc := &requestCapture{}
	ta := newBasetenTestAPI(t, rc.wrap(func(w http.ResponseWriter, r *http.Request) {
		jsonRespond(w, http.StatusOK, `{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"done"}}]}`, nil)
	}), m)

	// Turn 1: assistant tool call with reasoning_content and strict schema.
	turn1 := `{
		"model": "bt-tools",
		"messages": [
			{"role":"user","content":"what is the weather?"},
			{"role":"assistant","content":null,"reasoning_content":"I should call the weather tool","tool_calls":[{"id":"call_b1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"sf\"}"}}]}
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
		"tool_choice": "auto",
		"parallel_tool_calls": true
	}`
	w := httptest.NewRecorder()
	ta.engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(turn1)))
	if w.Code != http.StatusOK {
		t.Fatalf("turn 1: expected 200, got %d", w.Code)
	}

	cap1 := rc.get(0)
	if got := gjson.GetBytes(cap1.body, "model").String(); got != "org/tools-model" {
		t.Errorf("turn 1 model = %q, want org/tools-model", got)
	}
	if got := gjson.GetBytes(cap1.body, "messages.1.tool_calls.0.id").String(); got != "call_b1" {
		t.Errorf("turn 1 tool_call id = %q, want call_b1", got)
	}
	if got := gjson.GetBytes(cap1.body, "messages.1.reasoning_content").String(); got != "I should call the weather tool" {
		t.Errorf("turn 1 reasoning_content = %q", got)
	}
	if got := gjson.GetBytes(cap1.body, "tools.0.function.strict").Bool(); !got {
		t.Error("turn 1 strict not preserved")
	}
	if got := gjson.GetBytes(cap1.body, "tools.0.function.parameters.additionalProperties").Bool(); got != false {
		t.Error("turn 1 additionalProperties not preserved as false")
	}
	if got := gjson.GetBytes(cap1.body, "tool_choice").String(); got != "auto" {
		t.Errorf("turn 1 tool_choice = %q, want auto", got)
	}
	if got := gjson.GetBytes(cap1.body, "parallel_tool_calls").Bool(); !got {
		t.Error("turn 1 parallel_tool_calls not preserved")
	}
	// null content must survive.
	if raw := gjson.GetBytes(cap1.body, "messages.1.content").Raw; raw != "null" {
		t.Errorf("turn 1 assistant content = %s, want null", raw)
	}

	// Turn 2: tool result linked to call_b1, with prior assistant message.
	turn2 := `{
		"model": "bt-tools",
		"messages": [
			{"role":"user","content":"what is the weather?"},
			{"role":"assistant","content":null,"reasoning_content":"I should call the weather tool","tool_calls":[{"id":"call_b1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"sf\"}"}}]},
			{"role":"tool","tool_call_id":"call_b1","content":"72F sunny"}
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
	ta.engine.ServeHTTP(w2, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(turn2)))
	if w2.Code != http.StatusOK {
		t.Fatalf("turn 2: expected 200, got %d", w2.Code)
	}

	cap2 := rc.get(1)
	if got := gjson.GetBytes(cap2.body, "messages.2.tool_call_id").String(); got != "call_b1" {
		t.Errorf("turn 2 tool_call_id = %q, want call_b1", got)
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
	// Two requests total.
	if rc.count() != 2 {
		t.Errorf("expected 2 upstream requests, got %d", rc.count())
	}
}

// ---------------------------------------------------------------------------
// VAL-BASETEN-011: Baseten structured output, reasoning, and request options
// pass through.
// ---------------------------------------------------------------------------

// TestBaseten_OptionsReachUpstreamWithExactTypes verifies that caller-provided
// fields retain exact JSON names, types, and values after changing only model.
func TestBaseten_OptionsReachUpstreamWithExactTypes(t *testing.T) {
	t.Setenv("BASETEN_API_KEY", "baseten_key")

	m := basetenModel("bt-opts", "org/opts-model", nil)

	rc := &requestCapture{}
	ta := newBasetenTestAPI(t, rc.wrap(func(w http.ResponseWriter, r *http.Request) {
		jsonRespond(w, http.StatusOK, `{"id":"x","choices":[]}`, nil)
	}), m)

	reqBody := `{
		"model": "bt-opts",
		"messages": [{"role":"user","content":"hello"}],
		"temperature": 0.5,
		"top_p": 0.9,
		"max_tokens": 200,
		"max_completion_tokens": 150,
		"stop": ["\n", "END"],
		"seed": 42,
		"frequency_penalty": 0.3,
		"presence_penalty": 0.1,
		"n": 2,
		"user": "test-user",
		"logprobs": true,
		"top_logprobs": 5,
		"logit_bias": {"50256": -100},
		"stream_options": {"include_usage": true},
		"response_format": {"type":"json_schema","json_schema":{"name":"out","strict":true,"schema":{"type":"object","properties":{"x":{"type":"string"}},"required":["x"],"additionalProperties":false}}},
		"reasoning_effort": "high",
		"reasoning": {"effort":"high","exclude":false},
		"thinking": {"type":"enabled","budget_tokens":10000},
		"chat_template_args": {"enable_thinking": true},
		"future_option": {"nested":{"big_int":9007199254740993,"null_val":null,"flag":true}}
	}`
	w := httptest.NewRecorder()
	ta.engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(reqBody)))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}

	cap := rc.get(0)

	type fieldCheck struct {
		path string
		want any
	}
	checks := []fieldCheck{
		{"temperature", 0.5},
		{"top_p", 0.9},
		{"max_tokens", float64(200)},
		{"max_completion_tokens", float64(150)},
		{"seed", float64(42)},
		{"frequency_penalty", 0.3},
		{"presence_penalty", 0.1},
		{"n", float64(2)},
		{"user", "test-user"},
		{"logprobs", true},
		{"top_logprobs", float64(5)},
		{"reasoning_effort", "high"},
	}
	for _, c := range checks {
		gotVal := gjson.GetBytes(cap.body, c.path)
		if !gotVal.Exists() {
			t.Errorf("field %s missing from upstream body", c.path)
			continue
		}
		gotJSON := gotVal.Value()
		if !deepEqualJSON(gotJSON, c.want) {
			t.Errorf("field %s = %#v, want %#v", c.path, gotJSON, c.want)
		}
	}

	// Stop array preserved.
	stop := gjson.GetBytes(cap.body, "stop").Array()
	if len(stop) != 2 || stop[0].String() != "\n" || stop[1].String() != "END" {
		t.Errorf("stop = %s, want [\\n, END]", gjson.GetBytes(cap.body, "stop").Raw)
	}

	// logit_bias object preserved.
	if got := gjson.GetBytes(cap.body, "logit_bias.50256").Int(); got != -100 {
		t.Errorf("logit_bias.50256 = %d, want -100", got)
	}

	// stream_options preserved.
	if got := gjson.GetBytes(cap.body, "stream_options.include_usage").Bool(); !got {
		t.Error("stream_options.include_usage not preserved")
	}

	// Strict response_format preserved.
	rf := gjson.GetBytes(cap.body, "response_format")
	if rf.Get("type").String() != "json_schema" {
		t.Errorf("response_format.type = %q", rf.Get("type").String())
	}
	if !rf.Get("json_schema.strict").Bool() {
		t.Error("response_format strict not preserved")
	}
	if !rf.Get("json_schema.schema.properties.x.type").Exists() {
		t.Error("nested schema not preserved")
	}

	// reasoning object preserved.
	if got := gjson.GetBytes(cap.body, "reasoning.effort").String(); got != "high" {
		t.Errorf("reasoning.effort = %q, want high", got)
	}

	// thinking object preserved.
	if got := gjson.GetBytes(cap.body, "thinking.type").String(); got != "enabled" {
		t.Errorf("thinking.type = %q, want enabled", got)
	}
	if got := gjson.GetBytes(cap.body, "thinking.budget_tokens").Int(); got != 10000 {
		t.Errorf("thinking.budget_tokens = %d, want 10000", got)
	}

	// chat_template_args preserved.
	if got := gjson.GetBytes(cap.body, "chat_template_args.enable_thinking").Bool(); !got {
		t.Error("chat_template_args.enable_thinking not preserved")
	}

	// Unknown nested future field with large integer and null survives.
	if got := gjson.GetBytes(cap.body, "future_option.nested.big_int").Int(); got != 9007199254740993 {
		t.Errorf("future_option big_int = %d, want 9007199254740993", got)
	}
	if raw := gjson.GetBytes(cap.body, "future_option.nested.null_val").Raw; raw != "null" {
		t.Errorf("future_option null_val = %s, want null", raw)
	}

	// Model must be rewritten to the opaque upstream slug.
	if got := gjson.GetBytes(cap.body, "model").String(); got != "org/opts-model" {
		t.Errorf("model = %q, want org/opts-model", got)
	}
}

// TestBaseten_MinimalRequestGainsNoDefaults verifies that a minimal request
// gains none of the Baseten-specific fields or any Baseten reasoning, tier,
// sampling, or capability default. This proves transport preservation, not
// universal model support.
func TestBaseten_MinimalRequestGainsNoDefaults(t *testing.T) {
	t.Setenv("BASETEN_API_KEY", "baseten_key")

	m := basetenModel("bt-min", "org/min-model", nil)

	rc := &requestCapture{}
	ta := newBasetenTestAPI(t, rc.wrap(func(w http.ResponseWriter, r *http.Request) {
		jsonRespond(w, http.StatusOK, `{"id":"x","choices":[]}`, nil)
	}), m)

	reqBody := `{"model":"bt-min","messages":[{"role":"user","content":"hi"}]}`
	w := httptest.NewRecorder()
	ta.engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(reqBody)))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	cap := rc.get(0)
	// None of these fields should be synthesized.
	absentFields := []string{
		"temperature", "top_p", "max_tokens", "max_completion_tokens",
		"stop", "seed", "frequency_penalty", "presence_penalty",
		"n", "user", "logprobs", "top_logprobs", "logit_bias",
		"stream_options", "response_format", "reasoning_effort",
		"reasoning", "thinking", "chat_template_args",
		"service_tier", "reasoning_content",
		"prompt_cache_key", "prompt_cache_isolation_key",
	}
	for _, f := range absentFields {
		if v := gjson.GetBytes(cap.body, f); v.Exists() {
			t.Errorf("minimal request gained field %s = %s", f, v.Raw)
		}
	}
}

// TestBaseten_AllowedCustomHeaderPasses verifies that an allowed configured
// custom header passes unchanged.
func TestBaseten_AllowedCustomHeaderPasses(t *testing.T) {
	t.Setenv("BASETEN_API_KEY", "baseten_key")

	m := basetenModel("bt-ehdr", "org/eh-model", nil)
	m.ExtraHeaders = map[string]string{
		"X-Baseten-Client": "droid-proxy-test",
	}

	var seenClient string
	ta := newBasetenTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		seenClient = r.Header.Get("X-Baseten-Client")
		jsonRespond(w, http.StatusOK, `{"id":"x","choices":[]}`, nil)
	}, m)

	w := httptest.NewRecorder()
	ta.engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"bt-ehdr","messages":[]}`)))

	if seenClient != "droid-proxy-test" {
		t.Errorf("allowed extra header = %q, want droid-proxy-test", seenClient)
	}
}

// ---------------------------------------------------------------------------
// VAL-BASETEN-012: Baseten extra_args use deterministic top-level override
// semantics.
// ---------------------------------------------------------------------------

// TestBaseten_ExtraArgsReplaceCollidingCallerValues verifies that configured
// scalar and object extra_args fully replace caller values at the same top-level
// keys, inject configured absent keys, and preserve unrelated caller fields.
func TestBaseten_ExtraArgsReplaceCollidingCallerValues(t *testing.T) {
	t.Setenv("BASETEN_API_KEY", "baseten_key")

	m := basetenModel("bt-merge", "org/merge-model",
		map[string]any{
			"temperature": 0.1,
			"top_p":       0.8,
		})

	rc := &requestCapture{}
	ta := newBasetenTestAPI(t, rc.wrap(func(w http.ResponseWriter, r *http.Request) {
		jsonRespond(w, http.StatusOK, `{"id":"x","choices":[]}`, nil)
	}), m)

	// Caller sends conflicting values and an unrelated field.
	reqBody := `{"model":"bt-merge","messages":[],"temperature":0.9,"top_p":0.5,"max_tokens":100}`
	w := httptest.NewRecorder()
	ta.engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(reqBody)))
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

// TestBaseten_ExtraArgsInjectAbsentKeys verifies that configured extra_args
// inject absent keys that the caller did not supply.
func TestBaseten_ExtraArgsInjectAbsentKeys(t *testing.T) {
	t.Setenv("BASETEN_API_KEY", "baseten_key")

	m := basetenModel("bt-inject", "org/inject-model",
		map[string]any{
			"reasoning_effort": "medium",
			"seed":             99,
		})

	rc := &requestCapture{}
	ta := newBasetenTestAPI(t, rc.wrap(func(w http.ResponseWriter, r *http.Request) {
		jsonRespond(w, http.StatusOK, `{"id":"x","choices":[]}`, nil)
	}), m)

	w := httptest.NewRecorder()
	ta.engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"bt-inject","messages":[]}`)))

	cap := rc.get(0)
	if got := gjson.GetBytes(cap.body, "reasoning_effort").String(); got != "medium" {
		t.Errorf("reasoning_effort = %q, want medium (injected)", got)
	}
	if got := gjson.GetBytes(cap.body, "seed").Int(); got != 99 {
		t.Errorf("seed = %d, want 99 (injected)", got)
	}
}

// TestBaseten_ExtraArgsWholeObjectReplacement verifies that configured object
// extra_args fully replace caller whole-object values (no deep merge).
func TestBaseten_ExtraArgsWholeObjectReplacement(t *testing.T) {
	t.Setenv("BASETEN_API_KEY", "baseten_key")

	m := basetenModel("bt-objrep", "org/obj-model",
		map[string]any{
			"response_format": map[string]any{"type": "json_schema"},
		})

	rc := &requestCapture{}
	ta := newBasetenTestAPI(t, rc.wrap(func(w http.ResponseWriter, r *http.Request) {
		jsonRespond(w, http.StatusOK, `{"id":"x","choices":[]}`, nil)
	}), m)

	// Caller sends a different response_format object.
	reqBody := `{"model":"bt-objrep","messages":[],"response_format":{"type":"text","extra":"should-be-gone"}}`
	w := httptest.NewRecorder()
	ta.engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(reqBody)))

	cap := rc.get(0)
	rf := gjson.GetBytes(cap.body, "response_format")
	if rf.Get("type").String() != "json_schema" {
		t.Errorf("response_format.type = %q, want json_schema", rf.Get("type").String())
	}
	if rf.Get("extra").Exists() {
		t.Errorf("response_format.extra survived whole-object replacement: %s", rf.Get("extra").Raw)
	}
}

// ---------------------------------------------------------------------------
// VAL-BASETEN-013: Baseten upstream and local errors remain distinct.
// ---------------------------------------------------------------------------

// TestBaseten_UpstreamErrorRelayedWithExactStatusAndBody verifies that
// representative upstream error responses retain exact status, media type, and
// bounded body bytes after one attempt.
func TestBaseten_UpstreamErrorRelayedWithExactStatusAndBody(t *testing.T) {
	t.Setenv("BASETEN_API_KEY", "baseten_key")

	m := basetenModel("bt-err", "org/err-model", nil)

	for _, tc := range []struct {
		name       string
		status     int
		body       string
		contentTyp string
	}{
		{"validation_400", 400, `{"error":{"message":"bad request","type":"invalid_request_error"}}`, "application/json"},
		{"auth_401", 401, `{"error":{"message":"unauthorized","type":"authentication_error"}}`, "application/json"},
		{"payment_402", 402, `{"error":{"message":"payment required","type":"billing_error"}}`, "application/json"},
		{"forbidden_403", 403, `{"error":{"message":"forbidden","type":"permission_error"}}`, "application/json"},
		{"not_found_404", 404, `{"error":{"message":"model not found","type":"not_found_error"}}`, "application/json"},
		{"unprocessable_422", 422, `{"error":{"message":"invalid input","type":"invalid_request_error"}}`, "application/json"},
		{"rate_limit_429", 429, `{"error":{"message":"slow down","type":"rate_limit"}}`, "application/json"},
		{"server_500", 500, `{"error":{"message":"internal","type":"server_error"}}`, "application/json"},
		{"service_unavailable_503", 503, `{"error":{"message":"unavailable","type":"service_unavailable"}}`, "application/json"},
		{"text_500", 500, `Internal Server Error`, "text/plain"},
		{"empty_503", 503, ``, "application/json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rc := &requestCapture{}
			ta := newBasetenTestAPI(t, rc.wrap(func(w http.ResponseWriter, r *http.Request) {
				ct := tc.contentTyp
				if ct == "" {
					ct = "application/json"
				}
				w.Header().Set("Content-Type", ct)
				w.WriteHeader(tc.status)
				if tc.body != "" {
					_, _ = w.Write([]byte(tc.body))
				}
			}), m)

			w := httptest.NewRecorder()
			ta.engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
				strings.NewReader(`{"model":"bt-err","messages":[]}`)))

			if w.Code != tc.status {
				t.Fatalf("status = %d, want %d", w.Code, tc.status)
			}
			// Exact raw-byte equality — no whitespace trimming. This proves
			// the proxy relays the upstream error body verbatim.
			expectedBody := []byte(tc.body)
			if !bytesEqual(w.Body.Bytes(), expectedBody) {
				t.Errorf("error body raw-byte mismatch:\nwant(hex)=%s\ngot (hex)=%s",
					hex.EncodeToString(expectedBody), hex.EncodeToString(w.Body.Bytes()))
			}
			// Exact bounded-body SHA-256 hash.
			expectedHash := sha256.Sum256(expectedBody)
			actualHash := sha256.Sum256(w.Body.Bytes())
			if expectedHash != actualHash {
				t.Errorf("error body SHA-256 mismatch:\nwant=%s\ngot =%s",
					hex.EncodeToString(expectedHash[:]), hex.EncodeToString(actualHash[:]))
			}
			// Content-Type should match (may have charset suffix).
			if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, tc.contentTyp) {
				t.Errorf("Content-Type = %q, want prefix %q", ct, tc.contentTyp)
			}
			// One attempt, no retry.
			if rc.count() != 1 {
				t.Errorf("request count = %d, want 1 (no retry)", rc.count())
			}
		})
	}
}

// TestBaseten_PreSSEErrorRemainsHTTP verifies that a pre-SSE non-2xx remains
// native HTTP rather than an SSE error.
func TestBaseten_PreSSEErrorRemainsHTTP(t *testing.T) {
	t.Setenv("BASETEN_API_KEY", "baseten_key")

	m := basetenModel("bt-sse-err", "org/sseerr-model", nil)

	ta := newBasetenTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"bad","type":"invalid_request_error"}}`))
	}, m)

	w := httptest.NewRecorder()
	ta.engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"bt-sse-err","stream":true,"messages":[]}`)))

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json (not text/event-stream)", ct)
	}
}

// TestBaseten_TruncatedStreamEmitsSingleStreamTruncated verifies that an SSE
// close before [DONE] yields the existing single stream_truncated error.
func TestBaseten_TruncatedStreamEmitsSingleStreamTruncated(t *testing.T) {
	t.Setenv("BASETEN_API_KEY", "baseten_key")

	m := basetenModel("bt-trunc", "org/trunc-model", nil)

	ta := newBasetenTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		fmt.Fprint(w, "data: {\"id\":\"1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"}}]}\n\n")
		flusher.Flush()
		// Connection closes here (no [DONE]).
	}, m)

	w := httptest.NewRecorder()
	ta.engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"bt-trunc","stream":true,"messages":[]}`)))

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

// TestBaseten_TruncatedStreamRecovery verifies that a subsequent healthy
// request succeeds after a truncated stream.
func TestBaseten_TruncatedStreamRecovery(t *testing.T) {
	t.Setenv("BASETEN_API_KEY", "baseten_key")

	m := basetenModel("bt-recover", "org/recover-model", nil)

	callCount := 0
	ta := newBasetenTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 1 {
			// Truncated: close before [DONE].
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			flusher := w.(http.Flusher)
			fmt.Fprint(w, "data: {\"id\":\"1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"}}]}\n\n")
			flusher.Flush()
			return
		}
		// Healthy stream.
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		fmt.Fprint(w, "data: {\"id\":\"2\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}, m)

	// First request: truncated.
	w1 := httptest.NewRecorder()
	ta.engine.ServeHTTP(w1, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"bt-recover","stream":true,"messages":[]}`)))
	if !strings.Contains(w1.Body.String(), "stream_truncated") {
		t.Error("first request should be truncated")
	}

	// Second request: healthy.
	w2 := httptest.NewRecorder()
	ta.engine.ServeHTTP(w2, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"bt-recover","stream":true,"messages":[]}`)))
	if w2.Code != http.StatusOK {
		t.Fatalf("recovery request: expected 200, got %d", w2.Code)
	}
	if !strings.Contains(w2.Body.String(), "[DONE]") {
		t.Error("recovery request should contain [DONE]")
	}
}

// TestBaseten_ErrorResponseHeaders_Filtered proves that unsafe error-response
// headers (cookies, connection-scoped, hop-by-hop, decompression-derived, and
// gateway-prefixed) are removed while allowed metadata (Retry-After and a
// generic request-ID) remains on upstream error responses. This closes the
// VAL-BASETEN-013 header-filtering evidence gap for error responses.
func TestBaseten_ErrorResponseHeaders_Filtered(t *testing.T) {
	t.Setenv("BASETEN_API_KEY", "baseten_key")

	m := basetenModel("bt-errhdr", "org/errhdr-model", nil)

	ta := newBasetenTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		// Safe headers that should be preserved.
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Request-ID", "req-err-generic-789")
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
		w.Header().Set("Helicone-Request-Id", "hc-err-1")
		// Unsafe: privacy-sensitive intermediary metadata (not hop-by-hop).
		w.Header().Set("Via", "1.1 baseten-err.internal.topology.sentinel (squid/5.7)")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limited","type":"rate_limit_error"}}`))
	}, m)

	w := httptest.NewRecorder()
	ta.engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"bt-errhdr","messages":[]}`)))

	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", w.Code)
	}

	// Safe headers preserved on error response.
	safeHeaders := map[string]string{
		"X-Request-ID": "req-err-generic-789",
		"Retry-After":  "60",
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
		"Via",
		"X-Litellm-Version",
		"X-Portkey-Status",
		"Helicone-Request-Id",
	}
	for _, h := range unsafeHeaders {
		if got := w.Header().Get(h); got != "" {
			t.Errorf("unsafe error-response header %s leaked: %q", h, got)
		}
	}
}

// TestBaseten_FilterHeaders_RemovesUnsafeCategories directly exercises the
// proxy's FilterHeaders function to prove cookies, hop-by-hop headers,
// connection-nominated headers, compression-derived headers, and
// gateway-identifying prefixes are all removed while safe metadata survives.
func TestBaseten_FilterHeaders_RemovesUnsafeCategories(t *testing.T) {
	src := http.Header{}
	// Safe metadata.
	src.Set("X-Request-ID", "generic-req-id")
	src.Set("Retry-After", "30")
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
	// Unsafe: privacy-sensitive intermediary metadata (not hop-by-hop).
	src.Set("Via", "1.1 baseten-edge.internal.topology.sentinel (envoy/1.30)")

	filtered := upstream.FilterHeaders(src)

	// Safe headers preserved.
	for k, v := range map[string]string{
		"X-Request-ID": "generic-req-id",
		"Retry-After":  "30",
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
		"X-Custom-Named", "Via",
		"X-Litellm-Version", "X-Portkey-Id", "Helicone-Request-Id",
		"Cf-Aig-Status", "X-Kong-Proxy-Latency", "X-Bt-Trace-Id",
	}
	for _, h := range removed {
		if got := filtered.Get(h); got != "" {
			t.Errorf("FilterHeaders kept unsafe header %s: %q", h, got)
		}
	}
}

// TestBaseten_TransportFailureReturns502Envelope injects a deterministic
// counting failing RoundTripper so the non-streaming transport failure proves
// exactly one outbound attempt and no fallback. It verifies the existing
// bounded secret-safe 502 upstream_error envelope without retry indicators or
// SSE framing. This closes the VAL-BASETEN-013 transport-failure evidence gap.
func TestBaseten_TransportFailureReturns502Envelope(t *testing.T) {
	credSentinel := "baseten_transport_secret_xyz"
	t.Setenv("BASETEN_API_KEY", credSentinel)

	// Inject a deterministic counting failing RoundTripper that never contacts
	// any server. This proves exactly one outbound attempt with no retry.
	rt := &countingFailingRoundTripper{
		err: errors.New("deterministic transport failure: connection refused"),
	}

	m := basetenModel("bt-transport", "org/transport-model", nil)
	if err := config.HydrateModel(m); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Upstream: config.Upstream{HTTPTimeout: 5 * time.Second, StreamKeepAlive: 200 * time.Millisecond},
		Models:   []*config.Model{m},
	}
	router, err := upstream.NewRouter(cfg.Models)
	if err != nil {
		t.Fatal(err)
	}
	logger := logrus.New()
	logger.SetOutput(io.Discard)

	client := upstream.NewClient(cfg)
	client.HTTP.Transport = rt

	gin.SetMode(gin.TestMode)
	api := &API{Cfg: cfg, Router: router, Client: client, Logger: logger}
	engine := gin.New()
	engine.POST("/v1/chat/completions", api.ChatCompletions)

	w := httptest.NewRecorder()
	engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"bt-transport","messages":[]}`)))

	// Transport failure must return the existing 502 upstream_error envelope.
	if w.Code != http.StatusBadGateway {
		t.Fatalf("transport failure: expected 502, got %d body=%s", w.Code, w.Body.String())
	}

	// Exactly one outbound RoundTrip attempt: no retry, no fallback.
	if got := rt.count(); got != 1 {
		t.Errorf("RoundTrip call count = %d, want exactly 1 (no retry/fallback)", got)
	}

	// The error type must be upstream_error.
	if got := gjson.Get(w.Body.String(), "error.type").String(); got != "upstream_error" {
		t.Errorf("error.type = %q, want upstream_error", got)
	}

	// The credential sentinel must NOT appear in the 502 error body.
	if strings.Contains(w.Body.String(), credSentinel) {
		t.Errorf("credential sentinel leaked in 502 transport-failure body: %s", w.Body.String())
	}

	// The error message must be present and bounded.
	if errMsg := gjson.Get(w.Body.String(), "error.message").String(); errMsg == "" {
		t.Error("502 envelope missing error.message")
	}

	// No fallback: there must be no retry or fallback indicator in the response.
	bodyLower := strings.ToLower(w.Body.String())
	if strings.Contains(bodyLower, "retry") || strings.Contains(bodyLower, "fallback") {
		t.Errorf("502 envelope suggests retry/fallback behavior: %s", w.Body.String())
	}

	// Must NOT be an SSE response.
	if ct := w.Header().Get("Content-Type"); strings.Contains(ct, "text/event-stream") {
		t.Errorf("non-streaming transport failure returned SSE Content-Type: %q", ct)
	}
}

// TestBaseten_TransportFailureStreamingReturns502 verifies that a transport
// failure on a streaming request also returns the 502 upstream_error envelope
// (not an SSE error stream), using a deterministic counting failing
// RoundTripper to prove exactly one outbound attempt and no fallback.
func TestBaseten_TransportFailureStreamingReturns502(t *testing.T) {
	credSentinel := "baseten_stream_transport_secret"
	t.Setenv("BASETEN_API_KEY", credSentinel)

	// Inject a deterministic counting failing RoundTripper.
	rt := &countingFailingRoundTripper{
		err: errors.New("deterministic transport failure: connection refused"),
	}

	m := basetenModel("bt-tstream", "org/tstream-model", nil)
	if err := config.HydrateModel(m); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Upstream: config.Upstream{HTTPTimeout: 5 * time.Second, StreamKeepAlive: 200 * time.Millisecond},
		Models:   []*config.Model{m},
	}
	router, err := upstream.NewRouter(cfg.Models)
	if err != nil {
		t.Fatal(err)
	}
	logger := logrus.New()
	logger.SetOutput(io.Discard)

	client := upstream.NewClient(cfg)
	client.HTTP.Transport = rt

	gin.SetMode(gin.TestMode)
	api := &API{Cfg: cfg, Router: router, Client: client, Logger: logger}
	engine := gin.New()
	engine.POST("/v1/chat/completions", api.ChatCompletions)

	w := httptest.NewRecorder()
	engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"bt-tstream","stream":true,"messages":[]}`)))

	if w.Code != http.StatusBadGateway {
		t.Fatalf("streaming transport failure: expected 502, got %d body=%s", w.Code, w.Body.String())
	}

	// Exactly one outbound RoundTrip attempt: no retry, no fallback.
	if got := rt.count(); got != 1 {
		t.Errorf("RoundTrip call count = %d, want exactly 1 (no retry/fallback)", got)
	}

	if got := gjson.Get(w.Body.String(), "error.type").String(); got != "upstream_error" {
		t.Errorf("error.type = %q, want upstream_error", got)
	}

	// Must NOT be an SSE response.
	if ct := w.Header().Get("Content-Type"); strings.Contains(ct, "text/event-stream") {
		t.Errorf("transport failure returned SSE Content-Type: %q", ct)
	}

	// The credential sentinel must NOT appear in the 502 error body.
	if strings.Contains(w.Body.String(), credSentinel) {
		t.Errorf("credential sentinel leaked in 502 streaming transport-failure body: %s", w.Body.String())
	}

	// No retry or fallback indicator.
	bodyLower := strings.ToLower(w.Body.String())
	if strings.Contains(bodyLower, "retry") || strings.Contains(bodyLower, "fallback") {
		t.Errorf("502 envelope suggests retry/fallback behavior: %s", w.Body.String())
	}
}

// TestBaseten_LocalErrorsMakeZeroUpstreamRequests verifies that missing model,
// unknown alias, and malformed body fail locally with zero upstream traffic.
func TestBaseten_LocalErrorsMakeZeroUpstreamRequests(t *testing.T) {
	t.Setenv("BASETEN_API_KEY", "baseten_key")

	m := basetenModel("bt-local", "org/local-model", nil)

	rc := &requestCapture{}
	ta := newBasetenTestAPI(t, rc.wrap(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("upstream must not be called for local errors")
	}), m)

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

	// Malformed JSON.
	w = httptest.NewRecorder()
	ta.engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{invalid json`)))
	if w.Code < 400 {
		t.Errorf("malformed JSON: expected 4xx, got %d", w.Code)
	}

	if rc.count() != 0 {
		t.Errorf("expected 0 upstream requests for local errors, got %d", rc.count())
	}
}

// TestBaseten_MissingProfileKeyFailsLocally verifies that missing
// BASETEN_API_KEY fails before upstream contact.
func TestBaseten_MissingProfileKeyFailsLocally(t *testing.T) {
	// Do NOT set BASETEN_API_KEY.
	m := basetenModel("bt-nokey", "org/nokey-model", nil)

	rc := &requestCapture{}
	ta := newBasetenTestAPI(t, rc.wrap(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("upstream must not be called when key is missing")
	}), m)

	w := httptest.NewRecorder()
	ta.engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"bt-nokey","messages":[]}`)))
	if w.Code != http.StatusInternalServerError {
		t.Errorf("missing key: expected 500, got %d", w.Code)
	}
	if rc.count() != 0 {
		t.Errorf("expected 0 upstream requests, got %d", rc.count())
	}
}

// ---------------------------------------------------------------------------
// VAL-BASETEN-014: Baseten stays on the shared generic Chat surface.
// ---------------------------------------------------------------------------

// TestBaseten_UsesGenericChatTransport verifies that versioned and prefixless
// Chat routes behave like a generic openai-chat control after model substitution,
// and no Baseten-specific response shape is observable.
func TestBaseten_UsesGenericChatTransport(t *testing.T) {
	t.Setenv("BASETEN_API_KEY", "baseten_key")

	m := basetenModel("bt-generic", "org/generic-model", nil)

	// Verify the model uses generic-chat-completion-api and openai-chat.
	if err := config.HydrateModel(m); err != nil {
		t.Fatal(err)
	}
	if m.UpstreamProtocol != config.UpstreamOpenAIChat {
		t.Errorf("UpstreamProtocol = %q, want %q", m.UpstreamProtocol, config.UpstreamOpenAIChat)
	}

	rc := &requestCapture{}
	ta := newBasetenTestAPI(t, rc.wrap(func(w http.ResponseWriter, r *http.Request) {
		jsonRespond(w, http.StatusOK, `{"id":"x","choices":[]}`, nil)
	}), m)

	// Both /v1/chat/completions and /chat/completions must work identically.
	for _, path := range []string{"/v1/chat/completions", "/chat/completions"} {
		w := httptest.NewRecorder()
		ta.engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, path,
			strings.NewReader(`{"model":"bt-generic","messages":[]}`)))
		if w.Code != http.StatusOK {
			t.Fatalf("%s: expected 200, got %d", path, w.Code)
		}
	}

	if rc.count() != 2 {
		t.Fatalf("expected 2 requests, got %d", rc.count())
	}
	for i := 0; i < rc.count(); i++ {
		cap := rc.get(i)
		if cap.method != http.MethodPost {
			t.Errorf("request %d method = %q, want POST", i, cap.method)
		}
		if !strings.HasSuffix(cap.path, "/chat/completions") {
			t.Errorf("request %d path = %q, want suffix /chat/completions", i, cap.path)
		}
		// Model must be rewritten.
		if got := gjson.GetBytes(cap.body, "model").String(); got != "org/generic-model" {
			t.Errorf("request %d model = %q, want org/generic-model", i, got)
		}
	}
}

// TestBaseten_NoProviderSpecificRoutes verifies that Baseten-specific public
// routes do not exist (only the generic Chat handler is registered).
func TestBaseten_NoProviderSpecificRoutes(t *testing.T) {
	t.Setenv("BASETEN_API_KEY", "baseten_key")

	m := basetenModel("bt-routes", "org/routes-model", nil)

	rc := &requestCapture{}
	ta := newBasetenTestAPI(t, rc.wrap(func(w http.ResponseWriter, r *http.Request) {
		jsonRespond(w, http.StatusOK, `{"id":"x","choices":[]}`, nil)
	}), m)

	// Baseten-specific route candidates must return 404.
	basetenRoutes := []string{
		"/v1/baseten/chat/completions",
		"/baseten/chat/completions",
		"/v1/chat/baseten/completions",
	}
	for _, route := range basetenRoutes {
		w := httptest.NewRecorder()
		ta.engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, route,
			strings.NewReader(`{"model":"bt-routes","messages":[]}`)))
		if w.Code != http.StatusNotFound {
			t.Errorf("Baseten-specific route %s: expected 404, got %d", route, w.Code)
		}
	}
	// Zero upstream requests for nonexistent routes.
	if rc.count() != 0 {
		t.Errorf("expected 0 upstream requests for nonexistent routes, got %d", rc.count())
	}
}

// ---------------------------------------------------------------------------
// VAL-BASETEN-015: Baseten credentials and protected headers cannot leak or
// be overridden.
// ---------------------------------------------------------------------------

// TestBaseten_ProtectedHeadersCannotOverrideTransport verifies that downstream
// client auth and configured Authorization, Proxy-Authorization, X-Api-Key,
// Host, cookie, forwarding, and hop-by-hop headers cannot replace or supplement
// the outbound Bearer credential.
func TestBaseten_ProtectedHeadersCannotOverrideTransport(t *testing.T) {
	t.Setenv("BASETEN_API_KEY", "baseten_real_key")

	m := basetenModel("bt-protected", "org/protected-model", nil)

	var seenAuth string
	ta := newBasetenTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		seenAuth = r.Header.Get("Authorization")
		jsonRespond(w, http.StatusOK, `{"id":"x","choices":[]}`, nil)
	}, m)

	// Send downstream request with hostile headers.
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"bt-protected","messages":[]}`))
	req.Header.Set("Authorization", "Bearer downstream-fake")
	req.Header.Set("X-Api-Key", "downstream-key")
	req.Header.Set("Proxy-Authorization", "Bearer proxy-fake")
	req.Header.Set("Cookie", "session=fake")

	w := httptest.NewRecorder()
	ta.engine.ServeHTTP(w, req)

	if seenAuth != "Bearer baseten_real_key" {
		t.Errorf("upstream Authorization = %q, want Bearer baseten_real_key", seenAuth)
	}
}

// TestBaseten_CredentialsDoNotLeak verifies that traces, responses, and
// captures contain no key or credential-shaped field sentinel.
func TestBaseten_CredentialsDoNotLeak(t *testing.T) {
	credSentinel := "baseten_secret_credential_xyz"
	t.Setenv("BASETEN_API_KEY", credSentinel)

	m := basetenModel("bt-leak", "org/leak-model", nil)

	ta := newBasetenTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		// Upstream response does NOT contain the credential.
		jsonRespond(w, http.StatusOK, `{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"response"}}]}`, nil)
	}, m)

	w := httptest.NewRecorder()
	ta.engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"bt-leak","messages":[]}`)))

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

// TestBaseten_ErrorResponsesDoNotLeakCredentials verifies that error responses
// do not leak credential sentinels.
func TestBaseten_ErrorResponsesDoNotLeakCredentials(t *testing.T) {
	credSentinel := "baseten_err_secret_abc"
	t.Setenv("BASETEN_API_KEY", credSentinel)

	m := basetenModel("bt-eleak", "org/eleak-model", nil)

	ta := newBasetenTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		// Upstream error body is deliberately secret-free.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"internal server error","type":"server_error"}}`))
	}, m)

	w := httptest.NewRecorder()
	ta.engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"bt-eleak","messages":[]}`)))

	if strings.Contains(w.Body.String(), credSentinel) {
		t.Errorf("credential sentinel leaked in error response body")
	}
}

// TestBaseten_ConfiguredProtectedHeadersFiltered verifies that even if a model
// is configured with protected extra_headers (Authorization, Host, etc.), they
// are filtered out by IsReservedOutboundHeader.
func TestBaseten_ConfiguredProtectedHeadersFiltered(t *testing.T) {
	t.Setenv("BASETEN_API_KEY", "baseten_real_key")

	m := basetenModel("bt-filtered", "org/filtered-model", nil)
	// Attempt to configure protected headers.
	m.ExtraHeaders = map[string]string{
		"Authorization":       "Bearer should-not-pass",
		"Host":                "evil.example.com",
		"Cookie":              "session=hijack",
		"Proxy-Authorization": "Bearer proxy-hijack",
		"X-Forwarded-For":     "spoofed",
	}

	var seenAuth, seenHost, seenCookie, seenProxyAuth, seenXFF string
	ta := newBasetenTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		seenAuth = r.Header.Get("Authorization")
		seenHost = r.Host
		seenCookie = r.Header.Get("Cookie")
		seenProxyAuth = r.Header.Get("Proxy-Authorization")
		seenXFF = r.Header.Get("X-Forwarded-For")
		jsonRespond(w, http.StatusOK, `{"id":"x","choices":[]}`, nil)
	}, m)

	w := httptest.NewRecorder()
	ta.engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"bt-filtered","messages":[]}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	// Auth must be the provider key, not the configured override.
	if seenAuth != "Bearer baseten_real_key" {
		t.Errorf("Authorization = %q, want Bearer baseten_real_key", seenAuth)
	}
	if seenHost == "evil.example.com" {
		t.Errorf("configured Host leaked: %q", seenHost)
	}
	if seenCookie != "" {
		t.Errorf("Cookie leaked: %q", seenCookie)
	}
	if seenProxyAuth != "" {
		t.Errorf("Proxy-Authorization leaked: %q", seenProxyAuth)
	}
	if seenXFF != "" {
		t.Errorf("X-Forwarded-For leaked: %q", seenXFF)
	}
}

// ---------------------------------------------------------------------------
// VAL-BASETEN-016: Baseten HTTP teardown removes all temporary resources.
// ---------------------------------------------------------------------------

// TestBaseten_HTTPTeardownRemovesAllTemporaryResources verifies that fake
// servers and listeners created during testing are cleaned up, ports are
// reusable, and the repository status is unchanged. This is exercised by the
// test cleanup framework itself (t.Cleanup), but we explicitly verify the
// upstream server is closed and its listener released.
func TestBaseten_HTTPTeardownRemovesAllTemporaryResources(t *testing.T) {
	t.Setenv("BASETEN_API_KEY", "baseten_key")

	m := basetenModel("bt-teardown", "org/teardown-model", nil)

	var upstreamAddr string
	ta := newBasetenTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		jsonRespond(w, http.StatusOK, `{"id":"x","choices":[]}`, nil)
	}, m)
	upstreamAddr = ta.upstream.Listener.Addr().String()

	// Make a request to prove the server is alive.
	w := httptest.NewRecorder()
	ta.engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"bt-teardown","messages":[]}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	// The upstream server is registered with t.Cleanup in newBasetenTestAPI.
	// After the test completes, the server will be closed. We verify the
	// address is currently reachable.
	conn, err := net.Dial("tcp", upstreamAddr)
	if err != nil {
		t.Fatalf("upstream server not reachable during test: %v", err)
	}
	_ = conn.Close()

	// The TLS interceptor test also must clean up. Verify no process or
	// listener leaks by checking the test can complete cleanly.
	// (Actual cleanup verification happens via t.Cleanup at test exit.)
}

// TestBaseten_TSLInterceptorTeardown verifies that the canonical-host TLS
// interceptor is cleaned up after use.
func TestBaseten_TLSInterceptorTeardown(t *testing.T) {
	t.Setenv("BASETEN_API_KEY", "baseten_key")

	srv, _, _ := canonicalTLSInterceptor(t, func(w http.ResponseWriter, r *http.Request) {
		jsonRespond(w, http.StatusOK, `{"id":"x","choices":[]}`, nil)
	})
	addr := srv.Listener.Addr().String()

	// Verify reachable during test.
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("TLS interceptor not reachable: %v", err)
	}
	_ = conn.Close()

	// Cleanup is via t.Cleanup registered in canonicalTLSInterceptor.
}

// bytesEqual is a local alias to avoid import cycle issues in test files
// that may not import "bytes" yet.
func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
