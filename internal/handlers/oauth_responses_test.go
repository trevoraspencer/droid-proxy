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
	var captured map[string]any
	api := newOAuthResponsesTestAPI(t, config.OAuthProviderCodex, config.UpstreamCodexResponses, func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		capturedAuth = r.Header.Get("Authorization")
		capturedAccount = r.Header.Get("Chatgpt-Account-Id")
		capturedOriginator = r.Header.Get("Originator")
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

	body := `{"model":"droid-oauth","input":[{"role":"user","content":"hi"},{"type":"function_call_output","call_id":"call_1","output":"tool ok"}],"stream":false,"previous_response_id":"resp_old","stream_options":{"include_usage":true}}`
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
	if captured["model"] != "oauth-upstream-model" || captured["stream"] != true {
		t.Fatalf("bad oauth upstream payload: %#v", captured)
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

func TestResponses_OAuthXAIStreamingForwardsSSEAndConversationID(t *testing.T) {
	var capturedAuth, capturedConvID string
	var captured map[string]any
	api := newOAuthResponsesTestAPI(t, config.OAuthProviderXAI, config.UpstreamXAIResponses, func(w http.ResponseWriter, r *http.Request) {
		capturedAuth = r.Header.Get("Authorization")
		capturedConvID = r.Header.Get("x-grok-conv-id")
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
