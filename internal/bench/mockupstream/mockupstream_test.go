package mockupstream

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

var httptestClient = http.DefaultClient

func newTestServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	s := New(Options{StreamChunks: 5, SimulatePromptCache: true})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return s, ts
}

func post(t *testing.T, url, body string) (int, []byte) {
	t.Helper()
	resp, err := httptestClient.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp.StatusCode, buf.Bytes()
}

func TestChatNonStreamUsageAndCapture(t *testing.T) {
	_, ts := newTestServer(t)
	body := `{"model":"m","messages":[{"role":"system","content":"sys"},{"role":"user","content":"one"},{"role":"user","content":"two"}]}`

	status, raw := post(t, ts.URL+"/v1/chat/completions", body)
	if status != 200 {
		t.Fatalf("status %d: %s", status, raw)
	}
	if got := gjson.GetBytes(raw, "usage.prompt_tokens_details.cached_tokens").Int(); got != 0 {
		t.Fatalf("first request should have zero cached tokens, got %d", got)
	}

	// Identical prefix (all but last message) → cache hit.
	status, raw = post(t, ts.URL+"/v1/chat/completions", body)
	if status != 200 {
		t.Fatalf("status %d", status)
	}
	if got := gjson.GetBytes(raw, "usage.prompt_tokens_details.cached_tokens").Int(); got <= 0 {
		t.Fatalf("repeat request should report cached tokens, got %d", got)
	}

	// Capture endpoint returns both request bodies verbatim.
	resp, err := httptestClient.Get(ts.URL + "/__mock/requests")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var payload struct {
		Requests []CapturedRequest `json:"requests"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Requests) != 2 {
		t.Fatalf("expected 2 captures, got %d", len(payload.Requests))
	}
	if string(payload.Requests[0].Body) != body {
		t.Fatalf("captured body mismatch:\n%s\nvs\n%s", payload.Requests[0].Body, body)
	}
}

func TestChatStreamShape(t *testing.T) {
	_, ts := newTestServer(t)
	body := `{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	status, raw := post(t, ts.URL+"/v1/chat/completions", body)
	if status != 200 {
		t.Fatalf("status %d", status)
	}
	text := string(raw)
	if !strings.Contains(text, "data: [DONE]") {
		t.Fatal("stream missing [DONE] terminator")
	}
	if got := strings.Count(text, `"content":"tok`); got != 5 {
		t.Fatalf("expected 5 content chunks, got %d", got)
	}
	if !strings.Contains(text, `"usage"`) {
		t.Fatal("stream missing usage frame")
	}
}

func TestAnthropicStreamShape(t *testing.T) {
	_, ts := newTestServer(t)
	body := `{"model":"m","stream":true,"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`
	status, raw := post(t, ts.URL+"/v1/messages", body)
	if status != 200 {
		t.Fatalf("status %d", status)
	}
	text := string(raw)
	for _, marker := range []string{"event: message_start", "event: content_block_delta", "event: message_delta", "event: message_stop"} {
		if !strings.Contains(text, marker) {
			t.Fatalf("stream missing %q", marker)
		}
	}
}

func TestResponsesNonStream(t *testing.T) {
	_, ts := newTestServer(t)
	body := `{"model":"m","instructions":"sys","input":[{"role":"user","content":[{"type":"input_text","text":"hi"}]}]}`
	status, raw := post(t, ts.URL+"/v1/responses", body)
	if status != 200 {
		t.Fatalf("status %d", status)
	}
	if gjson.GetBytes(raw, "status").String() != "completed" {
		t.Fatalf("unexpected response: %s", raw)
	}
	if !gjson.GetBytes(raw, "usage.input_tokens_details.cached_tokens").Exists() {
		t.Fatal("usage missing cached_tokens detail")
	}
}

func TestResetClearsCacheAndCaptures(t *testing.T) {
	_, ts := newTestServer(t)
	body := `{"model":"m","messages":[{"role":"user","content":"a"},{"role":"user","content":"b"}]}`
	post(t, ts.URL+"/v1/chat/completions", body)
	post(t, ts.URL+"/__mock/reset", "")
	_, raw := post(t, ts.URL+"/v1/chat/completions", body)
	if got := gjson.GetBytes(raw, "usage.prompt_tokens_details.cached_tokens").Int(); got != 0 {
		t.Fatalf("cache should be cold after reset, got %d cached tokens", got)
	}
}

func TestRequestBodyLimitRejectsBeforeCapture(t *testing.T) {
	t.Parallel()

	s := New(Options{MaxRequestBodyBytes: 32})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	status, raw := post(t, ts.URL+"/v1/chat/completions", strings.Repeat("x", 33))
	if status != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d: %s", status, http.StatusRequestEntityTooLarge, raw)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.captures) != 0 {
		t.Fatalf("oversized request was retained in capture ring: %d captures", len(s.captures))
	}
}

func TestCaptureRingEvictsByAggregateBodyBytes(t *testing.T) {
	t.Parallel()

	s := New(Options{
		CaptureLimit:        100,
		MaxRequestBodyBytes: 32,
		CaptureBytesLimit:   64,
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	for i := 0; i < 3; i++ {
		post(t, ts.URL+"/v1/chat/completions", strings.Repeat(string(rune('a'+i)), 32))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.captureBytes != 64 {
		t.Fatalf("retained body bytes = %d, want 64", s.captureBytes)
	}
	if len(s.captures) != 2 || s.captures[0].Seq != 2 || s.captures[1].Seq != 3 {
		t.Fatalf("capture ring did not retain only the newest bodies: %+v", s.captures)
	}
}
