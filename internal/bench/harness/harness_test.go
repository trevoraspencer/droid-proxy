package harness

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/trevoraspencer/droid-proxy/internal/bench/mockupstream"
)

func TestLoadConfigRejectsUnknownAndInvalidWorkloadFields(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		scenario string
		want     string
	}{
		{"unknown", "request: 1", "field request not found"},
		{"negative", "requests: -1", "requests must not be negative"},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "bench.yaml")
			raw := "targets:\n  - name: local\n    base_url: http://127.0.0.1:1\n    model: m\nscenarios:\n  - name: paid-call-shape\n    protocol: openai-chat\n    " + tc.scenario + "\n"
			if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := LoadConfig(path)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("LoadConfig error = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestResponsesFailureEventMarksStreamSampleFailed(t *testing.T) {
	t.Parallel()

	resp := &http.Response{Body: io.NopCloser(strings.NewReader("data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"message\":\"provider rejected request\"}}}\n\n"))}
	sample := Sample{Status: http.StatusOK}
	readStream(resp, ProtocolOpenAIResponses, time.Now(), &sample)
	if sample.ok() {
		t.Fatalf("failed Responses event counted as success: %+v", sample)
	}
	if !strings.Contains(sample.Err, "provider rejected request") {
		t.Fatalf("failure message was not retained: %q", sample.Err)
	}
}

func TestAnthropicMessageDeltaMergesCompleteUsage(t *testing.T) {
	t.Parallel()

	var usage Usage
	mergeStreamUsage([]byte(`{"type":"message_start","message":{"usage":{"input_tokens":10,"cache_read_input_tokens":2,"cache_creation_input_tokens":3}}}`), ProtocolAnthropicMessages, &usage)
	mergeStreamUsage([]byte(`{"type":"message_delta","usage":{"input_tokens":20,"output_tokens":7,"cache_read_input_tokens":11,"cache_creation_input_tokens":5}}`), ProtocolAnthropicMessages, &usage)
	want := (Usage{PromptTokens: 20, CompletionTokens: 7, CachedTokens: 11, CacheWriteTokens: 5})
	if usage != want {
		t.Fatalf("usage = %+v, want %+v", usage, want)
	}
}

// TestRunnerAgainstMock exercises the full measurement pipeline (payload
// build, SSE timing, usage extraction, summarization, report rendering)
// against the in-process mock provider for all three protocols.
func TestRunnerAgainstMock(t *testing.T) {
	mock := httptest.NewServer(mockupstream.New(mockupstream.Options{
		StreamChunks:        4,
		SimulatePromptCache: true,
	}).Handler())
	defer mock.Close()

	cfg := Config{
		Targets: []Target{
			{Name: "mock-direct", BaseURL: mock.URL, Model: "mock-model", Baseline: true},
		},
		Scenarios: []Scenario{
			{Name: "chat-nonstream", Protocol: ProtocolOpenAIChat, Requests: 3, Warmup: 1},
			{Name: "chat-stream", Protocol: ProtocolOpenAIChat, Stream: true, Requests: 3, HistoryTurns: 2, IncludeTools: true},
			{Name: "anthropic-stream", Protocol: ProtocolAnthropicMessages, Stream: true, Requests: 3, CacheControl: true},
			{Name: "responses-stream", Protocol: ProtocolOpenAIResponses, Stream: true, Requests: 2},
			{Name: "cache-growth", Protocol: ProtocolOpenAIChat, Requests: 4, HistoryTurns: 8, GrowingConversation: true},
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	runner := &Runner{Config: cfg}
	results, err := runner.Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(results) != len(cfg.Scenarios) {
		t.Fatalf("expected %d results, got %d", len(cfg.Scenarios), len(results))
	}
	streaming := map[string]bool{}
	for _, sc := range cfg.Scenarios {
		streaming[sc.Name] = sc.Stream
	}
	report := BuildReport(results)
	for _, s := range report.Summaries {
		if s.Skipped != "" {
			t.Fatalf("%s/%s unexpectedly skipped: %s", s.Scenario, s.Target, s.Skipped)
		}
		if s.Errors > 0 {
			t.Fatalf("%s/%s had %d errors", s.Scenario, s.Target, s.Errors)
		}
		if s.TTFBp50 <= 0 || s.Totalp50 <= 0 {
			t.Fatalf("%s/%s missing latency data: %+v", s.Scenario, s.Target, s)
		}
		if s.PromptTokens <= 0 {
			t.Fatalf("%s/%s missing usage extraction: %+v", s.Scenario, s.Target, s)
		}
		if streaming[s.Scenario] && s.TTFTp50 <= 0 {
			t.Fatalf("%s/%s missing TTFT for streaming scenario", s.Scenario, s.Target)
		}
	}

	// The growing-conversation scenario replays a stable prefix, so the mock's
	// simulated prompt cache must report hits.
	for _, s := range report.Summaries {
		if s.Scenario == "cache-growth" && s.CachedTokens <= 0 {
			t.Fatalf("cache-growth scenario should observe cached tokens, got %+v", s)
		}
	}

	var text, md bytes.Buffer
	report.WriteText(&text)
	report.WriteMarkdown(&md)
	for _, out := range []string{text.String(), md.String()} {
		if !strings.Contains(out, "chat-stream") || !strings.Contains(out, "mock-direct") {
			t.Fatalf("report rendering missing expected content:\n%s", out)
		}
	}
	var js bytes.Buffer
	if err := report.WriteJSON(&js); err != nil {
		t.Fatalf("json report: %v", err)
	}
}

// TestRunnerInterleavedRepeats pins the paired-delta aggregation path: with
// Repeat > 1, every cell runs once per rep, aggregates carry mean±sd, and
// non-baseline targets get deltas paired against the same-rep baseline.
func TestRunnerInterleavedRepeats(t *testing.T) {
	mock := httptest.NewServer(mockupstream.New(mockupstream.Options{StreamChunks: 3}).Handler())
	defer mock.Close()

	cfg := Config{
		Targets: []Target{
			{Name: "base", BaseURL: mock.URL, Model: "m", Baseline: true},
			{Name: "other", BaseURL: mock.URL, Model: "m"},
		},
		Scenarios: []Scenario{
			{Name: "s", Protocol: ProtocolOpenAIChat, Requests: 3},
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	runner := &Runner{Config: cfg, Repeat: 3}
	results, err := runner.Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(results) != 6 {
		t.Fatalf("expected 2 targets × 3 reps = 6 results, got %d", len(results))
	}
	report := BuildReport(results)
	if len(report.Aggregates) != 2 {
		t.Fatalf("expected 2 aggregates, got %d", len(report.Aggregates))
	}
	for _, a := range report.Aggregates {
		if a.Reps != 3 {
			t.Fatalf("aggregate %s/%s has %d reps", a.Scenario, a.Target, a.Reps)
		}
		if a.Totalp50ms.N != 3 || a.Totalp50ms.Mean <= 0 {
			t.Fatalf("aggregate %s/%s missing latency stats: %+v", a.Scenario, a.Target, a.Totalp50ms)
		}
		if a.Baseline && a.TotalDeltaPct.N != 0 {
			t.Fatalf("baseline should have no paired delta: %+v", a)
		}
		if !a.Baseline && a.TotalDeltaPct.N != 3 {
			t.Fatalf("non-baseline should have 3 paired deltas: %+v", a)
		}
	}
	var text bytes.Buffer
	report.WriteText(&text)
	if !strings.Contains(text.String(), "interleaved reps") || !strings.Contains(text.String(), "±") {
		t.Fatalf("aggregate rendering missing:\n%s", text.String())
	}
}

// TestGrowingConversationPrefixReuse pins that request i's messages extend
// request i-1's messages byte-for-byte (the property that makes provider
// prompt caches hit across agent turns).
func TestGrowingConversationPrefixReuse(t *testing.T) {
	sc := Scenario{Name: "g", Protocol: ProtocolOpenAIChat, HistoryTurns: 8, GrowingConversation: true}
	sc.applyDefaults()
	a, err := bodyForRequest(sc, "m", 1)
	if err != nil {
		t.Fatal(err)
	}
	b, err := bodyForRequest(sc, "m", 2)
	if err != nil {
		t.Fatal(err)
	}
	// The shorter conversation minus its final user turn must appear verbatim
	// inside the longer one.
	aStr, bStr := string(a), string(b)
	cut := strings.Index(aStr, `{"content":"user-final`)
	if cut < 0 {
		// key order is alphabetical from json.Marshal: content before role.
		t.Fatalf("could not locate final user message in %s", aStr)
	}
	prefix := aStr[:cut]
	if !strings.HasPrefix(bStr, prefix) {
		t.Fatalf("growing conversation does not reuse prefix bytes:\n%s\nvs\n%s", aStr, bStr)
	}
}
