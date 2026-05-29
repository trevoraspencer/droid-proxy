// Package reasoning implements DeepSeek-style reasoning replay: when an upstream
// model returns reasoning_content alongside tool_calls, the proxy captures it and
// re-injects it on subsequent requests that reference the same tool_call ids,
// so the upstream sees a complete assistant turn even though the client's
// transcript may omit reasoning.
//
// The cache is keyed by provider, auth hash, model, base URL, session id, and
// the set of tool_call ids (or a content hash when no tool_calls are present).
package reasoning

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	DefaultMaxEntries = 1024
	DefaultTTL        = 30 * time.Minute
)

// Scope identifies a "logical session" — the set of fields under which two
// requests share a reasoning context.
type Scope struct {
	Provider string
	AuthHash string
	Model    string
	BaseURL  string
	Session  string
	// ThinkingMode lets users with multiple thinking budgets keep separate caches.
	ThinkingMode string
}

// Key augments Scope with the specific assistant turn being looked up.
type Key struct {
	Scope
	ToolCallIDs string
	TurnHash    string
}

func (k Key) valid() bool {
	return k.Provider != "" && k.AuthHash != "" && k.Model != "" && k.BaseURL != "" && k.Session != "" && (k.ToolCallIDs != "" || k.TurnHash != "")
}

type entry struct {
	reasoning string
	createdAt time.Time
}

// Cache holds reasoning blobs with TTL + max-entries eviction.
type Cache struct {
	mu         sync.RWMutex
	now        func() time.Time
	ttl        time.Duration
	maxEntries int
	entries    map[Key]entry
}

// NewCache builds a Cache with the given limits. Zero values fall back to defaults.
func NewCache(maxEntries int, ttl time.Duration) *Cache {
	if maxEntries <= 0 {
		maxEntries = DefaultMaxEntries
	}
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	return &Cache{
		now:        time.Now,
		ttl:        ttl,
		maxEntries: maxEntries,
		entries:    make(map[Key]entry),
	}
}

// Store inserts a reasoning blob under key. No-op on empty reasoning or invalid key.
func (c *Cache) Store(key Key, reasoning string) {
	if c == nil || strings.TrimSpace(reasoning) == "" || !key.valid() {
		return
	}
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = entry{reasoning: reasoning, createdAt: now}
	c.evictLocked(now)
}

// Lookup returns the stored reasoning string for key, plus a found bool.
func (c *Cache) Lookup(key Key) (string, bool) {
	if c == nil || !key.valid() {
		return "", false
	}
	now := c.now()
	c.mu.RLock()
	e, ok := c.entries[key]
	c.mu.RUnlock()
	if !ok {
		return "", false
	}
	if now.Sub(e.createdAt) > c.ttl {
		c.mu.Lock()
		if current, present := c.entries[key]; present && now.Sub(current.createdAt) > c.ttl {
			delete(c.entries, key)
		}
		c.mu.Unlock()
		return "", false
	}
	return e.reasoning, true
}

// Len returns the current number of entries (for tests/metrics).
func (c *Cache) Len() int {
	if c == nil {
		return 0
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.entries)
}

func (c *Cache) evictLocked(now time.Time) {
	for k, e := range c.entries {
		if now.Sub(e.createdAt) > c.ttl {
			delete(c.entries, k)
		}
	}
	for len(c.entries) > c.maxEntries {
		var oldestKey Key
		var oldest time.Time
		first := true
		for k, e := range c.entries {
			if first || e.createdAt.Before(oldest) {
				oldestKey = k
				oldest = e.createdAt
				first = false
			}
		}
		delete(c.entries, oldestKey)
	}
}

// APIKeyHash returns a stable, short hash of an API key suitable for cache scoping.
func APIKeyHash(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:8])
}

// KeyForMessage builds a cache Key from a JSON-decoded assistant message.
func KeyForMessage(scope Scope, message map[string]any) Key {
	tool := toolCallIDs(message["tool_calls"])
	turn := ""
	if tool == "" {
		turn = assistantTurnHash(message)
	}
	return Key{Scope: scope, ToolCallIDs: tool, TurnHash: turn}
}

func toolCallIDs(raw any) string {
	tcs, ok := raw.([]any)
	if !ok || len(tcs) == 0 {
		return ""
	}
	ids := make([]string, 0, len(tcs))
	for _, rt := range tcs {
		t, ok := rt.(map[string]any)
		if !ok {
			continue
		}
		id := strings.TrimSpace(stringFromAny(t["id"]))
		if id != "" {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return ""
	}
	sort.Strings(ids)
	return strings.Join(ids, "\x00")
}

func assistantTurnHash(message map[string]any) string {
	if len(message) == 0 {
		return ""
	}
	out := make(map[string]any, len(message))
	for k, v := range message {
		if k == "reasoning_content" {
			continue
		}
		out[k] = v
	}
	canonical, err := json.Marshal(out)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}

func stringFromAny(raw any) string {
	switch v := raw.(type) {
	case string:
		return v
	case []byte:
		return string(v)
	}
	return ""
}

// validToolCalls checks that every tool_call has the fields required for replay.
func validToolCalls(raw any) bool {
	tcs, ok := raw.([]any)
	if !ok || len(tcs) == 0 {
		return false
	}
	for _, rt := range tcs {
		t, ok := rt.(map[string]any)
		if !ok {
			return false
		}
		if strings.TrimSpace(stringFromAny(t["id"])) == "" {
			return false
		}
		fn, ok := t["function"].(map[string]any)
		if !ok {
			return false
		}
		if strings.TrimSpace(stringFromAny(fn["name"])) == "" {
			return false
		}
		if _, ok := fn["arguments"].(string); !ok {
			return false
		}
	}
	return true
}

// PatchRequest scans an outgoing chat-completions JSON payload and injects
// cached reasoning_content on assistant messages that have tool_calls but no
// reasoning_content yet.
func PatchRequest(payload []byte, scope Scope, cache *Cache) []byte {
	if len(payload) == 0 || cache == nil {
		return payload
	}
	var root map[string]any
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.UseNumber()
	if err := dec.Decode(&root); err != nil {
		return payload
	}
	messages, ok := root["messages"].([]any)
	if !ok {
		return payload
	}
	changed := false
	for _, rm := range messages {
		m, ok := rm.(map[string]any)
		if !ok || strings.TrimSpace(stringFromAny(m["role"])) != "assistant" {
			continue
		}
		if _, ex := m["reasoning_content"]; ex {
			continue
		}
		tcs, ok := m["tool_calls"].([]any)
		if !ok || len(tcs) == 0 {
			continue
		}
		key := KeyForMessage(scope, m)
		reasoning, ok := cache.Lookup(key)
		if !ok {
			continue
		}
		m["reasoning_content"] = reasoning
		changed = true
	}
	if !changed {
		return payload
	}
	out, err := json.Marshal(root)
	if err != nil {
		return payload
	}
	return out
}

// CaptureNonStream reads a non-streaming chat-completions response body and
// stores any reasoning_content present on assistant messages that include
// tool_calls (the case we need to replay later).
func CaptureNonStream(body []byte, scope Scope, cache *Cache) {
	if len(body) == 0 || cache == nil {
		return
	}
	var root map[string]any
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&root); err != nil {
		return
	}
	choices, ok := root["choices"].([]any)
	if !ok {
		return
	}
	for _, rc := range choices {
		c, ok := rc.(map[string]any)
		if !ok {
			continue
		}
		msg, ok := c["message"].(map[string]any)
		if !ok {
			continue
		}
		if strings.TrimSpace(stringFromAny(msg["role"])) != "assistant" {
			continue
		}
		reasoning, ok := msg["reasoning_content"].(string)
		if !ok || strings.TrimSpace(reasoning) == "" {
			continue
		}
		if !validToolCalls(msg["tool_calls"]) {
			continue
		}
		cache.Store(KeyForMessage(scope, msg), reasoning)
	}
}
