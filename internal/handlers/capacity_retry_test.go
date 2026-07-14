package handlers

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/trevoraspencer/droid-proxy/internal/config"
)

func TestUpstreamCapacityRejection(t *testing.T) {
	xaiCapacityBody := []byte(`{"code":"Some resource has been exhausted","error":"The service is temporarily at capacity. Please retry your request shortly."}`)
	cases := []struct {
		name   string
		status int
		body   []byte
		want   bool
	}{
		{"429 any body", 429, []byte(`{"error":"slow down"}`), true},
		{"503 any body", 503, []byte(``), true},
		{"live xAI capacity 422", 422, xaiCapacityBody, true},
		{"capacity-worded 500", 500, xaiCapacityBody, true},
		{"unrelated 422", 422, []byte(`{"error":"invalid type: string \"hot\", expected f32"}`), false},
		{"unrelated 400", 400, []byte(`{"error":"bad tool schema"}`), false},
		{"200", 200, xaiCapacityBody, false},
	}
	for _, tc := range cases {
		if got := upstreamCapacityRejection(tc.status, tc.body); got != tc.want {
			t.Errorf("%s: upstreamCapacityRejection(%d) = %v, want %v", tc.name, tc.status, got, tc.want)
		}
	}
}

func TestCapacityRetryDelay(t *testing.T) {
	h := http.Header{}
	if d := capacityRetryDelay(h, 0); d != capacityRetryBaseDelay {
		t.Fatalf("attempt 0 default delay = %v, want %v", d, capacityRetryBaseDelay)
	}
	if d := capacityRetryDelay(h, 1); d != 2*capacityRetryBaseDelay {
		t.Fatalf("attempt 1 default delay = %v, want %v", d, 2*capacityRetryBaseDelay)
	}
	h.Set("Retry-After", "1")
	if d := capacityRetryDelay(h, 0); d < 500*time.Millisecond || d > time.Second {
		t.Fatalf("Retry-After 1s not honored: %v", d)
	}
	h.Set("Retry-After", "3600")
	if d := capacityRetryDelay(h, 0); d != capacityRetryMaxDelay {
		t.Fatalf("Retry-After should be capped at %v, got %v", capacityRetryMaxDelay, d)
	}
}

// shrinkCapacityDelays makes backoff instant for handler tests.
func shrinkCapacityDelays(t *testing.T) {
	t.Helper()
	prev := capacityRetryBaseDelay
	capacityRetryBaseDelay = time.Millisecond
	t.Cleanup(func() { capacityRetryBaseDelay = prev })
}

func TestResponses_OAuthXAICapacity429RetriesWithBackoff(t *testing.T) {
	shrinkCapacityDelays(t)
	var hits int
	api := newOAuthResponsesTestAPI(t, config.OAuthProviderXAI, config.UpstreamXAIResponses, func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = io.ReadAll(r.Body)
		if hits <= 2 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"code":"Some resource has been exhausted","error":"The service is temporarily at capacity. Please retry your request shortly."}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.completed\n" +
			`data: {"type":"response.completed","response":{"id":"r1","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"survived"}]}]}}` + "\n\n"))
	}, nil)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/responses", strings.NewReader(
		`{"model":"droid-oauth","stream":true,"input":[{"role":"user","content":[{"type":"input_text","text":"hi"}]}]}`))
	api.engine.ServeHTTP(w, req)

	if hits != 3 {
		t.Fatalf("expected 2 capacity retries then success, attempts = %d", hits)
	}
	if !strings.Contains(w.Body.String(), "survived") {
		t.Fatalf("expected recovered output, got %s", w.Body.String())
	}
}

func TestResponses_OAuthXAICapacityPersistentRelaysAfterBudget(t *testing.T) {
	shrinkCapacityDelays(t)
	var hits int
	api := newOAuthResponsesTestAPI(t, config.OAuthProviderXAI, config.UpstreamXAIResponses, func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"The service is temporarily at capacity."}`))
	}, nil)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/responses", strings.NewReader(
		`{"model":"droid-oauth","stream":true,"input":[{"role":"user","content":[{"type":"input_text","text":"hi"}]}]}`))
	api.engine.ServeHTTP(w, req)

	if hits != 1+capacityRetryMaxAttempts {
		t.Fatalf("expected %d attempts (1 + %d retries), got %d", 1+capacityRetryMaxAttempts, capacityRetryMaxAttempts, hits)
	}
	if !strings.Contains(w.Body.String(), "event: error") || !strings.Contains(w.Body.String(), "at capacity") {
		t.Fatalf("persistent capacity error must be relayed: %s", w.Body.String())
	}
}

// The 2026-07-14 mission-death scenario: a capacity-worded 422 on a thread
// carrying reasoning items. The strip-retry fires first (instant, useless for
// capacity), then the backoff retry must still ride out the blip.
func TestResponses_OAuthXAICapacity422WithReasoningStripThenBackoff(t *testing.T) {
	shrinkCapacityDelays(t)
	var hits int
	api := newOAuthResponsesTestAPI(t, config.OAuthProviderXAI, config.UpstreamXAIResponses, func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = io.ReadAll(r.Body)
		if hits <= 2 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"code":"Some resource has been exhausted","error":"The service is temporarily at capacity. Please retry your request shortly."}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.completed\n" +
			`data: {"type":"response.completed","response":{"id":"r1","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"survived"}]}]}}` + "\n\n"))
	}, nil)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/responses", strings.NewReader(
		`{"model":"droid-oauth","stream":true,"input":[{"id":"rs_native","type":"reasoning","encrypted_content":"blob"},{"role":"user","content":[{"type":"input_text","text":"hi"}]}]}`))
	api.engine.ServeHTTP(w, req)

	// attempt 1 (422) -> strip-retry attempt 2 (422) -> backoff retry attempt 3 (success)
	if hits != 3 {
		t.Fatalf("expected strip-retry then backoff retry then success, attempts = %d", hits)
	}
	if !strings.Contains(w.Body.String(), "survived") {
		t.Fatalf("expected recovered output, got %s", w.Body.String())
	}
}

func TestResponses_NativeCapacity429RetriesWithBackoff(t *testing.T) {
	shrinkCapacityDelays(t)
	var hits int
	api := newResponsesTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = io.ReadAll(r.Body)
		if hits == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"message":"overloaded"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","status":"completed","output":[]}`))
	}, nil)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(
		`{"model":"droid-gpt","stream":false,"input":"hi"}`))
	api.engine.ServeHTTP(w, req)

	if hits != 2 {
		t.Fatalf("expected 503 backoff retry then success, attempts = %d", hits)
	}
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 after retry, got %d body=%s", w.Code, w.Body.String())
	}
}
