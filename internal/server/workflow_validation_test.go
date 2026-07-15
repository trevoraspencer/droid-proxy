package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/trevoraspencer/droid-proxy/internal/config"
)

type validationCapture struct {
	Method       string
	Path         string
	Headers      http.Header
	Body         string
	Scripted     string
	Downstream   string
	TerminalSeen bool
}

type validationFakeUpstream struct {
	server   *httptest.Server
	mu       sync.Mutex
	captures []validationCapture
}

func newValidationFakeUpstream(t *testing.T) *validationFakeUpstream {
	t.Helper()
	f := &validationFakeUpstream{}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		cap := validationCapture{
			Method:  r.Method,
			Path:    r.URL.Path,
			Headers: r.Header.Clone(),
			Body:    string(body),
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/chat/completions", "/chat/completions":
			cap.Scripted = `{"id":"chat_fake","choices":[{"message":{"role":"assistant","content":"chat ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`
		case "/v1/responses", "/responses":
			cap.Scripted = `{"id":"resp_fake","object":"response","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"responses ok"}]}]}`
		case "/v1/messages", "/messages":
			cap.Scripted = `{"id":"msg_fake","type":"message","role":"assistant","content":[{"type":"text","text":"messages ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`
		case "/v1/messages/count_tokens", "/messages/count_tokens":
			cap.Scripted = `{"input_tokens":7}`
		default:
			http.NotFound(w, r)
			return
		}
		f.mu.Lock()
		f.captures = append(f.captures, cap)
		f.mu.Unlock()
		_, _ = w.Write([]byte(cap.Scripted))
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *validationFakeUpstream) URL() string {
	return f.server.URL
}

func (f *validationFakeUpstream) capturesSnapshot() []validationCapture {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]validationCapture, len(f.captures))
	copy(out, f.captures)
	return out
}

func TestWorkflowValidation_EndpointMatrixFakeUpstreamWireCapture(t *testing.T) {
	fake := newValidationFakeUpstream(t)
	cfg := workflowValidationConfig(t, fake.URL(), 0)
	assertLoopbackOnlyValidationUpstreams(t, cfg)
	s, err := New(cfg, discardLogger())
	if err != nil {
		t.Fatalf("server new: %v", err)
	}

	cases := []struct {
		method       string
		path         string
		body         string
		wantStatus   int
		wantBody     string
		wantTerminal string
		wantUpstream string
	}{
		{http.MethodGet, "/health", "", http.StatusOK, `"status":"ok"`, "", ""},
		{http.MethodGet, "/healthz", "", http.StatusOK, `"status":"ok"`, "", ""},
		{http.MethodHead, "/healthz", "", http.StatusOK, "", "", ""},
		{http.MethodGet, "/v1/models", "", http.StatusOK, `"id":"droid-chat"`, "", ""},
		{http.MethodGet, "/models", "", http.StatusOK, `"id":"droid-openai"`, "", ""},
		{http.MethodPost, "/v1/chat/completions", `{"model":"droid-chat","messages":[{"role":"user","content":"hi"}]}`, http.StatusOK, "chat ok", "chat ok", "/chat/completions"},
		{http.MethodPost, "/chat/completions", `{"model":"droid-chat","messages":[{"role":"user","content":"hi"}]}`, http.StatusOK, "chat ok", "chat ok", "/chat/completions"},
		{http.MethodPost, "/v1/responses", `{"model":"droid-openai","input":"hi"}`, http.StatusOK, "responses ok", `"status":"completed"`, "/responses"},
		{http.MethodPost, "/responses", `{"model":"droid-openai","input":"hi"}`, http.StatusOK, "responses ok", `"status":"completed"`, "/responses"},
		{http.MethodPost, "/v1/messages", `{"model":"droid-anthropic","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`, http.StatusOK, "messages ok", `"stop_reason":"end_turn"`, "/v1/messages"},
		{http.MethodPost, "/messages", `{"model":"droid-anthropic","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`, http.StatusOK, "messages ok", `"stop_reason":"end_turn"`, "/v1/messages"},
		{http.MethodPost, "/v1/messages/count_tokens", `{"model":"droid-anthropic","messages":[{"role":"user","content":"hi"}]}`, http.StatusOK, `"input_tokens":7`, `"input_tokens"`, "/v1/messages/count_tokens"},
		{http.MethodPost, "/messages/count_tokens", `{"model":"droid-anthropic","messages":[{"role":"user","content":"hi"}]}`, http.StatusOK, `"input_tokens":7`, `"input_tokens"`, "/v1/messages/count_tokens"},
	}

	var downstream []validationCapture
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			s.Engine().ServeHTTP(w, req)
			if w.Code != tc.wantStatus {
				t.Fatalf("status=%d want=%d body=%s", w.Code, tc.wantStatus, w.Body.String())
			}
			if tc.wantBody != "" && !strings.Contains(w.Body.String(), tc.wantBody) {
				t.Fatalf("body missing %q: %s", tc.wantBody, w.Body.String())
			}
			if tc.wantTerminal != "" && !strings.Contains(w.Body.String(), tc.wantTerminal) {
				t.Fatalf("terminal marker/body shape %q missing from %s", tc.wantTerminal, w.Body.String())
			}
			downstream = append(downstream, validationCapture{
				Method:       tc.method,
				Path:         tc.path,
				Body:         tc.body,
				Downstream:   w.Body.String(),
				TerminalSeen: tc.wantTerminal == "" || strings.Contains(w.Body.String(), tc.wantTerminal),
			})
		})
	}

	captures := fake.capturesSnapshot()
	if len(captures) != 8 {
		t.Fatalf("fake upstream capture count=%d want 8 captures=%#v", len(captures), captures)
	}
	wantUpstreamPaths := make(map[string]int)
	for _, tc := range cases {
		if tc.wantUpstream != "" {
			wantUpstreamPaths[tc.wantUpstream]++
		}
	}
	for _, cap := range captures {
		if cap.Method != http.MethodPost {
			t.Fatalf("fake upstream saw non-POST request: %#v", cap)
		}
		wantUpstreamPaths[cap.Path]--
		if cap.Headers.Get("Authorization") == "" && cap.Headers.Get("x-api-key") == "" {
			t.Fatalf("fake upstream capture lacks provider auth evidence: %#v", cap)
		}
		if cap.Body == "" || cap.Scripted == "" {
			t.Fatalf("fake upstream capture lacks request/scripted body evidence: %#v", cap)
		}
	}
	for path, delta := range wantUpstreamPaths {
		if delta != 0 {
			t.Fatalf("fake upstream path %s unmatched delta=%d captures=%#v", path, delta, captures)
		}
	}
	if len(downstream) != len(cases) {
		t.Fatalf("downstream transcript count=%d want=%d", len(downstream), len(cases))
	}
}

func TestWorkflowValidation_FactorySettingsExamplesDriveRuntimeEndpoints(t *testing.T) {
	fake := newValidationFakeUpstream(t)
	settings := loadFactorySettingsExamples(t)
	cfg := workflowValidationConfigFromFactorySettings(t, fake.URL(), 0, settings)
	assertLoopbackOnlyValidationUpstreams(t, cfg)
	s, err := New(cfg, discardLogger())
	if err != nil {
		t.Fatalf("server new: %v", err)
	}

	modelBodies := map[string]string{}
	for _, path := range []string{"/v1/models", "/models"} {
		w := httptest.NewRecorder()
		s.Engine().ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("%s failed: status=%d body=%s", path, w.Code, w.Body.String())
		}
		modelBodies[path] = w.Body.String()
	}

	for _, example := range settings {
		t.Run(example.File, func(t *testing.T) {
			for _, cm := range example.CustomModels {
				if cm.BaseURL != "http://127.0.0.1:9787" || strings.Contains(cm.APIKey, "sk-") {
					t.Fatalf("unsafe Factory settings example: %+v", cm)
				}
				for path, body := range modelBodies {
					if !strings.Contains(body, `"id":"`+cm.Model+`"`) {
						t.Fatalf("%s does not expose documented settings alias %q: %s", path, cm.Model, body)
					}
				}
				before := len(fake.capturesSnapshot())
				postPath, body := endpointForFactoryProvider(cm.Provider, cm.Model)
				w := httptest.NewRecorder()
				s.Engine().ServeHTTP(w, httptest.NewRequest(http.MethodPost, postPath, strings.NewReader(body)))
				if w.Code != http.StatusOK {
					t.Fatalf("settings-driven endpoint %s for alias %s failed: status=%d body=%s", postPath, cm.Model, w.Code, w.Body.String())
				}
				captures := fake.capturesSnapshot()
				if len(captures) != before+1 {
					t.Fatalf("settings alias %s did not drive exactly one runtime upstream request: before=%d after=%d", cm.Model, before, len(captures))
				}
				if captures[len(captures)-1].Body == "" || !strings.Contains(captures[len(captures)-1].Scripted, "ok") {
					t.Fatalf("missing wire evidence for settings alias %s: %#v", cm.Model, captures[len(captures)-1])
				}
			}
		})
	}
}

func TestWorkflowValidation_GuardRejectsRealProviderURLs(t *testing.T) {
	for _, rawURL := range []string{
		"https://api.openai.com/v1",
		"https://api.anthropic.com/v1",
		"https://api.deepseek.com/v1",
	} {
		t.Run(rawURL, func(t *testing.T) {
			cfg := workflowValidationConfig(t, "http://127.0.0.1:8788", 0)
			cfg.Models[0].BaseURL = rawURL
			if err := validateLoopbackOnlyValidationUpstreams(cfg); err == nil {
				t.Fatalf("validation guard accepted real provider URL %s", rawURL)
			}
		})
	}
}

func TestWorkflowValidation_EndToEndDroidProviderWorkflows(t *testing.T) {
	fake := newDroidWorkflowFakeUpstream(t)
	cfg := droidWorkflowValidationConfig(t, fake.URL(), false)
	assertLoopbackOnlyValidationUpstreams(t, cfg)
	s, err := New(cfg, discardLogger())
	if err != nil {
		t.Fatalf("server new: %v", err)
	}

	type workflowCase struct {
		name          string
		path          string
		body          string
		wantFragments []string
		wantEvents    []string
		wantUpstream  string
	}
	cases := []workflowCase{
		{
			name:          "generic chat text prefixed",
			path:          "/v1/chat/completions",
			body:          `{"model":"droid-chat","messages":[{"role":"user","content":"hello"}]}`,
			wantFragments: []string{"chat text ok"},
			wantUpstream:  "/chat/completions",
		},
		{
			name:          "generic chat streaming tool prefixless",
			path:          "/chat/completions",
			body:          `{"model":"droid-chat","stream":true,"messages":[{"role":"user","content":"tool please"}],"tools":[{"type":"function","function":{"name":"lookup"}}]}`,
			wantFragments: []string{"call_chat", "[DONE]"},
			wantUpstream:  "/chat/completions",
		},
		{
			name:          "generic chat tool result followup",
			path:          "/v1/chat/completions",
			body:          `{"model":"droid-chat","messages":[{"role":"user","content":"tool please"},{"role":"assistant","tool_calls":[{"id":"call_chat","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"chat\"}"}}]},{"role":"tool","tool_call_id":"call_chat","content":"tool result"}]}`,
			wantFragments: []string{"chat followup ok"},
			wantUpstream:  "/chat/completions",
		},
		{
			name:          "native responses text",
			path:          "/v1/responses",
			body:          `{"model":"droid-openai-native","input":"hello"}`,
			wantFragments: []string{"native responses ok", `"status":"completed"`},
			wantUpstream:  "/responses",
		},
		{
			name:         "native responses stream prefixless",
			path:         "/responses",
			body:         `{"model":"droid-openai-native","stream":true,"input":"hello"}`,
			wantEvents:   []string{"response.created", "response.output_text.delta", "response.completed"},
			wantUpstream: "/responses",
		},
		{
			name:          "responses over chat text",
			path:          "/v1/responses",
			body:          `{"model":"droid-openai-chat","input":"hello"}`,
			wantFragments: []string{"translated responses ok", `"status":"completed"`},
			wantUpstream:  "/chat/completions",
		},
		{
			name:         "responses over chat streaming tool prefixless",
			path:         "/responses",
			body:         `{"model":"droid-openai-chat","stream":true,"input":"tool please","tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}]}`,
			wantEvents:   []string{"response.output_item.added", "response.function_call_arguments.delta", "response.output_item.done", "response.completed"},
			wantUpstream: "/chat/completions",
		},
		{
			name:          "responses over chat tool result followup",
			path:          "/v1/responses",
			body:          `{"model":"droid-openai-chat","input":[{"role":"user","content":"tool please"},{"type":"function_call","call_id":"call_resp","name":"lookup","arguments":"{\"q\":\"responses\"}"},{"type":"function_call_output","call_id":"call_resp","output":"tool result"}]}`,
			wantFragments: []string{"responses followup ok"},
			wantUpstream:  "/chat/completions",
		},
		{
			name:          "native anthropic message",
			path:          "/v1/messages",
			body:          `{"model":"droid-anthropic-native","max_tokens":32,"messages":[{"role":"user","content":"hello"}]}`,
			wantFragments: []string{"native anthropic ok", `"stop_reason":"end_turn"`},
			wantUpstream:  "/v1/messages",
		},
		{
			name:          "native anthropic count tokens prefixless",
			path:          "/messages/count_tokens",
			body:          `{"model":"droid-anthropic-native","messages":[{"role":"user","content":"hello"}]}`,
			wantFragments: []string{`"input_tokens":11`},
			wantUpstream:  "/v1/messages/count_tokens",
		},
		{
			name:         "native anthropic stream",
			path:         "/v1/messages",
			body:         `{"model":"droid-anthropic-native","stream":true,"max_tokens":32,"messages":[{"role":"user","content":"hello"}]}`,
			wantEvents:   []string{"message_start", "content_block_delta", "message_stop"},
			wantUpstream: "/v1/messages",
		},
		{
			name:          "anthropic over chat text prefixless",
			path:          "/messages",
			body:          `{"model":"droid-anthropic-chat","max_tokens":32,"messages":[{"role":"user","content":"hello"}]}`,
			wantFragments: []string{"translated anthropic ok", `"stop_reason":"end_turn"`},
			wantUpstream:  "/chat/completions",
		},
		{
			name:         "anthropic over chat streaming tool",
			path:         "/v1/messages",
			body:         `{"model":"droid-anthropic-chat","stream":true,"max_tokens":32,"messages":[{"role":"user","content":"tool please"}],"tools":[{"name":"lookup","input_schema":{"type":"object"}}]}`,
			wantEvents:   []string{"content_block_start", "content_block_delta", "message_stop"},
			wantUpstream: "/chat/completions",
		},
		{
			name:          "anthropic over chat tool result followup",
			path:          "/messages",
			body:          `{"model":"droid-anthropic-chat","max_tokens":32,"messages":[{"role":"user","content":"tool please"},{"role":"assistant","content":[{"type":"tool_use","id":"toolu_workflow","name":"lookup","input":{"q":"anthropic"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_workflow","content":"tool result"}]}]}`,
			wantFragments: []string{"anthropic followup ok"},
			wantUpstream:  "/chat/completions",
		},
		{
			name:          "chat backed count tokens local fallback",
			path:          "/v1/messages/count_tokens",
			body:          `{"model":"droid-anthropic-chat","messages":[{"role":"user","content":"hello workflow validation"}]}`,
			wantFragments: []string{`"input_tokens":`},
			wantUpstream:  "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := fake.count()
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			s.Engine().ServeHTTP(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			for _, want := range tc.wantFragments {
				if !strings.Contains(w.Body.String(), want) {
					t.Fatalf("downstream transcript missing %q:\n%s", want, w.Body.String())
				}
			}
			if len(tc.wantEvents) > 0 {
				events := parseWorkflowSSE(t, w.Body.String())
				for _, want := range tc.wantEvents {
					if !workflowHasEvent(events, want) {
						t.Fatalf("SSE transcript missing event %q: %#v\n%s", want, events, w.Body.String())
					}
				}
				if strings.Contains(w.Body.String(), `"choices"`) && (strings.Contains(tc.name, "responses over chat") || strings.Contains(tc.name, "anthropic over chat")) {
					t.Fatalf("translated stream leaked raw Chat choices:\n%s", w.Body.String())
				}
			}
			after := fake.count()
			if tc.wantUpstream == "" {
				if after != before {
					t.Fatalf("expected no upstream call, count before=%d after=%d", before, after)
				}
				return
			}
			cap := fake.lastCapture(t)
			if cap.Path != tc.wantUpstream {
				t.Fatalf("upstream path=%s want=%s capture=%#v", cap.Path, tc.wantUpstream, cap)
			}
			if cap.Method != http.MethodPost || cap.Body == "" || cap.Scripted == "" || cap.Downstream == "" {
				t.Fatalf("wire capture lacks method/body/scripted/downstream evidence: %#v", cap)
			}
			if strings.Contains(tc.body, `"role":"tool"`) || strings.Contains(tc.body, "function_call_output") || strings.Contains(tc.body, "tool_result") {
				if !strings.Contains(cap.Body, `"role":"tool"`) && !strings.Contains(cap.Body, "tool result") {
					t.Fatalf("tool result was not preserved in upstream request: %s", cap.Body)
				}
			}
		})
	}
}

func TestWorkflowValidation_DeepSeekReasoningToolWorkflowIsolation(t *testing.T) {
	fake := newDroidWorkflowFakeUpstream(t)
	cfg := droidWorkflowValidationConfig(t, fake.URL(), false)
	assertLoopbackOnlyValidationUpstreams(t, cfg)
	s, err := New(cfg, discardLogger())
	if err != nil {
		t.Fatalf("server new: %v", err)
	}

	nonStreamFirst := `{"model":"droid-deepseek","conversation_id":"conv-nonstream","messages":[{"role":"user","content":"weather nonstream?"}]}`
	w := httptest.NewRecorder()
	s.Engine().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(nonStreamFirst)))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "call_deepseek") {
		t.Fatalf("non-stream DeepSeek reasoning/tool first turn failed: status=%d body=%s", w.Code, w.Body.String())
	}
	nonStreamFollowup := `{"model":"droid-deepseek","conversation_id":"conv-nonstream","messages":[{"role":"user","content":"weather nonstream?"},{"role":"assistant","content":"","tool_calls":[{"id":"call_deepseek","type":"function","function":{"name":"lookup","arguments":"{\"city\":\"sf\"}"}}]},{"role":"tool","tool_call_id":"call_deepseek","content":"72F"}]}`
	w = httptest.NewRecorder()
	s.Engine().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/chat/completions", strings.NewReader(nonStreamFollowup)))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "deepseek followup ok") {
		t.Fatalf("non-stream DeepSeek follow-up failed: status=%d body=%s", w.Code, w.Body.String())
	}
	if got := fake.lastBodyForModel("deepseek-upstream"); !strings.Contains(got, `"reasoning_content":"deepseek workflow reasoning nonstream"`) {
		t.Fatalf("non-stream matching session did not replay captured reasoning: %s", got)
	}
	nonStreamCrossSession := strings.Replace(nonStreamFollowup, "conv-nonstream", "conv-nonstream-other", 1)
	w = httptest.NewRecorder()
	s.Engine().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(nonStreamCrossSession)))
	if w.Code != http.StatusOK {
		t.Fatalf("non-stream cross-session follow-up failed: status=%d body=%s", w.Code, w.Body.String())
	}
	if got := fake.lastBodyForModel("deepseek-upstream"); strings.Contains(got, "deepseek workflow reasoning nonstream") {
		t.Fatalf("non-stream reasoning leaked across sessions: %s", got)
	}

	first := `{"model":"droid-deepseek","stream":true,"conversation_id":"conv-a","messages":[{"role":"user","content":"weather?"}]}`
	w = httptest.NewRecorder()
	s.Engine().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(first)))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "call_deepseek") || !strings.Contains(w.Body.String(), "[DONE]") {
		t.Fatalf("first DeepSeek stream failed: status=%d body=%s", w.Code, w.Body.String())
	}
	second := `{"model":"droid-deepseek","conversation_id":"conv-a","messages":[{"role":"user","content":"weather?"},{"role":"assistant","content":"","tool_calls":[{"id":"call_deepseek","type":"function","function":{"name":"lookup","arguments":"{\"city\":\"sf\"}"}}]},{"role":"tool","tool_call_id":"call_deepseek","content":"72F"}]}`
	w = httptest.NewRecorder()
	s.Engine().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/chat/completions", strings.NewReader(second)))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "deepseek followup ok") {
		t.Fatalf("DeepSeek follow-up failed: status=%d body=%s", w.Code, w.Body.String())
	}
	if got := fake.lastBodyForModel("deepseek-upstream"); !strings.Contains(got, `"reasoning_content":"deepseek workflow reasoning"`) {
		t.Fatalf("matching session did not replay captured reasoning into follow-up: %s", got)
	}
	crossSession := `{"model":"droid-deepseek","conversation_id":"conv-b","messages":[{"role":"user","content":"weather?"},{"role":"assistant","content":"","tool_calls":[{"id":"call_deepseek","type":"function","function":{"name":"lookup","arguments":"{\"city\":\"sf\"}"}}]},{"role":"tool","tool_call_id":"call_deepseek","content":"72F"}]}`
	w = httptest.NewRecorder()
	s.Engine().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(crossSession)))
	if w.Code != http.StatusOK {
		t.Fatalf("cross-session follow-up failed: status=%d body=%s", w.Code, w.Body.String())
	}
	if got := fake.lastBodyForModel("deepseek-upstream"); strings.Contains(got, "deepseek workflow reasoning") || strings.Contains(got, "reasoning_content") {
		t.Fatalf("reasoning leaked across sessions: %s", got)
	}
	models := httptest.NewRecorder()
	s.Engine().ServeHTTP(models, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if models.Code != http.StatusOK || !strings.Contains(models.Body.String(), `"id":"droid-deepseek"`) || !strings.Contains(models.Body.String(), `"agent_ready":true`) {
		t.Fatalf("models output missing DeepSeek readiness evidence: status=%d body=%s", models.Code, models.Body.String())
	}
}

func TestWorkflowValidation_ErrorTruncationAuthRedactionAndHardeningJourneys(t *testing.T) {
	fake := newDroidWorkflowFakeUpstream(t)
	cfg := droidWorkflowValidationConfig(t, fake.URL(), true)
	cfg.Logging.TraceRequests = true
	cfg.Logging.Redact = true
	cfg.Server.RequestBodyMaxBytes = 512
	cfg.Upstream.HTTPTimeout = 50 * time.Millisecond
	assertLoopbackOnlyValidationUpstreams(t, cfg)
	var logs bytes.Buffer
	logger := logrus.New()
	logger.SetOutput(&logs)
	logger.SetLevel(logrus.DebugLevel)
	s, err := New(cfg, logger)
	if err != nil {
		t.Fatalf("server new: %v", err)
	}

	for _, tc := range []struct {
		name string
		path string
		body string
	}{
		{"responses prefixed auth failure with tools", "/v1/responses", `{"model":"droid-openai-chat","stream":true,"input":"auth fail","tools":[{"type":"function","name":"lookup","description":"sk-WORKFLOWSECRET123456","parameters":{"type":"object"}}]}`},
		{"responses prefixless auth failure with tools", "/responses", `{"model":"droid-openai-chat","stream":true,"input":"auth fail","tools":[{"type":"function","name":"lookup","description":"sk-WORKFLOWSECRET123456","parameters":{"type":"object"}}]}`},
		{"messages prefixed auth failure with tools", "/v1/messages", `{"model":"droid-anthropic-chat","stream":true,"max_tokens":32,"messages":[{"role":"user","content":"auth fail"}],"tools":[{"name":"lookup","description":"sk-WORKFLOWSECRET123456","input_schema":{"type":"object"}}]}`},
		{"messages prefixless auth failure with tools", "/messages", `{"model":"droid-anthropic-chat","stream":true,"max_tokens":32,"messages":[{"role":"user","content":"auth fail"}],"tools":[{"name":"lookup","description":"sk-WORKFLOWSECRET123456","input_schema":{"type":"object"}}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := fake.count()
			authFail := httptest.NewRecorder()
			s.Engine().ServeHTTP(authFail, httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body)))
			if authFail.Code != http.StatusUnauthorized || fake.count() != before {
				t.Fatalf("auth must fail before translation/upstream: status=%d before=%d after=%d body=%s", authFail.Code, before, fake.count(), authFail.Body.String())
			}
		})
	}
	oversized := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"droid-anthropic-chat","messages":[{"role":"user","content":"`+strings.Repeat("x", 700)+`"}]}`))
	req.Header.Set("Authorization", "Bearer workflow-client-key")
	s.Engine().ServeHTTP(oversized, req)
	if oversized.Code != http.StatusRequestEntityTooLarge || strings.Contains(oversized.Body.String(), strings.Repeat("x", 20)) || fake.count() != 0 {
		t.Fatalf("body limit did not reject safely before upstream: status=%d upstream=%d body=%s", oversized.Code, fake.count(), oversized.Body.String())
	}

	for _, tc := range []struct {
		name       string
		path       string
		body       string
		wantStatus int
		wantBody   string
	}{
		{"chat pre stream error", "/v1/chat/completions", `{"model":"droid-chat","stream":true,"messages":[{"role":"user","content":"prestream error sk-WORKFLOWSECRET123456"}]}`, http.StatusTooManyRequests, "rate_limited"},
		{"responses translated truncation", "/responses", `{"model":"droid-openai-chat","stream":true,"input":"truncate sk-WORKFLOWSECRET123456"}`, http.StatusOK, "Chat stream ended before [DONE]"},
		{"anthropic translated truncation", "/v1/messages", `{"model":"droid-anthropic-chat","stream":true,"max_tokens":32,"messages":[{"role":"user","content":"truncate sk-WORKFLOWSECRET123456"}]}`, http.StatusOK, "Chat stream ended before [DONE]"},
		{"native responses upstream error", "/v1/responses", `{"model":"droid-openai-native","input":"upstream error sk-WORKFLOWSECRET123456"}`, http.StatusBadGateway, "upstream failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer workflow-client-key")
			s.Engine().ServeHTTP(w, req)
			if w.Code != tc.wantStatus || !strings.Contains(w.Body.String(), tc.wantBody) {
				t.Fatalf("status/body mismatch got status=%d body=%s", w.Code, w.Body.String())
			}
		})
	}

	authenticatedJourneys := []struct {
		name       string
		path       string
		body       string
		wantEvents []string
	}{
		{"responses prefixed streaming tool redaction", "/v1/responses?token=sk-WORKFLOWSECRET123456", `{"model":"droid-openai-chat","stream":true,"input":"tool please sk-WORKFLOWSECRET123456","tools":[{"type":"function","name":"lookup","description":"sk-WORKFLOWSECRET123456","parameters":{"type":"object","properties":{"secret":{"const":"sk-WORKFLOWSECRET123456"}}}}]}`, []string{"response.output_item.added", "response.function_call_arguments.delta", "response.completed"}},
		{"responses prefixless streaming tool redaction", "/responses?token=sk-WORKFLOWSECRET123456", `{"model":"droid-openai-chat","stream":true,"input":"tool please sk-WORKFLOWSECRET123456","tools":[{"type":"function","name":"lookup","description":"sk-WORKFLOWSECRET123456","parameters":{"type":"object","properties":{"secret":{"const":"sk-WORKFLOWSECRET123456"}}}}]}`, []string{"response.output_item.added", "response.function_call_arguments.delta", "response.completed"}},
		{"anthropic prefixed streaming tool redaction", "/v1/messages?token=sk-WORKFLOWSECRET123456", `{"model":"droid-anthropic-chat","stream":true,"max_tokens":32,"messages":[{"role":"user","content":"tool please sk-WORKFLOWSECRET123456"}],"tools":[{"name":"lookup","description":"sk-WORKFLOWSECRET123456","input_schema":{"type":"object","properties":{"secret":{"const":"sk-WORKFLOWSECRET123456"}}}}]}`, []string{"content_block_start", "content_block_delta", "message_stop"}},
		{"anthropic prefixless streaming tool redaction", "/messages?token=sk-WORKFLOWSECRET123456", `{"model":"droid-anthropic-chat","stream":true,"max_tokens":32,"messages":[{"role":"user","content":"tool please sk-WORKFLOWSECRET123456"}],"tools":[{"name":"lookup","description":"sk-WORKFLOWSECRET123456","input_schema":{"type":"object","properties":{"secret":{"const":"sk-WORKFLOWSECRET123456"}}}}]}`, []string{"content_block_start", "content_block_delta", "message_stop"}},
		{"responses tool result redaction", "/v1/responses?token=sk-WORKFLOWSECRET123456", `{"model":"droid-openai-chat","input":[{"role":"user","content":"tool please"},{"type":"function_call","call_id":"call_resp","name":"lookup","arguments":"{\"secret\":\"sk-WORKFLOWSECRET123456\"}"},{"type":"function_call_output","call_id":"call_resp","output":"tool result sk-WORKFLOWSECRET123456"}]}`, nil},
		{"anthropic tool result redaction", "/messages?token=sk-WORKFLOWSECRET123456", `{"model":"droid-anthropic-chat","max_tokens":32,"messages":[{"role":"user","content":"tool please"},{"role":"assistant","content":[{"type":"tool_use","id":"toolu_workflow","name":"lookup","input":{"secret":"sk-WORKFLOWSECRET123456"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_workflow","content":"tool result sk-WORKFLOWSECRET123456"}]}]}`, nil},
	}
	for _, tc := range authenticatedJourneys {
		t.Run(tc.name, func(t *testing.T) {
			redact := httptest.NewRecorder()
			req = httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer workflow-client-key")
			s.Engine().ServeHTTP(redact, req)
			if redact.Code != http.StatusOK {
				t.Fatalf("authenticated redaction request failed: status=%d body=%s", redact.Code, redact.Body.String())
			}
			if len(tc.wantEvents) > 0 {
				events := parseWorkflowSSE(t, redact.Body.String())
				for _, want := range tc.wantEvents {
					if !workflowHasEvent(events, want) {
						t.Fatalf("SSE transcript missing %q: %#v\n%s", want, events, redact.Body.String())
					}
				}
			}
		})
	}
	logText := logs.String()
	for _, secret := range []string{"sk-WORKFLOWSECRET123456", "workflow-client-key", "sentinel-openai-key", "sentinel-anthropic-key"} {
		if strings.Contains(logText, secret) {
			t.Fatalf("trace/access logs leaked sentinel %q:\n%s", secret, logText)
		}
	}
	if !strings.Contains(logText, "***") {
		t.Fatalf("expected redaction marker in trace logs, got:\n%s", logText)
	}
}

func TestWorkflowValidation_TerminalTruncationCancellationAllStreamSurfaces(t *testing.T) {
	fake := newDroidWorkflowFakeUpstream(t)
	cfg := droidWorkflowValidationConfig(t, fake.URL(), false)
	assertLoopbackOnlyValidationUpstreams(t, cfg)
	s, err := New(cfg, discardLogger())
	if err != nil {
		t.Fatalf("server new: %v", err)
	}

	successCases := []struct {
		name     string
		path     string
		body     string
		terminal string
	}{
		{"chat native success", "/v1/chat/completions", `{"model":"droid-chat","stream":true,"messages":[{"role":"user","content":"hello"}]}`, "[DONE]"},
		{"responses native success", "/v1/responses", `{"model":"droid-openai-native","stream":true,"input":"hello"}`, "response.completed"},
		{"responses translated success", "/responses", `{"model":"droid-openai-chat","stream":true,"input":"tool please"}`, "response.completed"},
		{"messages native success", "/v1/messages", `{"model":"droid-anthropic-native","stream":true,"max_tokens":32,"messages":[{"role":"user","content":"hello"}]}`, "message_stop"},
		{"messages translated success", "/messages", `{"model":"droid-anthropic-chat","stream":true,"max_tokens":32,"messages":[{"role":"user","content":"tool please"}]}`, "message_stop"},
	}
	for _, tc := range successCases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			s.Engine().ServeHTTP(w, httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body)))
			if w.Code != http.StatusOK {
				t.Fatalf("success stream status=%d body=%s", w.Code, w.Body.String())
			}
			events := parseWorkflowSSE(t, w.Body.String())
			if !workflowHasEvent(events, tc.terminal) {
				t.Fatalf("terminal %q missing from events %#v transcript=%s", tc.terminal, events, w.Body.String())
			}
		})
	}

	truncationCases := []struct {
		name      string
		path      string
		body      string
		wantError string
	}{
		{"chat native truncation", "/v1/chat/completions", `{"model":"droid-chat","stream":true,"messages":[{"role":"user","content":"truncate"}]}`, "upstream stream ended before terminal marker"},
		{"responses native truncation", "/v1/responses", `{"model":"droid-openai-native","stream":true,"input":"truncate"}`, "upstream stream ended before terminal marker"},
		{"responses translated truncation", "/responses", `{"model":"droid-openai-chat","stream":true,"input":"truncate"}`, "Chat stream ended before [DONE]"},
		{"messages native truncation", "/v1/messages", `{"model":"droid-anthropic-native","stream":true,"max_tokens":32,"messages":[{"role":"user","content":"truncate"}]}`, "upstream stream ended before terminal marker"},
		{"messages translated truncation", "/messages", `{"model":"droid-anthropic-chat","stream":true,"max_tokens":32,"messages":[{"role":"user","content":"truncate"}]}`, "Chat stream ended before [DONE]"},
	}
	for _, tc := range truncationCases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			s.Engine().ServeHTTP(w, httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body)))
			if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), tc.wantError) {
				t.Fatalf("truncation stream status=%d want error %q body=%s", w.Code, tc.wantError, w.Body.String())
			}
			_ = parseWorkflowSSE(t, w.Body.String())
		})
	}

	proxy := httptest.NewServer(s.Engine())
	defer proxy.Close()
	cancelCases := []struct {
		name string
		path string
		body string
	}{
		{"chat native cancel", "/v1/chat/completions", `{"model":"droid-chat","stream":true,"messages":[{"role":"user","content":"cancel me"}]}`},
		{"responses native cancel", "/v1/responses", `{"model":"droid-openai-native","stream":true,"input":"cancel me"}`},
		{"responses translated cancel", "/responses", `{"model":"droid-openai-chat","stream":true,"input":"cancel me"}`},
		{"messages native cancel", "/v1/messages", `{"model":"droid-anthropic-native","stream":true,"max_tokens":32,"messages":[{"role":"user","content":"cancel me"}]}`},
		{"messages translated cancel", "/messages", `{"model":"droid-anthropic-chat","stream":true,"max_tokens":32,"messages":[{"role":"user","content":"cancel me"}]}`},
	}
	for _, tc := range cancelCases {
		t.Run(tc.name, func(t *testing.T) {
			before := fake.cancelCount()
			ctx, cancel := context.WithCancel(context.Background())
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, proxy.URL+tc.path, strings.NewReader(tc.body))
			if err != nil {
				t.Fatal(err)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("start cancel stream: %v", err)
			}
			buf := make([]byte, 128)
			_, _ = resp.Body.Read(buf)
			cancel()
			_ = resp.Body.Close()
			if !fake.waitForCancelCount(t, before+1, time.Second) {
				t.Fatalf("fake upstream did not observe cancellation for %s; before=%d after=%d", tc.name, before, fake.cancelCount())
			}
		})
	}
}

func TestWorkflowValidation_EndpointTruthTableReadinessAndRuntimeHardening(t *testing.T) {
	fake := newDroidWorkflowFakeUpstream(t)
	cfg := droidWorkflowValidationConfig(t, fake.URL(), false)
	assertLoopbackOnlyValidationUpstreams(t, cfg)
	s, err := New(cfg, discardLogger())
	if err != nil {
		t.Fatalf("server new: %v", err)
	}
	assertProviderMatrixDocs(t)
	for _, path := range []string{"/health", "/healthz"} {
		w := httptest.NewRecorder()
		s.Engine().ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", path, w.Code, w.Body.String())
		}
	}
	wHead := httptest.NewRecorder()
	s.Engine().ServeHTTP(wHead, httptest.NewRequest(http.MethodHead, "/healthz", nil))
	if wHead.Code != http.StatusOK {
		t.Fatalf("HEAD /healthz status=%d body=%q", wHead.Code, wHead.Body.String())
	}
	for _, modelsPath := range []string{"/v1/models", "/models"} {
		w := httptest.NewRecorder()
		s.Engine().ServeHTTP(w, httptest.NewRequest(http.MethodGet, modelsPath, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", modelsPath, w.Code, w.Body.String())
		}
		assertAgentReadyPerWorkflowModel(t, w.Body.Bytes(), cfg.Models)
	}

	type routeExpectation struct {
		path string
		body func(alias string) string
		ok   func(m *config.Model) bool
	}
	routes := []routeExpectation{
		{"/v1/chat/completions", func(alias string) string {
			return `{"model":"` + alias + `","messages":[{"role":"user","content":"hi"}]}`
		}, func(m *config.Model) bool {
			return m.UpstreamProtocol == config.UpstreamOpenAIChat && (m.FactoryProvider == config.FactoryProviderGeneric || m.FactoryProvider == config.FactoryProviderOpenAI)
		}},
		{"/chat/completions", func(alias string) string {
			return `{"model":"` + alias + `","messages":[{"role":"user","content":"hi"}]}`
		}, func(m *config.Model) bool {
			return m.UpstreamProtocol == config.UpstreamOpenAIChat && (m.FactoryProvider == config.FactoryProviderGeneric || m.FactoryProvider == config.FactoryProviderOpenAI)
		}},
		{"/v1/responses", func(alias string) string { return `{"model":"` + alias + `","input":"hi"}` }, func(m *config.Model) bool {
			return m.FactoryProvider == config.FactoryProviderOpenAI && (m.UpstreamProtocol == config.UpstreamOpenAIResponses || m.UpstreamProtocol == config.UpstreamOpenAIChat)
		}},
		{"/responses", func(alias string) string { return `{"model":"` + alias + `","input":"hi"}` }, func(m *config.Model) bool {
			return m.FactoryProvider == config.FactoryProviderOpenAI && (m.UpstreamProtocol == config.UpstreamOpenAIResponses || m.UpstreamProtocol == config.UpstreamOpenAIChat)
		}},
		{"/v1/messages", func(alias string) string {
			return `{"model":"` + alias + `","max_tokens":32,"messages":[{"role":"user","content":"hi"}]}`
		}, func(m *config.Model) bool {
			return m.FactoryProvider == config.FactoryProviderAnthropic && (m.UpstreamProtocol == config.UpstreamAnthropicMessages || m.UpstreamProtocol == config.UpstreamOpenAIChat)
		}},
		{"/messages", func(alias string) string {
			return `{"model":"` + alias + `","max_tokens":32,"messages":[{"role":"user","content":"hi"}]}`
		}, func(m *config.Model) bool {
			return m.FactoryProvider == config.FactoryProviderAnthropic && (m.UpstreamProtocol == config.UpstreamAnthropicMessages || m.UpstreamProtocol == config.UpstreamOpenAIChat)
		}},
		{"/v1/messages/count_tokens", func(alias string) string {
			return `{"model":"` + alias + `","messages":[{"role":"user","content":"hi"}]}`
		}, func(m *config.Model) bool { return m.FactoryProvider == config.FactoryProviderAnthropic }},
		{"/messages/count_tokens", func(alias string) string {
			return `{"model":"` + alias + `","messages":[{"role":"user","content":"hi"}]}`
		}, func(m *config.Model) bool { return m.FactoryProvider == config.FactoryProviderAnthropic }},
	}
	for _, m := range cfg.Models {
		for _, route := range routes {
			t.Run(string(m.FactoryProvider)+" "+string(m.UpstreamProtocol)+" "+m.Alias+" "+route.path, func(t *testing.T) {
				w := httptest.NewRecorder()
				s.Engine().ServeHTTP(w, httptest.NewRequest(http.MethodPost, route.path, strings.NewReader(route.body(m.Alias))))
				wantOK := route.ok(m)
				if wantOK && w.Code != http.StatusOK {
					t.Fatalf("%s %s/%s status=%d want 200 body=%s", route.path, m.FactoryProvider, m.UpstreamProtocol, w.Code, w.Body.String())
				}
				if !wantOK && w.Code < 400 {
					t.Fatalf("%s %s/%s status=%d want local error body=%s", route.path, m.FactoryProvider, m.UpstreamProtocol, w.Code, w.Body.String())
				}
			})
		}
	}
	t.Run("unknown alias local errors", func(t *testing.T) {
		for _, route := range routes {
			w := httptest.NewRecorder()
			s.Engine().ServeHTTP(w, httptest.NewRequest(http.MethodPost, route.path, strings.NewReader(route.body("missing"))))
			if w.Code != http.StatusNotFound {
				t.Fatalf("%s unknown alias status=%d want 404 body=%s", route.path, w.Code, w.Body.String())
			}
		}
	})
}

type workflowSSEEvent struct {
	Name string
	Data string
}

func parseWorkflowSSE(t *testing.T, transcript string) []workflowSSEEvent {
	t.Helper()
	frames := strings.Split(transcript, "\n\n")
	var events []workflowSSEEvent
	for _, frame := range frames {
		frame = strings.TrimSpace(frame)
		if frame == "" || strings.HasPrefix(frame, ":") {
			continue
		}
		ev := workflowSSEEvent{Name: "message"}
		for _, line := range strings.Split(frame, "\n") {
			switch {
			case strings.HasPrefix(line, "event:"):
				ev.Name = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			case strings.HasPrefix(line, "data:"):
				ev.Data += strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			}
		}
		events = append(events, ev)
	}
	if len(events) == 0 && strings.TrimSpace(transcript) != "" {
		t.Fatalf("no SSE events parsed from transcript:\n%s", transcript)
	}
	return events
}

func workflowHasEvent(events []workflowSSEEvent, name string) bool {
	for _, ev := range events {
		if ev.Name == name || (name == "[DONE]" && ev.Data == "[DONE]") {
			return true
		}
	}
	return false
}

type droidWorkflowFakeUpstream struct {
	server        *httptest.Server
	mu            sync.Mutex
	caps          []validationCapture
	cancelSignals int
}

func newDroidWorkflowFakeUpstream(t *testing.T) *droidWorkflowFakeUpstream {
	t.Helper()
	f := &droidWorkflowFakeUpstream{}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		body := string(bodyBytes)
		cap := validationCapture{
			Method:  r.Method,
			Path:    r.URL.Path,
			Headers: r.Header.Clone(),
			Body:    body,
		}
		status, contentType, scripted := scriptDroidWorkflowResponse(r.URL.Path, body)
		cap.Scripted = scripted
		w.Header().Set("Content-Type", contentType)
		if status != http.StatusOK {
			w.WriteHeader(status)
		}
		if strings.Contains(contentType, "text/event-stream") {
			flusher, _ := w.(http.Flusher)
			for _, frame := range strings.Split(scripted, "\n\n") {
				if strings.TrimSpace(frame) == "" {
					continue
				}
				select {
				case <-r.Context().Done():
					f.recordCancel()
					return
				default:
				}
				_, _ = w.Write([]byte(frame + "\n\n"))
				if flusher != nil {
					flusher.Flush()
				}
				if strings.Contains(body, "truncate") {
					break
				}
				if strings.Contains(body, "cancel me") {
					time.Sleep(25 * time.Millisecond)
				}
			}
		} else {
			_, _ = w.Write([]byte(scripted))
		}
		cap.Downstream = scripted
		f.mu.Lock()
		f.caps = append(f.caps, cap)
		f.mu.Unlock()
	}))
	t.Cleanup(f.server.Close)
	return f
}

func scriptDroidWorkflowResponse(path, body string) (int, string, string) {
	if strings.Contains(body, "upstream error") {
		return http.StatusBadGateway, "application/json", `{"error":{"message":"upstream failed","code":"rate_limited"}}`
	}
	stream := strings.Contains(body, `"stream":true`)
	switch path {
	case "/v1/chat/completions", "/chat/completions":
		if strings.Contains(body, "deepseek-upstream") || strings.Contains(body, "droid-deepseek") {
			if stream {
				return http.StatusOK, "text/event-stream", strings.Join([]string{
					`data: {"id":"deepseek_1","choices":[{"index":0,"delta":{"reasoning_content":"deepseek workflow reasoning"},"finish_reason":null}]}`,
					`data: {"id":"deepseek_1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_deepseek","type":"function","function":{"name":"lookup","arguments":"{\"city\":\"sf\"}"}}]},"finish_reason":"tool_calls"}]}`,
					`data: [DONE]`,
				}, "\n\n")
			}
			if strings.Contains(body, `"role":"tool"`) {
				return http.StatusOK, "application/json", `{"id":"deepseek_2","choices":[{"message":{"role":"assistant","content":"deepseek followup ok"},"finish_reason":"stop"}]}`
			}
			return http.StatusOK, "application/json", `{"id":"deepseek_nonstream_1","choices":[{"message":{"role":"assistant","content":"","reasoning_content":"deepseek workflow reasoning nonstream","tool_calls":[{"id":"call_deepseek","type":"function","function":{"name":"lookup","arguments":"{\"city\":\"sf\"}"}}]},"finish_reason":"tool_calls"}]}`
		}
		if stream {
			if strings.Contains(body, "prestream error") {
				return http.StatusTooManyRequests, "application/json", `{"error":{"message":"rate_limited","type":"rate_limit_exceeded"}}`
			}
			if strings.Contains(body, "tool please") {
				return http.StatusOK, "text/event-stream", strings.Join([]string{
					`data: {"id":"chat_workflow","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
					`data: {"id":"chat_workflow","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_chat","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"chat\"}"}}]},"finish_reason":"tool_calls"}]}`,
					`data: [DONE]`,
				}, "\n\n")
			}
			if strings.Contains(body, "truncate") {
				return http.StatusOK, "text/event-stream", `data: {"id":"chat_trunc","choices":[{"index":0,"delta":{"content":"partial"},"finish_reason":null}]}`
			}
			if strings.Contains(body, "cancel me") {
				return http.StatusOK, "text/event-stream", strings.Join([]string{
					`data: {"id":"chat_cancel","choices":[{"index":0,"delta":{"content":"partial"},"finish_reason":null}]}`,
					`data: {"id":"chat_cancel","choices":[{"index":0,"delta":{"content":"still streaming"},"finish_reason":null}]}`,
					`data: {"id":"chat_cancel","choices":[{"index":0,"delta":{"content":"done"},"finish_reason":"stop"}]}`,
					`data: [DONE]`,
				}, "\n\n")
			}
			return http.StatusOK, "text/event-stream", strings.Join([]string{
				`data: {"id":"chat_workflow","choices":[{"index":0,"delta":{"content":"chat stream ok"},"finish_reason":"stop"}]}`,
				`data: [DONE]`,
			}, "\n\n")
		}
		if strings.Contains(body, `"role":"tool"`) {
			switch {
			case strings.Contains(body, "call_resp"):
				return http.StatusOK, "application/json", `{"id":"chat_resp_follow","choices":[{"message":{"role":"assistant","content":"responses followup ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`
			case strings.Contains(body, "toolu_workflow"):
				return http.StatusOK, "application/json", `{"id":"chat_anth_follow","choices":[{"message":{"role":"assistant","content":"anthropic followup ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`
			case strings.Contains(body, "call_deepseek"):
				return http.StatusOK, "application/json", `{"id":"deepseek_2","choices":[{"message":{"role":"assistant","content":"deepseek followup ok"},"finish_reason":"stop"}]}`
			default:
				return http.StatusOK, "application/json", `{"id":"chat_follow","choices":[{"message":{"role":"assistant","content":"chat followup ok"},"finish_reason":"stop"}]}`
			}
		}
		if strings.Contains(body, "responses-chat-upstream") {
			if stream {
				return http.StatusOK, "text/event-stream", responsesChatToolStream()
			}
			if strings.Contains(body, `"tools"`) || strings.Contains(body, "tool please") {
				return http.StatusOK, "application/json", `{"id":"chat_resp_tool","choices":[{"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_resp","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"responses\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`
			}
			return http.StatusOK, "application/json", `{"id":"chat_resp_text","choices":[{"message":{"role":"assistant","content":"translated responses ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`
		}
		if strings.Contains(body, "anthropic-chat-upstream") {
			if stream {
				return http.StatusOK, "text/event-stream", anthropicChatToolStream()
			}
			if strings.Contains(body, `"tools"`) || strings.Contains(body, "tool please") {
				return http.StatusOK, "application/json", `{"id":"chat_anth_tool","choices":[{"message":{"role":"assistant","content":null,"tool_calls":[{"id":"toolu_workflow","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"anthropic\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`
			}
			return http.StatusOK, "application/json", `{"id":"chat_anth_text","choices":[{"message":{"role":"assistant","content":"translated anthropic ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`
		}
		return http.StatusOK, "application/json", `{"id":"chat_text","choices":[{"message":{"role":"assistant","content":"chat text ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`
	case "/v1/responses", "/responses":
		if stream {
			if strings.Contains(body, "cancel me") {
				return http.StatusOK, "text/event-stream", strings.Join([]string{
					`event: response.created` + "\n" + `data: {"type":"response.created","response":{"id":"resp_native","status":"in_progress"}}`,
					`event: response.output_text.delta` + "\n" + `data: {"type":"response.output_text.delta","output_index":0,"delta":"partial"}`,
					`event: response.output_text.delta` + "\n" + `data: {"type":"response.output_text.delta","output_index":0,"delta":"more"}`,
					`event: response.completed` + "\n" + `data: {"type":"response.completed","response":{"id":"resp_native","status":"completed"}}`,
				}, "\n\n")
			}
			return http.StatusOK, "text/event-stream", strings.Join([]string{
				`event: response.created` + "\n" + `data: {"type":"response.created","response":{"id":"resp_native","status":"in_progress"}}`,
				`event: response.output_text.delta` + "\n" + `data: {"type":"response.output_text.delta","output_index":0,"delta":"native responses stream ok"}`,
				`event: response.completed` + "\n" + `data: {"type":"response.completed","response":{"id":"resp_native","status":"completed"}}`,
			}, "\n\n")
		}
		return http.StatusOK, "application/json", `{"id":"resp_native","object":"response","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"native responses ok"}]}]}`
	case "/v1/messages", "/messages":
		if stream {
			if strings.Contains(body, "cancel me") {
				return http.StatusOK, "text/event-stream", strings.Join([]string{
					`event: message_start` + "\n" + `data: {"type":"message_start","message":{"id":"msg_native","type":"message","role":"assistant","content":[]}}`,
					`event: content_block_start` + "\n" + `data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
					`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial"}}`,
					`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"more"}}`,
					`event: message_stop` + "\n" + `data: {"type":"message_stop"}`,
				}, "\n\n")
			}
			return http.StatusOK, "text/event-stream", strings.Join([]string{
				`event: message_start` + "\n" + `data: {"type":"message_start","message":{"id":"msg_native","type":"message","role":"assistant","content":[]}}`,
				`event: content_block_start` + "\n" + `data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
				`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"native anthropic stream ok"}}`,
				`event: content_block_stop` + "\n" + `data: {"type":"content_block_stop","index":0}`,
				`event: message_stop` + "\n" + `data: {"type":"message_stop"}`,
			}, "\n\n")
		}
		return http.StatusOK, "application/json", `{"id":"msg_native","type":"message","role":"assistant","content":[{"type":"text","text":"native anthropic ok"}],"stop_reason":"end_turn","usage":{"input_tokens":4,"output_tokens":2}}`
	case "/v1/messages/count_tokens", "/messages/count_tokens":
		return http.StatusOK, "application/json", `{"input_tokens":11}`
	default:
		return http.StatusNotFound, "application/json", `{"error":{"message":"not found"}}`
	}
}

func responsesChatToolStream() string {
	return strings.Join([]string{
		`data: {"id":"chat_resp_stream","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_resp","type":"function","function":{"name":"lookup","arguments":"{\"q\""}}]},"finish_reason":null}]}`,
		`data: {"id":"chat_resp_stream","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":":\"responses\"}"}}]},"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`,
	}, "\n\n")
}

func anthropicChatToolStream() string {
	return strings.Join([]string{
		`data: {"id":"chat_anth_stream","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"toolu_workflow","type":"function","function":{"name":"lookup","arguments":"{\"q\""}}]},"finish_reason":null}]}`,
		`data: {"id":"chat_anth_stream","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":":\"anthropic\"}"}}]},"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`,
	}, "\n\n")
}

func (f *droidWorkflowFakeUpstream) URL() string { return f.server.URL }

func (f *droidWorkflowFakeUpstream) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.caps)
}

func (f *droidWorkflowFakeUpstream) recordCancel() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancelSignals++
}

func (f *droidWorkflowFakeUpstream) cancelCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cancelSignals
}

func (f *droidWorkflowFakeUpstream) waitForCancelCount(t *testing.T, want int, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if f.cancelCount() >= want {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return f.cancelCount() >= want
}

func (f *droidWorkflowFakeUpstream) lastCapture(t *testing.T) validationCapture {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.caps) == 0 {
		t.Fatal("no fake upstream captures")
	}
	return f.caps[len(f.caps)-1]
}

func (f *droidWorkflowFakeUpstream) lastBodyForModel(model string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.caps) - 1; i >= 0; i-- {
		if strings.Contains(f.caps[i].Body, model) {
			return f.caps[i].Body
		}
	}
	return ""
}

func droidWorkflowValidationConfig(t *testing.T, upstreamURL string, clientAuth bool) *config.Config {
	t.Helper()
	t.Setenv("WORKFLOW_VALIDATION_OPENAI_KEY", "sentinel-openai-key")
	t.Setenv("WORKFLOW_VALIDATION_ANTHROPIC_KEY", "sentinel-anthropic-key")
	cfg, err := configFromYAMLForWorkflow([]byte(droidWorkflowValidationConfigYAML(upstreamURL, clientAuth)))
	if err != nil {
		t.Fatalf("droid workflow validation config: %v", err)
	}
	return cfg
}

func droidWorkflowValidationConfigYAML(upstreamURL string, clientAuth bool) string {
	auth := "client_auth:\n  enabled: false\n"
	if clientAuth {
		auth = "client_auth:\n  enabled: true\n  api_keys: [workflow-client-key]\n"
	}
	return fmt.Sprintf(`
listen:
  host: 127.0.0.1
  port: 0
%s
logging:
  redact: true
server:
  shutdown_timeout: 2s
  request_body_max_bytes: 1048576
upstream:
  http_timeout: 2s
  stream_keep_alive: 25ms
models:
  - alias: droid-chat
    display_name: "Workflow Generic Chat"
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    base_url: %s
    upstream_model: chat-upstream
    api_key_env: WORKFLOW_VALIDATION_OPENAI_KEY
  - alias: droid-openai-native
    display_name: "Workflow Native Responses"
    factory_provider: openai
    upstream_protocol: openai-responses
    base_url: %s
    upstream_model: responses-upstream
    api_key_env: WORKFLOW_VALIDATION_OPENAI_KEY
  - alias: droid-openai-chat
    display_name: "Workflow Responses over Chat"
    factory_provider: openai
    upstream_protocol: openai-chat
    base_url: %s
    upstream_model: responses-chat-upstream
    api_key_env: WORKFLOW_VALIDATION_OPENAI_KEY
  - alias: droid-anthropic-native
    display_name: "Workflow Native Anthropic"
    factory_provider: anthropic
    upstream_protocol: anthropic-messages
    known_auth: anthropic
    base_url: %s
    upstream_model: claude-native-upstream
    api_key_env: WORKFLOW_VALIDATION_ANTHROPIC_KEY
  - alias: droid-anthropic-chat
    display_name: "Workflow Anthropic over Chat"
    factory_provider: anthropic
    upstream_protocol: openai-chat
    base_url: %s
    upstream_model: anthropic-chat-upstream
    api_key_env: WORKFLOW_VALIDATION_OPENAI_KEY
  - alias: droid-deepseek
    display_name: "Workflow DeepSeek Reasoning"
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    known_auth: deepseek
    base_url: %s
    upstream_model: deepseek-upstream
    api_key_env: WORKFLOW_VALIDATION_OPENAI_KEY
    capabilities:
      reasoning: deepseek
`, auth, upstreamURL, upstreamURL, upstreamURL, upstreamURL, upstreamURL, upstreamURL)
}

func workflowValidationConfig(t *testing.T, upstreamURL string, port int) *config.Config {
	t.Helper()
	t.Setenv("WORKFLOW_VALIDATION_OPENAI_KEY", "sentinel-openai-key")
	t.Setenv("WORKFLOW_VALIDATION_ANTHROPIC_KEY", "sentinel-anthropic-key")
	cfg, err := configFromYAMLForWorkflow([]byte(workflowValidationConfigYAML(upstreamURL, port)))
	if err != nil {
		t.Fatalf("workflow validation config: %v", err)
	}
	return cfg
}

type factorySettingsExample struct {
	File         string
	CustomModels []factorySettingsCustomModel `json:"customModels"`
}

type factorySettingsCustomModel struct {
	Model           string `json:"model"`
	DisplayName     string `json:"displayName"`
	Provider        string `json:"provider"`
	BaseURL         string `json:"baseUrl"`
	APIKey          string `json:"apiKey"`
	MaxOutputTokens int    `json:"maxOutputTokens"`
}

func loadFactorySettingsExamples(t *testing.T) []factorySettingsExample {
	t.Helper()
	settingsDir := filepath.Join(repoRootFromServerTest(t), "docs", "factory-settings")
	entries, err := os.ReadDir(settingsDir)
	if err != nil {
		t.Fatalf("read factory settings: %v", err)
	}
	var out []factorySettingsExample
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(settingsDir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		var example factorySettingsExample
		example.File = entry.Name()
		if err := json.Unmarshal(raw, &example); err != nil {
			t.Fatalf("%s settings JSON parses: %v", entry.Name(), err)
		}
		if len(example.CustomModels) == 0 {
			t.Fatalf("%s settings example has no customModels", entry.Name())
		}
		out = append(out, example)
	}
	return out
}

func workflowValidationConfigFromFactorySettings(t *testing.T, upstreamURL string, port int, examples []factorySettingsExample) *config.Config {
	t.Helper()
	t.Setenv("WORKFLOW_VALIDATION_OPENAI_KEY", "sentinel-openai-key")
	t.Setenv("WORKFLOW_VALIDATION_ANTHROPIC_KEY", "sentinel-anthropic-key")
	var b strings.Builder
	fmt.Fprintf(&b, `
listen:
  host: 127.0.0.1
  port: %d
server:
  shutdown_timeout: 2s
upstream:
  http_timeout: 2s
models:
`, port)
	for _, example := range examples {
		for _, cm := range example.CustomModels {
			protocol := "openai-chat"
			keyEnv := "WORKFLOW_VALIDATION_OPENAI_KEY"
			knownAuth := ""
			switch cm.Provider {
			case string(config.FactoryProviderOpenAI):
				protocol = "openai-responses"
			case string(config.FactoryProviderAnthropic):
				protocol = "anthropic-messages"
				keyEnv = "WORKFLOW_VALIDATION_ANTHROPIC_KEY"
				knownAuth = "    known_auth: anthropic\n"
			}
			fmt.Fprintf(&b, `  - alias: %s
    display_name: %q
    factory_provider: %s
    upstream_protocol: %s
%s    base_url: %s
    api_key_env: %s
`, cm.Model, cm.DisplayName, cm.Provider, protocol, knownAuth, upstreamURL, keyEnv)
		}
	}
	cfg, err := configFromYAMLForWorkflow([]byte(b.String()))
	if err != nil {
		t.Fatalf("factory settings validation config: %v\n%s", err, b.String())
	}
	return cfg
}

func configFromYAMLForWorkflow(raw []byte) (*config.Config, error) {
	tmp, err := os.CreateTemp("", "droid-proxy-validation-*.yaml")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}
	return config.Load(tmp.Name())
}

func workflowValidationConfigYAML(upstreamURL string, port int) string {
	return fmt.Sprintf(`
listen:
  host: 127.0.0.1
  port: %d
server:
  shutdown_timeout: 2s
upstream:
  http_timeout: 2s
models:
  - alias: droid-chat
    display_name: "Validation Chat"
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    base_url: %s
    api_key_env: WORKFLOW_VALIDATION_OPENAI_KEY
  - alias: droid-openai
    display_name: "Validation OpenAI"
    factory_provider: openai
    upstream_protocol: openai-responses
    base_url: %s
    api_key_env: WORKFLOW_VALIDATION_OPENAI_KEY
  - alias: droid-anthropic
    display_name: "Validation Anthropic"
    factory_provider: anthropic
    upstream_protocol: anthropic-messages
    known_auth: anthropic
    base_url: %s
    api_key_env: WORKFLOW_VALIDATION_ANTHROPIC_KEY
`, port, upstreamURL, upstreamURL, upstreamURL)
}

func assertProviderMatrixDocs(t *testing.T) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRootFromServerTest(t), "docs", "PROVIDERS.md"))
	if err != nil {
		t.Fatal(err)
	}
	doc := string(raw)
	for _, row := range []string{
		"| `generic-chat-completion-api` | `openai-chat`",
		"| `openai` | `openai-responses`",
		"| `openai` | `openai-chat`",
		"| `anthropic` | `anthropic-messages`",
		"| `anthropic` | `openai-chat`",
	} {
		if !strings.Contains(doc, row) || !strings.Contains(doc, "✅ supported") {
			t.Fatalf("provider matrix missing supported row evidence for %s", row)
		}
	}
}

func assertAgentReadyPerWorkflowModel(t *testing.T, body []byte, models []*config.Model) {
	t.Helper()
	var decoded struct {
		Data []struct {
			ID               string `json:"id"`
			FactoryProvider  string `json:"factory_provider"`
			UpstreamProtocol string `json:"upstream_protocol"`
			AgentReady       bool   `json:"agent_ready"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("models JSON parses: %v body=%s", err, body)
	}
	byID := map[string]struct {
		FactoryProvider  string
		UpstreamProtocol string
		AgentReady       bool
	}{}
	for _, item := range decoded.Data {
		byID[item.ID] = struct {
			FactoryProvider  string
			UpstreamProtocol string
			AgentReady       bool
		}{item.FactoryProvider, item.UpstreamProtocol, item.AgentReady}
	}
	for _, m := range models {
		got, ok := byID[m.Alias]
		if !ok {
			t.Fatalf("models output missing alias %s: %s", m.Alias, body)
		}
		if got.FactoryProvider != string(m.FactoryProvider) || got.UpstreamProtocol != string(m.UpstreamProtocol) {
			t.Fatalf("models metadata mismatch for %s: got provider=%s protocol=%s want %s/%s", m.Alias, got.FactoryProvider, got.UpstreamProtocol, m.FactoryProvider, m.UpstreamProtocol)
		}
		if got.AgentReady != m.AgentReady() {
			t.Fatalf("agent_ready mismatch for %s (%s/%s): got=%v want=%v body=%s", m.Alias, m.FactoryProvider, m.UpstreamProtocol, got.AgentReady, m.AgentReady(), body)
		}
	}
}

func endpointForFactoryProvider(provider, alias string) (string, string) {
	switch provider {
	case string(config.FactoryProviderAnthropic):
		return "/v1/messages", `{"model":"` + alias + `","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`
	case string(config.FactoryProviderOpenAI):
		return "/v1/responses", `{"model":"` + alias + `","input":"hi"}`
	default:
		return "/v1/chat/completions", `{"model":"` + alias + `","messages":[{"role":"user","content":"hi"}]}`
	}
}

func assertLoopbackOnlyValidationUpstreams(t *testing.T, cfg *config.Config) {
	t.Helper()
	if err := validateLoopbackOnlyValidationUpstreams(cfg); err != nil {
		t.Fatal(err)
	}
}

func validateLoopbackOnlyValidationUpstreams(cfg *config.Config) error {
	for _, m := range cfg.Models {
		u, err := url.Parse(m.BaseURL)
		if err != nil {
			return fmt.Errorf("model %s base_url parse: %w", m.Alias, err)
		}
		host := u.Hostname()
		ip := net.ParseIP(host)
		if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return fmt.Errorf("model %s validation base_url %s is not loopback", m.Alias, m.BaseURL)
		}
		for _, forbidden := range []string{"api.openai.com", "api.anthropic.com", "api.deepseek.com"} {
			if strings.EqualFold(host, forbidden) {
				return fmt.Errorf("model %s validation base_url uses forbidden provider host %s", m.Alias, host)
			}
		}
	}
	return nil
}

func repoRootFromServerTest(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}
