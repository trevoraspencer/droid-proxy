package harness

import (
	"bytes"
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/trevoraspencer/droid-proxy/internal/bench/mockupstream"
)

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
