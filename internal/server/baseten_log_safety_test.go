package server

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sirupsen/logrus"
)

// ---------------------------------------------------------------------------
// VAL-BASETEN-015: Baseten credentials and protected headers cannot leak or be
// overridden.
//
// These tests prove that default (access-log only) and bounded redacted trace
// logging across representative success, tool, streaming, query-credential,
// local-error, and upstream-error requests expose none of the synthetic
// credentials originating in local env, client auth, configured headers, or
// credential-named request query/body/response fields, while retaining useful
// method/path/status context.
//
// Per the contract: "ordinary opaque prompt/tool content is not falsely claimed
// to be sanitized from a body that must relay exactly." Only credential-shaped
// values (env secrets, auth headers, query/body credential-named fields, and
// credential-named response fields) must be absent from logs.
//
// All tests are mock-only and use httptest (OS-assigned ports) with complete
// teardown. No provider-specific handler, SDK, or transport is introduced.
// ---------------------------------------------------------------------------

// btCredentialSentinels are unique synthetic values placed in credential-bearing
// locations (env, client auth, query, body credential fields, response
// credential-named fields, and error credential fields). None must ever appear
// in captured logs.
var btCredentialSentinels = []string{
	"bt_envlog_secret_xyz",       // provider credential (BASETEN_API_KEY)
	"bt_querylog_secret_abcde",   // query-string credential (?api_key=)
	"bt_bodylog_secret_fghij",    // request-body credential field ("apiKey":)
	"bt_clientlog_token_uvwxy",   // downstream client auth header
	"bt_resplog_token_klmno",     // credential-named field in response body ("token":)
	"bt_errlog_credential_pqrst", // credential-named field in error body ("api_key":)
}

func assertNoBasetenLogSentinels(t *testing.T, logs string) {
	t.Helper()
	for _, s := range btCredentialSentinels {
		if strings.Contains(logs, s) {
			t.Fatalf("log output leaked credential sentinel %q:\n%s", s, logs)
		}
	}
}

// newBasetenLogServer builds a real server with a Baseten model pointing at the
// given upstream handler. The logger writes to the returned buffer. If trace
// is true, trace-request logging is enabled.
func newBasetenLogServer(t *testing.T, upstreamHandler http.HandlerFunc, trace bool) (*Server, *bytes.Buffer) {
	t.Helper()

	upstreamSrv := httptest.NewServer(upstreamHandler)
	t.Cleanup(upstreamSrv.Close)

	traceYAML := ""
	if trace {
		traceYAML = "  trace_requests: true\n  redact: true\n"
	}

	cfg := mustConfig(t, `
logging:
  level: debug
`+traceYAML+`models:
  - alias: bt-log-test
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    known_auth: baseten
    upstream_model: org/log-test-model
    base_url: `+upstreamSrv.URL+`/v1
`)

	var logs bytes.Buffer
	logger := logrus.New()
	logger.SetOutput(&logs)
	if trace {
		logger.SetLevel(logrus.DebugLevel)
	} else {
		logger.SetLevel(logrus.InfoLevel)
	}

	s, err := New(cfg, logger)
	if err != nil {
		t.Fatalf("server new: %v", err)
	}
	return s, &logs
}

// btRespBodyWithCredField is a success response that contains ordinary content
// plus a credential-named field. The credential value must be redacted in trace
// logs; the ordinary content is legitimate response data that the trace log
// captures as useful context.
const btRespBodyWithCredField = `{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"ordinary baseten response text"}}],"token":"bt_resplog_token_klmno"}`

// TestBasetenLogSafety_DefaultLogging_NoCredentialLeak exercises default
// (access-log only) logging across representative request cases and verifies
// that no synthetic credential appears in any log line, while useful
// method/path/status context is retained. Default logging never includes
// request/response bodies.
func TestBasetenLogSafety_DefaultLogging_NoCredentialLeak(t *testing.T) {
	t.Setenv("BASETEN_API_KEY", "bt_envlog_secret_xyz")

	s, logs := newBasetenLogServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(btRespBodyWithCredField))
	}, false)

	cases := []struct {
		name string
		path string
		body string
	}{
		{
			name: "success",
			path: "/v1/chat/completions",
			body: `{"model":"bt-log-test","messages":[{"role":"user","content":"hi"}]}`,
		},
		{
			name: "tool_call",
			path: "/v1/chat/completions",
			body: `{"model":"bt-log-test","messages":[{"role":"user","content":"call tool"}],"tools":[{"type":"function","function":{"name":"f","parameters":{"type":"object"}}}]}`,
		},
		{
			name: "query_credential",
			path: "/v1/chat/completions?api_key=bt_querylog_secret_abcde",
			body: `{"model":"bt-log-test","apiKey":"bt_bodylog_secret_fghij","messages":[]}`,
		},
		{
			name: "local_error_missing_model",
			path: "/v1/chat/completions",
			body: `{"messages":[]}`,
		},
		{
			name: "local_error_unknown_alias",
			path: "/v1/chat/completions",
			body: `{"model":"nonexistent","messages":[]}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logs.Reset()

			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer bt_clientlog_token_uvwxy")
			s.Engine().ServeHTTP(w, req)

			// No credential sentinel may appear in default logs.
			assertNoBasetenLogSentinels(t, logs.String())

			// Default logs must retain useful method/path/status context.
			if !strings.Contains(logs.String(), "POST") {
				t.Errorf("default log missing method context:\n%s", logs.String())
			}
			if !strings.Contains(logs.String(), "/v1/chat/completions") {
				t.Errorf("default log missing path context:\n%s", logs.String())
			}
			// Default logs must NOT include body or credential field names.
			if strings.Contains(logs.String(), "apiKey") {
				t.Errorf("default log leaked body field name:\n%s", logs.String())
			}
			if strings.Contains(logs.String(), "ordinary baseten response text") {
				t.Errorf("default log leaked response body content:\n%s", logs.String())
			}
		})
	}
}

// TestBasetenLogSafety_TraceLogging_NoCredentialLeak exercises bounded
// redacted trace logging across representative success, tool, and
// query-credential cases. Credential sentinels are absent in all logs while
// redaction placeholders and useful method/path/status context are present.
// Ordinary response content IS captured (it is not a credential).
func TestBasetenLogSafety_TraceLogging_NoCredentialLeak(t *testing.T) {
	t.Setenv("BASETEN_API_KEY", "bt_envlog_secret_xyz")

	s, logs := newBasetenLogServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(btRespBodyWithCredField))
	}, true)

	cases := []struct {
		name           string
		path           string
		body           string
		expectRedacted bool // true when request has credentials that must be redacted
	}{
		{
			name: "success",
			path: "/v1/chat/completions",
			body: `{"model":"bt-log-test","messages":[{"role":"user","content":"hi"}]}`,
		},
		{
			name: "tool_call",
			path: "/v1/chat/completions",
			body: `{"model":"bt-log-test","messages":[{"role":"user","content":"call tool"}],"tools":[{"type":"function","function":{"name":"f","parameters":{"type":"object"}}}]}`,
		},
		{
			name:           "query_credential",
			path:           "/v1/chat/completions?api_key=bt_querylog_secret_abcde",
			body:           `{"model":"bt-log-test","apiKey":"bt_bodylog_secret_fghij","messages":[]}`,
			expectRedacted: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logs.Reset()

			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer bt_clientlog_token_uvwxy")
			s.Engine().ServeHTTP(w, req)

			// No credential sentinel in trace logs.
			assertNoBasetenLogSentinels(t, logs.String())

			// Trace logs must retain useful method/path/status context.
			if !strings.Contains(logs.String(), "POST") {
				t.Errorf("trace log missing method context:\n%s", logs.String())
			}
			if !strings.Contains(logs.String(), "/v1/chat/completions") {
				t.Errorf("trace log missing path context:\n%s", logs.String())
			}

			if tc.expectRedacted {
				// Credential fields in query/body must be redacted.
				if !strings.Contains(logs.String(), "***") {
					t.Errorf("trace log missing redaction placeholder for credentials:\n%s", logs.String())
				}
			}

			// The response body's credential-named field must be redacted.
			// "token":"bt_resplog_token_klmno" should become "token":"***".
			if strings.Contains(logs.String(), "bt_resplog_token_klmno") {
				t.Errorf("trace log leaked response credential-named field:\n%s", logs.String())
			}
			// Ordinary response content IS captured as useful trace context.
			if !strings.Contains(logs.String(), "ordinary baseten response text") {
				t.Errorf("trace log missing ordinary response content (over-redacted):\n%s", logs.String())
			}
		})
	}
}

// TestBasetenLogSafety_TraceStreaming_NoCredentialLeak verifies that trace
// logging of an SSE streaming response does not leak credential sentinels.
// The SSE frames contain a credential-named field that must be redacted.
func TestBasetenLogSafety_TraceStreaming_NoCredentialLeak(t *testing.T) {
	t.Setenv("BASETEN_API_KEY", "bt_envlog_secret_xyz")

	sseFrames := []string{
		`data: {"id":"1","choices":[{"index":0,"delta":{"content":"baseten streaming content"}}]}`,
		`data: {"id":"1","token":"bt_resplog_token_klmno"}`,
		`data: [DONE]`,
	}

	s, logs := newBasetenLogServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		for _, frame := range sseFrames {
			fmt.Fprintf(w, "%s\n\n", frame)
			flusher.Flush()
		}
	}, true)

	logs.Reset()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"bt-log-test","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer bt_clientlog_token_uvwxy")
	s.Engine().ServeHTTP(w, req)

	// No credential sentinel in trace logs.
	assertNoBasetenLogSentinels(t, logs.String())

	if !strings.Contains(logs.String(), "POST") {
		t.Errorf("trace stream log missing method context:\n%s", logs.String())
	}
	// Ordinary SSE content is captured.
	if !strings.Contains(logs.String(), "baseten streaming content") {
		t.Errorf("trace stream log missing ordinary content:\n%s", logs.String())
	}
}

// TestBasetenLogSafety_UpstreamError_NoCredentialLeak verifies that an
// upstream error response whose body contains a credential-shaped field is
// redacted in both default and trace logs. The error body is relayed
// byte-for-byte downstream (provider-owned), but the log must be clean.
func TestBasetenLogSafety_UpstreamError_NoCredentialLeak(t *testing.T) {
	t.Setenv("BASETEN_API_KEY", "bt_envlog_secret_xyz")

	errBody := `{"error":{"message":"rate limited","type":"rate_limit_error"},"api_key":"bt_errlog_credential_pqrst"}`

	for _, trace := range []bool{false, true} {
		label := "default"
		if trace {
			label = "trace"
		}
		t.Run(label, func(t *testing.T) {
			s, logs := newBasetenLogServer(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Retry-After", "30")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(errBody))
			}, trace)

			logs.Reset()
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
				strings.NewReader(`{"model":"bt-log-test","messages":[]}`))
			req.Header.Set("Authorization", "Bearer bt_clientlog_token_uvwxy")
			s.Engine().ServeHTTP(w, req)

			if w.Code != http.StatusTooManyRequests {
				t.Fatalf("expected 429, got %d", w.Code)
			}
			assertNoBasetenLogSentinels(t, logs.String())

			// The error body is relayed byte-for-byte downstream (provider-owned).
			if !strings.Contains(w.Body.String(), "rate limited") {
				t.Errorf("error response body not relayed:\n%s", w.Body.String())
			}
		})
	}
}

// TestBasetenLogSafety_MissingKeyFailsLocally verifies that missing profile
// key produces a local error with zero upstream requests and no credential
// leakage in logs.
func TestBasetenLogSafety_MissingKeyFailsLocally(t *testing.T) {
	// Do NOT set BASETEN_API_KEY.

	upstreamCalled := false
	s, logs := newBasetenLogServer(t, func(w http.ResponseWriter, r *http.Request) {
		upstreamCalled = true
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"x","choices":[]}`))
	}, true)

	logs.Reset()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"bt-log-test","messages":[]}`))
	s.Engine().ServeHTTP(w, req)

	if upstreamCalled {
		t.Fatal("upstream must not be called when BASETEN_API_KEY is missing")
	}
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 for missing key, got %d", w.Code)
	}

	assertNoBasetenLogSentinels(t, logs.String())
}
