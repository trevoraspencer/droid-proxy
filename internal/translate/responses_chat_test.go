package translate

import (
	"encoding/json"
	"strings"
	"testing"
)

func decodeObject(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("decode JSON: %v\n%s", err, string(b))
	}
	return out
}

func TestResponsesToChatRequest_TextPriorTurnsUnicodeAndControls(t *testing.T) {
	body := []byte(`{
		"model":"droid-gpt",
		"instructions":"Be precise.\nUse metric.",
		"input":[
			{"role":"user","content":[{"type":"input_text","text":"Hello 🌍\nLine two"}]},
			{"role":"assistant","content":[{"type":"output_text","text":"Bonjour"}]},
			{"role":"user","content":"Next"}
		],
		"max_output_tokens":77,
		"temperature":0.2,
		"top_p":0.9
	}`)
	got, err := ResponsesToChatRequest(body, "gpt-test", map[string]any{"seed": float64(123)})
	if err != nil {
		t.Fatal(err)
	}
	obj := decodeObject(t, got)
	if obj["model"] != "gpt-test" || obj["max_tokens"].(float64) != 77 || obj["seed"].(float64) != 123 {
		t.Fatalf("unexpected mapped controls: %#v", obj)
	}
	msgs := obj["messages"].([]any)
	if len(msgs) != 4 {
		t.Fatalf("expected 4 messages, got %d: %#v", len(msgs), msgs)
	}
	want := []struct{ role, content string }{
		{"system", "Be precise.\nUse metric."},
		{"user", "Hello 🌍\nLine two"},
		{"assistant", "Bonjour"},
		{"user", "Next"},
	}
	for i, w := range want {
		msg := msgs[i].(map[string]any)
		if msg["role"] != w.role || msg["content"] != w.content {
			t.Fatalf("message %d = %#v, want role=%q content=%q", i, msg, w.role, w.content)
		}
	}
}

func TestResponsesToChatRequest_ToolsToolChoiceAndToolResults(t *testing.T) {
	body := []byte(`{
		"model":"droid-gpt",
		"input":[
			{"role":"user","content":"weather?"},
			{"type":"function_call","call_id":"call_1","name":"get_weather","arguments":"{\"city\":\"Paris\"}"},
			{"type":"function_call_output","call_id":"call_1","output":"sunny"}
		],
		"tools":[{"type":"function","name":"get_weather","description":"Get weather","parameters":{"type":"object","properties":{"city":{"type":"string"}}},"strict":true}],
		"tool_choice":{"type":"function","name":"get_weather"}
	}`)
	got, err := ResponsesToChatRequest(body, "gpt-test", nil)
	if err != nil {
		t.Fatal(err)
	}
	obj := decodeObject(t, got)
	tools := obj["tools"].([]any)
	fn := tools[0].(map[string]any)["function"].(map[string]any)
	if fn["name"] != "get_weather" || fn["strict"] != true {
		t.Fatalf("bad tool mapping: %#v", tools[0])
	}
	choice := obj["tool_choice"].(map[string]any)
	if choice["type"] != "function" || choice["function"].(map[string]any)["name"] != "get_weather" {
		t.Fatalf("bad tool choice: %#v", choice)
	}
	msgs := obj["messages"].([]any)
	asst := msgs[1].(map[string]any)
	tc := asst["tool_calls"].([]any)[0].(map[string]any)
	if tc["id"] != "call_1" || tc["function"].(map[string]any)["arguments"] != `{"city":"Paris"}` {
		t.Fatalf("bad assistant tool call: %#v", asst)
	}
	tool := msgs[2].(map[string]any)
	if tool["role"] != "tool" || tool["tool_call_id"] != "call_1" || tool["content"] != "sunny" {
		t.Fatalf("bad tool result message: %#v", tool)
	}
}

func TestChatToResponsesResponse_TextUsageAndNoChoices(t *testing.T) {
	body := []byte(`{"id":"chat_1","model":"gpt-test","created":123,"choices":[{"index":0,"message":{"role":"assistant","content":"Hi there"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":7,"total_tokens":12}}`)
	got, err := ChatToResponsesResponse(body, "gpt-test")
	if err != nil {
		t.Fatal(err)
	}
	obj := decodeObject(t, got)
	if _, ok := obj["choices"]; ok {
		t.Fatalf("Responses output leaked raw choices: %#v", obj)
	}
	if obj["status"] != "completed" || obj["object"] != "response" {
		t.Fatalf("bad response envelope: %#v", obj)
	}
	msg := obj["output"].([]any)[0].(map[string]any)
	text := msg["content"].([]any)[0].(map[string]any)["text"]
	if msg["type"] != "message" || text != "Hi there" {
		t.Fatalf("bad output message: %#v", msg)
	}
	usage := obj["usage"].(map[string]any)
	if usage["input_tokens"].(float64) != 5 || usage["output_tokens"].(float64) != 7 || usage["total_tokens"].(float64) != 12 {
		t.Fatalf("bad usage: %#v", usage)
	}
}

func TestChatToResponsesResponse_OneAndMultipleToolCalls(t *testing.T) {
	for _, tc := range []struct {
		name  string
		calls string
		want  int
	}{
		{"one", `[{"id":"call_1","type":"function","function":{"name":"one","arguments":"{\"n\":1}"}}]`, 1},
		{"multiple", `[{"id":"call_1","type":"function","function":{"name":"one","arguments":"{\"n\":1}"}},{"id":"call_2","type":"function","function":{"name":"two","arguments":"{\"n\":2}"}}]`, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(`{"id":"chat_1","choices":[{"message":{"role":"assistant","content":null,"tool_calls":` + tc.calls + `},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`)
			got, err := ChatToResponsesResponse(body, "gpt-test")
			if err != nil {
				t.Fatal(err)
			}
			obj := decodeObject(t, got)
			output := obj["output"].([]any)
			if len(output) != tc.want {
				t.Fatalf("output len=%d want=%d: %#v", len(output), tc.want, output)
			}
			first := output[0].(map[string]any)
			if first["type"] != "function_call" || first["call_id"] != "call_1" || first["name"] != "one" {
				t.Fatalf("bad function_call item: %#v", first)
			}
			var args map[string]any
			if err := json.Unmarshal([]byte(first["arguments"].(string)), &args); err != nil || args["n"].(float64) != 1 {
				t.Fatalf("arguments lost JSON semantics: args=%#v err=%v", args, err)
			}
		})
	}
}

func TestChatToResponsesResponse_TextAndToolCallsPreserved(t *testing.T) {
	body := []byte(`{"id":"chat_1","model":"gpt-test","created":123,"choices":[{"index":0,"message":{"role":"assistant","content":"I will check that now.","tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{\"city\":\"Paris\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":4,"completion_tokens":6,"total_tokens":10}}`)
	got, err := ChatToResponsesResponse(body, "gpt-test")
	if err != nil {
		t.Fatal(err)
	}
	obj := decodeObject(t, got)
	if obj["status"] != "completed" {
		t.Fatalf("status=%v want completed obj=%#v", obj["status"], obj)
	}
	output := obj["output"].([]any)
	if len(output) != 2 {
		t.Fatalf("output len=%d want 2: %#v", len(output), output)
	}
	msg := output[0].(map[string]any)
	if msg["type"] != "message" || msg["role"] != "assistant" {
		t.Fatalf("bad message output: %#v", msg)
	}
	text := msg["content"].([]any)[0].(map[string]any)
	if text["type"] != "output_text" || text["text"] != "I will check that now." {
		t.Fatalf("assistant text was not preserved first: %#v", msg)
	}
	call := output[1].(map[string]any)
	if call["type"] != "function_call" || call["call_id"] != "call_1" || call["name"] != "lookup" {
		t.Fatalf("bad function call output: %#v", call)
	}
}

func TestChatToResponsesResponse_FinishReasonMapping(t *testing.T) {
	for _, tc := range []struct {
		reason     string
		status     string
		incomplete string
	}{
		{"stop", "completed", ""},
		{"length", "incomplete", "max_output_tokens"},
		{"content_filter", "incomplete", "content_filter"},
		{"tool_calls", "completed", ""},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			body := []byte(`{"id":"chat_1","choices":[{"message":{"role":"assistant","content":"Hi"},"finish_reason":"` + tc.reason + `"}]}`)
			got, err := ChatToResponsesResponse(body, "gpt-test")
			if err != nil {
				t.Fatal(err)
			}
			obj := decodeObject(t, got)
			if obj["status"] != tc.status {
				t.Fatalf("status=%v want=%s obj=%#v", obj["status"], tc.status, obj)
			}
			if tc.incomplete != "" {
				details := obj["incomplete_details"].(map[string]any)
				if details["reason"] != tc.incomplete {
					t.Fatalf("incomplete reason=%v want=%s", details["reason"], tc.incomplete)
				}
			}
		})
	}
}

func TestChatStreamToResponsesSSERejectsMultiChoiceAndMalformed(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{"multi", `data: {"choices":[{"index":0,"delta":{"content":"a"}},{"index":1,"delta":{"content":"b"}}]}` + "\n\n", "multiple choices"},
		{"malformed", "data: {not-json}\n\n", "invalid Chat stream JSON"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ChatStreamToResponsesSSE(strings.NewReader(tc.body), "gpt-test")
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(got), "event: error") || !strings.Contains(string(got), tc.want) {
				t.Fatalf("expected SSE error containing %q, got %s", tc.want, got)
			}
		})
	}
}

func TestChatStreamToResponsesSSERejectsMalformedToolArguments(t *testing.T) {
	body := strings.Join([]string{
		`data: {"id":"chat_1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{\"q\""}}]},"finish_reason":null}]}`,
		``,
		`data: {"id":"chat_1","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n")
	got, err := ChatStreamToResponsesSSE(strings.NewReader(body), "gpt-test")
	if err != nil {
		t.Fatal(err)
	}
	events := parseTranslateSSE(t, string(got))
	if len(events) != 1 || events[0].name != "error" {
		t.Fatalf("expected exactly one error event, got %#v\n%s", events, got)
	}
	if strings.Contains(string(got), "response.completed") {
		t.Fatalf("malformed final arguments must not complete:\n%s", got)
	}
}

func TestChatStreamToAnthropicSSERejectsMalformedToolArguments(t *testing.T) {
	body := strings.Join([]string{
		`data: {"id":"chat_1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"toolu_1","type":"function","function":{"name":"lookup","arguments":"{\"q\""}}]},"finish_reason":null}]}`,
		``,
		`data: {"id":"chat_1","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n")
	got, err := ChatStreamToAnthropicSSE(strings.NewReader(body), "claude-test")
	if err != nil {
		t.Fatal(err)
	}
	events := parseTranslateSSE(t, string(got))
	if len(events) != 1 || events[0].name != "error" {
		t.Fatalf("expected exactly one error event, got %#v\n%s", events, got)
	}
	if strings.Contains(string(got), "message_stop") {
		t.Fatalf("malformed final arguments must not stop successfully:\n%s", got)
	}
}

func TestChatStreamToResponsesSSETextAndFirstToolUseDistinctOutputIndexes(t *testing.T) {
	body := strings.Join([]string{
		`data: {"id":"chat_1","choices":[{"index":0,"delta":{"content":"Hi"},"finish_reason":null}]}`,
		``,
		`data: {"id":"chat_1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"x\"}"}}]},"finish_reason":"tool_calls"}]}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n")
	got, err := ChatStreamToResponsesSSE(strings.NewReader(body), "gpt-test")
	if err != nil {
		t.Fatal(err)
	}
	events := parseTranslateSSE(t, string(got))
	var textIndexes, toolIndexes []float64
	completed := 0
	for _, ev := range events {
		switch ev.name {
		case "response.output_text.delta":
			textIndexes = append(textIndexes, ev.payload["output_index"].(float64))
		case "response.output_item.added":
			item := ev.payload["item"].(map[string]any)
			if item["type"] == "function_call" {
				toolIndexes = append(toolIndexes, ev.payload["output_index"].(float64))
			}
		case "response.completed":
			completed++
		}
	}
	if completed != 1 || len(textIndexes) != 1 || textIndexes[0] != 0 || len(toolIndexes) != 1 || toolIndexes[0] == 0 {
		t.Fatalf("expected text at output_index 0 and tool at distinct index with one completion; events=%#v\n%s", events, got)
	}
}

func TestChatStreamToAnthropicSSETextAndFirstToolUseDistinctBlockIndexes(t *testing.T) {
	body := strings.Join([]string{
		`data: {"id":"chat_1","choices":[{"index":0,"delta":{"content":"Hi"},"finish_reason":null}]}`,
		``,
		`data: {"id":"chat_1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"toolu_1","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"x\"}"}}]},"finish_reason":"tool_calls"}]}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n")
	got, err := ChatStreamToAnthropicSSE(strings.NewReader(body), "claude-test")
	if err != nil {
		t.Fatal(err)
	}
	events := parseTranslateSSE(t, string(got))
	var textIndex, toolIndex float64 = -1, -1
	stops := 0
	for _, ev := range events {
		if ev.name == "content_block_start" {
			block := ev.payload["content_block"].(map[string]any)
			if block["type"] == "text" {
				textIndex = ev.payload["index"].(float64)
			}
			if block["type"] == "tool_use" {
				toolIndex = ev.payload["index"].(float64)
			}
		}
		if ev.name == "message_stop" {
			stops++
		}
	}
	if stops != 1 || textIndex != 0 || toolIndex <= 0 {
		t.Fatalf("expected text block 0 and tool block at distinct positive index with one stop; events=%#v\n%s", events, got)
	}
}

func TestChatToAnthropicResponse_FinishReasonMapping(t *testing.T) {
	for _, tc := range []struct {
		reason string
		want   string
	}{
		{"stop", "end_turn"},
		{"length", "max_tokens"},
		{"content_filter", "stop_sequence"},
		{"tool_calls", "tool_use"},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			body := []byte(`{"id":"chat_1","choices":[{"message":{"role":"assistant","content":"Hi"},"finish_reason":"` + tc.reason + `"}]}`)
			got, err := ChatToAnthropicResponse(body, "claude-test")
			if err != nil {
				t.Fatal(err)
			}
			obj := decodeObject(t, got)
			if obj["stop_reason"] != tc.want {
				t.Fatalf("stop_reason=%v want=%s obj=%#v", obj["stop_reason"], tc.want, obj)
			}
		})
	}
}

func TestChatToAnthropicResponse_TextAndToolCallsPreserved(t *testing.T) {
	body := []byte(`{"id":"chat_1","model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":"I will check that now.","tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{\"city\":\"Paris\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":4,"completion_tokens":6}}`)
	got, err := ChatToAnthropicResponse(body, "claude-test")
	if err != nil {
		t.Fatal(err)
	}
	obj := decodeObject(t, got)
	if obj["stop_reason"] != "tool_use" {
		t.Fatalf("stop_reason=%v want tool_use obj=%#v", obj["stop_reason"], obj)
	}
	content := obj["content"].([]any)
	if len(content) != 2 {
		t.Fatalf("content len=%d want 2: %#v", len(content), content)
	}
	text := content[0].(map[string]any)
	if text["type"] != "text" || text["text"] != "I will check that now." {
		t.Fatalf("assistant text was not preserved first: %#v", content)
	}
	tool := content[1].(map[string]any)
	if tool["type"] != "tool_use" || tool["id"] != "call_1" || tool["name"] != "lookup" {
		t.Fatalf("bad tool_use block: %#v", tool)
	}
	input := tool["input"].(map[string]any)
	if input["city"] != "Paris" {
		t.Fatalf("tool input lost JSON semantics: %#v", input)
	}
}

type translateSSEEvent struct {
	name    string
	payload map[string]any
}

func parseTranslateSSE(t *testing.T, body string) []translateSSEEvent {
	t.Helper()
	var events []translateSSEEvent
	for _, frame := range strings.Split(strings.TrimSpace(body), "\n\n") {
		if strings.TrimSpace(frame) == "" {
			continue
		}
		var ev translateSSEEvent
		for _, line := range strings.Split(frame, "\n") {
			if strings.HasPrefix(line, "event:") {
				ev.name = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			}
			if strings.HasPrefix(line, "data:") {
				var payload map[string]any
				if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &payload); err != nil {
					t.Fatalf("invalid SSE JSON payload in frame %q: %v", frame, err)
				}
				ev.payload = payload
			}
		}
		if ev.name == "" || ev.payload == nil {
			t.Fatalf("incomplete SSE frame %q", frame)
		}
		events = append(events, ev)
	}
	return events
}

// TestChatStreamTranslatorsTolerateEmptyChoiceChunks pins that zero-choice
// chunks — Azure OpenAI's leading prompt-filter-results chunk and the final
// usage chunk emitted under stream_options.include_usage — are skipped rather
// than aborting the translated stream with an error frame.
func TestChatStreamTranslatorsTolerateEmptyChoiceChunks(t *testing.T) {
	body := strings.Join([]string{
		`data: {"id":"chat_1","choices":[],"prompt_filter_results":[{"prompt_index":0}]}`,
		``,
		`data: {"id":"chat_1","choices":[{"index":0,"delta":{"content":"hello"},"finish_reason":null}]}`,
		``,
		`data: {"id":"chat_1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		``,
		`data: {"id":"chat_1","choices":[],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n")

	t.Run("responses_target", func(t *testing.T) {
		got, err := ChatStreamToResponsesSSE(strings.NewReader(body), "gpt-test")
		if err != nil {
			t.Fatal(err)
		}
		out := string(got)
		if strings.Contains(out, "event: error") {
			t.Fatalf("empty-choice chunks must not abort the stream:\n%s", out)
		}
		if !strings.Contains(out, "response.completed") {
			t.Fatalf("expected response.completed, got:\n%s", out)
		}
		if !strings.Contains(out, "hello") {
			t.Fatalf("expected text delta preserved, got:\n%s", out)
		}
	})

	t.Run("anthropic_target", func(t *testing.T) {
		got, err := ChatStreamToAnthropicSSE(strings.NewReader(body), "claude-test")
		if err != nil {
			t.Fatal(err)
		}
		out := string(got)
		if strings.Contains(out, "event: error") {
			t.Fatalf("empty-choice chunks must not abort the stream:\n%s", out)
		}
		if !strings.Contains(out, "message_stop") {
			t.Fatalf("expected message_stop, got:\n%s", out)
		}
		if !strings.Contains(out, "hello") {
			t.Fatalf("expected text delta preserved, got:\n%s", out)
		}
	})
}
