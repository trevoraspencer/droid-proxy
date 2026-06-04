package oauth

// This file contains hardening tests for VAL-CROSS-001, VAL-CROSS-010, and
// VAL-CROSS-011. They verify:
//   - Real user auth files are never touched by tests or validation
//   - CLI/TUI auth status/enable/disable/logout remain compatible for Codex/xAI
//   - Pool runtime state stays in memory and auth file permissions stay restrictive

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"droid-proxy/internal/config"
)

// --------------- VAL-CROSS-001: Real auth files are not touched ---------------

// TestAuthDirDefaultNeverUsedInTests verifies that constructing a Manager with
// the default AuthDir (empty config) resolves to the real ~/.droid-proxy/auth,
// confirming that test-only configs must always set an explicit AuthDir.
// This test documents the contract: real auth files are never touched.
func TestAuthDirDefaultNeverUsedInTests(t *testing.T) {
	// With empty config, AuthDir resolves to the real user path
	mgr := NewManager(&config.Config{})
	dir, err := mgr.AuthDir()
	if err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	if home != "" && dir != filepath.Join(home, ".droid-proxy", "auth") {
		t.Fatalf("default AuthDir = %q, want ~/.droid-proxy/auth", dir)
	}
}

// TestAllTestManagersUseTempAuthDir verifies that newTestManager (the shared
// test helper) always configures a temp auth dir, never the real default path.
func TestAllTestManagersUseTempAuthDir(t *testing.T) {
	dir := t.TempDir()
	mgr := newTestManager(t, dir)
	resolved, err := mgr.AuthDir()
	if err != nil {
		t.Fatal(err)
	}
	if resolved != dir {
		t.Fatalf("test manager AuthDir = %q, want %q", resolved, dir)
	}
	home, _ := os.UserHomeDir()
	if home != "" && strings.HasPrefix(resolved, home) {
		t.Fatalf("test manager AuthDir %q is under HOME %q — must use t.TempDir()", resolved, home)
	}
}

// --------------- VAL-CROSS-010: Account management compatibility ---------------

// TestAccountManagement_DisableCodexReflectsInPool verifies that disabling a
// Codex token via Manager.SetTokenDisabled is reflected in the pool, and that
// the disabled account is ineligible for selection.
func TestAccountManagement_DisableCodexReflectsInPool(t *testing.T) {
	authDir := t.TempDir()
	mgr := NewManager(&config.Config{OAuth: config.OAuth{AuthDir: authDir}})

	tok := makeToken("user@example.com", "access-SENTINEL", "refresh-SENTINEL", false)
	_, err := mgr.SaveToken(tok)
	if err != nil {
		t.Fatal(err)
	}

	// Load the token back to get the file path
	loaded, err := mgr.LoadToken(ProviderCodex, "user@example.com")
	if err != nil {
		t.Fatal(err)
	}

	pool := NewAccountPool([]*Token{loaded}, fakeTime, TestPoolLB(), nil)

	// Initially eligible
	if len(pool.Eligible(nil)) != 1 {
		t.Fatal("expected 1 eligible initially")
	}

	// Disable via Manager (CLI "auth disable" path)
	disabled, err := mgr.SetTokenDisabled(ProviderCodex, "user@example.com", true)
	if err != nil {
		t.Fatal(err)
	}
	if !disabled.Disabled {
		t.Fatal("expected token to be disabled")
	}

	// Reload pool from updated token files
	tokens, err := mgr.LoadTokens(ProviderCodex)
	if err != nil {
		t.Fatal(err)
	}
	pool.Reload(tokens)

	// Pool should reflect disabled state
	eligible := pool.Eligible(nil)
	if len(eligible) != 0 {
		t.Fatalf("expected 0 eligible after disable, got %d", len(eligible))
	}

	// Snapshot should still show the account (read-only)
	snap := pool.Snapshot()
	if len(snap.Accounts) != 1 {
		t.Fatalf("expected 1 account in snapshot, got %d", len(snap.Accounts))
	}
	if !snap.Accounts[0].Disabled {
		t.Fatal("expected Disabled=true in snapshot")
	}
}

// TestAccountManagement_EnableCodexRestoresEligibility verifies that re-enabling
// a disabled Codex account restores it to eligibility.
func TestAccountManagement_EnableCodexRestoresEligibility(t *testing.T) {
	authDir := t.TempDir()
	mgr := NewManager(&config.Config{OAuth: config.OAuth{AuthDir: authDir}})

	tok := makeToken("user@example.com", "access-SENTINEL", "refresh-SENTINEL", false)
	_, err := mgr.SaveToken(tok)
	if err != nil {
		t.Fatal(err)
	}

	loaded, err := mgr.LoadToken(ProviderCodex, "user@example.com")
	if err != nil {
		t.Fatal(err)
	}

	pool := NewAccountPool([]*Token{loaded}, fakeTime, TestPoolLB(), nil)

	// Disable
	_, err = mgr.SetTokenDisabled(ProviderCodex, "user@example.com", true)
	if err != nil {
		t.Fatal(err)
	}
	tokens, _ := mgr.LoadTokens(ProviderCodex)
	pool.Reload(tokens)
	if len(pool.Eligible(nil)) != 0 {
		t.Fatal("expected 0 eligible after disable")
	}

	// Re-enable (CLI "auth enable" path)
	enabled, err := mgr.SetTokenDisabled(ProviderCodex, "user@example.com", false)
	if err != nil {
		t.Fatal(err)
	}
	if enabled.Disabled {
		t.Fatal("expected token to be enabled")
	}

	tokens, _ = mgr.LoadTokens(ProviderCodex)
	pool.Reload(tokens)
	if len(pool.Eligible(nil)) != 1 {
		t.Fatalf("expected 1 eligible after re-enable, got %d", len(pool.Eligible(nil)))
	}
}

// TestAccountManagement_LogoutCodexReflectsInPool verifies that deleting a
// Codex token via Manager.DeleteToken (CLI "auth logout") is reflected in the pool.
func TestAccountManagement_LogoutCodexReflectsInPool(t *testing.T) {
	authDir := t.TempDir()
	mgr := NewManager(&config.Config{OAuth: config.OAuth{AuthDir: authDir}})

	tok1 := makeToken("user1@example.com", "access1-SENTINEL", "refresh1-SENTINEL", false)
	_, err := mgr.SaveToken(tok1)
	if err != nil {
		t.Fatal(err)
	}
	tok2 := makeToken("user2@example.com", "access2-SENTINEL", "refresh2-SENTINEL", false)
	_, err = mgr.SaveToken(tok2)
	if err != nil {
		t.Fatal(err)
	}

	tokens, err := mgr.LoadTokens(ProviderCodex)
	if err != nil {
		t.Fatal(err)
	}
	pool := NewAccountPool(tokens, fakeTime, TestPoolLB(), nil)
	if len(pool.Snapshot().Accounts) != 2 {
		t.Fatalf("expected 2 accounts initially, got %d", len(pool.Snapshot().Accounts))
	}

	// Logout user1 (CLI "auth logout" path)
	_, err = mgr.DeleteToken(ProviderCodex, "user1@example.com")
	if err != nil {
		t.Fatal(err)
	}

	// Reload pool
	tokens, _ = mgr.LoadTokens(ProviderCodex)
	pool.Reload(tokens)

	snap := pool.Snapshot()
	if len(snap.Accounts) != 1 {
		t.Fatalf("expected 1 account after logout, got %d", len(snap.Accounts))
	}
	if snap.Accounts[0].Selector != "user2@example.com" {
		t.Fatalf("expected user2 to survive, got %q", snap.Accounts[0].Selector)
	}
	if len(pool.Eligible(nil)) != 1 {
		t.Fatalf("expected 1 eligible after logout, got %d", len(pool.Eligible(nil)))
	}
}

// TestAccountManagement_XAIRemainsSingleAccount verifies that xAI account
// operations (disable, enable, logout) work but do not interact with the
// Codex pool, and xAI tokens are never in the Codex pool.
func TestAccountManagement_XAIRemainsSingleAccount(t *testing.T) {
	authDir := t.TempDir()
	mgr := NewManager(&config.Config{OAuth: config.OAuth{AuthDir: authDir}})

	// Save an xAI token
	xaiTok := &Token{
		Type:         string(ProviderXAI),
		AccessToken:  "xai-access-SENTINEL",
		RefreshToken: "xai-refresh-SENTINEL",
		Email:        "xai@example.com",
		Expired:      fakeTime().Add(time.Hour).UTC().Format(time.RFC3339),
	}
	_, err := mgr.SaveToken(xaiTok)
	if err != nil {
		t.Fatal(err)
	}

	// Also save a Codex token
	codexTok := makeToken("codex@example.com", "codex-access-SENTINEL", "codex-refresh-SENTINEL", false)
	_, err = mgr.SaveToken(codexTok)
	if err != nil {
		t.Fatal(err)
	}

	// Codex pool should only contain Codex tokens
	codexTokens, _ := mgr.LoadTokens(ProviderCodex)
	pool := NewAccountPool(codexTokens, fakeTime, TestPoolLB(), nil)
	snap := pool.Snapshot()
	if len(snap.Accounts) != 1 {
		t.Fatalf("expected 1 Codex account in pool, got %d", len(snap.Accounts))
	}
	if snap.Accounts[0].Selector != "codex@example.com" {
		t.Fatalf("expected codex@example.com, got %q", snap.Accounts[0].Selector)
	}

	// xAI status/enable/disable/logout work independently
	tokens, err := mgr.LoadTokens(ProviderXAI)
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 1 {
		t.Fatalf("expected 1 xAI token, got %d", len(tokens))
	}

	// Disable xAI account
	_, err = mgr.SetTokenDisabled(ProviderXAI, "xai@example.com", true)
	if err != nil {
		t.Fatal(err)
	}

	// Verify xAI is disabled (LoadTokens returns all including disabled)
	xaiTokens, _ := mgr.LoadTokens(ProviderXAI)
	if len(xaiTokens) != 1 {
		t.Fatalf("expected 1 xAI token (LoadTokens includes disabled), got %d", len(xaiTokens))
	}
	if !xaiTokens[0].Disabled {
		t.Fatal("expected xAI token to be disabled")
	}

	// Verify LoadToken (which skips disabled) returns no match
	if _, err := mgr.LoadToken(ProviderXAI, "xai@example.com"); err == nil {
		t.Fatal("expected LoadToken to skip disabled xAI account")
	}

	// Codex pool should be unchanged
	codexTokens2, _ := mgr.LoadTokens(ProviderCodex)
	pool.Reload(codexTokens2)
	if len(pool.Snapshot().Accounts) != 1 {
		t.Fatalf("Codex pool should still have 1 account after xAI disable, got %d", len(pool.Snapshot().Accounts))
	}

	// Re-enable xAI
	_, err = mgr.SetTokenDisabled(ProviderXAI, "xai@example.com", false)
	if err != nil {
		t.Fatal(err)
	}

	// Logout xAI (delete)
	_, err = mgr.DeleteToken(ProviderXAI, "xai@example.com")
	if err != nil {
		t.Fatal(err)
	}

	// Codex pool still unaffected
	codexTokens3, _ := mgr.LoadTokens(ProviderCodex)
	pool.Reload(codexTokens3)
	if len(pool.Snapshot().Accounts) != 1 {
		t.Fatalf("Codex pool should still have 1 account after xAI logout, got %d", len(pool.Snapshot().Accounts))
	}
}

// TestAccountManagement_DisableCodexAccountByFilename verifies that CLI disable
// by filename stem (as used in TUI) works correctly with the pool.
func TestAccountManagement_DisableCodexAccountByFilename(t *testing.T) {
	authDir := t.TempDir()
	mgr := NewManager(&config.Config{OAuth: config.OAuth{AuthDir: authDir}})

	tok := makeToken("user@example.com", "access-SENTINEL", "refresh-SENTINEL", false)
	path, err := mgr.SaveToken(tok)
	if err != nil {
		t.Fatal(err)
	}

	// Disable by filename stem (TUI uses filepath.Base(path) sans extension)
	filename := filepath.Base(path)
	stem := strings.TrimSuffix(filename, filepath.Ext(filename))

	disabled, err := mgr.SetTokenDisabled(ProviderCodex, stem, true)
	if err != nil {
		t.Fatal(err)
	}
	if !disabled.Disabled {
		t.Fatal("expected token to be disabled by filename stem")
	}

	// Verify file on disk reflects disabled state
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var onDisk struct {
		Disabled bool `json:"disabled"`
	}
	if err := json.Unmarshal(raw, &onDisk); err != nil {
		t.Fatal(err)
	}
	if !onDisk.Disabled {
		t.Fatal("token file on disk should have disabled=true")
	}
}

// --------------- VAL-CROSS-011: Runtime state in memory, restrictive permissions ---------------

// TestPoolRuntimeStateNotPersistedToTokenJSON verifies that pool runtime state
// (InFlight, LastUsed, CooldownUntil, RateLimitedUntil, Healthy) is never
// written to token JSON files. Only documented token fields should appear.
func TestPoolRuntimeStateNotPersistedToTokenJSON(t *testing.T) {
	authDir := t.TempDir()
	mgr := NewManager(&config.Config{OAuth: config.OAuth{AuthDir: authDir}})

	tok := makeToken("user@example.com", "access-SENTINEL", "refresh-SENTINEL", false)
	path, err := mgr.SaveToken(tok)
	if err != nil {
		t.Fatal(err)
	}

	loaded, err := mgr.LoadToken(ProviderCodex, "user@example.com")
	if err != nil {
		t.Fatal(err)
	}

	pool := NewAccountPool([]*Token{loaded}, fakeTime, TestPoolLB(), nil)

	// Apply various runtime state changes
	pool.Begin(path)
	pool.MarkRateLimited(path, fakeTime().Add(time.Hour))
	pool.MarkCooldown(path, fakeTime().Add(30*time.Minute))
	pool.MarkUnhealthy(path)

	// Snapshot should show runtime state
	snap := pool.Snapshot()
	if len(snap.Accounts) != 1 {
		t.Fatal("expected 1 account")
	}
	acct := snap.Accounts[0]
	if acct.InFlight != 1 {
		t.Fatalf("expected in_flight=1, got %d", acct.InFlight)
	}
	if acct.Healthy {
		t.Fatal("expected unhealthy")
	}
	if acct.CooldownUntil == nil {
		t.Fatal("expected cooldown")
	}
	if acct.RateLimitedUntil == nil {
		t.Fatal("expected rate limited")
	}

	// Now read the token file on disk and verify no runtime-only fields
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	runtimeOnlyFields := []string{
		"in_flight",
		"last_used",
		"cooldown_until",
		"rate_limit_until",
		"healthy",
		"unhealthy",
		"inflight",
		"lastUsed",
		"cooldownUntil",
		"rateLimitedUntil",
		"selector_cursor",
		"cursor",
	}
	for _, field := range runtimeOnlyFields {
		if strings.Contains(string(raw), field) {
			t.Fatalf("token file contains runtime-only field %q:\n%s", field, string(raw))
		}
	}

	// Verify only expected JSON keys are present
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatal(err)
	}
	expectedKeys := map[string]bool{
		"type": true, "access_token": true, "refresh_token": true,
		"expired": true, "email": true,
	}
	for key := range parsed {
		if !expectedKeys[key] {
			// Some fields like disabled, codex_quota, rate_limit_reset_at, last_seen_at
			// are documented persisted fields, not runtime-only
			documentedKeys := map[string]bool{
				"type": true, "access_token": true, "refresh_token": true,
				"expired": true, "email": true, "sub": true, "account_id": true,
				"base_url": true, "redirect_uri": true, "token_endpoint": true,
				"auth_kind": true, "disabled": true, "codex_quota": true,
				"rate_limit_reset_at": true, "last_seen_at": true,
				"token_type": true, "expires_in": true, "last_refresh": true,
				"id_token": true,
			}
			if !documentedKeys[key] {
				t.Fatalf("token file contains unexpected key %q (might be runtime-only):\n%s", key, string(raw))
			}
		}
	}

	pool.End(path)
}

// TestAuthDirPermissionsRestrictive verifies that auth directories created
// by SaveToken have 0700 permissions.
func TestAuthDirPermissionsRestrictive(t *testing.T) {
	authDir := t.TempDir()
	mgr := NewManager(&config.Config{OAuth: config.OAuth{AuthDir: authDir}})

	tok := makeToken("user@example.com", "access-SENTINEL", "refresh-SENTINEL", false)
	_, err := mgr.SaveToken(tok)
	if err != nil {
		t.Fatal(err)
	}

	assertPerm(t, authDir, 0o700)
}

// TestTokenFilePermissionsRestrictive verifies that token files created
// by SaveToken have 0600 permissions.
func TestTokenFilePermissionsRestrictive(t *testing.T) {
	authDir := t.TempDir()
	mgr := NewManager(&config.Config{OAuth: config.OAuth{AuthDir: authDir}})

	tok := makeToken("user@example.com", "access-SENTINEL", "refresh-SENTINEL", false)
	path, err := mgr.SaveToken(tok)
	if err != nil {
		t.Fatal(err)
	}

	assertPerm(t, path, 0o600)
}

// TestPermissionsAfterDisableEnableLogout verifies that token file permissions
// remain restrictive (0600) after disable, re-enable, and logout operations.
func TestPermissionsAfterDisableEnableLogout(t *testing.T) {
	authDir := t.TempDir()
	mgr := NewManager(&config.Config{OAuth: config.OAuth{AuthDir: authDir}})

	tok := makeToken("user@example.com", "access-SENTINEL", "refresh-SENTINEL", false)
	path, err := mgr.SaveToken(tok)
	if err != nil {
		t.Fatal(err)
	}

	assertPerm(t, authDir, 0o700)
	assertPerm(t, path, 0o600)

	// Disable
	_, err = mgr.SetTokenDisabled(ProviderCodex, "user@example.com", true)
	if err != nil {
		t.Fatal(err)
	}
	assertPerm(t, path, 0o600)

	// Re-enable
	_, err = mgr.SetTokenDisabled(ProviderCodex, "user@example.com", false)
	if err != nil {
		t.Fatal(err)
	}
	assertPerm(t, path, 0o600)

	// Logout
	_, err = mgr.DeleteToken(ProviderCodex, "user@example.com")
	if err != nil {
		t.Fatal(err)
	}
	// File should be gone
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("token file should be deleted after logout, err=%v", err)
	}
}

// TestInstallationIDPermissionsRestrictive verifies that the installation_id
// file created by InstallationID() has restrictive permissions.
func TestInstallationIDPermissionsRestrictive(t *testing.T) {
	authDir := t.TempDir()
	mgr := NewManager(&config.Config{OAuth: config.OAuth{AuthDir: authDir}})

	id, err := mgr.InstallationID()
	if err != nil {
		t.Fatal(err)
	}
	if id == "" {
		t.Fatal("expected non-empty installation ID")
	}

	assertPerm(t, authDir, 0o700)
	idPath := filepath.Join(authDir, "installation_id")
	assertPerm(t, idPath, 0o600)
}

// TestRefreshLockDirPermissions verifies that the .locks directory created
// during refresh locking has restrictive permissions.
func TestRefreshLockDirPermissions(t *testing.T) {
	authDir := t.TempDir()
	mgr := NewManager(&config.Config{OAuth: config.OAuth{AuthDir: authDir}})

	tok := makeToken("user@example.com", "access-SENTINEL", "refresh-SENTINEL", false)
	_, err := mgr.SaveToken(tok)
	if err != nil {
		t.Fatal(err)
	}

	loaded, err := mgr.LoadToken(ProviderCodex, "user@example.com")
	if err != nil {
		t.Fatal(err)
	}

	// Acquire a refresh lock to trigger .locks directory creation
	cleanup, err := mgr.acquireRefreshFileLock(t.Context(), refreshLockKey(loaded))
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	lockDir := filepath.Join(authDir, ".locks")
	assertPerm(t, lockDir, 0o700)
}

// TestWatcherReloadDoesNotMutateTokenFileWithRuntimeState verifies that watcher
// reloads do not modify token files with runtime-only pool state.
func TestWatcherReloadDoesNotMutateTokenFileWithRuntimeState(t *testing.T) {
	dir := t.TempDir()
	mgr := newTestManager(t, dir)

	tok := makeToken("user@example.com", "access-SENTINEL", "refresh-SENTINEL", false)
	path := saveTokenFile(t, dir, tok)

	pool := NewAccountPool([]*Token{tok}, fakeTime, TestPoolLB(), nil)
	w, err := NewWatcher(mgr, pool, 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	// Wait for initial seed
	if !poolWithin(t, pool, 3*time.Second, func(s *PoolSnapshot) bool {
		return len(s.Accounts) == 1
	}) {
		t.Fatal("pool did not seed")
	}

	// Apply runtime state
	pool.Begin(path)
	pool.MarkRateLimited(path, fakeTime().Add(time.Hour))
	pool.MarkCooldown(path, fakeTime().Add(30*time.Minute))
	pool.MarkUnhealthy(path)

	// Touch the file to trigger a watcher reload
	time.Sleep(100 * time.Millisecond)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	tok.LastRefresh = now
	saveTokenFile(t, dir, tok)

	// Wait for reload
	if !poolWithin(t, pool, 3*time.Second, func(s *PoolSnapshot) bool {
		return s.Accounts[0].Selector == "user@example.com"
	}) {
		t.Fatalf("watcher did not reload; snapshot: %+v", pool.Snapshot())
	}

	// Read the file again and verify no runtime-only fields
	afterRaw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	runtimeOnlyFields := []string{
		"in_flight", "last_used", "cooldown_until", "rate_limit_until",
		"healthy", "unhealthy", "inflight", "lastUsed",
		"cooldownUntil", "rateLimitedUntil", "selector_cursor", "cursor",
	}
	for _, field := range runtimeOnlyFields {
		if strings.Contains(string(afterRaw), field) {
			t.Fatalf("token file contains runtime-only field %q after watcher reload:\n%s", field, string(afterRaw))
		}
	}

	pool.End(path)
}

// TestAccountManagement_MultipleCodexDisableLogoutSequence verifies a complex
// sequence of disable/logout operations across multiple Codex accounts
// and confirms the pool reflects each change correctly.
func TestAccountManagement_MultipleCodexDisableLogoutSequence(t *testing.T) {
	authDir := t.TempDir()
	mgr := NewManager(&config.Config{OAuth: config.OAuth{AuthDir: authDir}})

	// Save three Codex accounts
	tok1 := makeToken("user1@example.com", "access1-SENTINEL", "refresh1-SENTINEL", false)
	mgr.SaveToken(tok1)
	tok2 := makeToken("user2@example.com", "access2-SENTINEL", "refresh2-SENTINEL", false)
	mgr.SaveToken(tok2)
	tok3 := makeToken("user3@example.com", "access3-SENTINEL", "refresh3-SENTINEL", false)
	mgr.SaveToken(tok3)

	tokens, _ := mgr.LoadTokens(ProviderCodex)
	pool := NewAccountPool(tokens, fakeTime, TestPoolLB(), nil)

	if len(pool.Eligible(nil)) != 3 {
		t.Fatalf("expected 3 eligible initially, got %d", len(pool.Eligible(nil)))
	}

	// Disable user1
	mgr.SetTokenDisabled(ProviderCodex, "user1@example.com", true)
	tokens, _ = mgr.LoadTokens(ProviderCodex)
	pool.Reload(tokens)
	if len(pool.Eligible(nil)) != 2 {
		t.Fatalf("expected 2 eligible after disable user1, got %d", len(pool.Eligible(nil)))
	}

	// Logout user2
	mgr.DeleteToken(ProviderCodex, "user2@example.com")
	tokens, _ = mgr.LoadTokens(ProviderCodex)
	pool.Reload(tokens)
	if len(pool.Eligible(nil)) != 1 {
		t.Fatalf("expected 1 eligible after logout user2, got %d", len(pool.Eligible(nil)))
	}
	if len(pool.Snapshot().Accounts) != 2 {
		t.Fatalf("expected 2 accounts in snapshot (1 disabled, 1 enabled), got %d", len(pool.Snapshot().Accounts))
	}

	// Re-enable user1
	mgr.SetTokenDisabled(ProviderCodex, "user1@example.com", false)
	tokens, _ = mgr.LoadTokens(ProviderCodex)
	pool.Reload(tokens)
	if len(pool.Eligible(nil)) != 2 {
		t.Fatalf("expected 2 eligible after re-enable user1, got %d", len(pool.Eligible(nil)))
	}

	// Verify only user1 and user3 remain
	eligible := pool.Eligible(nil)
	selectors := map[string]bool{}
	for _, e := range eligible {
		selectors[e.Selector] = true
	}
	if !selectors["user1@example.com"] || !selectors["user3@example.com"] {
		t.Fatalf("expected user1 and user3 eligible, got %v", selectors)
	}
}

// TestRecordCodexUsageDoesNotLeakRuntimeState verifies that RecordCodexUsage
// (which persists quota metadata) does not write pool runtime-only fields to
// the token file.
func TestRecordCodexUsageDoesNotLeakRuntimeState(t *testing.T) {
	authDir := t.TempDir()
	mgr := NewManager(&config.Config{OAuth: config.OAuth{AuthDir: authDir}})

	tok := makeToken("user@example.com", "access-SENTINEL", "refresh-SENTINEL", false)
	path, err := mgr.SaveToken(tok)
	if err != nil {
		t.Fatal(err)
	}

	loaded, err := mgr.LoadToken(ProviderCodex, "user@example.com")
	if err != nil {
		t.Fatal(err)
	}

	pool := NewAccountPool([]*Token{loaded}, fakeTime, TestPoolLB(), nil)

	// Apply runtime state
	pool.Begin(path)
	pool.MarkRateLimited(path, fakeTime().Add(time.Hour))
	pool.MarkUnhealthy(path)

	// Record quota usage (this writes to the token file)
	resetAt := fakeTime().Add(60 * time.Second).Unix()
	quota := &CodexQuota{
		Primary: &CodexQuotaWindow{
			UsedPercent: 50,
			ResetAt:     &resetAt,
		},
	}
	resetTime := fakeTime().Add(60 * time.Second)
	err = mgr.RecordCodexUsage(loaded, quota, &resetTime)
	if err != nil {
		t.Fatal(err)
	}

	// Read the token file and verify no runtime-only fields
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	runtimeOnlyFields := []string{
		"in_flight", "last_used", "cooldown_until", "rate_limit_until",
		"healthy", "unhealthy",
	}
	for _, field := range runtimeOnlyFields {
		if strings.Contains(string(raw), field) {
			t.Fatalf("token file contains runtime-only field %q after RecordCodexUsage:\n%s", field, string(raw))
		}
	}

	// Verify codex_quota IS present (it's documented as persisted)
	if !strings.Contains(string(raw), "codex_quota") {
		t.Fatal("expected codex_quota to be persisted in token file")
	}

	// Verify rate_limit_reset_at IS present (it's documented as persisted)
	if !strings.Contains(string(raw), "rate_limit_reset_at") {
		t.Fatal("expected rate_limit_reset_at to be persisted in token file")
	}

	pool.End(path)
}
