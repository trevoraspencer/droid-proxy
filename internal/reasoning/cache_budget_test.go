package reasoning

import (
	"testing"
	"time"
)

func TestReasoningCacheRetainedTextBudget(t *testing.T) {
	c := NewCache(1024, time.Minute)
	c.maxBytes = 8
	now := time.Unix(100, 0)
	c.now = func() time.Time { return now }
	first := validKey()
	second := first
	second.ToolCallIDs = "other"
	c.Store(first, "12345")
	now = now.Add(time.Second)
	c.Store(second, "67890")
	if _, ok := c.Lookup(first); ok {
		t.Fatal("oldest entry survived budget eviction")
	}
	if got, ok := c.Lookup(second); !ok || got != "67890" {
		t.Fatal("new reasoning lost")
	}
	c.Store(second, "abc")
	if c.bytes != 3 {
		t.Fatalf("replacement accounting = %d", c.bytes)
	}
	c.Store(first, "oversized")
	if c.Len() != 1 || c.bytes != 3 {
		t.Fatal("oversized value displaced useful reasoning")
	}
	now = now.Add(2 * time.Minute)
	if _, ok := c.Lookup(second); ok || c.bytes != 0 {
		t.Fatal("expired entry retained memory accounting")
	}
}
