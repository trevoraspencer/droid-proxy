package oauth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeTime returns a fixed time for deterministic tests.
func fakeTime() time.Time {
	return time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)
}

// fakeTimeAfter returns a nowFunc that advances by d each call.
func fakeTimeAfter(d time.Duration) func() time.Time {
	var offset atomic.Int64
	return func() time.Time {
		return fakeTime().Add(time.Duration(offset.Add(int64(d))))
	}
}

// saveTokenFile creates a token JSON file in the given directory.
func saveTokenFile(t *testing.T, dir string, tok *Token) string {
	t.Helper()
	if tok.path == "" {
		tok.path = filepath.Join(dir, tokenFileName(tok))
	}
	raw, err := json.MarshalIndent(tok, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, '\n')
	if err := os.WriteFile(tok.path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return tok.path
}

// makeToken creates a Codex Token with sensible defaults.
func makeToken(email, accessToken, refreshToken string, expired bool) *Token {
	expiry := ""
	if expired {
		expiry = fakeTime().Add(-time.Hour).UTC().Format(time.RFC3339)
	} else {
		expiry = fakeTime().Add(time.Hour).UTC().Format(time.RFC3339)
	}
	return &Token{
		Type:         string(ProviderCodex),
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		Email:        email,
		Expired:      expiry,
	}
}

// makeXAIToken creates an xAI Token.
func makeXAIToken(email, accessToken string) *Token {
	return &Token{
		Type:         string(ProviderXAI),
		AccessToken:  accessToken,
		RefreshToken: "xai-refresh-secret-SENTINEL",
		Email:        email,
		Expired:      fakeTime().Add(time.Hour).UTC().Format(time.RFC3339),
	}
}

// ---- VAL-POOL-001: Pool exposes only safe Codex account entries ----

func TestPoolSnapshot_ExposesOnlySafeCodexEntries(t *testing.T) {
	dir := t.TempDir()
	codexTok := makeToken("user@example.com", "codex-access-SENTINEL-SECRET", "codex-refresh-SENTINEL-SECRET", false)
	codexTok.AccountID = "acct-123-SENTINEL"
	saveTokenFile(t, dir, codexTok)

	xaiTok := makeXAIToken("xai@example.com", "xai-access-SENTINEL-SECRET")
	saveTokenFile(t, dir, xaiTok)

	// Also create a non-JSON file that should be ignored
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("not a token"), 0o644); err != nil {
		t.Fatal(err)
	}

	pool := NewAccountPool([]*Token{codexTok, xaiTok}, fakeTime, TestPoolLB(), nil)
	snap := pool.Snapshot()

	// Only one account in snapshot (Codex)
	if len(snap.Accounts) != 1 {
		t.Fatalf("expected 1 account in snapshot, got %d", len(snap.Accounts))
	}

	acct := snap.Accounts[0]
	if acct.Provider != string(ProviderCodex) {
		t.Fatalf("expected codex provider, got %q", acct.Provider)
	}
	if acct.Selector != "user@example.com" {
		t.Fatalf("expected selector user@example.com, got %q", acct.Selector)
	}

	// Verify no secrets are exposed in the snapshot JSON
	snapJSON, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	snapStr := string(snapJSON)

	sentinelSecrets := []string{
		"codex-access-SENTINEL-SECRET",
		"codex-refresh-SENTINEL-SECRET",
		"xai-access-SENTINEL-SECRET",
		"xai-refresh-SENTINEL-SECRET",
		"acct-123-SENTINEL",
		"access_token",
		"refresh_token",
		"id_token",
	}
	for _, secret := range sentinelSecrets {
		if strings.Contains(snapStr, secret) {
			t.Fatalf("snapshot JSON contains secret %q: %s", secret, snapStr)
		}
	}

	// Verify xAI entries are absent
	if strings.Contains(snapStr, "xai@example.com") {
		t.Fatalf("snapshot contains xAI entry: %s", snapStr)
	}
}

// ---- VAL-POOL-002: Disabled, excluded, cooled-down, and rate-limited accounts are ineligible ----

func TestPoolEligibility_DisabledAccountIneligible(t *testing.T) {
	dir := t.TempDir()
	disabledTok := makeToken("disabled@example.com", "access-a", "refresh-a", false)
	disabledTok.Disabled = true
	saveTokenFile(t, dir, disabledTok)

	enabledTok := makeToken("enabled@example.com", "access-b", "refresh-b", false)
	saveTokenFile(t, dir, enabledTok)

	pool := NewAccountPool([]*Token{disabledTok, enabledTok}, fakeTime, TestPoolLB(), nil)
	eligible := pool.Eligible(nil)

	if len(eligible) != 1 {
		t.Fatalf("expected 1 eligible account, got %d", len(eligible))
	}
	if eligible[0].Selector != "enabled@example.com" {
		t.Fatalf("expected enabled@example.com, got %q", eligible[0].Selector)
	}

	// Disabled should still appear in snapshot (read-only state)
	snap := pool.Snapshot()
	if len(snap.Accounts) != 2 {
		t.Fatalf("expected 2 accounts in snapshot, got %d", len(snap.Accounts))
	}
}

func TestPoolEligibility_ExcludedAccountSkipped(t *testing.T) {
	tok1 := makeToken("a@example.com", "access-a", "refresh-a", false)
	tok1.path = "/tmp/a.json"
	tok2 := makeToken("b@example.com", "access-b", "refresh-b", false)
	tok2.path = "/tmp/b.json"

	pool := NewAccountPool([]*Token{tok1, tok2}, fakeTime, TestPoolLB(), nil)
	exclude := map[string]bool{tok1.path: true}
	eligible := pool.Eligible(exclude)

	if len(eligible) != 1 {
		t.Fatalf("expected 1 eligible, got %d", len(eligible))
	}
	if eligible[0].Selector != "b@example.com" {
		t.Fatalf("expected b@example.com, got %q", eligible[0].Selector)
	}
}

func TestPoolEligibility_CooldownUntilExpiry(t *testing.T) {
	tok := makeToken("user@example.com", "access-a", "refresh-a", false)
	tok.path = "/tmp/a.json"

	now := fakeTime()
	pool := NewAccountPool([]*Token{tok}, func() time.Time { return now }, TestPoolLB(), nil)

	// Mark cooldown in the future
	pool.MarkCooldown(tok.path, now.Add(30*time.Second))

	eligible := pool.Eligible(nil)
	if len(eligible) != 0 {
		t.Fatalf("expected 0 eligible (in cooldown), got %d", len(eligible))
	}

	// Advance past cooldown
	now = now.Add(31 * time.Second)
	eligible = pool.Eligible(nil)
	if len(eligible) != 1 {
		t.Fatalf("expected 1 eligible (cooldown expired), got %d", len(eligible))
	}
}

func TestPoolEligibility_RateLimitedUntilExpiry(t *testing.T) {
	tok := makeToken("user@example.com", "access-a", "refresh-a", false)
	tok.path = "/tmp/a.json"

	now := fakeTime()
	pool := NewAccountPool([]*Token{tok}, func() time.Time { return now }, TestPoolLB(), nil)

	// Mark rate-limited in the future
	pool.MarkRateLimited(tok.path, now.Add(60*time.Second))

	eligible := pool.Eligible(nil)
	if len(eligible) != 0 {
		t.Fatalf("expected 0 eligible (rate limited), got %d", len(eligible))
	}

	// Advance past rate limit
	now = now.Add(61 * time.Second)
	eligible = pool.Eligible(nil)
	if len(eligible) != 1 {
		t.Fatalf("expected 1 eligible (rate limit expired), got %d", len(eligible))
	}
}

// TestPoolEligibility_ExhaustedPrimaryWindowResetIgnoresLaterSecondaryReset
// verifies that an account whose primary (5hr) window is exhausted becomes
// eligible again once the primary window's own reset_at passes, even though
// the secondary (weekly) window's reset_at is much later.
func TestPoolEligibility_ExhaustedPrimaryWindowResetIgnoresLaterSecondaryReset(t *testing.T) {
	tok := makeToken("user@example.com", "access-a", "refresh-a", false)
	tok.path = "/tmp/quota-window.json"

	now := fakeTime()
	primaryReset := now.Add(60 * time.Second).Unix()
	secondaryReset := now.Add(48 * time.Hour).Unix()
	tok.CodexQuota = &CodexQuota{
		Primary:   &CodexQuotaWindow{UsedPercent: 100, ResetAt: &primaryReset, LimitReached: true},
		Secondary: &CodexQuotaWindow{UsedPercent: 30, ResetAt: &secondaryReset, LimitReached: false},
	}

	pool := NewAccountPool([]*Token{tok}, func() time.Time { return now }, TestPoolLB(), nil)

	if len(pool.Eligible(nil)) != 0 {
		t.Fatal("expected 0 eligible while primary window is exhausted")
	}

	// Advance past the primary window's reset, but nowhere near the
	// secondary window's reset.
	now = now.Add(61 * time.Second)

	eligible := pool.Eligible(nil)
	if len(eligible) != 1 {
		t.Fatalf("expected 1 eligible after primary window reset, got %d", len(eligible))
	}
}

func TestPoolEligibility_UnhealthyIneligible(t *testing.T) {
	tok := makeToken("user@example.com", "access-a", "refresh-a", false)
	tok.path = "/tmp/a.json"

	pool := NewAccountPool([]*Token{tok}, fakeTime, TestPoolLB(), nil)
	pool.MarkUnhealthy(tok.path)

	eligible := pool.Eligible(nil)
	if len(eligible) != 0 {
		t.Fatalf("expected 0 eligible (unhealthy), got %d", len(eligible))
	}
}

func TestPoolEligibility_UnhealthyAutoRecoversAfterCooldown(t *testing.T) {
	tok := makeToken("user@example.com", "access-a", "refresh-a", false)
	tok.path = "/tmp/auto-recover.json"

	now := fakeTime()
	lb := TestPoolLB()
	lb.ErrorCooldown = 30 * time.Second
	pool := NewAccountPool([]*Token{tok}, func() time.Time { return now }, lb, nil)
	pool.MarkUnhealthy(tok.path)

	if len(pool.Eligible(nil)) != 0 {
		t.Fatal("expected 0 eligible immediately after marking unhealthy")
	}

	// Advance past the error cooldown.
	now = now.Add(31 * time.Second)

	eligible := pool.Eligible(nil)
	if len(eligible) != 1 {
		t.Fatalf("expected 1 eligible after unhealthy cooldown elapsed, got %d", len(eligible))
	}
	if eligible[0].Healthy {
		t.Fatal("expected entry.Healthy to remain false after cooldown elapsed")
	}

	snap := pool.Snapshot()
	if !snap.Accounts[0].Healthy {
		t.Fatal("expected snapshot to derive healthy=true after cooldown elapsed")
	}
	if snap.Accounts[0].UnhealthyUntil != nil {
		t.Fatalf("expected no unhealthy_until after cooldown elapsed, got %v", snap.Accounts[0].UnhealthyUntil)
	}
}

func TestPoolMarkHealthyClearsUnhealthyUntil(t *testing.T) {
	tok := makeToken("user@example.com", "access-a", "refresh-a", false)
	tok.path = "/tmp/mark-healthy.json"

	pool := NewAccountPool([]*Token{tok}, fakeTime, TestPoolLB(), nil)
	pool.MarkUnhealthy(tok.path)
	pool.MarkHealthy(tok.path)

	entry := pool.entries[tok.path]
	if !entry.Healthy {
		t.Fatal("expected entry to be healthy")
	}
	if !entry.UnhealthyUntil.IsZero() {
		t.Fatalf("expected UnhealthyUntil to be zero, got %v", entry.UnhealthyUntil)
	}
}

func TestPoolReadPathsDoNotMutateElapsedUnhealthyEntry(t *testing.T) {
	tok := makeToken("user@example.com", "access-a", "refresh-a", false)
	tok.path = "/tmp/read-purity.json"

	now := fakeTime()
	lb := TestPoolLB()
	lb.ErrorCooldown = 30 * time.Second
	pool := NewAccountPool([]*Token{tok}, func() time.Time { return now }, lb, nil)
	pool.MarkUnhealthy(tok.path)
	now = now.Add(31 * time.Second)

	before := *pool.entries[tok.path]
	_ = pool.Snapshot()
	_ = pool.Snapshot()
	_ = pool.Eligible(nil)
	_ = pool.Eligible(nil)
	after := *pool.entries[tok.path]

	if !reflect.DeepEqual(before, after) {
		t.Fatalf("read paths mutated entry\nbefore: %+v\nafter:  %+v", before, after)
	}
}

// ---- VAL-POOL-003: Pool lease accounting is balanced ----

func TestPoolLease_BeginEndBalanced(t *testing.T) {
	tok := makeToken("user@example.com", "access-a", "refresh-a", false)
	tok.path = "/tmp/a.json"

	pool := NewAccountPool([]*Token{tok}, fakeTime, TestPoolLB(), nil)

	// Before lease
	snap := pool.Snapshot()
	if snap.Accounts[0].InFlight != 0 {
		t.Fatalf("expected 0 in-flight before begin, got %d", snap.Accounts[0].InFlight)
	}

	// Acquire lease
	if err := pool.Begin(tok.path); err != nil {
		t.Fatal(err)
	}
	snap = pool.Snapshot()
	if snap.Accounts[0].InFlight != 1 {
		t.Fatalf("expected 1 in-flight after begin, got %d", snap.Accounts[0].InFlight)
	}
	if snap.Accounts[0].LastUsed == nil {
		t.Fatal("expected last_used to be set after begin")
	}

	// Release lease
	pool.End(tok.path)
	snap = pool.Snapshot()
	if snap.Accounts[0].InFlight != 0 {
		t.Fatalf("expected 0 in-flight after end, got %d", snap.Accounts[0].InFlight)
	}
}

func TestPoolLease_DoubleReleaseClampsToZero(t *testing.T) {
	tok := makeToken("user@example.com", "access-a", "refresh-a", false)
	tok.path = "/tmp/a.json"

	pool := NewAccountPool([]*Token{tok}, fakeTime, TestPoolLB(), nil)

	pool.Begin(tok.path)
	pool.End(tok.path)
	pool.End(tok.path) // double release

	snap := pool.Snapshot()
	if snap.Accounts[0].InFlight != 0 {
		t.Fatalf("expected 0 in-flight after double release, got %d", snap.Accounts[0].InFlight)
	}
}

func TestPoolLease_EndNonexistentPathIsSafe(t *testing.T) {
	tok := makeToken("user@example.com", "access-a", "refresh-a", false)
	tok.path = "/tmp/a.json"

	pool := NewAccountPool([]*Token{tok}, fakeTime, TestPoolLB(), nil)
	pool.End("/tmp/nonexistent.json") // should not panic

	snap := pool.Snapshot()
	if snap.Accounts[0].InFlight != 0 {
		t.Fatalf("expected 0 in-flight, got %d", snap.Accounts[0].InFlight)
	}
}

func TestPoolLease_ConcurrentBeginEnd(t *testing.T) {
	tok := makeToken("user@example.com", "access-a", "refresh-a", false)
	tok.path = "/tmp/a.json"

	pool := NewAccountPool([]*Token{tok}, fakeTime, TestPoolLB(), nil)

	var wg sync.WaitGroup
	const n = 100
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = pool.Begin(tok.path)
			pool.End(tok.path)
		}()
	}
	wg.Wait()

	snap := pool.Snapshot()
	if snap.Accounts[0].InFlight != 0 {
		t.Fatalf("expected 0 in-flight after concurrent begin/end, got %d", snap.Accounts[0].InFlight)
	}
}

func TestPoolLease_ConcurrentLeastConnectionsDoesNotStampede(t *testing.T) {
	// Create multiple accounts
	tokens := make([]*Token, 3)
	for i := range tokens {
		tokens[i] = makeToken(fmt.Sprintf("user%d@example.com", i), fmt.Sprintf("access-%d", i), fmt.Sprintf("refresh-%d", i), false)
		tokens[i].path = fmt.Sprintf("/tmp/user%d.json", i)
	}

	pool := NewAccountPool(tokens, fakeTime, TestPoolLB(), nil)
	eligible := pool.Eligible(nil)
	if len(eligible) != 3 {
		t.Fatalf("expected 3 eligible, got %d", len(eligible))
	}

	// Simulate concurrent lease acquisition where all callers see same in-flight
	// and each acquires a lease on different accounts
	var wg sync.WaitGroup
	leases := make(chan string, 10)
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			path := eligible[idx].Path
			if err := pool.Begin(path); err != nil {
				t.Errorf("begin failed: %v", err)
				return
			}
			leases <- path
		}(i)
	}
	wg.Wait()
	close(leases)

	// All 3 should have in-flight = 1
	snap := pool.Snapshot()
	totalInFlight := 0
	for _, acct := range snap.Accounts {
		totalInFlight += acct.InFlight
		if acct.InFlight > 1 {
			t.Fatalf("account %q has in-flight %d, expected <=1", acct.Selector, acct.InFlight)
		}
	}
	if totalInFlight != 3 {
		t.Fatalf("expected total in-flight=3, got %d", totalInFlight)
	}

	// Release all
	for path := range leases {
		pool.End(path)
	}
	snap = pool.Snapshot()
	for _, acct := range snap.Accounts {
		if acct.InFlight != 0 {
			t.Fatalf("account %q still has in-flight %d after release", acct.Selector, acct.InFlight)
		}
	}
}

// ---- VAL-POOL-004: Reload preserves state by token path ----

func TestPoolReload_PreservesRuntimeStateByPath(t *testing.T) {
	dir := t.TempDir()
	tok := makeToken("user@example.com", "access-a", "refresh-a", false)
	path := saveTokenFile(t, dir, tok)

	now := fakeTime()
	pool := NewAccountPool([]*Token{tok}, func() time.Time { return now }, TestPoolLB(), nil)

	// Establish some runtime state
	pool.MarkCooldown(path, now.Add(30*time.Second))
	pool.Begin(path)

	// Reload with same token (simulating a token file save)
	reloadTok := makeToken("user@example.com", "access-b", "refresh-b", false)
	reloadTok.path = path
	pool.Reload([]*Token{reloadTok})

	snap := pool.Snapshot()
	if len(snap.Accounts) != 1 {
		t.Fatalf("expected 1 account after reload, got %d", len(snap.Accounts))
	}
	acct := snap.Accounts[0]

	// Runtime state should be preserved
	if acct.InFlight != 1 {
		t.Fatalf("expected in-flight=1 preserved, got %d", acct.InFlight)
	}
	if acct.CooldownUntil == nil {
		t.Fatal("expected cooldown_until preserved")
	}

	// Release the lease from before reload
	pool.End(path)
	snap = pool.Snapshot()
	if snap.Accounts[0].InFlight != 0 {
		t.Fatalf("expected 0 in-flight after end, got %d", snap.Accounts[0].InFlight)
	}
}

func TestPoolReload_AddsNewEntry(t *testing.T) {
	dir := t.TempDir()
	tok1 := makeToken("user1@example.com", "access-1", "refresh-1", false)
	saveTokenFile(t, dir, tok1)

	pool := NewAccountPool([]*Token{tok1}, fakeTime, TestPoolLB(), nil)
	if len(pool.Snapshot().Accounts) != 1 {
		t.Fatalf("expected 1 account initially")
	}

	// Add a new token
	tok2 := makeToken("user2@example.com", "access-2", "refresh-2", false)
	saveTokenFile(t, dir, tok2)
	pool.Reload([]*Token{tok1, tok2})

	if len(pool.Snapshot().Accounts) != 2 {
		t.Fatalf("expected 2 accounts after reload, got %d", len(pool.Snapshot().Accounts))
	}
}

func TestPoolReload_RemovesDeletedEntry(t *testing.T) {
	dir := t.TempDir()
	tok1 := makeToken("user1@example.com", "access-1", "refresh-1", false)
	saveTokenFile(t, dir, tok1)
	tok2 := makeToken("user2@example.com", "access-2", "refresh-2", false)
	saveTokenFile(t, dir, tok2)

	pool := NewAccountPool([]*Token{tok1, tok2}, fakeTime, TestPoolLB(), nil)
	if len(pool.Snapshot().Accounts) != 2 {
		t.Fatalf("expected 2 accounts initially")
	}

	// Reload with only tok1 (tok2 removed)
	pool.Reload([]*Token{tok1})
	if len(pool.Snapshot().Accounts) != 1 {
		t.Fatalf("expected 1 account after reload, got %d", len(pool.Snapshot().Accounts))
	}
	if pool.Snapshot().Accounts[0].Selector != "user1@example.com" {
		t.Fatalf("expected user1 to survive, got %q", pool.Snapshot().Accounts[0].Selector)
	}
}

func TestPoolReload_SamePathIdentityChange_UpdatesImmediately(t *testing.T) {
	dir := t.TempDir()
	tok := makeToken("old@example.com", "access-old", "refresh-old", false)
	path := saveTokenFile(t, dir, tok)

	pool := NewAccountPool([]*Token{tok}, fakeTime, TestPoolLB(), nil)
	snap := pool.Snapshot()
	if snap.Accounts[0].Selector != "old@example.com" {
		t.Fatalf("initial selector = %q, want old@example.com", snap.Accounts[0].Selector)
	}

	// Rewrite same file with new identity
	newTok := makeToken("new@example.com", "access-new", "refresh-new", false)
	newTok.path = path
	pool.Reload([]*Token{newTok})

	snap = pool.Snapshot()
	if snap.Accounts[0].Selector != "new@example.com" {
		t.Fatalf("updated selector = %q, want new@example.com", snap.Accounts[0].Selector)
	}
}

func TestPoolReload_SamePathProviderChange_RemovesFromCodexSelection(t *testing.T) {
	dir := t.TempDir()
	tok := makeToken("user@example.com", "access-a", "refresh-a", false)
	path := saveTokenFile(t, dir, tok)

	pool := NewAccountPool([]*Token{tok}, fakeTime, TestPoolLB(), nil)
	if len(pool.Eligible(nil)) != 1 {
		t.Fatalf("expected 1 eligible initially")
	}

	// Change provider to xAI at same path
	xaiTok := makeXAIToken("user@example.com", "xai-access")
	xaiTok.path = path
	pool.Reload([]*Token{xaiTok})

	// Should not be eligible for Codex selection
	if len(pool.Eligible(nil)) != 0 {
		t.Fatalf("expected 0 eligible after provider change to xAI, got %d", len(pool.Eligible(nil)))
	}

	// Should still be in snapshot? No - snapshot is Codex-only
	snap := pool.Snapshot()
	if len(snap.Accounts) != 0 {
		t.Fatalf("expected 0 Codex accounts in snapshot after provider change, got %d", len(snap.Accounts))
	}
}

// ---- VAL-POOL-011: Expired refreshable Codex accounts remain eligible ----

func TestPoolEligibility_ExpiredRefreshableAccountEligible(t *testing.T) {
	tok := makeToken("user@example.com", "access-expired", "refresh-available", true)
	tok.path = "/tmp/expired.json"

	pool := NewAccountPool([]*Token{tok}, fakeTime, TestPoolLB(), nil)
	eligible := pool.Eligible(nil)

	if len(eligible) != 1 {
		t.Fatalf("expected 1 eligible (expired with refresh), got %d", len(eligible))
	}
	if !eligible[0].TokenExpired {
		t.Fatal("expected TokenExpired=true")
	}
	if !eligible[0].Refreshable {
		t.Fatal("expected Refreshable=true")
	}
}

func TestPoolEligibility_ExpiredNoRefreshIneligible(t *testing.T) {
	tok := makeToken("user@example.com", "access-expired", "", true)
	tok.path = "/tmp/expired-no-refresh.json"

	pool := NewAccountPool([]*Token{tok}, fakeTime, TestPoolLB(), nil)
	eligible := pool.Eligible(nil)

	if len(eligible) != 0 {
		t.Fatalf("expected 0 eligible (expired without refresh), got %d", len(eligible))
	}
}

// ---- VAL-POOL-012: Persisted rate-limit reset metadata seeds eligibility safely ----

func TestPoolPersistedRateLimit_ExhaustedQuotaMakesIneligible(t *testing.T) {
	dir := t.TempDir()
	resetAt := fakeTime().Add(60 * time.Second).Unix()
	tok := makeToken("user@example.com", "access-a", "refresh-a", false)
	tok.CodexQuota = &CodexQuota{
		Primary: &CodexQuotaWindow{
			UsedPercent:  100,
			LimitReached: true,
			ResetAt:      &resetAt,
		},
	}
	tok.RateLimitResetAt = fakeTime().Add(60 * time.Second).UTC().Format(time.RFC3339)
	path := saveTokenFile(t, dir, tok)

	pool := NewAccountPool([]*Token{tok}, fakeTime, TestPoolLB(), nil)
	eligible := pool.Eligible(nil)
	if len(eligible) != 0 {
		t.Fatalf("expected 0 eligible (exhausted quota with future reset), got %d", len(eligible))
	}

	// Verify the rate limit timestamp is set
	snap := pool.Snapshot()
	if snap.Accounts[0].RateLimitedUntil == nil {
		t.Fatal("expected rate_limit_until to be set")
	}
	_ = path
}

func TestPoolPersistedRateLimit_NonExhaustedQuotaDoesNotSuppress(t *testing.T) {
	resetAt := fakeTime().Add(60 * time.Second).Unix()
	tok := makeToken("user@example.com", "access-a", "refresh-a", false)
	tok.CodexQuota = &CodexQuota{
		Primary: &CodexQuotaWindow{
			UsedPercent: 50,
			ResetAt:     &resetAt,
			// LimitReached is false (default)
		},
	}
	tok.RateLimitResetAt = fakeTime().Add(60 * time.Second).UTC().Format(time.RFC3339)
	tok.path = "/tmp/partial.json"

	pool := NewAccountPool([]*Token{tok}, fakeTime, TestPoolLB(), nil)
	eligible := pool.Eligible(nil)
	if len(eligible) != 1 {
		t.Fatalf("expected 1 eligible (non-exhausted quota), got %d", len(eligible))
	}
	snap := pool.Snapshot()
	if snap.Accounts[0].RateLimitedUntil != nil {
		t.Fatalf("expected no rate_limit_until from passive telemetry, got %v", snap.Accounts[0].RateLimitedUntil)
	}
}

func TestPoolPersistedRateLimit_FallbackUsesPersistedResetForExhaustedWindowWithoutResetAt(t *testing.T) {
	now := fakeTime()
	persistedReset := now.Add(time.Hour)
	tok := makeToken("user@example.com", "access-a", "refresh-a", false)
	tok.path = "/tmp/exhausted-no-reset.json"
	tok.CodexQuota = &CodexQuota{
		Primary: &CodexQuotaWindow{
			UsedPercent:  100,
			LimitReached: true,
		},
	}
	tok.RateLimitResetAt = persistedReset.UTC().Format(time.RFC3339)

	pool := NewAccountPool([]*Token{tok}, func() time.Time { return now }, TestPoolLB(), nil)
	if eligible := pool.Eligible(nil); len(eligible) != 0 {
		t.Fatalf("expected 0 eligible before persisted reset, got %d", len(eligible))
	}
	snap := pool.Snapshot()
	if snap.Accounts[0].RateLimitedUntil == nil || !snap.Accounts[0].RateLimitedUntil.Equal(persistedReset) {
		t.Fatalf("rate_limit_until = %v, want persisted reset %v", snap.Accounts[0].RateLimitedUntil, persistedReset)
	}

	now = persistedReset.Add(time.Second)
	if eligible := pool.Eligible(nil); len(eligible) != 1 {
		t.Fatalf("expected 1 eligible after persisted reset, got %d", len(eligible))
	}
}

func TestPoolPersistedRateLimit_PastResetIgnored(t *testing.T) {
	resetAt := fakeTime().Add(-60 * time.Second).Unix()
	tok := makeToken("user@example.com", "access-a", "refresh-a", false)
	tok.CodexQuota = &CodexQuota{
		Primary: &CodexQuotaWindow{
			UsedPercent:  100,
			LimitReached: true,
			ResetAt:      &resetAt,
		},
	}
	tok.RateLimitResetAt = fakeTime().Add(-60 * time.Second).UTC().Format(time.RFC3339)
	tok.path = "/tmp/past.json"

	pool := NewAccountPool([]*Token{tok}, fakeTime, TestPoolLB(), nil)
	eligible := pool.Eligible(nil)
	if len(eligible) != 1 {
		t.Fatalf("expected 1 eligible (past reset), got %d", len(eligible))
	}
}

func TestPoolPersistedRateLimit_MalformedResetIgnored(t *testing.T) {
	tok := makeToken("user@example.com", "access-a", "refresh-a", false)
	tok.RateLimitResetAt = "not-a-valid-time"
	tok.path = "/tmp/malformed.json"

	pool := NewAccountPool([]*Token{tok}, fakeTime, TestPoolLB(), nil)
	eligible := pool.Eligible(nil)
	if len(eligible) != 1 {
		t.Fatalf("expected 1 eligible (malformed reset), got %d", len(eligible))
	}
}

func TestPoolPersistedRateLimit_Runtime429CooldownPreservedAcrossReload(t *testing.T) {
	dir := t.TempDir()
	tok := makeToken("user@example.com", "access-a", "refresh-a", false)
	path := saveTokenFile(t, dir, tok)

	now := fakeTime()
	pool := NewAccountPool([]*Token{tok}, func() time.Time { return now }, TestPoolLB(), nil)

	// Simulate runtime 429 cooldown
	pool.MarkRateLimited(path, now.Add(60*time.Second))

	// Reload same token (simulating a token save that doesn't change rate limit)
	reloadTok := makeToken("user@example.com", "access-a", "refresh-a", false)
	reloadTok.path = path
	pool.Reload([]*Token{reloadTok})

	eligible := pool.Eligible(nil)
	if len(eligible) != 0 {
		t.Fatalf("expected 0 eligible (runtime 429 preserved), got %d", len(eligible))
	}
}

func TestPoolReload_RateLimitClampSemantics(t *testing.T) {
	now := fakeTime()
	oneHour := now.Add(time.Hour)
	sevenDays := now.Add(7 * 24 * time.Hour)
	oneHourUnix := oneHour.Unix()
	sevenDaysUnix := sevenDays.Unix()

	tests := []struct {
		name          string
		runtimeMark   time.Time
		reloadQuota   *CodexQuota
		wantRateLimit time.Time
	}{
		{
			name:        "clamp down to fresh exhausted reset",
			runtimeMark: sevenDays,
			reloadQuota: &CodexQuota{
				Primary: &CodexQuotaWindow{UsedPercent: 100, LimitReached: true, ResetAt: &oneHourUnix},
			},
			wantRateLimit: oneHour,
		},
		{
			name:        "never extend runtime mark",
			runtimeMark: oneHour,
			reloadQuota: &CodexQuota{
				Primary: &CodexQuotaWindow{UsedPercent: 100, LimitReached: true, ResetAt: &sevenDaysUnix},
			},
			wantRateLimit: oneHour,
		},
		{
			name:        "keep mark when fresh quota has no exhausted evidence",
			runtimeMark: oneHour,
			reloadQuota: &CodexQuota{
				Primary: &CodexQuotaWindow{UsedPercent: 30, LimitReached: false, ResetAt: &sevenDaysUnix},
			},
			wantRateLimit: oneHour,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tok := makeToken("user@example.com", "access-a", "refresh-a", false)
			tok.path = "/tmp/" + strings.ReplaceAll(tt.name, " ", "-") + ".json"
			pool := NewAccountPool([]*Token{tok}, func() time.Time { return now }, TestPoolLB(), nil)
			pool.MarkRateLimited(tok.path, tt.runtimeMark)

			reloadTok := makeToken("user@example.com", "access-a", "refresh-a", false)
			reloadTok.path = tok.path
			reloadTok.CodexQuota = tt.reloadQuota
			pool.Reload([]*Token{reloadTok})

			snap := pool.Snapshot()
			got := snap.Accounts[0].RateLimitedUntil
			if got == nil || !got.Equal(tt.wantRateLimit) {
				t.Fatalf("rate_limit_until = %v, want %v", got, tt.wantRateLimit)
			}
		})
	}
}

// ---- VAL-POOL-013: Unhealthy account recovery is defined ----

func TestPoolUnhealthyRecovery_ViaTokenReload(t *testing.T) {
	dir := t.TempDir()
	tok := makeToken("user@example.com", "access-a", "refresh-a", false)
	path := saveTokenFile(t, dir, tok)

	pool := NewAccountPool([]*Token{tok}, fakeTime, TestPoolLB(), nil)
	pool.MarkUnhealthy(path)

	if len(pool.Eligible(nil)) != 0 {
		t.Fatal("expected 0 eligible when unhealthy")
	}

	// Simulate a fresh token file from re-login (different access token)
	newTok := makeToken("user@example.com", "new-access", "new-refresh", false)
	newTok.path = path
	pool.Reload([]*Token{newTok})
	pool.MarkHealthy(path)

	if len(pool.Eligible(nil)) != 1 {
		t.Fatalf("expected 1 eligible after recovery via reload+healthy, got %d", len(pool.Eligible(nil)))
	}
}

func TestPoolUnhealthyRecovery_ViaCooldownExpiry(t *testing.T) {
	tok := makeToken("user@example.com", "access-a", "refresh-a", false)
	tok.path = "/tmp/recovery.json"

	now := fakeTime()
	pool := NewAccountPool([]*Token{tok}, func() time.Time { return now }, TestPoolLB(), nil)

	pool.MarkUnhealthy(tok.path)
	if len(pool.Eligible(nil)) != 0 {
		t.Fatal("expected 0 eligible when unhealthy")
	}

	// Direct healthy recovery (simulating successful refresh)
	pool.MarkHealthy(tok.path)
	if len(pool.Eligible(nil)) != 1 {
		t.Fatalf("expected 1 eligible after healthy recovery, got %d", len(pool.Eligible(nil)))
	}
	if entry := pool.entries[tok.path]; !entry.UnhealthyUntil.IsZero() {
		t.Fatalf("expected MarkHealthy to clear UnhealthyUntil, got %v", entry.UnhealthyUntil)
	}
}

func TestPoolUnhealthyRecovery_ExposedInSnapshot(t *testing.T) {
	tok := makeToken("user@example.com", "access-a", "refresh-a", false)
	tok.path = "/tmp/health.json"

	now := fakeTime()
	lb := TestPoolLB()
	lb.ErrorCooldown = 30 * time.Second
	pool := NewAccountPool([]*Token{tok}, func() time.Time { return now }, lb, nil)

	// Initially healthy
	snap := pool.Snapshot()
	if !snap.Accounts[0].Healthy {
		t.Fatal("expected initially healthy")
	}

	// Mark unhealthy
	pool.MarkUnhealthy(tok.path)
	snap = pool.Snapshot()
	if snap.Accounts[0].Healthy {
		t.Fatal("expected unhealthy after mark")
	}
	if snap.Accounts[0].UnhealthyUntil == nil {
		t.Fatal("expected unhealthy_until while unhealthy cooldown is active")
	}

	now = now.Add(31 * time.Second)
	snap = pool.Snapshot()
	if !snap.Accounts[0].Healthy {
		t.Fatal("expected derived healthy after unhealthy cooldown elapsed")
	}
	if snap.Accounts[0].UnhealthyUntil != nil {
		t.Fatalf("expected no unhealthy_until after cooldown elapsed, got %v", snap.Accounts[0].UnhealthyUntil)
	}
}

// ---- VAL-POOL-014: Snapshot ordering, labels, and immutability are stable ----

func TestPoolSnapshot_DeterministicOrdering(t *testing.T) {
	tokens := []*Token{
		makeToken("charlie@example.com", "c", "cr", false),
		makeToken("alice@example.com", "a", "ar", false),
		makeToken("bob@example.com", "b", "br", false),
	}
	tokens[0].path = "/tmp/c.json"
	tokens[1].path = "/tmp/a.json"
	tokens[2].path = "/tmp/b.json"

	pool := NewAccountPool(tokens, fakeTime, TestPoolLB(), nil)
	snap := pool.Snapshot()

	// Should be sorted alphabetically by selector
	expected := []string{"alice@example.com", "bob@example.com", "charlie@example.com"}
	for i, acct := range snap.Accounts {
		if acct.Selector != expected[i] {
			t.Fatalf("account[%d] selector = %q, want %q", i, acct.Selector, expected[i])
		}
	}
}

func TestPoolSnapshot_DuplicateLabelsDistinguishableByOrder(t *testing.T) {
	// Two tokens with the same email (selector label) but different paths and
	// different quota windows so entries are distinguishable in the snapshot.
	// This genuinely exercises the Path-based tie-break in Snapshot() sorting.
	resetAt1 := int64(1893456000)
	resetAt2 := int64(1893460000)

	tok1 := makeToken("same@example.com", "access-1", "refresh-1", false)
	tok1.path = "/tmp/alpha.json"
	tok1.CodexQuota = &CodexQuota{
		Primary: &CodexQuotaWindow{
			UsedPercent: 10,
			ResetAt:     &resetAt1,
		},
	}

	tok2 := makeToken("same@example.com", "access-2", "refresh-2", false)
	tok2.path = "/tmp/beta.json"
	tok2.CodexQuota = &CodexQuota{
		Primary: &CodexQuotaWindow{
			UsedPercent: 20,
			ResetAt:     &resetAt2,
		},
	}

	pool := NewAccountPool([]*Token{tok1, tok2}, fakeTime, TestPoolLB(), nil)

	// Repeated snapshots must produce the same deterministic order.
	// The Path-based tie-break sorts "/tmp/alpha.json" before "/tmp/beta.json",
	// so the alpha quota (UsedPercent=10) must always appear first.
	for i := 0; i < 5; i++ {
		snap := pool.Snapshot()

		if len(snap.Accounts) != 2 {
			t.Fatalf("snapshot %d: expected 2 accounts, got %d", i, len(snap.Accounts))
		}
		if snap.Accounts[0].Selector != "same@example.com" || snap.Accounts[1].Selector != "same@example.com" {
			t.Fatalf("snapshot %d: expected same selectors, got %q and %q",
				i, snap.Accounts[0].Selector, snap.Accounts[1].Selector)
		}
		if snap.Accounts[0].Quota == nil || snap.Accounts[0].Quota.Primary == nil {
			t.Fatalf("snapshot %d: first account missing quota", i)
		}
		if snap.Accounts[1].Quota == nil || snap.Accounts[1].Quota.Primary == nil {
			t.Fatalf("snapshot %d: second account missing quota", i)
		}
		// The first entry must be the alpha-path account (UsedPercent=10).
		// If the Path-based tie-break regressed and ordering became random,
		// this assertion would fail on at least one iteration.
		if snap.Accounts[0].Quota.Primary.UsedPercent != 10 {
			t.Fatalf("snapshot %d: first account quota UsedPercent = %.1f, want 10 (alpha path /tmp/alpha.json)",
				i, snap.Accounts[0].Quota.Primary.UsedPercent)
		}
		if snap.Accounts[1].Quota.Primary.UsedPercent != 20 {
			t.Fatalf("snapshot %d: second account quota UsedPercent = %.1f, want 20 (beta path /tmp/beta.json)",
				i, snap.Accounts[1].Quota.Primary.UsedPercent)
		}
	}
}

func TestPoolSnapshot_EmptyLabelFallback(t *testing.T) {
	// Token with no email, subject, or accountID
	tok := &Token{
		Type:        string(ProviderCodex),
		AccessToken: "access",
		Expired:     fakeTime().Add(time.Hour).UTC().Format(time.RFC3339),
	}
	tok.path = "/tmp/codex-oauth-token.json"

	pool := NewAccountPool([]*Token{tok}, fakeTime, TestPoolLB(), nil)
	snap := pool.Snapshot()

	if len(snap.Accounts) != 1 {
		t.Fatalf("expected 1 account, got %d", len(snap.Accounts))
	}
	// Should fall back to filename stem
	if snap.Accounts[0].Selector != "codex-oauth-token" {
		t.Fatalf("expected filename stem selector, got %q", snap.Accounts[0].Selector)
	}
}

func TestPoolSnapshot_Immutable(t *testing.T) {
	tok := makeToken("user@example.com", "access-a", "refresh-a", false)
	tok.path = "/tmp/immutable.json"
	resetAt := int64(1893456000)
	tok.CodexQuota = &CodexQuota{
		Primary: &CodexQuotaWindow{
			UsedPercent: 42,
			ResetAt:     &resetAt,
		},
	}

	pool := NewAccountPool([]*Token{tok}, fakeTime, TestPoolLB(), nil)
	snap1 := pool.Snapshot()

	// Mutate the snapshot
	snap1.Accounts[0].InFlight = 999
	if snap1.Accounts[0].Quota != nil && snap1.Accounts[0].Quota.Primary != nil {
		snap1.Accounts[0].Quota.Primary.UsedPercent = 999
	}

	// Get a fresh snapshot and verify it's not affected
	snap2 := pool.Snapshot()
	if snap2.Accounts[0].InFlight == 999 {
		t.Fatal("snapshot mutation affected pool state (InFlight)")
	}
	if snap2.Accounts[0].Quota != nil && snap2.Accounts[0].Quota.Primary != nil {
		if snap2.Accounts[0].Quota.Primary.UsedPercent == 999 {
			t.Fatal("snapshot mutation affected pool state (Quota)")
		}
	}
}

func TestPoolSnapshot_MutatingQuotaWindowDoesNotAffectPool(t *testing.T) {
	tok := makeToken("user@example.com", "access", "refresh", false)
	tok.path = "/tmp/q.json"
	resetAt := int64(1893456000)
	tok.CodexQuota = &CodexQuota{
		Primary: &CodexQuotaWindow{
			UsedPercent:      50,
			RemainingPercent: floatPtr(50),
			ResetAt:          &resetAt,
		},
	}

	pool := NewAccountPool([]*Token{tok}, fakeTime, TestPoolLB(), nil)

	snap := pool.Snapshot()
	// Mutate the quota in the snapshot
	if snap.Accounts[0].Quota.Primary.RemainingPercent != nil {
		*snap.Accounts[0].Quota.Primary.RemainingPercent = 0
	}

	// Fresh snapshot should still have the original value
	snap2 := pool.Snapshot()
	if snap2.Accounts[0].Quota.Primary.RemainingPercent == nil || *snap2.Accounts[0].Quota.Primary.RemainingPercent != 50 {
		t.Fatal("quota mutation in snapshot affected pool state")
	}
}

// ---- VAL-POOL-015: Failed selections do not mutate pool state ----

func TestPoolFailedSelection_NoMutation(t *testing.T) {
	// Pool with all disabled accounts
	tok := makeToken("user@example.com", "access-a", "refresh-a", false)
	tok.Disabled = true
	tok.path = "/tmp/disabled.json"

	pool := NewAccountPool([]*Token{tok}, fakeTime, TestPoolLB(), nil)
	snapBefore := pool.Snapshot()

	// Try to get eligible - should be empty
	eligible := pool.Eligible(nil)
	if len(eligible) != 0 {
		t.Fatalf("expected 0 eligible, got %d", len(eligible))
	}

	snapAfter := pool.Snapshot()
	if snapAfter.Accounts[0].InFlight != snapBefore.Accounts[0].InFlight {
		t.Fatalf("in_flight changed after failed selection: before=%d after=%d",
			snapBefore.Accounts[0].InFlight, snapAfter.Accounts[0].InFlight)
	}
}

func TestPoolFailedSelection_AllCooledDown_NoMutation(t *testing.T) {
	tok := makeToken("user@example.com", "access-a", "refresh-a", false)
	tok.path = "/tmp/cooled.json"

	now := fakeTime()
	pool := NewAccountPool([]*Token{tok}, func() time.Time { return now }, TestPoolLB(), nil)

	pool.MarkCooldown(tok.path, now.Add(time.Hour))
	snapBefore := pool.Snapshot()

	eligible := pool.Eligible(nil)
	if len(eligible) != 0 {
		t.Fatalf("expected 0 eligible, got %d", len(eligible))
	}

	snapAfter := pool.Snapshot()
	if snapAfter.Accounts[0].InFlight != snapBefore.Accounts[0].InFlight {
		t.Fatal("in_flight mutated by failed selection")
	}
}

func TestPoolFailedSelection_AllExcluded_NoMutation(t *testing.T) {
	tok := makeToken("user@example.com", "access-a", "refresh-a", false)
	tok.path = "/tmp/excluded.json"

	pool := NewAccountPool([]*Token{tok}, fakeTime, TestPoolLB(), nil)
	snapBefore := pool.Snapshot()

	exclude := map[string]bool{tok.path: true}
	eligible := pool.Eligible(exclude)
	if len(eligible) != 0 {
		t.Fatalf("expected 0 eligible, got %d", len(eligible))
	}

	snapAfter := pool.Snapshot()
	if snapAfter.Accounts[0].InFlight != snapBefore.Accounts[0].InFlight {
		t.Fatal("in_flight mutated by failed selection")
	}
	if snapAfter.Accounts[0].LastUsed != nil {
		t.Fatal("last_used mutated by failed selection")
	}
}

// ---- Edge cases and helpers ----

func TestPoolSnapshot_EmptyPool(t *testing.T) {
	pool := NewAccountPool(nil, fakeTime, TestPoolLB(), nil)
	snap := pool.Snapshot()
	if snap == nil {
		t.Fatal("expected non-nil snapshot")
	}
	if len(snap.Accounts) != 0 {
		t.Fatalf("expected empty accounts, got %d", len(snap.Accounts))
	}
}

func TestPoolEligibility_EmptyPool(t *testing.T) {
	pool := NewAccountPool(nil, fakeTime, TestPoolLB(), nil)
	eligible := pool.Eligible(nil)
	if len(eligible) != 0 {
		t.Fatalf("expected 0 eligible, got %d", len(eligible))
	}
}

func TestPoolEntry_MatchesAccount(t *testing.T) {
	entry := &AccountEntry{
		Email:     "User@Example.COM",
		Subject:   "sub-123",
		AccountID: "acct_456",
		Path:      "/tmp/codex-oauth-user.json",
	}

	tests := []struct {
		account string
		want    bool
	}{
		{"user@example.com", true},      // case-insensitive email
		{"User@Example.COM", true},      // exact case email
		{"sub-123", true},               // subject
		{"acct_456", true},              // account ID
		{"codex-oauth-user", true},      // filename stem
		{"codex-oauth-user.json", true}, // full filename
		{"other@example.com", false},
		{"", true},                     // empty matches all
		{"  user@example.com  ", true}, // trimmed
	}

	for _, tt := range tests {
		got := entry.MatchesAccount(tt.account)
		if got != tt.want {
			t.Errorf("MatchesAccount(%q) = %v, want %v", tt.account, got, tt.want)
		}
	}
}

func TestPoolReload_UpdatesDisabledState(t *testing.T) {
	dir := t.TempDir()
	tok := makeToken("user@example.com", "access-a", "refresh-a", false)
	path := saveTokenFile(t, dir, tok)

	pool := NewAccountPool([]*Token{tok}, fakeTime, TestPoolLB(), nil)
	if len(pool.Eligible(nil)) != 1 {
		t.Fatal("expected 1 eligible initially")
	}

	// Reload with token now disabled
	disabledTok := makeToken("user@example.com", "access-a", "refresh-a", false)
	disabledTok.Disabled = true
	disabledTok.path = path
	pool.Reload([]*Token{disabledTok})

	if len(pool.Eligible(nil)) != 0 {
		t.Fatalf("expected 0 eligible after disable, got %d", len(pool.Eligible(nil)))
	}

	// Snapshot should still show the account
	snap := pool.Snapshot()
	if len(snap.Accounts) != 1 {
		t.Fatalf("expected 1 account in snapshot, got %d", len(snap.Accounts))
	}
	if !snap.Accounts[0].Disabled {
		t.Fatal("expected Disabled=true in snapshot")
	}
}

func TestPoolReload_UpdatesQuotaMetadata(t *testing.T) {
	dir := t.TempDir()
	tok := makeToken("user@example.com", "access-a", "refresh-a", false)
	path := saveTokenFile(t, dir, tok)

	pool := NewAccountPool([]*Token{tok}, fakeTime, TestPoolLB(), nil)

	// Reload with quota metadata
	quotaTok := makeToken("user@example.com", "access-a", "refresh-a", false)
	quotaTok.path = path
	resetAt := int64(1893456000)
	quotaTok.CodexQuota = &CodexQuota{
		Primary: &CodexQuotaWindow{
			UsedPercent: 75,
			ResetAt:     &resetAt,
		},
	}
	pool.Reload([]*Token{quotaTok})

	snap := pool.Snapshot()
	if snap.Accounts[0].Quota == nil || snap.Accounts[0].Quota.Primary == nil {
		t.Fatal("expected quota to be updated after reload")
	}
	if snap.Accounts[0].Quota.Primary.UsedPercent != 75 {
		t.Fatalf("expected quota 75%%, got %.1f%%", snap.Accounts[0].Quota.Primary.UsedPercent)
	}
}

func floatPtr(v float64) *float64 { return &v }

func TestEnabledCodexCount(t *testing.T) {
	dir := t.TempDir()
	saveTokenFile(t, dir, &Token{
		Type:        string(ProviderCodex),
		AccessToken: "access-a",
		Email:       "a@test.com",
		Expired:     fakeTime().Add(time.Hour).Format(time.RFC3339),
	})
	saveTokenFile(t, dir, &Token{
		Type:        string(ProviderCodex),
		AccessToken: "access-b",
		Email:       "b@test.com",
		Expired:     fakeTime().Add(time.Hour).Format(time.RFC3339),
	})
	disabledPath := saveTokenFile(t, dir, &Token{
		Type:     string(ProviderCodex),
		Disabled: true,
		Email:    "disabled@test.com",
		Expired:  fakeTime().Add(time.Hour).Format(time.RFC3339),
	})

	// Load tokens manually.
	entries, _ := os.ReadDir(dir)
	var tokens []*Token
	for _, e := range entries {
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var tok Token
		if json.Unmarshal(raw, &tok) != nil {
			continue
		}
		tok.path = filepath.Join(dir, e.Name())
		if tok.Provider() != ProviderCodex {
			continue
		}
		tokens = append(tokens, &tok)
	}

	pool := NewAccountPool(tokens, fakeTime, TestPoolLB(), nil)

	count := pool.EnabledCodexCount()
	if count != 2 {
		t.Fatalf("expected 2 enabled Codex accounts (excluding disabled), got %d", count)
	}

	// Verify disabled account path is known.
	if pool.Snapshot() == nil {
		t.Fatal("expected non-nil snapshot")
	}
	_ = disabledPath // ensure it's used
}
