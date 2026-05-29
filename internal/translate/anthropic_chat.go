package translate

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// AnthropicToChatRequest translates a non-streaming Anthropic Messages request
// into an OpenAI Chat Completions request.
func AnthropicToChatRequest(body []byte, upstreamModel string, extraArgs map[string]any) ([]byte, error) {
	var in map[string]any
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	if err := validateUnsupportedAnthropicFields(in); err != nil {
		return nil, err
	}
	model := stringValue(in["model"])
	if strings.TrimSpace(upstreamModel) != "" {
		model = upstreamModel
	}
	if strings.TrimSpace(model) == "" {
		return nil, errors.New("request is missing required field: model")
	}

	messages, err := anthropicMessagesToChatMessages(in)
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"model":    model,
		"messages": messages,
	}
	if b, ok := in["stream"].(bool); ok {
		out["stream"] = b
	}
	copyIfPresent(out, in, "temperature", "top_p")
	if v, ok := in["max_tokens"]; ok {
		out["max_tokens"] = v
	}
	if v, ok := in["stop_sequences"]; ok {
		out["stop"] = v
	}
	if tools, ok := in["tools"]; ok {
		chatTools, err := anthropicToolsToChatTools(tools)
		if err != nil {
			return nil, err
		}
		if len(chatTools) > 0 {
			out["tools"] = chatTools
		}
	}
	if choice, ok := in["tool_choice"]; ok {
		chatChoice, err := anthropicToolChoiceToChatToolChoice(choice)
		if err != nil {
			return nil, err
		}
		out["tool_choice"] = chatChoice
	}
	for k, v := range extraArgs {
		out[k] = v
	}
	return json.Marshal(out)
}

func anthropicMessagesToChatMessages(in map[string]any) ([]any, error) {
	var out []any
	if sys, ok := in["system"]; ok {
		text, err := anthropicSystemToText(sys)
		if err != nil {
			return nil, err
		}
		if text != "" {
			out = append(out, map[string]any{"role": "system", "content": text})
		}
	}
	rawMessages, ok := in["messages"].([]any)
	if !ok {
		return out, nil
	}
	for _, raw := range rawMessages {
		msgs, err := anthropicMessageToChatMessages(raw)
		if err != nil {
			return nil, err
		}
		out = append(out, msgs...)
	}
	return out, nil
}

func anthropicMessageToChatMessages(raw any) ([]any, error) {
	m, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("Anthropic message must be an object, got %T", raw)
	}
	role := stringValue(m["role"])
	if role == "" {
		return nil, errors.New("Anthropic message is missing role")
	}
	switch role {
	case "user":
		return anthropicUserContentToChatMessages(m["content"])
	case "assistant":
		return anthropicAssistantContentToChatMessages(m["content"])
	default:
		return nil, fmt.Errorf("unsupported Anthropic message role %q", role)
	}
}

func anthropicSystemToText(raw any) (string, error) {
	switch v := raw.(type) {
	case nil:
		return "", nil
	case string:
		return v, nil
	case []any:
		return anthropicTextBlocksToString(v)
	default:
		return "", fmt.Errorf("unsupported Anthropic system type %T", raw)
	}
}

func validateUnsupportedAnthropicFields(in map[string]any) error {
	if _, ok := in["thinking"]; ok {
		return errors.New("unsupported Anthropic field \"thinking\"")
	}
	return nil
}

func anthropicUserContentToChatMessages(raw any) ([]any, error) {
	switch v := raw.(type) {
	case nil:
		return []any{map[string]any{"role": "user", "content": ""}}, nil
	case string:
		return []any{map[string]any{"role": "user", "content": v}}, nil
	case []any:
		var out []any
		var text strings.Builder
		flushText := func() {
			if text.Len() > 0 {
				out = append(out, map[string]any{"role": "user", "content": text.String()})
				text.Reset()
			}
		}
		for _, rawBlock := range v {
			block, ok := rawBlock.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("Anthropic content block must be an object, got %T", rawBlock)
			}
			switch typ := stringValue(block["type"]); typ {
			case "text", "":
				if _, ok := block["cache_control"]; ok {
					return nil, errors.New("unsupported Anthropic cache_control on content block")
				}
				if text.Len() > 0 {
					text.WriteByte('\n')
				}
				text.WriteString(stringValue(block["text"]))
			case "tool_result":
				flushText()
				callID := stringValue(block["tool_use_id"])
				if callID == "" {
					return nil, errors.New("tool_result block is missing tool_use_id")
				}
				content, err := anthropicToolResultContentToString(block["content"])
				if err != nil {
					return nil, err
				}
				out = append(out, map[string]any{
					"role":         "tool",
					"tool_call_id": callID,
					"content":      content,
				})
			default:
				return nil, fmt.Errorf("unsupported Anthropic user content block type %q", typ)
			}
		}
		flushText()
		if len(out) == 0 {
			out = append(out, map[string]any{"role": "user", "content": ""})
		}
		return out, nil
	default:
		return nil, fmt.Errorf("unsupported Anthropic user content type %T", raw)
	}
}

func anthropicAssistantContentToChatMessages(raw any) ([]any, error) {
	switch v := raw.(type) {
	case nil:
		return []any{map[string]any{"role": "assistant", "content": ""}}, nil
	case string:
		return []any{map[string]any{"role": "assistant", "content": v}}, nil
	case []any:
		var text strings.Builder
		var toolCalls []any
		for _, rawBlock := range v {
			block, ok := rawBlock.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("Anthropic content block must be an object, got %T", rawBlock)
			}
			switch typ := stringValue(block["type"]); typ {
			case "text", "":
				if _, ok := block["cache_control"]; ok {
					return nil, errors.New("unsupported Anthropic cache_control on content block")
				}
				if text.Len() > 0 {
					text.WriteByte('\n')
				}
				text.WriteString(stringValue(block["text"]))
			case "tool_use":
				callID := stringValue(block["id"])
				if callID == "" {
					return nil, errors.New("tool_use block is missing id")
				}
				name := stringValue(block["name"])
				if name == "" {
					return nil, errors.New("tool_use block is missing name")
				}
				args, err := json.Marshal(block["input"])
				if err != nil {
					return nil, fmt.Errorf("tool_use input is not JSON-serializable: %w", err)
				}
				toolCalls = append(toolCalls, map[string]any{
					"id":   callID,
					"type": "function",
					"function": map[string]any{
						"name":      name,
						"arguments": string(args),
					},
				})
			default:
				return nil, fmt.Errorf("unsupported Anthropic assistant content block type %q", typ)
			}
		}
		msg := map[string]any{"role": "assistant", "content": text.String()}
		if len(toolCalls) > 0 {
			msg["tool_calls"] = toolCalls
		}
		return []any{msg}, nil
	default:
		return nil, fmt.Errorf("unsupported Anthropic assistant content type %T", raw)
	}
}

func anthropicTextBlocksToString(blocks []any) (string, error) {
	var b strings.Builder
	for _, rawBlock := range blocks {
		block, ok := rawBlock.(map[string]any)
		if !ok {
			return "", fmt.Errorf("Anthropic text block must be an object, got %T", rawBlock)
		}
		if typ := stringValue(block["type"]); typ != "" && typ != "text" {
			return "", fmt.Errorf("unsupported Anthropic text block type %q", typ)
		}
		if _, ok := block["cache_control"]; ok {
			return "", errors.New("unsupported Anthropic cache_control on system block")
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(stringValue(block["text"]))
	}
	return b.String(), nil
}

func anthropicToolResultContentToString(raw any) (string, error) {
	switch v := raw.(type) {
	case nil:
		return "", nil
	case string:
		return v, nil
	case []any:
		return anthropicTextBlocksToString(v)
	default:
		return outputValueToString(raw), nil
	}
}

func anthropicToolsToChatTools(raw any) ([]any, error) {
	arr, ok := raw.([]any)
	if !ok {
		return nil, errors.New("tools must be an array")
	}
	out := make([]any, 0, len(arr))
	for _, rt := range arr {
		t, ok := rt.(map[string]any)
		if !ok {
			return nil, errors.New("tool entries must be objects")
		}
		name := stringValue(t["name"])
		if strings.TrimSpace(name) == "" {
			return nil, errors.New("Anthropic tool is missing name")
		}
		fn := map[string]any{
			"name":       name,
			"parameters": t["input_schema"],
		}
		if desc := stringValue(t["description"]); desc != "" {
			fn["description"] = desc
		}
		out = append(out, map[string]any{"type": "function", "function": fn})
	}
	return out, nil
}

func anthropicToolChoiceToChatToolChoice(raw any) (any, error) {
	choice, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("unsupported Anthropic tool_choice type %T", raw)
	}
	switch typ := stringValue(choice["type"]); typ {
	case "auto":
		return "auto", nil
	case "any":
		return "required", nil
	case "tool":
		name := stringValue(choice["name"])
		if name == "" {
			return nil, errors.New("tool_choice tool is missing name")
		}
		return map[string]any{"type": "function", "function": map[string]any{"name": name}}, nil
	default:
		return nil, fmt.Errorf("unsupported Anthropic tool_choice type %q", typ)
	}
}

// ChatToAnthropicResponse translates a non-streaming Chat Completions response
// into an Anthropic Messages response envelope.
func ChatToAnthropicResponse(body []byte, model string) ([]byte, error) {
	var in map[string]any
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, fmt.Errorf("invalid Chat response JSON: %w", err)
	}
	choices, ok := in["choices"].([]any)
	if !ok || len(choices) == 0 {
		return nil, errors.New("Chat response is missing choices")
	}
	if len(choices) > 1 {
		return nil, errors.New("Chat response contains multiple choices, which this translator does not merge")
	}
	choice, ok := choices[0].(map[string]any)
	if !ok {
		return nil, errors.New("Chat choice is not an object")
	}
	message, ok := choice["message"].(map[string]any)
	if !ok {
		return nil, errors.New("Chat choice is missing message")
	}

	content := []any{}
	stopReason := chatFinishReasonToAnthropicStopReason(stringValue(choice["finish_reason"]))
	if rawTools, ok := message["tool_calls"].([]any); ok && len(rawTools) > 0 {
		for _, rawTool := range rawTools {
			block, err := chatToolCallToAnthropicToolUse(rawTool)
			if err != nil {
				return nil, err
			}
			content = append(content, block)
		}
		stopReason = "tool_use"
	} else {
		content = append(content, map[string]any{"type": "text", "text": stringValue(message["content"])})
	}

	resp := map[string]any{
		"id":          firstNonEmpty(stringValue(in["id"]), fmt.Sprintf("msg_%d", time.Now().UnixNano())),
		"type":        "message",
		"role":        "assistant",
		"model":       firstNonEmpty(stringValue(in["model"]), model),
		"content":     content,
		"stop_reason": stopReason,
	}
	if usage, ok := chatUsageToAnthropicUsage(in["usage"]); ok {
		resp["usage"] = usage
	}
	return json.Marshal(resp)
}

func chatToolCallToAnthropicToolUse(raw any) (map[string]any, error) {
	tc, ok := raw.(map[string]any)
	if !ok {
		return nil, errors.New("Chat tool_call is not an object")
	}
	if stringValue(tc["type"]) != "" && stringValue(tc["type"]) != "function" {
		return nil, fmt.Errorf("unsupported Chat tool_call type %q", stringValue(tc["type"]))
	}
	fn, ok := tc["function"].(map[string]any)
	if !ok {
		return nil, errors.New("Chat tool_call is missing function")
	}
	callID := stringValue(tc["id"])
	if callID == "" {
		return nil, errors.New("Chat tool_call is missing id")
	}
	name := stringValue(fn["name"])
	if name == "" {
		return nil, errors.New("Chat tool_call function is missing name")
	}
	args := stringValue(fn["arguments"])
	if args == "" {
		args = "{}"
	}
	var input any
	if err := json.Unmarshal([]byte(args), &input); err != nil {
		return nil, fmt.Errorf("Chat tool_call arguments are not valid JSON: %w", err)
	}
	return map[string]any{
		"type":  "tool_use",
		"id":    callID,
		"name":  name,
		"input": input,
	}, nil
}

func chatFinishReasonToAnthropicStopReason(reason string) string {
	switch reason {
	case "length":
		return "max_tokens"
	case "tool_calls":
		return "tool_use"
	case "content_filter":
		return "stop_sequence"
	case "stop", "":
		return "end_turn"
	default:
		return "end_turn"
	}
}

func chatUsageToAnthropicUsage(raw any) (map[string]any, bool) {
	u, ok := raw.(map[string]any)
	if !ok {
		return nil, false
	}
	out := map[string]any{}
	if v, ok := u["prompt_tokens"]; ok {
		out["input_tokens"] = v
	}
	if v, ok := u["completion_tokens"]; ok {
		out["output_tokens"] = v
	}
	return out, true
}
