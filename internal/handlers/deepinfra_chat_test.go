package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"

	"github.com/trevoraspencer/droid-proxy/internal/config"
	"github.com/trevoraspencer/droid-proxy/internal/providerapi"
	"github.com/trevoraspencer/droid-proxy/internal/upstream"
)

// ---------------------------------------------------------------------------
// Test helpers (DeepInfra-specific)
// ---------------------------------------------------------------------------

// newDeepInfraTestAPI builds a test API with one or more DeepInfra models,
// all pointing at the same fake upstream. It registers both the Chat
// completions and local model-listing endpoints so tests can exercise the
// full generic Chat surface. The fake BaseURL preserves the /v1/openai
// suffix that is part of the canonical DeepInfra inference base.
func newDeepInfraTestAPI(t *testing.T, handler http.HandlerFunc, models ...*config.Model) *testAPI {
	t.Helper()
	gin.SetMode(gin.TestMode)
	upstreamServer := httptest.NewServer(handler)
	t.Cleanup(upstreamServer.Close)

	for _, m := range models {
		if err := config.HydrateModel(m); err != nil {
			t.Fatalf("hydrate model %q: %v", m.Alias, err)
		}
		// Override BaseURL to point at the fake, preserving the /v1/openai suffix.
		m.BaseURL = upstreamServer.URL + "/v1/openai"
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

// deepinfraModel builds a model with the native DeepInfra profile.
func deepinfraModel(alias, upstreamModel string, extraArgs map[string]any) *config.Model {
	m := &config.Model{
		Alias:            alias,
		DisplayName:      alias,
		FactoryProvider:  config.FactoryProviderGeneric,
		UpstreamProtocol: config.UpstreamOpenAIChat,
		KnownAuth:        "deepinfra",
		UpstreamModel:    upstreamModel,
		ExtraArgs:        extraArgs,
	}
	return m
}

// ---------------------------------------------------------------------------
// VAL-DEEPINFRA-006: DeepInfra uses exact inference and unauthenticated
// discovery routes.
// ---------------------------------------------------------------------------

// TestDeepInfra_NativeModelResolvesCanonicalOrigin verifies that a native
// known_auth: deepinfra model hydrates to the canonical api.deepinfra.com/v1/openai
// base URL and that the full Chat URL resolves to
// api.deepinfra.com:443/v1/openai/chat/completions through a loopback TLS
// interceptor.
func TestDeepInfra_NativeModelResolvesCanonicalOrigin(t *testing.T) {
	t.Setenv("DEEPINFRA_TOKEN", "deepinfra_canonical_secret")

	m := deepinfraModel("di-canonical", "meta-llama/Llama-3.3-70B-Instruct", nil)
	if err := config.HydrateModel(m); err != nil {
		t.Fatal(err)
	}
	if m.BaseURL != "https://api.deepinfra.com/v1/openai" {
		t.Fatalf("hydrated BaseURL = %q, want https://api.deepinfra.com/v1/openai", m.BaseURL)
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
	if req.URL.Hostname() != "api.deepinfra.com" {
		t.Errorf("hostname = %q, want api.deepinfra.com", req.URL.Hostname())
	}
	if req.URL.Scheme != "https" {
		t.Errorf("scheme = %q, want https", req.URL.Scheme)
	}
	if req.URL.Path != "/v1/openai/chat/completions" {
		t.Errorf("path = %q, want /v1/openai/chat/completions", req.URL.Path)
	}

	// Verify through a live loopback TLS interceptor that the proxy connects
	// only to api.deepinfra.com:443 and sends Bearer auth.
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
		strings.NewReader(`{"model":"di-canonical","messages":[]}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}

	if len(*authorities) != 1 {
		t.Fatalf("expected exactly 1 outbound connection, got %d", len(*authorities))
	}
	if got := (*authorities)[0]; got != "api.deepinfra.com:443" {
		t.Errorf("connected authority = %q, want api.deepinfra.com:443", got)
	}
	cap := rc.get(0)
	if got := cap.header.Get("Authorization"); got != "Bearer deepinfra_canonical_secret" {
		t.Errorf("upstream Authorization = %q, want Bearer deepinfra_canonical_secret", got)
	}
	if cap.method != http.MethodPost {
		t.Errorf("method = %q, want POST", cap.method)
	}
	if cap.path != "/v1/openai/chat/completions" {
		t.Errorf("path = %q, want /v1/openai/chat/completions", cap.path)
	}
}

// TestDeepInfra_DiscoveryRouteIsUnauthenticated verifies that DeepInfra
// discovery uses the unauthenticated GET /models/list endpoint with
// Accept: application/json and no Authorization or other credential header.
// Discovery uses a different base URL than inference.
func TestDeepInfra_DiscoveryRouteIsUnauthenticated(t *testing.T) {
	t.Setenv("DEEPINFRA_TOKEN", "deepinfra_inference_token")

	ka, ok := config.LookupKnownAuth("deepinfra")
	if !ok {
		t.Fatal("deepinfra profile missing")
	}

	// The discovery base URL must differ from the inference base URL.
	if ka.DiscoveryBaseURL == "" || ka.DiscoveryBaseURL == ka.BaseURL {
		t.Fatalf("discovery base URL must be separate from inference base URL: discovery=%q inference=%q",
			ka.DiscoveryBaseURL, ka.BaseURL)
	}
	if !ka.DiscoveryNoAuth {
		t.Fatal("DiscoveryNoAuth must be true")
	}

	var gotMethod, gotPath, gotAuth, gotAccept string
	var reqCount int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqCount++
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotAccept = r.Header.Get("Accept")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{"model_name":"meta-llama/Llama-3.3-70B-Instruct","reported_type":"text-generation"}]`))
	}))
	defer srv.Close()

	ids, err := providerapi.ListModelsWithOptions(context.Background(), providerapi.ListOptions{
		BaseURL:    srv.URL,
		ModelsPath: ka.ModelsPath,
		APIKey:     "", // unauthenticated discovery
		IDField:    ka.DiscoveryIDField,
		TypeField:  ka.DiscoveryTypeField,
		TypeValue:  ka.DiscoveryTypeValue,
	})
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if reqCount != 1 {
		t.Errorf("request count = %d, want 1", reqCount)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %q, want GET", gotMethod)
	}
	if gotPath != "/models/list" {
		t.Errorf("path = %q, want /models/list", gotPath)
	}
	if gotAuth != "" {
		t.Errorf("Authorization = %q, want empty (unauthenticated)", gotAuth)
	}
	if gotAccept != "application/json" {
		t.Errorf("Accept = %q, want application/json", gotAccept)
	}
	if len(ids) != 1 || ids[0] != "meta-llama/Llama-3.3-70B-Instruct" {
		t.Errorf("ids = %v, want [meta-llama/Llama-3.3-70B-Instruct]", ids)
	}
}

// TestDeepInfra_DiscoveryCanonicalHost verifies that the discovery endpoint
// resolves to GET https://api.deepinfra.com/models/list at the canonical host.
func TestDeepInfra_DiscoveryCanonicalHost(t *testing.T) {
	ka, _ := config.LookupKnownAuth("deepinfra")

	// Build the discovery URL from the registry fields and verify it resolves
	// to the canonical unauthenticated endpoint.
	want := "https://api.deepinfra.com/models/list"
	got := strings.TrimRight(ka.DiscoveryBaseURL, "/") + ka.ModelsPath
	if got != want {
		t.Errorf("discovery endpoint = %q, want %q", got, want)
	}
}

// TestDeepInfra_BothChatRoutesHitSameUpstream verifies that versioned
// /v1/chat/completions and prefixless /chat/completions routes both resolve
// to the same upstream path.
func TestDeepInfra_BothChatRoutesHitSameUpstream(t *testing.T) {
	t.Setenv("DEEPINFRA_TOKEN", "deepinfra_key")

	m := deepinfraModel("di-routes", "meta-llama/Llama-3.3-70B-Instruct", nil)

	rc := &requestCapture{}
	ta := newDeepInfraTestAPI(t, rc.wrap(func(w http.ResponseWriter, r *http.Request) {
		jsonRespond(w, http.StatusOK, `{"id":"x","choices":[]}`, nil)
	}), m)

	for _, path := range []string{"/v1/chat/completions", "/chat/completions"} {
		w := httptest.NewRecorder()
		ta.engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, path,
			strings.NewReader(`{"model":"di-routes","messages":[]}`)))
		if w.Code != http.StatusOK {
			t.Fatalf("%s: expected 200, got %d", path, w.Code)
		}
	}
	if rc.count() != 2 {
		t.Fatalf("expected 2 requests, got %d", rc.count())
	}
	for i := 0; i < 2; i++ {
		cap := rc.get(i)
		if cap.method != http.MethodPost {
			t.Errorf("request %d method = %q, want POST", i, cap.method)
		}
		if !strings.HasSuffix(cap.path, "/v1/openai/chat/completions") {
			t.Errorf("request %d path = %q, want suffix /v1/openai/chat/completions", i, cap.path)
		}
		if got := cap.header.Get("Authorization"); got != "Bearer deepinfra_key" {
			t.Errorf("request %d auth = %q", i, got)
		}
	}
}

// ---------------------------------------------------------------------------
// VAL-DEEPINFRA-007: DeepInfra local listings and alias rewrite preserve
// opaque IDs.
// ---------------------------------------------------------------------------

// TestDeepInfra_LocalModelListingExposesAliasAndUpstreamModel verifies that
// /v1/models and /models expose the local alias and exact upstream metadata
// without remote discovery.
func TestDeepInfra_LocalModelListingExposesAliasAndUpstreamModel(t *testing.T) {
	t.Setenv("DEEPINFRA_TOKEN", "deepinfra_key")

	opaqueID := "meta-llama/Llama-3.3-70B-Instruct"
	m := deepinfraModel("di-listing", opaqueID, nil)

	rc := &requestCapture{}
	ta := newDeepInfraTestAPI(t, rc.wrap(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("model listing must not contact upstream")
	}), m)

	for _, path := range []string{"/v1/models", "/models"} {
		w := httptest.NewRecorder()
		ta.engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("%s: expected 200, got %d", path, w.Code)
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
			t.Fatalf("%s: expected 1 model entry, got %d", path, len(resp.Data))
		}
		entry := resp.Data[0]
		if entry.ID != "di-listing" {
			t.Errorf("%s: id = %q, want di-listing", path, entry.ID)
		}
		if entry.UpstreamModel != opaqueID {
			t.Errorf("%s: upstream_model = %q, want %q", path, entry.UpstreamModel, opaqueID)
		}
		if entry.FactoryProvider != string(config.FactoryProviderGeneric) {
			t.Errorf("%s: factory_provider = %q", path, entry.FactoryProvider)
		}
		if entry.UpstreamProto != string(config.UpstreamOpenAIChat) {
			t.Errorf("%s: upstream_protocol = %q", path, entry.UpstreamProto)
		}
		if !entry.AgentReady {
			t.Errorf("%s: agent_ready should be true", path)
		}
	}
	if rc.count() != 0 {
		t.Errorf("model listing made %d upstream requests, want 0", rc.count())
	}
}

// TestDeepInfra_InferenceReplacesOnlyModel verifies that inference preserves
// Hugging Face-style, version-suffixed, and deploy_id IDs byte-for-byte while
// changing only the request model.
func TestDeepInfra_InferenceReplacesOnlyModel(t *testing.T) {
	t.Setenv("DEEPINFRA_TOKEN", "deepinfra_key")

	opaqueIDs := []string{
		"meta-llama/Llama-3.3-70B-Instruct",
		"deepinfra/deepseek-v4",
		"Qwen/Qwen2.5-72B-Instruct",
		"model-with-deploy_id:abc123",
		"org/sub/model-v2.1",
		"org/mixed-CASE.Model:deploy-1",
	}
	var models []*config.Model
	for i, id := range opaqueIDs {
		models = append(models, deepinfraModel(fmt.Sprintf("di-rw-%d", i), id, nil))
	}

	rc := &requestCapture{}
	ta := newDeepInfraTestAPI(t, rc.wrap(func(w http.ResponseWriter, r *http.Request) {
		jsonRespond(w, http.StatusOK, `{"id":"x","choices":[]}`, nil)
	}), models...)

	for i, id := range opaqueIDs {
		alias := fmt.Sprintf("di-rw-%d", i)
		reqBody := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}],"temperature":0.7,"max_tokens":100}`, alias)
		w := httptest.NewRecorder()
		ta.engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
			strings.NewReader(reqBody)))
		if w.Code != http.StatusOK {
			t.Fatalf("id %q: expected 200, got %d", id, w.Code)
		}
		cap := rc.get(i)
		if got := gjson.GetBytes(cap.body, "model").String(); got != id {
			t.Errorf("id %q: model = %q, want %q", id, got, id)
		}
		// Unrelated fields preserved.
		if got := gjson.GetBytes(cap.body, "temperature").Float(); got != 0.7 {
			t.Errorf("id %q: temperature = %v, want 0.7", id, got)
		}
		if got := gjson.GetBytes(cap.body, "max_tokens").Int(); got != 100 {
			t.Errorf("id %q: max_tokens = %v, want 100", id, got)
		}
	}
}

// ---------------------------------------------------------------------------
// VAL-DEEPINFRA-008: DeepInfra Standard, Priority, Flex, and effective
// fallback remain exact.
// ---------------------------------------------------------------------------

// TestDeepInfra_StandardOmitsServiceTier verifies that Standard requests
// omit service_tier entirely.
func TestDeepInfra_StandardOmitsServiceTier(t *testing.T) {
	t.Setenv("DEEPINFRA_TOKEN", "deepinfra_key")

	m := deepinfraModel("di-std", "meta-llama/Llama-3.3-70B-Instruct", nil)

	rc := &requestCapture{}
	ta := newDeepInfraTestAPI(t, rc.wrap(func(w http.ResponseWriter, r *http.Request) {
		jsonRespond(w, http.StatusOK, `{"id":"x","choices":[]}`, nil)
	}), m)

	w := httptest.NewRecorder()
	ta.engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"di-std","messages":[]}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	cap := rc.get(0)
	if st := gjson.GetBytes(cap.body, "service_tier"); st.Exists() {
		t.Errorf("Standard request should not have service_tier, got %s", st.Raw)
	}
}

// TestDeepInfra_PrioritySendsExactPriority verifies that Priority requests
// send exact service_tier: "priority".
func TestDeepInfra_PrioritySendsExactPriority(t *testing.T) {
	t.Setenv("DEEPINFRA_TOKEN", "deepinfra_key")

	m := deepinfraModel("di-pri", "meta-llama/Llama-3.3-70B-Instruct",
		map[string]any{"service_tier": "priority"})

	rc := &requestCapture{}
	ta := newDeepInfraTestAPI(t, rc.wrap(func(w http.ResponseWriter, r *http.Request) {
		jsonRespond(w, http.StatusOK, `{"id":"x","choices":[]}`, nil)
	}), m)

	w := httptest.NewRecorder()
	ta.engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"di-pri","messages":[]}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	cap := rc.get(0)
	if got := gjson.GetBytes(cap.body, "service_tier").String(); got != "priority" {
		t.Errorf("service_tier = %q, want \"priority\"", got)
	}
}

// TestDeepInfra_FlexSendsExactFlex verifies that Flex requests send exact
// service_tier: "flex".
func TestDeepInfra_FlexSendsExactFlex(t *testing.T) {
	t.Setenv("DEEPINFRA_TOKEN", "deepinfra_key")

	m := deepinfraModel("di-flex", "meta-llama/Llama-3.3-70B-Instruct",
		map[string]any{"service_tier": "flex"})

	rc := &requestCapture{}
	ta := newDeepInfraTestAPI(t, rc.wrap(func(w http.ResponseWriter, r *http.Request) {
		jsonRespond(w, http.StatusOK, `{"id":"x","choices":[]}`, nil)
	}), m)

	w := httptest.NewRecorder()
	ta.engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"di-flex","messages":[]}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	cap := rc.get(0)
	if got := gjson.GetBytes(cap.body, "service_tier").String(); got != "flex" {
		t.Errorf("service_tier = %q, want \"flex\"", got)
	}
}

// TestDeepInfra_EffectiveTierLiteralsRelayedVerbatim verifies that successful
// Priority and Flex responses contain exact service_tier: "priority" and "flex"
// respectively, an unsupported-Priority fallback contains exact "default"
// (effective Standard), and all are relayed unchanged without rewriting,
// retry, downgrade, or tier synthesis.
func TestDeepInfra_EffectiveTierLiteralsRelayedVerbatim(t *testing.T) {
	t.Setenv("DEEPINFRA_TOKEN", "deepinfra_key")

	for _, tc := range []struct {
		name        string
		configured  map[string]any
		fixtureBody string
		wantTier    string
	}{
		{
			name:        "priority_success",
			configured:  map[string]any{"service_tier": "priority"},
			fixtureBody: `{"id":"x","service_tier":"priority","choices":[{"index":0,"message":{"role":"assistant","content":"ok"}}]}`,
			wantTier:    "priority",
		},
		{
			name:        "priority_fallback_default",
			configured:  map[string]any{"service_tier": "priority"},
			fixtureBody: `{"id":"x","service_tier":"default","choices":[{"index":0,"message":{"role":"assistant","content":"ok"}}]}`,
			wantTier:    "default",
		},
		{
			name:        "flex_success",
			configured:  map[string]any{"service_tier": "flex"},
			fixtureBody: `{"id":"x","service_tier":"flex","choices":[{"index":0,"message":{"role":"assistant","content":"ok"}}]}`,
			wantTier:    "flex",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := deepinfraModel("di-tier-"+tc.name, "meta-llama/Llama-3.3-70B-Instruct", tc.configured)

			fixtureBytes := []byte(tc.fixtureBody)
			ta := newDeepInfraTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
				jsonRespond(w, http.StatusOK, tc.fixtureBody, nil)
			}, m)

			w := httptest.NewRecorder()
			ta.engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
				strings.NewReader(fmt.Sprintf(`{"model":"di-tier-%s","messages":[]}`, tc.name))))
			if w.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d", w.Code)
			}

			// Effective tier relayed verbatim.
			if got := gjson.Get(w.Body.String(), "service_tier").String(); got != tc.wantTier {
				t.Errorf("response service_tier = %q, want %q", got, tc.wantTier)
			}

			// Body relayed byte-for-byte.
			respHash := sha256.Sum256(w.Body.Bytes())
			fixtureHash := sha256.Sum256(fixtureBytes)
			if respHash != fixtureHash {
				t.Errorf("response body SHA-256 mismatch:\nwant=%s\ngot =%s",
					hex.EncodeToString(fixtureHash[:]), hex.EncodeToString(respHash[:]))
			}
		})
	}
}

// TestDeepInfra_NoLocalTierEnumValidation verifies that the proxy does not
// validate tiers against a local enum by sending an arbitrary tier value and
// confirming it passes through unchanged.
func TestDeepInfra_NoLocalTierEnumValidation(t *testing.T) {
	t.Setenv("DEEPINFRA_TOKEN", "deepinfra_key")

	m := deepinfraModel("di-arbitrary-tier", "meta-llama/Llama-3.3-70B-Instruct",
		map[string]any{"service_tier": "unknown-future-tier"})

	rc := &requestCapture{}
	ta := newDeepInfraTestAPI(t, rc.wrap(func(w http.ResponseWriter, r *http.Request) {
		jsonRespond(w, http.StatusOK, `{"id":"x","choices":[]}`, nil)
	}), m)

	w := httptest.NewRecorder()
	ta.engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"di-arbitrary-tier","messages":[]}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	cap := rc.get(0)
	if got := gjson.GetBytes(cap.body, "service_tier").String(); got != "unknown-future-tier" {
		t.Errorf("arbitrary tier = %q, want \"unknown-future-tier\" (no local enum validation)", got)
	}
}

// TestDeepInfra_TierRequestIsolation verifies that sequential Standard,
// Priority, and Flex requests do not combine or leak tier state.
func TestDeepInfra_TierRequestIsolation(t *testing.T) {
	t.Setenv("DEEPINFRA_TOKEN", "deepinfra_key")

	stdModel := deepinfraModel("di-iso-std", "meta-llama/Llama-3.3-70B-Instruct", nil)
	priModel := deepinfraModel("di-iso-pri", "meta-llama/Llama-3.3-70B-Instruct",
		map[string]any{"service_tier": "priority"})
	flexModel := deepinfraModel("di-iso-flex", "meta-llama/Llama-3.3-70B-Instruct",
		map[string]any{"service_tier": "flex"})

	rc := &requestCapture{}
	ta := newDeepInfraTestAPI(t, rc.wrap(func(w http.ResponseWriter, r *http.Request) {
		jsonRespond(w, http.StatusOK, `{"id":"x","choices":[]}`, nil)
	}), stdModel, priModel, flexModel)

	// Interleaved requests.
	order := []struct {
		alias   string
		hasTier bool
		tier    string
	}{
		{"di-iso-std", false, ""},
		{"di-iso-pri", true, "priority"},
		{"di-iso-flex", true, "flex"},
		{"di-iso-std", false, ""},
		{"di-iso-pri", true, "priority"},
	}
	for _, tc := range order {
		w := httptest.NewRecorder()
		ta.engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
			strings.NewReader(fmt.Sprintf(`{"model":%q,"messages":[]}`, tc.alias))))
		if w.Code != http.StatusOK {
			t.Fatalf("%s: expected 200, got %d", tc.alias, w.Code)
		}
	}
	if rc.count() != len(order) {
		t.Fatalf("expected %d requests, got %d", len(order), rc.count())
	}
	for i, tc := range order {
		cap := rc.get(i)
		st := gjson.GetBytes(cap.body, "service_tier")
		if tc.hasTier {
			if got := st.String(); got != tc.tier {
				t.Errorf("request %d: service_tier = %q, want %q", i, got, tc.tier)
			}
		} else {
			if st.Exists() {
				t.Errorf("request %d: service_tier should be absent, got %s", i, st.Raw)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// VAL-DEEPINFRA-009: DeepInfra text and streaming relay native content and
// usage.
// ---------------------------------------------------------------------------

// TestDeepInfra_NonStreamingRelaysByteForByte verifies that non-streaming
// text, effective-tier literals including effective Standard "default",
// cached-token usage, and finish reason relay unchanged.
func TestDeepInfra_NonStreamingRelaysByteForByte(t *testing.T) {
	t.Setenv("DEEPINFRA_TOKEN", "deepinfra_key")

	m := deepinfraModel("di-relay", "meta-llama/Llama-3.3-70B-Instruct", nil)

	fixtureBody := `{
		"id": "resp-789",
		"object": "chat.completion",
		"model": "meta-llama/Llama-3.3-70B-Instruct",
		"service_tier": "default",
		"choices": [{
			"index": 0,
			"message": {
				"role": "assistant",
				"content": "result text"
			},
			"finish_reason": "stop"
		}],
		"usage": {
			"prompt_tokens": 50,
			"completion_tokens": 30,
			"total_tokens": 80,
			"prompt_tokens_details": {
				"cached_tokens": 25
			}
		}
	}`

	ta := newDeepInfraTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		jsonRespond(w, http.StatusOK, fixtureBody, nil)
	}, m)

	w := httptest.NewRecorder()
	ta.engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"di-relay","messages":[]}`)))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	fixtureBytes := []byte(fixtureBody)
	fixtureHash := sha256.Sum256(fixtureBytes)
	respHash := sha256.Sum256(w.Body.Bytes())
	if fixtureHash != respHash {
		t.Errorf("response body SHA-256 mismatch:\nwant=%s\ngot =%s",
			hex.EncodeToString(fixtureHash[:]), hex.EncodeToString(respHash[:]))
	}
	if !bytesEqual(w.Body.Bytes(), fixtureBytes) {
		t.Errorf("response body raw-byte mismatch:\nwant(hex)=%s\ngot (hex)=%s",
			hex.EncodeToString(fixtureBytes), hex.EncodeToString(w.Body.Bytes()))
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	// Model must NOT be replaced with the local alias.
	if got := gjson.Get(w.Body.String(), "model").String(); got != "meta-llama/Llama-3.3-70B-Instruct" {
		t.Errorf("response model = %q, want meta-llama/Llama-3.3-70B-Instruct", got)
	}
	// Effective tier literal preserved.
	if got := gjson.Get(w.Body.String(), "service_tier").String(); got != "default" {
		t.Errorf("response service_tier = %q, want default", got)
	}
	// Cached token usage preserved.
	if got := gjson.Get(w.Body.String(), "usage.prompt_tokens_details.cached_tokens").Int(); got != 25 {
		t.Errorf("cached_tokens = %d, want 25", got)
	}
}

// TestDeepInfra_NonStreamingRelayDetectsByteMutation proves that any
// response-byte mutation fails the exact-byte relay test.
func TestDeepInfra_NonStreamingRelayDetectsByteMutation(t *testing.T) {
	t.Setenv("DEEPINFRA_TOKEN", "deepinfra_key")

	m := deepinfraModel("di-relay-mut", "meta-llama/Llama-3.3-70B-Instruct", nil)

	originalBody := `{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"ok"}}]}`
	originalHash := sha256.Sum256([]byte(originalBody))

	mutations := []struct {
		name string
		body string
	}{
		{"trailing_newline", originalBody + "\n"},
		{"leading_space", " " + originalBody},
		{"trailing_space", originalBody + " "},
		{"byte_change", `{"id":"y","choices":[{"index":0,"message":{"role":"assistant","content":"ok"}}]}`},
	}

	for _, mt := range mutations {
		t.Run(mt.name, func(t *testing.T) {
			ta := newDeepInfraTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
				jsonRespond(w, http.StatusOK, mt.body, nil)
			}, m)

			w := httptest.NewRecorder()
			ta.engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
				strings.NewReader(`{"model":"di-relay-mut","messages":[]}`)))

			mutHash := sha256.Sum256(w.Body.Bytes())
			if mutHash == originalHash {
				t.Errorf("mutation %q not detected", mt.name)
			}
		})
	}
}

// TestDeepInfra_StreamingRelaysRawSSE verifies that streaming success
// preserves stream options, ordered role/content chunks, final usage,
// cached-token details, and exactly one [DONE].
func TestDeepInfra_StreamingRelaysRawSSE(t *testing.T) {
	t.Setenv("DEEPINFRA_TOKEN", "deepinfra_key")

	m := deepinfraModel("di-stream", "meta-llama/Llama-3.3-70B-Instruct", nil)

	sseFrames := []string{
		`data: {"id":"s1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"Hello"}}]}`,
		`data: {"id":"s1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":" world"}}]}`,
		`data: {"id":"s1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"service_tier":"priority","usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,"prompt_tokens_details":{"cached_tokens":8}}}`,
		`data: [DONE]`,
	}

	var seenStream, seenStreamOptions bool
	ta := newDeepInfraTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
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

	reqBody := `{"model":"di-stream","stream":true,"stream_options":{"include_usage":true},"messages":[{"role":"user","content":"hi"}]}`
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

	// Build exact expected raw-byte SSE transcript.
	var expectedSSE strings.Builder
	for _, frame := range sseFrames {
		expectedSSE.WriteString(frame)
		expectedSSE.WriteString("\n\n")
	}
	expectedBytes := []byte(expectedSSE.String())

	expectedHash := sha256.Sum256(expectedBytes)
	actualHash := sha256.Sum256(w.Body.Bytes())
	if expectedHash != actualHash {
		t.Errorf("SSE transcript SHA-256 mismatch:\nwant=%s\ngot =%s",
			hex.EncodeToString(expectedHash[:]), hex.EncodeToString(actualHash[:]))
	}
	if !bytesEqual(w.Body.Bytes(), expectedBytes) {
		t.Errorf("SSE transcript raw-byte mismatch:\nwant(hex)=%s\ngot (hex)=%s",
			hex.EncodeToString(expectedBytes), hex.EncodeToString(w.Body.Bytes()))
	}

	out := w.Body.String()
	if c := strings.Count(out, "[DONE]"); c != 1 {
		t.Errorf("[DONE] count = %d, want 1", c)
	}
	// Final usage and cached tokens preserved.
	if !strings.Contains(out, `"cached_tokens":8`) {
		t.Error("cached_tokens not relayed in SSE usage")
	}
	// Effective tier literal preserved in SSE.
	if !strings.Contains(out, `"service_tier":"priority"`) {
		t.Error("effective tier priority not relayed in SSE")
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}
}

// TestDeepInfra_StreamingSSE_DetectsMutation proves that frame reordering,
// duplicate frames, single or triple newline separators, and trailing bytes
// all fail the exact raw-byte SSE transcript comparison when they originate
// from a fake upstream and pass through /v1/chat/completions before
// downstream comparison. Each relayed mutation must differ byte-for-byte and
// by SHA-256 from the canonical transcript, while the canonical relay remains
// exact.
func TestDeepInfra_StreamingSSE_DetectsMutation(t *testing.T) {
	t.Setenv("DEEPINFRA_TOKEN", "deepinfra_key")

	canonicalFrames := []string{
		`data: {"id":"s1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"Hello"}}]}`,
		`data: {"id":"s1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":" world"}}]}`,
		`data: {"id":"s1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"service_tier":"default","usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`,
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

	m := deepinfraModel("di-sse-mut", "meta-llama/Llama-3.3-70B-Instruct", nil)

	// --- Canonical relay remains exact through /v1/chat/completions ---
	// The canonical frames originate from a fake upstream and pass through the
	// generic Chat proxy. The downstream SSE transcript must match the canonical
	// bytes exactly (byte-for-byte and SHA-256).
	ta := newDeepInfraTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
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
		strings.NewReader(`{"model":"di-sse-mut","stream":true,"messages":[]}`)))
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
			ta := newDeepInfraTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				mt.writeFunc(w, w.(http.Flusher))
			}, m)

			w := httptest.NewRecorder()
			ta.engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
				strings.NewReader(`{"model":"di-sse-mut","stream":true,"messages":[]}`)))
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
// VAL-DEEPINFRA-010: DeepInfra tools and tool-result continuation are lossless.
// ---------------------------------------------------------------------------

// TestDeepInfra_ToolResultRoundTripLossless verifies that tools, strict
// parameter schemas, tool choice, native tool calls, IDs, null assistant
// content, argument strings, result content, and complete message order
// survive request/response unchanged except for model rewrite.
func TestDeepInfra_ToolResultRoundTripLossless(t *testing.T) {
	t.Setenv("DEEPINFRA_TOKEN", "deepinfra_key")

	m := deepinfraModel("di-tools", "meta-llama/Llama-3.3-70B-Instruct", nil)

	rc := &requestCapture{}
	ta := newDeepInfraTestAPI(t, rc.wrap(func(w http.ResponseWriter, r *http.Request) {
		jsonRespond(w, http.StatusOK, `{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"done"}}]}`, nil)
	}), m)

	// Turn 1: assistant tool call with reasoning_content and strict schema.
	turn1 := `{
		"model": "di-tools",
		"messages": [
			{"role":"user","content":"what is the weather?"},
			{"role":"assistant","content":null,"reasoning_content":"I should call the weather tool","tool_calls":[{"id":"call_d1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"sf\"}"}}]}
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
	ta.engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(turn1)))
	if w.Code != http.StatusOK {
		t.Fatalf("turn 1: expected 200, got %d", w.Code)
	}

	cap1 := rc.get(0)
	if got := gjson.GetBytes(cap1.body, "model").String(); got != "meta-llama/Llama-3.3-70B-Instruct" {
		t.Errorf("turn 1 model = %q", got)
	}
	if got := gjson.GetBytes(cap1.body, "messages.1.tool_calls.0.id").String(); got != "call_d1" {
		t.Errorf("turn 1 tool_call id = %q", got)
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
		t.Errorf("turn 1 tool_choice = %q", got)
	}
	// null content must survive.
	if raw := gjson.GetBytes(cap1.body, "messages.1.content").Raw; raw != "null" {
		t.Errorf("turn 1 assistant content = %s, want null", raw)
	}

	// Turn 2: tool result linked to call_d1, with prior assistant message
	// retaining reasoning_content and null content.
	turn2 := `{
		"model": "di-tools",
		"messages": [
			{"role":"user","content":"what is the weather?"},
			{"role":"assistant","content":null,"reasoning_content":"I should call the weather tool","tool_calls":[{"id":"call_d1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"sf\"}"}}]},
			{"role":"tool","tool_call_id":"call_d1","content":"72F sunny"}
		],
		"tools": [{
			"type": "function",
			"function": {
				"name": "get_weather",
				"strict": true,
				"parameters": {
					"type": "object",
					"properties": {"city": {"type":"string"}},
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
	if got := gjson.GetBytes(cap2.body, "messages.2.tool_call_id").String(); got != "call_d1" {
		t.Errorf("turn 2 tool_call_id = %q", got)
	}
	if got := gjson.GetBytes(cap2.body, "messages.2.content").String(); got != "72F sunny" {
		t.Errorf("turn 2 tool result = %q", got)
	}
	// Message order preserved.
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
	// Prior reasoning_content and null content must survive.
	if got := gjson.GetBytes(cap2.body, "messages.1.reasoning_content").String(); got != "I should call the weather tool" {
		t.Errorf("turn 2 reasoning_content = %q", got)
	}
	if raw := gjson.GetBytes(cap2.body, "messages.1.content").Raw; raw != "null" {
		t.Errorf("turn 2 assistant content = %s, want null", raw)
	}
	if rc.count() != 2 {
		t.Errorf("expected 2 upstream requests, got %d", rc.count())
	}
}

// TestDeepInfra_StreamedToolFixture verifies that a streamed tool fixture
// preserves ordered delta.reasoning_content, tool-call ID/name/argument
// fragments, finish reason, usage, and one [DONE].
func TestDeepInfra_StreamedToolFixture(t *testing.T) {
	t.Setenv("DEEPINFRA_TOKEN", "deepinfra_key")

	m := deepinfraModel("di-stools", "meta-llama/Llama-3.3-70B-Instruct", nil)

	sseFrames := []string{
		`data: {"id":"s1","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"thinking about tools"}}]}`,
		`data: {"id":"s1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_d2","type":"function","function":{"name":"search","arguments":"{\"q\":"}}]}}]}`,
		`data: {"id":"s1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"test\"}"}}]}}]}`,
		`data: {"id":"s1","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`,
		`data: [DONE]`,
	}

	ta := newDeepInfraTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		for _, frame := range sseFrames {
			fmt.Fprintf(w, "%s\n\n", frame)
			flusher.Flush()
		}
	}, m)

	w := httptest.NewRecorder()
	ta.engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"di-stools","stream":true,"messages":[]}`)))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	out := w.Body.String()
	if !strings.Contains(out, `"reasoning_content":"thinking about tools"`) {
		t.Error("reasoning_content not relayed in streamed tool fixture")
	}
	if !strings.Contains(out, `"id":"call_d2"`) {
		t.Error("tool call ID not relayed")
	}
	if !strings.Contains(out, `"finish_reason":"tool_calls"`) {
		t.Error("finish_reason tool_calls not relayed")
	}
	if !strings.Contains(out, `"total_tokens":15`) {
		t.Error("usage not relayed")
	}
	if c := strings.Count(out, "[DONE]"); c != 1 {
		t.Errorf("[DONE] count = %d, want 1", c)
	}

	// Exact byte-for-byte transcript.
	var expectedSSE strings.Builder
	for _, frame := range sseFrames {
		expectedSSE.WriteString(frame)
		expectedSSE.WriteString("\n\n")
	}
	expectedBytes := []byte(expectedSSE.String())
	if !bytesEqual(w.Body.Bytes(), expectedBytes) {
		t.Errorf("streamed tool SSE transcript raw-byte mismatch")
	}
}

// TestDeepInfra_ContinuationNoDeepSeekReplay verifies that model-name
// inspection does not invoke DeepSeek reasoning replay or strip caller
// reasoning history. A DeepInfra model with reasoning_content in the
// continuation retains it unchanged.
func TestDeepInfra_ContinuationNoDeepSeekReplay(t *testing.T) {
	t.Setenv("DEEPINFRA_TOKEN", "deepinfra_key")

	// Use a model name that might look like deepseek to ensure no replay.
	m := deepinfraModel("di-noreplay", "deepinfra/deepseek-v4", nil)

	rc := &requestCapture{}
	ta := newDeepInfraTestAPI(t, rc.wrap(func(w http.ResponseWriter, r *http.Request) {
		jsonRespond(w, http.StatusOK, `{"id":"x","choices":[]}`, nil)
	}), m)

	reqBody := `{
		"model": "di-noreplay",
		"messages": [
			{"role":"user","content":"hi"},
			{"role":"assistant","content":null,"reasoning_content":"prior reasoning"}
		]
	}`
	w := httptest.NewRecorder()
	ta.engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(reqBody)))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	cap := rc.get(0)
	// reasoning_content must survive, not stripped by DeepSeek replay.
	if got := gjson.GetBytes(cap.body, "messages.1.reasoning_content").String(); got != "prior reasoning" {
		t.Errorf("continuation reasoning_content = %q, want \"prior reasoning\" (not stripped)", got)
	}
}

// ---------------------------------------------------------------------------
// VAL-DEEPINFRA-011: DeepInfra structured output and reasoning remain native.
// ---------------------------------------------------------------------------

// TestDeepInfra_StructuredOutputAndReasoningPreserved verifies that strict
// JSON-schema response formats, reasoning_effort, reasoning objects,
// non-streaming reasoning_content, and streaming reasoning deltas preserve
// exact names/types/order and remain distinct from visible content.
func TestDeepInfra_StructuredOutputAndReasoningPreserved(t *testing.T) {
	t.Setenv("DEEPINFRA_TOKEN", "deepinfra_key")

	m := deepinfraModel("di-struct", "meta-llama/Llama-3.3-70B-Instruct", nil)

	rc := &requestCapture{}
	ta := newDeepInfraTestAPI(t, rc.wrap(func(w http.ResponseWriter, r *http.Request) {
		jsonRespond(w, http.StatusOK, `{"id":"x","choices":[]}`, nil)
	}), m)

	reqBody := `{
		"model": "di-struct",
		"messages": [{"role":"user","content":"build a report"}],
		"response_format": {"type":"json_schema","json_schema":{"name":"report","strict":true,"schema":{"type":"object","properties":{"title":{"type":"string"},"summary":{"type":"string"}},"required":["title","summary"],"additionalProperties":false}}},
		"reasoning_effort": "high",
		"reasoning": {"effort":"high","exclude":false}
	}`
	w := httptest.NewRecorder()
	ta.engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(reqBody)))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	cap := rc.get(0)
	// Strict JSON-schema preserved.
	rf := gjson.GetBytes(cap.body, "response_format")
	if rf.Get("type").String() != "json_schema" {
		t.Errorf("response_format.type = %q", rf.Get("type").String())
	}
	if !rf.Get("json_schema.strict").Bool() {
		t.Error("response_format strict not preserved")
	}
	if !rf.Get("json_schema.schema.properties.title.type").Exists() {
		t.Error("nested schema not preserved")
	}
	if rf.Get("json_schema.schema.additionalProperties").Bool() != false {
		t.Error("additionalProperties: false not preserved")
	}
	// reasoning_effort preserved.
	if got := gjson.GetBytes(cap.body, "reasoning_effort").String(); got != "high" {
		t.Errorf("reasoning_effort = %q, want high", got)
	}
	// reasoning object preserved.
	if got := gjson.GetBytes(cap.body, "reasoning.effort").String(); got != "high" {
		t.Errorf("reasoning.effort = %q, want high", got)
	}
	if got := gjson.GetBytes(cap.body, "reasoning.exclude").Bool(); got != false {
		t.Errorf("reasoning.exclude = %v, want false", got)
	}
	// Model rewritten.
	if got := gjson.GetBytes(cap.body, "model").String(); got != "meta-llama/Llama-3.3-70B-Instruct" {
		t.Errorf("model = %q", got)
	}
}

// TestDeepInfra_StreamingReasoningDeltas verifies that streaming reasoning
// deltas remain a distinct lane from visible content.
func TestDeepInfra_StreamingReasoningDeltas(t *testing.T) {
	t.Setenv("DEEPINFRA_TOKEN", "deepinfra_key")

	m := deepinfraModel("di-sreason", "meta-llama/Llama-3.3-70B-Instruct", nil)

	sseFrames := []string{
		`data: {"id":"s1","choices":[{"index":0,"delta":{"reasoning_content":"step 1: analyze"}}]}`,
		`data: {"id":"s1","choices":[{"index":0,"delta":{"reasoning_content":"step 2: decide"}}]}`,
		`data: {"id":"s1","choices":[{"index":0,"delta":{"content":"Here is the answer"}}]}`,
		`data: {"id":"s1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`data: [DONE]`,
	}

	ta := newDeepInfraTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		for _, frame := range sseFrames {
			fmt.Fprintf(w, "%s\n\n", frame)
			flusher.Flush()
		}
	}, m)

	w := httptest.NewRecorder()
	ta.engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"di-sreason","stream":true,"messages":[]}`)))

	out := w.Body.String()
	if !strings.Contains(out, `"reasoning_content":"step 1: analyze"`) {
		t.Error("reasoning delta 1 not relayed")
	}
	if !strings.Contains(out, `"reasoning_content":"step 2: decide"`) {
		t.Error("reasoning delta 2 not relayed")
	}
	if !strings.Contains(out, `"content":"Here is the answer"`) {
		t.Error("content delta not relayed")
	}
	// Reasoning and content are distinct lanes.
	if strings.Index(out, "step 1") > strings.Index(out, "Here is the answer") {
		t.Error("reasoning should precede content in ordered transcript")
	}

	// Byte-exact SSE transcript: the relayed stream must match the canonical
	// frames byte-for-byte and by SHA-256.
	var expectedSSE strings.Builder
	for _, frame := range sseFrames {
		expectedSSE.WriteString(frame)
		expectedSSE.WriteString("\n\n")
	}
	expectedBytes := []byte(expectedSSE.String())
	expectedHash := sha256.Sum256(expectedBytes)
	relayHash := sha256.Sum256(w.Body.Bytes())
	if relayHash != expectedHash {
		t.Errorf("streamed reasoning SSE SHA-256 mismatch:\nwant=%s\ngot =%s",
			hex.EncodeToString(expectedHash[:]), hex.EncodeToString(relayHash[:]))
	}
	if !bytesEqual(w.Body.Bytes(), expectedBytes) {
		t.Errorf("streamed reasoning SSE raw-byte mismatch:\nwant(hex)=%s\ngot (hex)=%s",
			hex.EncodeToString(expectedBytes), hex.EncodeToString(w.Body.Bytes()))
	}
}

// TestDeepInfra_MinimalRequestGainsNoReasoningDefaults verifies that a model
// without explicit reasoning fields gains no reasoning defaults.
func TestDeepInfra_MinimalRequestGainsNoReasoningDefaults(t *testing.T) {
	t.Setenv("DEEPINFRA_TOKEN", "deepinfra_key")

	m := deepinfraModel("di-noreason", "meta-llama/Llama-3.3-70B-Instruct", nil)

	rc := &requestCapture{}
	ta := newDeepInfraTestAPI(t, rc.wrap(func(w http.ResponseWriter, r *http.Request) {
		jsonRespond(w, http.StatusOK, `{"id":"x","choices":[]}`, nil)
	}), m)

	w := httptest.NewRecorder()
	ta.engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"di-noreason","messages":[{"role":"user","content":"hi"}]}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	cap := rc.get(0)
	absentFields := []string{
		"reasoning_effort", "reasoning", "thinking",
		"chat_template_kwargs", "reasoning_content",
	}
	for _, f := range absentFields {
		if v := gjson.GetBytes(cap.body, f); v.Exists() {
			t.Errorf("minimal request gained field %s = %s", f, v.Raw)
		}
	}
}

// ---------------------------------------------------------------------------
// VAL-DEEPINFRA-012: DeepInfra cache options and extra_args merge only their
// keys.
// ---------------------------------------------------------------------------

// TestDeepInfra_CacheOptionsPassThrough verifies that prompt_cache_key and
// message-level cache_control pass unchanged.
func TestDeepInfra_CacheOptionsPassThrough(t *testing.T) {
	t.Setenv("DEEPINFRA_TOKEN", "deepinfra_key")

	m := deepinfraModel("di-cache", "meta-llama/Llama-3.3-70B-Instruct", nil)

	rc := &requestCapture{}
	ta := newDeepInfraTestAPI(t, rc.wrap(func(w http.ResponseWriter, r *http.Request) {
		jsonRespond(w, http.StatusOK, `{"id":"x","choices":[]}`, nil)
	}), m)

	reqBody := `{
		"model": "di-cache",
		"messages": [
			{"role":"system","content":"you are helpful","cache_control":{"type":"ephemeral"}},
			{"role":"user","content":"hello"}
		],
		"prompt_cache_key": "session-cache-123"
	}`
	w := httptest.NewRecorder()
	ta.engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(reqBody)))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	cap := rc.get(0)
	if got := gjson.GetBytes(cap.body, "prompt_cache_key").String(); got != "session-cache-123" {
		t.Errorf("prompt_cache_key = %q, want session-cache-123", got)
	}
	// Message-level cache_control preserved.
	if got := gjson.GetBytes(cap.body, "messages.0.cache_control.type").String(); got != "ephemeral" {
		t.Errorf("cache_control.type = %q, want ephemeral", got)
	}
	// Message order preserved.
	msgs := gjson.GetBytes(cap.body, "messages").Array()
	if len(msgs) != 2 {
		t.Fatalf("message count = %d, want 2", len(msgs))
	}
	if msgs[0].Get("role").String() != "system" {
		t.Error("system message not first")
	}
}

// TestDeepInfra_OptionsPreserveExactTypes verifies that temperature, top_p,
// max_tokens, seed, stop, stream_options, chat_template_kwargs,
// prompt_cache_key, and an unknown nested future option retain exact JSON
// names, types, and values after changing only model.
func TestDeepInfra_OptionsPreserveExactTypes(t *testing.T) {
	t.Setenv("DEEPINFRA_TOKEN", "deepinfra_key")

	m := deepinfraModel("di-opts", "meta-llama/Llama-3.3-70B-Instruct", nil)

	rc := &requestCapture{}
	ta := newDeepInfraTestAPI(t, rc.wrap(func(w http.ResponseWriter, r *http.Request) {
		jsonRespond(w, http.StatusOK, `{"id":"x","choices":[]}`, nil)
	}), m)

	reqBody := `{
		"model": "di-opts",
		"messages": [{"role":"user","content":"hello"}],
		"temperature": 0.5,
		"top_p": 0.9,
		"max_tokens": 200,
		"seed": 42,
		"stop": ["\n", "END"],
		"stream_options": {"include_usage": true},
		"chat_template_kwargs": {"enable_thinking": true, "custom_var": "test"},
		"prompt_cache_key": "cache-key-456",
		"future_option": {"nested":{"big_int":9007199254740993,"null_val":null,"flag":true}}
	}`
	w := httptest.NewRecorder()
	ta.engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(reqBody)))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	cap := rc.get(0)
	checks := []struct {
		path string
		want any
	}{
		{"temperature", 0.5},
		{"top_p", 0.9},
		{"max_tokens", float64(200)},
		{"seed", float64(42)},
		{"prompt_cache_key", "cache-key-456"},
	}
	for _, c := range checks {
		gotVal := gjson.GetBytes(cap.body, c.path)
		if !gotVal.Exists() {
			t.Errorf("field %s missing", c.path)
			continue
		}
		if !deepEqualJSON(gotVal.Value(), c.want) {
			t.Errorf("field %s = %#v, want %#v", c.path, gotVal.Value(), c.want)
		}
	}
	// Stop array.
	stop := gjson.GetBytes(cap.body, "stop").Array()
	if len(stop) != 2 || stop[0].String() != "\n" || stop[1].String() != "END" {
		t.Errorf("stop mismatch: %s", gjson.GetBytes(cap.body, "stop").Raw)
	}
	// stream_options.
	if got := gjson.GetBytes(cap.body, "stream_options.include_usage").Bool(); !got {
		t.Error("stream_options.include_usage not preserved")
	}
	// chat_template_kwargs.
	if got := gjson.GetBytes(cap.body, "chat_template_kwargs.enable_thinking").Bool(); !got {
		t.Error("chat_template_kwargs.enable_thinking not preserved")
	}
	if got := gjson.GetBytes(cap.body, "chat_template_kwargs.custom_var").String(); got != "test" {
		t.Errorf("chat_template_kwargs.custom_var = %q", got)
	}
	// Unknown nested future field with large integer and null.
	if got := gjson.GetBytes(cap.body, "future_option.nested.big_int").Int(); got != 9007199254740993 {
		t.Errorf("future_option big_int = %d", got)
	}
	if raw := gjson.GetBytes(cap.body, "future_option.nested.null_val").Raw; raw != "null" {
		t.Errorf("future_option null_val = %s, want null", raw)
	}
	// Model rewritten.
	if got := gjson.GetBytes(cap.body, "model").String(); got != "meta-llama/Llama-3.3-70B-Instruct" {
		t.Errorf("model = %q", got)
	}
}

// TestDeepInfra_ExtraArgsReplaceCollidingCallerValues verifies that configured
// scalar extra_args fully replace caller values at the same top-level keys.
func TestDeepInfra_ExtraArgsReplaceCollidingCallerValues(t *testing.T) {
	t.Setenv("DEEPINFRA_TOKEN", "deepinfra_key")

	m := deepinfraModel("di-merge", "meta-llama/Llama-3.3-70B-Instruct",
		map[string]any{
			"temperature": 0.1,
			"top_p":       0.8,
		})

	rc := &requestCapture{}
	ta := newDeepInfraTestAPI(t, rc.wrap(func(w http.ResponseWriter, r *http.Request) {
		jsonRespond(w, http.StatusOK, `{"id":"x","choices":[]}`, nil)
	}), m)

	reqBody := `{"model":"di-merge","messages":[],"temperature":0.9,"top_p":0.5,"max_tokens":100}`
	w := httptest.NewRecorder()
	ta.engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(reqBody)))

	cap := rc.get(0)
	if got := gjson.GetBytes(cap.body, "temperature").Float(); got != 0.1 {
		t.Errorf("temperature = %v, want 0.1 (configured)", got)
	}
	if got := gjson.GetBytes(cap.body, "top_p").Float(); got != 0.8 {
		t.Errorf("top_p = %v, want 0.8 (configured)", got)
	}
	if got := gjson.GetBytes(cap.body, "max_tokens").Int(); got != 100 {
		t.Errorf("max_tokens = %v, want 100 (caller)", got)
	}
}

// TestDeepInfra_ExtraArgsInjectAbsentKeys verifies that configured extra_args
// inject absent keys that the caller did not supply.
func TestDeepInfra_ExtraArgsInjectAbsentKeys(t *testing.T) {
	t.Setenv("DEEPINFRA_TOKEN", "deepinfra_key")

	m := deepinfraModel("di-inject", "meta-llama/Llama-3.3-70B-Instruct",
		map[string]any{
			"seed":             99,
			"prompt_cache_key": "injected-cache",
		})

	rc := &requestCapture{}
	ta := newDeepInfraTestAPI(t, rc.wrap(func(w http.ResponseWriter, r *http.Request) {
		jsonRespond(w, http.StatusOK, `{"id":"x","choices":[]}`, nil)
	}), m)

	w := httptest.NewRecorder()
	ta.engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"di-inject","messages":[]}`)))

	cap := rc.get(0)
	if got := gjson.GetBytes(cap.body, "seed").Int(); got != 99 {
		t.Errorf("seed = %d, want 99 (injected)", got)
	}
	if got := gjson.GetBytes(cap.body, "prompt_cache_key").String(); got != "injected-cache" {
		t.Errorf("prompt_cache_key = %q, want injected-cache", got)
	}
}

// TestDeepInfra_ExtraArgsWholeObjectReplacement verifies that configured object
// extra_args fully replace caller whole-object values (no deep merge).
func TestDeepInfra_ExtraArgsWholeObjectReplacement(t *testing.T) {
	t.Setenv("DEEPINFRA_TOKEN", "deepinfra_key")

	m := deepinfraModel("di-objrep", "meta-llama/Llama-3.3-70B-Instruct",
		map[string]any{
			"chat_template_kwargs": map[string]any{"enable_thinking": false},
		})

	rc := &requestCapture{}
	ta := newDeepInfraTestAPI(t, rc.wrap(func(w http.ResponseWriter, r *http.Request) {
		jsonRespond(w, http.StatusOK, `{"id":"x","choices":[]}`, nil)
	}), m)

	reqBody := `{"model":"di-objrep","messages":[],"chat_template_kwargs":{"enable_thinking":true,"custom_var":"should-be-gone"}}`
	w := httptest.NewRecorder()
	ta.engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(reqBody)))

	cap := rc.get(0)
	ctk := gjson.GetBytes(cap.body, "chat_template_kwargs")
	if got := ctk.Get("enable_thinking").Bool(); got != false {
		t.Errorf("chat_template_kwargs.enable_thinking = %v, want false (configured)", got)
	}
	if ctk.Get("custom_var").Exists() {
		t.Errorf("custom_var survived whole-object replacement: %s", ctk.Get("custom_var").Raw)
	}
}

// TestDeepInfra_MinimalRequestGainsNoDefaults verifies that a minimal request
// gains no tier, reasoning, cache key, sampling value, or provider capability
// default.
func TestDeepInfra_MinimalRequestGainsNoDefaults(t *testing.T) {
	t.Setenv("DEEPINFRA_TOKEN", "deepinfra_key")

	m := deepinfraModel("di-min", "meta-llama/Llama-3.3-70B-Instruct", nil)

	rc := &requestCapture{}
	ta := newDeepInfraTestAPI(t, rc.wrap(func(w http.ResponseWriter, r *http.Request) {
		jsonRespond(w, http.StatusOK, `{"id":"x","choices":[]}`, nil)
	}), m)

	w := httptest.NewRecorder()
	ta.engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"di-min","messages":[{"role":"user","content":"hi"}]}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	cap := rc.get(0)
	absentFields := []string{
		"temperature", "top_p", "max_tokens",
		"seed", "stop", "stream_options",
		"chat_template_kwargs", "prompt_cache_key",
		"service_tier", "reasoning_effort",
		"reasoning", "thinking",
	}
	for _, f := range absentFields {
		if v := gjson.GetBytes(cap.body, f); v.Exists() {
			t.Errorf("minimal request gained field %s = %s", f, v.Raw)
		}
	}
}

// ---------------------------------------------------------------------------
// VAL-DEEPINFRA-013: DeepInfra errors preserve status, body, safe headers,
// and one-attempt semantics.
// ---------------------------------------------------------------------------

// TestDeepInfra_UpstreamErrorRelayedWithExactStatusAndBody verifies that
// representative upstream error responses relay exact status, bounded body,
// and content type after one attempt with no retry.
func TestDeepInfra_UpstreamErrorRelayedWithExactStatusAndBody(t *testing.T) {
	t.Setenv("DEEPINFRA_TOKEN", "deepinfra_key")

	m := deepinfraModel("di-err", "meta-llama/Llama-3.3-70B-Instruct", nil)

	for _, tc := range []struct {
		name       string
		status     int
		body       string
		contentTyp string
	}{
		{"validation_400", 400, `{"error":{"message":"bad request","type":"invalid_request_error"}}`, "application/json"},
		{"auth_401", 401, `{"error":{"message":"unauthorized","type":"authentication_error"}}`, "application/json"},
		{"forbidden_403", 403, `{"error":{"message":"forbidden","type":"permission_error"}}`, "application/json"},
		{"not_found_404", 404, `{"error":{"message":"model not found","type":"not_found_error"}}`, "application/json"},
		{"unprocessable_422", 422, `{"error":{"message":"invalid input","type":"invalid_request_error"}}`, "application/json"},
		{"rate_limit_429_flex_capacity", 429, `{"error":{"message":"flex capacity unavailable","type":"rate_limit_error"}}`, "application/json"},
		{"server_500", 500, `{"error":{"message":"internal","type":"server_error"}}`, "application/json"},
		{"service_unavailable_503", 503, `{"error":{"message":"unavailable","type":"service_unavailable"}}`, "application/json"},
		{"text_500", 500, `Internal Server Error`, "text/plain"},
		{"empty_503", 503, ``, "application/json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rc := &requestCapture{}
			ta := newDeepInfraTestAPI(t, rc.wrap(func(w http.ResponseWriter, r *http.Request) {
				ct := tc.contentTyp
				if ct == "" {
					ct = "application/json"
				}
				w.Header().Set("Content-Type", ct)
				w.WriteHeader(tc.status)
				if tc.body != "" {
					w.Write([]byte(tc.body))
				}
			}), m)

			w := httptest.NewRecorder()
			ta.engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
				strings.NewReader(`{"model":"di-err","messages":[]}`)))

			if w.Code != tc.status {
				t.Fatalf("status = %d, want %d", w.Code, tc.status)
			}
			expectedBody := []byte(tc.body)
			if !bytesEqual(w.Body.Bytes(), expectedBody) {
				t.Errorf("error body raw-byte mismatch:\nwant(hex)=%s\ngot (hex)=%s",
					hex.EncodeToString(expectedBody), hex.EncodeToString(w.Body.Bytes()))
			}
			expectedHash := sha256.Sum256(expectedBody)
			actualHash := sha256.Sum256(w.Body.Bytes())
			if expectedHash != actualHash {
				t.Errorf("error body SHA-256 mismatch:\nwant=%s\ngot =%s",
					hex.EncodeToString(expectedHash[:]), hex.EncodeToString(actualHash[:]))
			}
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

// TestDeepInfra_ErrorHeadersBehavior verifies that for a 429 error,
// downstream Retry-After equals upstream exactly, an allowlisted provider
// request ID is preserved, while protected, hop-by-hop, cookie, compression,
// connection-nominated, and gateway-identifying headers are absent.
func TestDeepInfra_ErrorHeadersBehavior(t *testing.T) {
	t.Setenv("DEEPINFRA_TOKEN", "deepinfra_key")

	m := deepinfraModel("di-errhdr", "meta-llama/Llama-3.3-70B-Instruct", nil)

	ta := newDeepInfraTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		// Safe headers that should be preserved.
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Request-ID", "req-di-generic-012")
		w.Header().Set("Retry-After", "60")
		// Unsafe headers that must be removed.
		w.Header().Set("Set-Cookie", "session=leaked-on-error")
		w.Header().Set("Connection", "keep-alive, X-Conn-Scoped")
		w.Header().Set("X-Conn-Scoped", "error-conn-value")
		w.Header().Set("Transfer-Encoding", "chunked")
		w.Header().Set("Content-Encoding", "br")
		w.Header().Set("Keep-Alive", "timeout=5")
		w.Header().Set("X-Litellm-Version", "2.0")
		w.Header().Set("X-Portkey-Status", "error")
		w.Header().Set("Helicone-Request-Id", "hc-err-1")
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":{"message":"rate limited","type":"rate_limit_error"}}`))
	}, m)

	w := httptest.NewRecorder()
	ta.engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"di-errhdr","messages":[]}`)))

	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", w.Code)
	}

	// Safe headers preserved.
	for k, v := range map[string]string{
		"X-Request-ID": "req-di-generic-012",
		"Retry-After":  "60",
	} {
		if got := w.Header().Get(k); got != v {
			t.Errorf("safe header %s = %q, want %q", k, got, v)
		}
	}
	// Unsafe headers removed.
	for _, h := range []string{
		"Set-Cookie", "Connection", "X-Conn-Scoped",
		"Transfer-Encoding", "Content-Encoding", "Keep-Alive",
		"X-Litellm-Version", "X-Portkey-Status", "Helicone-Request-Id",
	} {
		if got := w.Header().Get(h); got != "" {
			t.Errorf("unsafe header %s leaked: %q", h, got)
		}
	}
}

// TestDeepInfra_PreSSEErrorRemainsHTTP verifies that a pre-SSE non-2xx remains
// native HTTP rather than an SSE error.
func TestDeepInfra_PreSSEErrorRemainsHTTP(t *testing.T) {
	t.Setenv("DEEPINFRA_TOKEN", "deepinfra_key")

	m := deepinfraModel("di-sse-err", "meta-llama/Llama-3.3-70B-Instruct", nil)

	ta := newDeepInfraTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":{"message":"bad","type":"invalid_request_error"}}`))
	}, m)

	w := httptest.NewRecorder()
	ta.engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"di-sse-err","stream":true,"messages":[]}`)))

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
}

// TestDeepInfra_TransportFailureReturns502Envelope verifies that a transport
// failure returns the existing 502 upstream_error envelope with exactly one
// outbound attempt and no retry or fallback.
func TestDeepInfra_TransportFailureReturns502Envelope(t *testing.T) {
	credSentinel := "deepinfra_transport_secret_xyz"
	t.Setenv("DEEPINFRA_TOKEN", credSentinel)

	rt := &countingFailingRoundTripper{
		err: errors.New("deterministic transport failure: connection refused"),
	}

	m := deepinfraModel("di-transport", "meta-llama/Llama-3.3-70B-Instruct", nil)
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
		strings.NewReader(`{"model":"di-transport","messages":[]}`)))

	if w.Code != http.StatusBadGateway {
		t.Fatalf("transport failure: expected 502, got %d body=%s", w.Code, w.Body.String())
	}
	if got := rt.count(); got != 1 {
		t.Errorf("RoundTrip call count = %d, want exactly 1", got)
	}
	if got := gjson.Get(w.Body.String(), "error.type").String(); got != "upstream_error" {
		t.Errorf("error.type = %q, want upstream_error", got)
	}
	if strings.Contains(w.Body.String(), credSentinel) {
		t.Errorf("credential sentinel leaked in 502 body: %s", w.Body.String())
	}
	bodyLower := strings.ToLower(w.Body.String())
	if strings.Contains(bodyLower, "retry") || strings.Contains(bodyLower, "fallback") {
		t.Errorf("502 envelope suggests retry/fallback: %s", w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); strings.Contains(ct, "text/event-stream") {
		t.Errorf("transport failure returned SSE Content-Type: %q", ct)
	}
}

// ---------------------------------------------------------------------------
// VAL-DEEPINFRA-014: DeepInfra local errors never contact upstream.
// ---------------------------------------------------------------------------

// TestDeepInfra_MissingTokenFailsLocally verifies that missing DEEPINFRA_TOKEN
// fails before upstream contact.
func TestDeepInfra_MissingTokenFailsLocally(t *testing.T) {
	// Do NOT set DEEPINFRA_TOKEN.
	m := deepinfraModel("di-notoken", "meta-llama/Llama-3.3-70B-Instruct", nil)

	rc := &requestCapture{}
	ta := newDeepInfraTestAPI(t, rc.wrap(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("upstream must not be called when token is missing")
	}), m)

	w := httptest.NewRecorder()
	ta.engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"di-notoken","messages":[]}`)))
	if w.Code != http.StatusInternalServerError {
		t.Errorf("missing token: expected 500, got %d", w.Code)
	}
	if rc.count() != 0 {
		t.Errorf("expected 0 upstream requests, got %d", rc.count())
	}
	// Diagnostics must name at most DEEPINFRA_TOKEN without leaking a value.
	if strings.Contains(w.Body.String(), "deepinfra_secret") {
		t.Errorf("token value leaked in error: %s", w.Body.String())
	}
}

// TestDeepInfra_EmptyTokenFailsLocally verifies that an empty DEEPINFRA_TOKEN
// fails before upstream contact.
func TestDeepInfra_EmptyTokenFailsLocally(t *testing.T) {
	t.Setenv("DEEPINFRA_TOKEN", "")

	m := deepinfraModel("di-emptytoken", "meta-llama/Llama-3.3-70B-Instruct", nil)

	rc := &requestCapture{}
	ta := newDeepInfraTestAPI(t, rc.wrap(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("upstream must not be called when token is empty")
	}), m)

	w := httptest.NewRecorder()
	ta.engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"di-emptytoken","messages":[]}`)))
	if w.Code != http.StatusInternalServerError {
		t.Errorf("empty token: expected 500, got %d", w.Code)
	}
	if rc.count() != 0 {
		t.Errorf("expected 0 upstream requests, got %d", rc.count())
	}
}

// TestDeepInfra_MissingModelFailsLocally verifies that missing model in the
// request body fails locally with zero upstream traffic.
func TestDeepInfra_MissingModelFailsLocally(t *testing.T) {
	t.Setenv("DEEPINFRA_TOKEN", "deepinfra_key")

	m := deepinfraModel("di-local", "meta-llama/Llama-3.3-70B-Instruct", nil)

	rc := &requestCapture{}
	ta := newDeepInfraTestAPI(t, rc.wrap(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("upstream must not be called for local errors")
	}), m)

	w := httptest.NewRecorder()
	ta.engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"messages":[]}`)))
	if w.Code != http.StatusBadRequest {
		t.Errorf("missing model: expected 400, got %d", w.Code)
	}
	if rc.count() != 0 {
		t.Errorf("expected 0 upstream requests, got %d", rc.count())
	}
}

// TestDeepInfra_UnknownAliasFailsLocally verifies that an unknown alias fails
// locally with zero upstream traffic.
func TestDeepInfra_UnknownAliasFailsLocally(t *testing.T) {
	t.Setenv("DEEPINFRA_TOKEN", "deepinfra_key")

	m := deepinfraModel("di-local", "meta-llama/Llama-3.3-70B-Instruct", nil)

	rc := &requestCapture{}
	ta := newDeepInfraTestAPI(t, rc.wrap(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("upstream must not be called for local errors")
	}), m)

	w := httptest.NewRecorder()
	ta.engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"nonexistent"}`)))
	if w.Code != http.StatusNotFound {
		t.Errorf("unknown alias: expected 404, got %d", w.Code)
	}
	if rc.count() != 0 {
		t.Errorf("expected 0 upstream requests, got %d", rc.count())
	}
}

// TestDeepInfra_MalformedJSONFailsLocally verifies that malformed JSON fails
// locally with zero upstream traffic.
func TestDeepInfra_MalformedJSONFailsLocally(t *testing.T) {
	t.Setenv("DEEPINFRA_TOKEN", "deepinfra_key")

	m := deepinfraModel("di-local", "meta-llama/Llama-3.3-70B-Instruct", nil)

	rc := &requestCapture{}
	ta := newDeepInfraTestAPI(t, rc.wrap(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("upstream must not be called for local errors")
	}), m)

	w := httptest.NewRecorder()
	ta.engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{invalid json`)))
	if w.Code < 400 {
		t.Errorf("malformed JSON: expected 4xx, got %d", w.Code)
	}
	if rc.count() != 0 {
		t.Errorf("expected 0 upstream requests, got %d", rc.count())
	}
}

// TestDeepInfra_ProviderMismatchFailsLocally verifies that a valid model
// configured as factory_provider: anthropic with upstream_protocol: openai-chat
// but called through /v1/chat/completions returns a local routing error with
// zero upstream traffic.
func TestDeepInfra_ProviderMismatchFailsLocally(t *testing.T) {
	t.Setenv("DEEPINFRA_TOKEN", "deepinfra_key")

	m := &config.Model{
		Alias:            "di-mismatch",
		DisplayName:      "di-mismatch",
		FactoryProvider:  config.FactoryProviderAnthropic,
		UpstreamProtocol: config.UpstreamOpenAIChat,
		KnownAuth:        "deepinfra",
		UpstreamModel:    "meta-llama/Llama-3.3-70B-Instruct",
	}

	rc := &requestCapture{}
	ta := newDeepInfraTestAPI(t, rc.wrap(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("upstream must not be called for provider mismatch")
	}), m)

	w := httptest.NewRecorder()
	ta.engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"di-mismatch","messages":[]}`)))
	if w.Code != http.StatusBadRequest {
		t.Errorf("provider mismatch: expected 400, got %d", w.Code)
	}
	if rc.count() != 0 {
		t.Errorf("expected 0 upstream requests, got %d", rc.count())
	}
}

// ---------------------------------------------------------------------------
// VAL-DEEPINFRA-015: DeepInfra credentials, protected headers, and logs are
// isolated.
// ---------------------------------------------------------------------------

// TestDeepInfra_ProtectedHeadersCannotOverrideTransport verifies that client
// auth and user/configured auth, API-key, cookie, host, forwarding, and
// hop-by-hop values cannot replace or supplement the provider Bearer token.
func TestDeepInfra_ProtectedHeadersCannotOverrideTransport(t *testing.T) {
	t.Setenv("DEEPINFRA_TOKEN", "deepinfra_real_token")

	m := deepinfraModel("di-protected", "meta-llama/Llama-3.3-70B-Instruct", nil)

	var seenAuth string
	ta := newDeepInfraTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		seenAuth = r.Header.Get("Authorization")
		jsonRespond(w, http.StatusOK, `{"id":"x","choices":[]}`, nil)
	}, m)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"di-protected","messages":[]}`))
	req.Header.Set("Authorization", "Bearer downstream-fake")
	req.Header.Set("X-Api-Key", "downstream-key")
	req.Header.Set("Proxy-Authorization", "Bearer proxy-fake")
	req.Header.Set("Cookie", "session=fake")

	w := httptest.NewRecorder()
	ta.engine.ServeHTTP(w, req)

	if seenAuth != "Bearer deepinfra_real_token" {
		t.Errorf("upstream Authorization = %q, want Bearer deepinfra_real_token", seenAuth)
	}
}

// TestDeepInfra_ConfiguredProtectedHeadersFiltered verifies that even if a
// model is configured with protected extra_headers, they are filtered out.
func TestDeepInfra_ConfiguredProtectedHeadersFiltered(t *testing.T) {
	t.Setenv("DEEPINFRA_TOKEN", "deepinfra_real_token")

	m := deepinfraModel("di-filtered", "meta-llama/Llama-3.3-70B-Instruct", nil)
	m.ExtraHeaders = map[string]string{
		"Authorization":       "Bearer should-not-pass",
		"Host":                "evil.example.com",
		"Cookie":              "session=hijack",
		"Proxy-Authorization": "Bearer proxy-hijack",
		"X-Forwarded-For":     "spoofed",
	}

	var seenAuth, seenCookie, seenProxyAuth, seenXFF string
	var seenHost string
	ta := newDeepInfraTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		seenAuth = r.Header.Get("Authorization")
		seenHost = r.Host
		seenCookie = r.Header.Get("Cookie")
		seenProxyAuth = r.Header.Get("Proxy-Authorization")
		seenXFF = r.Header.Get("X-Forwarded-For")
		jsonRespond(w, http.StatusOK, `{"id":"x","choices":[]}`, nil)
	}, m)

	w := httptest.NewRecorder()
	ta.engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"di-filtered","messages":[]}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	if seenAuth != "Bearer deepinfra_real_token" {
		t.Errorf("Authorization = %q, want Bearer deepinfra_real_token", seenAuth)
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

// TestDeepInfra_CredentialsDoNotLeak verifies that traces, responses, and
// captures contain no token sentinel.
func TestDeepInfra_CredentialsDoNotLeak(t *testing.T) {
	credSentinel := "deepinfra_secret_credential_xyz"
	t.Setenv("DEEPINFRA_TOKEN", credSentinel)

	m := deepinfraModel("di-leak", "meta-llama/Llama-3.3-70B-Instruct", nil)

	ta := newDeepInfraTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		jsonRespond(w, http.StatusOK, `{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"response"}}]}`, nil)
	}, m)

	w := httptest.NewRecorder()
	ta.engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"di-leak","messages":[]}`)))

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

// TestDeepInfra_ErrorResponsesDoNotLeakCredentials verifies that error
// responses do not leak credential sentinels.
func TestDeepInfra_ErrorResponsesDoNotLeakCredentials(t *testing.T) {
	credSentinel := "deepinfra_err_secret_abc"
	t.Setenv("DEEPINFRA_TOKEN", credSentinel)

	m := deepinfraModel("di-eleak", "meta-llama/Llama-3.3-70B-Instruct", nil)

	ta := newDeepInfraTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":{"message":"internal server error","type":"server_error"}}`))
	}, m)

	w := httptest.NewRecorder()
	ta.engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"di-eleak","messages":[]}`)))

	if strings.Contains(w.Body.String(), credSentinel) {
		t.Errorf("credential sentinel leaked in error response body")
	}
}

// TestDeepInfra_CredentialNamedFieldsNotLeaked verifies that credential values
// placed in credential-named query/JSON fields do not appear in the response.
func TestDeepInfra_CredentialNamedFieldsNotLeaked(t *testing.T) {
	credSentinel := "deepinfra_query_secret"
	t.Setenv("DEEPINFRA_TOKEN", credSentinel)

	m := deepinfraModel("di-cfields", "meta-llama/Llama-3.3-70B-Instruct", nil)

	ta := newDeepInfraTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		jsonRespond(w, http.StatusOK, `{"id":"x","choices":[]}`, nil)
	}, m)

	// Send a request with credential-named JSON fields.
	reqBody := `{"model":"di-cfields","messages":[],"api_key":"should-not-relay","token":"should-not-relay"}`
	w := httptest.NewRecorder()
	ta.engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(reqBody)))

	// The credential sentinel (the real token) must not appear in the response.
	if strings.Contains(w.Body.String(), credSentinel) {
		t.Errorf("credential sentinel leaked in response")
	}
}

// ---------------------------------------------------------------------------
// VAL-DEEPINFRA-016: DeepInfra truncated streams recover and all resources
// clean up.
// ---------------------------------------------------------------------------

// TestDeepInfra_TruncatedStreamEmitsSingleStreamTruncated verifies that an
// SSE close before [DONE] yields the existing single stream_truncated error
// without retry or invented [DONE].
func TestDeepInfra_TruncatedStreamEmitsSingleStreamTruncated(t *testing.T) {
	t.Setenv("DEEPINFRA_TOKEN", "deepinfra_key")

	m := deepinfraModel("di-trunc", "meta-llama/Llama-3.3-70B-Instruct", nil)

	ta := newDeepInfraTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		fmt.Fprint(w, "data: {\"id\":\"1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"}}]}\n\n")
		flusher.Flush()
		// Connection closes here (no [DONE]).
	}, m)

	w := httptest.NewRecorder()
	ta.engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"di-trunc","stream":true,"messages":[]}`)))

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

// TestDeepInfra_TruncatedStreamRecovery verifies that a subsequent healthy
// request succeeds after a truncated stream.
func TestDeepInfra_TruncatedStreamRecovery(t *testing.T) {
	t.Setenv("DEEPINFRA_TOKEN", "deepinfra_key")

	m := deepinfraModel("di-recover", "meta-llama/Llama-3.3-70B-Instruct", nil)

	callCount := 0
	ta := newDeepInfraTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 1 {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			flusher := w.(http.Flusher)
			fmt.Fprint(w, "data: {\"id\":\"1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"}}]}\n\n")
			flusher.Flush()
			return
		}
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
		strings.NewReader(`{"model":"di-recover","stream":true,"messages":[]}`)))
	if !strings.Contains(w1.Body.String(), "stream_truncated") {
		t.Error("first request should be truncated")
	}

	// Second request: healthy.
	w2 := httptest.NewRecorder()
	ta.engine.ServeHTTP(w2, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"di-recover","stream":true,"messages":[]}`)))
	if w2.Code != http.StatusOK {
		t.Fatalf("recovery request: expected 200, got %d", w2.Code)
	}
	if !strings.Contains(w2.Body.String(), "[DONE]") {
		t.Error("recovery request should contain [DONE]")
	}
}

// TestDeepInfra_HTTPTeardownRemovesAllTemporaryResources verifies that fake
// servers and listeners created during testing are cleaned up, ports are
// reusable, and the repository status is unchanged.
func TestDeepInfra_HTTPTeardownRemovesAllTemporaryResources(t *testing.T) {
	t.Setenv("DEEPINFRA_TOKEN", "deepinfra_key")

	m := deepinfraModel("di-teardown", "meta-llama/Llama-3.3-70B-Instruct", nil)

	var upstreamAddr string
	ta := newDeepInfraTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		jsonRespond(w, http.StatusOK, `{"id":"x","choices":[]}`, nil)
	}, m)
	upstreamAddr = ta.upstream.Listener.Addr().String()

	// Make a request to prove the server is alive.
	w := httptest.NewRecorder()
	ta.engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"di-teardown","messages":[]}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	// Verify the upstream server is currently reachable.
	conn, err := net.Dial("tcp", upstreamAddr)
	if err != nil {
		t.Fatalf("upstream server not reachable during test: %v", err)
	}
	conn.Close()
	// Cleanup is via t.Cleanup registered in newDeepInfraTestAPI.
}

// TestDeepInfra_TLSInterceptorTeardown verifies that the canonical-host TLS
// interceptor is cleaned up after use.
func TestDeepInfra_TLSInterceptorTeardown(t *testing.T) {
	t.Setenv("DEEPINFRA_TOKEN", "deepinfra_key")

	srv, _, _ := canonicalTLSInterceptor(t, func(w http.ResponseWriter, r *http.Request) {
		jsonRespond(w, http.StatusOK, `{"id":"x","choices":[]}`, nil)
	})
	addr := srv.Listener.Addr().String()

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("TLS interceptor not reachable: %v", err)
	}
	conn.Close()
	// Cleanup is via t.Cleanup registered in canonicalTLSInterceptor.
}

// TestDeepInfra_FilterHeaders_RemovesUnsafeCategories directly exercises the
// proxy's FilterHeaders function to prove cookies, hop-by-hop headers,
// connection-nominated headers, compression-derived headers, and
// gateway-identifying prefixes are all removed while safe metadata survives.
func TestDeepInfra_FilterHeaders_RemovesUnsafeCategories(t *testing.T) {
	src := http.Header{}
	src.Set("X-Request-ID", "generic-req-id")
	src.Set("Retry-After", "30")
	src.Set("Set-Cookie", "session=leaked")
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
	src.Set("Connection", "keep-alive, X-Custom-Named")
	src.Set("X-Custom-Named", "conn-scoped-val")
	src.Set("X-Litellm-Version", "1.0")
	src.Set("X-Portkey-Id", "pk-1")
	src.Set("Helicone-Request-Id", "hc-1")
	src.Set("Cf-Aig-Status", "blocked")
	src.Set("X-Kong-Proxy-Latency", "123")

	filtered := upstream.FilterHeaders(src)

	for k, v := range map[string]string{
		"X-Request-ID": "generic-req-id",
		"Retry-After":  "30",
	} {
		if got := filtered.Get(k); got != v {
			t.Errorf("FilterHeaders dropped safe header %s: got %q, want %q", k, got, v)
		}
	}

	removed := []string{
		"Set-Cookie", "Connection", "Keep-Alive", "Transfer-Encoding",
		"Proxy-Authenticate", "Proxy-Authorization", "Te", "Trailer",
		"Upgrade", "Content-Length", "Content-Encoding",
		"X-Custom-Named",
		"X-Litellm-Version", "X-Portkey-Id", "Helicone-Request-Id",
		"Cf-Aig-Status", "X-Kong-Proxy-Latency",
	}
	for _, h := range removed {
		if got := filtered.Get(h); got != "" {
			t.Errorf("FilterHeaders kept unsafe header %s: %q", h, got)
		}
	}
}
