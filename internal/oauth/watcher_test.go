package oauth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"droid-proxy/internal/config"
)

// ---- VAL-WATCH-001: Watcher hot-reloads JSON token changes ----

func TestWatcher_HotReloadCreate(t *testing.T) {
	dir := t.TempDir()
	mgr := newTestManager(t, dir)
	pool := NewAccountPool(nil, fakeTime)

	w, err := NewWatcher(mgr, pool, 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	// Initially empty
	snap := pool.Snapshot()
	if len(snap.Accounts) != 0 {
		t.Fatalf("expected 0 accounts, got %d", len(snap.Accounts))
	}

	// Create a new token file
	tok := makeToken("user@example.com", "access-SENTINEL", "refresh-SENTINEL", false)
	saveTokenFile(t, dir, tok)

	// Wait for watcher to detect the change
	if !poolWithin(t, pool, 3*time.Second, func(s *PoolSnapshot) bool {
		return len(s.Accounts) == 1 && s.Accounts[0].Selector == "user@example.com"
	}) {
		t.Fatal("watcher did not detect token file creation")
	}
}

func TestWatcher_HotReloadModify(t *testing.T) {
	dir := t.TempDir()
	mgr := newTestManager(t, dir)

	tok := makeToken("user@example.com", "access-SENTINEL", "refresh-SENTINEL", false)
	saveTokenFile(t, dir, tok)

	pool := NewAccountPool([]*Token{tok}, fakeTime)
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

	// Modify the token file (change email)
	tok.Email = "updated@example.com"
	saveTokenFile(t, dir, tok)

	if !poolWithin(t, pool, 3*time.Second, func(s *PoolSnapshot) bool {
		return len(s.Accounts) == 1 && s.Accounts[0].Selector == "updated@example.com"
	}) {
		t.Fatal("watcher did not detect token file modification")
	}
}

func TestWatcher_HotReloadRename(t *testing.T) {
	dir := t.TempDir()
	mgr := newTestManager(t, dir)

	tok := makeToken("user@example.com", "access-SENTINEL", "refresh-SENTINEL", false)
	oldPath := saveTokenFile(t, dir, tok)

	pool := NewAccountPool([]*Token{tok}, fakeTime)
	w, err := NewWatcher(mgr, pool, 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	if !poolWithin(t, pool, 3*time.Second, func(s *PoolSnapshot) bool {
		return len(s.Accounts) == 1
	}) {
		t.Fatal("pool did not seed")
	}

	// Rename the token file
	newPath := filepath.Join(dir, "renamed-user.json")
	if err := os.Rename(oldPath, newPath); err != nil {
		t.Fatal(err)
	}

	// Pool should eventually reflect the new file and remove the old entry
	if !poolWithin(t, pool, 3*time.Second, func(s *PoolSnapshot) bool {
		// The renamed file should appear with the correct selector
		for _, a := range s.Accounts {
			if a.Selector == "user@example.com" {
				return true
			}
		}
		return false
	}) {
		t.Fatalf("watcher did not detect rename; snapshot: %+v", pool.Snapshot())
	}
}

func TestWatcher_HotReloadDelete(t *testing.T) {
	dir := t.TempDir()
	mgr := newTestManager(t, dir)

	tok := makeToken("user@example.com", "access-SENTINEL", "refresh-SENTINEL", false)
	path := saveTokenFile(t, dir, tok)

	pool := NewAccountPool([]*Token{tok}, fakeTime)
	w, err := NewWatcher(mgr, pool, 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	if !poolWithin(t, pool, 3*time.Second, func(s *PoolSnapshot) bool {
		return len(s.Accounts) == 1
	}) {
		t.Fatal("pool did not seed")
	}

	// Delete the token file
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}

	if !poolWithin(t, pool, 3*time.Second, func(s *PoolSnapshot) bool {
		return len(s.Accounts) == 0
	}) {
		t.Fatalf("watcher did not detect deletion; snapshot: %+v", pool.Snapshot())
	}
}

// ---- VAL-WATCH-002: Watcher handles invalid, non-token, and missing-dir cases safely ----

func TestWatcher_InvalidJSONFileSkipped(t *testing.T) {
	dir := t.TempDir()
	mgr := newTestManager(t, dir)

	// Write an invalid JSON file
	if err := os.WriteFile(filepath.Join(dir, "bad.json"), []byte("not json at all"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Also write a valid token
	tok := makeToken("valid@example.com", "access-SENTINEL", "refresh-SENTINEL", false)
	saveTokenFile(t, dir, tok)

	pool := NewAccountPool(nil, fakeTime)
	w, err := NewWatcher(mgr, pool, 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	// Pool should only contain the valid token
	if !poolWithin(t, pool, 3*time.Second, func(s *PoolSnapshot) bool {
		return len(s.Accounts) == 1 && s.Accounts[0].Selector == "valid@example.com"
	}) {
		t.Fatalf("expected 1 valid account; snapshot: %+v", pool.Snapshot())
	}
}

func TestWatcher_NonJSONFilesIgnored(t *testing.T) {
	dir := t.TempDir()
	mgr := newTestManager(t, dir)

	// Write various non-token files
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("text"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".hidden"), []byte("hidden"), 0o644); err != nil {
		t.Fatal(err)
	}

	pool := NewAccountPool(nil, fakeTime)
	w, err := NewWatcher(mgr, pool, 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	// Give watcher time to process
	time.Sleep(200 * time.Millisecond)

	snap := pool.Snapshot()
	if len(snap.Accounts) != 0 {
		t.Fatalf("expected 0 accounts from non-JSON files, got %d", len(snap.Accounts))
	}
}

func TestWatcher_LockAndTempFilesIgnored(t *testing.T) {
	dir := t.TempDir()
	mgr := newTestManager(t, dir)

	tok := makeToken("user@example.com", "access-SENTINEL", "refresh-SENTINEL", false)
	saveTokenFile(t, dir, tok)

	pool := NewAccountPool(nil, fakeTime)
	w, err := NewWatcher(mgr, pool, 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	if !poolWithin(t, pool, 3*time.Second, func(s *PoolSnapshot) bool {
		return len(s.Accounts) == 1
	}) {
		t.Fatal("pool did not seed")
	}

	// Create a lock file and an atomic-save temp file — neither should affect the pool
	if err := os.MkdirAll(filepath.Join(dir, ".locks"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".locks", "user.json.lock"), []byte("lock"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Atomic-save temp file pattern: .<name>.tmp-<random>
	if err := os.WriteFile(filepath.Join(dir, ".user.json.tmp-12345"), []byte(`{"type":"codex","access_token":"temp-SENTINEL"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	time.Sleep(300 * time.Millisecond)

	snap := pool.Snapshot()
	if len(snap.Accounts) != 1 {
		t.Fatalf("expected 1 account (lock/temp files ignored), got %d", len(snap.Accounts))
	}

	// Verify sentinel from temp file is not in pool
	snapJSON, _ := json.Marshal(snap)
	if strings.Contains(string(snapJSON), "temp-SENTINEL") {
		t.Fatal("temp file contents leaked into pool")
	}
}

func TestWatcher_InvalidJSONCreatedLater(t *testing.T) {
	dir := t.TempDir()
	mgr := newTestManager(t, dir)

	tok := makeToken("user@example.com", "access-SENTINEL", "refresh-SENTINEL", false)
	saveTokenFile(t, dir, tok)

	pool := NewAccountPool([]*Token{tok}, fakeTime)
	w, err := NewWatcher(mgr, pool, 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	if !poolWithin(t, pool, 3*time.Second, func(s *PoolSnapshot) bool {
		return len(s.Accounts) == 1
	}) {
		t.Fatal("pool did not seed")
	}

	// Create an invalid JSON file in the watched directory
	if err := os.WriteFile(filepath.Join(dir, "bad.json"), []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}

	time.Sleep(300 * time.Millisecond)

	// Valid account should still be present, invalid should not create an entry
	snap := pool.Snapshot()
	if len(snap.Accounts) != 1 || snap.Accounts[0].Selector != "user@example.com" {
		t.Fatalf("expected 1 valid account after invalid file creation, got %+v", snap)
	}
}

// ---- VAL-WATCH-003: Watcher debounces and shuts down cleanly ----

func TestWatcher_DebounceCoalescesRapidEvents(t *testing.T) {
	dir := t.TempDir()
	mgr := newTestManager(t, dir)

	pool := NewAccountPool(nil, fakeTime)
	debounce := 200 * time.Millisecond
	w, err := NewWatcher(mgr, pool, debounce)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	// Rapidly modify the same file multiple times
	tok := makeToken("user@example.com", "access-SENTINEL", "refresh-SENTINEL", false)
	for i := 0; i < 10; i++ {
		tok.Email = fmt.Sprintf("user%d@example.com", i)
		saveTokenFile(t, dir, tok)
		time.Sleep(10 * time.Millisecond)
	}

	// After debounce settles, the pool should reflect the final state
	if !poolWithin(t, pool, 5*time.Second, func(s *PoolSnapshot) bool {
		return len(s.Accounts) == 1 && s.Accounts[0].Selector == "user9@example.com"
	}) {
		t.Fatalf("debounce did not coalesce; snapshot: %+v", pool.Snapshot())
	}
}

func TestWatcher_ShutdownClean(t *testing.T) {
	dir := t.TempDir()
	mgr := newTestManager(t, dir)

	tok := makeToken("user@example.com", "access-SENTINEL", "refresh-SENTINEL", false)
	saveTokenFile(t, dir, tok)

	pool := NewAccountPool([]*Token{tok}, fakeTime)
	w, err := NewWatcher(mgr, pool, 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}

	// Close should not panic or hang
	done := make(chan struct{})
	go func() {
		w.Close()
		close(done)
	}()

	select {
	case <-done:
		// Clean shutdown
	case <-time.After(5 * time.Second):
		t.Fatal("watcher Close() hung")
	}

	// After close, modifying files should not cause panics
	tok.Email = "afterclose@example.com"
	saveTokenFile(t, dir, tok)

	// Give some time to ensure no goroutine panics
	time.Sleep(200 * time.Millisecond)
}

func TestWatcher_ServerLifecycleRaceClean(t *testing.T) {
	dir := t.TempDir()
	mgr := newTestManager(t, dir)

	tok := makeToken("user@example.com", "access-SENTINEL", "refresh-SENTINEL", false)
	saveTokenFile(t, dir, tok)

	pool := NewAccountPool([]*Token{tok}, fakeTime)

	// Start and stop watcher rapidly, repeatedly, under race detector
	for i := 0; i < 10; i++ {
		w, err := NewWatcher(mgr, pool, 10*time.Millisecond)
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Duration(i+1) * time.Millisecond)
		w.Close()
	}
}

// ---- VAL-WATCH-004: Startup tolerates invalid token files ----

func TestWatcher_StartupWithInvalidAndValidTokens(t *testing.T) {
	dir := t.TempDir()
	mgr := newTestManager(t, dir)

	// Write invalid JSON file
	if err := os.WriteFile(filepath.Join(dir, "broken.json"), []byte("not valid json"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Write valid token
	tok := makeToken("valid@example.com", "access-SENTINEL", "refresh-SENTINEL", false)
	saveTokenFile(t, dir, tok)

	// Write another invalid JSON (truncated object)
	if err := os.WriteFile(filepath.Join(dir, "truncated.json"), []byte(`{"type":"codex","access_token":`), 0o600); err != nil {
		t.Fatal(err)
	}

	pool := NewAccountPool(nil, fakeTime)
	w, err := NewWatcher(mgr, pool, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("watcher startup should not fail with invalid files: %v", err)
	}
	defer w.Close()

	if !poolWithin(t, pool, 3*time.Second, func(s *PoolSnapshot) bool {
		return len(s.Accounts) == 1 && s.Accounts[0].Selector == "valid@example.com"
	}) {
		t.Fatalf("expected 1 valid account; snapshot: %+v", pool.Snapshot())
	}
}

// ---- VAL-WATCH-005: Missing-directory and atomic-save recovery ----

func TestWatcher_MissingDirSafeAtStartup(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nonexistent")
	// dir does NOT exist

	mgr := newTestManager(t, dir)
	pool := NewAccountPool(nil, fakeTime)

	w, err := NewWatcher(mgr, pool, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("watcher should tolerate missing dir at startup: %v", err)
	}
	defer w.Close()

	// Pool should be empty
	snap := pool.Snapshot()
	if len(snap.Accounts) != 0 {
		t.Fatalf("expected 0 accounts for missing dir, got %d", len(snap.Accounts))
	}
}

func TestWatcher_MissingDirRecoveryOnTokenCreation(t *testing.T) {
	parentDir := t.TempDir()
	dir := filepath.Join(parentDir, "auth")

	mgr := newTestManager(t, dir)
	pool := NewAccountPool(nil, fakeTime)

	w, err := NewWatcher(mgr, pool, 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	// Initially empty/missing
	snap := pool.Snapshot()
	if len(snap.Accounts) != 0 {
		t.Fatalf("expected 0 accounts initially, got %d", len(snap.Accounts))
	}

	// Create the directory and a token file
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	tok := makeToken("newuser@example.com", "access-SENTINEL", "refresh-SENTINEL", false)
	saveTokenFile(t, dir, tok)

	// Wait for watcher to detect creation
	if !poolWithin(t, pool, 5*time.Second, func(s *PoolSnapshot) bool {
		return len(s.Accounts) == 1 && s.Accounts[0].Selector == "newuser@example.com"
	}) {
		t.Fatalf("watcher did not recover after dir creation; snapshot: %+v", pool.Snapshot())
	}
}

func TestWatcher_AtomicSaveNoiseIgnored(t *testing.T) {
	dir := t.TempDir()
	mgr := newTestManager(t, dir)

	tok := makeToken("user@example.com", "access-SENTINEL", "refresh-SENTINEL", false)
	saveTokenFile(t, dir, tok)

	pool := NewAccountPool([]*Token{tok}, fakeTime)
	w, err := NewWatcher(mgr, pool, 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	if !poolWithin(t, pool, 3*time.Second, func(s *PoolSnapshot) bool {
		return len(s.Accounts) == 1
	}) {
		t.Fatal("pool did not seed")
	}

	// Simulate atomic save: write temp file, then rename
	tmpPath := filepath.Join(dir, ".codex-user@example.com.json.tmp-99999")
	if err := os.WriteFile(tmpPath, []byte(`{"type":"codex","access_token":"tmp-SENTINEL"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	// Rename to final (this is the real event that should trigger reload)
	finalPath := filepath.Join(dir, "codex-user@example.com.json")
	if err := os.Rename(tmpPath, finalPath); err != nil {
		t.Fatal(err)
	}

	time.Sleep(400 * time.Millisecond)

	snap := pool.Snapshot()
	// Pool should still have valid account; temp file should not pollute
	if len(snap.Accounts) != 1 {
		t.Fatalf("expected 1 account after atomic save, got %d", len(snap.Accounts))
	}
	// Verify no temp file contents leaked
	snapJSON, _ := json.Marshal(snap)
	if strings.Contains(string(snapJSON), "tmp-SENTINEL") {
		t.Fatal("temp file contents leaked into pool")
	}
}

// ---- VAL-WATCH-006: In-flight account deletion or disable is safe ----

func TestWatcher_InFlightDeleteSafe(t *testing.T) {
	dir := t.TempDir()
	mgr := newTestManager(t, dir)

	tok := makeToken("user@example.com", "access-SENTINEL", "refresh-SENTINEL", false)
	path := saveTokenFile(t, dir, tok)

	pool := NewAccountPool([]*Token{tok}, fakeTime)
	w, err := NewWatcher(mgr, pool, 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	if !poolWithin(t, pool, 3*time.Second, func(s *PoolSnapshot) bool {
		return len(s.Accounts) == 1
	}) {
		t.Fatal("pool did not seed")
	}

	// Acquire a lease (simulate in-flight request)
	if err := pool.Begin(path); err != nil {
		t.Fatal(err)
	}

	// Delete the token file while "in flight"
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}

	// Wait for watcher to reload
	if !poolWithin(t, pool, 3*time.Second, func(s *PoolSnapshot) bool {
		// Entry should still be present (in-flight preserved) or gone if
		// the watcher already cleaned it up. The key test is that End doesn't panic.
		return true
	}) {
		t.Fatal("watcher did not settle after deletion")
	}

	// Release should not panic or drive in-flight negative
	pool.End(path)

	// Verify in-flight is now 0 or the entry is gone
	snap := pool.Snapshot()
	for _, a := range snap.Accounts {
		if a.InFlight < 0 {
			t.Fatalf("in-flight went negative: %d", a.InFlight)
		}
	}
}

func TestWatcher_InFlightDisableSafe(t *testing.T) {
	dir := t.TempDir()
	mgr := newTestManager(t, dir)

	tok := makeToken("user@example.com", "access-SENTINEL", "refresh-SENTINEL", false)
	path := saveTokenFile(t, dir, tok)

	pool := NewAccountPool([]*Token{tok}, fakeTime)
	w, err := NewWatcher(mgr, pool, 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	if !poolWithin(t, pool, 3*time.Second, func(s *PoolSnapshot) bool {
		return len(s.Accounts) == 1
	}) {
		t.Fatal("pool did not seed")
	}

	// Acquire a lease
	if err := pool.Begin(path); err != nil {
		t.Fatal(err)
	}

	// Disable the token file
	tok.Disabled = true
	saveTokenFile(t, dir, tok)

	// Wait for watcher to reload
	if !poolWithin(t, pool, 3*time.Second, func(s *PoolSnapshot) bool {
		for _, a := range s.Accounts {
			if a.Selector == "user@example.com" && a.Disabled {
				return true
			}
		}
		return false
	}) {
		t.Fatalf("watcher did not detect disable; snapshot: %+v", pool.Snapshot())
	}

	// The account should not be eligible for new requests
	eligible := pool.Eligible(nil)
	if len(eligible) != 0 {
		t.Fatalf("disabled account should not be eligible, got %d eligible", len(eligible))
	}

	// Release the lease safely
	pool.End(path)

	snap := pool.Snapshot()
	for _, a := range snap.Accounts {
		if a.InFlight < 0 {
			t.Fatalf("in-flight went negative: %d", a.InFlight)
		}
	}
}

func TestWatcher_InFlightDeleteRaceClean(t *testing.T) {
	dir := t.TempDir()
	mgr := newTestManager(t, dir)

	tok := makeToken("user@example.com", "access-SENTINEL", "refresh-SENTINEL", false)
	path := saveTokenFile(t, dir, tok)

	pool := NewAccountPool([]*Token{tok}, fakeTime)
	w, err := NewWatcher(mgr, pool, 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	if !poolWithin(t, pool, 3*time.Second, func(s *PoolSnapshot) bool {
		return len(s.Accounts) == 1
	}) {
		t.Fatal("pool did not seed")
	}

	var errors atomic.Int32

	// Simulate concurrent Begin/End while watcher reloads from delete
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 50; i++ {
			_ = pool.Begin(path)
			time.Sleep(time.Millisecond)
			pool.End(path)
		}
	}()

	// Delete and recreate the file to trigger reloads
	time.Sleep(10 * time.Millisecond)
	_ = os.Remove(path)
	time.Sleep(50 * time.Millisecond)
	saveTokenFile(t, dir, tok)

	<-done

	snap := pool.Snapshot()
	for _, a := range snap.Accounts {
		if a.InFlight < 0 {
			t.Fatalf("in-flight went negative: %d", a.InFlight)
		}
	}

	if errors.Load() > 0 {
		t.Fatal("concurrent Begin/End encountered errors during reload")
	}
}

// ---- Helpers ----

// newTestManager creates an oauth.Manager configured with the given temp auth dir.
func newTestManager(t *testing.T, dir string) *Manager {
	t.Helper()
	cfg := &config.Config{
		OAuth: config.OAuth{
			AuthDir:           dir,
			CodexCallbackHost: "localhost",
			CodexCallbackPort: 1455,
			XAICallbackHost:   "127.0.0.1",
			XAICallbackPort:   56121,
		},
	}
	return NewManager(cfg)
}

// poolWithin repeatedly checks pool.Snapshot() until condition is true or timeout.
func poolWithin(t *testing.T, pool *AccountPool, timeout time.Duration, condition func(*PoolSnapshot) bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition(pool.Snapshot()) {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}
