package handlers

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
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

	"github.com/trevoraspencer/droid-proxy/internal/config"
	"github.com/trevoraspencer/droid-proxy/internal/upstream"
)

func newAnthropicTestAPI(t *testing.T, upstreamHandler http.HandlerFunc, mutate func(*config.Model)) *testAPI {
	t.Helper()
	gin.SetMode(gin.TestMode)
	srv := httptest.NewServer(upstreamHandler)
	t.Cleanup(srv.Close)

	m := &config.Model{
		Alias:            "droid-claude",
		DisplayName:      "Claude (test)",
		FactoryProvider:  config.FactoryProviderAnthropic,
		UpstreamProtocol: config.UpstreamAnthropicMessages,
		KnownAuth:        "anthropic",
		BaseURL:          srv.URL,
		UpstreamModel:    "claude-test",
		APIKeyEnv:        "DROID_PROXY_ANTHROPIC_KEY",
	}
	if mutate != nil {
		mutate(m)
	}
	if err := config.HydrateModel(m); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DROID_PROXY_ANTHROPIC_KEY", "sk-ant-test")

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
	api := &API{Cfg: cfg, Router: router, Client: upstream.NewClient(cfg), Logger: logger}
	engine := gin.New()
	engine.POST("/v1/messages", api.Messages)
	engine.POST("/messages", api.Messages)
	engine.POST("/v1/messages/count_tokens", api.CountTokens)
	engine.POST("/messages/count_tokens", api.CountTokens)
	return &testAPI{api: api, upstream: srv, engine: engine}
}

func TestMessages_NonStream_NativePassthrough(t *testing.T) {
	upstreamBody := `{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"hello"}]}`
	var seenKey, seenVersion, seenPath string
	api := newAnthropicTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		seenKey = r.Header.Get("x-api-key")
		seenVersion = r.Header.Get("anthropic-version")
		seenPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(upstreamBody))
	}, nil)

	reqBody := `{"model":"droid-claude","max_tokens":256,"messages":[{"role":"user","content":"hi"}]}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(reqBody))
	api.engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	if seenKey != "sk-ant-test" {
		t.Errorf("expected x-api-key forwarded, got %q", seenKey)
	}
	if seenVersion != "2023-06-01" {
		t.Errorf("expected default anthropic-version header, got %q", seenVersion)
	}
	if seenPath != "/v1/messages" {
		t.Errorf("expected upstream path /v1/messages, got %q", seenPath)
	}
	if w.Body.String() != upstreamBody {
		t.Errorf("body mismatch:\nwant=%s\ngot =%s", upstreamBody, w.Body.String())
	}
}

func TestMessages_GzippedNonStreamDecodes(t *testing.T) {
	expected := `{"id":"msg_2","type":"message","content":[{"type":"text","text":"gzipped"}]}`
	api := newAnthropicTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		var buf bytes.Buffer
		gw := gzip.NewWriter(&buf)
		_, _ = gw.Write([]byte(expected))
		_ = gw.Close()
		w.Header().Set("Content-Type", "application/json") // intentionally NOT setting Content-Encoding
		_, _ = w.Write(buf.Bytes())
	}, nil)
	reqBody := `{"model":"droid-claude","messages":[{"role":"user","content":"hi"}]}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(reqBody))
	api.engine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	if w.Body.String() != expected {
		t.Errorf("expected gunzipped body %q, got %q", expected, w.Body.String())
	}
}

func TestMessages_GzipBombIsBoundedAndSecretSafe(t *testing.T) {
	sentinel := "sk-1234567890abcdef"
	api := newAnthropicTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		var buf bytes.Buffer
		gw := gzip.NewWriter(&buf)
		_, _ = gw.Write([]byte(strings.Repeat("x", 64) + sentinel))
		_ = gw.Close()
		w.Header().Set("Content-Type", "application/json") // intentionally no Content-Encoding
		_, _ = w.Write(buf.Bytes())
	}, nil)
	api.api.Cfg.Upstream.ResponseBodyMaxBytes = 32

	reqBody := `{"model":"droid-claude","messages":[{"role":"user","content":"hi"}]}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(reqBody))
	api.engine.ServeHTTP(w, req)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected bounded 502, got %d body=%s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), sentinel) || len(w.Body.Bytes()) > 256 {
		t.Fatalf("gzip bomb leaked or produced unbounded response: %q", w.Body.String())
	}
}

func TestMessages_DownstreamCredentialsDoNotReachUpstream(t *testing.T) {
	var seenAuthorization, seenAPIKey, seenCookie string
	api := newAnthropicTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		seenAuthorization = r.Header.Get("Authorization")
		seenAPIKey = r.Header.Get("x-api-key")
		seenCookie = r.Header.Get("Cookie")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}]}`))
	}, nil)

	reqBody := `{"model":"droid-claude","messages":[{"role":"user","content":"hi"}]}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(reqBody))
	req.Header.Set("Authorization", "Bearer downstream-secret")
	req.Header.Set("x-api-key", "downstream-secret")
	req.Header.Set("Cookie", "apiKey=downstream-secret")
	api.engine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	if seenAPIKey != "sk-ant-test" {
		t.Fatalf("upstream x-api-key = %q, want provider credential only", seenAPIKey)
	}
	if seenAuthorization != "" || seenCookie != "" {
		t.Fatalf("downstream credentials reached upstream: Authorization=%q Cookie=%q", seenAuthorization, seenCookie)
	}
}

func TestMessages_StreamPassthrough(t *testing.T) {
	api := newAnthropicTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		chunks := []string{
			"event: message_start",
			"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_3\",\"role\":\"assistant\"}}",
			"",
			"event: content_block_start",
			"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}",
			"",
			"event: content_block_delta",
			"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}",
			"",
			"event: message_stop",
			"data: {\"type\":\"message_stop\"}",
			"",
		}
		for _, c := range chunks {
			_, _ = fmt.Fprintf(w, "%s\n", c)
			flusher.Flush()
		}
	}, nil)
	reqBody := `{"model":"droid-claude","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(reqBody))
	api.engine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Errorf("expected text/event-stream, got %q", ct)
	}
	for _, fragment := range []string{"message_start", "content_block_delta", `"hi"`, "message_stop"} {
		if !strings.Contains(w.Body.String(), fragment) {
			t.Errorf("expected fragment %s in stream body", fragment)
		}
	}
}

func TestMessages_WrongFactoryProvider(t *testing.T) {
	api := newAnthropicTestAPI(t, nil, func(m *config.Model) {
		m.FactoryProvider = config.FactoryProviderGeneric
		m.UpstreamProtocol = config.UpstreamOpenAIChat
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"droid-claude"}`))
	api.engine.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestMessages_ChatUpstreamTranslatesText(t *testing.T) {
	var capturedPath, capturedAuth string
	var captured map[string]any
	api := newAnthropicTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		capturedAuth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Fatalf("captured request JSON: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chat_msg_1","model":"claude-test","created":123,"choices":[{"index":0,"message":{"role":"assistant","content":"Hello back"},"finish_reason":"stop"}],"usage":{"prompt_tokens":11,"completion_tokens":4,"total_tokens":15}}`))
	}, func(m *config.Model) {
		m.UpstreamProtocol = config.UpstreamOpenAIChat
		m.KnownAuth = ""
		m.ExtraHeaders = nil
	})

	reqBody := `{"model":"droid-claude","system":[{"type":"text","text":"You are helpful."}],"max_tokens":12,"temperature":0.2,"top_p":0.9,"stop_sequences":["END"],"messages":[{"role":"user","content":[{"type":"text","text":"Hello 🌍"},{"type":"text","text":"multiline\ntext"}]},{"role":"assistant","content":"previous answer"}]}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(reqBody))
	api.engine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	if capturedPath != "/chat/completions" || capturedAuth != "Bearer sk-ant-test" {
		t.Fatalf("bad upstream routing path=%q auth=%q", capturedPath, capturedAuth)
	}
	if captured["model"] != "claude-test" || captured["max_tokens"].(float64) != 12 {
		t.Fatalf("bad translated request: %#v", captured)
	}
	if captured["temperature"].(float64) != 0.2 || captured["top_p"].(float64) != 0.9 {
		t.Fatalf("generation parameters were not preserved: %#v", captured)
	}
	if captured["stop"].([]any)[0] != "END" {
		t.Fatalf("stop_sequences did not map to stop: %#v", captured)
	}
	msgs := captured["messages"].([]any)
	if msgs[0].(map[string]any)["role"] != "system" || msgs[0].(map[string]any)["content"] != "You are helpful." {
		t.Fatalf("bad system message: %#v", msgs)
	}
	if msgs[1].(map[string]any)["role"] != "user" || msgs[1].(map[string]any)["content"] != "Hello 🌍\nmultiline\ntext" {
		t.Fatalf("bad user content: %#v", msgs)
	}
	if msgs[2].(map[string]any)["role"] != "assistant" || msgs[2].(map[string]any)["content"] != "previous answer" {
		t.Fatalf("bad assistant content: %#v", msgs)
	}

	var downstream map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &downstream); err != nil {
		t.Fatal(err)
	}
	if _, ok := downstream["choices"]; ok {
		t.Fatalf("raw Chat choices leaked in Anthropic body: %#v", downstream)
	}
	if downstream["type"] != "message" || downstream["role"] != "assistant" || downstream["stop_reason"] != "end_turn" {
		t.Fatalf("bad downstream Anthropic envelope: %#v", downstream)
	}
	content := downstream["content"].([]any)[0].(map[string]any)
	if content["type"] != "text" || content["text"] != "Hello back" {
		t.Fatalf("bad Anthropic text content: %#v", downstream)
	}
	usage := downstream["usage"].(map[string]any)
	if usage["input_tokens"].(float64) != 11 || usage["output_tokens"].(float64) != 4 {
		t.Fatalf("bad usage mapping: %#v", downstream)
	}
}

func TestMessages_ChatUpstreamTranslatesToolsAndFollowup(t *testing.T) {
	var captured []map[string]any
	api := newAnthropicTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("captured request JSON: %v", err)
		}
		captured = append(captured, req)
		w.Header().Set("Content-Type", "application/json")
		if len(captured) == 1 {
			_, _ = w.Write([]byte(`{"id":"chat_1","choices":[{"message":{"role":"assistant","content":null,"tool_calls":[{"id":"toolu_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Paris\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"chat_2","choices":[{"message":{"role":"assistant","content":"It is sunny."},"finish_reason":"stop"}],"usage":{"prompt_tokens":6,"completion_tokens":4,"total_tokens":10}}`))
	}, func(m *config.Model) {
		m.UpstreamProtocol = config.UpstreamOpenAIChat
		m.KnownAuth = ""
		m.ExtraHeaders = nil
	})

	firstBody := `{"model":"droid-claude","messages":[{"role":"user","content":"weather?"}],"max_tokens":64,"tools":[{"name":"get_weather","description":"weather lookup","input_schema":{"type":"object","properties":{"city":{"type":"string"}}}}],"tool_choice":{"type":"tool","name":"get_weather"}}`
	w1 := httptest.NewRecorder()
	api.engine.ServeHTTP(w1, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(firstBody)))
	if w1.Code != http.StatusOK {
		t.Fatalf("first turn expected 200, got %d body=%s", w1.Code, w1.Body.String())
	}
	tools := captured[0]["tools"].([]any)
	fn := tools[0].(map[string]any)["function"].(map[string]any)
	if fn["name"] != "get_weather" || fn["description"] != "weather lookup" {
		t.Fatalf("bad first-turn tool request: %#v", captured[0])
	}
	choice := captured[0]["tool_choice"].(map[string]any)
	if choice["type"] != "function" || choice["function"].(map[string]any)["name"] != "get_weather" {
		t.Fatalf("bad tool choice mapping: %#v", captured[0])
	}
	var firstResp map[string]any
	if err := json.Unmarshal(w1.Body.Bytes(), &firstResp); err != nil {
		t.Fatal(err)
	}
	block := firstResp["content"].([]any)[0].(map[string]any)
	if block["type"] != "tool_use" || block["id"] != "toolu_1" || block["name"] != "get_weather" || firstResp["stop_reason"] != "tool_use" {
		t.Fatalf("bad tool_use response: %#v", firstResp)
	}
	if block["input"].(map[string]any)["city"] != "Paris" {
		t.Fatalf("tool arguments not parsed into input object: %#v", block)
	}

	followBody := `{"model":"droid-claude","messages":[{"role":"user","content":"weather?"},{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"get_weather","input":{"city":"Paris"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"sunny"}]}]}`
	w2 := httptest.NewRecorder()
	api.engine.ServeHTTP(w2, httptest.NewRequest(http.MethodPost, "/messages", strings.NewReader(followBody)))
	if w2.Code != http.StatusOK {
		t.Fatalf("follow-up expected 200, got %d body=%s", w2.Code, w2.Body.String())
	}
	msgs := captured[1]["messages"].([]any)
	asst := msgs[1].(map[string]any)
	if asst["role"] != "assistant" || asst["tool_calls"].([]any)[0].(map[string]any)["id"] != "toolu_1" {
		t.Fatalf("assistant tool-call context not preserved: %#v", msgs)
	}
	tool := msgs[2].(map[string]any)
	if tool["role"] != "tool" || tool["tool_call_id"] != "toolu_1" || tool["content"] != "sunny" {
		t.Fatalf("tool result not preserved: %#v", msgs)
	}
}

func TestMessages_ChatUpstreamTranslatesMultipleToolCalls(t *testing.T) {
	api := newAnthropicTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chat_1","choices":[{"message":{"role":"assistant","content":null,"tool_calls":[{"id":"toolu_1","type":"function","function":{"name":"first","arguments":"{\"n\":1}"}},{"id":"toolu_2","type":"function","function":{"name":"second","arguments":"{\"n\":2}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`))
	}, func(m *config.Model) {
		m.UpstreamProtocol = config.UpstreamOpenAIChat
		m.KnownAuth = ""
		m.ExtraHeaders = nil
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"droid-claude","messages":[{"role":"user","content":"use tools"}]}`))
	api.engine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	blocks := resp["content"].([]any)
	if len(blocks) != 2 {
		t.Fatalf("expected two tool_use blocks, got %#v", blocks)
	}
	for i, want := range []struct {
		id   string
		name string
		n    float64
	}{{"toolu_1", "first", 1}, {"toolu_2", "second", 2}} {
		block := blocks[i].(map[string]any)
		if block["type"] != "tool_use" || block["id"] != want.id || block["name"] != want.name || block["input"].(map[string]any)["n"] != want.n {
			t.Fatalf("block %d not preserved in order: %#v", i, block)
		}
	}
	if resp["stop_reason"] != "tool_use" {
		t.Fatalf("expected stop_reason tool_use, got %#v", resp)
	}
}

func TestMessages_ChatUpstreamUnsupportedToolChoiceIsLocalError(t *testing.T) {
	calls := 0
	api := newAnthropicTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		t.Fatal("upstream must not be called for unsupported local tool_choice")
	}, func(m *config.Model) {
		m.UpstreamProtocol = config.UpstreamOpenAIChat
		m.KnownAuth = ""
		m.ExtraHeaders = nil
	})
	body := `{"model":"droid-claude","messages":[{"role":"user","content":"hi"}],"tool_choice":{"type":"none"}}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	api.engine.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", w.Code, w.Body.String())
	}
	if calls != 0 {
		t.Fatalf("upstream was called %d times", calls)
	}
	if !strings.Contains(w.Body.String(), "unsupported Anthropic tool_choice") {
		t.Fatalf("expected clear local error, got %s", w.Body.String())
	}
}

func TestMessages_ChatUpstreamTranslatedStreamTextAndTool(t *testing.T) {
	var captured map[string]any
	api := newAnthropicTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Fatalf("captured request JSON: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		for _, chunk := range []string{
			`data: {"id":"chat_1","choices":[{"index":0,"delta":{"role":"assistant","content":"Hi"},"finish_reason":null}]}` + "\n\n",
			`data: {"id":"chat_1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"toolu_1","type":"function","function":{"name":"lookup","arguments":"{\"q\""}}]},"finish_reason":null}]}` + "\n\n",
			`data: {"id":"chat_1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":":\"x\"}"}}]},"finish_reason":"tool_calls"}]}` + "\n\n",
			"data: [DONE]\n\n",
		} {
			_, _ = w.Write([]byte(chunk))
			flusher.Flush()
		}
	}, func(m *config.Model) {
		m.UpstreamProtocol = config.UpstreamOpenAIChat
		m.KnownAuth = ""
		m.ExtraHeaders = nil
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/messages", strings.NewReader(`{"model":"droid-claude","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	api.engine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	if captured["stream"] != true {
		t.Fatalf("translated upstream request did not set stream=true: %#v", captured)
	}
	body := w.Body.String()
	if strings.Contains(body, "[DONE]") || strings.Contains(body, `"choices"`) {
		t.Fatalf("raw Chat stream leaked downstream:\n%s", body)
	}
	events := parseHandlerSSE(t, body)
	assertNoRawChatSSE(t, events)
	assertEventCount(t, events, "message_stop", 1)
	assertEventCount(t, events, "error", 0)
	var textIndex, toolIndex float64 = -1, -1
	for _, ev := range events {
		if ev.name != "content_block_start" {
			continue
		}
		block := ev.payload["content_block"].(map[string]any)
		if block["type"] == "text" {
			textIndex = ev.payload["index"].(float64)
		}
		if block["type"] == "tool_use" {
			toolIndex = ev.payload["index"].(float64)
		}
	}
	if textIndex != 0 || toolIndex <= 0 || toolIndex == textIndex {
		t.Fatalf("expected text block index 0 and distinct tool block index, got text=%v tool=%v events=%#v", textIndex, toolIndex, events)
	}
}

func TestMessages_ChatUpstreamTranslatedStreamIsIncrementalAndCancels(t *testing.T) {
	firstSent := make(chan struct{})
	upstreamCanceled := make(chan struct{})
	api := newAnthropicTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		defer close(upstreamCanceled)
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		_, _ = w.Write([]byte(`data: {"id":"chat_inc","choices":[{"index":0,"delta":{"content":"Hel"},"finish_reason":null}]}` + "\n\n"))
		flusher.Flush()
		close(firstSent)
		<-r.Context().Done()
	}, func(m *config.Model) {
		m.UpstreamProtocol = config.UpstreamOpenAIChat
		m.KnownAuth = ""
		m.ExtraHeaders = nil
	})
	srv := httptest.NewServer(api.engine)
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/v1/messages", strings.NewReader(`{"model":"droid-claude","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	<-firstSent
	reader := bufio.NewReader(resp.Body)
	gotDelta := false
	deadline := time.After(500 * time.Millisecond)
	for !gotDelta {
		select {
		case <-deadline:
			t.Fatal("translated Anthropic stream buffered upstream body instead of forwarding first event")
		default:
		}
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(line, "content_block_delta") || strings.Contains(line, `"Hel"`) {
			gotDelta = true
		}
	}
	cancel()
	select {
	case <-upstreamCanceled:
	case <-time.After(time.Second):
		t.Fatal("upstream did not observe cancellation for translated Anthropic stream")
	}
}

func TestMessages_ChatUpstreamTranslatedStreamIdleTimeoutEmitsErrorAndCancels(t *testing.T) {
	upstreamCanceled := make(chan struct{})
	api := newAnthropicTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		defer close(upstreamCanceled)
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		_, _ = w.Write([]byte(`data: {"id":"chat_idle","choices":[{"index":0,"delta":{"content":"Hel"},"finish_reason":null}]}` + "\n\n"))
		flusher.Flush()
		<-r.Context().Done()
	}, func(m *config.Model) {
		m.UpstreamProtocol = config.UpstreamOpenAIChat
	})
	api.api.Cfg.Upstream.HTTPTimeout = 40 * time.Millisecond
	api.api.Cfg.Upstream.StreamKeepAlive = 10 * time.Millisecond
	srv := httptest.NewServer(api.engine)
	defer srv.Close()

	resp, err := srv.Client().Post(srv.URL+"/v1/messages", "application/json", strings.NewReader(`{"model":"droid-claude","stream":true,"max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	body := string(bodyBytes)
	if !strings.Contains(body, ": keep-alive\n\n") {
		t.Fatalf("expected keepalive comment during translated idle stream, got %q", body)
	}
	events := parseHandlerSSE(t, body)
	assertNoRawChatSSE(t, events)
	assertEventCount(t, events, "message_stop", 0)
	assertEventCount(t, events, "error", 1)
	select {
	case <-upstreamCanceled:
	case <-time.After(time.Second):
		t.Fatal("upstream did not observe cancellation after translated Anthropic idle timeout")
	}
}

func TestMessages_ChatUpstreamStreamRejectsMalformedToolArguments(t *testing.T) {
	api := newAnthropicTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		for _, chunk := range []string{
			`data: {"id":"chat_1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"toolu_1","type":"function","function":{"name":"lookup","arguments":"{\"q\""}}]},"finish_reason":null}]}` + "\n\n",
			`data: {"id":"chat_1","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}` + "\n\n",
			"data: [DONE]\n\n",
		} {
			_, _ = w.Write([]byte(chunk))
			flusher.Flush()
		}
	}, func(m *config.Model) {
		m.UpstreamProtocol = config.UpstreamOpenAIChat
		m.KnownAuth = ""
		m.ExtraHeaders = nil
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/messages", strings.NewReader(`{"model":"droid-claude","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	api.engine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 SSE error, got %d body=%s", w.Code, w.Body.String())
	}
	events := parseHandlerSSE(t, w.Body.String())
	assertEventCount(t, events, "error", 1)
	assertEventCount(t, events, "message_stop", 0)
	assertNoRawChatSSE(t, events)
}

func TestMessages_ChatUpstreamErrorsAndLocalRejectsUseAnthropicShape(t *testing.T) {
	t.Run("upstream error", func(t *testing.T) {
		api := newAnthropicTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"rate limited","code":"rate_limit_exceeded"}}`))
		}, func(m *config.Model) {
			m.UpstreamProtocol = config.UpstreamOpenAIChat
			m.KnownAuth = ""
			m.ExtraHeaders = nil
		})
		w := httptest.NewRecorder()
		api.engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"droid-claude","messages":[{"role":"user","content":"hi"}]}`)))
		if w.Code != http.StatusTooManyRequests {
			t.Fatalf("expected 429, got %d body=%s", w.Code, w.Body.String())
		}
		var out map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		if out["type"] != "error" || !strings.Contains(fmt.Sprint(out["error"]), "rate limited") {
			t.Fatalf("bad Anthropic error envelope: %#v", out)
		}
	})

	t.Run("local reject no upstream", func(t *testing.T) {
		calls := 0
		api := newAnthropicTestAPI(t, func(http.ResponseWriter, *http.Request) {
			calls++
		}, func(m *config.Model) {
			m.UpstreamProtocol = config.UpstreamOpenAIChat
			m.KnownAuth = ""
			m.ExtraHeaders = nil
		})
		w := httptest.NewRecorder()
		api.engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/messages", strings.NewReader(`{"model":"droid-claude","thinking":{},"messages":[{"role":"user","content":"hi"}]}`)))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d body=%s", w.Code, w.Body.String())
		}
		if calls != 0 {
			t.Fatalf("upstream was called %d times", calls)
		}
		if !strings.Contains(w.Body.String(), `"type":"error"`) {
			t.Fatalf("expected Anthropic error shape, got %s", w.Body.String())
		}
	})
}

func TestCountTokens_NativeForward(t *testing.T) {
	upstreamBody := `{"input_tokens":1234}`
	var seenPath string
	api := newAnthropicTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		seenPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(upstreamBody))
	}, nil)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens",
		strings.NewReader(`{"model":"droid-claude","messages":[{"role":"user","content":"hi"}]}`))
	api.engine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	if seenPath != "/v1/messages/count_tokens" {
		t.Errorf("expected upstream path /v1/messages/count_tokens, got %q", seenPath)
	}
	if w.Body.String() != upstreamBody {
		t.Errorf("body mismatch: %s", w.Body.String())
	}
}

func TestCountTokens_LocalFallback(t *testing.T) {
	// Configure a model whose upstream is openai-chat — local count should fire.
	calls := 0
	api := newAnthropicTestAPI(t, func(http.ResponseWriter, *http.Request) {
		calls++
		t.Fatal("upstream must not be called when local count is used")
	}, func(m *config.Model) {
		m.UpstreamProtocol = config.UpstreamOpenAIChat
	})
	body := `{"model":"droid-claude","system":"You are helpful.","messages":[{"role":"user","content":"hello world"}]}`
	for _, path := range []string{"/v1/messages/count_tokens", "/messages/count_tokens"} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		api.engine.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("%s expected 200, got %d body=%s", path, w.Code, w.Body.String())
		}
		var out map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		n, _ := out["input_tokens"].(float64)
		if n <= 0 {
			t.Errorf("%s expected input_tokens > 0, got %v", path, out["input_tokens"])
		}
	}
	if calls != 0 {
		t.Fatalf("upstream was called %d times", calls)
	}
}

func TestCountTokens_LocalFallbackRejectsBadRequestsWithoutUpstream(t *testing.T) {
	calls := 0
	api := newAnthropicTestAPI(t, func(http.ResponseWriter, *http.Request) {
		calls++
		t.Fatal("upstream must not be called for local count_tokens errors")
	}, func(m *config.Model) {
		m.UpstreamProtocol = config.UpstreamOpenAIChat
	})

	for _, body := range []string{
		`{"messages":[{"role":"user","content":"hi"}]}`,
		`{"model":"missing-model","messages":[{"role":"user","content":"hi"}]}`,
	} {
		w := httptest.NewRecorder()
		api.engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", strings.NewReader(body)))
		if w.Code < 400 {
			t.Fatalf("expected error for body %s, got %d body=%s", body, w.Code, w.Body.String())
		}
	}
	if calls != 0 {
		t.Fatalf("upstream was called %d times", calls)
	}
}

func TestCountTokens_LocalFallbackWrongProviderRejectsWithoutUpstream(t *testing.T) {
	calls := 0
	api := newAnthropicTestAPI(t, func(http.ResponseWriter, *http.Request) {
		calls++
		t.Fatal("upstream must not be called for wrong provider count_tokens")
	}, func(m *config.Model) {
		m.FactoryProvider = config.FactoryProviderOpenAI
		m.UpstreamProtocol = config.UpstreamOpenAIChat
	})
	w := httptest.NewRecorder()
	api.engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/messages/count_tokens", strings.NewReader(`{"model":"droid-claude","messages":[{"role":"user","content":"hi"}]}`)))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", w.Code, w.Body.String())
	}
	if calls != 0 {
		t.Fatalf("upstream was called %d times", calls)
	}
}
