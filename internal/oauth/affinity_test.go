package oauth

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"droid-proxy/internal/config"
)

func TestAffinityStore_BindAndLookup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "conversation_affinity.json")
	now := time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC)
	store, err := NewAffinityStore(AffinityOptions{
		Path:    path,
		TTL:     time.Hour,
		MaxEntries: 100,
		NowFunc: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Bind("conv-a", "/tmp/a.json"); err != nil {
		t.Fatal(err)
	}
	if got := store.Lookup("conv-a"); got != "/tmp/a.json" {
		t.Fatalf("lookup = %q, want /tmp/a.json", got)
	}

	store2, err := NewAffinityStore(AffinityOptions{Path: path, NowFunc: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if got := store2.Lookup("conv-a"); got != "/tmp/a.json" {
		t.Fatalf("reloaded lookup = %q", got)
	}
}

func TestPool_Select_StickySameConversation(t *testing.T) {
	dir := t.TempDir()
	affinityPath := filepath.Join(dir, "affinity.json")
	now := time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC)
	aff, err := NewAffinityStore(AffinityOptions{Path: affinityPath, NowFunc: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}

	tok1 := makeToken("a@test.com", "access-1", "refresh-1", false)
	tok1.path = filepath.Join(dir, "a.json")
	tok2 := makeToken("b@test.com", "access-2", "refresh-2", false)
	tok2.path = filepath.Join(dir, "b.json")

	lb := config.LoadBalancing{Strategy: config.LoadBalancingSticky, QuotaSoftCapPercent: 0}
	pool := NewAccountPool([]*Token{tok1, tok2}, func() time.Time { return now }, lb, aff)

	first, err := pool.Select("", nil, "session-1")
	if err != nil {
		t.Fatal(err)
	}
	second, err := pool.Select("", nil, "session-1")
	if err != nil {
		t.Fatal(err)
	}
	if first.Path != second.Path {
		t.Fatalf("sticky mismatch: %s vs %s", first.Path, second.Path)
	}

	other, err := pool.Select("", nil, "session-2")
	if err != nil {
		t.Fatal(err)
	}
	// Parallel sessions may use different accounts when both are fresh.
	_ = other
}

func TestPool_Select_StickyFailoverRebind(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC)
	aff, err := NewAffinityStore(AffinityOptions{Path: filepath.Join(dir, "affinity.json"), NowFunc: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	tok1 := makeToken("a@test.com", "access-1", "refresh-1", false)
	tok1.path = filepath.Join(dir, "a.json")
	tok2 := makeToken("b@test.com", "access-2", "refresh-2", false)
	tok2.path = filepath.Join(dir, "b.json")
	lb := config.LoadBalancing{Strategy: config.LoadBalancingSticky, QuotaSoftCapPercent: 0}
	pool := NewAccountPool([]*Token{tok1, tok2}, func() time.Time { return now }, lb, aff)

	first, _ := pool.Select("", nil, "conv-x")
	exclude := map[string]bool{first.Path: true}
	second, err := pool.Select("", exclude, "conv-x")
	if err != nil {
		t.Fatal(err)
	}
	if second.Path == first.Path {
		t.Fatal("expected different account after exclude")
	}
	third, _ := pool.Select("", nil, "conv-x")
	if third.Path != second.Path {
		t.Fatalf("expected rebound to %s, got %s", second.Path, third.Path)
	}
}

func TestAffinityStore_PruneRemovesUnknownPaths(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "affinity.json")
	store, err := NewAffinityStore(AffinityOptions{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	_ = store.Bind("c1", "/gone.json")
	if err := store.Prune(map[string]bool{"/keep.json": true}); err != nil {
		t.Fatal(err)
	}
	if store.Lookup("c1") != "" {
		t.Fatal("expected pruned binding")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) == 0 {
		t.Fatal("expected affinity file")
	}
}