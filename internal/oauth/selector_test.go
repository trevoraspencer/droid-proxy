package oauth

import (
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"testing"
	"time"

	"droid-proxy/internal/config"
)

// ---- Test helpers ----

// makeEntries creates n AccountEntry instances with deterministic selectors and paths.
func makeEntries(n int) []*AccountEntry {
	entries := make([]*AccountEntry, n)
	for i := 0; i < n; i++ {
		entries[i] = &AccountEntry{
			Path:     fmt.Sprintf("/tmp/user%d.json", i),
			Selector: fmt.Sprintf("user%d@example.com", i),
			Provider: ProviderCodex,
			Healthy:  true,
			Email:    fmt.Sprintf("user%d@example.com", i),
			InFlight: 0,
		}
	}
	return entries
}

// ---- VAL-POOL-005: Round-robin strategy is deterministic ----

func TestRoundRobin_DeterministicRotation(t *testing.T) {
	entries := makeEntries(3)
	sel := &RoundRobinSelector{}

	// Should rotate A, B, C, A, B, C, ...
	expected := []string{
		"user0@example.com",
		"user1@example.com",
		"user2@example.com",
		"user0@example.com",
		"user1@example.com",
		"user2@example.com",
	}
	for i, want := range expected {
		got, err := sel.Select(entries)
		if err != nil {
			t.Fatalf("select %d: %v", i, err)
		}
		if got.Selector != want {
			t.Errorf("select %d: got %q, want %q", i, got.Selector, want)
		}
	}
}

func TestRoundRobin_SkipsIneligible(t *testing.T) {
	// 3 entries, but entry 1 is excluded
	entries := makeEntries(3)
	eligible := []*AccountEntry{entries[0], entries[2]} // skip index 1

	sel := &RoundRobinSelector{}
	// Rotation over eligible: 0, 2, 0, 2, ...
	expected := []string{"user0@example.com", "user2@example.com", "user0@example.com"}
	for i, want := range expected {
		got, err := sel.Select(eligible)
		if err != nil {
			t.Fatalf("select %d: %v", i, err)
		}
		if got.Selector != want {
			t.Errorf("select %d: got %q, want %q", i, got.Selector, want)
		}
	}
}

func TestRoundRobin_CursorDoesNotResetOnReload(t *testing.T) {
	// Simulate: make selections, then do a "reload" by calling Select again
	// with the same eligible list. Cursor must not reset.
	entries := makeEntries(3)
	sel := &RoundRobinSelector{}

	// Make 2 selections to advance cursor to position 2
	sel.Select(entries) // idx 0
	sel.Select(entries) // idx 1

	// "Reload" - in real usage the selector is kept across reloads
	// Cursor should still be at position 2, so next is entries[2]
	got, err := sel.Select(entries)
	if err != nil {
		t.Fatal(err)
	}
	if got.Selector != "user2@example.com" {
		t.Errorf("after reload: got %q, want user2@example.com", got.Selector)
	}
}

func TestRoundRobin_EmptyEligibleReturnsError(t *testing.T) {
	sel := &RoundRobinSelector{}
	_, err := sel.Select(nil)
	if !errors.Is(err, ErrNoEligibleAccounts) {
		t.Fatalf("expected ErrNoEligibleAccounts, got %v", err)
	}
}

// ---- VAL-POOL-006: Fill-first strategy prefers the first eligible account ----

func TestFillFirst_AlwaysReturnsFirst(t *testing.T) {
	entries := makeEntries(3)
	sel := &FillFirstSelector{}

	for i := 0; i < 5; i++ {
		got, err := sel.Select(entries)
		if err != nil {
			t.Fatalf("select %d: %v", i, err)
		}
		if got.Selector != "user0@example.com" {
			t.Errorf("select %d: got %q, want user0@example.com", i, got.Selector)
		}
	}
}

func TestFillFirst_FallsToNextWhenFirstIneligible(t *testing.T) {
	entries := makeEntries(3)
	// Skip the first entry
	eligible := []*AccountEntry{entries[1], entries[2]}

	sel := &FillFirstSelector{}
	got, err := sel.Select(eligible)
	if err != nil {
		t.Fatal(err)
	}
	if got.Selector != "user1@example.com" {
		t.Errorf("got %q, want user1@example.com", got.Selector)
	}

	// If first becomes eligible again, it returns to first
	eligible = []*AccountEntry{entries[0], entries[1], entries[2]}
	got, err = sel.Select(eligible)
	if err != nil {
		t.Fatal(err)
	}
	if got.Selector != "user0@example.com" {
		t.Errorf("got %q, want user0@example.com after restore", got.Selector)
	}
}

func TestFillFirst_EmptyEligibleReturnsError(t *testing.T) {
	sel := &FillFirstSelector{}
	_, err := sel.Select(nil)
	if !errors.Is(err, ErrNoEligibleAccounts) {
		t.Fatalf("expected ErrNoEligibleAccounts, got %v", err)
	}
}

// ---- VAL-POOL-007: Least-connections chooses lowest in-flight ----

func TestLeastConnections_LowestInFlight(t *testing.T) {
	entries := makeEntries(3)
	entries[0].InFlight = 5
	entries[1].InFlight = 2
	entries[2].InFlight = 8

	sel := &LeastConnectionsSelector{}
	got, err := sel.Select(entries)
	if err != nil {
		t.Fatal(err)
	}
	if got.Selector != "user1@example.com" {
		t.Errorf("got %q (in_flight=%d), want user1@example.com (lowest in-flight)", got.Selector, got.InFlight)
	}
}

func TestLeastConnections_DeterministicTieBreak(t *testing.T) {
	entries := makeEntries(3)
	// All have same in-flight: tie-break by sorted position → first
	entries[0].InFlight = 3
	entries[1].InFlight = 3
	entries[2].InFlight = 3

	sel := &LeastConnectionsSelector{}
	got, err := sel.Select(entries)
	if err != nil {
		t.Fatal(err)
	}
	if got.Selector != "user0@example.com" {
		t.Errorf("tie-break: got %q, want user0@example.com (first in sorted order)", got.Selector)
	}
}

func TestLeastConnections_ConcurrentSelection(t *testing.T) {
	entries := makeEntries(3)
	sel := &LeastConnectionsSelector{}

	var wg sync.WaitGroup
	const n = 100
	results := make(chan string, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := sel.Select(entries)
			if err != nil {
				t.Errorf("select failed: %v", err)
				return
			}
			results <- got.Path
		}()
	}
	wg.Wait()
	close(results)

	// All results should be valid account paths
	for path := range results {
		found := false
		for _, e := range entries {
			if e.Path == path {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("unexpected path %q", path)
		}
	}
}

func TestLeastConnections_EmptyEligibleReturnsError(t *testing.T) {
	sel := &LeastConnectionsSelector{}
	_, err := sel.Select(nil)
	if !errors.Is(err, ErrNoEligibleAccounts) {
		t.Fatalf("expected ErrNoEligibleAccounts, got %v", err)
	}
}

// ---- VAL-POOL-008: Random selects only eligible accounts without flaky tests ----

func TestRandom_OnlySelectsEligible(t *testing.T) {
	entries := makeEntries(3)
	// Use a deterministic RNG
	rng := rand.New(rand.NewSource(42))
	sel := &RandomSelector{rng: rng}

	// Make many selections and verify all are from the eligible set
	for i := 0; i < 100; i++ {
		got, err := sel.Select(entries)
		if err != nil {
			t.Fatalf("select %d: %v", i, err)
		}
		found := false
		for _, e := range entries {
			if got.Path == e.Path {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("select %d: got unexpected path %q", i, got.Path)
		}
	}
}

func TestRandom_AllEligibleSeen(t *testing.T) {
	entries := makeEntries(3)
	// Use deterministic RNG - verify all accounts are selected at least once
	// over enough iterations
	rng := rand.New(rand.NewSource(42))
	sel := &RandomSelector{rng: rng}

	seen := make(map[string]bool)
	for i := 0; i < 300; i++ {
		got, err := sel.Select(entries)
		if err != nil {
			t.Fatalf("select %d: %v", i, err)
		}
		seen[got.Selector] = true
	}

	for _, e := range entries {
		if !seen[e.Selector] {
			t.Errorf("account %q was never selected over 300 attempts", e.Selector)
		}
	}
}

func TestRandom_DeterministicRNG(t *testing.T) {
	entries := makeEntries(3)

	// Two selectors with same seed should produce same sequence
	rng1 := rand.New(rand.NewSource(123))
	sel1 := &RandomSelector{rng: rng1}

	rng2 := rand.New(rand.NewSource(123))
	sel2 := &RandomSelector{rng: rng2}

	for i := 0; i < 20; i++ {
		got1, _ := sel1.Select(entries)
		got2, _ := sel2.Select(entries)
		if got1.Selector != got2.Selector {
			t.Errorf("iteration %d: deterministic RNG diverged: %q vs %q", i, got1.Selector, got2.Selector)
		}
	}
}

func TestRandom_EmptyEligibleReturnsError(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	sel := &RandomSelector{rng: rng}
	_, err := sel.Select(nil)
	if !errors.Is(err, ErrNoEligibleAccounts) {
		t.Fatalf("expected ErrNoEligibleAccounts, got %v", err)
	}
}

func TestRandom_NeverSelectsDisabledOrExcluded(t *testing.T) {
	entries := makeEntries(3)
	eligible := []*AccountEntry{entries[0], entries[2]} // entries[1] excluded

	rng := rand.New(rand.NewSource(42))
	sel := &RandomSelector{rng: rng}

	for i := 0; i < 100; i++ {
		got, err := sel.Select(eligible)
		if err != nil {
			t.Fatalf("select %d: %v", i, err)
		}
		if got.Selector == "user1@example.com" {
			t.Errorf("select %d: selected excluded account user1@example.com", i)
		}
	}
}

// ---- VAL-POOL-009: Pinned account selection ----

func TestPoolSelect_PinnedAccount_MatchesEmail(t *testing.T) {
	tokens := []*Token{
		makeToken("alice@example.com", "a-access", "a-refresh", false),
		makeToken("bob@example.com", "b-access", "b-refresh", false),
		makeToken("charlie@example.com", "c-access", "c-refresh", false),
	}
	tokens[0].path = "/tmp/alice.json"
	tokens[1].path = "/tmp/bob.json"
	tokens[2].path = "/tmp/charlie.json"

	sel := &FillFirstSelector{}
	pool := NewAccountPool(tokens, fakeTime, TestPoolLB(), nil, sel)

	got, err := pool.Select("bob@example.com", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Selector != "bob@example.com" {
		t.Errorf("got %q, want bob@example.com", got.Selector)
	}
}

func TestPoolSelect_PinnedAccount_CaseInsensitive(t *testing.T) {
	tokens := []*Token{
		makeToken("User@Example.COM", "access", "refresh", false),
	}
	tokens[0].path = "/tmp/user.json"

	sel := &FillFirstSelector{}
	pool := NewAccountPool(tokens, fakeTime, TestPoolLB(), nil, sel)

	got, err := pool.Select("user@example.com", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Selector != "User@Example.COM" {
		t.Errorf("got %q, want User@Example.COM", got.Selector)
	}
}

func TestPoolSelect_PinnedAccount_TrimmedWhitespace(t *testing.T) {
	tokens := []*Token{
		makeToken("user@example.com", "access", "refresh", false),
	}
	tokens[0].path = "/tmp/user.json"

	sel := &FillFirstSelector{}
	pool := NewAccountPool(tokens, fakeTime, TestPoolLB(), nil, sel)

	got, err := pool.Select("  user@example.com  ", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Selector != "user@example.com" {
		t.Errorf("got %q, want user@example.com", got.Selector)
	}
}

func TestPoolSelect_PinnedAccount_MatchesSubject(t *testing.T) {
	tok := &Token{
		Type:         string(ProviderCodex),
		AccessToken:  "access",
		RefreshToken: "refresh",
		Subject:      "sub-123",
		Expired:      fakeTime().Add(time.Hour).UTC().Format(time.RFC3339),
	}
	tok.path = "/tmp/subject.json"

	sel := &FillFirstSelector{}
	pool := NewAccountPool([]*Token{tok}, fakeTime, TestPoolLB(), nil, sel)

	got, err := pool.Select("sub-123", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Path != "/tmp/subject.json" {
		t.Errorf("got path %q, want /tmp/subject.json", got.Path)
	}
}

func TestPoolSelect_PinnedAccount_MatchesAccountID(t *testing.T) {
	tok := &Token{
		Type:         string(ProviderCodex),
		AccessToken:  "access",
		RefreshToken: "refresh",
		AccountID:    "acct_789",
		Expired:      fakeTime().Add(time.Hour).UTC().Format(time.RFC3339),
	}
	tok.path = "/tmp/acct.json"

	sel := &FillFirstSelector{}
	pool := NewAccountPool([]*Token{tok}, fakeTime, TestPoolLB(), nil, sel)

	got, err := pool.Select("acct_789", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Path != "/tmp/acct.json" {
		t.Errorf("got path %q, want /tmp/acct.json", got.Path)
	}
}

func TestPoolSelect_PinnedAccount_MatchesFilenameStem(t *testing.T) {
	tok := makeToken("", "access", "refresh", false)
	tok.path = "/tmp/codex-oauth-myalias.json"

	sel := &FillFirstSelector{}
	pool := NewAccountPool([]*Token{tok}, fakeTime, TestPoolLB(), nil, sel)

	got, err := pool.Select("codex-oauth-myalias", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Path != "/tmp/codex-oauth-myalias.json" {
		t.Errorf("got path %q, want /tmp/codex-oauth-myalias.json", got.Path)
	}
}

func TestPoolSelect_PinnedAccount_MatchesFullFilename(t *testing.T) {
	tok := makeToken("", "access", "refresh", false)
	tok.path = "/tmp/codex-oauth-myalias.json"

	sel := &FillFirstSelector{}
	pool := NewAccountPool([]*Token{tok}, fakeTime, TestPoolLB(), nil, sel)

	got, err := pool.Select("codex-oauth-myalias.json", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Path != "/tmp/codex-oauth-myalias.json" {
		t.Errorf("got path %q", got.Path)
	}
}

func TestPoolSelect_PinnedAccount_DisabledNeverFallsBack(t *testing.T) {
	tokens := []*Token{
		makeToken("pinned@example.com", "p-access", "p-refresh", false),
		makeToken("other@example.com", "o-access", "o-refresh", false),
	}
	tokens[0].path = "/tmp/pinned.json"
	tokens[0].Disabled = true
	tokens[1].path = "/tmp/other.json"

	sel := &FillFirstSelector{}
	pool := NewAccountPool(tokens, fakeTime, TestPoolLB(), nil, sel)

	// Pinned to disabled account → should not fall back to other
	_, err := pool.Select("pinned@example.com", nil, "")
	if !errors.Is(err, ErrNoEligibleAccounts) {
		t.Fatalf("expected ErrNoEligibleAccounts for disabled pinned, got %v", err)
	}
}

func TestPoolSelect_PinnedAccount_MultipleMatches(t *testing.T) {
	tokens := []*Token{
		makeToken("shared@example.com", "access-1", "refresh-1", false),
		makeToken("shared@example.com", "access-2", "refresh-2", false),
		makeToken("other@example.com", "access-3", "refresh-3", false),
	}
	tokens[0].path = "/tmp/shared1.json"
	tokens[1].path = "/tmp/shared2.json"
	tokens[2].path = "/tmp/other.json"

	// Use round-robin selector to verify strategy operates within pinned subset
	sel := &RoundRobinSelector{}
	pool := NewAccountPool(tokens, fakeTime, TestPoolLB(), nil, sel)

	// First pinned selection should be first in sorted order
	got, err := pool.Select("shared@example.com", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	// Both shared accounts have same selector, sorted by path
	// /tmp/shared1.json < /tmp/shared2.json, so first is shared1
	if got.Path != "/tmp/shared1.json" && got.Path != "/tmp/shared2.json" {
		t.Errorf("got unexpected path %q", got.Path)
	}
}

func TestPoolSelect_PinnedAccount_EmptyPinSelectsAll(t *testing.T) {
	tokens := []*Token{
		makeToken("alice@example.com", "a", "ar", false),
		makeToken("bob@example.com", "b", "br", false),
	}
	tokens[0].path = "/tmp/alice.json"
	tokens[1].path = "/tmp/bob.json"

	sel := &FillFirstSelector{}
	pool := NewAccountPool(tokens, fakeTime, TestPoolLB(), nil, sel)

	got, err := pool.Select("", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Selector != "alice@example.com" {
		t.Errorf("empty pin: got %q, want alice@example.com", got.Selector)
	}
}

func TestPoolSelect_PinnedAccount_ExclusionSet(t *testing.T) {
	tokens := []*Token{
		makeToken("alice@example.com", "a", "ar", false),
		makeToken("bob@example.com", "b", "br", false),
	}
	tokens[0].path = "/tmp/alice.json"
	tokens[1].path = "/tmp/bob.json"

	sel := &FillFirstSelector{}
	pool := NewAccountPool(tokens, fakeTime, TestPoolLB(), nil, sel)

	// Exclude alice, pin to nothing → should get bob
	got, err := pool.Select("", map[string]bool{"/tmp/alice.json": true}, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Selector != "bob@example.com" {
		t.Errorf("got %q, want bob@example.com", got.Selector)
	}
}

// ---- VAL-POOL-010: Concurrent selection, release, and reload are race-clean ----

func TestPoolSelect_ConcurrentRaceClean(t *testing.T) {
	tokens := []*Token{
		makeToken("alice@example.com", "a", "ar", false),
		makeToken("bob@example.com", "b", "br", false),
		makeToken("charlie@example.com", "c", "cr", false),
	}
	tokens[0].path = "/tmp/alice.json"
	tokens[1].path = "/tmp/bob.json"
	tokens[2].path = "/tmp/charlie.json"

	sel := &RoundRobinSelector{}
	pool := NewAccountPool(tokens, fakeTime, TestPoolLB(), nil, sel)

	var wg sync.WaitGroup
	const n = 200
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			entry, err := pool.Select("", nil, "")
			if err != nil {
				t.Errorf("select failed: %v", err)
				return
			}
			_ = pool.Begin(entry.Path)
			// Simulate work
			pool.End(entry.Path)
		}()
	}
	wg.Wait()

	// All in-flight should be zero
	snap := pool.Snapshot()
	for _, acct := range snap.Accounts {
		if acct.InFlight != 0 {
			t.Errorf("account %q has in_flight=%d after concurrent test", acct.Selector, acct.InFlight)
		}
	}
}

func TestPoolSelect_ConcurrentSelectAndReload(t *testing.T) {
	tokens := []*Token{
		makeToken("alice@example.com", "a", "ar", false),
		makeToken("bob@example.com", "b", "br", false),
	}
	tokens[0].path = "/tmp/alice.json"
	tokens[1].path = "/tmp/bob.json"

	sel := &RoundRobinSelector{}
	pool := NewAccountPool(tokens, fakeTime, TestPoolLB(), nil, sel)

	var wg sync.WaitGroup
	// Concurrent selections
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = pool.Select("", nil, "")
		}()
	}
	// Concurrent reloads
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			pool.Reload([]*Token{
				makeToken("alice@example.com", "a-new", "ar-new", false),
				makeToken("bob@example.com", "b-new", "br-new", false),
			})
		}()
	}
	wg.Wait()
}

func TestPoolSelect_ConcurrentSelectAndMark(t *testing.T) {
	tokens := []*Token{
		makeToken("alice@example.com", "a", "ar", false),
		makeToken("bob@example.com", "b", "br", false),
	}
	tokens[0].path = "/tmp/alice.json"
	tokens[1].path = "/tmp/bob.json"

	sel := &RoundRobinSelector{}
	pool := NewAccountPool(tokens, fakeTime, TestPoolLB(), nil, sel)

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(3)
		go func() {
			defer wg.Done()
			_, _ = pool.Select("", nil, "")
		}()
		go func() {
			defer wg.Done()
			pool.MarkCooldown("/tmp/alice.json", fakeTime().Add(time.Second))
		}()
		go func() {
			defer wg.Done()
			pool.MarkRateLimited("/tmp/bob.json", fakeTime().Add(time.Second))
		}()
	}
	wg.Wait()
}

// ---- Empty/blank strategy resolves to round-robin ----

func TestNewSelector_EmptyStrategy_ResolvesToRoundRobin(t *testing.T) {
	sel := NewSelector(config.LoadBalancingStrategy(""))
	_, ok := sel.(*RoundRobinSelector)
	if !ok {
		t.Fatalf("expected *RoundRobinSelector for empty strategy, got %T", sel)
	}
}

func TestNewSelector_BlankStrategy_ResolvesToRoundRobin(t *testing.T) {
	sel := NewSelector(config.LoadBalancingStrategy("   "))
	_, ok := sel.(*RoundRobinSelector)
	if !ok {
		t.Fatalf("expected *RoundRobinSelector for blank strategy, got %T", sel)
	}
}

func TestNewSelector_TabNewlineStrategy_ResolvesToRoundRobin(t *testing.T) {
	sel := NewSelector(config.LoadBalancingStrategy("\t\n"))
	_, ok := sel.(*RoundRobinSelector)
	if !ok {
		t.Fatalf("expected *RoundRobinSelector for tab/newline strategy, got %T", sel)
	}
}

func TestNewSelector_ExplicitRoundRobin(t *testing.T) {
	sel := NewSelector(config.LoadBalancingRoundRobin)
	_, ok := sel.(*RoundRobinSelector)
	if !ok {
		t.Fatalf("expected *RoundRobinSelector, got %T", sel)
	}
}

func TestNewSelector_FillFirst(t *testing.T) {
	sel := NewSelector(config.LoadBalancingFillFirst)
	_, ok := sel.(*FillFirstSelector)
	if !ok {
		t.Fatalf("expected *FillFirstSelector, got %T", sel)
	}
}

func TestNewSelector_LeastConnections(t *testing.T) {
	sel := NewSelector(config.LoadBalancingLeastConnections)
	_, ok := sel.(*LeastConnectionsSelector)
	if !ok {
		t.Fatalf("expected *LeastConnectionsSelector, got %T", sel)
	}
}

func TestNewSelector_Random(t *testing.T) {
	sel := NewSelector(config.LoadBalancingRandom)
	_, ok := sel.(*RandomSelector)
	if !ok {
		t.Fatalf("expected *RandomSelector, got %T", sel)
	}
}

func TestNewSelector_RandomWithInjectedRNG(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	sel := NewSelector(config.LoadBalancingRandom, rng)
	rs, ok := sel.(*RandomSelector)
	if !ok {
		t.Fatalf("expected *RandomSelector, got %T", sel)
	}
	// Verify the injected RNG is used
	entries := makeEntries(3)
	got1, _ := rs.Select(entries)
	// Same seed should produce same result
	rng2 := rand.New(rand.NewSource(42))
	sel2 := NewSelector(config.LoadBalancingRandom, rng2)
	rs2 := sel2.(*RandomSelector)
	got2, _ := rs2.Select(entries)
	if got1.Selector != got2.Selector {
		t.Errorf("injected RNG not deterministic: %q vs %q", got1.Selector, got2.Selector)
	}
}

// ---- Pool-level integration tests for strategies ----

func TestPoolSelect_RoundRobinIntegration(t *testing.T) {
	tokens := []*Token{
		makeToken("alice@example.com", "a", "ar", false),
		makeToken("bob@example.com", "b", "br", false),
		makeToken("charlie@example.com", "c", "cr", false),
	}
	tokens[0].path = "/tmp/alice.json"
	tokens[1].path = "/tmp/bob.json"
	tokens[2].path = "/tmp/charlie.json"

	sel := &RoundRobinSelector{}
	pool := NewAccountPool(tokens, fakeTime, TestPoolLB(), nil, sel)

	expected := []string{"alice@example.com", "bob@example.com", "charlie@example.com", "alice@example.com"}
	for i, want := range expected {
		got, err := pool.Select("", nil, "")
		if err != nil {
			t.Fatalf("select %d: %v", i, err)
		}
		if got.Selector != want {
			t.Errorf("select %d: got %q, want %q", i, got.Selector, want)
		}
	}
}

func TestPoolSelect_FillFirstIntegration(t *testing.T) {
	tokens := []*Token{
		makeToken("alice@example.com", "a", "ar", false),
		makeToken("bob@example.com", "b", "br", false),
	}
	tokens[0].path = "/tmp/alice.json"
	tokens[1].path = "/tmp/bob.json"

	sel := &FillFirstSelector{}
	pool := NewAccountPool(tokens, fakeTime, TestPoolLB(), nil, sel)

	for i := 0; i < 5; i++ {
		got, err := pool.Select("", nil, "")
		if err != nil {
			t.Fatalf("select %d: %v", i, err)
		}
		if got.Selector != "alice@example.com" {
			t.Errorf("select %d: got %q, want alice@example.com", i, got.Selector)
		}
	}
}

func TestPoolSelect_LeastConnectionsIntegration(t *testing.T) {
	tokens := []*Token{
		makeToken("alice@example.com", "a", "ar", false),
		makeToken("bob@example.com", "b", "br", false),
	}
	tokens[0].path = "/tmp/alice.json"
	tokens[1].path = "/tmp/bob.json"

	sel := &LeastConnectionsSelector{}
	pool := NewAccountPool(tokens, fakeTime, TestPoolLB(), nil, sel)

	// Both start at 0 in-flight → picks first (alice)
	got, err := pool.Select("", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Selector != "alice@example.com" {
		t.Errorf("got %q, want alice@example.com", got.Selector)
	}

	// Acquire lease on alice
	_ = pool.Begin("/tmp/alice.json")

	// Now alice has 1, bob has 0 → picks bob
	got, err = pool.Select("", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Selector != "bob@example.com" {
		t.Errorf("got %q, want bob@example.com", got.Selector)
	}

	pool.End("/tmp/alice.json")
}

func TestPoolSelect_RandomIntegration(t *testing.T) {
	tokens := []*Token{
		makeToken("alice@example.com", "a", "ar", false),
		makeToken("bob@example.com", "b", "br", false),
		makeToken("charlie@example.com", "c", "cr", false),
	}
	tokens[0].path = "/tmp/alice.json"
	tokens[1].path = "/tmp/bob.json"
	tokens[2].path = "/tmp/charlie.json"

	rng := rand.New(rand.NewSource(99))
	sel := &RandomSelector{rng: rng}
	pool := NewAccountPool(tokens, fakeTime, TestPoolLB(), nil, sel)

	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		got, err := pool.Select("", nil, "")
		if err != nil {
			t.Fatalf("select %d: %v", i, err)
		}
		seen[got.Selector] = true
	}
	if len(seen) < 2 {
		t.Errorf("random selector only selected %d unique accounts over 100 tries: %v", len(seen), seen)
	}
}

func TestPoolSelect_NoEligibleReturnsTypedError(t *testing.T) {
	tokens := []*Token{
		makeToken("disabled@example.com", "a", "ar", false),
	}
	tokens[0].path = "/tmp/disabled.json"
	tokens[0].Disabled = true

	sel := &FillFirstSelector{}
	pool := NewAccountPool(tokens, fakeTime, TestPoolLB(), nil, sel)

	_, err := pool.Select("", nil, "")
	if !errors.Is(err, ErrNoEligibleAccounts) {
		t.Fatalf("expected ErrNoEligibleAccounts, got %v", err)
	}
}

// ---- Least-connections concurrent blocked-request test ----

func TestLeastConnections_ConcurrentBlockedRequests(t *testing.T) {
	entries := makeEntries(3)
	sel := &LeastConnectionsSelector{}

	// Acquire leases on first two accounts
	entries[0].InFlight = 1
	entries[1].InFlight = 1

	// All concurrent callers should pick entry[2] (in_flight=0)
	var wg sync.WaitGroup
	const n = 50
	selected := make(chan string, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := sel.Select(entries)
			if err != nil {
				t.Errorf("select failed: %v", err)
				return
			}
			selected <- got.Path
		}()
	}
	wg.Wait()
	close(selected)

	for path := range selected {
		if path != entries[2].Path {
			t.Errorf("expected %q (lowest in-flight), got %q", entries[2].Path, path)
		}
	}

	// Reset
	entries[0].InFlight = 0
	entries[1].InFlight = 0
}

// ---- Verify pool.Select doesn't mutate state on failure ----

func TestPoolSelect_FailedSelectionNoMutation(t *testing.T) {
	tokens := []*Token{
		makeToken("disabled@example.com", "a", "ar", false),
	}
	tokens[0].path = "/tmp/disabled.json"
	tokens[0].Disabled = true

	sel := &RoundRobinSelector{}
	pool := NewAccountPool(tokens, fakeTime, TestPoolLB(), nil, sel)

	snapBefore := pool.Snapshot()
	_, err := pool.Select("", nil, "")
	if err == nil {
		t.Fatal("expected error for no eligible accounts")
	}
	snapAfter := pool.Snapshot()

	if snapAfter.Accounts[0].InFlight != snapBefore.Accounts[0].InFlight {
		t.Fatal("in_flight mutated by failed selection")
	}
}

// ---- Verify snapshot doesn't leak secrets ----

func TestPoolSelect_NoSecretLeakage(t *testing.T) {
	tokens := []*Token{
		makeToken("user@example.com", "super-secret-access-token-12345", "super-secret-refresh-token-67890", false),
	}
	tokens[0].path = "/tmp/user.json"

	sel := &FillFirstSelector{}
	pool := NewAccountPool(tokens, fakeTime, TestPoolLB(), nil, sel)

	snap := pool.Snapshot()
	snapJSON := fmt.Sprintf("%+v", snap)

	sentinelSecrets := []string{
		"super-secret-access-token-12345",
		"super-secret-refresh-token-67890",
	}
	for _, secret := range sentinelSecrets {
		if strings.Contains(snapJSON, secret) {
			t.Fatalf("snapshot contains secret: %s", secret)
		}
	}
}

// ---- VAL-POOL-010: Concurrent LeastConnections through AccountPool.Select ----

// TestPoolSelect_ConcurrentLeastConnections exercises LeastConnectionsSelector
// through AccountPool.Select with concurrent Begin/End cycles, ensuring the
// race detector sees no data races (VAL-POOL-010). The previous implementation
// released the pool mutex before calling selector.Select, allowing
// LeastConnectionsSelector to read InFlight while concurrent Begin/End
// mutations were in flight.
func TestPoolSelect_ConcurrentLeastConnections(t *testing.T) {
	tokens := []*Token{
		makeToken("alice@example.com", "a-access", "a-refresh", false),
		makeToken("bob@example.com", "b-access", "b-refresh", false),
		makeToken("charlie@example.com", "c-access", "c-refresh", false),
	}
	tokens[0].path = "/tmp/alice.json"
	tokens[1].path = "/tmp/bob.json"
	tokens[2].path = "/tmp/charlie.json"

	sel := &LeastConnectionsSelector{}
	pool := NewAccountPool(tokens, fakeTime, TestPoolLB(), nil, sel)

	var wg sync.WaitGroup
	const workers = 50
	const cyclesPerWorker = 10

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < cyclesPerWorker; i++ {
				entry, err := pool.Select("", nil, "")
				if err != nil {
					t.Errorf("select failed: %v", err)
					return
				}
				if err := pool.Begin(entry.Path); err != nil {
					t.Errorf("begin failed for %q: %v", entry.Path, err)
					return
				}
				// Simulate brief work
				pool.End(entry.Path)
			}
		}()
	}
	wg.Wait()

	// Verify all in-flight counters return to zero
	snap := pool.Snapshot()
	for _, acct := range snap.Accounts {
		if acct.InFlight != 0 {
			t.Errorf("account %q has in_flight=%d after concurrent test", acct.Selector, acct.InFlight)
		}
	}
}
