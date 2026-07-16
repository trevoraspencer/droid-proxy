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

// testAPI wires a fake upstream and a real handler into a gin engine for testing.
type testAPI struct {
	api      *API
	upstream *httptest.Server
	engine   *gin.Engine
}

// newTestAPI builds an API pointed at an httptest upstream that serves the given handler.
// extraModel lets a test override or extend the model configuration.
func newTestAPI(t *testing.T, upstreamHandler http.HandlerFunc, mutateModel func(*config.Model)) *testAPI {
	t.Helper()
	gin.SetMode(gin.TestMode)
	upstreamServer := httptest.NewServer(upstreamHandler)
	t.Cleanup(upstreamServer.Close)

	m := &config.Model{
		Alias:            "droid-test",
		DisplayName:      "Test Model",
		FactoryProvider:  config.FactoryProviderGeneric,
		UpstreamProtocol: config.UpstreamOpenAIChat,
		BaseURL:          upstreamServer.URL,
		UpstreamModel:    "upstream-test-model",
		APIKeyEnv:        "DROID_PROXY_TEST_KEY",
	}
	if mutateModel != nil {
		mutateModel(m)
	}
	t.Setenv("DROID_PROXY_TEST_KEY", "test-key-value")

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

func TestChat_NonStream_Passthrough(t *testing.T) {
	upBody := `{"id":"chatcmpl-1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hi"}}]}`
	var seenAuth, seenModel, seenBody string
	api := newTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		seenAuth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		seenBody = string(body)
		var v map[string]any
		_ = json.Unmarshal(body, &v)
		if m, ok := v["model"].(string); ok {
			seenModel = m
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Custom", "from-upstream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(upBody))
	}, nil)

	reqBody := `{"model":"droid-test","messages":[{"role":"user","content":"hi"}],"stream":false}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(reqBody))
	api.engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	if w.Body.String() != upBody {
		t.Errorf("body mismatch:\nwant=%s\ngot =%s", upBody, w.Body.String())
	}
	if seenAuth != "Bearer test-key-value" {
		t.Errorf("upstream did not see Bearer auth: %q", seenAuth)
	}
	if seenModel != "upstream-test-model" {
		t.Errorf("expected model rewritten to upstream-test-model, got %q (body=%s)", seenModel, seenBody)
	}
	if w.Header().Get("X-Custom") != "from-upstream" {
		t.Errorf("expected upstream X-Custom header copied, got %q", w.Header().Get("X-Custom"))
	}
}

func TestChat_NonStream_UpstreamErrorPassthrough(t *testing.T) {
	api := newTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"slow down","type":"rate_limit_exceeded"}}`))
	}, nil)

	reqBody := `{"model":"droid-test","messages":[{"role":"user","content":"hi"}]}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(reqBody))
	api.engine.ServeHTTP(w, req)

	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "rate_limit_exceeded") {
		t.Errorf("expected upstream body preserved, got %s", w.Body.String())
	}
}

func TestChat_NonStream_ConfiguredAcceptEncodingDoesNotBreakDecompression(t *testing.T) {
	expected := `{"id":"chatcmpl-gzip","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"decoded"}}]}`
	api := newTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Accept-Encoding"); got == "br" {
			t.Fatalf("profile-controlled Accept-Encoding reached upstream: %q", got)
		}
		var buf bytes.Buffer
		gw := gzip.NewWriter(&buf)
		_, _ = gw.Write([]byte(expected))
		_ = gw.Close()
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Encoding", "gzip")
		_, _ = w.Write(buf.Bytes())
	}, func(m *config.Model) {
		m.ExtraHeaders = map[string]string{"Accept-Encoding": "br"}
	})

	reqBody := `{"model":"droid-test","messages":[{"role":"user","content":"hi"}]}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(reqBody))
	api.engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	if w.Body.String() != expected {
		t.Fatalf("expected decoded upstream body %q, got %q", expected, w.Body.String())
	}
	if got := w.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("downstream response kept stripped Content-Encoding header: %q", got)
	}
}

func TestChat_NonStream_UpstreamSuccessAndErrorBodiesAreCapped(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
	}{
		{name: "success", status: http.StatusOK},
		{name: "error", status: http.StatusBadGateway},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sentinel := "sk-1234567890abcdef"
			api := newTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(strings.Repeat("x", 17) + sentinel))
			}, nil)
			api.api.Cfg.Upstream.ResponseBodyMaxBytes = 16
			api.api.Cfg.Upstream.ErrorBodyMaxBytes = 16

			reqBody := `{"model":"droid-test","messages":[{"role":"user","content":"hi"}]}`
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(reqBody))
			api.engine.ServeHTTP(w, req)

			if w.Code != http.StatusBadGateway {
				t.Fatalf("expected bounded 502, got %d body=%s", w.Code, w.Body.String())
			}
			if strings.Contains(w.Body.String(), sentinel) || strings.Contains(w.Body.String(), strings.Repeat("x", 17)) {
				t.Fatalf("oversized upstream body leaked downstream: %q", w.Body.String())
			}
		})
	}
}

func TestChat_MissingModel(t *testing.T) {
	api := newTestAPI(t, func(http.ResponseWriter, *http.Request) {
		t.Fatal("upstream must not be called when model is missing")
	}, nil)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"messages":[]}`))
	api.engine.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestChat_UnknownModelAlias(t *testing.T) {
	api := newTestAPI(t, func(http.ResponseWriter, *http.Request) {
		t.Fatal("upstream must not be called when alias is unknown")
	}, nil)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"no-such-alias"}`))
	api.engine.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestChat_WrongFactoryProvider(t *testing.T) {
	api := newTestAPI(t, nil, func(m *config.Model) {
		m.FactoryProvider = config.FactoryProviderAnthropic
		m.UpstreamProtocol = config.UpstreamAnthropicMessages
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"droid-test"}`))
	api.engine.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "factory_provider") {
		t.Errorf("expected provider-mismatch message, got %s", w.Body.String())
	}
}

func TestChat_Stream_PassthroughChunks(t *testing.T) {
	api := newTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		chunks := []string{
			`data: {"id":"1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant"}}]}`,
			`data: {"id":"1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"hi"}}]}`,
			`data: {"id":"1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":" there"}}]}`,
			`data: [DONE]`,
		}
		for _, c := range chunks {
			_, _ = fmt.Fprintf(w, "%s\n\n", c)
			flusher.Flush()
		}
	}, nil)

	reqBody := `{"model":"droid-test","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(reqBody))
	api.engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Errorf("expected text/event-stream content-type, got %q", ct)
	}
	out := w.Body.String()
	for _, fragment := range []string{`"role":"assistant"`, `"content":"hi"`, `"content":" there"`, "[DONE]"} {
		if !strings.Contains(out, fragment) {
			t.Errorf("missing %s in stream:\n%s", fragment, out)
		}
	}
}

func TestChat_Stream_ToolCallsPreserved(t *testing.T) {
	api := newTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		chunks := []string{
			`data: {"id":"1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_abc","type":"function","function":{"name":"get_weather","arguments":""}}]}}]}`,
			`data: {"id":"1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"city\":"}}]}}]}`,
			`data: {"id":"1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"sf\"}"}}]}}]}`,
			`data: {"id":"1","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
			`data: [DONE]`,
		}
		for _, c := range chunks {
			_, _ = fmt.Fprintf(w, "%s\n\n", c)
			flusher.Flush()
		}
	}, nil)
	reqBody := `{"model":"droid-test","stream":true,"messages":[{"role":"user","content":"weather?"}],"tools":[{"type":"function","function":{"name":"get_weather"}}]}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(reqBody))
	api.engine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	out := w.Body.String()
	for _, fragment := range []string{`"call_abc"`, `"get_weather"`, `\"city\":`, `\"sf\"`, `"tool_calls"`} {
		if !strings.Contains(out, fragment) {
			t.Errorf("expected fragment %s in stream:\n%s", fragment, out)
		}
	}
}

func TestChat_NonStream_ToolMessagesPassthrough(t *testing.T) {
	// Verify a request containing tool result messages (role:tool, tool_call_id) is
	// forwarded byte-for-byte to upstream, with no field reshaping.
	var seenBody string
	api := newTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		seenBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"1","choices":[{"index":0,"message":{"role":"assistant","content":"ok"}}]}`))
	}, nil)

	reqBody := `{"model":"droid-test","messages":[` +
		`{"role":"user","content":"what's the weather"},` +
		`{"role":"assistant","content":null,"tool_calls":[{"id":"call_abc","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"sf\"}"}}]},` +
		`{"role":"tool","tool_call_id":"call_abc","content":"72F sunny"}` +
		`]}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(reqBody))
	api.engine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	for _, fragment := range []string{`"call_abc"`, `"tool_call_id"`, `"role":"tool"`, `"72F sunny"`} {
		if !strings.Contains(seenBody, fragment) {
			t.Errorf("upstream body missing %s\nbody=%s", fragment, seenBody)
		}
	}
}

func TestChat_Stream_ClientCancelStopsUpstream(t *testing.T) {
	// We arrange an upstream that emits one chunk then blocks. After receiving
	// the chunk on the client side we cancel the context and assert the upstream's
	// request context fires (visible via a channel).
	upstreamReqDone := make(chan struct{}, 1)
	api := newTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		_, _ = fmt.Fprintf(w, "data: {\"a\":1}\n\n")
		flusher.Flush()
		<-r.Context().Done()
		upstreamReqDone <- struct{}{}
	}, nil)

	srv := httptest.NewServer(api.engine)
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	httpReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"droid-test","stream":true}`))
	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	reader := bufio.NewReader(resp.Body)
	// read the first chunk we know the upstream emitted
	got := make([]byte, 0, 64)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		b, err := reader.ReadByte()
		if err != nil {
			break
		}
		got = append(got, b)
		if strings.Contains(string(got), "{\"a\":1}") {
			break
		}
	}
	if !strings.Contains(string(got), "{\"a\":1}") {
		t.Fatalf("did not see initial chunk, got %q", got)
	}
	cancel() // client cancels
	select {
	case <-upstreamReqDone:
	case <-time.After(3 * time.Second):
		t.Fatal("upstream request did not see cancellation within 3s")
	}
}

func TestChat_ExtraArgsApplied(t *testing.T) {
	var seenBody string
	api := newTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		seenBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"x"}`))
	}, func(m *config.Model) {
		m.ExtraArgs = map[string]any{
			"temperature": 0.2,
			"top_p":       0.9,
		}
	})
	reqBody := `{"model":"droid-test","messages":[],"temperature":0.7}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(reqBody))
	api.engine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if !strings.Contains(seenBody, `"temperature":0.2`) {
		t.Errorf("expected extra_args to override temperature, body=%s", seenBody)
	}
	if !strings.Contains(seenBody, `"top_p":0.9`) {
		t.Errorf("expected top_p applied, body=%s", seenBody)
	}
}

func TestChat_NoTrailingV1PrefixWorks(t *testing.T) {
	upBody := `{"id":"chatcmpl-1","choices":[]}`
	api := newTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(upBody))
	}, nil)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/chat/completions", strings.NewReader(`{"model":"droid-test"}`))
	api.engine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
}

// TestChat_NonStream_BodyReadFailureIsNotReportedAsTooLarge pins that a
// transport failure while reading the upstream body (here: upstream declares
// a Content-Length it never delivers) is reported as a read failure, not as a
// size-cap ("too large") violation.
func TestChat_NonStream_BodyReadFailureIsNotReportedAsTooLarge(t *testing.T) {
	api := newTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", "512")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"truncated`))
		// Handler returns having written fewer bytes than Content-Length; the
		// server closes the connection and the proxy's body read fails.
	}, nil)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"droid-test","messages":[{"role":"user","content":"hi"}],"stream":false}`))
	api.engine.ServeHTTP(w, req)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d body=%s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "too large") {
		t.Fatalf("read failure must not be reported as size-cap violation: %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "failed to read upstream response body") {
		t.Fatalf("expected read-failure message, got: %s", w.Body.String())
	}
}

// ---------------------------------------------------------------------------
// VAL-CROSS-017: Via intermediary metadata is never relayed.
//
// These generic-provider integration fixtures exercise the shared generic Chat
// handler end to end against a local fake upstream that returns a Via header
// carrying a synthetic internal-topology sentinel. They prove Via is removed
// uniformly from relayed success, upstream-error, and streaming responses while
// exact safe Retry-After and allowlisted request-ID metadata survive.
// ---------------------------------------------------------------------------

// viaTopologySentinel is a synthetic internal-topology sentinel embedded in
// upstream Via values. It must never appear in any relayed downstream header,
// body, or captured artifact.
const viaTopologySentinel = "egress-pool-node-42.internal.topology.sentinel"

// TestChat_ViaHeader_FilteredOnSuccess proves that upstream Via intermediary
// metadata (including a synthetic internal-topology sentinel) is never relayed
// on a 2xx success response, while safe Retry-After and allowlisted request-ID
// metadata survive byte-exact.
func TestChat_ViaHeader_FilteredOnSuccess(t *testing.T) {
	viaValue := "1.1 " + viaTopologySentinel + " (envoy/1.30)"
	api := newTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Via", viaValue)
		w.Header().Set("X-Request-ID", "req-via-success-123")
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"chatcmpl-via","choices":[{"index":0,"message":{"role":"assistant","content":"ok"}}]}`))
	}, nil)

	reqBody := `{"model":"droid-test","messages":[{"role":"user","content":"hi"}]}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(reqBody))
	api.engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Via"); got != "" {
		t.Errorf("Via leaked on success response: %q", got)
	}
	if got := w.Header().Get("X-Request-ID"); got != "req-via-success-123" {
		t.Errorf("safe X-Request-ID not preserved on success: %q", got)
	}
	if got := w.Header().Get("Retry-After"); got != "120" {
		t.Errorf("safe Retry-After not preserved on success: %q", got)
	}
	if strings.Contains(w.Body.String(), viaTopologySentinel) {
		t.Errorf("internal-topology sentinel leaked into success body: %q", viaTopologySentinel)
	}
}

// TestChat_ViaHeader_FilteredOnUpstreamError proves Via is removed from an
// upstream-error response while safe Retry-After and allowlisted request-ID
// metadata survive byte-exact.
func TestChat_ViaHeader_FilteredOnUpstreamError(t *testing.T) {
	viaValue := "1.1 dl-intermediate-relay.eu.example.org, 1.1 " + viaTopologySentinel + " (haproxy 2.9)"
	api := newTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Via", viaValue)
		w.Header().Set("X-Request-ID", "req-via-error-456")
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limited","type":"rate_limit_error"}}`))
	}, nil)

	reqBody := `{"model":"droid-test","messages":[{"role":"user","content":"hi"}]}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(reqBody))
	api.engine.ServeHTTP(w, req)

	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d body=%s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Via"); got != "" {
		t.Errorf("Via leaked on upstream-error response: %q", got)
	}
	if got := w.Header().Get("X-Request-ID"); got != "req-via-error-456" {
		t.Errorf("safe X-Request-ID not preserved on upstream error: %q", got)
	}
	if got := w.Header().Get("Retry-After"); got != "60" {
		t.Errorf("safe Retry-After not preserved on upstream error: %q", got)
	}
	if strings.Contains(w.Body.String(), viaTopologySentinel) {
		t.Errorf("internal-topology sentinel leaked into error body: %q", viaTopologySentinel)
	}
}

// TestChat_ViaHeader_FilteredOnStream proves Via is removed from a streaming
// response's headers while safe Retry-After and allowlisted request-ID metadata
// survive, and the internal-topology sentinel never appears in the relayed SSE
// body.
func TestChat_ViaHeader_FilteredOnStream(t *testing.T) {
	viaValue := "1.1 " + viaTopologySentinel + " (varnish 7.4)"
	api := newTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Via", viaValue)
		w.Header().Set("X-Request-ID", "req-via-stream-789")
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		chunks := []string{
			`data: {"id":"1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"hi"}}]}`,
			`data: [DONE]`,
		}
		for _, c := range chunks {
			_, _ = fmt.Fprintf(w, "%s\n\n", c)
			flusher.Flush()
		}
	}, nil)

	reqBody := `{"model":"droid-test","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(reqBody))
	api.engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Via"); got != "" {
		t.Errorf("Via leaked on streaming response: %q", got)
	}
	if got := w.Header().Get("X-Request-ID"); got != "req-via-stream-789" {
		t.Errorf("safe X-Request-ID not preserved on stream: %q", got)
	}
	if got := w.Header().Get("Retry-After"); got != "30" {
		t.Errorf("safe Retry-After not preserved on stream: %q", got)
	}
	if strings.Contains(w.Body.String(), viaTopologySentinel) {
		t.Errorf("internal-topology sentinel leaked into SSE body: %q", viaTopologySentinel)
	}
}
