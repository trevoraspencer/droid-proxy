package translate

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

// TestAnthropicToChatRequestDropsCacheControl pins that cache_control hints
// are dropped (not rejected) when translating to OpenAI Chat Completions.
// Factory Droid sends cache_control whenever Anthropic prompt caching is
// enabled; OpenAI-style upstreams cache prefixes implicitly, so the hint must
// not fail the request and must not leak upstream.
func TestAnthropicToChatRequestDropsCacheControl(t *testing.T) {
	body := []byte(`{
		"model": "claude-x",
		"max_tokens": 64,
		"system": [
			{"type": "text", "text": "be precise", "cache_control": {"type": "ephemeral"}}
		],
		"messages": [
			{"role": "user", "content": [
				{"type": "text", "text": "hello", "cache_control": {"type": "ephemeral"}}
			]},
			{"role": "assistant", "content": [
				{"type": "text", "text": "hi", "cache_control": {"type": "ephemeral"}}
			]},
			{"role": "user", "content": [{"type": "text", "text": "continue"}]}
		]
	}`)
	out, err := AnthropicToChatRequest(body, "upstream-model", nil)
	if err != nil {
		t.Fatalf("translation rejected cache_control: %v", err)
	}
	if strings.Contains(string(out), "cache_control") {
		t.Fatalf("cache_control leaked into translated payload: %s", out)
	}
	msgs := gjson.GetBytes(out, "messages").Array()
	if len(msgs) != 4 {
		t.Fatalf("expected 4 chat messages (system + 3), got %d: %s", len(msgs), out)
	}
	if got := msgs[0].Get("content").String(); got != "be precise" {
		t.Fatalf("system text lost: %q", got)
	}
	if got := msgs[1].Get("content").String(); got != "hello" {
		t.Fatalf("user text lost: %q", got)
	}
}
