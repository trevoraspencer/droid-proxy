package translate

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ResponsesToChatRequest translates the non-streaming OpenAI Responses request
// shape accepted by Droid into an OpenAI Chat Completions request.
func ResponsesToChatRequest(body []byte, upstreamModel string, extraArgs map[string]any) ([]byte, error) {
	var in map[string]any
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	if err := validateUnsupportedResponsesFields(in); err != nil {
		return nil, err
	}
	model := stringValue(in["model"])
	if strings.TrimSpace(upstreamModel) != "" {
		model = upstreamModel
	}
	if strings.TrimSpace(model) == "" {
		return nil, errors.New("request is missing required field: model")
	}

	out := map[string]any{"model": model}
	if b, ok := in["stream"].(bool); ok {
		out["stream"] = b
	}
	messages, err := responsesInputToChatMessages(in)
	if err != nil {
		return nil, err
	}
	out["messages"] = messages

	copyIfPresent(out, in, "temperature", "top_p", "stop")
	if v, ok := in["max_output_tokens"]; ok {
		out["max_tokens"] = v
	}
	if tools, ok := in["tools"]; ok {
		chatTools, err := responsesToolsToChatTools(tools)
		if err != nil {
			return nil, err
		}
		if len(chatTools) > 0 {
			out["tools"] = chatTools
		}
	}
	if choice, ok := in["tool_choice"]; ok {
		chatChoice, err := responsesToolChoiceToChatToolChoice(choice)
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

func responsesInputToChatMessages(in map[string]any) ([]any, error) {
	var messages []any
	if instructions := stringValue(in["instructions"]); instructions != "" {
		messages = append(messages, map[string]any{"role": "system", "content": instructions})
	}
	input, ok := in["input"]
	if !ok {
		return messages, nil
	}
	switch v := input.(type) {
	case string:
		messages = append(messages, map[string]any{"role": "user", "content": v})
	case []any:
		for _, raw := range v {
			msgs, err := responsesInputItemToChatMessages(raw)
			if err != nil {
				return nil, err
			}
			messages = append(messages, msgs...)
		}
	default:
		return nil, fmt.Errorf("unsupported Responses input type %T", input)
	}
	return messages, nil
}

func responsesInputItemToChatMessages(raw any) ([]any, error) {
	item, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("unsupported Responses input item %T", raw)
	}
	typ := stringValue(item["type"])
	switch typ {
	case "", "message":
		role := stringValue(item["role"])
		if role == "" {
			role = "user"
		}
		content, err := responsesContentToString(item["content"])
		if err != nil {
			return nil, err
		}
		return []any{map[string]any{"role": role, "content": content}}, nil
	case "function_call":
		callID := firstNonEmpty(stringValue(item["call_id"]), stringValue(item["id"]))
		if callID == "" {
			return nil, errors.New("function_call item is missing call_id")
		}
		name := stringValue(item["name"])
		if name == "" {
			return nil, errors.New("function_call item is missing name")
		}
		args := stringValue(item["arguments"])
		return []any{map[string]any{
			"role":    "assistant",
			"content": "",
			"tool_calls": []any{map[string]any{
				"id":   callID,
				"type": "function",
				"function": map[string]any{
					"name":      name,
					"arguments": args,
				},
			}},
		}}, nil
	case "function_call_output":
		callID := firstNonEmpty(stringValue(item["call_id"]), stringValue(item["id"]))
		if callID == "" {
			return nil, errors.New("function_call_output item is missing call_id")
		}
		return []any{map[string]any{
			"role":         "tool",
			"tool_call_id": callID,
			"content":      outputValueToString(item["output"]),
		}}, nil
	default:
		return nil, fmt.Errorf("unsupported Responses input item type %q", typ)
	}
}

func validateUnsupportedResponsesFields(in map[string]any) error {
	for _, k := range []string{"previous_response_id", "parallel_tool_calls", "include", "store"} {
		if _, ok := in[k]; ok {
			return fmt.Errorf("unsupported Responses field %q", k)
		}
	}
	if text, ok := in["text"].(map[string]any); ok {
		if _, ok := text["format"]; ok {
			return errors.New("unsupported Responses field \"text.format\"")
		}
	}
	return nil
}

func responsesContentToString(raw any) (string, error) {
	switch v := raw.(type) {
	case nil:
		return "", nil
	case string:
		return v, nil
	case []any:
		var b strings.Builder
		for _, blockRaw := range v {
			block, ok := blockRaw.(map[string]any)
			if !ok {
				return "", fmt.Errorf("unsupported Responses content block %T", blockRaw)
			}
			typ := stringValue(block["type"])
			switch typ {
			case "input_text", "output_text", "text", "":
				b.WriteString(stringValue(block["text"]))
			case "input_image", "input_file":
				return "", fmt.Errorf("unsupported Responses content block type %q", typ)
			default:
				return "", fmt.Errorf("unsupported Responses content block type %q", typ)
			}
		}
		return b.String(), nil
	default:
		return "", fmt.Errorf("unsupported Responses content type %T", raw)
	}
}

func responsesToolsToChatTools(raw any) ([]any, error) {
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
		if stringValue(t["type"]) != "function" {
			return nil, fmt.Errorf("unsupported Responses tool type %q", stringValue(t["type"]))
		}
		fn := map[string]any{}
		if nested, ok := t["function"].(map[string]any); ok {
			for k, v := range nested {
				fn[k] = v
			}
		} else {
			for _, k := range []string{"name", "description", "parameters", "strict"} {
				if v, ok := t[k]; ok {
					fn[k] = v
				}
			}
		}
		if strings.TrimSpace(stringValue(fn["name"])) == "" {
			return nil, errors.New("function tool is missing name")
		}
		out = append(out, map[string]any{"type": "function", "function": fn})
	}
	return out, nil
}

func responsesToolChoiceToChatToolChoice(raw any) (any, error) {
	switch v := raw.(type) {
	case string:
		switch v {
		case "auto", "none", "required":
			return v, nil
		default:
			return nil, fmt.Errorf("unsupported tool_choice %q", v)
		}
	case map[string]any:
		if stringValue(v["type"]) != "function" {
			return nil, fmt.Errorf("unsupported tool_choice type %q", stringValue(v["type"]))
		}
		name := stringValue(v["name"])
		if name == "" {
			return nil, errors.New("function tool_choice is missing name")
		}
		return map[string]any{"type": "function", "function": map[string]any{"name": name}}, nil
	default:
		return nil, fmt.Errorf("unsupported tool_choice type %T", raw)
	}
}

// ChatToResponsesResponse translates a non-streaming Chat Completions response
// into the Responses API response envelope.
func ChatToResponsesResponse(body []byte, model string) ([]byte, error) {
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

	output := []any{}
	if rawTools, ok := message["tool_calls"].([]any); ok && len(rawTools) > 0 {
		for _, rawTool := range rawTools {
			item, err := chatToolCallToResponseItem(rawTool)
			if err != nil {
				return nil, err
			}
			output = append(output, item)
		}
	} else {
		output = append(output, map[string]any{
			"id":     "msg_0",
			"type":   "message",
			"status": "completed",
			"role":   "assistant",
			"content": []any{map[string]any{
				"type":        "output_text",
				"text":        stringValue(message["content"]),
				"annotations": []any{},
			}},
		})
	}

	status := "completed"
	incompleteReason := ""
	switch stringValue(choice["finish_reason"]) {
	case "length":
		status = "incomplete"
		incompleteReason = "max_output_tokens"
	case "content_filter":
		status = "incomplete"
		incompleteReason = "content_filter"
	}
	resp := map[string]any{
		"id":         firstNonEmpty(stringValue(in["id"]), fmt.Sprintf("resp_%d", time.Now().UnixNano())),
		"object":     "response",
		"created_at": numericOrNow(in["created"]),
		"status":     status,
		"model":      firstNonEmpty(stringValue(in["model"]), model),
		"output":     output,
	}
	if usage, ok := chatUsageToResponsesUsage(in["usage"]); ok {
		resp["usage"] = usage
	}
	if incompleteReason != "" {
		resp["incomplete_details"] = map[string]any{"reason": incompleteReason}
	}
	return json.Marshal(resp)
}

func chatToolCallToResponseItem(raw any) (map[string]any, error) {
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
	var js any
	if err := json.Unmarshal([]byte(args), &js); err != nil {
		return nil, fmt.Errorf("Chat tool_call arguments are not valid JSON: %w", err)
	}
	return map[string]any{
		"type":      "function_call",
		"id":        callID,
		"call_id":   callID,
		"name":      name,
		"arguments": args,
		"status":    "completed",
	}, nil
}

func chatUsageToResponsesUsage(raw any) (map[string]any, bool) {
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
	if v, ok := u["total_tokens"]; ok {
		out["total_tokens"] = v
	}
	return out, true
}

func copyIfPresent(dst, src map[string]any, keys ...string) {
	for _, k := range keys {
		if v, ok := src[k]; ok {
			dst[k] = v
		}
	}
}

func stringValue(v any) string {
	s, _ := v.(string)
	return s
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func outputValueToString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	if v == nil {
		return ""
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

func numericOrNow(v any) any {
	switch v.(type) {
	case float64, int, int64, json.Number:
		return v
	default:
		return time.Now().Unix()
	}
}
