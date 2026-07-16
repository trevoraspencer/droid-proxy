package migration

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// TransactionOptions provides test seams for the transaction layer.
type TransactionOptions struct {
	// StateRoot overrides the migration state root directory. When empty,
	// the default (~/.droid-proxy/migration) is used.
	StateRoot string

	// DestinationChecker verifies that the migration destination port is
	// available for binding. When non-nil and it returns an error, the
	// transaction refuses before any target mutation. When nil, no
	// destination check is performed (config-only migration).
	DestinationChecker func(host string, port int) error

	// FaultHook is called at each commit boundary for fault injection. A
	// non-nil error aborts the transaction at that point, simulating
	// interruption. The phase argument identifies the boundary:
	// "before_destination_check", "before_config_commit",
	// "before_factory_commit", "after_commit".
	FaultHook func(phase string) error
}

// TransactionResult describes the outcome of a commit or recovery operation.
type TransactionResult struct {
	ID     string
	Action string // "migrated", "recovered", "rolled-back", "no-op"
}

// resolveStateRoot returns the effective state root from options or the
// default.
func resolveStateRoot(opts TransactionOptions) string {
	if opts.StateRoot != "" {
		return opts.StateRoot
	}
	return StateRoot()
}

// CommitTransaction creates and commits a migration transaction for the given
// plan. It creates immutable backups, writes a durable journal, stages and
// validates outputs, checks the destination (if configured), commits targets
// in order with journaled progress, and marks the transaction complete.
//
// If an incomplete transaction exists, it is recovered first.
func CommitTransaction(plan *Plan, opts TransactionOptions) (*TransactionResult, error) {
	if opts.StateRoot != "" {
		prev := stateRootOverride
		stateRootOverride = opts.StateRoot
		defer func() { stateRootOverride = prev }()
	}

	lock, err := AcquireLock()
	if err != nil {
		return nil, err
	}
	defer lock.Release()

	stateRoot, err := ensureStateRoot()
	if err != nil {
		return nil, err
	}

	// Recover any incomplete transaction before starting a new one.
	if recoveryResult, err := recoverIncompleteLocked(stateRoot, opts); err != nil {
		return nil, fmt.Errorf("recovery: %w", err)
	} else if recoveryResult != nil && recoveryResult.Action != "no-op" {
		// Recovery modified targets; we need to re-plan.
		return recoveryResult, nil
	}

	if !plan.HasChanges() || plan.FactoryUnsafe {
		if plan.FactoryUnsafe {
			return nil, fmt.Errorf("aborting: %s", plan.FactoryReason)
		}
		return &TransactionResult{Action: "no-op"}, nil
	}

	// Canonicalize and trust-check target paths.
	configAbs, err := filepath.Abs(plan.ConfigPath)
	if err != nil {
		return nil, fmt.Errorf("resolve config path: %w", err)
	}
	if err := checkTargetTrust(configAbs); err != nil {
		return nil, err
	}

	var factoryAbs string
	if plan.FactoryPresent && len(plan.FactoryChanges) > 0 {
		factoryAbs, err = filepath.Abs(plan.FactoryPath)
		if err != nil {
			return nil, fmt.Errorf("resolve factory path: %w", err)
		}
		if err := checkTargetTrust(factoryAbs); err != nil {
			return nil, err
		}
	}

	// Generate transaction ID and record.
	txID := generateTxID(configAbs)
	rec := &TransactionRecord{
		ID:        txID,
		Status:    StatusInProgress,
		CreatedAt: nowISO(),
		OldPort:   plan.OldPort,
		NewPort:   plan.NewPort,
		Host:      plan.Host,
	}

	// Compute hashes and create backups.
	configTarget, err := prepareTarget(plan.ConfigPath, configAbs, stateRoot, txID, "config", plan.configRaw)
	if err != nil {
		return nil, err
	}
	rec.Config = configTarget

	if factoryAbs != "" && plan.factoryRaw != nil {
		factoryTarget, err := prepareTarget(plan.FactoryPath, factoryAbs, stateRoot, txID, "factory", plan.factoryRaw)
		if err != nil {
			return nil, err
		}
		rec.Factory = factoryTarget
	}

	// Generate staged new content.
	stagedConfig, err := generateStagedConfig(plan)
	if err != nil {
		return nil, fmt.Errorf("stage config: %w", err)
	}
	if err := writePrivateFile(rec.Config.StagedPath, stagedConfig); err != nil {
		return nil, fmt.Errorf("write staged config: %w", err)
	}
	rec.Config.NewHash = sha256Hex(stagedConfig)

	if rec.Factory != nil {
		stagedFactory, err := generateStagedFactory(plan)
		if err != nil {
			return nil, fmt.Errorf("stage factory: %w", err)
		}
		if err := writePrivateFile(rec.Factory.StagedPath, stagedFactory); err != nil {
			return nil, fmt.Errorf("write staged factory: %w", err)
		}
		rec.Factory.NewHash = sha256Hex(stagedFactory)
	}

	// Write initial journal before any mutation.
	if err := writeJournal(stateRoot, rec); err != nil {
		return nil, fmt.Errorf("write journal: %w", err)
	}

	// Destination check (if configured).
	if opts.FaultHook != nil {
		if err := opts.FaultHook("before_destination_check"); err != nil {
			return nil, fmt.Errorf("aborted before destination check: %w", err)
		}
	}
	if opts.DestinationChecker != nil {
		if err := opts.DestinationChecker(plan.Host, plan.NewPort); err != nil {
			// Refuse before mutation. Clean up staged files but keep journal
			// as aborted for diagnostics.
			rec.Status = StatusAborted
			rec.AbortedAt = nowISO()
			_ = writeJournal(stateRoot, rec)
			cleanupStagedFiles(rec)
			return nil, fmt.Errorf("destination unavailable: %w", err)
		}
	}

	// Commit config.
	if opts.FaultHook != nil {
		if err := opts.FaultHook("before_config_commit"); err != nil {
			return nil, fmt.Errorf("aborted before config commit: %w", err)
		}
	}
	if err := commitTarget(rec.Config); err != nil {
		return nil, fmt.Errorf("commit config: %w", err)
	}
	rec.Config.Committed = true
	_ = writeJournal(stateRoot, rec)

	// Commit factory (if present).
	if rec.Factory != nil {
		if opts.FaultHook != nil {
			if err := opts.FaultHook("before_factory_commit"); err != nil {
				return nil, fmt.Errorf("aborted before factory commit: %w", err)
			}
		}
		if err := commitTarget(rec.Factory); err != nil {
			return nil, fmt.Errorf("commit factory: %w", err)
		}
		rec.Factory.Committed = true
		_ = writeJournal(stateRoot, rec)
	}

	if opts.FaultHook != nil {
		if err := opts.FaultHook("after_commit"); err != nil {
			return nil, fmt.Errorf("aborted after commit: %w", err)
		}
	}

	// Mark complete.
	rec.Status = StatusCompleted
	rec.CompletedAt = nowISO()
	if err := writeJournal(stateRoot, rec); err != nil {
		return nil, fmt.Errorf("mark complete: %w", err)
	}

	// Terminal state: remove staged full-file intermediates. Immutable
	// backups and the journal are retained.
	cleanupStagedFiles(rec)

	return &TransactionResult{ID: txID, Action: "migrated"}, nil
}

// prepareTarget computes the old hash, mode, backup path, and staged path for
// a migration target. It creates the immutable exact backup.
func prepareTarget(displayPath, absPath, stateRoot, txID, name string, originalData []byte) (*TargetRecord, error) {
	info, err := os.Stat(absPath)
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", name, err)
	}
	mode := formatMode(uint32(info.Mode().Perm()))

	backupDir := filepath.Join(stateRoot, "backups", txID)
	if err := mkdirPrivate(backupDir); err != nil {
		return nil, fmt.Errorf("create backup dir: %w", err)
	}

	backupPath := filepath.Join(backupDir, name)
	// Write exact backup with at most 0600.
	if err := writePrivateFile(backupPath, originalData); err != nil {
		return nil, fmt.Errorf("write backup %s: %w", name, err)
	}
	// Verify backup hash matches original.
	backupHash := sha256Hex(originalData)

	stagedPath := filepath.Join(backupDir, name+".staged")

	return &TargetRecord{
		Path:       absPath,
		OldHash:    backupHash,
		BackupPath: backupPath,
		StagedPath: stagedPath,
		Mode:       mode,
		Committed:  false,
	}, nil
}

// commitTarget writes the staged content to the target path, preserving the
// original file mode and ownership. It rechecks the target identity (regular,
// user-owned) and verifies that the current file hash still matches the
// expected old hash before writing. If the file was edited between planning
// and commit (third hash), the commit refuses to overwrite the user's edit.
func commitTarget(t *TargetRecord) error {
	// Recheck trust before writing.
	if err := checkTargetTrust(t.Path); err != nil {
		return err
	}
	// Recheck that the target still has the expected old hash. If the
	// file changed after planning, refuse to overwrite the user's edit.
	currentHash, err := hashFile(t.Path)
	if err != nil {
		return fmt.Errorf("recheck target hash %s: %w", t.Path, err)
	}
	if currentHash != t.OldHash {
		return fmt.Errorf("target %s has unexpected content (hash changed after planning); manual intervention required; backup at %s", t.Path, t.BackupPath)
	}
	// Read staged content.
	stagedData, err := os.ReadFile(t.StagedPath)
	if err != nil {
		return fmt.Errorf("read staged: %w", err)
	}
	// Verify staged hash.
	if sha256Hex(stagedData) != t.NewHash {
		return fmt.Errorf("staged content hash mismatch for %s", t.Path)
	}
	// Write preserving mode/owner.
	return writeFilePreservingMode(t.Path, stagedData)
}

// generateStagedConfig produces the rewritten config bytes from the plan.
func generateStagedConfig(plan *Plan) ([]byte, error) {
	if !plan.ConfigEligible || plan.configAnalysis == nil || plan.configAnalysis.PortNode == nil {
		return nil, fmt.Errorf("config not eligible for rewrite")
	}
	return RewriteListenPortScalar(
		plan.configRaw,
		plan.configAnalysis.PortNode,
		plan.OldPort,
		plan.NewPort,
	)
}

// generateStagedFactory produces the rewritten factory bytes from the plan.
func generateStagedFactory(plan *Plan) ([]byte, error) {
	if len(plan.FactoryChanges) == 0 || plan.factoryRaw == nil {
		return nil, fmt.Errorf("no factory changes")
	}
	return RewriteFactory(plan.factoryRaw, plan.FactoryChanges)
}

// checkTargetTrust verifies that a target file path is regular, user-owned,
// and free of user-replaceable symlinks. It rechecks at commit time for
// TOCTOU protection.
func checkTargetTrust(absPath string) error {
	trust, err := CheckFileTrust(absPath)
	if err != nil {
		return err
	}
	if !trust.Trusted {
		return fmt.Errorf("%s", trust.Reason)
	}
	return nil
}

// cleanupStagedFiles removes all *.staged full-file intermediates from a
// transaction record's targets. It is called after a terminal state
// (completed, rolled-back, or safely aborted) so that secret-bearing staged
// data does not persist beyond what is necessary for recovery. Immutable
// backups and the journal are always retained.
func cleanupStagedFiles(rec *TransactionRecord) {
	for _, t := range rec.targets() {
		if t.StagedPath != "" {
			os.Remove(t.StagedPath)
		}
	}
}

// recoverIncompleteLocked scans for in-progress transactions and recovers
// them to a coherent state. Must be called with the lock held.
func recoverIncompleteLocked(stateRoot string, opts TransactionOptions) (*TransactionResult, error) {
	records, err := listJournals(stateRoot)
	if err != nil {
		return nil, err
	}

	for _, rec := range records {
		if rec.Status != StatusInProgress {
			continue
		}

		// Verify journal file trust.
		jPath := journalPath(stateRoot, rec.ID)
		if err := checkTargetTrust(jPath); err != nil {
			// Untrusted journal: refuse to act on it.
			return nil, fmt.Errorf("untrusted journal %s: %w", rec.ID, err)
		}

		result, err := recoverOne(stateRoot, rec)
		if err != nil {
			return nil, fmt.Errorf("recover transaction %s: %w", rec.ID, err)
		}
		if result != nil && result.Action != "no-op" {
			return result, nil
		}
	}
	return &TransactionResult{Action: "no-op"}, nil
}

// recoverOne recovers a single in-progress transaction to a coherent state.
func recoverOne(stateRoot string, rec *TransactionRecord) (*TransactionResult, error) {
	// Determine the state of each target.
	configState, err := assessTarget(rec.Config)
	if err != nil {
		return nil, err
	}
	factoryState := targetOld // absent factory = old (nothing to do)
	if rec.Factory != nil {
		factoryState, err = assessTarget(rec.Factory)
		if err != nil {
			return nil, err
		}
	}

	// Determine coherent recovery action.
	allOld := configState == targetOld && factoryState == targetOld
	allNew := configState == targetNew && factoryState == targetNew
	split := !allOld && !allNew

	if allOld {
		// Nothing was committed. Abort the transaction; targets are
		// already in their original state. Return no-op so the caller
		// proceeds with a new migration.
		rec.Status = StatusAborted
		rec.AbortedAt = nowISO()
		if err := writeJournal(stateRoot, rec); err != nil {
			return nil, err
		}
		// Terminal state: clean up staged intermediates.
		cleanupStagedFiles(rec)
		return &TransactionResult{Action: "no-op"}, nil
	}

	if allNew {
		// Both targets were committed but journal wasn't finalized.
		rec.Status = StatusCompleted
		rec.CompletedAt = nowISO()
		if rec.Config != nil {
			rec.Config.Committed = true
		}
		if rec.Factory != nil {
			rec.Factory.Committed = true
		}
		if err := writeJournal(stateRoot, rec); err != nil {
			return nil, err
		}
		// Terminal state: clean up staged intermediates.
		cleanupStagedFiles(rec)
		return &TransactionResult{ID: rec.ID, Action: "recovered"}, nil
	}

	if split {
		// One target is new, the other is old. Roll forward: commit the
		// remaining target from its staged content.
		return recoverSplitForward(stateRoot, rec, configState, factoryState)
	}

	return &TransactionResult{Action: "no-op"}, nil
}

// targetAssessment describes the observed state of a target file relative to
// the transaction's expected hashes.
type targetAssessment int

const (
	targetOld        targetAssessment = iota // current hash == old hash
	targetNew                                // current hash == new hash
	targetThird                              // current hash matches neither (third hash)
	targetMissing                            // file does not exist
	targetUnreadable                         // file exists but cannot be read
)

// assessTarget reads the current target file and compares its hash to the
// expected old and new hashes.
func assessTarget(t *TargetRecord) (targetAssessment, error) {
	if t == nil {
		return targetOld, nil
	}
	info, err := os.Stat(t.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return targetMissing, nil
		}
		return targetUnreadable, nil
	}
	if !info.Mode().IsRegular() {
		return targetThird, nil
	}
	currentHash, err := hashFile(t.Path)
	if err != nil {
		return targetUnreadable, nil
	}
	if currentHash == t.OldHash {
		return targetOld, nil
	}
	if currentHash == t.NewHash {
		return targetNew, nil
	}
	// Third hash: user edited the file after migration started.
	return targetThird, nil
}

// recoverSplitForward handles the split-state case by rolling forward:
// committing the remaining uncommitted target from its staged content.
// If the staged content cannot be verified or the target has a third hash,
// recovery refuses.
func recoverSplitForward(stateRoot string, rec *TransactionRecord, configState, factoryState targetAssessment) (*TransactionResult, error) {
	// If any target has a third hash, refuse.
	if configState == targetThird || factoryState == targetThird {
		return nil, fmt.Errorf("recovery refused: target has unexpected content (third hash); manual intervention required; backup at %s",
			backupPathForDiagnostics(rec))
	}
	if configState == targetMissing || factoryState == targetMissing {
		return nil, fmt.Errorf("recovery refused: target file is missing; backup at %s",
			backupPathForDiagnostics(rec))
	}

	// Roll forward: commit the uncommitted target.
	if rec.Config != nil && !rec.Config.Committed && configState == targetOld {
		if err := commitTarget(rec.Config); err != nil {
			return nil, err
		}
		rec.Config.Committed = true
	}
	if rec.Factory != nil && !rec.Factory.Committed && factoryState == targetOld {
		if err := commitTarget(rec.Factory); err != nil {
			return nil, err
		}
		rec.Factory.Committed = true
	}

	rec.Status = StatusCompleted
	rec.CompletedAt = nowISO()
	if err := writeJournal(stateRoot, rec); err != nil {
		return nil, err
	}
	// Terminal state: clean up staged intermediates.
	cleanupStagedFiles(rec)
	return &TransactionResult{ID: rec.ID, Action: "recovered"}, nil
}

// backupPathForDiagnostics returns a sanitized string identifying backup
// locations for error messages.
func backupPathForDiagnostics(rec *TransactionRecord) string {
	var paths []string
	if rec.Config != nil {
		paths = append(paths, rec.Config.BackupPath)
	}
	if rec.Factory != nil {
		paths = append(paths, rec.Factory.BackupPath)
	}
	return strings.Join(paths, ", ")
}

// RecoverIncomplete is the public entry point for recovery. It acquires the
// lock and recovers any in-progress transactions.
func RecoverIncomplete(opts TransactionOptions) (*TransactionResult, error) {
	if opts.StateRoot != "" {
		prev := stateRootOverride
		stateRootOverride = opts.StateRoot
		defer func() { stateRootOverride = prev }()
	}

	lock, err := AcquireLock()
	if err != nil {
		return nil, err
	}
	defer lock.Release()

	stateRoot, err := ensureStateRoot()
	if err != nil {
		return nil, err
	}

	return recoverIncompleteLocked(stateRoot, opts)
}

// RollbackTransactionImpl performs the rollback for a given candidate. It
// restores exact backed-up bytes and original modes/owners, and marks the
// transaction as rolled back. It must be called with the lock held.
func RollbackTransactionImpl(candidate RollbackCandidate, opts TransactionOptions) error {
	if opts.StateRoot != "" {
		prev := stateRootOverride
		stateRootOverride = opts.StateRoot
		defer func() { stateRootOverride = prev }()
	}

	lock, err := AcquireLock()
	if err != nil {
		return err
	}
	defer lock.Release()

	stateRoot, err := ensureStateRoot()
	if err != nil {
		return err
	}

	rec, err := readJournal(journalPath(stateRoot, candidate.ID))
	if err != nil {
		return fmt.Errorf("read transaction journal: %w", err)
	}

	if rec.Status != StatusCompleted {
		return fmt.Errorf("transaction %s is not completed (status: %s); only completed transactions can be rolled back", rec.ID, rec.Status)
	}

	// Verify each target's current hash matches the post-migration new hash
	// (third-hash refusal).
	for _, t := range rec.targets() {
		state, err := assessTarget(t)
		if err != nil {
			return fmt.Errorf("assess %s: %w", t.Path, err)
		}
		if state == targetThird {
			return fmt.Errorf("rollback refused: %s has unexpected content (third hash); manual intervention required; backup at %s", t.Path, t.BackupPath)
		}
		if state == targetMissing {
			return fmt.Errorf("rollback refused: %s is missing; backup at %s", t.Path, t.BackupPath)
		}
	}

	// Restore targets from backups.
	for _, t := range rec.targets() {
		// Only restore targets that are currently in the new state.
		state, _ := assessTarget(t)
		if state != targetNew {
			continue
		}
		backupData, err := os.ReadFile(t.BackupPath)
		if err != nil {
			return fmt.Errorf("read backup for %s: %w", t.Path, err)
		}
		// Verify backup hash.
		if sha256Hex(backupData) != t.OldHash {
			return fmt.Errorf("backup hash mismatch for %s; manual intervention required", t.Path)
		}
		if err := checkTargetTrust(t.Path); err != nil {
			return err
		}
		if err := writeFilePreservingMode(t.Path, backupData); err != nil {
			return fmt.Errorf("restore %s: %w", t.Path, err)
		}
	}

	// Mark as rolled back.
	rec.Status = StatusRolledBack
	rec.RolledBackAt = nowISO()
	if err := writeJournal(stateRoot, rec); err != nil {
		return fmt.Errorf("mark rolled back: %w", err)
	}

	// Terminal state: clean up staged intermediates.
	cleanupStagedFiles(rec)

	return nil
}

// SelectRollbackCandidates finds completed, not-yet-rolled-back transactions.
// If configPath is non-empty, only transactions for that config are returned.
// The returned candidates are sorted newest-first.
func SelectRollbackCandidates(stateRoot, configPath string) ([]RollbackCandidate, error) {
	records, err := findJournalsByConfig(stateRoot, configPath)
	if err != nil {
		return nil, err
	}
	var candidates []RollbackCandidate
	for _, rec := range records {
		if rec.Status == StatusCompleted {
			candidates = append(candidates, RollbackCandidate{
				ConfigPath: rec.ConfigPath(),
				ID:         rec.ID,
			})
		}
	}
	// Sort newest-first (by ID descending).
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].ID > candidates[j].ID
	})
	return candidates, nil
}
