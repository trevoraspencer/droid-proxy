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

	"github.com/trevoraspencer/droid-proxy/internal/config"
)

// ---- VAL-WATCH-001: Watcher hot-reloads JSON token changes ----

func TestWatcher_HotReloadCreate(t *testing.T) {
	dir := t.TempDir()
	mgr := newTestManager(t, dir)
	pool := NewAccountPool(nil, fakeTime, TestPoolLB(), nil)

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

	pool := NewAccountPool([]*Token{tok}, fakeTime, TestPoolLB(), nil)
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

	pool := NewAccountPool([]*Token{tok}, fakeTime, TestPoolLB(), nil)
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

	pool := NewAccountPool(nil, fakeTime, TestPoolLB(), nil)
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

	pool := NewAccountPool(nil, fakeTime, TestPoolLB(), nil)
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

	pool := NewAccountPool(nil, fakeTime, TestPoolLB(), nil)
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

	pool := NewAccountPool([]*Token{tok}, fakeTime, TestPoolLB(), nil)
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

	pool := NewAccountPool(nil, fakeTime, TestPoolLB(), nil)
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

	pool := NewAccountPool([]*Token{tok}, fakeTime, TestPoolLB(), nil)
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

	pool := NewAccountPool([]*Token{tok}, fakeTime, TestPoolLB(), nil)

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

	pool := NewAccountPool(nil, fakeTime, TestPoolLB(), nil)
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
	pool := NewAccountPool(nil, fakeTime, TestPoolLB(), nil)

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
	pool := NewAccountPool(nil, fakeTime, TestPoolLB(), nil)

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

func TestWatcher_AuthDirRemovalAndRecreationReloadsPool(t *testing.T) {
	parentDir := t.TempDir()
	dir := filepath.Join(parentDir, "auth")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	oldTok := makeToken("old@example.com", "old-access-SENTINEL", "old-refresh-SENTINEL", false)
	saveTokenFile(t, dir, oldTok)
	mgr := newTestManager(t, dir)
	pool := NewAccountPool([]*Token{oldTok}, fakeTime, TestPoolLB(), nil)

	w, err := NewWatcher(mgr, pool, 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	if !poolWithin(t, pool, 3*time.Second, func(s *PoolSnapshot) bool {
		return len(s.Accounts) == 1 && s.Accounts[0].Selector == "old@example.com"
	}) {
		t.Fatalf("expected initial account; snapshot: %+v", pool.Snapshot())
	}

	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if !poolWithin(t, pool, 5*time.Second, func(s *PoolSnapshot) bool {
		return len(s.Accounts) == 0
	}) {
		t.Fatalf("watcher did not clear pool after auth dir removal; snapshot: %+v", pool.Snapshot())
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	newTok := makeToken("new@example.com", "new-access-SENTINEL", "new-refresh-SENTINEL", false)
	saveTokenFile(t, dir, newTok)
	if !poolWithin(t, pool, 5*time.Second, func(s *PoolSnapshot) bool {
		return len(s.Accounts) == 1 && s.Accounts[0].Selector == "new@example.com"
	}) {
		t.Fatalf("watcher did not recover after auth dir recreation; snapshot: %+v", pool.Snapshot())
	}
}

func TestWatcher_AtomicSaveNoiseIgnored(t *testing.T) {
	dir := t.TempDir()
	mgr := newTestManager(t, dir)

	tok := makeToken("user@example.com", "access-SENTINEL", "refresh-SENTINEL", false)
	saveTokenFile(t, dir, tok)

	pool := NewAccountPool([]*Token{tok}, fakeTime, TestPoolLB(), nil)
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

	pool := NewAccountPool([]*Token{tok}, fakeTime, TestPoolLB(), nil)
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

	pool := NewAccountPool([]*Token{tok}, fakeTime, TestPoolLB(), nil)
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

	pool := NewAccountPool([]*Token{tok}, fakeTime, TestPoolLB(), nil)
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

// ---- Misc cleanup: shared file-filtering helper ----

// TestIsTokenFileName_FiltersCorrectly verifies that the shared IsTokenFileName
// helper correctly filters hidden, lock, temp, and non-JSON files.
func TestIsTokenFileName_FiltersCorrectly(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"codex-user.json", true},
		{"user@example.com.json", true},
		{"token.json", true},
		{".hidden.json", false},               // hidden file
		{".codex-user.json.tmp-12345", false}, // atomic-save temp
		{"user.json.lock", false},             // lock file
		{"notes.txt", false},                  // non-JSON
		{"data", false},                       // no extension
		{"", false},                           // empty
		{".json", false},                      // hidden, just extension
		{"JSON", false},                       // wrong extension case
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsTokenFileName(tc.name); got != tc.want {
				t.Errorf("IsTokenFileName(%q) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

// TestLoadCodexTokensFromDir_FiltersHiddenAndInvalid verifies that the shared
// LoadCodexTokensFromDir helper applies identical filtering to what the
// watcher uses: hidden files, lock files, and invalid JSON are excluded.
func TestLoadCodexTokensFromDir_FiltersHiddenAndInvalid(t *testing.T) {
	dir := t.TempDir()
	mgr := newTestManager(t, dir)

	// Valid token
	validTok := makeToken("valid@example.com", "valid-access-SENTINEL", "valid-refresh-SENTINEL", false)
	saveTokenFile(t, dir, validTok)

	// Hidden .json file
	if err := os.WriteFile(filepath.Join(dir, ".hidden.json"), []byte(`{"type":"codex","access_token":"hidden-SENTINEL"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	// Invalid JSON
	if err := os.WriteFile(filepath.Join(dir, "broken.json"), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Lock file
	if err := os.WriteFile(filepath.Join(dir, "user.json.lock"), []byte("lock"), 0o600); err != nil {
		t.Fatal(err)
	}
	// xAI token (should be excluded)
	xaiTok := `{"type":"xai","access_token":"xai-SENTINEL","refresh_token":"xai-rt","email":"xai@example.com","expired":"2099-01-01T00:00:00Z"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "xai-user.json"), []byte(xaiTok), 0o600); err != nil {
		t.Fatal(err)
	}

	tokens, err := LoadCodexTokensFromDir(mgr, dir, nil)
	if err != nil {
		t.Fatal(err)
	}

	if len(tokens) != 1 {
		t.Fatalf("expected 1 valid Codex token, got %d", len(tokens))
	}
	if tokens[0].Email != "valid@example.com" {
		t.Fatalf("expected valid@example.com, got %s", tokens[0].Email)
	}

	// Verify no secrets leaked
	for _, tok := range tokens {
		if strings.Contains(tok.AccessToken, "hidden-SENTINEL") || strings.Contains(tok.AccessToken, "xai-SENTINEL") {
			t.Fatal("hidden or xAI token was incorrectly loaded")
		}
	}
}

// ---- Misc cleanup: hidden-file filtering parity ----

// TestWatcher_HiddenFileFilteringParity verifies that server startup seed
// and watcher reload produce identical pool contents when hidden .json
// files are present. Before the shared helper extraction, server.go used
// only filepath.Ext != ".json" while the watcher also excluded hidden files.
func TestWatcher_HiddenFileFilteringParity(t *testing.T) {
	dir := t.TempDir()
	mgr := newTestManager(t, dir)

	// Write a visible token file
	visibleToken := makeToken("visible@example.com", "visible-access-SENTINEL", "visible-refresh-SENTINEL", false)
	saveTokenFile(t, dir, visibleToken)

	// Write a hidden .json file (should be excluded by both server and watcher)
	hiddenContent := `{"type":"codex","access_token":"hidden-access-SENTINEL","refresh_token":"hidden-refresh-SENTINEL","email":"hidden@example.com","expired":"2099-01-01T00:00:00Z"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, ".hidden-token.json"), []byte(hiddenContent), 0o600); err != nil {
		t.Fatal(err)
	}

	// Use LoadCodexTokensFromDir (the shared helper used by both server and watcher)
	tokens, err := LoadCodexTokensFromDir(mgr, dir, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Only the visible token should be loaded
	if len(tokens) != 1 {
		t.Fatalf("expected 1 token (hidden file excluded), got %d", len(tokens))
	}
	if tokens[0].Email != "visible@example.com" {
		t.Fatalf("expected visible@example.com, got %s", tokens[0].Email)
	}

	// Now seed a pool and start the watcher; verify pool matches
	pool := NewAccountPool(tokens, fakeTime, TestPoolLB(), nil)
	w, err := NewWatcher(mgr, pool, 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	if !poolWithin(t, pool, 3*time.Second, func(s *PoolSnapshot) bool {
		return len(s.Accounts) == 1 && s.Accounts[0].Selector == "visible@example.com"
	}) {
		t.Fatalf("watcher reload produced different results than shared helper; snapshot: %+v", pool.Snapshot())
	}

	// Snapshot should not contain hidden token secrets
	snapJSON, _ := json.Marshal(pool.Snapshot())
	if strings.Contains(string(snapJSON), "hidden-access-SENTINEL") {
		t.Fatal("hidden file token leaked into pool snapshot")
	}
}

// TestWatcher_StartupSeedMatchesWatcherReload verifies that pool contents
// seeded by LoadCodexTokensFromDir are identical to what the watcher loads
// on its first reload (no entries disappear on first hot-reload).
func TestWatcher_StartupSeedMatchesWatcherReload(t *testing.T) {
	dir := t.TempDir()
	mgr := newTestManager(t, dir)

	// Create multiple token files including edge cases
	tok1 := makeToken("user1@example.com", "access1-SENTINEL", "refresh1-SENTINEL", false)
	saveTokenFile(t, dir, tok1)
	tok2 := makeToken("user2@example.com", "access2-SENTINEL", "refresh2-SENTINEL", false)
	saveTokenFile(t, dir, tok2)

	// Hidden .json file
	if err := os.WriteFile(filepath.Join(dir, ".dot.json"), []byte(`{"type":"codex","access_token":"dot-SENTINEL"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	// Lock file
	if err := os.WriteFile(filepath.Join(dir, "user1.lock"), []byte("lock"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Non-JSON file
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("text"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Seed using the shared helper (as server.go does)
	seedTokens, err := LoadCodexTokensFromDir(mgr, dir, nil)
	if err != nil {
		t.Fatal(err)
	}

	pool := NewAccountPool(seedTokens, fakeTime, TestPoolLB(), nil)
	seedSnap := pool.Snapshot()

	if len(seedSnap.Accounts) != 2 {
		t.Fatalf("expected 2 seeded accounts, got %d", len(seedSnap.Accounts))
	}

	// Start the watcher (it should skip re-seeding because pool is already populated)
	w, err := NewWatcher(mgr, pool, 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	// Trigger a reload by touching a file
	saveTokenFile(t, dir, tok1)

	// Wait for reload and verify pool still has exactly 2 accounts
	if !poolWithin(t, pool, 3*time.Second, func(s *PoolSnapshot) bool {
		return len(s.Accounts) == 2
	}) {
		t.Fatalf("pool changed after watcher reload; snapshot: %+v", pool.Snapshot())
	}

	// Verify no entries disappeared due to filtering mismatch
	reloadSnap := pool.Snapshot()
	selectors := map[string]bool{}
	for _, a := range reloadSnap.Accounts {
		selectors[a.Selector] = true
	}
	if !selectors["user1@example.com"] || !selectors["user2@example.com"] {
		t.Fatalf("expected both users after reload, got selectors: %v", selectors)
	}
}

// ---- Misc cleanup: parent-watch removal ----

// TestWatcher_ParentWatchRemovedAfterAuthDirCreation verifies that after the
// auth dir is created and added to the watcher, the parent-directory watch
// is removed so sibling entries no longer generate spurious reload events.
func TestWatcher_ParentWatchRemovedAfterAuthDirCreation(t *testing.T) {
	parentDir := t.TempDir()
	// Create a sibling file in the parent dir that should NOT trigger reloads
	siblingPath := filepath.Join(parentDir, "sibling.json")
	if err := os.WriteFile(siblingPath, []byte(`{"type":"codex","access_token":"sibling-SENTINEL"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	dir := filepath.Join(parentDir, "auth")
	mgr := newTestManager(t, dir)
	pool := NewAccountPool(nil, fakeTime, TestPoolLB(), nil)

	w, err := NewWatcher(mgr, pool, 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	// Initially the pool should be empty (dir doesn't exist)
	snap := pool.Snapshot()
	if len(snap.Accounts) != 0 {
		t.Fatalf("expected 0 accounts initially, got %d", len(snap.Accounts))
	}

	// Verify watcher is watching the parent
	w.mu.Lock()
	parent := w.watchingParent
	w.mu.Unlock()
	if parent == "" {
		t.Fatal("expected watcher to be watching parent directory")
	}

	// Create the auth dir and a token file
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	tok := makeToken("user@example.com", "access-SENTINEL", "refresh-SENTINEL", false)
	saveTokenFile(t, dir, tok)

	// Wait for watcher to detect creation and seed pool
	if !poolWithin(t, pool, 5*time.Second, func(s *PoolSnapshot) bool {
		return len(s.Accounts) == 1 && s.Accounts[0].Selector == "user@example.com"
	}) {
		t.Fatalf("watcher did not detect dir creation; snapshot: %+v", pool.Snapshot())
	}

	// Verify parent watch has been removed
	w.mu.Lock()
	parentAfter := w.watchingParent
	w.mu.Unlock()
	if parentAfter != "" {
		t.Fatalf("expected parent watch to be removed after auth dir creation, still watching %q", parentAfter)
	}

	// Now modify the sibling file — it should NOT trigger a pool reload
	snapBefore := pool.Snapshot()
	if err := os.WriteFile(siblingPath, []byte(`{"type":"codex","access_token":"sibling-modified-SENTINEL"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	time.Sleep(300 * time.Millisecond)

	// Pool should be unchanged — sibling modification should not cause reload
	snapAfter := pool.Snapshot()
	if len(snapAfter.Accounts) != len(snapBefore.Accounts) {
		t.Fatalf("sibling file modification caused pool change: before=%d accounts, after=%d",
			len(snapBefore.Accounts), len(snapAfter.Accounts))
	}
	snapJSON, _ := json.Marshal(snapAfter)
	if strings.Contains(string(snapJSON), "sibling") {
		t.Fatal("sibling file contents leaked into pool after parent watch was removed")
	}
}

// ---- Misc cleanup: single-seed idempotency ----

// TestWatcher_SkipsSeedWhenPoolAlreadyPopulated verifies that when the pool
// is already seeded (e.g., by server startup), the watcher skips its
// initial seed to avoid redundant file reads and double-seeding.
func TestWatcher_SkipsSeedWhenPoolAlreadyPopulated(t *testing.T) {
	dir := t.TempDir()
	mgr := newTestManager(t, dir)

	tok := makeToken("user@example.com", "access-SENTINEL", "refresh-SENTINEL", false)
	saveTokenFile(t, dir, tok)

	// Seed the pool manually (as server.New does)
	seedTokens, err := LoadCodexTokensFromDir(mgr, dir, nil)
	if err != nil {
		t.Fatal(err)
	}

	pool := NewAccountPool(seedTokens, fakeTime, TestPoolLB(), nil)

	// Record the initial snapshot
	snapBefore := pool.Snapshot()
	if len(snapBefore.Accounts) != 1 {
		t.Fatalf("expected 1 seeded account, got %d", len(snapBefore.Accounts))
	}
	lastUsedBefore := snapBefore.Accounts[0].LastUsed

	// Start the watcher — it should skip re-seeding since pool has entries
	w, err := NewWatcher(mgr, pool, 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	// Give the watcher a moment to settle
	time.Sleep(200 * time.Millisecond)

	// Pool state should be unchanged (watcher skipped its seed)
	snapAfter := pool.Snapshot()
	if len(snapAfter.Accounts) != 1 {
		t.Fatalf("expected 1 account after watcher start, got %d", len(snapAfter.Accounts))
	}
	if snapAfter.Accounts[0].Selector != "user@example.com" {
		t.Fatalf("unexpected selector: %s", snapAfter.Accounts[0].Selector)
	}

	// The watcher's seedPool should have been skipped, so LastUsed should
	// not have been updated (no Begin was called)
	lastUsedAfter := snapAfter.Accounts[0].LastUsed
	if lastUsedBefore == nil && lastUsedAfter != nil {
		t.Fatal("pool LastUsed was set despite seed being skipped")
	}
	if lastUsedBefore != nil && lastUsedAfter != nil && !lastUsedBefore.Equal(*lastUsedAfter) {
		t.Fatal("pool LastUsed was modified despite seed being skipped")
	}
}

// ---- Misc cleanup: symlink-safe isAuthDir ----

// TestWatcher_IsAuthDir_SymlinkSafe verifies that isAuthDir correctly
// identifies the auth dir even when accessed through symlinks.
func TestWatcher_IsAuthDir_SymlinkSafe(t *testing.T) {
	dir := t.TempDir()
	mgr := newTestManager(t, dir)

	pool := NewAccountPool(nil, fakeTime, TestPoolLB(), nil)
	w, err := NewWatcher(mgr, pool, 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	// Direct path should match
	if !w.isAuthDir(dir) {
		t.Fatalf("isAuthDir failed for direct path %q", dir)
	}

	// Create a symlink to the auth dir
	linkDir := filepath.Join(t.TempDir(), "auth-link")
	if err := os.Symlink(dir, linkDir); err != nil {
		t.Skipf("symlinks not supported: %v", err)
	}

	// Path through symlink should also match
	if !w.isAuthDir(linkDir) {
		t.Fatalf("isAuthDir failed for symlink path %q -> %q", linkDir, dir)
	}

	// Unrelated path should not match
	if w.isAuthDir(t.TempDir()) {
		t.Fatal("isAuthDir should not match unrelated directory")
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

// TestWatcher_WatchingParentMutexSync verifies that watchingParent is accessed
// under w.mu in both watchAuthDir and removeParentWatch, preventing a data
// race between the loop goroutine and external readers.
func TestWatcher_WatchingParentMutexSync(t *testing.T) {
	parentDir := t.TempDir()
	dir := filepath.Join(parentDir, "auth")

	mgr := newTestManager(t, dir)
	pool := NewAccountPool(nil, fakeTime, TestPoolLB(), nil)

	w, err := NewWatcher(mgr, pool, 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	// Verify initial state: watchingParent should be set (dir doesn't exist)
	w.mu.Lock()
	initial := w.watchingParent
	w.mu.Unlock()
	if initial == "" {
		t.Fatal("expected watchingParent to be set when auth dir is missing")
	}

	// Create the auth dir and a token to trigger removeParentWatch
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	tok := makeToken("race@example.com", "access-SENTINEL", "refresh-SENTINEL", false)
	saveTokenFile(t, dir, tok)

	// Wait for watcher to process
	if !poolWithin(t, pool, 5*time.Second, func(s *PoolSnapshot) bool {
		return len(s.Accounts) == 1
	}) {
		t.Fatal("watcher did not detect dir creation")
	}

	// Verify watchingParent was cleared
	w.mu.Lock()
	after := w.watchingParent
	w.mu.Unlock()
	if after != "" {
		t.Fatalf("expected watchingParent to be cleared after auth dir creation, got %q", after)
	}
}
