package handlers

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/trevoraspencer/droid-proxy/internal/config"
)

// TestApplyUpstreamPayloadOverridesDeterministic pins the property the
// cache-fidelity suite relies on: identical inputs always produce identical
// upstream bytes, even with multiple extra_args (map iteration order must not
// leak into the payload).
func TestApplyUpstreamPayloadOverridesDeterministic(t *testing.T) {
	m := &config.Model{
		Alias:         "bench",
		UpstreamModel: "real-model",
		ExtraArgs: map[string]any{
			"reasoning_effort": "high",
			"thinking":         map[string]any{"type": "enabled"},
			"temperature":      0.2,
			"top_p":            0.9,
		},
	}
	body := []byte(`{"model":"alias","messages":[{"role":"user","content":"hi"}]}`)
	first := applyUpstreamPayloadOverrides(body, m)
	for i := 0; i < 50; i++ {
		next := applyUpstreamPayloadOverrides(body, m)
		if string(next) != string(first) {
			t.Fatalf("iteration %d produced different bytes:\n%s\nvs\n%s", i, first, next)
		}
	}
}

// BenchmarkApplyUpstreamPayloadOverrides measures the per-request payload
// rewrite on native passthrough routes. Run with:
//
//	go test -bench=. -benchmem -run='^$' ./internal/handlers/
func BenchmarkApplyUpstreamPayloadOverrides(b *testing.B) {
	m := &config.Model{
		Alias:         "bench",
		UpstreamModel: "real-model",
		ExtraArgs: map[string]any{
			"reasoning_effort": "high",
			"thinking":         map[string]any{"type": "enabled"},
		},
	}
	filler := strings.Repeat("inspect upstream latency and propagate cache control ", 40)
	var messages []any
	for i := 0; i < 20; i++ {
		messages = append(messages,
			map[string]any{"role": "user", "content": fmt.Sprintf("turn %d: %s", i, filler)},
			map[string]any{"role": "assistant", "content": filler},
		)
	}
	body, err := json.Marshal(map[string]any{"model": "alias", "messages": messages, "max_tokens": 512})
	if err != nil {
		b.Fatal(err)
	}
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = applyUpstreamPayloadOverrides(body, m)
	}
}
