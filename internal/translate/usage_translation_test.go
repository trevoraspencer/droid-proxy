package translate

import (
	"bytes"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

// TestChatToAnthropicResponseRelaysCachedTokens pins that OpenAI cached-token
// accounting survives the chat→anthropic response translation, so prompt-cache
// behavior stays observable to Droid on translated aliases.
func TestChatToAnthropicResponseRelaysCachedTokens(t *testing.T) {
	body := []byte(`{"id":"c1","object":"chat.completion","model":"m",` +
		`"choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],` +
		`"usage":{"prompt_tokens":900,"completion_tokens":12,"total_tokens":912,` +
		`"prompt_tokens_details":{"cached_tokens":768}}}`)
	out, err := ChatToAnthropicResponse(body, "m")
	if err != nil {
		t.Fatal(err)
	}
	if got := gjson.GetBytes(out, "usage.cache_read_input_tokens").Int(); got != 768 {
		t.Fatalf("cache_read_input_tokens = %d, want 768 (usage: %s)", got, gjson.GetBytes(out, "usage").Raw)
	}
	if got := gjson.GetBytes(out, "usage.input_tokens").Int(); got != 900 {
		t.Fatalf("input_tokens = %d, want 900", got)
	}
}

// TestForwardChatStreamToAnthropicEmitsUsage pins that the streaming
// translation relays the include_usage final chunk as message_delta usage.
func TestForwardChatStreamToAnthropicEmitsUsage(t *testing.T) {
	src := strings.Join([]string{
		`data: {"id":"c","model":"m","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
		`data: {"id":"c","model":"m","choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":null}]}`,
		`data: {"id":"c","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`data: {"id":"c","model":"m","choices":[],"usage":{"prompt_tokens":900,"completion_tokens":12,"prompt_tokens_details":{"cached_tokens":768}}}`,
		`data: [DONE]`,
	}, "\n\n") + "\n\n"

	var out bytes.Buffer
	if err := ForwardChatStreamToAnthropic(strings.NewReader(src), &out, func() {}, "m"); err != nil {
		t.Fatal(err)
	}
	usage := extractSSEData(t, out.String(), "message_delta", "usage")
	if usage.Get("output_tokens").Int() != 12 {
		t.Fatalf("message_delta usage.output_tokens = %s", usage.Raw)
	}
	if usage.Get("cache_read_input_tokens").Int() != 768 {
		t.Fatalf("message_delta usage missing cache_read_input_tokens: %s", usage.Raw)
	}
}

// TestForwardChatStreamToResponsesEmitsUsage pins the same relay for the
// Responses translation (response.completed carries usage).
func TestForwardChatStreamToResponsesEmitsUsage(t *testing.T) {
	src := strings.Join([]string{
		`data: {"id":"c","model":"m","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
		`data: {"id":"c","model":"m","choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":null}]}`,
		`data: {"id":"c","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`data: {"id":"c","model":"m","choices":[],"usage":{"prompt_tokens":900,"completion_tokens":12,"prompt_tokens_details":{"cached_tokens":768}}}`,
		`data: [DONE]`,
	}, "\n\n") + "\n\n"

	var out bytes.Buffer
	if err := ForwardChatStreamToResponses(strings.NewReader(src), &out, func() {}, "m"); err != nil {
		t.Fatal(err)
	}
	usage := extractSSEData(t, out.String(), "response.completed", "response.usage")
	if usage.Get("input_tokens").Int() != 900 || usage.Get("output_tokens").Int() != 12 {
		t.Fatalf("response.completed usage wrong: %s", usage.Raw)
	}
	if usage.Get("input_tokens_details.cached_tokens").Int() != 768 {
		t.Fatalf("response.completed usage missing cached_tokens: %s", usage.Raw)
	}
}

// TestResponsesToChatRequestPreservesPromptCacheKey pins prompt_cache_key
// forwarding through the responses→chat request translation.
func TestResponsesToChatRequestPreservesPromptCacheKey(t *testing.T) {
	body := []byte(`{"model":"m","prompt_cache_key":"pck-1","input":[{"role":"user","content":[{"type":"input_text","text":"hi"}]}]}`)
	out, err := ResponsesToChatRequest(body, "up", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := gjson.GetBytes(out, "prompt_cache_key").String(); got != "pck-1" {
		t.Fatalf("prompt_cache_key = %q, want pck-1 (%s)", got, out)
	}
}

// TestTranslatorExtraArgsUseSJSONPaths pins that translated routes apply
// extra_args with the same sjson-path semantics as native routes: dotted keys
// nest instead of becoming literal top-level fields.
func TestTranslatorExtraArgsUseSJSONPaths(t *testing.T) {
	body := []byte(`{"model":"claude-x","max_tokens":8,"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)
	out, err := AnthropicToChatRequest(body, "up", map[string]any{
		"thinking.type":    "enabled",
		"reasoning_effort": "high",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := gjson.GetBytes(out, "thinking.type").String(); got != "enabled" {
		t.Fatalf("dotted extra_args key did not nest: %s", out)
	}
	if bytes.Contains(out, []byte(`"thinking.type"`)) {
		t.Fatalf("dotted key emitted as literal field: %s", out)
	}
	// Determinism across repeats.
	for i := 0; i < 20; i++ {
		next, err := AnthropicToChatRequest(body, "up", map[string]any{
			"thinking.type":    "enabled",
			"reasoning_effort": "high",
		})
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(out, next) {
			t.Fatalf("translated extra_args nondeterministic:\n%s\nvs\n%s", out, next)
		}
	}
}

// extractSSEData finds the first SSE data payload whose type matches event
// and returns the gjson result at path within it.
func extractSSEData(t *testing.T, stream, event, path string) gjson.Result {
	t.Helper()
	for _, line := range strings.Split(stream, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if gjson.Get(data, "type").String() != event {
			continue
		}
		res := gjson.Get(data, path)
		if !res.Exists() {
			t.Fatalf("%s event has no %s: %s", event, path, data)
		}
		return res
	}
	t.Fatalf("stream has no %s event:\n%s", event, stream)
	return gjson.Result{}
}
