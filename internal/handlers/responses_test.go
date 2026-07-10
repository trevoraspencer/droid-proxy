package handlers

import (
	"bufio"
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

func newResponsesTestAPI(t *testing.T, upstreamHandler http.HandlerFunc, mutate func(*config.Model)) *testAPI {
	t.Helper()
	gin.SetMode(gin.TestMode)
	srv := httptest.NewServer(upstreamHandler)
	t.Cleanup(srv.Close)

	m := &config.Model{
		Alias:            "droid-gpt",
		DisplayName:      "GPT (test)",
		FactoryProvider:  config.FactoryProviderOpenAI,
		UpstreamProtocol: config.UpstreamOpenAIResponses,
		BaseURL:          srv.URL,
		UpstreamModel:    "gpt-test",
		APIKeyEnv:        "DROID_PROXY_OPENAI_KEY",
	}
	if mutate != nil {
		mutate(m)
	}
	if err := config.HydrateModel(m); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DROID_PROXY_OPENAI_KEY", "sk-test")

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
	engine.POST("/v1/responses", api.Responses)
	engine.POST("/responses", api.Responses)
	return &testAPI{api: api, upstream: srv, engine: engine}
}

func TestResponses_NonStream_NativePassthrough(t *testing.T) {
	upstreamBody := `{"id":"resp_1","object":"response","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"hi"}]}]}`
	var seenAuth, seenModel string
	api := newResponsesTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		seenAuth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		seenModel = string(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(upstreamBody))
	}, nil)

	reqBody := `{"model":"droid-gpt","input":"hi","prompt_cache_options":{"mode":"explicit","ttl":"30m"}}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(reqBody))
	api.engine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	if seenAuth != "Bearer sk-test" {
		t.Errorf("upstream Authorization: %q", seenAuth)
	}
	if !strings.Contains(seenModel, "gpt-test") {
		t.Errorf("expected upstream model rewritten, got body=%s", seenModel)
	}
	var seenPayload map[string]any
	if err := json.Unmarshal([]byte(seenModel), &seenPayload); err != nil {
		t.Fatalf("decode native upstream payload: %v", err)
	}
	cache, ok := seenPayload["prompt_cache_options"].(map[string]any)
	if !ok || cache["mode"] != "explicit" || cache["ttl"] != "30m" {
		t.Fatalf("public non-OAuth prompt_cache_options changed: %#v", seenPayload)
	}
	if w.Body.String() != upstreamBody {
		t.Errorf("body mismatch:\nwant=%s\ngot =%s", upstreamBody, w.Body.String())
	}
}

func TestResponses_NativeAndTranslatedUpstreamBodiesAreCapped(t *testing.T) {
	for _, tc := range []struct {
		name     string
		protocol config.UpstreamProtocol
	}{
		{name: "native", protocol: config.UpstreamOpenAIResponses},
		{name: "translated", protocol: config.UpstreamOpenAIChat},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sentinel := "sk-1234567890abcdef"
			api := newResponsesTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(strings.Repeat("x", 17) + sentinel))
			}, func(m *config.Model) {
				m.UpstreamProtocol = tc.protocol
			})
			api.api.Cfg.Upstream.ResponseBodyMaxBytes = 16

			reqBody := `{"model":"droid-gpt","input":"hi"}`
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(reqBody))
			api.engine.ServeHTTP(w, req)
			if w.Code != http.StatusBadGateway {
				t.Fatalf("expected bounded 502, got %d body=%s", w.Code, w.Body.String())
			}
			if strings.Contains(w.Body.String(), sentinel) {
				t.Fatalf("oversized upstream body leaked downstream: %q", w.Body.String())
			}
		})
	}
}

func TestResponses_StreamPassthrough(t *testing.T) {
	api := newResponsesTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		chunks := []string{
			`event: response.created`,
			`data: {"type":"response.created","response":{"id":"resp_x","status":"in_progress"}}`,
			``,
			`event: response.output_text.delta`,
			`data: {"type":"response.output_text.delta","delta":"hi"}`,
			``,
			`event: response.completed`,
			`data: {"type":"response.completed","response":{"id":"resp_x","status":"completed"}}`,
			``,
		}
		for _, c := range chunks {
			_, _ = fmt.Fprintf(w, "%s\n", c)
			flusher.Flush()
		}
	}, nil)

	reqBody := `{"model":"droid-gpt","input":"hi","stream":true}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(reqBody))
	api.engine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	for _, fragment := range []string{"response.created", "output_text.delta", "response.completed", `"hi"`} {
		if !strings.Contains(w.Body.String(), fragment) {
			t.Errorf("missing fragment %s in stream", fragment)
		}
	}
	events := parseHandlerSSE(t, w.Body.String())
	var completed map[string]any
	for _, ev := range events {
		if ev.name == "response.completed" {
			completed = ev.payload
		}
	}
	if completed == nil {
		t.Fatalf("missing response.completed event in %#v", events)
	}
	response := completed["response"].(map[string]any)
	if _, repaired := response["output"]; repaired {
		t.Fatalf("native Responses stream should pass through omitted final output without repair, got %#v", response)
	}
}

func TestResponses_StreamErrorBeforeBody(t *testing.T) {
	// Upstream returns 429 with an OpenAI-shaped error body BEFORE the SSE starts.
	// The proxy should emit a single SSE error chunk so the client's stream parser
	// doesn't choke on a non-SSE response.
	api := newResponsesTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"slow down","code":"rate_limit_exceeded"}}`))
	}, nil)
	reqBody := `{"model":"droid-gpt","input":"hi","stream":true}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(reqBody))
	api.engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 (SSE preamble), got %d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "event: error") {
		t.Errorf("expected event: error frame in body=%s", body)
	}
	// extract the JSON payload from the SSE frame
	idx := strings.Index(body, "data:")
	if idx < 0 {
		t.Fatalf("no data: line in body=%s", body)
	}
	rest := strings.TrimSpace(body[idx+len("data:"):])
	if newlineIdx := strings.Index(rest, "\n"); newlineIdx > 0 {
		rest = rest[:newlineIdx]
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(rest), &parsed); err != nil {
		t.Fatalf("error chunk is not valid JSON: %v\nchunk=%s", err, rest)
	}
	if parsed["type"] != "error" {
		t.Errorf("expected type=error, got %v", parsed["type"])
	}
	if !strings.Contains(fmt.Sprint(parsed["message"]), "slow down") {
		t.Errorf("expected message preserved, got %v", parsed["message"])
	}
}

func TestResponses_WrongFactoryProvider(t *testing.T) {
	api := newResponsesTestAPI(t, nil, func(m *config.Model) {
		m.FactoryProvider = config.FactoryProviderGeneric
		m.UpstreamProtocol = config.UpstreamOpenAIChat
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"droid-gpt"}`))
	api.engine.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestResponses_ChatUpstreamTranslatesText(t *testing.T) {
	var capturedPath, capturedAuth string
	var captured map[string]any
	api := newResponsesTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		capturedAuth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Fatalf("captured request JSON: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chat_1","model":"gpt-test","created":123,"choices":[{"index":0,"message":{"role":"assistant","content":"Hello back"},"finish_reason":"stop"}],"usage":{"prompt_tokens":9,"completion_tokens":4,"total_tokens":13}}`))
	}, func(m *config.Model) {
		m.UpstreamProtocol = config.UpstreamOpenAIChat
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"droid-gpt","instructions":"sys","input":"Hello 🌍","max_output_tokens":12}`))
	api.engine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	if capturedPath != "/chat/completions" || capturedAuth != "Bearer sk-test" {
		t.Fatalf("bad upstream routing path=%q auth=%q", capturedPath, capturedAuth)
	}
	if captured["model"] != "gpt-test" || captured["max_tokens"].(float64) != 12 {
		t.Fatalf("bad translated request: %#v", captured)
	}
	msgs := captured["messages"].([]any)
	if msgs[0].(map[string]any)["role"] != "system" || msgs[1].(map[string]any)["content"] != "Hello 🌍" {
		t.Fatalf("bad messages: %#v", msgs)
	}
	var downstream map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &downstream); err != nil {
		t.Fatal(err)
	}
	if _, ok := downstream["choices"]; ok {
		t.Fatalf("raw Chat choices leaked in Responses body: %#v", downstream)
	}
	output := downstream["output"].([]any)[0].(map[string]any)
	text := output["content"].([]any)[0].(map[string]any)["text"]
	if downstream["status"] != "completed" || text != "Hello back" {
		t.Fatalf("bad downstream Responses body: %#v", downstream)
	}
}

func TestResponses_ChatUpstreamTranslatesToolsAndFollowup(t *testing.T) {
	var captured []map[string]any
	api := newResponsesTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("captured request JSON: %v", err)
		}
		captured = append(captured, req)
		w.Header().Set("Content-Type", "application/json")
		if len(captured) == 1 {
			_, _ = w.Write([]byte(`{"id":"chat_1","choices":[{"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Paris\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"chat_2","choices":[{"message":{"role":"assistant","content":"It is sunny."},"finish_reason":"stop"}],"usage":{"prompt_tokens":6,"completion_tokens":4,"total_tokens":10}}`))
	}, func(m *config.Model) {
		m.UpstreamProtocol = config.UpstreamOpenAIChat
	})

	firstBody := `{"model":"droid-gpt","input":"weather?","tools":[{"type":"function","name":"get_weather","parameters":{"type":"object","properties":{"city":{"type":"string"}}},"strict":true}],"tool_choice":"auto"}`
	w1 := httptest.NewRecorder()
	api.engine.ServeHTTP(w1, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(firstBody)))
	if w1.Code != http.StatusOK {
		t.Fatalf("first turn expected 200, got %d body=%s", w1.Code, w1.Body.String())
	}
	tools := captured[0]["tools"].([]any)
	if tools[0].(map[string]any)["function"].(map[string]any)["strict"] != true || captured[0]["tool_choice"] != "auto" {
		t.Fatalf("bad first-turn tool request: %#v", captured[0])
	}
	var firstResp map[string]any
	if err := json.Unmarshal(w1.Body.Bytes(), &firstResp); err != nil {
		t.Fatal(err)
	}
	call := firstResp["output"].([]any)[0].(map[string]any)
	if call["type"] != "function_call" || call["call_id"] != "call_1" || call["name"] != "get_weather" {
		t.Fatalf("bad function_call response: %#v", firstResp)
	}

	followBody := `{"model":"droid-gpt","input":[{"role":"user","content":"weather?"},{"type":"function_call","call_id":"call_1","name":"get_weather","arguments":"{\"city\":\"Paris\"}"},{"type":"function_call_output","call_id":"call_1","output":"sunny"}]}`
	w2 := httptest.NewRecorder()
	api.engine.ServeHTTP(w2, httptest.NewRequest(http.MethodPost, "/responses", strings.NewReader(followBody)))
	if w2.Code != http.StatusOK {
		t.Fatalf("follow-up expected 200, got %d body=%s", w2.Code, w2.Body.String())
	}
	msgs := captured[1]["messages"].([]any)
	asst := msgs[1].(map[string]any)
	if asst["role"] != "assistant" || asst["tool_calls"].([]any)[0].(map[string]any)["id"] != "call_1" {
		t.Fatalf("assistant tool-call context not preserved: %#v", msgs)
	}
	tool := msgs[2].(map[string]any)
	if tool["role"] != "tool" || tool["tool_call_id"] != "call_1" || tool["content"] != "sunny" {
		t.Fatalf("tool result not preserved: %#v", msgs)
	}
}

func TestResponses_ChatUpstreamTranslatedStreamTextAndTool(t *testing.T) {
	var captured map[string]any
	api := newResponsesTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Fatalf("captured request JSON: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		for _, chunk := range []string{
			`data: {"id":"chat_1","choices":[{"index":0,"delta":{"role":"assistant","content":"Hel"},"finish_reason":null}]}` + "\n\n",
			`data: {"id":"chat_1","choices":[{"index":0,"delta":{"content":"lo"},"finish_reason":null}]}` + "\n\n",
			`data: {"id":"chat_1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{\"q\""}}]},"finish_reason":null}]}` + "\n\n",
			`data: {"id":"chat_1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":":\"x\"}"}}]},"finish_reason":"tool_calls"}]}` + "\n\n",
			"data: [DONE]\n\n",
		} {
			_, _ = w.Write([]byte(chunk))
			flusher.Flush()
		}
	}, func(m *config.Model) {
		m.UpstreamProtocol = config.UpstreamOpenAIChat
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/responses", strings.NewReader(`{"model":"droid-gpt","input":"hi","stream":true}`))
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
	assertEventCount(t, events, "response.completed", 1)
	assertEventCount(t, events, "response.output_text.done", 1)
	assertEventCount(t, events, "response.function_call_arguments.done", 1)
	assertEventCount(t, events, "response.output_item.done", 2)
	assertEventCount(t, events, "error", 0)
	var textIndex, toolIndex float64 = -1, -1
	var completedOutput []any
	for _, ev := range events {
		switch ev.name {
		case "response.output_text.delta":
			textIndex = ev.payload["output_index"].(float64)
		case "response.output_item.added":
			item := ev.payload["item"].(map[string]any)
			if item["type"] == "function_call" {
				toolIndex = ev.payload["output_index"].(float64)
			}
		case "response.completed":
			completedOutput = ev.payload["response"].(map[string]any)["output"].([]any)
		}
	}
	if textIndex != 0 || toolIndex <= 0 || toolIndex == textIndex {
		t.Fatalf("expected text output_index 0 and distinct tool output index, got text=%v tool=%v events=%#v", textIndex, toolIndex, events)
	}
	if len(completedOutput) != 2 {
		t.Fatalf("expected completed output to include text and tool items, got %#v", completedOutput)
	}
	textPart := completedOutput[0].(map[string]any)["content"].([]any)[0].(map[string]any)
	toolItem := completedOutput[1].(map[string]any)
	if textPart["text"] != "Hello" || toolItem["call_id"] != "call_1" || toolItem["arguments"] != `{"q":"x"}` {
		t.Fatalf("completed output lost translated content: %#v", completedOutput)
	}
}

func TestResponses_ChatUpstreamTranslatedStreamIsIncrementalAndCancels(t *testing.T) {
	firstSent := make(chan struct{})
	release := make(chan struct{})
	upstreamCanceled := make(chan struct{})
	api := newResponsesTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		defer close(upstreamCanceled)
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		_, _ = w.Write([]byte(`data: {"id":"chat_inc","choices":[{"index":0,"delta":{"content":"Hel"},"finish_reason":null}]}` + "\n\n"))
		flusher.Flush()
		close(firstSent)
		select {
		case <-release:
			_, _ = w.Write([]byte(`data: {"id":"chat_inc","choices":[{"index":0,"delta":{"content":"lo"},"finish_reason":"stop"}]}` + "\n\n" + "data: [DONE]\n\n"))
			flusher.Flush()
		case <-r.Context().Done():
		}
	}, func(m *config.Model) {
		m.UpstreamProtocol = config.UpstreamOpenAIChat
	})
	srv := httptest.NewServer(api.engine)
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/v1/responses", strings.NewReader(`{"model":"droid-gpt","input":"hi","stream":true}`))
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
			t.Fatal("translated Responses stream buffered upstream body instead of forwarding first event")
		default:
		}
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(line, "response.output_text.delta") || strings.Contains(line, `"Hel"`) {
			gotDelta = true
		}
	}
	cancel()
	select {
	case <-upstreamCanceled:
	case <-time.After(time.Second):
		t.Fatal("upstream did not observe cancellation for translated Responses stream")
	}
}

func TestResponses_ChatUpstreamTranslatedStreamIdleTimeoutEmitsErrorAndCancels(t *testing.T) {
	upstreamCanceled := make(chan struct{})
	api := newResponsesTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
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

	resp, err := srv.Client().Post(srv.URL+"/v1/responses", "application/json", strings.NewReader(`{"model":"droid-gpt","input":"hi","stream":true}`))
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
	assertEventCount(t, events, "response.completed", 0)
	assertEventCount(t, events, "error", 1)
	select {
	case <-upstreamCanceled:
	case <-time.After(time.Second):
		t.Fatal("upstream did not observe cancellation after translated Responses idle timeout")
	}
}

func TestResponses_ChatUpstreamStreamRejectsMalformedToolArguments(t *testing.T) {
	api := newResponsesTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		for _, chunk := range []string{
			`data: {"id":"chat_1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{\"q\""}}]},"finish_reason":null}]}` + "\n\n",
			`data: {"id":"chat_1","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}` + "\n\n",
			"data: [DONE]\n\n",
		} {
			_, _ = w.Write([]byte(chunk))
			flusher.Flush()
		}
	}, func(m *config.Model) {
		m.UpstreamProtocol = config.UpstreamOpenAIChat
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"droid-gpt","input":"hi","stream":true}`))
	api.engine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 SSE error, got %d body=%s", w.Code, w.Body.String())
	}
	events := parseHandlerSSE(t, w.Body.String())
	assertEventCount(t, events, "error", 1)
	assertEventCount(t, events, "response.completed", 0)
	assertNoRawChatSSE(t, events)
}

func TestResponses_ChatUpstreamLocalErrorsDoNotCallUpstreamAndUseEnvelope(t *testing.T) {
	calls := 0
	api := newResponsesTestAPI(t, func(http.ResponseWriter, *http.Request) {
		calls++
	}, func(m *config.Model) {
		m.UpstreamProtocol = config.UpstreamOpenAIChat
	})
	cases := []string{
		`{"model":"droid-gpt","previous_response_id":"resp_old","input":"hi"}`,
		`{"model":"droid-gpt","input":[{"role":"user","content":[{"type":"input_image","image_url":"x"}]}]}`,
		`{"model":"droid-gpt","input":"hi","tools":[{"type":"web_search_preview","name":"web"}]}`,
		`{"model":"droid-gpt","input":"hi","tool_choice":{"type":"file_search"}}`,
	}
	for _, body := range cases {
		w := httptest.NewRecorder()
		api.engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body)))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("expected local 400, got %d body=%s", w.Code, w.Body.String())
		}
		var out map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("error body is not JSON: %v", err)
		}
		if out["error"].(map[string]any)["message"] == "" {
			t.Fatalf("empty error message: %#v", out)
		}
	}
	if calls != 0 {
		t.Fatalf("upstream was called %d times for local errors", calls)
	}
}

func TestResponses_ChatUpstreamUnsupportedToolChoiceIsLocalError(t *testing.T) {
	calls := 0
	api := newResponsesTestAPI(t, func(http.ResponseWriter, *http.Request) {
		calls++
		t.Fatal("upstream must not be called for unsupported Responses tool_choice")
	}, func(m *config.Model) {
		m.UpstreamProtocol = config.UpstreamOpenAIChat
	})
	for _, body := range []string{
		`{"model":"droid-gpt","input":"hi","tool_choice":"banana"}`,
		`{"model":"droid-gpt","input":"hi","tool_choice":{"type":"file_search"}}`,
	} {
		w := httptest.NewRecorder()
		api.engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/responses", strings.NewReader(body)))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d body=%s", w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), "unsupported tool_choice") {
			t.Fatalf("expected clear unsupported tool_choice error, got %s", w.Body.String())
		}
	}
	if calls != 0 {
		t.Fatalf("upstream was called %d times", calls)
	}
}

func TestResponses_ChatUpstreamMalformedAndMultiChoiceFailSafely(t *testing.T) {
	for name, upstreamBody := range map[string]string{
		"malformed":    `{"id":"chat_1","choices":[]}`,
		"multi_choice": `{"id":"chat_1","choices":[{"message":{"content":"a"},"finish_reason":"stop"},{"message":{"content":"b"},"finish_reason":"stop"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			api := newResponsesTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(upstreamBody))
			}, func(m *config.Model) {
				m.UpstreamProtocol = config.UpstreamOpenAIChat
			})
			w := httptest.NewRecorder()
			api.engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"droid-gpt","input":"hi"}`)))
			if w.Code != http.StatusBadGateway {
				t.Fatalf("expected 502, got %d body=%s", w.Code, w.Body.String())
			}
			if strings.Contains(w.Body.String(), `"choices"`) {
				t.Fatalf("raw Chat choices leaked in error body: %s", w.Body.String())
			}
		})
	}
}

type handlerSSEEvent struct {
	name    string
	payload map[string]any
	rawData string
}

func parseHandlerSSE(t *testing.T, body string) []handlerSSEEvent {
	t.Helper()
	var events []handlerSSEEvent
	for _, frame := range strings.Split(strings.TrimSpace(body), "\n\n") {
		if strings.TrimSpace(frame) == "" {
			continue
		}
		var ev handlerSSEEvent
		for _, line := range strings.Split(frame, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), ":") {
				continue
			}
			if strings.HasPrefix(line, "event:") {
				ev.name = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			}
			if strings.HasPrefix(line, "data:") {
				ev.rawData = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
				if err := json.Unmarshal([]byte(ev.rawData), &ev.payload); err != nil {
					t.Fatalf("invalid SSE JSON payload in frame %q: %v", frame, err)
				}
			}
		}
		if ev.name == "" && ev.payload == nil {
			continue
		}
		if ev.name == "" || ev.payload == nil {
			t.Fatalf("incomplete SSE frame %q", frame)
		}
		events = append(events, ev)
	}
	if len(events) == 0 {
		t.Fatalf("no SSE events in body %q", body)
	}
	return events
}

func assertEventCount(t *testing.T, events []handlerSSEEvent, name string, want int) {
	t.Helper()
	got := 0
	for _, ev := range events {
		if ev.name == name {
			got++
		}
	}
	if got != want {
		t.Fatalf("event %q count=%d want=%d events=%#v", name, got, want, events)
	}
}

func assertNoRawChatSSE(t *testing.T, events []handlerSSEEvent) {
	t.Helper()
	for _, ev := range events {
		if strings.Contains(ev.rawData, `"choices"`) || ev.rawData == "[DONE]" {
			t.Fatalf("raw Chat SSE leaked in event %#v", ev)
		}
	}
}
