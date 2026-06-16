package reasoning

import (
	"encoding/json"
	"testing"
	"time"
)

func validKey() Key {
	return Key{
		Scope:       Scope{Provider: "deepseek", AuthHash: "abc", Model: "deepseek-chat", BaseURL: "https://api.deepseek.com", Session: "s1"},
		ToolCallIDs: "call_1",
	}
}

func TestCache_StoreLookup(t *testing.T) {
	c := NewCache(8, time.Minute)
	c.Store(validKey(), "reasoning-text")
	got, ok := c.Lookup(validKey())
	if !ok || got != "reasoning-text" {
		t.Fatalf("expected hit, got %q ok=%v", got, ok)
	}
}

func TestCache_InvalidKeyNoStore(t *testing.T) {
	c := NewCache(8, time.Minute)
	bad := Key{Scope: Scope{Provider: "deepseek"}, ToolCallIDs: "c"}
	c.Store(bad, "x")
	if c.Len() != 0 {
		t.Fatalf("expected zero entries, got %d", c.Len())
	}
}

func TestCache_KeyRequiresExplicitIsolationScope(t *testing.T) {
	c := NewCache(8, time.Minute)
	for name, mutate := range map[string]func(*Key){
		"provider": func(k *Key) { k.Provider = "" },
		"auth":     func(k *Key) { k.AuthHash = "" },
		"model":    func(k *Key) { k.Model = "" },
		"base_url": func(k *Key) { k.BaseURL = "" },
		"session":  func(k *Key) { k.Session = "" },
	} {
		t.Run(name, func(t *testing.T) {
			k := validKey()
			mutate(&k)
			c.Store(k, "x")
			if _, ok := c.Lookup(k); ok {
				t.Fatalf("invalid key should not be stored")
			}
		})
	}
	if c.Len() != 0 {
		t.Fatalf("invalid keys should not populate cache, got %d entries", c.Len())
	}
}

func TestCache_IsolatesByScopeFields(t *testing.T) {
	c := NewCache(8, time.Minute)
	base := validKey()
	c.Store(base, "reasoning-text")
	for name, mutate := range map[string]func(*Key){
		"session":       func(k *Key) { k.Session = "other-session" },
		"client_auth":   func(k *Key) { k.AuthHash = "other-auth-hash" },
		"model":         func(k *Key) { k.Model = "other-model" },
		"base_url":      func(k *Key) { k.BaseURL = "https://other.example.test" },
		"thinking_mode": func(k *Key) { k.ThinkingMode = `{"budget_tokens":1024}` },
	} {
		t.Run(name, func(t *testing.T) {
			k := base
			mutate(&k)
			if got, ok := c.Lookup(k); ok {
				t.Fatalf("expected isolated miss, got %q", got)
			}
		})
	}
}

func TestCache_TTLExpiry(t *testing.T) {
	c := NewCache(8, 10*time.Millisecond)
	c.Store(validKey(), "x")
	time.Sleep(30 * time.Millisecond)
	if _, ok := c.Lookup(validKey()); ok {
		t.Fatalf("expected miss after TTL")
	}
}

func TestCache_MaxEntriesEviction(t *testing.T) {
	c := NewCache(2, time.Minute)
	for i := 0; i < 5; i++ {
		k := validKey()
		k.Session = "s" + string(rune('0'+i))
		c.Store(k, "r")
	}
	if c.Len() != 2 {
		t.Fatalf("expected 2 entries after eviction, got %d", c.Len())
	}
}

func TestKeyForMessage_PrefersToolCallIDs(t *testing.T) {
	scope := Scope{Provider: "deepseek", AuthHash: "h", Model: "m", BaseURL: "https://api.deepseek.com", Session: "s"}
	msg := map[string]any{
		"role": "assistant",
		"tool_calls": []any{
			map[string]any{"id": "call_b"},
			map[string]any{"id": "call_a"},
		},
	}
	k := KeyForMessage(scope, msg)
	if k.ToolCallIDs == "" {
		t.Fatal("expected non-empty tool call ids")
	}
	if k.TurnHash != "" {
		t.Errorf("turn hash should be empty when tool_calls present")
	}
}

func TestKeyForMessage_FallsBackToTurnHash(t *testing.T) {
	scope := Scope{Provider: "deepseek", AuthHash: "h", Model: "m", BaseURL: "https://api.deepseek.com", Session: "s"}
	msg := map[string]any{"role": "assistant", "content": "hello"}
	k := KeyForMessage(scope, msg)
	if k.ToolCallIDs != "" {
		t.Errorf("expected empty tool call ids")
	}
	if k.TurnHash == "" {
		t.Errorf("expected turn hash fallback")
	}
}

func TestCaptureNonStream_StoresReasoningWithToolCalls(t *testing.T) {
	c := NewCache(8, time.Minute)
	scope := Scope{Provider: "deepseek", AuthHash: "h", Model: "deepseek-chat", BaseURL: "https://api.deepseek.com", Session: "s"}
	body := []byte(`{"choices":[{"index":0,"message":{"role":"assistant","content":"","reasoning_content":"I should call the tool","tool_calls":[{"id":"call_x","type":"function","function":{"name":"f","arguments":"{}"}}]}}]}`)
	CaptureNonStream(body, scope, c)
	if c.Len() != 1 {
		t.Fatalf("expected 1 entry, got %d", c.Len())
	}
}

func TestCaptureNonStream_IgnoresMissingReasoning(t *testing.T) {
	c := NewCache(8, time.Minute)
	scope := Scope{Provider: "deepseek", AuthHash: "h", Model: "m", BaseURL: "https://api.deepseek.com", Session: "s"}
	body := []byte(`{"choices":[{"index":0,"message":{"role":"assistant","content":"hi"}}]}`)
	CaptureNonStream(body, scope, c)
	if c.Len() != 0 {
		t.Fatalf("expected 0 entries, got %d", c.Len())
	}
}

func TestPatchRequest_InjectsCachedReasoning(t *testing.T) {
	c := NewCache(8, time.Minute)
	scope := Scope{Provider: "deepseek", AuthHash: "h", Model: "m", BaseURL: "https://api.deepseek.com", Session: "s"}
	// First capture from a prior response.
	prior := []byte(`{"choices":[{"index":0,"message":{"role":"assistant","content":"","reasoning_content":"step by step","tool_calls":[{"id":"call_y","type":"function","function":{"name":"f","arguments":"{}"}}]}}]}`)
	CaptureNonStream(prior, scope, c)

	// Now an outgoing request omits reasoning_content but has the same tool_call.
	req := []byte(`{"messages":[
		{"role":"user","content":"hi"},
		{"role":"assistant","content":"","tool_calls":[{"id":"call_y","type":"function","function":{"name":"f","arguments":"{}"}}]},
		{"role":"tool","tool_call_id":"call_y","content":"42"}
	]}`)
	patched := PatchRequest(req, scope, c)
	var root map[string]any
	if err := json.Unmarshal(patched, &root); err != nil {
		t.Fatal(err)
	}
	msgs := root["messages"].([]any)
	asst := msgs[1].(map[string]any)
	if asst["reasoning_content"] != "step by step" {
		t.Errorf("expected reasoning injected, got %v", asst["reasoning_content"])
	}
}

func TestReasoningCaptureThenReplayRoundTrip(t *testing.T) {
	c := NewCache(8, time.Minute)
	scope := Scope{Provider: "deepseek", AuthHash: "h", Model: "deepseek-chat", BaseURL: "https://api.deepseek.com", Session: "chat-42"}

	prior := []byte(`{"choices":[{"index":0,"message":{"role":"assistant","content":"I will use the tool.","reasoning_content":"Need current weather before answering.","tool_calls":[{"id":"call_weather","type":"function","function":{"name":"weather","arguments":"{\"city\":\"Paris\"}"}}]}}]}`)
	CaptureNonStream(prior, scope, c)

	next := []byte(`{"messages":[
		{"role":"user","content":"weather in Paris?"},
		{"role":"assistant","content":"I will use the tool.","tool_calls":[{"id":"call_weather","type":"function","function":{"name":"weather","arguments":"{\"city\":\"Paris\"}"}}]},
		{"role":"tool","tool_call_id":"call_weather","content":"sunny"}
	]}`)
	patched := PatchRequest(next, scope, c)
	var root map[string]any
	if err := json.Unmarshal(patched, &root); err != nil {
		t.Fatal(err)
	}
	msgs := root["messages"].([]any)
	asst := msgs[1].(map[string]any)
	if asst["reasoning_content"] != "Need current weather before answering." {
		t.Fatalf("reasoning replay mismatch: %#v", asst)
	}
}

func TestPatchRequest_LeavesPresentReasoning(t *testing.T) {
	c := NewCache(8, time.Minute)
	scope := Scope{Provider: "deepseek", AuthHash: "h", Model: "m", BaseURL: "https://api.deepseek.com", Session: "s"}
	c.Store(Key{Scope: scope, ToolCallIDs: "call_z"}, "from-cache")
	req := []byte(`{"messages":[{"role":"assistant","content":"","reasoning_content":"already there","tool_calls":[{"id":"call_z"}]}]}`)
	out := PatchRequest(req, scope, c)
	var root map[string]any
	_ = json.Unmarshal(out, &root)
	msgs := root["messages"].([]any)
	if msgs[0].(map[string]any)["reasoning_content"] != "already there" {
		t.Errorf("expected existing reasoning preserved")
	}
}

func TestAPIKeyHash_DifferentKeysDifferentHashes(t *testing.T) {
	if APIKeyHash("a") == APIKeyHash("b") {
		t.Fatalf("expected different hashes")
	}
	if APIKeyHash("a") != APIKeyHash("a") {
		t.Fatalf("expected stable hash")
	}
}
