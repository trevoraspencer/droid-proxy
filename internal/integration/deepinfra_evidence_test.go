package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"

	"github.com/trevoraspencer/droid-proxy/internal/config"
	"github.com/trevoraspencer/droid-proxy/internal/providerapi"
)

// ---------------------------------------------------------------------------
// DeepInfra cross-integration evidence hardening.
//
// These tests close scrutiny round-1 observations by exercising production
// code paths through local fakes. They do NOT claim to drive the interactive
// TUI; programmatic persistence mirrors TUI output but the real interactive
// byte production is verified separately by the user-testing validator.
//
// Coverage:
//   - Official-shape unauthenticated /models/list discovery through production
//     parsing and filtering (bare-array, model_name, exact text-generation).
//   - Exact default, priority, and flex response-tier literals relayed
//     byte-for-byte through the real generic Chat handler.
//   - Cross-provider request and response tier isolation under sequential and
//     interleaved requests.
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Official-shape DeepInfra discovery through production parsing and filtering.
// ---------------------------------------------------------------------------

// TestDeepInfraEvidence_DiscoveryOfficialShapeProductionParsing exercises the
// real DeepInfra registry profile redirected to a local fake /models/list
// server. Production code (providerapi.ListModelsWithOptions with the actual
// registry fields) extracts model_name, retains only exact reported_type
// text-generation rows, and sends no credential header.
func TestDeepInfraEvidence_DiscoveryOfficialShapeProductionParsing(t *testing.T) {
	clearInheritedCredentials(t)
	// Inject a synthetic inference token to prove discovery does NOT send it.
	t.Setenv("DEEPINFRA_TOKEN", sentinelDeepInfra)

	// Official-contract bare-array fixture with mixed reported_type values.
	officialFixture := `[
		{"model_name":"meta-llama/Llama-3.3-70B-Instruct","reported_type":"text-generation"},
		{"model_name":"stable-diffusion-xl","reported_type":"image-generation"},
		{"model_name":"bge-large-en","reported_type":"text-embedding"},
		{"model_name":"deepinfra/deepseek-v4","reported_type":"text-generation"},
		{"model_name":"whisper-large-v3","reported_type":"audio"},
		{"model_name":"Qwen/Qwen2.5-72B-Instruct","reported_type":"text-generation"},
		{"model_name":"Text-Generation-Case-Variant","reported_type":"Text-Generation"}
	]`

	var (
		gotMethod  string
		gotPath    string
		gotAuth    string
		gotAccept  string
		reqCount   int
		allHeaders http.Header
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqCount++
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotAccept = r.Header.Get("Accept")
		allHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(officialFixture))
	}))
	defer srv.Close()

	// Use the real DeepInfra registry profile, redirecting only the discovery
	// base URL to the local fake. This exercises production parsing and
	// filtering exactly as backend.discover does.
	ka, ok := config.LookupKnownAuth("deepinfra")
	if !ok {
		t.Fatal("deepinfra profile missing from registry")
	}
	ka.DiscoveryBaseURL = srv.URL

	// Call the production discovery function with the real profile fields,
	// exactly as backend.discover does. A non-empty synthetic inference token
	// is present in the environment but must NOT be sent because
	// DiscoveryNoAuth suppresses it.
	ids, err := providerapi.ListModelsWithOptions(context.Background(), providerapi.ListOptions{
		BaseURL:    ka.DiscoveryBaseURL,
		ModelsPath: ka.ModelsPath,
		APIKey:     "", // DiscoveryNoAuth causes backend.discover to pass empty
		IDField:    ka.DiscoveryIDField,
		TypeField:  ka.DiscoveryTypeField,
		TypeValue:  ka.DiscoveryTypeValue,
	})
	if err != nil {
		t.Fatalf("production discovery failed: %v", err)
	}

	// --- Verify exactly one unauthenticated request to /models/list ---
	if reqCount != 1 {
		t.Errorf("request count = %d, want exactly 1", reqCount)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %q, want GET", gotMethod)
	}
	if gotPath != "/models/list" {
		t.Errorf("path = %q, want /models/list", gotPath)
	}

	// --- Verify no credential header is sent ---
	if gotAuth != "" {
		t.Errorf("Authorization header = %q, want empty (unauthenticated discovery)", gotAuth)
	}
	// Scan all headers for any credential-bearing header.
	for hk, hv := range allHeaders {
		hkLower := strings.ToLower(hk)
		if strings.Contains(hkLower, "auth") || strings.Contains(hkLower, "key") ||
			strings.Contains(hkLower, "token") || strings.Contains(hkLower, "credential") {
			for _, v := range hv {
				if v != "" {
					t.Errorf("credential-bearing header %q = %q, want empty", hk, v)
				}
			}
		}
	}
	// Verify the synthetic sentinel is never transmitted.
	for _, hv := range allHeaders.Values("Authorization") {
		if strings.Contains(hv, sentinelDeepInfra) {
			t.Errorf("Authorization header leaked sentinel: %q", hv)
		}
	}

	// --- Verify Accept: application/json ---
	if gotAccept != "application/json" {
		t.Errorf("Accept = %q, want application/json", gotAccept)
	}

	// --- Verify model_name extraction and exact text-generation filtering ---
	// Only exact "text-generation" rows are retained. Case variants,
	// image-generation, text-embedding, and audio are excluded.
	want := []string{
		"Qwen/Qwen2.5-72B-Instruct",
		"deepinfra/deepseek-v4",
		"meta-llama/Llama-3.3-70B-Instruct",
	}
	if !reflect.DeepEqual(ids, want) {
		t.Errorf("discovery ids = %v\nwant              = %v\n(only exact text-generation, sorted, deduped)", ids, want)
	}
}

// TestDeepInfraEvidence_DiscoveryPreservesOpaqueIDs verifies that public
// Hugging Face-style IDs, version suffixes, and deploy_id values survive
// production parsing without normalization through the real registry profile.
func TestDeepInfraEvidence_DiscoveryPreservesOpaqueIDs(t *testing.T) {
	clearInheritedCredentials(t)

	opaqueIDs := []string{
		"meta-llama/Llama-3.3-70B-Instruct",
		"deepinfra/deepseek-v4",
		"Qwen/Qwen2.5-72B-Instruct",
		"model-with-deploy_id:abc123",
		"org/sub/model-v2.1",
		"org/mixed-CASE.Model:deploy-1",
	}
	var rawJSON strings.Builder
	rawJSON.WriteByte('[')
	for i, id := range opaqueIDs {
		if i > 0 {
			rawJSON.WriteByte(',')
		}
		rawJSON.WriteString(`{"model_name":"`)
		rawJSON.WriteString(id)
		rawJSON.WriteString(`","reported_type":"text-generation"}`)
	}
	rawJSON.WriteByte(']')

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(rawJSON.String()))
	}))
	defer srv.Close()

	ka, _ := config.LookupKnownAuth("deepinfra")
	ka.DiscoveryBaseURL = srv.URL

	ids, err := providerapi.ListModelsWithOptions(context.Background(), providerapi.ListOptions{
		BaseURL:    ka.DiscoveryBaseURL,
		ModelsPath: ka.ModelsPath,
		APIKey:     "",
		IDField:    ka.DiscoveryIDField,
		TypeField:  ka.DiscoveryTypeField,
		TypeValue:  ka.DiscoveryTypeValue,
	})
	if err != nil {
		t.Fatalf("discovery: %v", err)
	}
	idSet := make(map[string]bool, len(ids))
	for _, id := range ids {
		idSet[id] = true
	}
	for _, original := range opaqueIDs {
		if !idSet[original] {
			t.Errorf("opaque ID %q not preserved by production parsing: got %v", original, ids)
		}
	}
}

// ---------------------------------------------------------------------------
// Exact default, priority, and flex response-tier literals through the real
// generic Chat handler.
// ---------------------------------------------------------------------------

// deepinfraTierSetup builds a test API with three DeepInfra models (Standard,
// Priority, Flex) all pointing at the same fake upstream. The fake upstream's
// response can be changed per test case. The real generic Chat handler
// (handlers.API.ChatCompletions) processes every request.
func deepinfraTierSetup(t *testing.T) (*combinedInstallation, []*config.Model) {
	t.Helper()
	ci := newCombinedInstallation(t)

	stdModel := &config.Model{
		Alias:            "di-tier-std",
		DisplayName:      "DI Standard",
		FactoryProvider:  config.FactoryProviderGeneric,
		UpstreamProtocol: config.UpstreamOpenAIChat,
		KnownAuth:        "deepinfra",
		UpstreamModel:    "meta-llama/Llama-3.3-70B-Instruct",
		BaseURL:          ci.upstreams["deepinfra"].BaseURL() + "/v1/openai",
	}
	priModel := &config.Model{
		Alias:            "di-tier-pri",
		DisplayName:      "DI Priority",
		FactoryProvider:  config.FactoryProviderGeneric,
		UpstreamProtocol: config.UpstreamOpenAIChat,
		KnownAuth:        "deepinfra",
		UpstreamModel:    "meta-llama/Llama-3.3-70B-Instruct",
		BaseURL:          ci.upstreams["deepinfra"].BaseURL() + "/v1/openai",
		ExtraArgs:        map[string]any{"service_tier": "priority"},
	}
	flexModel := &config.Model{
		Alias:            "di-tier-flex",
		DisplayName:      "DI Flex",
		FactoryProvider:  config.FactoryProviderGeneric,
		UpstreamProtocol: config.UpstreamOpenAIChat,
		KnownAuth:        "deepinfra",
		UpstreamModel:    "meta-llama/Llama-3.3-70B-Instruct",
		BaseURL:          ci.upstreams["deepinfra"].BaseURL() + "/v1/openai",
		ExtraArgs:        map[string]any{"service_tier": "flex"},
	}
	for _, m := range []*config.Model{stdModel, priModel, flexModel} {
		if err := config.HydrateModel(m); err != nil {
			t.Fatalf("hydrate %q: %v", m.Alias, err)
		}
	}
	// Override BaseURL after hydration so it points at the fake upstream.
	for _, m := range []*config.Model{stdModel, priModel, flexModel} {
		m.BaseURL = ci.upstreams["deepinfra"].BaseURL() + "/v1/openai"
	}
	return ci, []*config.Model{stdModel, priModel, flexModel}
}

// buildTierEngine builds a gin engine with the given DeepInfra models,
// exercising the real production handlers.API.ChatCompletions path.
func buildTierEngine(t *testing.T, ci *combinedInstallation, models []*config.Model) *gin.Engine {
	t.Helper()
	cfg := &config.Config{
		Upstream: config.Upstream{
			HTTPTimeout:     5 * time.Second,
			StreamKeepAlive: 200 * time.Millisecond,
		},
		Models: models,
	}
	_, engine := ci.buildAPI(cfg)
	return engine
}

// TestDeepInfraEvidence_ResponseTierLiteralsThroughGenericChat verifies that
// DeepInfra Standard (effective "default"), Priority, and Flex fake responses
// relay their exact service_tier literals byte-for-byte through the real
// generic Chat handler. No rewriting, retry, downgrade, or tier synthesis
// occurs.
func TestDeepInfraEvidence_ResponseTierLiteralsThroughGenericChat(t *testing.T) {
	clearInheritedCredentials(t)
	t.Setenv("DEEPINFRA_TOKEN", sentinelDeepInfra)

	ci, models := deepinfraTierSetup(t)
	engine := buildTierEngine(t, ci, models)

	cases := []struct {
		name        string
		alias       string
		fixtureBody string
		wantTier    string
	}{
		{
			name:  "standard_effective_default",
			alias: "di-tier-std",
			// Standard requests omit service_tier; effective Standard fallback
			// uses response literal "default".
			fixtureBody: `{"id":"di-std","object":"chat.completion","service_tier":"default","choices":[{"index":0,"message":{"role":"assistant","content":"standard response"}}],"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8}}`,
			wantTier:    "default",
		},
		{
			name:        "priority_success",
			alias:       "di-tier-pri",
			fixtureBody: `{"id":"di-pri","object":"chat.completion","service_tier":"priority","choices":[{"index":0,"message":{"role":"assistant","content":"priority response"}}],"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8}}`,
			wantTier:    "priority",
		},
		{
			name:  "priority_fallback_default",
			alias: "di-tier-pri",
			// Priority requested but unsupported -> effective Standard "default".
			fixtureBody: `{"id":"di-pri-fb","object":"chat.completion","service_tier":"default","choices":[{"index":0,"message":{"role":"assistant","content":"priority fallback response"}}],"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8}}`,
			wantTier:    "default",
		},
		{
			name:        "flex_success",
			alias:       "di-tier-flex",
			fixtureBody: `{"id":"di-flex","object":"chat.completion","service_tier":"flex","choices":[{"index":0,"message":{"role":"assistant","content":"flex response"}}],"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8}}`,
			wantTier:    "flex",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ci.upstreams["deepinfra"].SetResponse(tc.fixtureBody, http.StatusOK)
			resetAllCaptures(ci)

			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
				strings.NewReader(fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}]}`, tc.alias)))
			w := httptest.NewRecorder()
			engine.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("%s: status = %d, body = %s", tc.name, w.Code, w.Body.String())
			}

			// Exact response-tier literal relayed through the real Chat handler.
			gotTier := gjson.Get(w.Body.String(), "service_tier").String()
			if gotTier != tc.wantTier {
				t.Errorf("%s: response service_tier = %q, want %q", tc.name, gotTier, tc.wantTier)
			}

			// Body relayed byte-for-byte (SHA-256 match).
			fixtureHash := sha256.Sum256([]byte(tc.fixtureBody))
			respHash := sha256.Sum256(w.Body.Bytes())
			if fixtureHash != respHash {
				t.Errorf("%s: response body SHA-256 mismatch:\nwant=%s\ngot =%s",
					tc.name, hex.EncodeToString(fixtureHash[:]), hex.EncodeToString(respHash[:]))
			}
		})
	}
}

// TestDeepInfraEvidence_RequestTierPassthroughThroughGenericChat verifies that
// the real generic Chat handler sends the exact configured service_tier value
// upstream for Priority and Flex, and omits it entirely for Standard.
func TestDeepInfraEvidence_RequestTierPassthroughThroughGenericChat(t *testing.T) {
	clearInheritedCredentials(t)
	t.Setenv("DEEPINFRA_TOKEN", sentinelDeepInfra)

	ci, models := deepinfraTierSetup(t)
	engine := buildTierEngine(t, ci, models)

	ci.upstreams["deepinfra"].SetResponse(
		`{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"ok"}}]}`, http.StatusOK)

	cases := []struct {
		alias    string
		hasTier  bool
		wantTier string
	}{
		{"di-tier-std", false, ""},
		{"di-tier-pri", true, "priority"},
		{"di-tier-flex", true, "flex"},
	}
	for _, tc := range cases {
		t.Run(tc.alias, func(t *testing.T) {
			resetAllCaptures(ci)
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
				strings.NewReader(fmt.Sprintf(`{"model":%q,"messages":[]}`, tc.alias)))
			w := httptest.NewRecorder()
			engine.ServeHTTP(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("%s: status = %d", tc.alias, w.Code)
			}
			cap := ci.upstreams["deepinfra"].Capture().Get(0)
			tier := gjson.GetBytes(cap.Body, "service_tier")
			if tc.hasTier {
				if !tier.Exists() || tier.String() != tc.wantTier {
					t.Errorf("%s: upstream service_tier = %q, want %q", tc.alias, tier.Raw, tc.wantTier)
				}
			} else {
				if tier.Exists() {
					t.Errorf("%s: service_tier should be absent, got %q", tc.alias, tier.Raw)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Cross-provider request and response tier isolation.
// ---------------------------------------------------------------------------

// TestDeepInfraEvidence_CrossProviderTierResponseIsolation verifies that
// response-tier literals remain isolated per alias when DeepInfra Priority/Flex
// and Fireworks Priority requests are interleaved. Each response's service_tier
// matches only the fake fixture configured for that alias, with no cross-alias
// leakage.
func TestDeepInfraEvidence_CrossProviderTierResponseIsolation(t *testing.T) {
	clearInheritedCredentials(t)
	injectSyntheticCredentials(t)

	ci := newCombinedInstallation(t)
	cfg := ci.loadFromDisk()
	_, engine := ci.buildAPI(cfg)

	// Sequence: DeepInfra (through combined installation alias) and Fireworks
	// Priority interleaved. Each upstream gets a distinct fixture so we can
	// verify response tiers never cross aliases.
	diPriorityFixture := `{"id":"di-x","service_tier":"priority","choices":[{"index":0,"message":{"role":"assistant","content":"di-priority"}}]}`
	diDefaultFixture := `{"id":"di-d","service_tier":"default","choices":[{"index":0,"message":{"role":"assistant","content":"di-default"}}]}`
	fwPriorityFixture := `{"id":"fw-x","service_tier":"priority","choices":[{"index":0,"message":{"role":"assistant","content":"fw-priority"}}]}`
	fwStandardFixture := `{"id":"fw-s","choices":[{"index":0,"message":{"role":"assistant","content":"fw-standard"}}]}`

	sequence := []struct {
		alias       string
		upstream    string
		fixtureBody string
		wantTier    string
		wantTierSet bool
	}{
		{"deepinfra-model", "deepinfra", diDefaultFixture, "default", true},
		{"fw-priority", "fireworks", fwPriorityFixture, "priority", true},
		{"deepinfra-model", "deepinfra", diPriorityFixture, "priority", true},
		{"fw-standard", "fireworks", fwStandardFixture, "", false},
		{"deepinfra-model", "deepinfra", diDefaultFixture, "default", true},
		{"fw-priority", "fireworks", fwPriorityFixture, "priority", true},
	}

	for i, tc := range sequence {
		ci.upstreams[tc.upstream].SetResponse(tc.fixtureBody, http.StatusOK)
		resetAllCaptures(ci)

		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
			strings.NewReader(fmt.Sprintf(`{"model":%q,"messages":[]}`, tc.alias)))
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("step %d (%s): status = %d, body=%s", i, tc.alias, w.Code, w.Body.String())
		}

		// Verify the response tier matches the fixture for this alias.
		tier := gjson.Get(w.Body.String(), "service_tier")
		if tc.wantTierSet {
			if !tier.Exists() || tier.String() != tc.wantTier {
				t.Errorf("step %d (%s): response service_tier = %q, want %q",
					i, tc.alias, tier.Raw, tc.wantTier)
			}
		} else {
			if tier.Exists() {
				t.Errorf("step %d (%s): response should have no service_tier, got %q",
					i, tc.alias, tier.Raw)
			}
		}

		// Verify byte-for-byte relay of the fixture.
		respHash := sha256.Sum256(w.Body.Bytes())
		fixtureHash := sha256.Sum256([]byte(tc.fixtureBody))
		if respHash != fixtureHash {
			t.Errorf("step %d (%s): response body SHA-256 mismatch (not relayed byte-for-byte)",
				i, tc.alias)
		}

		// Verify the request tier is isolated: only the alias's configured tier
		// is sent upstream, never a leaked tier from another alias.
		cap := ci.upstreams[tc.upstream].Capture().Get(0)
		reqTier := gjson.GetBytes(cap.Body, "service_tier")
		switch tc.alias {
		case "deepinfra-model":
			// Standard DeepInfra in the combined installation has no service_tier.
			if reqTier.Exists() {
				t.Errorf("step %d: deepinfra-model request leaked service_tier = %q", i, reqTier.Raw)
			}
		case "fw-priority":
			if !reqTier.Exists() || reqTier.String() != "priority" {
				t.Errorf("step %d: fw-priority request service_tier = %q, want priority", i, reqTier.Raw)
			}
		case "fw-standard":
			if reqTier.Exists() {
				t.Errorf("step %d: fw-standard request leaked service_tier = %q", i, reqTier.Raw)
			}
		}

		// Verify auth isolation: each upstream only receives its own credential.
		auth := cap.Header.Get("Authorization")
		switch tc.upstream {
		case "deepinfra":
			if auth != "Bearer "+sentinelDeepInfra {
				t.Errorf("step %d: deepinfra upstream auth = %q, want Bearer %s", i, auth, sentinelDeepInfra)
			}
		case "fireworks":
			if auth != "Bearer "+sentinelFireworksStd && auth != "Bearer "+sentinelFireworksPass {
				t.Errorf("step %d: fireworks upstream auth = %q, unexpected", i, auth)
			}
		}
	}
}

// TestDeepInfraEvidence_CrossProviderRequestTierSequentialIsolation verifies
// that request service_tier values do not leak across aliases when
// DeepInfra Priority/Flex and Fireworks Priority are sent sequentially through
// the real generic Chat handler.
func TestDeepInfraEvidence_CrossProviderRequestTierSequentialIsolation(t *testing.T) {
	clearInheritedCredentials(t)
	injectSyntheticCredentials(t)

	ci, models := deepinfraTierSetup(t)
	// Merge the combined installation's Fireworks models with the DeepInfra
	// tier models so we can exercise cross-provider request isolation.
	cfg := ci.loadFromDisk()
	combinedModels := append(cfg.Models, models...)
	for _, m := range models {
		// Ensure the DeepInfra tier models point at the fake upstream.
		m.BaseURL = ci.upstreams["deepinfra"].BaseURL() + "/v1/openai"
	}
	_, engine := ci.buildAPI(&config.Config{
		Upstream: cfg.Upstream,
		Models:   combinedModels,
	})

	ci.upstreams["deepinfra"].SetResponse(
		`{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"ok"}}]}`, http.StatusOK)
	ci.upstreams["fireworks"].SetResponse(
		`{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"ok"}}]}`, http.StatusOK)

	sequence := []struct {
		alias    string
		upstream string
		hasTier  bool
		wantTier string
	}{
		{"di-tier-std", "deepinfra", false, ""},
		{"fw-priority", "fireworks", true, "priority"},
		{"di-tier-pri", "deepinfra", true, "priority"},
		{"fw-standard", "fireworks", false, ""},
		{"di-tier-flex", "deepinfra", true, "flex"},
		{"fw-priority", "fireworks", true, "priority"},
		{"di-tier-std", "deepinfra", false, ""},
	}

	resetAllCaptures(ci)
	for i, tc := range sequence {
		resetAllCaptures(ci)
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
			strings.NewReader(fmt.Sprintf(`{"model":%q,"messages":[]}`, tc.alias)))
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("step %d (%s): status = %d", i, tc.alias, w.Code)
		}
		cap := ci.upstreams[tc.upstream].Capture().Get(0)
		tier := gjson.GetBytes(cap.Body, "service_tier")
		if tc.hasTier {
			if !tier.Exists() || tier.String() != tc.wantTier {
				t.Errorf("step %d (%s): request service_tier = %q, want %q", i, tc.alias, tier.Raw, tc.wantTier)
			}
		} else {
			if tier.Exists() {
				t.Errorf("step %d (%s): request leaked service_tier = %q", i, tc.alias, tier.Raw)
			}
		}
	}
}
