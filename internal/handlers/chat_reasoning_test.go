package handlers

import (
	"context"
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

	"github.com/trevoraspencer/droid-proxy/internal/config"
	"github.com/trevoraspencer/droid-proxy/internal/reasoning"
	"github.com/trevoraspencer/droid-proxy/internal/upstream"
)

type reasoningTestAPI struct {
	api          *API
	engine       *gin.Engine
	upstreamReq  []string
	upstreamLock sync.Mutex
}

func (r *reasoningTestAPI) captureReq(body string) {
	r.upstreamLock.Lock()
	r.upstreamReq = append(r.upstreamReq, body)
	r.upstreamLock.Unlock()
}

func newReasoningTestAPI(t *testing.T, upstreamHandler func(w http.ResponseWriter, r *http.Request, captureReq func(string))) *reasoningTestAPI {
	t.Helper()
	gin.SetMode(gin.TestMode)
	state := &reasoningTestAPI{}
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHandler(w, r, state.captureReq)
	}))
	t.Cleanup(upstreamServer.Close)

	m := &config.Model{
		Alias:            "droid-deepseek",
		FactoryProvider:  config.FactoryProviderGeneric,
		UpstreamProtocol: config.UpstreamOpenAIChat,
		BaseURL:          upstreamServer.URL,
		UpstreamModel:    "deepseek-chat",
		APIKeyEnv:        "DROID_PROXY_TEST_KEY",
		Capabilities:     config.Capabilities{Reasoning: config.ReasoningDeepSeek},
	}
	t.Setenv("DROID_PROXY_TEST_KEY", "test-key")
	cfg := &config.Config{
		Upstream:       config.Upstream{HTTPTimeout: 5 * time.Second, StreamKeepAlive: 5 * time.Second},
		ReasoningCache: config.ReasoningCache{Enabled: true, MaxEntries: 16, TTL: time.Minute},
		Models:         []*config.Model{m},
	}
	router, err := upstream.NewRouter(cfg.Models)
	if err != nil {
		t.Fatal(err)
	}
	logger := logrus.New()
	logger.SetOutput(io.Discard)
	api := &API{Cfg: cfg, Router: router, Client: upstream.NewClient(cfg), Logger: logger, ReasoningCache: reasoning.NewCache(16, time.Minute)}
	engine := gin.New()
	engine.POST("/v1/chat/completions", api.ChatCompletions)
	state.api = api
	state.engine = engine
	return state
}

// firstTurn: assistant emits reasoning_content + tool_calls. Capture should populate cache.
// secondTurn: client sends a tool result; outgoing payload should be patched with reasoning.
func TestChat_ReasoningReplay_NonStream(t *testing.T) {
	state := newReasoningTestAPI(t, func(w http.ResponseWriter, r *http.Request, captureReq func(string)) {
		body, _ := io.ReadAll(r.Body)
		captureReq(string(body))
		w.Header().Set("Content-Type", "application/json")
		switch len(strings.Split(string(body), `"role":"tool"`)) {
		case 1:
			// First turn: user asked, model wants a tool call.
			_, _ = w.Write([]byte(`{
				"id":"r1",
				"choices":[{
					"index":0,
					"message":{
						"role":"assistant",
						"content":"",
						"reasoning_content":"I need to look this up.",
						"tool_calls":[{"id":"call_42","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"sf\"}"}}]
					}
				}]
			}`))
		default:
			// Second turn: client returned tool result; assistant answers.
			_, _ = w.Write([]byte(`{"id":"r2","choices":[{"index":0,"message":{"role":"assistant","content":"It's 72F."}}]}`))
		}
	})

	// First call
	turn1 := `{"model":"droid-deepseek","conversation_id":"conv-1","messages":[{"role":"user","content":"weather?"}]}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(turn1))
	state.engine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("turn 1: expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	if state.api.ReasoningCache.Len() != 1 {
		t.Fatalf("expected 1 reasoning cache entry after turn 1, got %d", state.api.ReasoningCache.Len())
	}

	// Second call — client did NOT include reasoning_content on the assistant message.
	turn2 := `{"model":"droid-deepseek","conversation_id":"conv-1","messages":[` +
		`{"role":"user","content":"weather?"},` +
		`{"role":"assistant","content":"","tool_calls":[{"id":"call_42","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"sf\"}"}}]},` +
		`{"role":"tool","tool_call_id":"call_42","content":"72F sunny"}` +
		`]}`
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(turn2))
	state.engine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("turn 2: expected 200, got %d body=%s", w.Code, w.Body.String())
	}

	state.upstreamLock.Lock()
	if len(state.upstreamReq) != 2 {
		state.upstreamLock.Unlock()
		t.Fatalf("expected 2 upstream calls, got %d", len(state.upstreamReq))
	}
	turn2Upstream := state.upstreamReq[1]
	state.upstreamLock.Unlock()

	// Decode turn 2 upstream payload and assert reasoning_content was injected.
	var parsed map[string]any
	if err := json.Unmarshal([]byte(turn2Upstream), &parsed); err != nil {
		t.Fatalf("upstream turn-2 not JSON: %v\n%s", err, turn2Upstream)
	}
	msgs, _ := parsed["messages"].([]any)
	if len(msgs) < 2 {
		t.Fatalf("expected >=2 messages, got %v", msgs)
	}
	asst, _ := msgs[1].(map[string]any)
	if got, _ := asst["reasoning_content"].(string); got != "I need to look this up." {
		t.Errorf("expected reasoning_content patched onto assistant turn, got %q\nfull payload:%s", got, turn2Upstream)
	}
}

func TestChat_ReasoningReplay_DoesNotUseWeakUserOnlyScope(t *testing.T) {
	state := newReasoningTestAPI(t, func(w http.ResponseWriter, r *http.Request, captureReq func(string)) {
		body, _ := io.ReadAll(r.Body)
		captureReq(string(body))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"choices":[{
				"message":{
					"role":"assistant",
					"reasoning_content":"do not share this across user-only metadata",
					"tool_calls":[{"id":"call_weak","type":"function","function":{"name":"f","arguments":"{}"}}]
				}
			}]
		}`))
	})

	turn := `{"model":"droid-deepseek","metadata":{"user_id":"shared-user"},"messages":[{"role":"user","content":"same prompt"}]}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(turn))
	state.engine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	if state.api.ReasoningCache.Len() != 0 {
		t.Fatalf("user-only metadata must not create a reasoning cache entry, got %d", state.api.ReasoningCache.Len())
	}
}

func TestChat_ReasoningReplay_IsolatesClientAuthIdentity(t *testing.T) {
	state := newReasoningTestAPI(t, func(w http.ResponseWriter, r *http.Request, captureReq func(string)) {
		body, _ := io.ReadAll(r.Body)
		captureReq(string(body))
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(string(body), `"role":"tool"`) {
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"done"}}]}`))
			return
		}
		_, _ = w.Write([]byte(`{
			"choices":[{
				"message":{
					"role":"assistant",
					"reasoning_content":"tenant-a reasoning",
					"tool_calls":[{"id":"call_shared","type":"function","function":{"name":"f","arguments":"{}"}}]
				}
			}]
		}`))
	})
	state.api.Cfg.ClientAuth = config.ClientAuth{Enabled: true, Header: "Authorization", Scheme: "Bearer", APIKeys: []string{"tenant-a", "tenant-b"}}

	turn1 := `{"model":"droid-deepseek","conversation_id":"conv-shared","messages":[{"role":"user","content":"weather?"}]}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(turn1))
	req.Header.Set("Authorization", "Bearer tenant-a")
	state.engine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("turn 1: expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	if state.api.ReasoningCache.Len() != 1 {
		t.Fatalf("expected tenant-a cache entry, got %d", state.api.ReasoningCache.Len())
	}

	turn2 := `{"model":"droid-deepseek","conversation_id":"conv-shared","messages":[` +
		`{"role":"user","content":"weather?"},` +
		`{"role":"assistant","content":"","tool_calls":[{"id":"call_shared","type":"function","function":{"name":"f","arguments":"{}"}}]},` +
		`{"role":"tool","tool_call_id":"call_shared","content":"72F"}` +
		`]}`
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(turn2))
	req.Header.Set("Authorization", "Bearer tenant-b")
	state.engine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("turn 2: expected 200, got %d body=%s", w.Code, w.Body.String())
	}

	state.upstreamLock.Lock()
	turn2Upstream := state.upstreamReq[len(state.upstreamReq)-1]
	state.upstreamLock.Unlock()
	if strings.Contains(turn2Upstream, "tenant-a reasoning") || strings.Contains(turn2Upstream, "reasoning_content") {
		t.Fatalf("tenant-b request received tenant-a cached reasoning: %s", turn2Upstream)
	}
}

func TestChat_ReasoningReplay_Stream(t *testing.T) {
	state := newReasoningTestAPI(t, func(w http.ResponseWriter, r *http.Request, captureReq func(string)) {
		body, _ := io.ReadAll(r.Body)
		captureReq(string(body))
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		chunks := []string{
			`data: {"choices":[{"index":0,"delta":{"reasoning_content":"I should call "}}]}`,
			`data: {"choices":[{"index":0,"delta":{"reasoning_content":"the weather tool"}}]}`,
			`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_99","type":"function","function":{"name":"w","arguments":""}}]}}]}`,
			`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{}"}}]}}]}`,
			`data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
			`data: [DONE]`,
		}
		for _, c := range chunks {
			_, _ = fmt.Fprintf(w, "%s\n\n", c)
			flusher.Flush()
		}
	})
	turn1 := `{"model":"droid-deepseek","stream":true,"conversation_id":"conv-X","messages":[{"role":"user","content":"weather?"}]}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(turn1))
	state.engine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if state.api.ReasoningCache.Len() != 1 {
		t.Fatalf("expected 1 cache entry after stream, got %d", state.api.ReasoningCache.Len())
	}
	// Spot-check the stored entry
	scope := reasoning.Scope{
		Provider: "droid-deepseek",
		AuthHash: reasoning.APIKeyHash("test-key"),
		Model:    "deepseek-chat",
		BaseURL:  state.api.Router.List()[0].BaseURL,
		Session:  "conv-X",
	}
	got, ok := state.api.ReasoningCache.Lookup(reasoning.Key{Scope: scope, ToolCallIDs: "call_99"})
	if !ok {
		t.Fatalf("expected cache hit; len=%d", state.api.ReasoningCache.Len())
	}
	if got != "I should call the weather tool" {
		t.Errorf("unexpected reasoning text: %q", got)
	}
}

func TestChat_ReasoningReplay_StreamTruncationDoesNotCache(t *testing.T) {
	state := newReasoningTestAPI(t, func(w http.ResponseWriter, r *http.Request, captureReq func(string)) {
		body, _ := io.ReadAll(r.Body)
		captureReq(string(body))
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		chunks := []string{
			`data: {"choices":[{"index":0,"delta":{"reasoning_content":"must not cache "}}]}`,
			`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_truncated","type":"function","function":{"name":"w","arguments":"{}"}}]}}]}`,
		}
		for _, c := range chunks {
			_, _ = fmt.Fprintf(w, "%s\n\n", c)
			flusher.Flush()
		}
	})
	turn1 := `{"model":"droid-deepseek","stream":true,"conversation_id":"conv-truncated","messages":[{"role":"user","content":"weather?"}]}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(turn1))
	state.engine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if state.api.ReasoningCache.Len() != 0 {
		t.Fatalf("truncated stream must not cache reasoning, got %d entries", state.api.ReasoningCache.Len())
	}
	if !strings.Contains(w.Body.String(), `"stream_truncated"`) {
		t.Fatalf("expected protocol truncation error, got %s", w.Body.String())
	}
}

func TestChat_ReasoningReplay_StreamCancellationDoesNotCache(t *testing.T) {
	firstSent := make(chan struct{})
	upstreamCanceled := make(chan struct{})
	state := newReasoningTestAPI(t, func(w http.ResponseWriter, r *http.Request, captureReq func(string)) {
		body, _ := io.ReadAll(r.Body)
		captureReq(string(body))
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		_, _ = fmt.Fprintf(w, "%s\n\n", `data: {"choices":[{"index":0,"delta":{"reasoning_content":"cancelled reasoning"}}]}`)
		flusher.Flush()
		close(firstSent)
		select {
		case <-r.Context().Done():
			close(upstreamCanceled)
		case <-time.After(time.Second):
		}
	})
	srv := httptest.NewServer(state.engine)
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/v1/chat/completions", strings.NewReader(`{"model":"droid-deepseek","stream":true,"conversation_id":"conv-cancel","messages":[{"role":"user","content":"weather?"}]}`))
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
	cancel()
	select {
	case <-upstreamCanceled:
	case <-time.After(time.Second):
		t.Fatal("upstream did not observe downstream cancellation")
	}
	time.Sleep(50 * time.Millisecond)
	if state.api.ReasoningCache.Len() != 0 {
		t.Fatalf("cancelled stream must not cache reasoning, got %d entries", state.api.ReasoningCache.Len())
	}
}

func TestChat_ReasoningReplay_StreamIdleTimeoutDoesNotCache(t *testing.T) {
	upstreamCanceled := make(chan struct{})
	state := newReasoningTestAPI(t, func(w http.ResponseWriter, r *http.Request, captureReq func(string)) {
		body, _ := io.ReadAll(r.Body)
		captureReq(string(body))
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		_, _ = fmt.Fprintf(w, "%s\n\n", `data: {"choices":[{"index":0,"delta":{"reasoning_content":"idle reasoning"}}]}`)
		flusher.Flush()
		<-r.Context().Done()
		close(upstreamCanceled)
	})
	state.api.Cfg.Upstream.HTTPTimeout = 40 * time.Millisecond
	state.api.Cfg.Upstream.StreamKeepAlive = 10 * time.Millisecond
	srv := httptest.NewServer(state.engine)
	defer srv.Close()

	resp, err := srv.Client().Post(srv.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"droid-deepseek","stream":true,"conversation_id":"conv-idle","messages":[{"role":"user","content":"weather?"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"stream_truncated"`) {
		t.Fatalf("expected idle timeout truncation frame, got %s", string(body))
	}
	select {
	case <-upstreamCanceled:
	case <-time.After(time.Second):
		t.Fatal("upstream did not observe idle timeout cancellation")
	}
	if state.api.ReasoningCache.Len() != 0 {
		t.Fatalf("idle-timeout stream must not cache reasoning, got %d entries", state.api.ReasoningCache.Len())
	}
}

func TestChat_ReasoningReplay_StreamPreBodyErrorDoesNotCache(t *testing.T) {
	state := newReasoningTestAPI(t, func(w http.ResponseWriter, r *http.Request, captureReq func(string)) {
		body, _ := io.ReadAll(r.Body)
		captureReq(string(body))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limited"}}`))
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"droid-deepseek","stream":true,"conversation_id":"conv-prebody","messages":[{"role":"user","content":"weather?"}]}`))
	state.engine.ServeHTTP(w, req)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("expected upstream pre-body status, got %d body=%s", w.Code, w.Body.String())
	}
	if state.api.ReasoningCache.Len() != 0 {
		t.Fatalf("pre-body stream error must not cache reasoning, got %d entries", state.api.ReasoningCache.Len())
	}
}
