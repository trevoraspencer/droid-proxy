package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sirupsen/logrus"

	"droid-proxy/internal/config"
)

func mustConfig(t *testing.T, raw string) *config.Config {
	t.Helper()
	cfg, err := config.Load("/dev/null")
	_ = cfg // placeholder if needed
	// use parse via temp file
	tmp := t.TempDir() + "/cfg.yaml"
	if err := writeFile(tmp, raw); err != nil {
		t.Fatal(err)
	}
	cfg, err = config.Load(tmp)
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	return cfg
}

func writeFile(path, content string) error {
	return writeFileBytes(path, []byte(content))
}

func writeFileBytes(path string, b []byte) error {
	return writeFileFn(path, b)
}

// indirection so the test can import "os" via a single small wrapper without cluttering imports
func writeFileFn(path string, b []byte) error {
	return testWriteFile(path, b)
}

func discardLogger() *logrus.Logger {
	l := logrus.New()
	l.SetOutput(devNull{})
	return l
}

type devNull struct{}

func (devNull) Write(p []byte) (int, error) { return len(p), nil }

func TestHealth(t *testing.T) {
	cfg := mustConfig(t, `
models:
  - alias: m
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    base_url: http://127.0.0.1:1/v1
`)
	s, err := New(cfg, discardLogger())
	if err != nil {
		t.Fatalf("server new: %v", err)
	}
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	s.Engine().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("json: %v", err)
	}
	if body["status"] != "ok" {
		t.Errorf("expected status=ok, got %v", body["status"])
	}
}

func TestModelsRouteWired(t *testing.T) {
	cfg := mustConfig(t, `
models:
  - alias: m
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    base_url: http://127.0.0.1:1/v1
`)
	s, err := New(cfg, discardLogger())
	if err != nil {
		t.Fatalf("server new: %v", err)
	}
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	s.Engine().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"id":"m"`) {
		t.Errorf("expected model entry in body, got %s", w.Body.String())
	}
}

func TestClientAuth_RequiresHeader(t *testing.T) {
	cfg := mustConfig(t, `
client_auth:
  enabled: true
  api_keys:
    - "test-key"
models:
  - alias: m
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    base_url: http://127.0.0.1:1/v1
`)
	s, err := New(cfg, discardLogger())
	if err != nil {
		t.Fatalf("server new: %v", err)
	}
	// missing header
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	s.Engine().ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("missing header: expected 401, got %d", w.Code)
	}
	// wrong key
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer nope")
	s.Engine().ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong key: expected 401, got %d", w.Code)
	}
	// right key — should hit the real handler
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	s.Engine().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("valid key: expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	// health stays open without auth
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/health", nil)
	s.Engine().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("health auth-bypass broken: %d", w.Code)
	}
}

func TestClientAuth_ExplicitRawScheme(t *testing.T) {
	cfg := mustConfig(t, `
client_auth:
  enabled: true
  api_keys:
    - "raw-key"
  scheme: ""
models:
  - alias: m
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    base_url: http://127.0.0.1:1/v1
`)
	s, err := New(cfg, discardLogger())
	if err != nil {
		t.Fatalf("server new: %v", err)
	}

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer raw-key")
	s.Engine().ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("bearer key should fail for raw auth, got %d", w.Code)
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "raw-key")
	s.Engine().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("raw key expected 200, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestClientAuth_GatesChatBackedCountTokens(t *testing.T) {
	cfg := mustConfig(t, `
client_auth:
  enabled: true
  api_keys:
    - "test-key"
models:
  - alias: droid-claude
    factory_provider: anthropic
    upstream_protocol: openai-chat
    base_url: http://127.0.0.1:1/v1
`)
	s, err := New(cfg, discardLogger())
	if err != nil {
		t.Fatalf("server new: %v", err)
	}
	body := `{"model":"droid-claude","messages":[{"role":"user","content":"hello world"}]}`

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", strings.NewReader(body))
	s.Engine().ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("missing auth expected 401, got %d body=%s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/messages/count_tokens", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-key")
	s.Engine().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("valid auth expected local 200, got %d body=%s", w.Code, w.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["input_tokens"].(float64) <= 0 {
		t.Fatalf("expected positive local input_tokens, got %#v", out)
	}
}

func TestClientAuth_RunsBeforeBodyLimitAndParsingOnAllNonHealthRoutes(t *testing.T) {
	cfg := mustConfig(t, `
client_auth:
  enabled: true
  api_keys:
    - "test-key"
server:
  request_body_max_bytes: 8
models:
  - alias: droid-gpt
    factory_provider: openai
    upstream_protocol: openai-chat
    base_url: http://127.0.0.1:1/v1
  - alias: droid-claude
    factory_provider: anthropic
    upstream_protocol: openai-chat
    base_url: http://127.0.0.1:1/v1
`)
	s, err := New(cfg, discardLogger())
	if err != nil {
		t.Fatalf("server new: %v", err)
	}
	oversized := strings.Repeat("x", 64)
	for _, tc := range []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/v1/models", ""},
		{http.MethodGet, "/models", ""},
		{http.MethodPost, "/v1/chat/completions", oversized},
		{http.MethodPost, "/chat/completions", oversized},
		{http.MethodPost, "/v1/responses", oversized},
		{http.MethodPost, "/responses", oversized},
		{http.MethodPost, "/v1/messages", oversized},
		{http.MethodPost, "/messages", oversized},
		{http.MethodPost, "/v1/messages/count_tokens", oversized},
		{http.MethodPost, "/messages/count_tokens", oversized},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			s.Engine().ServeHTTP(w, req)
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("expected auth 401 before body work, got %d body=%s", w.Code, w.Body.String())
			}
		})
	}

	for _, path := range []string{"/health", "/healthz"} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		s.Engine().ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("%s should remain unauthenticated, got %d", path, w.Code)
		}
	}
}

func TestRequestBodyLimitRejectsKnownLengthAndChunkedBeforeUpstream(t *testing.T) {
	upstreamHits := 0
	upstreamSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits++
		_, _ = w.Write([]byte(`{"id":"chat_1","choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer upstreamSrv.Close()

	cfg := mustConfig(t, `
server:
  request_body_max_bytes: 48
models:
  - alias: m
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    base_url: `+upstreamSrv.URL+`/v1
`)
	var logs bytes.Buffer
	logger := logrus.New()
	logger.SetOutput(&logs)
	s, err := New(cfg, logger)
	if err != nil {
		t.Fatalf("server new: %v", err)
	}
	within := `{"model":"m","messages":[]}`
	if int64(len(within)) > cfg.Server.RequestBodyMaxBytes {
		t.Fatalf("test body unexpectedly too large")
	}
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(within))
	s.Engine().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("body at/below limit should be processed, got %d body=%s", w.Code, w.Body.String())
	}
	if upstreamHits != 1 {
		t.Fatalf("expected one upstream hit for valid body, got %d", upstreamHits)
	}

	sentinel := "SECRET-SENTINEL-BODY"
	tooLarge := strings.Repeat("x", int(cfg.Server.RequestBodyMaxBytes)+1) + sentinel
	for _, tc := range []struct {
		name          string
		contentLength int64
		body          io.Reader
	}{
		{name: "known length", contentLength: int64(len(tooLarge)), body: strings.NewReader(tooLarge)},
		{name: "unknown length", contentLength: -1, body: io.NopCloser(strings.NewReader(tooLarge))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", tc.body)
			req.ContentLength = tc.contentLength
			s.Engine().ServeHTTP(w, req)
			if w.Code != http.StatusRequestEntityTooLarge {
				t.Fatalf("expected 413, got %d body=%s", w.Code, w.Body.String())
			}
			if len(w.Body.Bytes()) > 256 {
				t.Fatalf("413 body is not bounded: %d bytes", len(w.Body.Bytes()))
			}
			if strings.Contains(w.Body.String(), sentinel) || strings.Contains(logs.String(), sentinel) {
				t.Fatalf("payload secret leaked in response/logs: response=%q logs=%q", w.Body.String(), logs.String())
			}
			if upstreamHits != 1 {
				t.Fatalf("oversized body contacted upstream; hits=%d", upstreamHits)
			}
		})
	}
}

func TestTraceLoggingIsOptInBoundedAndRedacted(t *testing.T) {
	upstreamSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chat_1","choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"apiKey":"sk-1234567890abcdef"}`))
	}))
	defer upstreamSrv.Close()

	cfg := mustConfig(t, `
logging:
  trace_requests: true
  redact: true
models:
  - alias: m
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    base_url: `+upstreamSrv.URL+`/v1
`)
	var logs bytes.Buffer
	logger := logrus.New()
	logger.SetOutput(&logs)
	logger.SetLevel(logrus.DebugLevel)
	s, err := New(cfg, logger)
	if err != nil {
		t.Fatalf("server new: %v", err)
	}

	secret := "sk-1234567890abcdef"
	querySecret := "downstream-secret-123456789"
	encodedQuerySecret := "encoded%2Fquery%3Dsecret"
	reqBody := `{"model":"m","apiKey":"` + secret + `","messages":[{"role":"user","content":"hi"}]}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions?api_key="+secret+"&token="+querySecret+"&access_token="+encodedQuerySecret+"&model=m", strings.NewReader(reqBody))
	req.Header.Set("Authorization", "Bearer "+secret)
	s.Engine().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	if strings.Contains(logs.String(), secret) || strings.Contains(logs.String(), querySecret) || strings.Contains(logs.String(), encodedQuerySecret) {
		t.Fatalf("trace log leaked sentinel secret:\n%s", logs.String())
	}
	if !strings.Contains(logs.String(), "model=m") {
		t.Fatalf("trace log over-redacted benign query parameter:\n%s", logs.String())
	}
	if !strings.Contains(logs.String(), "***") {
		t.Fatalf("trace log did not show redaction placeholder:\n%s", logs.String())
	}
	if logs.Len() > 12*1024 {
		t.Fatalf("trace log unexpectedly large: %d bytes", logs.Len())
	}
}

func TestDefaultLoggingDoesNotTraceBodiesOrCredentials(t *testing.T) {
	upstreamSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chat_1","choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer upstreamSrv.Close()

	cfg := mustConfig(t, `
models:
  - alias: m
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    base_url: `+upstreamSrv.URL+`/v1
`)
	var logs bytes.Buffer
	logger := logrus.New()
	logger.SetOutput(&logs)
	s, err := New(cfg, logger)
	if err != nil {
		t.Fatalf("server new: %v", err)
	}

	secret := "sk-1234567890abcdef"
	reqBody := `{"model":"m","apiKey":"` + secret + `","messages":[{"role":"user","content":"hi"}]}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(reqBody))
	req.Header.Set("Authorization", "Bearer "+secret)
	s.Engine().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	if strings.Contains(logs.String(), secret) || strings.Contains(logs.String(), "apiKey") || strings.Contains(logs.String(), "messages") {
		t.Fatalf("default logs included body/credential data:\n%s", logs.String())
	}
}

func TestRequestBodyLimitAppliesBeforeTranslatedRoutes(t *testing.T) {
	upstreamHits := 0
	upstreamSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits++
		_, _ = w.Write([]byte(`{"id":"chat_1","choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer upstreamSrv.Close()

	cfg := mustConfig(t, `
server:
  request_body_max_bytes: 40
models:
  - alias: droid-gpt
    factory_provider: openai
    upstream_protocol: openai-chat
    base_url: `+upstreamSrv.URL+`/v1
    api_key_env: TEST_OPENAI_KEY
  - alias: droid-claude
    factory_provider: anthropic
    upstream_protocol: openai-chat
    base_url: `+upstreamSrv.URL+`/v1
    api_key_env: TEST_ANTHROPIC_KEY
`)
	t.Setenv("TEST_OPENAI_KEY", "sk-test")
	t.Setenv("TEST_ANTHROPIC_KEY", "anthropic-test")
	s, err := New(cfg, discardLogger())
	if err != nil {
		t.Fatalf("server new: %v", err)
	}

	for _, path := range []string{"/v1/responses", "/responses", "/v1/messages", "/messages"} {
		t.Run(path, func(t *testing.T) {
			body := `{"model":"droid-gpt","input":"` + strings.Repeat("x", 64) + `"}`
			if strings.Contains(path, "messages") {
				body = `{"model":"droid-claude","messages":[{"role":"user","content":"` + strings.Repeat("x", 64) + `"}],"max_tokens":1}`
			}
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
			s.Engine().ServeHTTP(w, req)
			if w.Code != http.StatusRequestEntityTooLarge {
				t.Fatalf("expected 413 before translation, got %d body=%s", w.Code, w.Body.String())
			}
		})
	}
	if upstreamHits != 0 {
		t.Fatalf("oversized translated requests contacted upstream %d times", upstreamHits)
	}
}

func TestRun_UsesConfiguredServerTimeouts(t *testing.T) {
	cfg := mustConfig(t, `
server:
  read_header_timeout: 3s
  read_timeout: 4s
  write_timeout: 5s
  idle_timeout: 6s
  shutdown_timeout: 7s
models:
  - alias: m
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    base_url: http://127.0.0.1:1/v1
`)
	s, err := New(cfg, discardLogger())
	if err != nil {
		t.Fatalf("server new: %v", err)
	}
	httpSrv := s.newHTTPServer()
	if httpSrv.ReadHeaderTimeout != 3*time.Second {
		t.Fatalf("ReadHeaderTimeout = %v", httpSrv.ReadHeaderTimeout)
	}
	if httpSrv.ReadTimeout != 4*time.Second {
		t.Fatalf("ReadTimeout = %v", httpSrv.ReadTimeout)
	}
	if httpSrv.WriteTimeout != 5*time.Second {
		t.Fatalf("WriteTimeout = %v", httpSrv.WriteTimeout)
	}
	if httpSrv.IdleTimeout != 6*time.Second {
		t.Fatalf("IdleTimeout = %v", httpSrv.IdleTimeout)
	}
	ctx, cancel := shutdownContext(cfg.Server.ShutdownTimeout)
	defer cancel()
	if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) <= 0 || time.Until(deadline) > 7*time.Second {
		t.Fatalf("shutdownContext deadline not derived from config: deadline=%v ok=%v", deadline, ok)
	}
}

func TestRun_ReadHeaderTimeoutBoundsSlowloris(t *testing.T) {
	cfg := mustConfig(t, `
listen:
  host: 127.0.0.1
  port: 0
server:
  read_header_timeout: 50ms
  read_timeout: 250ms
  write_timeout: 250ms
  idle_timeout: 250ms
models:
  - alias: m
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    base_url: http://127.0.0.1:1/v1
`)
	s, err := New(cfg, discardLogger())
	if err != nil {
		t.Fatalf("server new: %v", err)
	}
	srv := s.newHTTPServer()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ln) }()
	defer func() {
		_ = srv.Close()
		<-done
	}()

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("GET /health HTTP/1.1\r\nHost:")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	_ = conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	one := []byte{0}
	if _, err := conn.Read(one); err == nil {
		t.Fatal("slowloris connection remained readable/open past read_header_timeout")
	}
}

func TestRun_ZeroShutdownTimeoutOptsOutOfDeadline(t *testing.T) {
	ctx, cancel := shutdownContext(0)
	defer cancel()
	if _, ok := ctx.Deadline(); ok {
		t.Fatal("zero shutdown timeout should opt out of deadline")
	}
}

func TestRun_ShutsDownOnContextCancel(t *testing.T) {
	cfg := mustConfig(t, `
listen:
  host: 127.0.0.1
  port: 0
models:
  - alias: m
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    base_url: http://127.0.0.1:1/v1
`)
	// Override port with an unused one. We use 0 to ask the OS, but Run uses fixed addr.
	// For this test, allocate manually.
	ln, err := getEphemeralAddr()
	if err != nil {
		t.Skipf("no ephemeral port: %v", err)
	}
	cfg.Listen.Host, cfg.Listen.Port = ln.host, ln.port
	s, err := New(cfg, discardLogger())
	if err != nil {
		t.Fatalf("server new: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	case <-context.Background().Done():
	}
}
