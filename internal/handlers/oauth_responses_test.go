package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"

	"droid-proxy/internal/config"
	"droid-proxy/internal/oauth"
	"droid-proxy/internal/upstream"
)

func newOAuthResponsesTestAPI(t *testing.T, provider config.OAuthProvider, protocol config.UpstreamProtocol, upstreamHandler http.HandlerFunc, mutateToken func(*oauth.Token)) *testAPI {
	t.Helper()
	gin.SetMode(gin.TestMode)
	srv := httptest.NewServer(upstreamHandler)
	t.Cleanup(srv.Close)

	authDir := t.TempDir()
	cfg := &config.Config{
		OAuth: config.OAuth{AuthDir: authDir},
		Upstream: config.Upstream{
			HTTPTimeout:     5 * time.Second,
			StreamKeepAlive: 200 * time.Millisecond,
		},
		Models: []*config.Model{
			{
				Alias:            "droid-oauth",
				DisplayName:      "OAuth Test",
				FactoryProvider:  config.FactoryProviderOpenAI,
				UpstreamProtocol: protocol,
				OAuthProvider:    provider,
				BaseURL:          srv.URL,
				UpstreamModel:    "oauth-upstream-model",
			},
		},
	}
	manager := oauth.NewManager(cfg)
	token := &oauth.Token{
		Type:         string(provider),
		AccessToken:  "oauth-access-token",
		RefreshToken: "oauth-refresh-token",
		Email:        "user@example.com",
		AccountID:    "acct_123",
		Expired:      time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
	}
	if mutateToken != nil {
		mutateToken(token)
	}
	if _, err := manager.SaveToken(token); err != nil {
		t.Fatal(err)
	}

	router, err := upstream.NewRouter(cfg.Models)
	if err != nil {
		t.Fatal(err)
	}
	logger := logrus.New()
	logger.SetOutput(io.Discard)
	api := &API{Cfg: cfg, Router: router, Client: upstream.NewClient(cfg), OAuth: manager, Logger: logger}
	engine := gin.New()
	engine.POST("/v1/responses", api.Responses)
	engine.POST("/responses", api.Responses)
	return &testAPI{api: api, upstream: srv, engine: engine}
}

func TestResponses_OAuthCodexNonStreamReconstructsResponsesAndPreservesTools(t *testing.T) {
	var capturedAuth, capturedAccount, capturedOriginator, capturedPath string
	var capturedBeta, capturedResidency, capturedInstallation, capturedClientRequest, capturedSession, capturedWindow string
	var captured map[string]any
	api := newOAuthResponsesTestAPI(t, config.OAuthProviderCodex, config.UpstreamCodexResponses, func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		capturedAuth = r.Header.Get("Authorization")
		capturedAccount = r.Header.Get("Chatgpt-Account-Id")
		capturedOriginator = r.Header.Get("Originator")
		capturedBeta = r.Header.Get("OpenAI-Beta")
		capturedResidency = r.Header.Get("x-openai-internal-codex-residency")
		capturedInstallation = r.Header.Get("x-codex-installation-id")
		capturedClientRequest = r.Header.Get("x-client-request-id")
		capturedSession = r.Header.Get("session_id")
		capturedWindow = r.Header.Get("x-codex-window-id")
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Fatalf("captured request JSON: %v body=%s", err, body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, frame := range []string{
			`event: response.output_item.done`,
			`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"message","id":"msg_1","status":"completed","role":"assistant","content":[{"type":"output_text","text":"hello"}]}}`,
			``,
			`event: response.output_item.done`,
			`data: {"type":"response.output_item.done","output_index":1,"item":{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{\"q\":\"x\"}"}}`,
			``,
			`event: response.completed`,
			`data: {"type":"response.completed","response":{"id":"resp_1","object":"response","status":"completed","output":[]}}`,
			``,
		} {
			_, _ = fmt.Fprintf(w, "%s\n", frame)
		}
	}, nil)

	body := `{"model":"droid-oauth","input":[{"role":"user","content":"hi"},{"type":"function_call_output","call_id":"call_1","output":"tool ok"}],"stream":false,"previous_response_id":"resp_old","stream_options":{"include_usage":true},"max_output_tokens":16,"reasoning":{"effort":"low"}}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	api.engine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	if capturedPath != "/responses" || capturedAuth != "Bearer oauth-access-token" {
		t.Fatalf("bad oauth upstream routing path=%q auth=%q", capturedPath, capturedAuth)
	}
	if capturedAccount != "acct_123" || capturedOriginator == "" {
		t.Fatalf("missing codex oauth headers account=%q originator=%q", capturedAccount, capturedOriginator)
	}
	if capturedBeta != "responses_websockets=2026-02-06" || capturedResidency != "us" || capturedInstallation == "" {
		t.Fatalf("missing codex identity headers beta=%q residency=%q installation=%q", capturedBeta, capturedResidency, capturedInstallation)
	}
	if capturedClientRequest == "" || capturedSession == "" || capturedWindow != capturedSession+":0" {
		t.Fatalf("bad codex session headers client=%q session=%q window=%q", capturedClientRequest, capturedSession, capturedWindow)
	}
	if captured["model"] != "oauth-upstream-model" || captured["stream"] != true {
		t.Fatalf("bad oauth upstream payload: %#v", captured)
	}
	metadata, ok := captured["client_metadata"].(map[string]any)
	if !ok || metadata["x-codex-installation-id"] != capturedInstallation || metadata["x-codex-window-id"] != capturedWindow {
		t.Fatalf("missing codex client metadata: %#v", captured["client_metadata"])
	}
	if captured["instructions"] != codexDefaultInstructions {
		t.Fatalf("missing default codex instructions: %#v", captured)
	}
	if captured["store"] != false {
		t.Fatalf("codex store must be false: %#v", captured)
	}
	if _, exists := captured["previous_response_id"]; exists {
		t.Fatalf("previous_response_id should be removed for oauth upstream: %#v", captured)
	}
	if _, exists := captured["stream_options"]; exists {
		t.Fatalf("stream_options should be removed for oauth upstream: %#v", captured)
	}
	if _, exists := captured["max_output_tokens"]; exists {
		t.Fatalf("max_output_tokens should be removed for codex oauth upstream: %#v", captured)
	}
	reasoning, ok := captured["reasoning"].(map[string]any)
	if !ok || reasoning["effort"] != "low" {
		t.Fatalf("reasoning should be passed through for codex oauth upstream: %#v", captured)
	}
	if !strings.Contains(fmt.Sprint(captured["input"]), "function_call_output") {
		t.Fatalf("function_call_output was not preserved: %#v", captured["input"])
	}

	var downstream map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &downstream); err != nil {
		t.Fatal(err)
	}
	output := downstream["output"].([]any)
	if len(output) != 2 {
		t.Fatalf("expected reconstructed output items, got %#v", downstream)
	}
	if output[0].(map[string]any)["type"] != "message" || output[1].(map[string]any)["type"] != "function_call" {
		t.Fatalf("bad reconstructed output: %#v", output)
	}
}

func TestResponses_OAuthCodexPreservesCallerInstructions(t *testing.T) {
	var captured map[string]any
	api := newOAuthResponsesTestAPI(t, config.OAuthProviderCodex, config.UpstreamCodexResponses, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Fatalf("captured request JSON: %v body=%s", err, body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintln(w, `event: response.completed`)
		_, _ = fmt.Fprintln(w, `data: {"type":"response.completed","response":{"id":"resp_1","object":"response","status":"completed","output":[]}}`)
		_, _ = fmt.Fprintln(w)
	}, nil)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"droid-oauth","instructions":"Use caller instructions.","input":"hi"}`))
	api.engine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	if captured["instructions"] != "Use caller instructions." {
		t.Fatalf("caller instructions were not preserved: %#v", captured)
	}
	input, ok := captured["input"].([]any)
	if !ok || len(input) != 1 {
		t.Fatalf("string input was not normalized to list: %#v", captured["input"])
	}
	msg, ok := input[0].(map[string]any)
	if !ok || msg["role"] != "user" || msg["content"] != "hi" {
		t.Fatalf("bad normalized input: %#v", captured["input"])
	}
}

func TestResponses_OAuthCodexClientMetadataDoesNotOverwriteCallerValues(t *testing.T) {
	var captured map[string]any
	api := newOAuthResponsesTestAPI(t, config.OAuthProviderCodex, config.UpstreamCodexResponses, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Fatalf("captured request JSON: %v body=%s", err, body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintln(w, `event: response.completed`)
		_, _ = fmt.Fprintln(w, `data: {"type":"response.completed","response":{"id":"resp_1","object":"response","status":"completed","output":[]}}`)
		_, _ = fmt.Fprintln(w)
	}, nil)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"droid-oauth","input":"hi","prompt_cache_key":"session-a","client_metadata":{"x-codex-installation-id":"caller-install","custom":"keep"}}`))
	req.Header.Set("x-codex-turn-state", "turn-1")
	api.engine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	metadata, ok := captured["client_metadata"].(map[string]any)
	if !ok {
		t.Fatalf("missing metadata: %#v", captured)
	}
	if metadata["x-codex-installation-id"] != "caller-install" || metadata["custom"] != "keep" {
		t.Fatalf("caller metadata overwritten: %#v", metadata)
	}
	if metadata["x-codex-window-id"] != "session-a:0" || metadata["x-codex-turn-state"] != "turn-1" {
		t.Fatalf("proxy metadata not merged: %#v", metadata)
	}
}

func TestResponses_OAuthCodexNormalizesFastServiceTier(t *testing.T) {
	for _, tc := range []struct {
		name       string
		configured string
		want       string
	}{
		{name: "fast alias value", configured: "fast", want: "priority"},
		{name: "verified priority value", configured: "priority", want: "priority"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var captured map[string]any
			api := newOAuthResponsesTestAPI(t, config.OAuthProviderCodex, config.UpstreamCodexResponses, func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				if err := json.Unmarshal(body, &captured); err != nil {
					t.Fatalf("captured request JSON: %v body=%s", err, body)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprintln(w, `event: response.completed`)
				_, _ = fmt.Fprintln(w, `data: {"type":"response.completed","response":{"id":"resp_1","object":"response","status":"completed","output":[]}}`)
				_, _ = fmt.Fprintln(w)
			}, nil)
			api.api.Cfg.Models[0].ExtraArgs = map[string]any{"service_tier": tc.configured}

			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"droid-oauth","input":"hi"}`))
			api.engine.ServeHTTP(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
			}
			if captured["service_tier"] != tc.want {
				t.Fatalf("service_tier = %#v, want %q in captured payload %#v", captured["service_tier"], tc.want, captured)
			}
		})
	}
}

func TestResponses_OAuthCodexStreamingNormalizesUpstreamErrorEvent(t *testing.T) {
	api := newOAuthResponsesTestAPI(t, config.OAuthProviderCodex, config.UpstreamCodexResponses, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintln(w, `event: error`)
		_, _ = fmt.Fprintln(w, `data: {"type":"error","code":"invalid_request_error","message":"{\"detail\":\"Unsupported service_tier: fast\"}"}`)
		_, _ = fmt.Fprintln(w)
	}, nil)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"droid-oauth","input":"hi","stream":true}`))
	api.engine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected streaming 200, got %d body=%s", w.Code, w.Body.String())
	}
	events := parseHandlerSSE(t, w.Body.String())
	if len(events) != 1 || events[0].name != "error" {
		t.Fatalf("expected one normalized error event, got %#v body=%s", events, w.Body.String())
	}
	payload := events[0].payload
	if payload["type"] != "error" || payload["code"] != "invalid_request_error" {
		t.Fatalf("bad normalized error payload: %#v", payload)
	}
	if _, ok := payload["sequence_number"].(float64); !ok {
		t.Fatalf("normalized error missing sequence_number: %#v", payload)
	}
	if !strings.Contains(fmt.Sprint(payload["message"]), "Unsupported service_tier: fast") {
		t.Fatalf("normalized error lost upstream message: %#v", payload)
	}
}

func TestResponses_OAuthCodexRecordsQuotaMetadata(t *testing.T) {
	api := newOAuthResponsesTestAPI(t, config.OAuthProviderCodex, config.UpstreamCodexResponses, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("x-codex-primary-used-percent", "99")
		w.Header().Set("x-codex-primary-window-minutes", "300")
		w.Header().Set("x-codex-primary-reset-at", "1893456000")
		_, _ = fmt.Fprintln(w, `event: codex.rate_limits`)
		_, _ = fmt.Fprintln(w, `data: {"type":"codex.rate_limits","rate_limits":{"secondary":{"used_percent":12,"window_minutes":10080,"reset_at":1893542400}}}`)
		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintln(w, `event: response.completed`)
		_, _ = fmt.Fprintln(w, `data: {"type":"response.completed","response":{"id":"resp_1","object":"response","status":"completed","output":[]}}`)
		_, _ = fmt.Fprintln(w)
	}, nil)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"droid-oauth","input":"hi"}`))
	api.engine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	token, err := api.api.OAuth.LoadToken(config.OAuthProviderCodex, "user@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if token.CodexQuota == nil || token.CodexQuota.Primary == nil || token.CodexQuota.Secondary == nil {
		t.Fatalf("quota metadata not persisted: %+v", token)
	}
	if token.RateLimitResetAt != "2030-01-02T00:00:00Z" || token.LastSeenAt == "" {
		t.Fatalf("bad quota timestamps: reset=%q seen=%q quota=%+v", token.RateLimitResetAt, token.LastSeenAt, token.CodexQuota)
	}
}

func TestResponses_OAuthXAIStreamingForwardsSSEAndConversationID(t *testing.T) {
	var capturedAuth, capturedConvID, capturedModelOverride string
	var captured map[string]any
	api := newOAuthResponsesTestAPI(t, config.OAuthProviderXAI, config.UpstreamXAIResponses, func(w http.ResponseWriter, r *http.Request) {
		capturedAuth = r.Header.Get("Authorization")
		capturedConvID = r.Header.Get("x-grok-conv-id")
		capturedModelOverride = r.Header.Get("x-grok-model-override")
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Fatalf("captured request JSON: %v body=%s", err, body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		for _, frame := range []string{
			"event: response.created\n" + `data: {"type":"response.created","response":{"id":"resp_x","status":"in_progress"}}` + "\n\n",
			"event: response.output_text.delta\n" + `data: {"type":"response.output_text.delta","delta":"hi"}` + "\n\n",
			"event: response.completed\n" + `data: {"type":"response.completed","response":{"id":"resp_x","status":"completed"}}` + "\n\n",
		} {
			_, _ = w.Write([]byte(frame))
			flusher.Flush()
		}
	}, nil)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/responses", strings.NewReader(`{"model":"droid-oauth","input":"hi","stream":true,"previous_response_id":"resp_old"}`))
	req.Header.Set("X-Session-ID", "session-123")
	api.engine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	if capturedAuth != "Bearer oauth-access-token" || capturedConvID != "session-123" {
		t.Fatalf("bad xai oauth headers auth=%q conv=%q", capturedAuth, capturedConvID)
	}
	if capturedModelOverride != "" {
		t.Fatalf("api-default xai model override header = %q, want empty", capturedModelOverride)
	}
	if captured["model"] != "oauth-upstream-model" || captured["stream"] != true {
		t.Fatalf("bad xai oauth payload: %#v", captured)
	}
	if _, exists := captured["previous_response_id"]; exists {
		t.Fatalf("xai previous_response_id should be removed: %#v", captured)
	}
	events := parseHandlerSSE(t, w.Body.String())
	assertEventCount(t, events, "response.completed", 1)
	if !strings.Contains(w.Body.String(), `"hi"`) {
		t.Fatalf("stream delta not forwarded: %s", w.Body.String())
	}
}

func TestResponses_OAuthXAIComposerUsesConfiguredBaseURLAndModel(t *testing.T) {
	var capturedPath string
	var captured map[string]any
	api := newOAuthResponsesTestAPI(t, config.OAuthProviderXAI, config.UpstreamXAIResponses, func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Fatalf("captured request JSON: %v body=%s", err, body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintln(w, `event: response.completed`)
		_, _ = fmt.Fprintln(w, `data: {"type":"response.completed","response":{"id":"resp_1","object":"response","status":"completed","output":[]}}`)
		_, _ = fmt.Fprintln(w)
	}, nil)
	api.api.Cfg.Models[0].BaseURL = api.upstream.URL + "/v1"
	api.api.Cfg.Models[0].UpstreamModel = "grok-composer-2.5-fast"

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"droid-oauth","input":"hi","stream":false,"reasoning":{"effort":"high"}}`))
	api.engine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	if capturedPath != "/v1/responses" {
		t.Fatalf("composer upstream path = %q, want /v1/responses", capturedPath)
	}
	if captured["model"] != "grok-composer-2.5-fast" {
		t.Fatalf("composer upstream model not rewritten: %#v", captured)
	}
	if _, exists := captured["reasoning"]; exists {
		t.Fatalf("composer reasoning should be dropped by default: %#v", captured)
	}
}

func TestApplyOAuthResponsesHeaders_XAICLIChatProxyHeaders(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "https://cli-chat-proxy.grok.com/v1/responses", nil)
	model := &config.Model{
		Alias:         "grok-composer-2.5-fast",
		BaseURL:       "https://cli-chat-proxy.grok.com/v1",
		OAuthProvider: config.OAuthProviderXAI,
		UpstreamModel: "grok-composer-2.5-fast",
	}
	token := &oauth.Token{Type: string(config.OAuthProviderXAI), AccessToken: "oauth-access-token"}
	applyOAuthResponsesHeaders(req, http.Header{}, model, token, []byte(`{"model":"grok-composer-2.5-fast"}`), "", "")

	if got := req.Header.Get("x-grok-model-override"); got != "grok-composer-2.5-fast" {
		t.Fatalf("x-grok-model-override = %q, want grok-composer-2.5-fast", got)
	}
	if got := req.Header.Get("x-grok-client-version"); got != xaiGrokClientVersion {
		t.Fatalf("x-grok-client-version = %q, want %q", got, xaiGrokClientVersion)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer oauth-access-token" {
		t.Fatalf("authorization header = %q", got)
	}
}

func TestResponses_OAuthXAISanitizesPayloadForAgentCompatibility(t *testing.T) {
	var captured map[string]any
	api := newOAuthResponsesTestAPI(t, config.OAuthProviderXAI, config.UpstreamXAIResponses, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Fatalf("captured request JSON: %v body=%s", err, body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintln(w, `event: response.completed`)
		_, _ = fmt.Fprintln(w, `data: {"type":"response.completed","response":{"id":"resp_1","object":"response","status":"completed","output":[]}}`)
		_, _ = fmt.Fprintln(w)
	}, nil)

	body := `{
		"model":"droid-oauth",
		"input":[{"role":"user","content":"hi"},{"type":"reasoning","encrypted_content":"ciphertext"}],
		"stream":false,
		"service_tier":"auto",
		"previous_response_id":"resp_old",
		"prompt_cache_retention":"24h",
		"safety_identifier":"user-1",
		"stream_options":{"include_usage":true},
		"reasoning":{"effort":"high"},
		"include":["output_text"],
		"tools":[
			{"type":"namespace","tools":[
				{"type":"function","name":"lookup","parameters":{"type":"object","properties":{"mode":{"type":"string","format":"uri","pattern":"^ok$","enum":["ok","bad/value"]},"empty":{"type":"string","enum":["bad/value"]}}}},
				{"type":"tool_search","name":"tool_search"},
				{"type":"image_generation","name":"image_generation"},
				{"type":"custom","name":"custom_tool","input_schema":{"type":"object","properties":{"url":{"type":"string","format":"uri"}}}},
				{"type":"custom","name":"apply_patch","input_schema":{"type":"object"}}
			]},
			{"type":"web_search_preview","search_context_size":"high","user_location":{"type":"approximate"}}
		]
	}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	req.Header.Set("X-Session-ID", "session-123")
	api.engine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	for _, field := range []string{"service_tier", "reasoning", "previous_response_id", "prompt_cache_retention", "safety_identifier", "stream_options"} {
		if _, exists := captured[field]; exists {
			t.Fatalf("%s should be removed: %#v", field, captured)
		}
	}
	if captured["prompt_cache_key"] != "session-123" {
		t.Fatalf("prompt_cache_key not set from session header: %#v", captured)
	}
	if !containsString(captured["include"], "reasoning.encrypted_content") {
		t.Fatalf("encrypted reasoning include missing: %#v", captured["include"])
	}
	tools, ok := captured["tools"].([]any)
	if !ok || len(tools) != 3 {
		t.Fatalf("expected flattened supported tools only, got %#v", captured["tools"])
	}
	lookup := findCapturedTool(t, tools, "lookup")
	mode := lookup["parameters"].(map[string]any)["properties"].(map[string]any)["mode"].(map[string]any)
	if _, exists := mode["format"]; exists {
		t.Fatalf("format should be stripped from schema: %#v", mode)
	}
	if _, exists := mode["pattern"]; exists {
		t.Fatalf("pattern should be stripped from schema: %#v", mode)
	}
	if enum, ok := mode["enum"].([]any); !ok || len(enum) != 1 || enum[0] != "ok" {
		t.Fatalf("slash enum values should be removed, got %#v", mode["enum"])
	}
	empty := lookup["parameters"].(map[string]any)["properties"].(map[string]any)["empty"].(map[string]any)
	if _, exists := empty["enum"]; exists {
		t.Fatalf("empty enum should be removed: %#v", empty)
	}
	custom := findCapturedTool(t, tools, "custom_tool")
	if custom["type"] != "function" {
		t.Fatalf("custom tool should be converted to function: %#v", custom)
	}
	customURL := custom["parameters"].(map[string]any)["properties"].(map[string]any)["url"].(map[string]any)
	if _, exists := customURL["format"]; exists {
		t.Fatalf("custom tool schema format should be stripped: %#v", customURL)
	}
	webSearch := findCapturedToolByType(t, tools, "web_search_preview")
	if _, exists := webSearch["search_context_size"]; exists {
		t.Fatalf("web search search_context_size should be stripped: %#v", webSearch)
	}
	if _, exists := webSearch["user_location"]; exists {
		t.Fatalf("web search user_location should be stripped: %#v", webSearch)
	}
}

func TestResponses_OAuthXAIPassesFactoryReasoningWhenConfigured(t *testing.T) {
	var captured map[string]any
	api := newOAuthResponsesTestAPI(t, config.OAuthProviderXAI, config.UpstreamXAIResponses, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Fatalf("captured request JSON: %v body=%s", err, body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintln(w, `event: response.completed`)
		_, _ = fmt.Fprintln(w, `data: {"type":"response.completed","response":{"id":"resp_1","object":"response","status":"completed","output":[]}}`)
		_, _ = fmt.Fprintln(w)
	}, nil)
	api.api.Cfg.Models[0].Capabilities.FactoryReasoning = config.FactoryReasoningPassthrough

	body := `{
		"model":"droid-oauth",
		"input":"hi",
		"stream":false,
		"service_tier":"auto",
		"reasoning":{"effort":"high"}
	}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	api.engine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	if _, exists := captured["service_tier"]; exists {
		t.Fatalf("service_tier should still be removed: %#v", captured)
	}
	reasoning, ok := captured["reasoning"].(map[string]any)
	if !ok || reasoning["effort"] != "high" {
		t.Fatalf("reasoning should be passed through when configured: %#v", captured)
	}
	if !containsString(captured["include"], "reasoning.encrypted_content") {
		t.Fatalf("encrypted reasoning include missing: %#v", captured["include"])
	}
}

func TestResponses_OAuthXAISSERepairPatchesSplitCompletedOutputAndDone(t *testing.T) {
	var buf strings.Builder
	framer := responsesSSERepairFramer{outputItemsByIndex: map[int64][]byte{}}
	for _, chunk := range []string{
		"event: response.output_item.done\n",
		`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"message","id":"msg_1","content":[{"type":"output_text","text":"hi"}]}}` + "\n\n",
		"event: response.completed\n" + `data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[]`,
		`}}` + "\n\n",
		"data: [DONE]\n\n",
	} {
		if err := framer.WriteChunk(&buf, []byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}
	if err := framer.Flush(&buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, `"output":[{"type":"message","id":"msg_1"`) {
		t.Fatalf("completed output was not patched from tracked items: %s", out)
	}
	if !strings.Contains(out, "data: [DONE]") {
		t.Fatalf("[DONE] frame should be preserved: %s", out)
	}
}

func TestResponses_OAuthXAIStreamingSurfacesProviderErrorFrame(t *testing.T) {
	api := newOAuthResponsesTestAPI(t, config.OAuthProviderXAI, config.UpstreamXAIResponses, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		_, _ = w.Write([]byte(`data: {"type":"error","error":{"message":"Grok subscription tier required","code":"forbidden"}}` + "\n\n"))
		flusher.Flush()
	}, nil)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/responses", strings.NewReader(`{"model":"droid-oauth","input":"hi","stream":true}`))
	api.engine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "Grok subscription tier required") {
		t.Fatalf("provider error message was not preserved: %s", w.Body.String())
	}
	if strings.Contains(w.Body.String(), "upstream stream ended before terminal marker") {
		t.Fatalf("provider error frame should be terminal, got truncation frame: %s", w.Body.String())
	}
}

func TestResponses_OAuthXAICLIChatProxyVisibilityGuardNonStreaming(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr bool
	}{
		{
			name:    "empty output",
			body:    `data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[]}}` + "\n\n",
			wantErr: true,
		},
		{
			name:    "reasoning only",
			body:    `data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[{"type":"reasoning","content":[{"type":"reasoning_text","text":"internal"}]}]}}` + "\n\n",
			wantErr: true,
		},
		{
			name:    "visible text",
			body:    `data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"hi"}]}]}}` + "\n\n",
			wantErr: false,
		},
		{
			name:    "top-level output text",
			body:    `data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","output_text":"hi","output":[]}}` + "\n\n",
			wantErr: false,
		},
		{
			name:    "tool call",
			body:    `data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{}"}]}}` + "\n\n",
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := responseFromResponsesSSE([]byte(tt.body), responsesSSERepairOptions{RequireVisibleOutput: true})
			if tt.wantErr {
				if err == nil || !strings.Contains(err.Error(), noVisibleOAuthOutputMessage) {
					t.Fatalf("expected no-visible-output error, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("expected success, got %v", err)
			}
		})
	}
}

func TestResponses_OAuthXAICLIChatProxyVisibilityGuardStreaming(t *testing.T) {
	var buf strings.Builder
	framer := responsesSSERepairFramer{outputItemsByIndex: map[int64][]byte{}, requireVisibleOutput: true}
	if err := framer.WriteChunk(&buf, []byte(`data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[]}}`+"\n\n")); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "event: error") || !strings.Contains(out, noVisibleOAuthOutputMessage) {
		t.Fatalf("expected no-visible-output error frame, got %s", out)
	}
}

func TestResponses_OAuthXAIAPIDefaultAllowsEmptyOutput(t *testing.T) {
	_, err := responseFromResponsesSSE([]byte(`data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[]}}`+"\n\n"), responsesSSERepairOptions{})
	if err != nil {
		t.Fatalf("api-default path should not require visible output: %v", err)
	}
}

func TestResponses_OAuthUpstreamErrorMapping(t *testing.T) {
	api := newOAuthResponsesTestAPI(t, config.OAuthProviderCodex, config.UpstreamCodexResponses, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limited","type":"rate_limit_exceeded"}}`))
	}, nil)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"droid-oauth","input":"hi"}`))
	api.engine.ServeHTTP(w, req)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("expected upstream status, got %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "rate_limit_exceeded") {
		t.Fatalf("expected upstream error body preserved, got %s", w.Body.String())
	}
}

func findCapturedTool(t *testing.T, tools []any, name string) map[string]any {
	t.Helper()
	for _, raw := range tools {
		tool, ok := raw.(map[string]any)
		if ok && tool["name"] == name {
			return tool
		}
	}
	t.Fatalf("tool %q not found in %#v", name, tools)
	return nil
}

func findCapturedToolByType(t *testing.T, tools []any, typ string) map[string]any {
	t.Helper()
	for _, raw := range tools {
		tool, ok := raw.(map[string]any)
		if ok && tool["type"] == typ {
			return tool
		}
	}
	t.Fatalf("tool type %q not found in %#v", typ, tools)
	return nil
}

func containsString(value any, want string) bool {
	values, ok := value.([]any)
	if !ok {
		return false
	}
	for _, v := range values {
		if s, ok := v.(string); ok && s == want {
			return true
		}
	}
	return false
}
