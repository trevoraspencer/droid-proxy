package harness

import (
	"encoding/json"
	"fmt"
	"strings"
)

// filler produces deterministic prose of approximately n bytes, salted so
// different slots never collide byte-for-byte. The salt (which carries the
// uniqueness nonce) is never truncated — the result may exceed n when n is
// smaller than the salt, because losing the nonce would silently defeat
// unique_prompts.
func filler(n int, salt string) string {
	if n <= 0 {
		return ""
	}
	const words = "analyze the repository layout resolve the failing test refactor the handler " +
		"inspect upstream latency propagate cache control retry with backoff stream the tokens "
	var b strings.Builder
	b.WriteString(salt)
	b.WriteString(": ")
	if n < b.Len() {
		n = b.Len()
	}
	for b.Len() < n {
		b.WriteString(words)
	}
	return b.String()[:n]
}

// buildBody constructs the request body for scenario sc against protocol p
// using the given model name. turns is the number of history turns to include
// (used by growing-conversation mode); nonce salts the final user message when
// unique prompts are requested.
func buildBody(sc Scenario, p Protocol, model string, turns int, nonce string) ([]byte, error) {
	system := filler(sc.SystemPromptBytes, "system:"+sc.Name)
	finalUser := filler(sc.UserMessageBytes, "user-final:"+sc.Name+nonce)

	switch p {
	case ProtocolOpenAIChat:
		return buildChatBody(sc, model, system, finalUser, turns)
	case ProtocolAnthropicMessages:
		return buildAnthropicBody(sc, model, system, finalUser, turns)
	case ProtocolOpenAIResponses:
		return buildResponsesBody(sc, model, system, finalUser, turns)
	}
	return nil, fmt.Errorf("unsupported protocol %q", p)
}

func historyPair(sc Scenario, i int) (user, assistant string) {
	user = filler(sc.UserMessageBytes, fmt.Sprintf("user-%s-%d", sc.Name, i))
	assistant = filler(sc.UserMessageBytes/2+64, fmt.Sprintf("assistant-%s-%d", sc.Name, i))
	return
}

var chatTools = []any{
	map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        "run_shell",
			"description": "Run a shell command in the workspace and return stdout",
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"command": map[string]any{"type": "string", "description": "command to execute"},
				},
				"required": []any{"command"},
			},
		},
	},
}

var anthropicTools = []any{
	map[string]any{
		"name":        "run_shell",
		"description": "Run a shell command in the workspace and return stdout",
		"input_schema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{"type": "string", "description": "command to execute"},
			},
			"required": []any{"command"},
		},
	},
}

func buildChatBody(sc Scenario, model, system, finalUser string, turns int) ([]byte, error) {
	messages := []any{
		map[string]any{"role": "system", "content": system},
	}
	for i := 0; i < turns; i++ {
		user, assistant := historyPair(sc, i)
		messages = append(messages, map[string]any{"role": "user", "content": user})
		if sc.IncludeTools {
			callID := fmt.Sprintf("call_%s_%d", sc.Name, i)
			messages = append(messages,
				map[string]any{
					"role":    "assistant",
					"content": nil,
					"tool_calls": []any{map[string]any{
						"id":   callID,
						"type": "function",
						"function": map[string]any{
							"name":      "run_shell",
							"arguments": `{"command":"go test ./..."}`,
						},
					}},
				},
				map[string]any{
					"role":         "tool",
					"tool_call_id": callID,
					"content":      filler(sc.UserMessageBytes, fmt.Sprintf("tool-%s-%d", sc.Name, i)),
				},
			)
		}
		messages = append(messages, map[string]any{"role": "assistant", "content": assistant})
	}
	messages = append(messages, map[string]any{"role": "user", "content": finalUser})

	body := map[string]any{
		"model":      model,
		"messages":   messages,
		"max_tokens": sc.MaxTokens,
	}
	if sc.IncludeTools {
		body["tools"] = chatTools
	}
	if sc.Stream {
		body["stream"] = true
		body["stream_options"] = map[string]any{"include_usage": true}
	}
	return json.Marshal(body)
}

func buildAnthropicBody(sc Scenario, model, system, finalUser string, turns int) ([]byte, error) {
	systemBlock := map[string]any{"type": "text", "text": system}
	if sc.CacheControl {
		systemBlock["cache_control"] = map[string]any{"type": "ephemeral"}
	}
	var messages []any
	for i := 0; i < turns; i++ {
		user, assistant := historyPair(sc, i)
		messages = append(messages, map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "text", "text": user},
		}})
		if sc.IncludeTools {
			toolID := fmt.Sprintf("toolu_%s_%d", sc.Name, i)
			messages = append(messages,
				map[string]any{"role": "assistant", "content": []any{
					map[string]any{"type": "tool_use", "id": toolID, "name": "run_shell",
						"input": map[string]any{"command": "go test ./..."}},
				}},
				map[string]any{"role": "user", "content": []any{
					map[string]any{"type": "tool_result", "tool_use_id": toolID,
						"content": filler(sc.UserMessageBytes, fmt.Sprintf("tool-%s-%d", sc.Name, i))},
				}},
			)
		}
		messages = append(messages, map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "text", "text": assistant},
		}})
	}
	finalBlock := map[string]any{"type": "text", "text": finalUser}
	if sc.CacheControl {
		finalBlock["cache_control"] = map[string]any{"type": "ephemeral"}
	}
	messages = append(messages, map[string]any{"role": "user", "content": []any{finalBlock}})

	body := map[string]any{
		"model":      model,
		"system":     []any{systemBlock},
		"messages":   messages,
		"max_tokens": sc.MaxTokens,
	}
	if sc.IncludeTools {
		body["tools"] = anthropicTools
	}
	if sc.Stream {
		body["stream"] = true
	}
	return json.Marshal(body)
}

func buildResponsesBody(sc Scenario, model, system, finalUser string, turns int) ([]byte, error) {
	var input []any
	for i := 0; i < turns; i++ {
		user, assistant := historyPair(sc, i)
		input = append(input,
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "input_text", "text": user},
			}},
			map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "output_text", "text": assistant},
			}},
		)
	}
	input = append(input, map[string]any{"role": "user", "content": []any{
		map[string]any{"type": "input_text", "text": finalUser},
	}})

	body := map[string]any{
		"model":             model,
		"instructions":      system,
		"input":             input,
		"max_output_tokens": sc.MaxTokens,
	}
	if sc.Stream {
		body["stream"] = true
	}
	return json.Marshal(body)
}
