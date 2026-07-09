package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/trevoraspencer/droid-proxy/internal/config"
	"github.com/trevoraspencer/droid-proxy/internal/testutil"
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

func TestClientAPIKeyMatchesExactDigest(t *testing.T) {
	keys := [][sha256.Size]byte{
		sha256.Sum256([]byte("tenant-a")),
		sha256.Sum256([]byte("tenant-b")),
	}
	if !clientAPIKeyMatches("tenant-b", keys) {
		t.Fatal("expected exact key match")
	}
	for _, got := range []string{"tenant", "tenant-b ", "Tenant-B", "tenant-c"} {
		if clientAPIKeyMatches(got, keys) {
			t.Fatalf("unexpected key match for %q", got)
		}
	}
}

func TestClientAuthUsesConstantTimeCompare(t *testing.T) {
	raw, err := os.ReadFile("middleware.go")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte("subtle.ConstantTimeCompare")) {
		t.Fatal("client auth key comparison must use subtle.ConstantTimeCompare")
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
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("no ephemeral listener: %v", err)
	}
	s, err := New(cfg, discardLogger())
	if err != nil {
		t.Fatalf("server new: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.RunOnListener(ctx, ln) }()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	case <-context.Background().Done():
	}
}

func TestServer_WatcherLifecycleWithTempAuthDir(t *testing.T) {
	authDir := t.TempDir()

	cfg := mustConfig(t, `
listen:
  host: 127.0.0.1
  port: 0
oauth:
  auth_dir: `+authDir+`
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

	// Pool should exist even with empty auth dir
	if s.pool == nil {
		t.Fatal("expected pool to be created")
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("no ephemeral listener: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.RunOnListener(ctx, ln) }()

	// Server should be running with watcher
	time.Sleep(100 * time.Millisecond)

	// Verify health endpoint works
	resp, err := http.Get("http://" + ln.Addr().String() + "/health")
	if err != nil {
		t.Fatalf("health check failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("health status = %d, want 200", resp.StatusCode)
	}

	// Cancel context to trigger shutdown (which also stops the watcher)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("server shutdown timed out")
	}
}

func TestServer_StartupWithInvalidTokenFiles(t *testing.T) {
	authDir := t.TempDir()

	// Write an invalid JSON file
	if err := os.WriteFile(filepath.Join(authDir, "broken.json"), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Write a valid Codex token file
	validToken := `{"type":"codex","access_token":"valid-access-SENTINEL","refresh_token":"valid-refresh-SENTINEL","email":"user@example.com","expired":"2099-01-01T00:00:00Z"}` + "\n"
	if err := os.WriteFile(filepath.Join(authDir, "codex-user.json"), []byte(validToken), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := mustConfig(t, `
listen:
  host: 127.0.0.1
  port: 0
oauth:
  auth_dir: `+authDir+`
models:
  - alias: m
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    base_url: http://127.0.0.1:1/v1
`)

	s, err := New(cfg, discardLogger())
	if err != nil {
		t.Fatalf("server should start even with invalid token files: %v", err)
	}

	// Pool should have exactly 1 valid account
	snap := s.pool.Snapshot()
	if len(snap.Accounts) != 1 {
		t.Fatalf("expected 1 account, got %d", len(snap.Accounts))
	}
	if snap.Accounts[0].Selector != "user@example.com" {
		t.Fatalf("expected selector user@example.com, got %q", snap.Accounts[0].Selector)
	}
}

// --- Pool Health Endpoint Tests (VAL-API-001 through VAL-API-010) ---

// helperAuthDirServer creates a server with a temp auth dir containing the given
// token JSON files. Each entry is filename -> JSON content.
func helperAuthDirServer(t *testing.T, authDirFiles map[string]string, extraConfig string) (*Server, func()) {
	t.Helper()
	authDir := t.TempDir()
	for name, content := range authDirFiles {
		if err := os.WriteFile(filepath.Join(authDir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := mustConfig(t, `
listen:
  host: 127.0.0.1
  port: 0
oauth:
  auth_dir: `+authDir+`
`+extraConfig+`
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
	return s, func() {}
}

// VAL-API-001: Versioned and prefixless routes expose pool health
func TestPoolHealthRoutes_Return200WhenAuthorized(t *testing.T) {
	authDir := t.TempDir()
	token := `{"type":"codex","access_token":"tok","refresh_token":"rt","email":"user@example.com","expired":"2099-01-01T00:00:00Z"}` + "\n"
	if err := os.WriteFile(filepath.Join(authDir, "codex-user.json"), []byte(token), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := mustConfig(t, `
listen:
  host: 127.0.0.1
  port: 0
oauth:
  auth_dir: `+authDir+`
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
	for _, path := range []string{"/v1/oauth/pool-health", "/oauth/pool-health"} {
		t.Run(path, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, path, nil)
			s.Engine().ServeHTTP(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
			}
			var body map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatalf("json: %v", err)
			}
			if body["object"] != "oauth_pool_health" {
				t.Errorf("expected object=oauth_pool_health, got %v", body["object"])
			}
			if body["provider"] != "codex" {
				t.Errorf("expected provider=codex, got %v", body["provider"])
			}
		})
	}
}

// VAL-API-001: Empty pool returns successful response with empty accounts array
func TestPoolHealthRoutes_EmptyPoolReturnsEmptyAccounts(t *testing.T) {
	authDir := t.TempDir()
	cfg := mustConfig(t, `
listen:
  host: 127.0.0.1
  port: 0
oauth:
  auth_dir: `+authDir+`
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
	for _, path := range []string{"/v1/oauth/pool-health", "/oauth/pool-health"} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		s.Engine().ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: expected 200, got %d", path, w.Code)
		}
		var body map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("json: %v", err)
		}
		accounts, ok := body["accounts"].([]any)
		if !ok || len(accounts) != 0 {
			t.Fatalf("%s: expected empty accounts array, got %v", path, body["accounts"])
		}
	}
}

// VAL-API-002: Pool health is auth-gated like other non-health routes
func TestPoolHealth_AuthGated(t *testing.T) {
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
	for _, path := range []string{"/v1/oauth/pool-health", "/oauth/pool-health"} {
		t.Run("missing_auth_"+path, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, path, nil)
			s.Engine().ServeHTTP(w, req)
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("expected 401, got %d body=%s", w.Code, w.Body.String())
			}
		})
		t.Run("invalid_auth_"+path, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.Header.Set("Authorization", "Bearer wrong")
			s.Engine().ServeHTTP(w, req)
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("expected 401, got %d body=%s", w.Code, w.Body.String())
			}
		})
		t.Run("valid_auth_"+path, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.Header.Set("Authorization", "Bearer test-key")
			s.Engine().ServeHTTP(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
			}
		})
	}
	// Health endpoints remain unauthenticated
	for _, path := range []string{"/health", "/healthz"} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		s.Engine().ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("%s should remain unauthenticated, got %d", path, w.Code)
		}
	}
}

// VAL-API-002: Auth matches /v1/models pattern including custom/raw schemes
func TestPoolHealth_AuthMatchesModelsRoute(t *testing.T) {
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
	for _, path := range []string{"/v1/oauth/pool-health", "/v1/models"} {
		t.Run("raw_scheme_ok_"+path, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.Header.Set("Authorization", "raw-key")
			s.Engine().ServeHTTP(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("expected 200 for raw scheme on %s, got %d body=%s", path, w.Code, w.Body.String())
			}
		})
		t.Run("bearer_scheme_fails_"+path, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.Header.Set("Authorization", "Bearer raw-key")
			s.Engine().ServeHTTP(w, req)
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("expected 401 for bearer scheme on %s, got %d", path, w.Code)
			}
		})
	}
}

// VAL-API-003: Pool health includes safe operational state
func TestPoolHealth_IncludesSafeOperationalState(t *testing.T) {
	authDir := t.TempDir()
	token := `{"type":"codex","access_token":"tok-SENTINEL","refresh_token":"rt-SENTINEL","email":"user@example.com","expired":"2099-01-01T00:00:00Z"}` + "\n"
	if err := os.WriteFile(filepath.Join(authDir, "codex-user.json"), []byte(token), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := mustConfig(t, `
listen:
  host: 127.0.0.1
  port: 0
oauth:
  auth_dir: `+authDir+`
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
	req := httptest.NewRequest(http.MethodGet, "/v1/oauth/pool-health", nil)
	s.Engine().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("json: %v", err)
	}
	accounts, ok := body["accounts"].([]any)
	if !ok || len(accounts) != 1 {
		t.Fatalf("expected 1 account, got %v", body["accounts"])
	}
	acct := accounts[0].(map[string]any)

	// Verify safe operational fields are present
	for _, key := range []string{"selector", "provider", "disabled", "token_file_present", "healthy", "in_flight"} {
		if _, exists := acct[key]; !exists {
			t.Errorf("missing key %q in account: %v", key, acct)
		}
	}
	if acct["selector"] != "user@example.com" {
		t.Errorf("expected selector user@example.com, got %v", acct["selector"])
	}
	if acct["provider"] != "codex" {
		t.Errorf("expected provider codex, got %v", acct["provider"])
	}
	if acct["disabled"] != false {
		t.Errorf("expected disabled=false, got %v", acct["disabled"])
	}
	if acct["token_file_present"] != true {
		t.Errorf("expected token_file_present=true, got %v", acct["token_file_present"])
	}
	if acct["healthy"] != true {
		t.Errorf("expected healthy=true, got %v", acct["healthy"])
	}
	if inFlight, ok := acct["in_flight"].(float64); !ok || inFlight != 0 {
		t.Errorf("expected in_flight=0, got %v", acct["in_flight"])
	}
}

// VAL-API-004: Pool health is read-only and secret-safe
func TestPoolHealth_ReadOnlyAndSecretSafe(t *testing.T) {
	authDir := t.TempDir()
	secretAccessToken := "SENTINEL-ACCESS-TOKEN-abc123"
	secretRefreshToken := "SENTINEL-REFRESH-TOKEN-xyz789"
	token := `{"type":"codex","access_token":"` + secretAccessToken + `","refresh_token":"` + secretRefreshToken + `","email":"user@example.com","expired":"2099-01-01T00:00:00Z"}` + "\n"
	if err := os.WriteFile(filepath.Join(authDir, "codex-user.json"), []byte(token), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := mustConfig(t, `
listen:
  host: 127.0.0.1
  port: 0
oauth:
  auth_dir: `+authDir+`
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

	// Capture pool state before pool-health call
	snapBefore := s.pool.Snapshot()

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/oauth/pool-health", nil)
	s.Engine().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	bodyStr := w.Body.String()

	// Response must not contain secrets
	for _, secret := range []string{secretAccessToken, secretRefreshToken, "access_token", "refresh_token"} {
		if strings.Contains(bodyStr, secret) {
			t.Errorf("response contains secret %q: %s", secret, bodyStr)
		}
	}

	// Pool state must be unchanged
	snapAfter := s.pool.Snapshot()
	if len(snapAfter.Accounts) != len(snapBefore.Accounts) {
		t.Fatalf("pool accounts changed from %d to %d", len(snapBefore.Accounts), len(snapAfter.Accounts))
	}
	if snapAfter.Accounts[0].InFlight != snapBefore.Accounts[0].InFlight {
		t.Errorf("in_flight changed from %d to %d", snapBefore.Accounts[0].InFlight, snapAfter.Accounts[0].InFlight)
	}

	// Token files must be unchanged
	rawAfter, _ := os.ReadFile(filepath.Join(authDir, "codex-user.json"))
	if strings.Contains(string(rawAfter), "in_flight") || strings.Contains(string(rawAfter), "last_used") {
		t.Errorf("token file was mutated with runtime state: %s", string(rawAfter))
	}
}

// VAL-API-005: Only GET is part of the pool health contract
func TestPoolHealth_NonGetMethodsNotAccepted(t *testing.T) {
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
	for _, method := range []string{http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions} {
		for _, path := range []string{"/v1/oauth/pool-health", "/oauth/pool-health"} {
			t.Run(method+"_"+path, func(t *testing.T) {
				w := httptest.NewRecorder()
				req := httptest.NewRequest(method, path, nil)
				s.Engine().ServeHTTP(w, req)
				if w.Code == http.StatusOK {
					t.Fatalf("non-GET %s on %s should not return 200, got %d", method, path, w.Code)
				}
			})
		}
	}
}

// VAL-API-006: Pool health uses the standard route group and middleware
func TestPoolHealth_UsesStandardRouteGroup(t *testing.T) {
	cfg := mustConfig(t, `
client_auth:
  enabled: true
  api_keys:
    - "test-key"
server:
  request_body_max_bytes: 8
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
	// Both pool-health aliases should require auth before body limit (like /v1/models)
	for _, path := range []string{"/v1/oauth/pool-health", "/oauth/pool-health"} {
		t.Run("auth_before_body_limit_"+path, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, path, nil)
			s.Engine().ServeHTTP(w, req)
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("expected auth 401 before body work, got %d", w.Code)
			}
		})
	}
	// Authenticated request should succeed
	for _, path := range []string{"/v1/oauth/pool-health", "/oauth/pool-health"} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer test-key")
		s.Engine().ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("%s with valid auth: expected 200, got %d body=%s", path, w.Code, w.Body.String())
		}
	}
}

// VAL-API-006: Pool health auth behavior matches /v1/models for exact scheme matching
func TestPoolHealth_AuthMatchesModelsExactScheme(t *testing.T) {
	cfg := mustConfig(t, `
client_auth:
  enabled: true
  api_keys:
    - "test-key"
  scheme: "Bearer"
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
	for _, path := range []string{"/v1/oauth/pool-health", "/oauth/pool-health", "/v1/models", "/models"} {
		t.Run("valid_scheme_"+path, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.Header.Set("Authorization", "Bearer test-key")
			s.Engine().ServeHTTP(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("expected 200 for %s, got %d", path, w.Code)
			}
		})
		t.Run("no_scheme_"+path, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.Header.Set("Authorization", "test-key")
			s.Engine().ServeHTTP(w, req)
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("expected 401 without Bearer prefix on %s, got %d", path, w.Code)
			}
		})
	}
}

// VAL-API-007: Success response shape is deterministic
func TestPoolHealth_DeterministicResponseShape(t *testing.T) {
	authDir := t.TempDir()
	token1 := `{"type":"codex","access_token":"tok1","refresh_token":"rt1","email":"beta@example.com","expired":"2099-01-01T00:00:00Z"}` + "\n"
	token2 := `{"type":"codex","access_token":"tok2","refresh_token":"rt2","email":"alpha@example.com","expired":"2099-01-01T00:00:00Z"}` + "\n"
	if err := os.WriteFile(filepath.Join(authDir, "codex-beta.json"), []byte(token1), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(authDir, "codex-alpha.json"), []byte(token2), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := mustConfig(t, `
listen:
  host: 127.0.0.1
  port: 0
oauth:
  auth_dir: `+authDir+`
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
	req := httptest.NewRequest(http.MethodGet, "/v1/oauth/pool-health", nil)
	s.Engine().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	// Verify JSON content type
	ct := w.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("expected application/json content type, got %q", ct)
	}

	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("json: %v", err)
	}
	if body["object"] != "oauth_pool_health" {
		t.Errorf("expected object=oauth_pool_health, got %v", body["object"])
	}
	if body["provider"] != "codex" {
		t.Errorf("expected provider=codex, got %v", body["provider"])
	}

	accounts := body["accounts"].([]any)
	if len(accounts) != 2 {
		t.Fatalf("expected 2 accounts, got %d", len(accounts))
	}
	// Deterministic order: alpha before beta (sorted by selector)
	first := accounts[0].(map[string]any)
	second := accounts[1].(map[string]any)
	if first["selector"] != "alpha@example.com" {
		t.Errorf("expected first account selector=alpha@example.com, got %v", first["selector"])
	}
	if second["selector"] != "beta@example.com" {
		t.Errorf("expected second account selector=beta@example.com, got %v", second["selector"])
	}

	// Verify in_flight is a non-negative integer
	for i, acct := range accounts {
		a := acct.(map[string]any)
		if inFlight, ok := a["in_flight"].(float64); !ok || inFlight < 0 {
			t.Errorf("account %d: in_flight should be non-negative number, got %v", i, a["in_flight"])
		}
	}

	// Both paths return the same shape
	w2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/oauth/pool-health", nil)
	s.Engine().ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("prefixless: expected 200, got %d", w2.Code)
	}
	var body2 map[string]any
	if err := json.Unmarshal(w2.Body.Bytes(), &body2); err != nil {
		t.Fatalf("json: %v", err)
	}
	if body["object"] != body2["object"] || body["provider"] != body2["provider"] {
		t.Errorf("versioned and prefixless shapes differ: %v vs %v", body, body2)
	}
	acc1 := body["accounts"].([]any)
	acc2 := body2["accounts"].([]any)
	if len(acc1) != len(acc2) {
		t.Errorf("account count differs: %d vs %d", len(acc1), len(acc2))
	}
}

// VAL-API-008: Invalid token files and non-Codex tokens do not pollute the endpoint
func TestPoolHealth_InvalidTokenFilesAndXaiTokensDoNotPollute(t *testing.T) {
	authDir := t.TempDir()
	// Invalid JSON file
	if err := os.WriteFile(filepath.Join(authDir, "broken.json"), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Non-JSON file
	if err := os.WriteFile(filepath.Join(authDir, "notes.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	// xAI token
	xaiToken := `{"type":"xai","access_token":"xai-tok-SENTINEL","refresh_token":"xai-rt-SENTINEL","email":"xai@example.com","expired":"2099-01-01T00:00:00Z"}` + "\n"
	if err := os.WriteFile(filepath.Join(authDir, "xai-user.json"), []byte(xaiToken), 0o600); err != nil {
		t.Fatal(err)
	}
	// Valid Codex token
	codexToken := `{"type":"codex","access_token":"codex-tok","refresh_token":"codex-rt","email":"codex@example.com","expired":"2099-01-01T00:00:00Z"}` + "\n"
	if err := os.WriteFile(filepath.Join(authDir, "codex-user.json"), []byte(codexToken), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := mustConfig(t, `
listen:
  host: 127.0.0.1
  port: 0
oauth:
  auth_dir: `+authDir+`
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
	req := httptest.NewRequest(http.MethodGet, "/v1/oauth/pool-health", nil)
	s.Engine().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("json: %v", err)
	}
	accounts := body["accounts"].([]any)
	if len(accounts) != 1 {
		t.Fatalf("expected 1 Codex account, got %d", len(accounts))
	}
	acct := accounts[0].(map[string]any)
	if acct["selector"] != "codex@example.com" {
		t.Errorf("expected Codex account, got %v", acct["selector"])
	}
	// No xAI entries or secrets
	bodyStr := w.Body.String()
	for _, secret := range []string{"xai-tok-SENTINEL", "xai-rt-SENTINEL", "xai@example.com"} {
		if strings.Contains(bodyStr, secret) {
			t.Errorf("response contains xAI secret or label: %s", bodyStr)
		}
	}
}

// VAL-API-008: Empty auth dir returns 200 with empty accounts
func TestPoolHealth_MissingAuthDirReturnsEmptyAccounts(t *testing.T) {
	cfg := mustConfig(t, `
listen:
  host: 127.0.0.1
  port: 0
oauth:
  auth_dir: `+t.TempDir()+`/nonexistent
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
	req := httptest.NewRequest(http.MethodGet, "/v1/oauth/pool-health", nil)
	s.Engine().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("json: %v", err)
	}
	accounts, ok := body["accounts"].([]any)
	if !ok || len(accounts) != 0 {
		t.Fatalf("expected empty accounts, got %v", body["accounts"])
	}
}

// VAL-API-009: Selector label policy is safe
func TestPoolHealth_SafeSelectorLabels(t *testing.T) {
	authDir := t.TempDir()
	// Token with email — should use email, not account_id
	emailToken := `{"type":"codex","access_token":"tok1","refresh_token":"rt1","email":"email@example.com","sub":"sub-123","account_id":"accid-456","expired":"2099-01-01T00:00:00Z"}` + "\n"
	if err := os.WriteFile(filepath.Join(authDir, "codex-email.json"), []byte(emailToken), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := mustConfig(t, `
listen:
  host: 127.0.0.1
  port: 0
oauth:
  auth_dir: `+authDir+`
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
	req := httptest.NewRequest(http.MethodGet, "/v1/oauth/pool-health", nil)
	s.Engine().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("json: %v", err)
	}
	accounts := body["accounts"].([]any)
	acct := accounts[0].(map[string]any)

	// Email takes precedence
	if acct["selector"] != "email@example.com" {
		t.Errorf("expected email selector, got %v", acct["selector"])
	}
	// Raw account_id should not appear at top level
	bodyStr := w.Body.String()
	if strings.Contains(bodyStr, "accid-456") {
		t.Errorf("response contains raw account_id: %s", bodyStr)
	}
}

// VAL-API-009: Subject-only fallback
func TestPoolHealth_SubjectOnlySelectorFallback(t *testing.T) {
	authDir := t.TempDir()
	subjectToken := `{"type":"codex","access_token":"tok2","sub":"sub-only-789","expired":"2099-01-01T00:00:00Z"}` + "\n"
	if err := os.WriteFile(filepath.Join(authDir, "codex-sub.json"), []byte(subjectToken), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := mustConfig(t, `
listen:
  host: 127.0.0.1
  port: 0
oauth:
  auth_dir: `+authDir+`
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
	req := httptest.NewRequest(http.MethodGet, "/v1/oauth/pool-health", nil)
	s.Engine().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("json: %v", err)
	}
	accounts := body["accounts"].([]any)
	acct := accounts[0].(map[string]any)
	if acct["selector"] != "sub-only-789" {
		t.Errorf("expected subject-based selector, got %v", acct["selector"])
	}
}

// VAL-API-010: Pool health has no refresh or upstream side effects
func TestPoolHealth_NoRefreshOrUpstreamSideEffects(t *testing.T) {
	authDir := t.TempDir()
	// Expired token that would normally need refresh
	token := `{"type":"codex","access_token":"EXPIRED-ACCESS-SENTINEL","refresh_token":"REFRESH-SENTINEL","email":"expired@example.com","expired":"2000-01-01T00:00:00Z"}` + "\n"
	if err := os.WriteFile(filepath.Join(authDir, "codex-expired.json"), []byte(token), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := mustConfig(t, `
listen:
  host: 127.0.0.1
  port: 0
oauth:
  auth_dir: `+authDir+`
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

	// Capture state before
	snapBefore := s.pool.Snapshot()

	// Call pool health multiple times
	for i := 0; i < 3; i++ {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/v1/oauth/pool-health", nil)
		s.Engine().ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("call %d: expected 200, got %d", i, w.Code)
		}
	}

	// Verify state is unchanged after repeated reads
	snapAfter := s.pool.Snapshot()
	if len(snapAfter.Accounts) != len(snapBefore.Accounts) {
		t.Fatalf("account count changed")
	}
	if snapAfter.Accounts[0].InFlight != snapBefore.Accounts[0].InFlight {
		t.Errorf("in_flight changed: %d -> %d", snapBefore.Accounts[0].InFlight, snapAfter.Accounts[0].InFlight)
	}

	// Token file must not be modified (no refresh, no token-save)
	raw, _ := os.ReadFile(filepath.Join(authDir, "codex-expired.json"))
	if strings.Contains(string(raw), "last_used") || strings.Contains(string(raw), "in_flight") {
		t.Errorf("token file was mutated: %s", string(raw))
	}
	// Token file should still contain the expired token (no refresh happened)
	if !strings.Contains(string(raw), "EXPIRED-ACCESS-SENTINEL") {
		t.Errorf("access token was changed (refresh occurred?): %s", string(raw))
	}
}

// VAL-API-011 regression: Existing routes are not disturbed by pool health addition
func TestPoolHealthRoutes_ExistingRoutesUnaffected(t *testing.T) {
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
	// /v1/models should still work
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	s.Engine().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected /v1/models 200, got %d body=%s", w.Code, w.Body.String())
	}
	// /models should still work
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/models", nil)
	s.Engine().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected /models 200, got %d", w.Code)
	}
	// /health should still work without auth
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/health", nil)
	s.Engine().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected /health 200, got %d", w.Code)
	}
	// /healthz HEAD should still work
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodHead, "/healthz", nil)
	s.Engine().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected /healthz HEAD 200, got %d", w.Code)
	}
}

// ---------------------------------------------------------------------------
// VAL-API-011 comprehensive regression: Route alias and auth smoke matrix
// ---------------------------------------------------------------------------
// Exercises every public route listed in the assertion to confirm that
// adding pool-health and pool/watcher wiring does not change existing
// route registration, alias resolution, auth gating, or response shapes.

func TestRouteAliasAndAuthSmokeMatrix_VAL_API_011(t *testing.T) {
	upstreamSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}`))
	}))
	defer upstreamSrv.Close()

	authDir := t.TempDir()
	// Write a valid Codex token so the pool/watcher wiring is exercised
	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	codexToken := `{"type":"codex","access_token":"smoke-access-SENTINEL","refresh_token":"smoke-refresh-SENTINEL","email":"smoke@codex.example","expired":"` + future + `"}` + "\n"
	if err := os.WriteFile(filepath.Join(authDir, "codex-smoke.json"), []byte(codexToken), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := mustConfig(t, `
client_auth:
  enabled: true
  api_keys:
    - "matrix-key"
oauth:
  auth_dir: `+authDir+`
models:
  - alias: static-chat
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    base_url: `+upstreamSrv.URL+`/v1
  - alias: static-responses
    factory_provider: openai
    upstream_protocol: openai-responses
    base_url: `+upstreamSrv.URL+`/v1
  - alias: codex-resp
    factory_provider: openai
    upstream_protocol: codex-responses
    oauth_provider: codex
    oauth_account: "smoke@codex.example"
    base_url: `+upstreamSrv.URL+`/v1
  - alias: static-msg
    factory_provider: anthropic
    upstream_protocol: anthropic-messages
    base_url: `+upstreamSrv.URL+`/v1
`)

	s, err := New(cfg, discardLogger())
	if err != nil {
		t.Fatalf("server new: %v", err)
	}

	// GET routes that return 200 without body
	for _, tc := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/v1/models"},
		{http.MethodGet, "/models"},
		{http.MethodGet, "/v1/oauth/pool-health"},
		{http.MethodGet, "/oauth/pool-health"},
	} {
		t.Run("auth_ok_"+tc.method+"_"+tc.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(tc.method, tc.path, nil)
			req.Header.Set("Authorization", "Bearer matrix-key")
			s.Engine().ServeHTTP(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
			}
		})
	}

	// POST routes that return 200 with valid body
	for _, tc := range []struct {
		path string
		body string
	}{
		{"/v1/chat/completions", `{"model":"static-chat","messages":[{"role":"user","content":"hi"}],"stream":false}`},
		{"/chat/completions", `{"model":"static-chat","messages":[{"role":"user","content":"hi"}],"stream":false}`},
		{"/v1/responses", `{"model":"static-responses","input":"hi","stream":false}`},
		{"/responses", `{"model":"static-responses","input":"hi","stream":false}`},
		{"/v1/messages", `{"model":"static-msg","messages":[{"role":"user","content":"hi"}],"max_tokens":1}`},
		{"/messages", `{"model":"static-msg","messages":[{"role":"user","content":"hi"}],"max_tokens":1}`},
		{"/v1/messages/count_tokens", `{"model":"static-msg","messages":[{"role":"user","content":"hi"}]}`},
		{"/messages/count_tokens", `{"model":"static-msg","messages":[{"role":"user","content":"hi"}]}`},
	} {
		t.Run("auth_ok_POST_"+tc.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer matrix-key")
			req.Header.Set("Content-Type", "application/json")
			s.Engine().ServeHTTP(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
			}
		})
	}

	// Health endpoints are unauthenticated
	for _, tc := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/health"},
		{http.MethodGet, "/healthz"},
		{http.MethodHead, "/healthz"},
	} {
		t.Run("no_auth_ok_"+tc.method+"_"+tc.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(tc.method, tc.path, nil)
			s.Engine().ServeHTTP(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d", w.Code)
			}
		})
	}

	// Auth-gated routes reject without credentials
	for _, tc := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/v1/models"},
		{http.MethodGet, "/models"},
		{http.MethodGet, "/v1/oauth/pool-health"},
		{http.MethodGet, "/oauth/pool-health"},
		{http.MethodPost, "/v1/chat/completions"},
		{http.MethodPost, "/chat/completions"},
		{http.MethodPost, "/v1/responses"},
		{http.MethodPost, "/responses"},
		{http.MethodPost, "/v1/messages"},
		{http.MethodPost, "/messages"},
		{http.MethodPost, "/v1/messages/count_tokens"},
		{http.MethodPost, "/messages/count_tokens"},
	} {
		t.Run("no_auth_reject_"+tc.method+"_"+tc.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(tc.method, tc.path, nil)
			s.Engine().ServeHTTP(w, req)
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("expected 401, got %d body=%s", w.Code, w.Body.String())
			}
			// Verify consistent error shape
			var errBody map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &errBody); err != nil {
				t.Fatalf("error body is not JSON: %s", w.Body.String())
			}
			errDetail, ok := errBody["error"].(map[string]any)
			if !ok {
				t.Fatalf("missing error.detail: %v", errBody)
			}
			if errDetail["type"] != "authentication_error" {
				t.Errorf("expected type=authentication_error, got %v", errDetail["type"])
			}
		})
	}

	// Unknown model returns 404 with standard error shape
	for _, path := range []string{"/v1/chat/completions", "/chat/completions"} {
		t.Run("unknown_model_"+path, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"model":"nonexistent","messages":[{"role":"user","content":"hi"}]}`))
			req.Header.Set("Authorization", "Bearer matrix-key")
			req.Header.Set("Content-Type", "application/json")
			s.Engine().ServeHTTP(w, req)
			if w.Code != http.StatusNotFound {
				t.Fatalf("expected 404 for unknown model, got %d body=%s", w.Code, w.Body.String())
			}
		})
	}

	// Unsupported provider on wrong route returns expected error
	for _, path := range []string{"/v1/responses", "/responses"} {
		t.Run("unsupported_provider_responses_"+path, func(t *testing.T) {
			body := `{"model":"static-msg","input":"hi"}`
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
			req.Header.Set("Authorization", "Bearer matrix-key")
			req.Header.Set("Content-Type", "application/json")
			s.Engine().ServeHTTP(w, req)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("expected 400 for non-openai factory on /responses, got %d body=%s", w.Code, w.Body.String())
			}
		})
	}
}

// ---------------------------------------------------------------------------
// VAL-CROSS-005: /v1/models OAuth auth introspection remains compatible
// ---------------------------------------------------------------------------
// Verifies that pool/watcher wiring does not regress /v1/models oauth_auth
// introspection fields for Codex and xAI models.

func TestModelsOAuthIntrospection_CodexAndXAI_VAL_CROSS_005(t *testing.T) {
	authDir := t.TempDir()
	now := time.Now()
	future := now.Add(time.Hour).UTC().Format(time.RFC3339)
	past := now.Add(-time.Hour).UTC().Format(time.RFC3339)

	// Write Codex tokens: one active, one disabled, one expired, one active for pinned test
	for _, tok := range []struct {
		filename string
		content  string
	}{
		{"codex-active.json", `{"type":"codex","access_token":"codex-access-ACTIVE","refresh_token":"codex-refresh-ACTIVE","email":"codex-active@example.com","expired":"` + future + `"}`},
		{"codex-disabled.json", `{"type":"codex","access_token":"codex-access-DISABLED","refresh_token":"codex-refresh-DISABLED","email":"codex-disabled@example.com","expired":"` + future + `","disabled":true}`},
		{"codex-expired.json", `{"type":"codex","access_token":"codex-access-EXPIRED","refresh_token":"codex-refresh-EXPIRED","email":"codex-expired@example.com","expired":"` + past + `"}`},
		{"codex-pinned.json", `{"type":"codex","access_token":"codex-access-PINNED","refresh_token":"codex-refresh-PINNED","email":"pinned@codex.example","expired":"` + future + `"}`},
	} {
		if err := os.WriteFile(filepath.Join(authDir, tok.filename), []byte(tok.content+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// Write xAI tokens: one active, one disabled, one expired
	for _, tok := range []struct {
		filename string
		content  string
	}{
		{"xai-active.json", `{"type":"xai","access_token":"xai-access-ACTIVE","refresh_token":"xai-refresh-ACTIVE","email":"xai-active@example.com","expired":"` + future + `"}`},
		{"xai-disabled.json", `{"type":"xai","access_token":"xai-access-DISABLED","refresh_token":"xai-refresh-DISABLED","email":"xai-disabled@example.com","expired":"` + future + `","disabled":true}`},
		{"xai-expired.json", `{"type":"xai","access_token":"xai-access-EXPIRED","refresh_token":"xai-refresh-EXPIRED","email":"xai-expired@example.com","expired":"` + past + `"}`},
	} {
		if err := os.WriteFile(filepath.Join(authDir, tok.filename), []byte(tok.content+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	cfg := mustConfig(t, `
oauth:
  auth_dir: `+authDir+`
models:
  # Static model — no oauth_auth field
  - alias: static-chat
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    base_url: http://127.0.0.1:1/v1

  # Codex model — no pin (all Codex tokens match)
  - alias: codex-unpinned
    factory_provider: openai
    upstream_protocol: codex-responses
    oauth_provider: codex
    base_url: http://127.0.0.1:1/v1

  # Codex model — pinned to specific account
  - alias: codex-pinned
    factory_provider: openai
    upstream_protocol: codex-responses
    oauth_provider: codex
    oauth_account: "pinned@codex.example"
    base_url: http://127.0.0.1:1/v1

  # Codex model — pinned to nonexistent account
  - alias: codex-missing-pin
    factory_provider: openai
    upstream_protocol: codex-responses
    oauth_provider: codex
    oauth_account: "nonexistent@codex.example"
    base_url: http://127.0.0.1:1/v1

  # xAI model — no pin (all xAI tokens match)
  - alias: xai-unpinned
    factory_provider: openai
    upstream_protocol: xai-responses
    oauth_provider: xai
    base_url: http://127.0.0.1:1/v1

  # xAI model — pinned to specific account
  - alias: xai-pinned
    factory_provider: openai
    upstream_protocol: xai-responses
    oauth_provider: xai
    oauth_account: "xai-active@example.com"
    base_url: http://127.0.0.1:1/v1

  # xAI model — pinned to disabled account
  - alias: xai-pinned-disabled
    factory_provider: openai
    upstream_protocol: xai-responses
    oauth_provider: xai
    oauth_account: "xai-disabled@example.com"
    base_url: http://127.0.0.1:1/v1

  # xAI model — pinned to expired account
  - alias: xai-pinned-expired
    factory_provider: openai
    upstream_protocol: xai-responses
    oauth_provider: xai
    oauth_account: "xai-expired@example.com"
    base_url: http://127.0.0.1:1/v1
`)

	s, err := New(cfg, discardLogger())
	if err != nil {
		t.Fatalf("server new: %v", err)
	}

	// Hit /v1/models via both aliases
	for _, path := range []string{"/v1/models", "/models"} {
		t.Run("path_"+path, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, path, nil)
			s.Engine().ServeHTTP(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
			}
			var resp struct {
				Object string                   `json:"object"`
				Data   []map[string]interface{} `json:"data"`
			}
			if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
				t.Fatal(err)
			}
			if resp.Object != "list" {
				t.Fatalf("expected object=list, got %s", resp.Object)
			}

			byID := map[string]map[string]any{}
			for _, m := range resp.Data {
				byID[m["id"].(string)] = m
			}

			// Static model has no oauth_auth
			if _, has := byID["static-chat"]["oauth_auth"]; has {
				t.Error("static-chat should not have oauth_auth field")
			}

			// Codex unpinned: matches all 4 Codex tokens (3 active, 1 disabled, 1 expired)
			// active: codex-active, codex-pinned (not expired)
			// disabled: codex-disabled
			// expired: codex-expired
			codexUnpinned := byID["codex-unpinned"]
			testutil.AssertOAuthHealth(t, codexUnpinned, "codex", "", 4, 2, 1, 1, false)

			// Codex pinned to specific account
			codexPinned := byID["codex-pinned"]
			testutil.AssertOAuthHealth(t, codexPinned, "codex", "pinned@codex.example", 1, 1, 0, 0, false)

			// Codex pinned to nonexistent account → missing_auth
			codexMissingPin := byID["codex-missing-pin"]
			testutil.AssertOAuthHealth(t, codexMissingPin, "codex", "nonexistent@codex.example", 0, 0, 0, 0, true)

			// xAI unpinned: matches all 3 xAI tokens
			// active: xai-active (not expired, not disabled)
			// disabled: xai-disabled
			// expired: xai-expired
			xaiUnpinned := byID["xai-unpinned"]
			testutil.AssertOAuthHealth(t, xaiUnpinned, "xai", "", 3, 1, 1, 1, false)

			// xAI pinned to active account
			xaiPinned := byID["xai-pinned"]
			testutil.AssertOAuthHealth(t, xaiPinned, "xai", "xai-active@example.com", 1, 1, 0, 0, false)

			// xAI pinned to disabled account
			xaiPinnedDisabled := byID["xai-pinned-disabled"]
			testutil.AssertOAuthHealth(t, xaiPinnedDisabled, "xai", "xai-disabled@example.com", 1, 0, 1, 0, false)

			// xAI pinned to expired account
			xaiPinnedExpired := byID["xai-pinned-expired"]
			testutil.AssertOAuthHealth(t, xaiPinnedExpired, "xai", "xai-expired@example.com", 1, 0, 0, 1, false)
		})
	}
}

// VAL-CROSS-005: Verify that models response does not leak secrets through oauth_auth
func TestModelsOAuthIntrospection_NoSecretLeakage_VAL_CROSS_005(t *testing.T) {
	authDir := t.TempDir()
	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)

	secretAccessToken := "****************************"
	secretRefreshToken := "*****************************"
	codexToken := `{"type":"codex","access_token":"` + secretAccessToken + `","refresh_token":"` + secretRefreshToken + `","email":"secret@codex.example","expired":"` + future + `"}` + "\n"
	if err := os.WriteFile(filepath.Join(authDir, "codex-secret.json"), []byte(codexToken), 0o600); err != nil {
		t.Fatal(err)
	}

	xaiToken := `{"type":"xai","access_token":"` + secretAccessToken + `","refresh_token":"` + secretRefreshToken + `","email":"secret@xai.example","expired":"` + future + `"}` + "\n"
	if err := os.WriteFile(filepath.Join(authDir, "xai-secret.json"), []byte(xaiToken), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := mustConfig(t, `
oauth:
  auth_dir: `+authDir+`
models:
  - alias: codex-model
    factory_provider: openai
    upstream_protocol: codex-responses
    oauth_provider: codex
    base_url: http://127.0.0.1:1/v1
  - alias: xai-model
    factory_provider: openai
    upstream_protocol: xai-responses
    oauth_provider: xai
    base_url: http://127.0.0.1:1/v1
`)

	s, err := New(cfg, discardLogger())
	if err != nil {
		t.Fatalf("server new: %v", err)
	}

	for _, path := range []string{"/v1/models", "/models"} {
		t.Run(path, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, path, nil)
			s.Engine().ServeHTTP(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d", w.Code)
			}
			bodyStr := w.Body.String()
			for _, secret := range []string{secretAccessToken, secretRefreshToken} {
				if strings.Contains(bodyStr, secret) {
					t.Errorf("response leaked secret %q via %s: %s", secret, path, bodyStr)
				}
			}
		})
	}
}

// VAL-CROSS-005: Verify /v1/models oauth_auth fields include all required keys
func TestModelsOAuthIntrospection_FieldStructure_VAL_CROSS_005(t *testing.T) {
	authDir := t.TempDir()
	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)

	codexToken := `{"type":"codex","access_token":"tok","refresh_token":"rt","email":"user@codex.example","expired":"` + future + `"}` + "\n"
	if err := os.WriteFile(filepath.Join(authDir, "codex-user.json"), []byte(codexToken), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := mustConfig(t, `
oauth:
  auth_dir: `+authDir+`
models:
  - alias: codex-model
    factory_provider: openai
    upstream_protocol: codex-responses
    oauth_provider: codex
    oauth_account: "user@codex.example"
    base_url: http://127.0.0.1:1/v1
`)

	s, err := New(cfg, discardLogger())
	if err != nil {
		t.Fatalf("server new: %v", err)
	}

	for _, path := range []string{"/v1/models", "/models"} {
		t.Run(path, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, path, nil)
			s.Engine().ServeHTTP(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
			}
			var resp struct {
				Data []map[string]any `json:"data"`
			}
			if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
				t.Fatal(err)
			}

			oauthAuth := resp.Data[0]["oauth_auth"].(map[string]any)

			// Verify all required fields are present
			requiredFields := []string{
				"provider",
				"pinned_account",
				"matching_account_count",
				"active_count",
				"disabled_count",
				"expired_or_expiring_count",
				"missing_auth",
			}
			for _, field := range requiredFields {
				if _, ok := oauthAuth[field]; !ok {
					t.Errorf("missing required oauth_auth field: %s", field)
				}
			}

			// Verify specific values
			if oauthAuth["provider"] != "codex" {
				t.Errorf("expected provider=codex, got %v", oauthAuth["provider"])
			}
			if oauthAuth["pinned_account"] != "user@codex.example" {
				t.Errorf("expected pinned_account=user@codex.example, got %v", oauthAuth["pinned_account"])
			}
			if oauthAuth["missing_auth"] != false {
				t.Errorf("expected missing_auth=false, got %v", oauthAuth["missing_auth"])
			}
			if int(oauthAuth["matching_account_count"].(float64)) != 1 {
				t.Errorf("expected matching_account_count=1, got %v", oauthAuth["matching_account_count"])
			}
			if int(oauthAuth["active_count"].(float64)) != 1 {
				t.Errorf("expected active_count=1, got %v", oauthAuth["active_count"])
			}
			if int(oauthAuth["disabled_count"].(float64)) != 0 {
				t.Errorf("expected disabled_count=0, got %v", oauthAuth["disabled_count"])
			}
			if int(oauthAuth["expired_or_expiring_count"].(float64)) != 0 {
				t.Errorf("expected expired_or_expiring_count=0, got %v", oauthAuth["expired_or_expiring_count"])
			}
		})
	}
}
