package migration

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// --- Test helpers ---

// setupTxTestEnv creates an isolated temp HOME for transaction tests.
func setupTxTestEnv(t *testing.T) (home, configPath, factoryPath string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)

	configDir := filepath.Join(home, ".config", "droid-proxy")
	os.MkdirAll(configDir, 0o700)
	configPath = filepath.Join(configDir, "config.yaml")

	factoryDir := filepath.Join(home, ".factory")
	os.MkdirAll(factoryDir, 0o700)
	factoryPath = filepath.Join(factoryDir, "settings.json")

	return home, configPath, factoryPath
}

func writeTestConfig(t *testing.T, path string) {
	t.Helper()
	content := "listen:\n  host: 127.0.0.1\n  port: 8787\nmodels:\n  - alias: m\n    factory_provider: generic-chat-completion-api\n    upstream_protocol: openai-chat\n    upstream_model: m\n    base_url: http://u/v1\n    api_key_env: KEY\n"
	os.WriteFile(path, []byte(content), 0o600)
}

func writeTestConfigWithSecret(t *testing.T, path string) {
	t.Helper()
	content := "listen:\n  host: 127.0.0.1\n  port: 8787\nclient_auth:\n  enabled: true\n  api_keys:\n    - sk-test-secret\nmodels:\n  - alias: m\n    factory_provider: generic-chat-completion-api\n    upstream_protocol: openai-chat\n    upstream_model: m\n    base_url: http://u/v1\n    api_key_env: KEY\n"
	os.WriteFile(path, []byte(content), 0o600)
}

func writeTestFactory(t *testing.T, path string) {
	t.Helper()
	content := `{"customModels":[{"model":"m","provider":"generic-chat-completion-api","baseUrl":"http://127.0.0.1:8787"}]}`
	os.WriteFile(path, []byte(content), 0o600)
}

func writeTestFactoryWithSecret(t *testing.T, path string) {
	t.Helper()
	content := `{"customModels":[{"model":"m","provider":"generic-chat-completion-api","baseUrl":"http://127.0.0.1:8787","apiKey":"sk-fac-secret"}]}`
	os.WriteFile(path, []byte(content), 0o600)
}

func planAndCommit(t *testing.T, configPath, factoryPath string, opts TransactionOptions) (*TransactionResult, error) {
	t.Helper()
	plan, err := PlanMigration(PlanOptions{
		ConfigPath:  configPath,
		FactoryPath: factoryPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	return CommitTransaction(plan, opts)
}

func assertFileContains(t *testing.T, path, substr string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), substr) {
		t.Fatalf("file %s does not contain %q:\n%s", path, substr, data)
	}
}

func assertFileNotContains(t *testing.T, path, substr string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), substr) {
		t.Fatalf("file %s should not contain %q:\n%s", path, substr, data)
	}
}

func fileMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}

func dirMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	return fileMode(t, path)
}

// --- VAL-PORT-016: Migration backups and journal are durable and private ---

func TestTransactionCreatesImmutableBackups(t *testing.T) {
	home, configPath, factoryPath := setupTxTestEnv(t)
	writeTestConfig(t, configPath)
	writeTestFactory(t, factoryPath)

	configBefore, _ := os.ReadFile(configPath)
	factoryBefore, _ := os.ReadFile(factoryPath)
	configBeforeHash := sha256Hex(configBefore)
	factoryBeforeHash := sha256Hex(factoryBefore)

	result, err := planAndCommit(t, configPath, factoryPath, TransactionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Action != "migrated" {
		t.Fatalf("action = %q, want migrated", result.Action)
	}

	stateRoot := filepath.Join(home, ".droid-proxy", "migration")
	backupDir := filepath.Join(stateRoot, "backups", result.ID)

	configBackup := filepath.Join(backupDir, "config")
	factoryBackup := filepath.Join(backupDir, "factory")

	// Verify backups exist and match original hashes.
	configBackupData, err := os.ReadFile(configBackup)
	if err != nil {
		t.Fatalf("config backup missing: %v", err)
	}
	if sha256Hex(configBackupData) != configBeforeHash {
		t.Fatal("config backup hash does not match original")
	}

	factoryBackupData, err := os.ReadFile(factoryBackup)
	if err != nil {
		t.Fatalf("factory backup missing: %v", err)
	}
	if sha256Hex(factoryBackupData) != factoryBeforeHash {
		t.Fatal("factory backup hash does not match original")
	}
}

func TestTransactionBackupsImmutableAcrossCommands(t *testing.T) {
	home, configPath, factoryPath := setupTxTestEnv(t)
	writeTestConfig(t, configPath)
	writeTestFactory(t, factoryPath)

	// First migration.
	result1, err := planAndCommit(t, configPath, factoryPath, TransactionOptions{})
	if err != nil {
		t.Fatal(err)
	}

	stateRoot := filepath.Join(home, ".droid-proxy", "migration")
	backup1Dir := filepath.Join(stateRoot, "backups", result1.ID)

	configBackup1 := filepath.Join(backup1Dir, "config")
	configBackup1Data, _ := os.ReadFile(configBackup1)
	configBackup1Hash := sha256Hex(configBackup1Data)

	// Rollback.
	candidates, err := FindRollbackCandidates(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(candidates))
	}
	if err := RollbackTransaction(candidates[0]); err != nil {
		t.Fatal(err)
	}

	// Verify backup is unchanged after rollback.
	configBackup1After, _ := os.ReadFile(configBackup1)
	if sha256Hex(configBackup1After) != configBackup1Hash {
		t.Fatal("config backup changed after rollback")
	}

	// Remigrate.
	result2, err := planAndCommit(t, configPath, factoryPath, TransactionOptions{})
	if err != nil {
		t.Fatal(err)
	}

	// Verify original backup is still unchanged.
	configBackup1After2, _ := os.ReadFile(configBackup1)
	if sha256Hex(configBackup1After2) != configBackup1Hash {
		t.Fatal("config backup changed after remigration")
	}

	// New transaction should have its own distinct backup.
	if result1.ID == result2.ID {
		t.Fatal("remigration should create distinct transaction ID")
	}
}

func TestTransactionJournalIsDurableAndParseable(t *testing.T) {
	home, configPath, factoryPath := setupTxTestEnv(t)
	writeTestConfig(t, configPath)
	writeTestFactory(t, factoryPath)

	result, err := planAndCommit(t, configPath, factoryPath, TransactionOptions{})
	if err != nil {
		t.Fatal(err)
	}

	stateRoot := filepath.Join(home, ".droid-proxy", "migration")
	jPath := filepath.Join(stateRoot, "transactions", result.ID+".json")

	data, err := os.ReadFile(jPath)
	if err != nil {
		t.Fatalf("journal missing: %v", err)
	}

	var rec TransactionRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		t.Fatalf("journal not parseable: %v", err)
	}

	if rec.Status != StatusCompleted {
		t.Fatalf("status = %q, want %q", rec.Status, StatusCompleted)
	}
	if rec.Config == nil || rec.Config.OldHash == "" || rec.Config.NewHash == "" {
		t.Fatal("journal missing config hashes")
	}
	if rec.Factory == nil || rec.Factory.OldHash == "" || rec.Factory.NewHash == "" {
		t.Fatal("journal missing factory hashes")
	}
	if rec.Config.BackupPath == "" {
		t.Fatal("journal missing config backup path")
	}
	if rec.Config.Path == "" {
		t.Fatal("journal missing config path")
	}
}

func TestTransactionJournalNoSecrets(t *testing.T) {
	home, configPath, factoryPath := setupTxTestEnv(t)
	writeTestConfigWithSecret(t, configPath)
	writeTestFactoryWithSecret(t, factoryPath)

	result, err := planAndCommit(t, configPath, factoryPath, TransactionOptions{})
	if err != nil {
		t.Fatal(err)
	}

	stateRoot := filepath.Join(home, ".droid-proxy", "migration")
	jPath := filepath.Join(stateRoot, "transactions", result.ID+".json")

	data, err := os.ReadFile(jPath)
	if err != nil {
		t.Fatal(err)
	}
	journalStr := string(data)
	if strings.Contains(journalStr, "sk-test-secret") {
		t.Fatal("journal leaked config secret")
	}
	if strings.Contains(journalStr, "sk-fac-secret") {
		t.Fatal("journal leaked factory secret")
	}
}

func TestTransactionStateDirPermissions(t *testing.T) {
	home, configPath, factoryPath := setupTxTestEnv(t)
	writeTestConfig(t, configPath)

	_, err := planAndCommit(t, configPath, factoryPath, TransactionOptions{})
	if err != nil {
		t.Fatal(err)
	}

	stateRoot := filepath.Join(home, ".droid-proxy", "migration")
	txDir := filepath.Join(stateRoot, "transactions")
	bkDir := filepath.Join(stateRoot, "backups")

	for _, dir := range []string{stateRoot, txDir, bkDir} {
		mode := dirMode(t, dir)
		if mode&0o077 != 0 {
			t.Fatalf("dir %s has permissive mode %v", dir, mode)
		}
	}
}

func TestTransactionJournalFilePermissions(t *testing.T) {
	home, configPath, factoryPath := setupTxTestEnv(t)
	writeTestConfig(t, configPath)
	writeTestFactory(t, factoryPath)

	result, err := planAndCommit(t, configPath, factoryPath, TransactionOptions{})
	if err != nil {
		t.Fatal(err)
	}

	stateRoot := filepath.Join(home, ".droid-proxy", "migration")
	jPath := filepath.Join(stateRoot, "transactions", result.ID+".json")
	mode := fileMode(t, jPath)
	if mode&0o077 != 0 {
		t.Fatalf("journal file has permissive mode %v", mode)
	}
}

func TestTransactionConfigOnlyNoFactoryBackup(t *testing.T) {
	home, configPath, factoryPath := setupTxTestEnv(t)
	writeTestConfig(t, configPath)
	// No factory file.

	result, err := planAndCommit(t, configPath, factoryPath, TransactionOptions{})
	if err != nil {
		t.Fatal(err)
	}

	stateRoot := filepath.Join(home, ".droid-proxy", "migration")
	backupDir := filepath.Join(stateRoot, "backups", result.ID)

	// Config backup should exist.
	configBackup := filepath.Join(backupDir, "config")
	if _, err := os.Stat(configBackup); err != nil {
		t.Fatalf("config backup missing: %v", err)
	}

	// Factory backup should NOT exist.
	factoryBackup := filepath.Join(backupDir, "factory")
	if _, err := os.Stat(factoryBackup); !os.IsNotExist(err) {
		t.Fatal("factory backup should not exist for config-only migration")
	}
}

// --- VAL-PORT-017: Interrupted transactions recover to one coherent state ---

func TestRecoveryBeforeCommitLeavesTargetsOld(t *testing.T) {
	home, configPath, factoryPath := setupTxTestEnv(t)
	writeTestConfig(t, configPath)
	writeTestFactory(t, factoryPath)

	configBefore, _ := os.ReadFile(configPath)
	configBeforeHash := sha256Hex(configBefore)

	// Fault hook aborts before any commit (before destination check).
	_, err := planAndCommit(t, configPath, factoryPath, TransactionOptions{
		FaultHook: func(phase string) error {
			if phase == "before_destination_check" {
				return fmt.Errorf("simulated interruption")
			}
			return nil
		},
	})
	if err == nil {
		t.Fatal("expected interruption error")
	}

	// Targets should still be old.
	configAfter, _ := os.ReadFile(configPath)
	if sha256Hex(configAfter) != configBeforeHash {
		t.Fatal("config was mutated before commit phase")
	}

	// A journal should exist as in_progress or aborted.
	stateRoot := filepath.Join(home, ".droid-proxy", "migration")
	records, _ := listJournals(stateRoot)
	if len(records) != 1 {
		t.Fatalf("expected 1 journal, got %d", len(records))
	}

	// Now run a normal migration; recovery should handle the incomplete tx.
	result, err := planAndCommit(t, configPath, factoryPath, TransactionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Action != "migrated" && result.Action != "recovered" {
		t.Fatalf("unexpected action: %s", result.Action)
	}

	// Config should be migrated.
	assertFileContains(t, configPath, "port: 9787")
}

func TestRecoverySplitCommitRecoversToAllNew(t *testing.T) {
	_, configPath, factoryPath := setupTxTestEnv(t)
	writeTestConfig(t, configPath)
	writeTestFactory(t, factoryPath)

	factoryBefore, _ := os.ReadFile(factoryPath)
	factoryBeforeHash := sha256Hex(factoryBefore)

	// Fault hook aborts after config commit but before factory commit.
	_, err := planAndCommit(t, configPath, factoryPath, TransactionOptions{
		FaultHook: func(phase string) error {
			if phase == "before_factory_commit" {
				return fmt.Errorf("simulated interruption")
			}
			return nil
		},
	})
	if err == nil {
		t.Fatal("expected interruption error")
	}

	// Config should be new, factory should be old (split state).
	assertFileContains(t, configPath, "port: 9787")
	factoryAfter, _ := os.ReadFile(factoryPath)
	if sha256Hex(factoryAfter) != factoryBeforeHash {
		t.Fatal("factory should still be old after split interruption")
	}

	// Now recover.
	result, err := RecoverIncomplete(TransactionOptions{})
	if err != nil {
		t.Fatal(err)
	}

	// Both should be new after recovery.
	assertFileContains(t, configPath, "port: 9787")
	assertFileContains(t, factoryPath, ":9787")
	if result.Action != "recovered" {
		t.Fatalf("expected recovered, got %s", result.Action)
	}
}

func TestRecoveryAfterConfigOnlySplitCommit(t *testing.T) {
	_, configPath, _ := setupTxTestEnv(t)
	writeTestConfig(t, configPath)
	// No factory file.

	configBefore, _ := os.ReadFile(configPath)
	configBeforeHash := sha256Hex(configBefore)

	// Fault hook aborts before config commit.
	_, err := planAndCommit(t, configPath, "", TransactionOptions{
		FaultHook: func(phase string) error {
			if phase == "before_config_commit" {
				return fmt.Errorf("simulated interruption")
			}
			return nil
		},
	})
	if err == nil {
		t.Fatal("expected interruption error")
	}

	// Config should still be old.
	configAfter, _ := os.ReadFile(configPath)
	if sha256Hex(configAfter) != configBeforeHash {
		t.Fatal("config was mutated before commit")
	}

	// Recover and migrate.
	result, err := planAndCommit(t, configPath, "", TransactionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Action != "migrated" && result.Action != "recovered" {
		t.Fatalf("unexpected action: %s", result.Action)
	}
	assertFileContains(t, configPath, "port: 9787")
}

// --- VAL-PORT-018: Recovery refuses untrusted state and later edits ---

func TestRecoveryRefusesThirdHash(t *testing.T) {
	_, configPath, factoryPath := setupTxTestEnv(t)
	writeTestConfig(t, configPath)
	writeTestFactory(t, factoryPath)

	// Fault hook aborts after config commit (split state).
	_, err := planAndCommit(t, configPath, factoryPath, TransactionOptions{
		FaultHook: func(phase string) error {
			if phase == "before_factory_commit" {
				return fmt.Errorf("simulated interruption")
			}
			return nil
		},
	})
	if err == nil {
		t.Fatal("expected interruption error")
	}

	// User edits the config file (third hash).
	os.WriteFile(configPath, []byte("listen:\n  host: 127.0.0.1\n  port: 9787\n# user edited\n"), 0o600)

	// Recovery should refuse.
	_, err = RecoverIncomplete(TransactionOptions{})
	if err == nil {
		t.Fatal("expected recovery to refuse third hash")
	}
	if !strings.Contains(err.Error(), "third hash") && !strings.Contains(err.Error(), "unexpected content") {
		t.Fatalf("error should mention third hash: %v", err)
	}
}

func TestRecoveryRefusesThirdHashFactory(t *testing.T) {
	_, configPath, factoryPath := setupTxTestEnv(t)
	writeTestConfig(t, configPath)
	writeTestFactory(t, factoryPath)

	// Complete a migration.
	_, err := planAndCommit(t, configPath, factoryPath, TransactionOptions{})
	if err != nil {
		t.Fatal(err)
	}

	// User edits the factory file (third hash).
	os.WriteFile(factoryPath, []byte(`{"customModels":[{"model":"m","provider":"generic-chat-completion-api","baseUrl":"http://127.0.0.1:9787","apiKey":"new"}]}`), 0o600)

	// Rollback should refuse.
	candidates, _ := FindRollbackCandidates(configPath)
	if len(candidates) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(candidates))
	}
	err = RollbackTransaction(candidates[0])
	if err == nil {
		t.Fatal("expected rollback to refuse third hash")
	}
	if !strings.Contains(err.Error(), "third hash") && !strings.Contains(err.Error(), "unexpected content") {
		t.Fatalf("error should mention third hash: %v", err)
	}
}

func TestRecoveryRefusesMalformedJournal(t *testing.T) {
	home, _, _ := setupTxTestEnv(t)
	stateRoot := filepath.Join(home, ".droid-proxy", "migration")
	txDir := filepath.Join(stateRoot, "transactions")
	os.MkdirAll(txDir, 0o700)

	// Write a malformed journal.
	malformedPath := filepath.Join(txDir, "20260101-120000-abc123.json")
	os.WriteFile(malformedPath, []byte(`{bad json`), 0o600)

	// listJournals should skip it silently.
	records, err := listJournals(stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	// The malformed journal is skipped.
	for _, r := range records {
		if r.ID == "20260101-120000-abc123" {
			t.Fatal("malformed journal should be skipped")
		}
	}
}

// --- VAL-PORT-019: Concurrent migration and target-write failures remain safe ---

func TestConcurrentMigrationsSerialized(t *testing.T) {
	_, configPath, factoryPath := setupTxTestEnv(t)
	writeTestConfig(t, configPath)
	writeTestFactory(t, factoryPath)

	const N = 5
	var wg sync.WaitGroup
	errors := make([]error, N)
	results := make([]*TransactionResult, N)

	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			plan, err := PlanMigration(PlanOptions{
				ConfigPath:  configPath,
				FactoryPath: factoryPath,
			})
			if err != nil {
				errors[idx] = err
				return
			}
			result, err := CommitTransaction(plan, TransactionOptions{})
			errors[idx] = err
			results[idx] = result
		}(i)
	}
	wg.Wait()

	// At most one should succeed with "migrated" or "recovered"; the rest
	// should either be no-ops or serialized correctly.
	migratedCount := 0
	errorCount := 0
	for i := 0; i < N; i++ {
		if errors[i] != nil {
			errorCount++
		} else if results[i] != nil && (results[i].Action == "migrated" || results[i].Action == "recovered") {
			migratedCount++
		}
	}

	// The first migration migrates; subsequent ones should be no-ops since
	// the config already has 9787. But concurrent attempts should be
	// serialized by the lock.
	if migratedCount > 1 {
		t.Fatalf("expected at most 1 migration, got %d", migratedCount)
	}

	// Config should be in a coherent state (either old or new, not corrupted).
	assertFileContains(t, configPath, "port: 9787")
	assertFileContains(t, factoryPath, ":9787")
}

func TestConcurrentMigrationAndRollback(t *testing.T) {
	_, configPath, factoryPath := setupTxTestEnv(t)
	writeTestConfig(t, configPath)
	writeTestFactory(t, factoryPath)

	// First migration.
	result1, err := planAndCommit(t, configPath, factoryPath, TransactionOptions{})
	if err != nil {
		t.Fatal(err)
	}

	// Concurrent rollback and remigration attempts.
	var wg sync.WaitGroup
	wg.Add(2)

	var rollbackErr, migrateErr error

	go func() {
		defer wg.Done()
		candidates, _ := FindRollbackCandidates(configPath)
		if len(candidates) > 0 {
			// Find the right candidate.
			for _, c := range candidates {
				if c.ID == result1.ID {
					rollbackErr = RollbackTransaction(c)
					return
				}
			}
		}
	}()

	go func() {
		defer wg.Done()
		// Wait a tiny bit for potential rollback.
		plan, err := PlanMigration(PlanOptions{
			ConfigPath:  configPath,
			FactoryPath: factoryPath,
		})
		if err != nil {
			migrateErr = err
			return
		}
		_, migrateErr = CommitTransaction(plan, TransactionOptions{})
	}()

	wg.Wait()

	// Regardless of interleaving, the final state should be coherent.
	// Either everything is old (8787) or everything is new (9787).
	configData, _ := os.ReadFile(configPath)
	factoryData, _ := os.ReadFile(factoryPath)
	configStr := string(configData)
	factoryStr := string(factoryData)

	configNew := strings.Contains(configStr, "9787")
	factoryNew := strings.Contains(factoryStr, ":9787")
	configOld := strings.Contains(configStr, "8787")
	factoryOld := strings.Contains(factoryStr, ":8787")

	if configNew && factoryOld {
		t.Fatal("split state: config is new but factory is old")
	}
	if configOld && factoryNew {
		t.Fatal("split state: config is old but factory is new")
	}

	// Errors are acceptable (serialization refusal), but no panic or corruption.
	_ = rollbackErr
	_ = migrateErr
}

// --- VAL-PORT-020: Rollback restores exact originals and is idempotent ---

func TestRollbackRestoresExactOriginals(t *testing.T) {
	_, configPath, factoryPath := setupTxTestEnv(t)
	writeTestConfig(t, configPath)
	writeTestFactory(t, factoryPath)

	configBefore, _ := os.ReadFile(configPath)
	factoryBefore, _ := os.ReadFile(factoryPath)

	// Set specific modes.
	os.Chmod(configPath, 0o640)
	os.Chmod(factoryPath, 0o600)
	configModeBefore := fileMode(t, configPath)
	factoryModeBefore := fileMode(t, factoryPath)

	// Migrate.
	_, err := planAndCommit(t, configPath, factoryPath, TransactionOptions{})
	if err != nil {
		t.Fatal(err)
	}

	// Rollback.
	candidates, err := FindRollbackCandidates(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(candidates))
	}
	if err := RollbackTransaction(candidates[0]); err != nil {
		t.Fatal(err)
	}

	// Verify exact bytes restored.
	configAfter, _ := os.ReadFile(configPath)
	factoryAfter, _ := os.ReadFile(factoryPath)
	if string(configAfter) != string(configBefore) {
		t.Fatal("config not exactly restored after rollback")
	}
	if string(factoryAfter) != string(factoryBefore) {
		t.Fatal("factory not exactly restored after rollback")
	}

	// Verify modes restored.
	if fileMode(t, configPath) != configModeBefore {
		t.Fatalf("config mode changed: %v != %v", fileMode(t, configPath), configModeBefore)
	}
	if fileMode(t, factoryPath) != factoryModeBefore {
		t.Fatalf("factory mode changed: %v != %v", fileMode(t, factoryPath), factoryModeBefore)
	}
}

func TestRollbackIsIdempotent(t *testing.T) {
	_, configPath, factoryPath := setupTxTestEnv(t)
	writeTestConfig(t, configPath)
	writeTestFactory(t, factoryPath)

	// Migrate.
	planAndCommit(t, configPath, factoryPath, TransactionOptions{})

	// First rollback.
	candidates, _ := FindRollbackCandidates(configPath)
	if len(candidates) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(candidates))
	}
	if err := RollbackTransaction(candidates[0]); err != nil {
		t.Fatal(err)
	}

	// After rollback, no completed candidates should remain.
	candidates2, _ := FindRollbackCandidates(configPath)
	if len(candidates2) != 0 {
		t.Fatalf("expected 0 candidates after rollback, got %d", len(candidates2))
	}

	// Config should be back to old port.
	assertFileContains(t, configPath, "port: 8787")
}

func TestRollbackWithoutTransactionRefuses(t *testing.T) {
	_, configPath, _ := setupTxTestEnv(t)
	writeTestConfig(t, configPath)
	// Already on new port (no migration done).
	os.WriteFile(configPath, []byte("listen:\n  host: 127.0.0.1\n  port: 9787\n"), 0o600)

	candidates, err := FindRollbackCandidates(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 0 {
		t.Fatalf("expected 0 candidates, got %d", len(candidates))
	}
}

func TestRollbackConfigOnlySupported(t *testing.T) {
	_, configPath, _ := setupTxTestEnv(t)
	writeTestConfig(t, configPath)
	// No factory file.

	configBefore, _ := os.ReadFile(configPath)

	// Migrate (config-only).
	planAndCommit(t, configPath, "", TransactionOptions{})

	// Rollback.
	candidates, _ := FindRollbackCandidates(configPath)
	if len(candidates) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(candidates))
	}
	if err := RollbackTransaction(candidates[0]); err != nil {
		t.Fatal(err)
	}

	configAfter, _ := os.ReadFile(configPath)
	if string(configAfter) != string(configBefore) {
		t.Fatal("config not restored after config-only rollback")
	}
}

func TestRollbackMultipleCandidatesWithConfigSelects(t *testing.T) {
	_, configPath, factoryPath := setupTxTestEnv(t)
	writeTestConfig(t, configPath)
	writeTestFactory(t, factoryPath)

	// First migration.
	result1, _ := planAndCommit(t, configPath, factoryPath, TransactionOptions{})

	// Rollback.
	cands, _ := FindRollbackCandidates(configPath)
	RollbackTransaction(cands[0])

	// Second migration (different ID).
	result2, _ := planAndCommit(t, configPath, factoryPath, TransactionOptions{})

	// Now there should be 1 completed candidate (result2).
	cands2, _ := FindRollbackCandidates(configPath)
	if len(cands2) != 1 {
		t.Fatalf("expected 1 completed candidate, got %d", len(cands2))
	}
	if cands2[0].ID != result2.ID {
		t.Fatalf("expected latest candidate %s, got %s", result2.ID, cands2[0].ID)
	}

	// result1 should be rolled-back, not completed.
	if result1.ID == result2.ID {
		t.Fatal("transaction IDs should differ")
	}
}

// --- VAL-PORT-021: Remigration after rollback is deterministic ---

func TestRemigrationAfterRollbackDeterministic(t *testing.T) {
	home, configPath, factoryPath := setupTxTestEnv(t)
	writeTestConfig(t, configPath)
	writeTestFactory(t, factoryPath)

	// First migration.
	result1, _ := planAndCommit(t, configPath, factoryPath, TransactionOptions{})

	// Capture post-migration hashes.
	configMigrated1, _ := os.ReadFile(configPath)
	factoryMigrated1, _ := os.ReadFile(factoryPath)
	configMigratedHash1 := sha256Hex(configMigrated1)
	factoryMigratedHash1 := sha256Hex(factoryMigrated1)

	// Verify first backup.
	stateRoot := filepath.Join(home, ".droid-proxy", "migration")
	backup1Config := filepath.Join(stateRoot, "backups", result1.ID, "config")
	backup1Data, _ := os.ReadFile(backup1Config)
	backup1Hash := sha256Hex(backup1Data)

	// Rollback.
	cands, _ := FindRollbackCandidates(configPath)
	RollbackTransaction(cands[0])

	// Remigrate.
	result2, err := planAndCommit(t, configPath, factoryPath, TransactionOptions{})
	if err != nil {
		t.Fatal(err)
	}

	// Verify same post-migration hashes.
	configMigrated2, _ := os.ReadFile(configPath)
	factoryMigrated2, _ := os.ReadFile(factoryPath)
	if sha256Hex(configMigrated2) != configMigratedHash1 {
		t.Fatal("remigration produced different config hash")
	}
	if sha256Hex(factoryMigrated2) != factoryMigratedHash1 {
		t.Fatal("remigration produced different factory hash")
	}
	// Remigration creates a distinct transaction.
	if result1.ID == result2.ID {
		t.Fatal("remigration should create a distinct transaction ID")
	}

	// First backup should be unchanged.
	backup1After, _ := os.ReadFile(backup1Config)
	if sha256Hex(backup1After) != backup1Hash {
		t.Fatal("first backup changed after remigration")
	}

	// Second rollback should also succeed.
	cands2, _ := FindRollbackCandidates(configPath)
	if len(cands2) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(cands2))
	}
	if err := RollbackTransaction(cands2[0]); err != nil {
		t.Fatalf("second rollback failed: %v", err)
	}

	// Config should be back to old.
	assertFileContains(t, configPath, "port: 8787")
}

// --- VAL-PORT-013: Occupied destinations fail without contact or mutation ---

func TestOccupiedDestinationRefusesBeforeMutation(t *testing.T) {
	_, configPath, factoryPath := setupTxTestEnv(t)
	writeTestConfig(t, configPath)
	writeTestFactory(t, factoryPath)

	configBefore, _ := os.ReadFile(configPath)
	factoryBefore, _ := os.ReadFile(factoryPath)

	// Inject a destination checker that reports occupied.
	_, err := planAndCommit(t, configPath, factoryPath, TransactionOptions{
		DestinationChecker: func(host string, port int) error {
			return fmt.Errorf("destination occupied")
		},
	})
	if err == nil {
		t.Fatal("expected occupied destination refusal")
	}
	if !strings.Contains(err.Error(), "occupied") {
		t.Fatalf("error should mention occupied: %v", err)
	}

	// Targets should be unchanged.
	configAfter, _ := os.ReadFile(configPath)
	factoryAfter, _ := os.ReadFile(factoryPath)
	if string(configAfter) != string(configBefore) {
		t.Fatal("config was mutated despite occupied destination")
	}
	if string(factoryAfter) != string(factoryBefore) {
		t.Fatal("factory was mutated despite occupied destination")
	}
}

func TestAvailableDestinationAllowsMigration(t *testing.T) {
	_, configPath, factoryPath := setupTxTestEnv(t)
	writeTestConfig(t, configPath)
	writeTestFactory(t, factoryPath)

	// Destination checker reports available.
	result, err := planAndCommit(t, configPath, factoryPath, TransactionOptions{
		DestinationChecker: func(host string, port int) error {
			return nil // available
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Action != "migrated" {
		t.Fatalf("expected migrated, got %s", result.Action)
	}
	assertFileContains(t, configPath, "port: 9787")
}

// --- VAL-PORT-023: Migration diagnostics and artifacts do not leak secrets ---

func TestTransactionErrorMessagesNoSecrets(t *testing.T) {
	_, configPath, factoryPath := setupTxTestEnv(t)
	writeTestConfigWithSecret(t, configPath)
	writeTestFactoryWithSecret(t, factoryPath)

	// Cause an error with destination checker.
	_, err := planAndCommit(t, configPath, factoryPath, TransactionOptions{
		DestinationChecker: func(host string, port int) error {
			return fmt.Errorf("destination occupied")
		},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	errStr := err.Error()
	if strings.Contains(errStr, "sk-test-secret") {
		t.Fatal("error message leaked config secret")
	}
	if strings.Contains(errStr, "sk-fac-secret") {
		t.Fatal("error message leaked factory secret")
	}
}

func TestTransactionJournalBackupsContainOriginalContent(t *testing.T) {
	home, configPath, factoryPath := setupTxTestEnv(t)
	writeTestConfigWithSecret(t, configPath)
	writeTestFactoryWithSecret(t, factoryPath)

	result, _ := planAndCommit(t, configPath, factoryPath, TransactionOptions{})

	stateRoot := filepath.Join(home, ".droid-proxy", "migration")

	// Backups contain original content including secrets (they are private
	// exact copies). This is correct behavior per VAL-PORT-016.
	backupDir := filepath.Join(stateRoot, "backups", result.ID)
	configBackup, _ := os.ReadFile(filepath.Join(backupDir, "config"))
	if !strings.Contains(string(configBackup), "sk-test-secret") {
		// Backups should contain the original content.
		t.Fatal("config backup should contain original content")
	}

	// But journal should NOT contain secrets.
	journalData, _ := os.ReadFile(filepath.Join(stateRoot, "transactions", result.ID+".json"))
	if strings.Contains(string(journalData), "sk-test-secret") {
		t.Fatal("journal leaked config secret")
	}
	if strings.Contains(string(journalData), "sk-fac-secret") {
		t.Fatal("journal leaked factory secret")
	}

	// Backup files should be at most 0600.
	configBackupMode := fileMode(t, filepath.Join(backupDir, "config"))
	if configBackupMode&0o077 != 0 {
		t.Fatalf("backup file has permissive mode %v", configBackupMode)
	}
}

// --- Additional: Locked state root and path trust ---

func TestStateRootSymlinkRefused(t *testing.T) {
	home, _, _ := setupTxTestEnv(t)

	// Create a symlink as the state root.
	stateRootLink := filepath.Join(home, ".droid-proxy", "migration")
	os.MkdirAll(filepath.Join(home, ".droid-proxy"), 0o700)
	realDir := filepath.Join(home, "real-migration")
	os.MkdirAll(realDir, 0o700)
	os.Symlink(realDir, stateRootLink)

	_, configPath, factoryPath := "", filepath.Join(home, "config.yaml"), filepath.Join(home, "settings.json")
	writeTestConfig(t, configPath)
	writeTestFactory(t, factoryPath)

	// Migration should refuse because state root is a symlink.
	plan, err := PlanMigration(PlanOptions{
		ConfigPath:  configPath,
		FactoryPath: factoryPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = CommitTransaction(plan, TransactionOptions{})
	if err == nil {
		t.Fatal("expected symlink state root to be refused")
	}
}

// --- Additional: Transaction preserves modes/ownership ---

func TestTransactionPreservesModesThroughMigration(t *testing.T) {
	_, configPath, factoryPath := setupTxTestEnv(t)
	writeTestConfig(t, configPath)
	writeTestFactory(t, factoryPath)

	os.Chmod(configPath, 0o640)
	os.Chmod(factoryPath, 0o600)
	configModeBefore := fileMode(t, configPath)
	factoryModeBefore := fileMode(t, factoryPath)

	planAndCommit(t, configPath, factoryPath, TransactionOptions{})

	if fileMode(t, configPath) != configModeBefore {
		t.Fatalf("config mode changed: %v != %v", fileMode(t, configPath), configModeBefore)
	}
	if fileMode(t, factoryPath) != factoryModeBefore {
		t.Fatalf("factory mode changed: %v != %v", fileMode(t, factoryPath), factoryModeBefore)
	}
}

// --- Additional: Empty state root starts cleanly ---

func TestEmptyStateRootNoTransactions(t *testing.T) {
	_, _, _ = setupTxTestEnv(t)

	candidates, err := FindRollbackCandidates("")
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 0 {
		t.Fatalf("expected 0 candidates, got %d", len(candidates))
	}
}

// --- Additional: Rollback selection matrix ---

func TestRollbackWithoutConfigMultipleCandidates(t *testing.T) {
	_, _, _ = setupTxTestEnv(t)
	dir1 := t.TempDir()
	dir2 := t.TempDir()

	// Create two configs in different locations.
	cfg1 := filepath.Join(dir1, "config.yaml")
	cfg2 := filepath.Join(dir2, "config.yaml")
	writeTestConfig(t, cfg1)
	writeTestConfig(t, cfg2)

	// Migrate both.
	planAndCommit(t, cfg1, "", TransactionOptions{})
	planAndCommit(t, cfg2, "", TransactionOptions{})

	// Without --config, there should be multiple candidates.
	candidates, err := FindRollbackCandidates("")
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 2 {
		t.Fatalf("expected 2 candidates, got %d", len(candidates))
	}
}

func TestRollbackWithConfigSelectsLatest(t *testing.T) {
	_, _, _ = setupTxTestEnv(t)
	dir := t.TempDir()

	cfg := filepath.Join(dir, "config.yaml")
	writeTestConfig(t, cfg)

	// Migrate.
	r1, _ := planAndCommit(t, cfg, "", TransactionOptions{})

	// Rollback.
	cands, _ := FindRollbackCandidates(cfg)
	if len(cands) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(cands))
	}
	RollbackTransaction(cands[0])

	// Remigrate.
	r2, _ := planAndCommit(t, cfg, "", TransactionOptions{})

	// With --config, should select latest completed.
	cands2, _ := FindRollbackCandidates(cfg)
	if len(cands2) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(cands2))
	}
	if cands2[0].ID != r2.ID {
		t.Fatalf("expected latest candidate %s, got %s", r2.ID, cands2[0].ID)
	}

	// r1 should be rolled-back, not in candidates.
	if cands2[0].ID == r1.ID {
		t.Fatal("should select latest, not first")
	}
}

// --- Additional: Conflicting incomplete transaction blocks ---

func TestConflictingIncompleteTransactionRecoveredBeforeNewMigration(t *testing.T) {
	_, configPath, factoryPath := setupTxTestEnv(t)
	writeTestConfig(t, configPath)
	writeTestFactory(t, factoryPath)

	// Start a migration that gets interrupted after config commit (split state).
	_, _ = planAndCommit(t, configPath, factoryPath, TransactionOptions{
		FaultHook: func(phase string) error {
			if phase == "before_factory_commit" {
				return fmt.Errorf("interrupted")
			}
			return nil
		},
	})

	// Config is new, factory is old (split).
	assertFileContains(t, configPath, "port: 9787")

	// A new migration attempt should recover the split state first.
	result, err := planAndCommit(t, configPath, factoryPath, TransactionOptions{})
	if err != nil {
		t.Fatal(err)
	}

	// Both should be coherent after recovery.
	assertFileContains(t, configPath, "port: 9787")
	assertFileContains(t, factoryPath, ":9787")

	// The action should indicate recovery happened.
	if result.Action != "recovered" && result.Action != "migrated" {
		t.Fatalf("unexpected action: %s", result.Action)
	}
}

// --- Additional: Fault hook at each phase produces coherent state ---

func TestFaultAfterConfigCommitConfigOnlyRecovers(t *testing.T) {
	_, configPath, _ := setupTxTestEnv(t)
	writeTestConfig(t, configPath)

	// Config-only migration with fault after config commit (should complete
	// since there's no factory).
	_, err := planAndCommit(t, configPath, "", TransactionOptions{
		FaultHook: func(phase string) error {
			if phase == "after_commit" {
				return fmt.Errorf("interrupted")
			}
			return nil
		},
	})
	if err == nil {
		t.Fatal("expected interruption error")
	}

	// Config should be migrated but journal may be in_progress.
	assertFileContains(t, configPath, "port: 9787")

	// Recover.
	result2, err := RecoverIncomplete(TransactionOptions{})
	if err != nil {
		t.Fatal(err)
	}

	// Recovery should mark the transaction as complete.
	if result2.Action != "recovered" && result2.Action != "no-op" {
		t.Fatalf("unexpected action: %s", result2.Action)
	}

	// Config should still be migrated.
	assertFileContains(t, configPath, "port: 9787")
}

// --- Additional: Destination check is non-mutating on refusal ---

func TestDestinationCheckNonMutatingWithBackups(t *testing.T) {
	_, configPath, factoryPath := setupTxTestEnv(t)
	writeTestConfig(t, configPath)
	writeTestFactory(t, factoryPath)

	configBefore, _ := os.ReadFile(configPath)
	configBeforeHash := sha256Hex(configBefore)

	// Occupied destination.
	_, err := planAndCommit(t, configPath, factoryPath, TransactionOptions{
		DestinationChecker: func(host string, port int) error {
			return fmt.Errorf("destination occupied")
		},
	})
	if err == nil {
		t.Fatal("expected refusal")
	}

	// Targets should be unchanged.
	configAfter, _ := os.ReadFile(configPath)
	if sha256Hex(configAfter) != configBeforeHash {
		t.Fatal("config mutated despite destination refusal")
	}

	// But backups and journal should have been created (for diagnostics).
	// A subsequent normal migration should work.
	result, err := planAndCommit(t, configPath, factoryPath, TransactionOptions{
		DestinationChecker: func(host string, port int) error {
			return nil // available
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Action != "migrated" {
		t.Fatalf("expected migrated, got %s", result.Action)
	}
}

// --- Additional: Injected ports work through transaction ---

func TestTransactionWithInjectedDestinationPort(t *testing.T) {
	_, configPath, factoryPath := setupTxTestEnv(t)
	writeTestConfig(t, configPath)
	writeTestFactory(t, factoryPath)

	// Use injected ports 8787 -> 9999.
	plan, err := PlanMigration(PlanOptions{
		ConfigPath:  configPath,
		FactoryPath: factoryPath,
		OldPort:     8787,
		NewPort:     9999,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := CommitTransaction(plan, TransactionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Action != "migrated" {
		t.Fatalf("expected migrated, got %s", result.Action)
	}

	assertFileContains(t, configPath, "port: 9999")
	assertFileContains(t, factoryPath, ":9999")

	// Rollback should restore 8787.
	cands, _ := FindRollbackCandidates(configPath)
	if len(cands) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(cands))
	}
	RollbackTransaction(cands[0])

	assertFileContains(t, configPath, "port: 8787")
	assertFileContains(t, factoryPath, ":8787")
}
