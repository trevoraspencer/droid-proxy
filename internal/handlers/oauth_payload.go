package handlers

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"github.com/trevoraspencer/droid-proxy/internal/config"
)

const (
	codexDefaultInstructions     = "You are Codex, a concise coding assistant."
	xaiEncryptedReasoningInclude = "reasoning.encrypted_content"
)

func prepareOAuthResponsesPayload(body []byte, m *config.Model, stream bool, downstream http.Header) []byte {
	out := applyUpstreamPayloadOverrides(body, m)
	if next, err := sjson.SetBytes(out, "stream", stream); err == nil {
		out = next
	}
	if m.OAuthProvider == config.OAuthProviderCodex {
		out = prepareCodexResponsesPayload(out)
	}
	for _, field := range []string{"previous_response_id", "prompt_cache_retention", "safety_identifier", "stream_options"} {
		if next, err := sjson.DeleteBytes(out, field); err == nil {
			out = next
		}
	}
	if m.OAuthProvider == config.OAuthProviderXAI {
		out = prepareXAIResponsesPayload(out, m, downstream)
	}
	return out
}

func prepareCodexResponsesPayload(body []byte) []byte {
	out := body
	if strings.EqualFold(strings.TrimSpace(gjson.GetBytes(out, "service_tier").String()), "fast") {
		if next, err := sjson.SetBytes(out, "service_tier", "priority"); err == nil {
			out = next
		} else if next, deleteErr := sjson.DeleteBytes(out, "service_tier"); deleteErr == nil {
			out = next
		}
	}
	if strings.TrimSpace(gjson.GetBytes(out, "instructions").String()) == "" {
		if next, err := sjson.SetBytes(out, "instructions", codexDefaultInstructions); err == nil {
			out = next
		}
	}
	if next, err := sjson.SetBytes(out, "store", false); err == nil {
		out = next
	}
	// The Codex OAuth endpoint currently rejects this public Responses field.
	// Factory may send it from custom-model settings, so drop it for Codex only.
	if next, err := sjson.DeleteBytes(out, "max_output_tokens"); err == nil {
		out = next
	}
	input := gjson.GetBytes(out, "input")
	if input.Type == gjson.String {
		normalized := []map[string]string{{
			"role":    "user",
			"content": input.String(),
		}}
		if next, err := sjson.SetBytes(out, "input", normalized); err == nil {
			out = next
		}
	}
	return out
}

func prepareXAIResponsesPayload(body []byte, m *config.Model, downstream http.Header) []byte {
	out := body
	reasoningPresent := xaiReasoningPresentBytes(out)
	if next, err := sjson.DeleteBytes(out, "service_tier"); err == nil {
		out = next
	}
	if m == nil || m.ResolvedCapabilities().FactoryReasoning != config.FactoryReasoningPassthrough {
		// Grok Build rejects Factory's top-level reasoning effort parameter,
		// while encrypted reasoning input items still need include handling.
		if next, err := sjson.DeleteBytes(out, "reasoning"); err == nil {
			out = next
		}
	}
	if strings.TrimSpace(gjson.GetBytes(out, "prompt_cache_key").String()) == "" {
		if sessionID := xaiSessionID(downstream); sessionID != "" {
			if next, err := sjson.SetBytes(out, "prompt_cache_key", sessionID); err == nil {
				out = next
			}
		}
	}
	if tools := gjson.GetBytes(out, "tools"); tools.IsArray() {
		out = setXAITools(out, tools.Raw)
	}
	if reasoningPresent {
		out = setXAIInclude(out)
	}
	return out
}

// setXAITools normalizes the tools array, decoding only that subtree with
// UseNumber so numeric schema bounds keep their original precision.
func setXAITools(body []byte, rawTools string) []byte {
	dec := json.NewDecoder(strings.NewReader(rawTools))
	dec.UseNumber()
	var tools []any
	if err := dec.Decode(&tools); err != nil {
		return body
	}
	normalized := normalizeXAITools(tools)
	raw, err := json.Marshal(normalized)
	if err != nil {
		return body
	}
	if next, err := sjson.SetRawBytes(body, "tools", raw); err == nil {
		return next
	}
	return body
}

// setXAIInclude appends the encrypted-reasoning include marker, preserving any
// existing include entries.
func setXAIInclude(body []byte) []byte {
	var current any
	if inc := gjson.GetBytes(body, "include"); inc.Exists() {
		current = inc.Value()
	}
	updated := includeXAIEncryptedReasoning(current)
	if next, err := sjson.SetBytes(body, "include", updated); err == nil {
		return next
	}
	return body
}

func xaiSessionID(h http.Header) string {
	for _, v := range []string{
		h.Get("X-Session-ID"),
		h.Get("Session_id"),
		h.Get("X-Client-Request-Id"),
	} {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func normalizeXAITools(tools []any) []any {
	out := make([]any, 0, len(tools))
	for _, rawTool := range tools {
		tool, ok := rawTool.(map[string]any)
		if !ok {
			continue
		}
		toolType := strings.ToLower(strings.TrimSpace(stringValue(tool["type"])))
		if toolType == "namespace" || toolType == "namespace_tool" {
			if nested, ok := tool["tools"].([]any); ok {
				out = append(out, normalizeXAITools(nested)...)
			}
			continue
		}
		normalized, keep := normalizeXAITool(tool)
		if keep {
			out = append(out, normalized)
		}
	}
	return out
}

func normalizeXAITool(tool map[string]any) (map[string]any, bool) {
	toolType := strings.ToLower(strings.TrimSpace(stringValue(tool["type"])))
	name := xaiToolName(tool)
	if unsupportedXAITool(toolType, name) {
		return nil, false
	}
	if strings.HasPrefix(toolType, "web_search") || toolType == "web_search" {
		stripXAIWebSearchFields(tool)
		return sanitizeXAIToolFields(tool), true
	}
	if toolType == "custom" {
		tool["type"] = "function"
		toolType = "function"
		if _, ok := tool["parameters"]; !ok {
			if schema, ok := tool["input_schema"]; ok {
				tool["parameters"] = schema
			}
		}
		delete(tool, "input_schema")
	}
	if toolType == "function" {
		ensureXAIFunctionParameters(tool)
	}
	return sanitizeXAIToolFields(tool), true
}

func xaiToolName(tool map[string]any) string {
	if name := strings.TrimSpace(stringValue(tool["name"])); name != "" {
		return strings.ToLower(name)
	}
	if function, ok := tool["function"].(map[string]any); ok {
		return strings.ToLower(strings.TrimSpace(stringValue(function["name"])))
	}
	return ""
}

func unsupportedXAITool(toolType, name string) bool {
	switch toolType {
	case "tool_search", "image_generation", "apply_patch":
		return true
	}
	switch name {
	case "tool_search", "image_generation", "apply_patch":
		return true
	}
	return strings.HasSuffix(name, ".apply_patch") || strings.HasSuffix(name, "/apply_patch")
}

func stripXAIWebSearchFields(tool map[string]any) {
	for _, field := range []string{
		"allowed_domains",
		"blocked_domains",
		"filters",
		"ranking_options",
		"search_context_size",
		"site_search",
		"user_location",
	} {
		delete(tool, field)
	}
}

func ensureXAIFunctionParameters(tool map[string]any) {
	if function, ok := tool["function"].(map[string]any); ok {
		if _, ok := function["parameters"].(map[string]any); !ok {
			function["parameters"] = map[string]any{}
		}
		function["parameters"] = sanitizeXAIToolSchema(function["parameters"])
		return
	}
	if _, ok := tool["parameters"].(map[string]any); !ok {
		tool["parameters"] = map[string]any{}
	}
	tool["parameters"] = sanitizeXAIToolSchema(tool["parameters"])
}

func sanitizeXAIToolFields(tool map[string]any) map[string]any {
	for key, value := range tool {
		switch key {
		case "parameters", "input_schema":
			tool[key] = sanitizeXAIToolSchema(value)
		case "function":
			if function, ok := value.(map[string]any); ok {
				for fk, fv := range function {
					if fk == "parameters" {
						function[fk] = sanitizeXAIToolSchema(fv)
					}
				}
			}
		}
	}
	return tool
}

// sanitizeXAIToolSchema strips JSON-Schema keywords xAI rejects (pattern,
// format, and enum values containing "/") from a tool schema. Stripping only
// applies where map keys are JSON-Schema keywords: maps whose keys are
// user-defined names (properties, patternProperties, $defs, definitions)
// recurse into each value as a schema instead, so a tool parameter that is
// itself named "format", "pattern", or "enum" is preserved.
func sanitizeXAIToolSchema(value any) any {
	switch v := value.(type) {
	case map[string]any:
		for key, child := range v {
			switch key {
			case "pattern", "format":
				delete(v, key)
			case "enum":
				filtered := sanitizeXAIEnum(child)
				if len(filtered) == 0 {
					delete(v, key)
				} else {
					v[key] = filtered
				}
			case "properties", "patternProperties", "$defs", "definitions":
				v[key] = sanitizeXAISchemaMap(child)
			default:
				v[key] = sanitizeXAIToolSchema(child)
			}
		}
		return v
	case []any:
		for i, child := range v {
			v[i] = sanitizeXAIToolSchema(child)
		}
		return v
	default:
		return value
	}
}

// sanitizeXAISchemaMap sanitizes a map whose keys are user-defined names
// (property names, definition names) and whose values are schemas. The keys
// themselves are never treated as JSON-Schema keywords.
func sanitizeXAISchemaMap(value any) any {
	m, ok := value.(map[string]any)
	if !ok {
		return sanitizeXAIToolSchema(value)
	}
	for name, child := range m {
		m[name] = sanitizeXAIToolSchema(child)
	}
	return m
}

func sanitizeXAIEnum(value any) []any {
	values, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]any, 0, len(values))
	for _, v := range values {
		if s, ok := v.(string); ok && strings.Contains(s, "/") {
			continue
		}
		out = append(out, v)
	}
	return out
}

func xaiReasoningPresentBytes(body []byte) bool {
	if gjson.GetBytes(body, "reasoning").Exists() {
		return true
	}
	input := gjson.GetBytes(body, "input")
	if !input.Exists() {
		return false
	}
	return xaiValueContainsReasoning(input.Value())
}

func xaiValueContainsReasoning(value any) bool {
	switch v := value.(type) {
	case map[string]any:
		if strings.EqualFold(stringValue(v["type"]), "reasoning") {
			return true
		}
		if strings.TrimSpace(stringValue(v["encrypted_content"])) != "" {
			return true
		}
		for _, child := range v {
			if xaiValueContainsReasoning(child) {
				return true
			}
		}
	case []any:
		for _, child := range v {
			if xaiValueContainsReasoning(child) {
				return true
			}
		}
	}
	return false
}

func includeXAIEncryptedReasoning(value any) any {
	appendIfMissing := func(values []any) []any {
		for _, v := range values {
			if stringValue(v) == xaiEncryptedReasoningInclude {
				return values
			}
		}
		return append(values, xaiEncryptedReasoningInclude)
	}
	switch v := value.(type) {
	case nil:
		return []string{xaiEncryptedReasoningInclude}
	case []any:
		return appendIfMissing(v)
	case []string:
		values := make([]any, 0, len(v)+1)
		for _, s := range v {
			values = append(values, s)
		}
		return appendIfMissing(values)
	case string:
		values := []any{v}
		return appendIfMissing(values)
	default:
		return []string{xaiEncryptedReasoningInclude}
	}
}

func stringValue(value any) string {
	if s, ok := value.(string); ok {
		return s
	}
	return ""
}
