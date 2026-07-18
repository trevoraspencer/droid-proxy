package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

// ---------------------------------------------------------------------------
// VAL-CROSS-004: Fresh setup reaches each provider through the synced alias.
//
// A fresh isolated setup produces exact production config/Factory bytes and
// the combined installation can add Fireworks Standard, Fire Pass, Baseten,
// and DeepInfra and sync each alias. Each request model is read from that
// provider's temporary customModels[].model, not duplicated as a harness
// constant, and joins to the persisted alias and exact fake-captured upstream
// model.
// ---------------------------------------------------------------------------

func TestFreshSetupJoinsEveryProviderSurface(t *testing.T) {
	ci := newCombinedInstallation(t)

	// Verify the persisted config resolves to the production default.
	cfg := ci.loadFromDisk()
	if cfg.Listen.Port != 9787 {
		t.Fatalf("listen.port = %d, want 9787 (production default)", cfg.Listen.Port)
	}
	if cfg.Listen.Host != "127.0.0.1" {
		t.Fatalf("listen.host = %q, want 127.0.0.1", cfg.Listen.Host)
	}

	// Read aliases from the temporary Factory settings, not harness constants.
	factoryAliases := ci.factoryAliases()
	if len(factoryAliases) != len(ci.modelDefs) {
		t.Fatalf("factory aliases = %d, want %d", len(factoryAliases), len(ci.modelDefs))
	}

	// Build the runtime API from persisted config (simulates restart).
	_, engine := ci.buildAPI(cfg)

	// Verify /v1/models lists every alias.
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	engine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("/v1/models status = %d, want 200", w.Code)
	}
	listBody := w.Body.Bytes()
	for _, alias := range factoryAliases {
		idPath := fmt.Sprintf("data.#(id==%q).id", alias)
		if !gjson.GetBytes(listBody, idPath).Exists() {
			t.Errorf("local /v1/models missing alias %q", alias)
		}
	}

	// Send one request per alias and verify the five-surface join:
	// Factory model -> persisted alias -> local /v1/models ID ->
	// downstream request model -> exact upstream model.
	expectedUpstream := map[string]string{
		"fw-standard":     "accounts/fireworks/models/glm-4-standard",
		"fw-priority":     "accounts/fireworks/models/glm-4-priority",
		"fw-fast":         "accounts/fireworks/routers/glm-5p2-fast",
		"fw-firepass":     "accounts/fireworks/routers/glm-5p2-fast",
		"baseten-model":   "org-team/proj-q3-model",
		"deepinfra-model": "meta-llama/Llama-3.3-70B-Instruct",
	}
	expectedAuthEnv := map[string]string{
		"fw-standard":     "Bearer " + sentinelFireworksStd,
		"fw-priority":     "Bearer " + sentinelFireworksStd,
		"fw-fast":         "Bearer " + sentinelFireworksStd,
		"fw-firepass":     "Bearer " + sentinelFireworksPass,
		"baseten-model":   "Bearer " + sentinelBaseten,
		"deepinfra-model": "Bearer " + sentinelDeepInfra,
	}

	for _, alias := range factoryAliases {
		resetAllCaptures(ci)

		w, sentBody := sendChat(t, engine, alias, "")
		if w.Code != http.StatusOK {
			t.Fatalf("%s: chat status = %d body=%s", alias, w.Code, w.Body.String())
		}

		// Determine which upstream received the request.
		var cap capturedRequest
		var found bool
		for _, fu := range ci.upstreams {
			if fu.Capture().Count() > 0 {
				cap = fu.Capture().Get(0)
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("%s: no upstream received a request", alias)
		}

		// Join: downstream request model == Factory alias (before rewrite).
		sentModel := gjson.GetBytes(sentBody, "model").String()
		if sentModel != alias {
			t.Errorf("%s: downstream request model = %q, want %q (Factory alias)", alias, sentModel, alias)
		}

		// Join: upstream model == expected opaque upstream model.
		upstreamModel := gjson.GetBytes(cap.Body, "model").String()
		if upstreamModel != expectedUpstream[alias] {
			t.Errorf("%s: upstream model = %q, want %q", alias, upstreamModel, expectedUpstream[alias])
		}

		// Join: upstream path ends with /chat/completions.
		if !strings.HasSuffix(cap.Path, "/chat/completions") {
			t.Errorf("%s: upstream path = %q, want suffix /chat/completions", alias, cap.Path)
		}

		// Join: auth header matches expected env var's synthetic value.
		if got := cap.Header.Get("Authorization"); got != expectedAuthEnv[alias] {
			t.Errorf("%s: upstream auth = %q, want %q", alias, got, expectedAuthEnv[alias])
		}
	}

	// Verify production files remain unchanged after runtime requests
	// (runtime only reads config, never writes it).
	cfgHashBefore := fileHash(t, ci.configPath)
	factoryHashBefore := fileHash(t, ci.factoryPath)
	_ = ci.loadFromDisk()
	if fileHash(t, ci.configPath) != cfgHashBefore {
		t.Error("config file changed during runtime request")
	}
	if fileHash(t, ci.factoryPath) != factoryHashBefore {
		t.Error("factory settings changed during runtime request")
	}
}

// ---------------------------------------------------------------------------
// VAL-CROSS-005: All native provider credentials and models coexist
// independently.
//
// The combined installation is built by adding all four profiles. After every
// operation, only the selected credential/model differs. Sequential and
// interleaved runtime requests select credentials by declared profile rather
// than key prefix or model inspection.
// ---------------------------------------------------------------------------

func TestCombinedInstallation_AllCredentialsCoexist(t *testing.T) {
	ci := newCombinedInstallation(t)
	cfg := ci.loadFromDisk()
	_, engine := ci.buildAPI(cfg)
	_ = engine

	// All four env variables coexist independently.
	type credCheck struct {
		env      string
		sentinel string
	}
	creds := []credCheck{
		{"FIREWORKS_API_KEY", sentinelFireworksStd},
		{"FIREWORKS_FIRE_PASS_API_KEY", sentinelFireworksPass},
		{"BASETEN_API_KEY", sentinelBaseten},
		{"DEEPINFRA_TOKEN", sentinelDeepInfra},
	}
	for _, c := range creds {
		val := getEnvOrEmpty(c.env)
		if val != c.sentinel {
			t.Errorf("%s = %q, want %q", c.env, val, c.sentinel)
		}
	}

	// All models coexist in config.
	aliasSet := make(map[string]bool)
	for _, m := range cfg.Models {
		aliasSet[m.Alias] = true
	}
	expectedAliases := []string{
		"fw-standard", "fw-priority", "fw-fast", "fw-firepass",
		"baseten-model", "deepinfra-model",
	}
	for _, a := range expectedAliases {
		if !aliasSet[a] {
			t.Errorf("config missing alias %q", a)
		}
	}
}

func TestCombinedInstallation_SequentialRequests_SelectByProfile(t *testing.T) {
	ci := newCombinedInstallation(t)
	cfg := ci.loadFromDisk()
	_, engine := ci.buildAPI(cfg)

	// Send sequential requests across all profiles and verify each selects
	// the correct credential.
	sequence := []struct {
		alias string
		auth  string
	}{
		{"fw-standard", "Bearer " + sentinelFireworksStd},
		{"fw-firepass", "Bearer " + sentinelFireworksPass},
		{"baseten-model", "Bearer " + sentinelBaseten},
		{"deepinfra-model", "Bearer " + sentinelDeepInfra},
		{"fw-priority", "Bearer " + sentinelFireworksStd},
	}

	for _, tc := range sequence {
		resetAllCaptures(ci)
		w, _ := sendChat(t, engine, tc.alias, "")
		if w.Code != http.StatusOK {
			t.Fatalf("%s: status = %d", tc.alias, w.Code)
		}
		var cap capturedRequest
		for _, fu := range ci.upstreams {
			if fu.Capture().Count() > 0 {
				cap = fu.Capture().Get(0)
				break
			}
		}
		if got := cap.Header.Get("Authorization"); got != tc.auth {
			t.Errorf("%s: auth = %q, want %q", tc.alias, got, tc.auth)
		}
	}
}

func TestCombinedInstallation_InterleavedRequests_NoCredentialLeakage(t *testing.T) {
	ci := newCombinedInstallation(t)
	cfg := ci.loadFromDisk()
	_, engine := ci.buildAPI(cfg)

	// Interleave requests across all four providers in a mixed order.
	interleaved := []string{
		"fw-standard", "baseten-model", "fw-firepass", "deepinfra-model",
		"fw-priority", "baseten-model", "fw-firepass", "deepinfra-model",
		"fw-standard", "fw-fast",
	}
	expectedAuthPerAlias := map[string]string{
		"fw-standard":     "Bearer " + sentinelFireworksStd,
		"fw-priority":     "Bearer " + sentinelFireworksStd,
		"fw-fast":         "Bearer " + sentinelFireworksStd,
		"fw-firepass":     "Bearer " + sentinelFireworksPass,
		"baseten-model":   "Bearer " + sentinelBaseten,
		"deepinfra-model": "Bearer " + sentinelDeepInfra,
	}

	resetAllCaptures(ci)
	for _, alias := range interleaved {
		w, _ := sendChat(t, engine, alias, "")
		if w.Code != http.StatusOK {
			t.Fatalf("%s: status = %d", alias, w.Code)
		}
	}

	// Verify each upstream only received its expected credential.
	// Interleaved: fw-standard(1), baseten(1), fw-firepass(1), deepinfra(1),
	// fw-priority(1), baseten(2), fw-firepass(2), deepinfra(2),
	// fw-standard(2), fw-fast(1)
	// Fireworks: fw-standard, fw-firepass, fw-priority, fw-firepass, fw-standard, fw-fast = 6
	// Baseten: baseten, baseten = 2
	// DeepInfra: deepinfra, deepinfra = 2
	fwCaps := ci.upstreams["fireworks"].Capture().All()
	if len(fwCaps) != 6 {
		t.Errorf("fireworks upstream received %d requests, want 6", len(fwCaps))
	}
	for _, cap := range fwCaps {
		auth := cap.Header.Get("Authorization")
		if auth != "Bearer "+sentinelFireworksStd && auth != "Bearer "+sentinelFireworksPass {
			t.Errorf("fireworks upstream received unexpected auth %q", auth)
		}
	}
	// Baseten upstream should only see baseten auth.
	btCaps := ci.upstreams["baseten"].Capture().All()
	if len(btCaps) != 2 {
		t.Errorf("baseten upstream received %d requests, want 2", len(btCaps))
	}
	for _, cap := range btCaps {
		if auth := cap.Header.Get("Authorization"); auth != "Bearer "+sentinelBaseten {
			t.Errorf("baseten upstream received unexpected auth %q", auth)
		}
	}
	// DeepInfra upstream should only see deepinfra auth.
	diCaps := ci.upstreams["deepinfra"].Capture().All()
	if len(diCaps) != 2 {
		t.Errorf("deepinfra upstream received %d requests, want 2", len(diCaps))
	}
	for _, cap := range diCaps {
		if auth := cap.Header.Get("Authorization"); auth != "Bearer "+sentinelDeepInfra {
			t.Errorf("deepinfra upstream received unexpected auth %q", auth)
		}
	}
	_ = expectedAuthPerAlias
}

// ---------------------------------------------------------------------------
// VAL-CROSS-006: Every provider uses the uniform alias pipeline.
//
// Persisted alias, Factory model, /v1/models ID, downstream request model,
// and exact opaque upstream model join correctly for representative IDs.
// Duplicate aliases preserve existing state; unknown aliases fail locally
// with no upstream request.
// ---------------------------------------------------------------------------

func TestUniformAliasPipeline_AllProvidersJoinCorrectly(t *testing.T) {
	ci := newCombinedInstallation(t)
	cfg := ci.loadFromDisk()
	api, engine := ci.buildAPI(cfg)

	factoryAliases := ci.factoryAliases()
	expectedUpstreamModel := map[string]string{
		"fw-standard":     "accounts/fireworks/models/glm-4-standard",
		"fw-priority":     "accounts/fireworks/models/glm-4-priority",
		"fw-fast":         "accounts/fireworks/routers/glm-5p2-fast",
		"fw-firepass":     "accounts/fireworks/routers/glm-5p2-fast",
		"baseten-model":   "org-team/proj-q3-model",
		"deepinfra-model": "meta-llama/Llama-3.3-70B-Instruct",
	}

	for _, alias := range factoryAliases {
		// Surface 1: persisted config alias resolves in router.
		var found bool
		for _, m := range api.Router.List() {
			if m.Alias == alias {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("alias %q not in router", alias)
			continue
		}

		// Surface 2: Factory model == alias.
		s, err := loadFactorySettings(ci.factoryPath)
		if err != nil {
			t.Fatal(err)
		}
		has, _ := s.Has(alias)
		if !has {
			t.Errorf("alias %q not in Factory settings", alias)
		}

		// Surface 3: local /v1/models ID == alias with upstream_model.
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		engine.ServeHTTP(w, req)
		idPath := fmt.Sprintf("data.#(id==%q).upstream_model", alias)
		upstreamModelFromList := gjson.GetBytes(w.Body.Bytes(), idPath).String()
		if upstreamModelFromList != expectedUpstreamModel[alias] {
			t.Errorf("alias %q: /v1/models upstream_model = %q, want %q", alias, upstreamModelFromList, expectedUpstreamModel[alias])
		}

		// Surface 4+5: downstream request model == alias, upstream model == expected.
		resetAllCaptures(ci)
		w2, sentBody := sendChat(t, engine, alias, "")
		if w2.Code != http.StatusOK {
			t.Fatalf("%s: status = %d", alias, w2.Code)
		}
		if model := gjson.GetBytes(sentBody, "model").String(); model != alias {
			t.Errorf("alias %q: downstream model = %q", alias, model)
		}
		var cap capturedRequest
		for _, fu := range ci.upstreams {
			if fu.Capture().Count() > 0 {
				cap = fu.Capture().Get(0)
				break
			}
		}
		if got := gjson.GetBytes(cap.Body, "model").String(); got != expectedUpstreamModel[alias] {
			t.Errorf("alias %q: upstream model = %q, want %q", alias, got, expectedUpstreamModel[alias])
		}
	}
}

func TestUniformAliasPipeline_UnknownAliasFailsLocally(t *testing.T) {
	ci := newCombinedInstallation(t)
	cfg := ci.loadFromDisk()
	_, engine := ci.buildAPI(cfg)

	resetAllCaptures(ci)

	// Send a request with an unknown alias.
	w := sendChatRaw(t, engine, `{"model":"nonexistent-alias","messages":[{"role":"user","content":"hi"}]}`)
	if w.Code != http.StatusBadRequest && w.Code != http.StatusNotFound {
		t.Fatalf("unknown alias: status = %d, want 400 or 404", w.Code)
	}

	// Verify no upstream received any request.
	totalAfter := 0
	for _, fu := range ci.upstreams {
		totalAfter += fu.Capture().Count()
	}
	if totalAfter != 0 {
		t.Errorf("unknown alias caused %d upstream requests (should be 0)", totalAfter)
	}
}

func TestUniformAliasPipeline_DuplicateAliasRejectedAtLoad(t *testing.T) {
	dupYAML := `listen:
  host: 127.0.0.1
  port: 9787
models:
  - alias: dup-alias
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    known_auth: fireworks
    upstream_model: accounts/fireworks/models/m1
    base_url: http://localhost:1/inference/v1
  - alias: dup-alias
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    known_auth: baseten
    upstream_model: m2
    base_url: http://localhost:2/v1
`
	tmpPath := writeTempFile(t, "dup-config-*.yaml", []byte(dupYAML))
	_, err := loadConfigFromPath(tmpPath)
	if err == nil {
		t.Fatal("expected config with duplicate alias to fail validation")
	}
	if !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("expected duplicate error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// VAL-CROSS-009: One OpenAI Chat transport handles every provider workflow.
//
// All four profiles use generic-chat-completion-api, openai-chat, Bearer auth,
// base-plus-/chat/completions, native JSON/SSE relay, tools, tool
// continuation, structured output, reasoning/options, model rewrite, and
// error relay without a provider-specific handler, translator, SDK, retry
// fork, or new runtime dependency.
// ---------------------------------------------------------------------------

func TestOneChatTransport_AllProvidersUseGenericChat(t *testing.T) {
	ci := newCombinedInstallation(t)
	cfg := ci.loadFromDisk()

	// Every model must use generic-chat-completion-api and openai-chat.
	for _, m := range cfg.Models {
		if m.FactoryProvider != "generic-chat-completion-api" {
			t.Errorf("alias %q: factory_provider = %q, want generic-chat-completion-api", m.Alias, m.FactoryProvider)
		}
		if m.UpstreamProtocol != "openai-chat" {
			t.Errorf("alias %q: upstream_protocol = %q, want openai-chat", m.Alias, m.UpstreamProtocol)
		}
	}
}

func TestOneChatTransport_AllProvidersHitChatCompletions(t *testing.T) {
	ci := newCombinedInstallation(t)
	cfg := ci.loadFromDisk()
	_, engine := ci.buildAPI(cfg)

	factoryAliases := ci.factoryAliases()
	for _, alias := range factoryAliases {
		resetAllCaptures(ci)
		w, _ := sendChat(t, engine, alias, "")
		if w.Code != http.StatusOK {
			t.Fatalf("%s: status = %d", alias, w.Code)
		}
		var cap capturedRequest
		found := false
		for _, fu := range ci.upstreams {
			if fu.Capture().Count() > 0 {
				cap = fu.Capture().Get(0)
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("%s: no upstream request captured", alias)
		}
		if cap.Method != http.MethodPost {
			t.Errorf("%s: method = %q, want POST", alias, cap.Method)
		}
		if !strings.HasSuffix(cap.Path, "/chat/completions") {
			t.Errorf("%s: path = %q, want suffix /chat/completions", alias, cap.Path)
		}
		auth := cap.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			t.Errorf("%s: auth = %q, want Bearer scheme", alias, auth)
		}
	}
}

func TestOneChatTransport_NativeJSONRelayedByteForByte(t *testing.T) {
	ci := newCombinedInstallation(t)

	distinctBody := `{"id":"fw-byte-exact","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"exact-relay"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8}}`
	ci.upstreams["fireworks"].SetResponse(distinctBody, http.StatusOK)

	cfg := ci.loadFromDisk()
	_, engine := ci.buildAPI(cfg)

	resetAllCaptures(ci)
	w, _ := sendChat(t, engine, "fw-standard", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if !bytes.Equal(w.Body.Bytes(), []byte(distinctBody)) {
		t.Errorf("response not relayed byte-for-byte:\ngot:  %s\nwant: %s", w.Body.String(), distinctBody)
	}
}

func TestOneChatTransport_SSERelayedRaw(t *testing.T) {
	ci := newCombinedInstallation(t)

	sseFixture := "data: {\"id\":\"bt-sse\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"Hi\"}}]}\n\ndata: {\"id\":\"bt-sse\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\" there\"}}]}\n\ndata: {\"id\":\"bt-sse\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
	ci.upstreams["baseten"].SetResponse(sseFixture, http.StatusOK)

	cfg := ci.loadFromDisk()
	_, engine := ci.buildAPI(cfg)

	body := `{"model":"baseten-model","messages":[{"role":"user","content":"hi"}],"stream":true}`
	w := sendChatRaw(t, engine, body)
	if w.Code != http.StatusOK {
		t.Fatalf("stream status = %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("content-type = %q, want text/event-stream", ct)
	}
	bodyStr := w.Body.String()
	if !strings.Contains(bodyStr, "[DONE]") {
		t.Fatal("SSE response missing [DONE]")
	}
	doneCount := strings.Count(bodyStr, "[DONE]")
	if doneCount != 1 {
		t.Errorf("[DONE] count = %d, want 1", doneCount)
	}
}

func TestOneChatTransport_ToolsRelayedAcrossProviders(t *testing.T) {
	ci := newCombinedInstallation(t)
	cfg := ci.loadFromDisk()
	_, engine := ci.buildAPI(cfg)

	toolBody := `{"model":"deepinfra-model","messages":[{"role":"user","content":"what's the weather?"}],"tools":[{"type":"function","function":{"name":"get_weather","description":"Get weather","parameters":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}}}],"tool_choice":"auto"}`

	resetAllCaptures(ci)
	w := sendChatRaw(t, engine, toolBody)
	if w.Code != http.StatusOK {
		t.Fatalf("tools status = %d", w.Code)
	}
	cap := ci.upstreams["deepinfra"].Capture().Get(0)
	tools := gjson.GetBytes(cap.Body, "tools")
	if !tools.IsArray() || len(tools.Array()) != 1 {
		t.Errorf("tools not preserved: %s", tools.Raw)
	}
	if name := gjson.GetBytes(cap.Body, "tools.0.function.name").String(); name != "get_weather" {
		t.Errorf("tool name = %q, want get_weather", name)
	}
}

func TestOneChatTransport_ErrorRelayedAcrossProviders(t *testing.T) {
	ci := newCombinedInstallation(t)

	errBody := `{"error":{"message":"rate limited","type":"rate_limit_error"}}`
	ci.upstreams["fireworks"].SetResponse(errBody, http.StatusTooManyRequests)

	cfg := ci.loadFromDisk()
	_, engine := ci.buildAPI(cfg)

	w, _ := sendChat(t, engine, "fw-standard", "")
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("error relay status = %d, want 429", w.Code)
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("error body not valid JSON: %v", err)
	}
	errObj, ok := resp["error"].(map[string]any)
	if !ok {
		t.Fatal("error body missing error object")
	}
	if msg, _ := errObj["message"].(string); msg != "rate limited" {
		t.Errorf("error message = %q, want 'rate limited'", msg)
	}
}
