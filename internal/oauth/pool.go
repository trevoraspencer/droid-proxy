package oauth

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"droid-proxy/internal/config"
)

// ErrNoEligibleAccounts is returned when no accounts are eligible for selection.
var ErrNoEligibleAccounts = errors.New("no eligible codex accounts available")

// AccountEntry tracks the runtime state of a single Codex account in the pool.
// It is keyed by token file path and never exposes token secrets.
type AccountEntry struct {
	// Identity fields (updated on reload)
	Path     string
	Selector string // safe display label: email > subject > accountID > filename stem
	Provider config.OAuthProvider
	Disabled bool

	// Runtime state (preserved across reloads for same path)
	Healthy          bool
	CooldownUntil    time.Time
	RateLimitedUntil time.Time
	LastUsed         time.Time
	InFlight         int

	// Quota/metadata from token file (updated on reload)
	Quota            *CodexQuota
	RateLimitResetAt *time.Time
	HasRefreshToken  bool
	TokenExpired     bool
	Refreshable      bool // expired but has refresh token

	// Identity detail fields (updated on reload, used for MatchesAccount)
	Email     string
	Subject   string
	AccountID string
}

// AccountSnapshot is a safe, immutable deep-copy view of an account entry.
// It never exposes access tokens, refresh tokens, ID tokens, or raw token JSON.
type AccountSnapshot struct {
	Selector               string      `json:"selector"`
	Provider               string      `json:"provider"`
	Disabled               bool        `json:"disabled"`
	Healthy                bool        `json:"healthy"`
	CooldownUntil          *time.Time  `json:"cooldown_until,omitempty"`
	RateLimitedUntil       *time.Time  `json:"rate_limit_until,omitempty"`
	LastUsed               *time.Time  `json:"last_used,omitempty"`
	InFlight               int         `json:"in_flight"`
	Quota                  *CodexQuota `json:"quota,omitempty"`
	RateLimitResetAt       *time.Time  `json:"rate_limit_reset_at,omitempty"`
	MaxUsedPercent         *float64    `json:"max_used_percent,omitempty"`
	BoundConversationCount int         `json:"bound_conversation_count,omitempty"`
}

// PoolAffinitySnapshot is safe affinity metadata for pool-health.
type PoolAffinitySnapshot struct {
	BoundConversations int    `json:"bound_conversations"`
	File               string `json:"file,omitempty"`
}

// PoolSnapshot is a safe, deep-copy view of the entire pool state.
type PoolSnapshot struct {
	Strategy       string                `json:"strategy,omitempty"`
	CodexAccounts  int                   `json:"codex_account_count,omitempty"`
	EligibleCount  int                   `json:"eligible_count,omitempty"`
	Affinity       *PoolAffinitySnapshot `json:"affinity,omitempty"`
	Accounts       []AccountSnapshot     `json:"accounts"`
}

// AccountPool maintains an in-memory view of loaded Codex token files with
// runtime state for health, cooldown, rate-limiting, and in-flight accounting.
type AccountPool struct {
	mu            sync.Mutex
	entries       map[string]*AccountEntry // keyed by token file path
	nowFunc       func() time.Time
	selector      Selector
	strategy      config.LoadBalancingStrategy
	quotaSoftCap  float64
	affinity      *AffinityStore
}

// NewAccountPool creates a pool seeded from the given tokens.
// Only Codex tokens are included; other providers are ignored.
// If selector is nil, a default round-robin selector is used.
// lb configures sticky affinity and quota soft cap; affinity may be nil.
func NewAccountPool(tokens []*Token, nowFunc func() time.Time, lb config.LoadBalancing, affinity *AffinityStore, selector ...Selector) *AccountPool {
	if nowFunc == nil {
		nowFunc = time.Now
	}
	strategy := lb.Strategy
	if strategy == "" {
		strategy = config.LoadBalancingRoundRobin
	}
	var sel Selector
	if len(selector) > 0 && selector[0] != nil {
		sel = selector[0]
	} else {
		sel = NewSelector(strategy)
	}
	p := &AccountPool{
		entries:      make(map[string]*AccountEntry),
		nowFunc:      nowFunc,
		selector:     sel,
		strategy:     strategy,
		quotaSoftCap: lb.QuotaSoftCapPercent,
		affinity:     affinity,
	}
	p.seed(tokens)
	p.pruneAffinity()
	return p
}

// seed initializes pool entries from the given tokens.
func (p *AccountPool) seed(tokens []*Token) {
	for _, tok := range tokens {
		if tok.Provider() != ProviderCodex {
			continue
		}
		entry := p.entryFromToken(tok)
		p.entries[entry.Path] = entry
	}
}

// entryFromToken creates a new AccountEntry from a token.
func (p *AccountPool) entryFromToken(tok *Token) *AccountEntry {
	now := p.nowFunc()
	entry := &AccountEntry{
		Path:      tok.Path(),
		Selector:  safeSelectorLabel(tok),
		Provider:  tok.Provider(),
		Disabled:  tok.Disabled,
		Healthy:   true,
		Email:     tok.Email,
		Subject:   tok.Subject,
		AccountID: tok.AccountID,
	}

	// Quota deep copy
	if tok.CodexQuota != nil {
		entry.Quota = deepCopyCodexQuota(tok.CodexQuota)
	}

	// Rate limit reset from persisted metadata
	entry.RateLimitResetAt = parseRateLimitResetAt(tok.RateLimitResetAt)

	// Refresh token availability
	entry.HasRefreshToken = strings.TrimSpace(tok.RefreshToken) != ""

	// Token expiry status
	exp, hasExpiry := tok.Expiry()
	if hasExpiry && !exp.IsZero() && !exp.After(now) {
		entry.TokenExpired = true
		entry.Refreshable = entry.HasRefreshToken
	}

	// Determine if persisted rate-limit metadata should suppress eligibility
	p.applyPersistedRateLimit(entry, now)

	return entry
}

// applyPersistedRateLimit sets RateLimitedUntil from persisted metadata only if
// the quota window is exhausted (limit_reached=true). Passive telemetry from
// successful responses does not suppress eligibility.
func (p *AccountPool) applyPersistedRateLimit(entry *AccountEntry, now time.Time) {
	if entry.RateLimitResetAt == nil || !entry.RateLimitResetAt.After(now) {
		return
	}
	// Only suppress eligibility if an exhausted/limit-reached window exists
	if hasExhaustedWindow(entry.Quota) {
		entry.RateLimitedUntil = *entry.RateLimitResetAt
	}
}

// hasExhaustedWindow returns true if any quota window has limit_reached=true.
func hasExhaustedWindow(q *CodexQuota) bool {
	if q == nil {
		return false
	}
	for _, w := range []*CodexQuotaWindow{q.Primary, q.Secondary, q.CodeReview} {
		if w != nil && w.LimitReached {
			return true
		}
	}
	return false
}

// parseRateLimitResetAt parses a persisted rate_limit_reset_at string.
// Returns nil for empty, malformed, or past timestamps.
func parseRateLimitResetAt(raw string) *time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if tm, err := time.Parse(layout, raw); err == nil {
			return &tm
		}
	}
	return nil
}

// Reload updates pool entries from a fresh set of loaded tokens.
// Runtime state (healthy, cooldown, rate-limit, last-used, in-flight) is
// preserved for entries whose token path stays the same. Entries whose paths
// are no longer present are removed. New entries are added with fresh state.
// Identity changes (email, subject, etc.) at the same path update immediately.
func (p *AccountPool) Reload(tokens []*Token) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Build new token map keyed by path
	newTokens := make(map[string]*Token, len(tokens))
	for _, tok := range tokens {
		if tok.Provider() != ProviderCodex {
			continue
		}
		newTokens[tok.Path()] = tok
	}

	now := p.nowFunc()

	// Remove entries no longer present.
	//
	// In-flight entries whose path disappeared from the token set are
	// intentionally preserved with stale metadata. This is safe because:
	//   - The active request still holds a valid lease (Begin was called)
	//     and will call End to release it, decrementing InFlight.
	//   - Keeping the entry ensures End does not panic on a missing path
	//     and InFlight never goes negative.
	//   - These stale entries are skipped by Eligible/Select (their
	//     provider may no longer be Codex, or they may be filtered by
	//     the exclusion set) so they cannot be selected for new requests.
	//   - Once InFlight reaches zero via End, the next Reload will
	//     garbage-collect the entry.
	for path := range p.entries {
		if _, ok := newTokens[path]; !ok {
			if p.entries[path].InFlight > 0 {
				// Keep the entry but update its state
				continue
			}
			delete(p.entries, path)
		}
	}

	// Add or update entries
	for path, tok := range newTokens {
		if existing, ok := p.entries[path]; ok {
			// Update identity/metadata fields, preserve runtime state
			p.updateEntry(existing, tok, now)
		} else {
			entry := p.entryFromToken(tok)
			p.entries[path] = entry
		}
	}
	p.pruneAffinityLocked()
}

func (p *AccountPool) pruneAffinity() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pruneAffinityLocked()
}

func (p *AccountPool) pruneAffinityLocked() {
	if p.affinity == nil {
		return
	}
	valid := make(map[string]bool, len(p.entries))
	for path := range p.entries {
		valid[path] = true
	}
	_ = p.affinity.Prune(valid)
}

// BindConversation persists conversation→account affinity when sticky is enabled.
func (p *AccountPool) BindConversation(conversationID, accountPath string) {
	if p == nil || p.strategy != config.LoadBalancingSticky || p.affinity == nil {
		return
	}
	_ = p.affinity.Bind(conversationID, accountPath)
}

// ClearConversation removes a persisted conversation binding when sticky is enabled.
func (p *AccountPool) ClearConversation(conversationID string) {
	if p == nil || p.strategy != config.LoadBalancingSticky || p.affinity == nil {
		return
	}
	_ = p.affinity.Unbind(conversationID)
}

// updateEntry updates identity and metadata fields of an existing entry
// while preserving runtime state.
func (p *AccountPool) updateEntry(entry *AccountEntry, tok *Token, now time.Time) {
	// Update identity
	entry.Selector = safeSelectorLabel(tok)
	entry.Provider = tok.Provider()
	entry.Disabled = tok.Disabled
	entry.Email = tok.Email
	entry.Subject = tok.Subject
	entry.AccountID = tok.AccountID

	// Update quota (deep copy)
	if tok.CodexQuota != nil {
		entry.Quota = deepCopyCodexQuota(tok.CodexQuota)
	} else {
		entry.Quota = nil
	}

	// Update rate limit reset from persisted metadata
	entry.RateLimitResetAt = parseRateLimitResetAt(tok.RateLimitResetAt)

	// Update refresh token availability
	entry.HasRefreshToken = strings.TrimSpace(tok.RefreshToken) != ""

	// Update token expiry status
	exp, hasExpiry := tok.Expiry()
	entry.TokenExpired = false
	entry.Refreshable = false
	if hasExpiry && !exp.IsZero() && !exp.After(now) {
		entry.TokenExpired = true
		entry.Refreshable = entry.HasRefreshToken
	}

	// If the account was re-enabled via file change, mark healthy again
	if !entry.Disabled && !entry.Healthy {
		// Keep unhealthy unless the token itself changed meaningfully
		// (token reload after re-login may fix the issue)
		// For now, we keep the unhealthy state but allow recovery via
		// successful refresh or explicit recovery.
	}

	// If provider changed away from Codex, entry stays but won't be eligible
	// Selector and matching update immediately.

	// Re-evaluate persisted rate-limit only if not already in runtime rate-limit
	if entry.RateLimitedUntil.IsZero() || !entry.RateLimitedUntil.After(now) {
		entry.RateLimitedUntil = time.Time{} // clear stale
		p.applyPersistedRateLimit(entry, now)
	}
}

// Begin acquires a lease on the account at the given path.
// It increments in-flight and updates last_used. Returns an error if the
// account is not found in the pool.
func (p *AccountPool) Begin(path string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	entry, ok := p.entries[path]
	if !ok {
		return fmt.Errorf("account not found for path %q", filepath.Base(path))
	}
	entry.InFlight++
	entry.LastUsed = p.nowFunc()
	return nil
}

// End releases a lease on the account at the given path.
// It decrements in-flight and clamps to zero to prevent negative values.
func (p *AccountPool) End(path string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	entry, ok := p.entries[path]
	if !ok {
		return
	}
	entry.InFlight--
	if entry.InFlight < 0 {
		entry.InFlight = 0
	}
}

// MarkRateLimited marks the account at path as rate-limited until the given time.
func (p *AccountPool) MarkRateLimited(path string, until time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if entry, ok := p.entries[path]; ok {
		entry.RateLimitedUntil = until
	}
}

// MarkCooldown marks the account at path as in error cooldown until the given time.
func (p *AccountPool) MarkCooldown(path string, until time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if entry, ok := p.entries[path]; ok {
		entry.CooldownUntil = until
	}
}

// MarkUnhealthy marks the account at path as unhealthy.
func (p *AccountPool) MarkUnhealthy(path string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if entry, ok := p.entries[path]; ok {
		entry.Healthy = false
	}
}

// MarkHealthy marks the account at path as healthy (recovery).
func (p *AccountPool) MarkHealthy(path string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if entry, ok := p.entries[path]; ok {
		entry.Healthy = true
	}
}

// ClearRateLimit clears the rate-limit on the account at path (recovery via cooldown expiry).
func (p *AccountPool) ClearRateLimit(path string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if entry, ok := p.entries[path]; ok {
		entry.RateLimitedUntil = time.Time{}
	}
}

// ClearCooldown clears the error cooldown on the account at path.
func (p *AccountPool) ClearCooldown(path string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if entry, ok := p.entries[path]; ok {
		entry.CooldownUntil = time.Time{}
	}
}

// EnabledCodexCount returns the number of non-disabled Codex accounts in the pool.
// This is used to determine whether the pool is in single-account mode.
func (p *AccountPool) EnabledCodexCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	count := 0
	for _, entry := range p.entries {
		if entry.Provider == ProviderCodex && !entry.Disabled {
			count++
		}
	}
	return count
}

// Eligible returns entries eligible for selection, excluding those in the
// provided exclusion set. Eligibility criteria:
//   - Provider is Codex
//   - Not disabled
//   - Not in exclusion set
//   - Not in active cooldown (cooldownUntil > now)
//   - Not actively rate-limited (rateLimitedUntil > now)
//   - Healthy
//   - Has a usable token (expired with refresh token is still eligible)
//
// The returned entries are sorted by selector label, then path for determinism.
func (p *AccountPool) Eligible(exclude map[string]bool) []*AccountEntry {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.eligibleLocked(exclude)
}

// eligibleLocked builds the eligible entry list assuming p.mu is already held.
// This is used by both Eligible (which acquires the lock) and Select (which
// needs to call the selector under the same lock to avoid racing with
// Begin/End mutations to InFlight).
func (p *AccountPool) eligibleLocked(exclude map[string]bool) []*AccountEntry {
	now := p.nowFunc()
	var eligible []*AccountEntry
	for _, entry := range p.entries {
		if !p.isEligible(entry, exclude, now) {
			continue
		}
		eligible = append(eligible, entry)
	}

	// Deterministic sort: by selector, then by path as tie-breaker
	sort.Slice(eligible, func(i, j int) bool {
		if eligible[i].Selector != eligible[j].Selector {
			return eligible[i].Selector < eligible[j].Selector
		}
		return eligible[i].Path < eligible[j].Path
	})

	return eligible
}

// Select picks an eligible account using the configured strategy.
// If account is non-empty, selection is constrained to accounts matching
// the pinned selector (trimmed, case-insensitive match against email, subject,
// account ID, filename stem, or filename). Pinned selection never falls back
// outside the matching subset.
// exclude is the set of token paths already tried in the current failover loop.
// conversationID enables sticky affinity when strategy is sticky.
// Returns ErrNoEligibleAccounts if no account is eligible.
//
// The pool mutex is held through selector invocation so that
// LeastConnectionsSelector reads InFlight consistently with concurrent
// Begin/End mutations (VAL-POOL-010).
func (p *AccountPool) Select(account string, exclude map[string]bool, conversationID string) (*AccountEntry, error) {
	p.mu.Lock()

	eligible := p.eligibleLocked(exclude)
	if len(eligible) == 0 {
		p.mu.Unlock()
		return nil, ErrNoEligibleAccounts
	}

	// Apply pinned account filter
	if strings.TrimSpace(account) != "" {
		var pinned []*AccountEntry
		for _, e := range eligible {
			if e.MatchesAccount(account) {
				pinned = append(pinned, e)
			}
		}
		if len(pinned) == 0 {
			p.mu.Unlock()
			return nil, ErrNoEligibleAccounts
		}
		eligible = pinned
	}

	eligible = filterEligibleBySoftCap(eligible, p.quotaSoftCap)

	var picked *AccountEntry
	var err error

	if p.strategy == config.LoadBalancingSticky && strings.TrimSpace(conversationID) != "" {
		picked = p.selectStickyLocked(conversationID, eligible, exclude)
	}

	if picked == nil {
		picked, err = p.selector.Select(eligible)
	}

	p.mu.Unlock()

	if err != nil {
		return nil, err
	}
	return picked, nil
}

func (p *AccountPool) selectStickyLocked(conversationID string, eligible []*AccountEntry, exclude map[string]bool) *AccountEntry {
	if p.affinity == nil {
		return nil
	}
	bound := p.affinity.Lookup(conversationID)
	if bound != "" && !exclude[bound] {
		for _, e := range eligible {
			if e.Path == bound {
				return e
			}
		}
	}
	return pickByQuota(eligible)
}

// isEligible checks if a single entry is eligible for selection.
func (p *AccountPool) isEligible(entry *AccountEntry, exclude map[string]bool, now time.Time) bool {
	// Must be Codex
	if entry.Provider != ProviderCodex {
		return false
	}
	// Not disabled
	if entry.Disabled {
		return false
	}
	// Not in exclusion set
	if exclude[entry.Path] {
		return false
	}
	// Not in active cooldown
	if entry.CooldownUntil.After(now) {
		return false
	}
	// Not actively rate-limited
	if entry.RateLimitedUntil.After(now) {
		return false
	}
	// Must be healthy
	if !entry.Healthy {
		return false
	}
	// Must have some form of usable token
	// Expired tokens with refresh tokens are still eligible
	// Expired tokens without refresh tokens are not eligible
	if entry.TokenExpired && !entry.Refreshable {
		return false
	}
	return true
}

// Snapshot returns a deep-copy snapshot of all Codex pool entries.
// The snapshot is deterministic: entries are sorted by selector label then path,
// consistent with Eligible() ordering.
// The snapshot never exposes access tokens, refresh tokens, ID tokens, or raw JSON.
func (p *AccountPool) Snapshot() *PoolSnapshot {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Collect and sort entries by selector then path (matching Eligible)
	var entries []*AccountEntry
	for _, entry := range p.entries {
		if entry.Provider != ProviderCodex {
			continue
		}
		entries = append(entries, entry)
	}

	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Selector != entries[j].Selector {
			return entries[i].Selector < entries[j].Selector
		}
		return entries[i].Path < entries[j].Path
	})

	snap := &PoolSnapshot{
		Strategy:      string(p.strategy),
		CodexAccounts: len(entries),
	}
	snap.EligibleCount = len(p.eligibleLocked(nil))
	if p.affinity != nil {
		bound, file := p.affinity.Stats()
		snap.Affinity = &PoolAffinitySnapshot{BoundConversations: bound, File: file}
	}
	for _, entry := range entries {
		snap.Accounts = append(snap.Accounts, snapshotFromEntry(entry, p.affinity))
	}

	return snap
}

// MatchesAccount checks if the entry matches the given account selector string,
// using the same normalization as Token.MatchesAccount.
func (e *AccountEntry) MatchesAccount(account string) bool {
	account = strings.TrimSpace(account)
	if account == "" {
		return true
	}
	values := []string{
		e.Email,
		e.Subject,
		e.AccountID,
		strings.TrimSuffix(filepath.Base(e.Path), filepath.Ext(e.Path)),
		filepath.Base(e.Path),
	}
	for _, v := range values {
		if strings.EqualFold(strings.TrimSpace(v), account) {
			return true
		}
	}
	return false
}

// snapshotFromEntry creates a deep-copy AccountSnapshot from an AccountEntry.
func snapshotFromEntry(entry *AccountEntry, affinity *AffinityStore) AccountSnapshot {
	snap := AccountSnapshot{
		Selector: entry.Selector,
		Provider: string(entry.Provider),
		Disabled: entry.Disabled,
		Healthy:  entry.Healthy,
		InFlight: entry.InFlight,
	}
	if !entry.CooldownUntil.IsZero() {
		t := entry.CooldownUntil.UTC()
		snap.CooldownUntil = &t
	}
	if !entry.RateLimitedUntil.IsZero() {
		t := entry.RateLimitedUntil.UTC()
		snap.RateLimitedUntil = &t
	}
	if !entry.LastUsed.IsZero() {
		t := entry.LastUsed.UTC()
		snap.LastUsed = &t
	}
	if entry.Quota != nil {
		snap.Quota = deepCopyCodexQuota(entry.Quota)
		maxUsed := MaxAgentUsedPercent(entry.Quota)
		snap.MaxUsedPercent = &maxUsed
	}
	if entry.RateLimitResetAt != nil {
		t := *entry.RateLimitResetAt
		snap.RateLimitResetAt = &t
	}
	if affinity != nil {
		snap.BoundConversationCount = affinity.BoundCountForPath(entry.Path)
	}
	return snap
}

// safeSelectorLabel returns a safe display label for the token.
// Precedence: email > subject > accountID > filename stem.
// Never exposes token secrets, access tokens, or refresh tokens.
func safeSelectorLabel(tok *Token) string {
	for _, v := range []string{tok.Email, tok.Subject, tok.AccountID} {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	if strings.TrimSpace(tok.path) != "" {
		return strings.TrimSuffix(filepath.Base(tok.path), filepath.Ext(tok.path))
	}
	return "unknown"
}

// deepCopyCodexQuota creates a deep copy of a CodexQuota struct.
func deepCopyCodexQuota(q *CodexQuota) *CodexQuota {
	if q == nil {
		return nil
	}
	cp := *q
	if q.Primary != nil {
		w := *q.Primary
		if q.Primary.RemainingPercent != nil {
			v := *q.Primary.RemainingPercent
			w.RemainingPercent = &v
		}
		if q.Primary.WindowMinutes != nil {
			v := *q.Primary.WindowMinutes
			w.WindowMinutes = &v
		}
		if q.Primary.ResetAt != nil {
			v := *q.Primary.ResetAt
			w.ResetAt = &v
		}
		cp.Primary = &w
	}
	if q.Secondary != nil {
		w := *q.Secondary
		if q.Secondary.RemainingPercent != nil {
			v := *q.Secondary.RemainingPercent
			w.RemainingPercent = &v
		}
		if q.Secondary.WindowMinutes != nil {
			v := *q.Secondary.WindowMinutes
			w.WindowMinutes = &v
		}
		if q.Secondary.ResetAt != nil {
			v := *q.Secondary.ResetAt
			w.ResetAt = &v
		}
		cp.Secondary = &w
	}
	if q.CodeReview != nil {
		w := *q.CodeReview
		if q.CodeReview.RemainingPercent != nil {
			v := *q.CodeReview.RemainingPercent
			w.RemainingPercent = &v
		}
		if q.CodeReview.WindowMinutes != nil {
			v := *q.CodeReview.WindowMinutes
			w.WindowMinutes = &v
		}
		if q.CodeReview.ResetAt != nil {
			v := *q.CodeReview.ResetAt
			w.ResetAt = &v
		}
		cp.CodeReview = &w
	}
	return &cp
}
