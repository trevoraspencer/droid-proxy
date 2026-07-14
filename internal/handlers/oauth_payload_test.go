package handlers

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"

	"github.com/tidwall/gjson"

	"github.com/trevoraspencer/droid-proxy/internal/config"
	"github.com/trevoraspencer/droid-proxy/internal/factory"
)

func TestReasoningCapabilityDrivesFactorySettingsAndResponses(t *testing.T) {
	tests := []struct {
		name          string
		model         *config.Model
		wantEffort    config.FactoryReasoningEffort
		wantReasoning bool
	}{
		{
			name: "GPT-5.6 advertises max and preserves reasoning",
			model: &config.Model{
				Alias: "gpt-5.6", FactoryProvider: config.FactoryProviderOpenAI,
				UpstreamProtocol: config.UpstreamCodexResponses, OAuthProvider: config.OAuthProviderCodex,
				Capabilities: config.Capabilities{FactoryReasoning: config.FactoryReasoningPassthrough, FactoryReasoningEffort: config.FactoryReasoningEffortMax},
			},
			wantEffort: config.FactoryReasoningEffortMax, wantReasoning: true,
		},
		{
			name: "Grok 4.5 advertises high and preserves reasoning",
			model: &config.Model{
				Alias: "grok-4.5", FactoryProvider: config.FactoryProviderOpenAI,
				UpstreamProtocol: config.UpstreamXAIResponses, OAuthProvider: config.OAuthProviderXAI,
				Capabilities: config.Capabilities{FactoryReasoning: config.FactoryReasoningPassthrough, FactoryReasoningEffort: config.FactoryReasoningEffortHigh},
			},
			wantEffort: config.FactoryReasoningEffortHigh, wantReasoning: true,
		},
		{
			name: "Grok Build advertises nothing and drops reasoning",
			model: &config.Model{
				Alias: "grok-build-0.1", FactoryProvider: config.FactoryProviderOpenAI,
				UpstreamProtocol: config.UpstreamXAIResponses, OAuthProvider: config.OAuthProviderXAI,
				Capabilities: config.Capabilities{FactoryReasoning: config.FactoryReasoningDrop},
			},
		},
		{
			name: "Composer advertises nothing and drops reasoning",
			model: &config.Model{
				Alias: "grok-composer-2.5-fast", FactoryProvider: config.FactoryProviderOpenAI,
				UpstreamProtocol: config.UpstreamXAIResponses, OAuthProvider: config.OAuthProviderXAI,
				Capabilities: config.Capabilities{FactoryReasoning: config.FactoryReasoningDrop},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entry := factory.EntryFromModel(tt.model, "http://127.0.0.1:8787", "x")
			if entry.ReasoningEffort != tt.wantEffort {
				t.Fatalf("settings reasoningEffort = %q, want %q", entry.ReasoningEffort, tt.wantEffort)
			}
			body := prepareOAuthResponsesPayload([]byte(`{"model":"alias","input":"hi","reasoning":{"effort":"high"}}`), tt.model, false, http.Header{})
			if got := gjson.GetBytes(body, "reasoning").Exists(); got != tt.wantReasoning {
				t.Fatalf("forwarded reasoning exists = %v, want %v: %s", got, tt.wantReasoning, body)
			}
		})
	}
}

func TestPrepareOAuthResponsesPayload_XAIPrivateEndpointForcesUpstreamStreaming(t *testing.T) {
	cliModel := &config.Model{
		OAuthProvider: config.OAuthProviderXAI,
		BaseURL:       "https://cli-chat-proxy.grok.com/v1",
		UpstreamModel: "grok-4.5",
	}
	got := prepareOAuthResponsesPayload([]byte(`{"model":"grok-4.5","input":"hi","stream":false}`), cliModel, false, http.Header{})
	if !gjson.GetBytes(got, "stream").Bool() {
		t.Fatalf("CLI proxy upstream stream was not forced true: %s", got)
	}

	publicModel := &config.Model{
		OAuthProvider: config.OAuthProviderXAI,
		BaseURL:       "https://api.x.ai/v1",
		UpstreamModel: "grok-4.3",
	}
	got = prepareOAuthResponsesPayload([]byte(`{"model":"grok-4.3","input":"hi","stream":false}`), publicModel, false, http.Header{})
	if gjson.GetBytes(got, "stream").Bool() {
		t.Fatalf("public xAI upstream stream unexpectedly changed: %s", got)
	}
}

// TestSanitizeXAIToolSchema_KeywordPositionsOnly pins that the xAI schema
// sanitizer strips unsupported JSON-Schema keywords only at keyword positions.
// Tool parameters that are themselves *named* "format", "pattern", or "enum"
// (keys inside properties/patternProperties/$defs/definitions) must survive.
func TestSanitizeXAIToolSchema_KeywordPositionsOnly(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "unsupported keywords stripped at schema level",
			in:   `{"type":"string","format":"date-time","pattern":"^a+$"}`,
			want: `{"type":"string"}`,
		},
		{
			name: "property named format survives while its format keyword is stripped",
			in:   `{"type":"object","properties":{"format":{"type":"string","format":"uri"}}}`,
			want: `{"type":"object","properties":{"format":{"type":"string"}}}`,
		},
		{
			name: "property named pattern survives while its pattern keyword is stripped",
			in:   `{"type":"object","properties":{"pattern":{"type":"string","pattern":"^x$"}}}`,
			want: `{"type":"object","properties":{"pattern":{"type":"string"}}}`,
		},
		{
			name: "property named enum survives",
			in:   `{"type":"object","properties":{"enum":{"type":"string"}}}`,
			want: `{"type":"object","properties":{"enum":{"type":"string"}}}`,
		},
		{
			name: "enum keyword values containing slash are filtered",
			in:   `{"type":"string","enum":["a/b","c"]}`,
			want: `{"type":"string","enum":["c"]}`,
		},
		{
			name: "enum keyword removed when every value contains slash",
			in:   `{"type":"string","enum":["a/b","c/d"]}`,
			want: `{"type":"string"}`,
		},
		{
			name: "keywords nested under items and properties still stripped",
			in:   `{"type":"array","items":{"type":"object","properties":{"when":{"type":"string","format":"date"}}}}`,
			want: `{"type":"array","items":{"type":"object","properties":{"when":{"type":"string"}}}}`,
		},
		{
			name: "defs and definitions keys are names not keywords",
			in:   `{"$defs":{"format":{"type":"string","format":"uri"}},"definitions":{"pattern":{"type":"string"}}}`,
			want: `{"$defs":{"format":{"type":"string"}},"definitions":{"pattern":{"type":"string"}}}`,
		},
		{
			name: "patternProperties keys survive while value schemas are sanitized",
			in:   `{"type":"object","patternProperties":{"^f":{"type":"string","format":"uuid"}}}`,
			want: `{"type":"object","patternProperties":{"^f":{"type":"string"}}}`,
		},
		{
			name: "keywords inside anyOf branches still stripped",
			in:   `{"anyOf":[{"type":"string","format":"uri"},{"type":"integer"}]}`,
			want: `{"anyOf":[{"type":"string"},{"type":"integer"}]}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var in, want map[string]any
			if err := json.Unmarshal([]byte(tc.in), &in); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(tc.want), &want); err != nil {
				t.Fatal(err)
			}
			got := sanitizeXAIToolSchema(in)
			if !reflect.DeepEqual(got, any(want)) {
				t.Errorf("sanitizeXAIToolSchema mismatch:\n got=%#v\nwant=%#v", got, want)
			}
		})
	}
}

// TestSanitizeXAIToolFields_PreservesPropertyNamedFormat covers the full tool
// wrapper path: a function parameter literally named "format" must reach the
// model, while unsupported keyword content inside its schema is sanitized.
func TestSanitizeXAIToolFields_PreservesPropertyNamedFormat(t *testing.T) {
	tool := map[string]any{
		"type": "function",
		"function": map[string]any{
			"name": "render",
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"format": map[string]any{"type": "string", "enum": []any{"json", "text/plain"}},
				},
				"required": []any{"format"},
			},
		},
	}
	got := sanitizeXAIToolFields(tool)
	params, ok := got["function"].(map[string]any)["parameters"].(map[string]any)
	if !ok {
		t.Fatalf("parameters missing after sanitize: %#v", got)
	}
	props, ok := params["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties missing after sanitize: %#v", params)
	}
	formatSchema, ok := props["format"].(map[string]any)
	if !ok {
		t.Fatalf("property named format was removed: %#v", props)
	}
	if want := []any{"json"}; !reflect.DeepEqual(formatSchema["enum"], want) {
		t.Errorf("enum = %#v, want %#v (slash values filtered, rest kept)", formatSchema["enum"], want)
	}
	if !reflect.DeepEqual(params["required"], []any{"format"}) {
		t.Errorf("required list altered: %#v", params["required"])
	}
}

func TestStripReasoningInputItems(t *testing.T) {
	payload := []byte(`{
		"model": "grok-4.5",
		"input": [
			{"role": "user", "content": [{"type": "input_text", "text": "hi"}]},
			{"id": "rs_openai_minted", "type": "reasoning", "encrypted_content": "opaque-openai-blob", "summary": []},
			{"type": "function_call", "call_id": "call_1", "name": "LS", "arguments": "{}"},
			{"type": "function_call_output", "call_id": "call_1", "output": "ok"},
			{"type": "message", "role": "assistant", "content": [{"type": "output_text", "text": "done"}]}
		],
		"stream": true
	}`)
	stripped, changed := stripReasoningInputItems(payload)
	if !changed {
		t.Fatal("expected reasoning items to be stripped")
	}
	input := gjson.GetBytes(stripped, "input")
	if !input.IsArray() || len(input.Array()) != 4 {
		t.Fatalf("input after strip = %s", input.Raw)
	}
	for _, item := range input.Array() {
		if item.Get("type").String() == "reasoning" {
			t.Fatalf("reasoning item survived strip: %s", item.Raw)
		}
	}
	if gjson.GetBytes(stripped, "input.1.type").String() != "function_call" {
		t.Fatalf("item order not preserved: %s", input.Raw)
	}

	// No-op when there is nothing to strip.
	same, changed := stripReasoningInputItems(stripped)
	if changed {
		t.Fatalf("second strip should be a no-op, got %s", same)
	}

	// Only-reasoning input degrades to an empty array, not null.
	onlyReasoning := []byte(`{"input":[{"type":"reasoning","encrypted_content":"blob"}]}`)
	strippedOnly, changed := stripReasoningInputItems(onlyReasoning)
	if !changed || gjson.GetBytes(strippedOnly, "input").Raw != "[]" {
		t.Fatalf("only-reasoning strip = %s changed=%v", strippedOnly, changed)
	}

	// String input (non-array) is untouched.
	str := []byte(`{"input":"plain prompt"}`)
	if _, changed := stripReasoningInputItems(str); changed {
		t.Fatal("string input should not be modified")
	}
}

func TestIsEncryptedReasoningRejection(t *testing.T) {
	xaiBody := []byte(`{"code":"invalid-argument","error":"Could not decrypt the provided encrypted_content. Ensure the value is the unmodified encrypted_content from a previous response."}`)
	if !isEncryptedReasoningRejection(400, xaiBody) {
		t.Fatal("live xAI decrypt rejection should be detected")
	}
	if isEncryptedReasoningRejection(500, xaiBody) {
		t.Fatal("5xx must not trigger the reasoning-strip retry")
	}
	if isEncryptedReasoningRejection(400, []byte(`{"error":"model overloaded"}`)) {
		t.Fatal("unrelated 400 must not trigger the reasoning-strip retry")
	}
}
