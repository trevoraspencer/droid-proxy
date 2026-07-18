package translate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
)

// Benchmarks for the T3 translation hot paths. These quantify the per-request
// CPU and allocation overhead droid-proxy adds on translated routes, which is
// the main self-inflicted cost the proxy can control (network dominates
// everything else). Run with:
//
//	go test -bench=. -benchmem -run='^$' ./internal/translate/
func benchAnthropicRequest(turns, textBytes int) []byte {
	const fillerBase = "analyze the repository layout and fix the failing test "
	filler := strings.Repeat(fillerBase, textBytes/len(fillerBase)+1)[:textBytes]
	var messages []any
	for i := 0; i < turns; i++ {
		toolID := fmt.Sprintf("toolu_%d", i)
		messages = append(messages,
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "text", "text": fmt.Sprintf("turn %d: %s", i, filler)},
			}},
			map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "tool_use", "id": toolID, "name": "run_shell",
					"input": map[string]any{"command": "go test ./..."}},
			}},
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "tool_result", "tool_use_id": toolID, "content": filler},
			}},
			map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "text", "text": filler},
			}},
		)
	}
	messages = append(messages, map[string]any{"role": "user", "content": []any{
		map[string]any{"type": "text", "text": "final: summarize the failures"},
	}})
	body := map[string]any{
		"model":      "claude-x",
		"system":     []any{map[string]any{"type": "text", "text": filler, "cache_control": map[string]any{"type": "ephemeral"}}},
		"messages":   messages,
		"max_tokens": 512,
		"tools": []any{map[string]any{
			"name": "run_shell", "description": "Run a shell command",
			"input_schema": map[string]any{"type": "object", "properties": map[string]any{
				"command": map[string]any{"type": "string"},
			}},
		}},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		panic(err)
	}
	return raw
}

func benchmarkAnthropicToChat(b *testing.B, turns, textBytes int) {
	body := benchAnthropicRequest(turns, textBytes)
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := AnthropicToChatRequest(body, "upstream-model", map[string]any{"reasoning_effort": "high"}); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkAnthropicToChatRequestSmall(b *testing.B)   { benchmarkAnthropicToChat(b, 1, 256) }
func BenchmarkAnthropicToChatRequestAgentic(b *testing.B) { benchmarkAnthropicToChat(b, 12, 2048) }
func BenchmarkAnthropicToChatRequestLarge(b *testing.B)   { benchmarkAnthropicToChat(b, 24, 8192) }

func BenchmarkChatToAnthropicResponse(b *testing.B) {
	body := []byte(`{"id":"chatcmpl-1","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"` +
		strings.Repeat("the fix is to sort the keys before applying them ", 40) +
		`","tool_calls":[{"id":"call_1","type":"function","function":{"name":"run_shell","arguments":"{\"command\":\"go test ./...\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":900,"completion_tokens":120,"total_tokens":1020}}`)
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := ChatToAnthropicResponse(body, "m"); err != nil {
			b.Fatal(err)
		}
	}
}

// benchChatSSE builds a realistic upstream Chat Completions SSE stream with n
// content chunks.
func benchChatSSE(n int) []byte {
	var buf bytes.Buffer
	buf.WriteString(`data: {"id":"c","model":"m","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}` + "\n\n")
	for i := 0; i < n; i++ {
		fmt.Fprintf(&buf, `data: {"id":"c","model":"m","choices":[{"index":0,"delta":{"content":"tok%d "},"finish_reason":null}]}`+"\n\n", i)
	}
	buf.WriteString(`data: {"id":"c","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n")
	buf.WriteString(`data: {"id":"c","model":"m","choices":[],"usage":{"prompt_tokens":900,"completion_tokens":120,"total_tokens":1020}}` + "\n\n")
	buf.WriteString("data: [DONE]\n\n")
	return buf.Bytes()
}

func BenchmarkForwardChatStreamToAnthropic(b *testing.B) {
	src := benchChatSSE(120)
	b.SetBytes(int64(len(src)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := ForwardChatStreamToAnthropic(bytes.NewReader(src), io.Discard, func() {}, "m"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkForwardChatStreamToResponses(b *testing.B) {
	src := benchChatSSE(120)
	b.SetBytes(int64(len(src)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := ForwardChatStreamToResponses(bytes.NewReader(src), io.Discard, func() {}, "m"); err != nil {
			b.Fatal(err)
		}
	}
}
