package handlers

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/trevoraspencer/droid-proxy/internal/config"
	"github.com/trevoraspencer/droid-proxy/internal/oauth"
)

func TestNativeRequestsRejectMalformedJSONBeforeUpstream(t *testing.T) {
	api := newTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("invalid request reached upstream")
	}, nil)
	for _, body := range []string{
		`{"model":"droid-test",`, `{"model":"droid-test"} trailing`,
		`[{"model":"droid-test"}]`, `null`, `{"model":123}`, `{"model":{}}`,
		`{"model":"droid-test","stream":"true"}`,
		`{"model":"droid-test","model":"other"}`,
		`{"model":"droid-test","m\u006fdel":"other"}`,
		`{"model":"droid-test","stream":true,"stream":false}`,
	} {
		w := httptest.NewRecorder()
		api.engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body)))
		if w.Code != http.StatusBadRequest {
			t.Errorf("body %q: status %d, %s", body, w.Code, w.Body.String())
		}
	}
}

func TestNativeAnthropicErrorPreservesRetryMetadata(t *testing.T) {
	api := newTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "60")
		w.Header().Set("Request-Id", "req-upstream")
		w.Header().Set("Proxy-Connection", "keep-alive")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"rate_limit_error","message":"limited"}}`)
	}, func(m *config.Model) {
		m.FactoryProvider = config.FactoryProviderAnthropic
		m.UpstreamProtocol = config.UpstreamAnthropicMessages
	})
	api.engine.POST("/v1/messages", api.api.Messages)
	w := httptest.NewRecorder()
	api.engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"droid-test","messages":[]}`)))
	if w.Code != 429 || w.Header().Get("Retry-After") != "60" || w.Header().Get("Request-Id") != "req-upstream" || w.Header().Get("Proxy-Connection") != "" {
		t.Fatalf("lost retry metadata: %d, %v", w.Code, w.Header())
	}
}

func TestOAuthConfiguredHeadersProtectCredentials(t *testing.T) {
	for _, provider := range []config.OAuthProvider{config.OAuthProviderCodex, config.OAuthProviderXAI} {
		m := &config.Model{OAuthProvider: provider, ExtraHeaders: map[string]string{
			"Authorization": "Bearer override", "Chatgpt-Account-Id": "other", "X-XAI-Token-Auth": "other",
			"Api-Key": "override", "Proxy-Connection": "keep-alive", "Version": "custom", "x-grok-client-version": "custom",
		}}
		req := httptest.NewRequest(http.MethodPost, "https://example.com/responses", nil)
		applyOAuthResponsesHeaders(req, nil, m, &oauth.Token{AccessToken: "selected", AccountID: "account"}, []byte(`{"model":"grok-4.7"}`), "", "")
		if req.Header.Get("Authorization") != "Bearer selected" || req.Header.Get("Api-Key") != "" || req.Header.Get("Proxy-Connection") != "" {
			t.Fatalf("unsafe headers: %v", req.Header)
		}
		if req.Header.Get("Version") != "custom" || req.Header.Get("x-grok-client-version") != "custom" {
			t.Fatal("client metadata override ignored")
		}
		if provider == config.OAuthProviderCodex && req.Header.Get("Chatgpt-Account-Id") != "account" {
			t.Fatal("account identity overwritten")
		}
	}
}

func TestCodexQuotaCollectorMergesSeparateEvents(t *testing.T) {
	body := "data: {\"type\":\"codex.rate_limits\",\"rate_limits\":{\"primary\":{\"used_percent\":25}}}\r\n\r\n" +
		"data: {\"type\":\"codex.rate_limits\",\n" + "data: \"rate_limits\":{\"secondary\":{\"used_percent\":50}}}\n\n"
	q := codexQuotaFromSSEBody([]byte(body))
	if q == nil || q.Primary == nil || q.Secondary == nil || q.Primary.UsedPercent != 25 || q.Secondary.UsedPercent != 50 {
		t.Fatalf("lost telemetry: %#v", q)
	}
}

func TestPublicResponsesProviderNativeChat(t *testing.T) {
	for _, stream := range []bool{false, true} {
		response := `{"id":"chatcmpl-native","choices":[]}`
		request := `{"model":"droid-test","messages":[]}`
		if stream {
			request = `{"model":"droid-test","messages":[],"stream":true}`
			response = "data: [DONE]\n\n"
		}
		api := newTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/chat/completions" {
				t.Errorf("path = %q", r.URL.Path)
			}
			_, _ = io.WriteString(w, response)
		}, func(m *config.Model) {
			m.FactoryProvider = config.FactoryProviderOpenAI
			m.UpstreamProtocol = config.UpstreamOpenAIResponses
		})
		w := httptest.NewRecorder()
		api.engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(request)))
		if w.Code != http.StatusOK || w.Body.String() != response {
			t.Fatalf("native chat: %d, %s", w.Code, w.Body.String())
		}
	}
}

func TestOAuthCollectorPreservesIncompleteMultilineResponse(t *testing.T) {
	body := "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"partial\"}]}}\r\n\r\n" +
		"event: response.incomplete\r\ndata: {\"type\":\"response.incomplete\",\r\ndata: \"response\":{\"id\":\"resp_1\",\"status\":\"incomplete\",\"incomplete_details\":{\"reason\":\"max_output_tokens\"},\"output\":[]}}\r\n\r\n"
	response, err := responseFromResponsesSSE([]byte(body), responsesSSERepairOptions{RequireVisibleOutput: true})
	if err != nil {
		t.Fatal(err)
	}
	if gjson.GetBytes(response, "status").String() != "incomplete" || gjson.GetBytes(response, "output.0.content.0.text").String() != "partial" {
		t.Fatalf("lost partial result: %s", response)
	}
}

func TestOAuthCollectorReportsNestedFailureSafely(t *testing.T) {
	_, err := responseFromResponsesSSE([]byte("data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"message\":\"failed api_key=secret-sentinel\"}}}\n\n"), responsesSSERepairOptions{})
	if err == nil || !strings.Contains(err.Error(), "failed") || strings.Contains(err.Error(), "secret-sentinel") {
		t.Fatalf("unsafe or missing failure: %v", err)
	}
}

func TestOAuthCollectorAcceptsEmptyIncompleteResult(t *testing.T) {
	body := []byte("data: {\"type\":\"response.incomplete\",\"response\":{\"status\":\"incomplete\",\"output\":[],\"incomplete_details\":{\"reason\":\"max_output_tokens\"}}}\n\n")
	response, err := responseFromResponsesSSE(body, responsesSSERepairOptions{RequireVisibleOutput: true})
	if err != nil || gjson.GetBytes(response, "status").String() != "incomplete" {
		t.Fatalf("lost valid incomplete result: %s, %v", response, err)
	}
}
