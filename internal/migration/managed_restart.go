package migration

import (
	"fmt"
	"os"
	"path/filepath"
)

// ManagedRestartOptions configures a verified controlled restart that may
// perform automatic deferred migration.
type ManagedRestartOptions struct {
	// StateRoot overrides the migration state root directory.
	StateRoot string

	// ConfigPath is the canonical config path for the service being
	// restarted. Must be the per-user canonical or service-selected path,
	// not a CWD or arbitrary file.
	ConfigPath string

	// InstalledBinaryPath is the currently installed binary path, used to
	// revalidate deferred provenance.
	InstalledBinaryPath string

	// NoMigratePort disables automatic migration for this invocation only.
	// The omitted-port startup preflight remains enforced.
	NoMigratePort bool

	// DestinationChecker verifies that the migration destination port is
	// available for binding. When non-nil and it returns an error, the
	// transaction refuses before any target mutation.
	DestinationChecker func(host string, port int) error
}

// ManagedRestartResult describes the outcome of an automatic migration
// attempt during a managed restart.
type ManagedRestartResult struct {
	Action string // "migrated", "skipped", "no-provenance", "ineligible"
	Reason string // sanitized reason when not migrated
	Result *TransactionResult
}

// AttemptDeferredMigration checks for trusted deferred provenance and, if
// present and valid, performs automatic migration of an eligible explicit
// old-default config. It is called by verified controlled restart paths:
// CLI restart, TUI 'r', launchd/systemd restart, and update/installer
// restart.
//
// If NoMigratePort is true, automatic migration is skipped with a sanitized
// reason, but the provenance record is left intact for a future restart.
//
// If no provenance record exists, this is a no-op (returns "no-provenance").
// If the config does not have an explicit old-default port, this is a no-op
// (returns "ineligible").
//
// The provenance record is consumed once on successful migration.
func AttemptDeferredMigration(opts ManagedRestartOptions) (*ManagedRestartResult, error) {
	if opts.StateRoot != "" {
		prev := stateRootOverride
		stateRootOverride = opts.StateRoot
		defer func() { stateRootOverride = prev }()
	}

	// Invocation-scoped opt-out.
	if opts.NoMigratePort {
		return &ManagedRestartResult{
			Action: "skipped",
			Reason: "automatic port migration skipped for this invocation (--no-migrate-port)",
		}, nil
	}

	stateRoot, err := ensureStateRoot()
	if err != nil {
		return nil, err
	}

	// Check for deferred provenance.
	rec, err := ReadProvenance(stateRoot)
	if err != nil {
		return nil, fmt.Errorf("read deferred provenance: %w", err)
	}
	if rec == nil {
		return &ManagedRestartResult{Action: "no-provenance"}, nil
	}

	// Revalidate provenance against current state immediately before
	// mutation.
	if err := ValidateProvenance(rec, ProvenanceValidation{
		InstalledBinaryPath: opts.InstalledBinaryPath,
		ConfigPath:          opts.ConfigPath,
	}); err != nil {
		// Stale or mismatched provenance: refuse without action.
		return &ManagedRestartResult{
			Action: "skipped",
			Reason: fmt.Sprintf("deferred provenance is stale or mismatched: %s", err),
		}, nil
	}

	// Verify the config path matches the provenance record's config path.
	recConfigAbs, _ := filepath.Abs(rec.ConfigPath)
	optsConfigAbs, _ := filepath.Abs(opts.ConfigPath)
	if recConfigAbs != optsConfigAbs {
		return &ManagedRestartResult{
			Action: "skipped",
			Reason: "deferred provenance config path does not match the selected config",
		}, nil
	}

	// Plan the migration.
	plan, err := PlanMigration(PlanOptions{
		ConfigPath: opts.ConfigPath,
	})
	if err != nil {
		return nil, fmt.Errorf("plan migration: %w", err)
	}

	// Only explicit old-default configs are eligible for automatic
	// migration. Omitted ports are never rewritten.
	if !plan.ConfigEligible {
		// The config is not an explicit old-default. Consume the
		// provenance record since it no longer applies.
		_ = ConsumeProvenance(stateRoot)
		return &ManagedRestartResult{
			Action: "ineligible",
			Reason: plan.ConfigReason,
		}, nil
	}

	if plan.FactoryUnsafe {
		// Unsafe Factory state: refuse without consuming provenance,
		// so a later restart can retry after the user resolves it.
		return &ManagedRestartResult{
			Action: "skipped",
			Reason: fmt.Sprintf("unsafe Factory state: %s", plan.FactoryReason),
		}, nil
	}

	// Commit the migration through the transaction layer.
	txOpts := TransactionOptions{
		StateRoot:          opts.StateRoot,
		DestinationChecker: opts.DestinationChecker,
	}
	result, err := CommitTransaction(plan, txOpts)
	if err != nil {
		// Migration failed. Leave provenance intact for retry.
		return nil, fmt.Errorf("automatic migration failed: %w", err)
	}

	// Consume the provenance record (one-time consumption).
	if err := ConsumeProvenance(stateRoot); err != nil {
		// Non-fatal: the migration succeeded, but we couldn't remove
		// the provenance record. Report it.
		return &ManagedRestartResult{
			Action: "migrated",
			Result: result,
			Reason: fmt.Sprintf("migration succeeded but provenance consumption failed: %s", err),
		}, nil
	}

	return &ManagedRestartResult{
		Action: "migrated",
		Result: result,
	}, nil
}

// RecordDeferredProvenance creates a provenance record after a successful
// binary-only upgrade (e.g., update --no-restart). It captures the trusted
// tuple so the next verified controlled restart can perform the deferred
// migration.
//
// oldBinaryPath and oldBinaryHash describe the pre-upgrade binary.
// installedBinaryPath and installedBinaryHash describe the newly installed
// binary. configPath and configHash identify the canonical config.
// serviceKind is "launchd", "systemd", or "background-daemon".
func RecordDeferredProvenance(
	stateRoot string,
	oldBinaryPath, oldBinaryHash, oldBinaryVersion string,
	installedBinaryPath, installedBinaryHash, installedBinaryVersion string,
	configPath, configHash string,
	serviceKind, serviceDefPath, serviceDefHash string,
	backgroundDaemonPID int, backgroundDaemonExe string,
) error {
	rec := ProvenanceRecord{
		OldBinaryPath:          oldBinaryPath,
		OldBinaryHash:          oldBinaryHash,
		OldBinaryVersion:       oldBinaryVersion,
		InstalledBinaryPath:    installedBinaryPath,
		InstalledBinaryHash:    installedBinaryHash,
		InstalledBinaryVersion: installedBinaryVersion,
		ServiceKind:            serviceKind,
		ServiceDefPath:         serviceDefPath,
		ServiceDefHash:         serviceDefHash,
		BackgroundDaemonPID:    backgroundDaemonPID,
		BackgroundDaemonExe:    backgroundDaemonExe,
		ConfigPath:             configPath,
		ConfigHash:             configHash,
	}
	return WriteProvenance(stateRoot, rec)
}

// ResolveServiceKind determines the service kind for provenance recording.
// Returns "launchd", "systemd", or "background-daemon".
func ResolveServiceKind(launchdInstalled, systemdInstalled, backgroundRunning bool) string {
	if launchdInstalled {
		return "launchd"
	}
	if systemdInstalled {
		return "systemd"
	}
	if backgroundRunning {
		return "background-daemon"
	}
	return ""
}

// ReadConfigHashForProvenance reads the config file and returns its SHA-256
// hash. Returns empty string if the file cannot be read.
func ReadConfigHashForProvenance(configPath string) string {
	h, err := hashFile(configPath)
	if err != nil {
		return ""
	}
	return h
}

// ReadBinaryHashForProvenance reads a binary file and returns its SHA-256
// hash. Returns empty string if the file cannot be read.
func ReadBinaryHashForProvenance(binaryPath string) string {
	h, err := hashFile(binaryPath)
	if err != nil {
		return ""
	}
	return h
}

// fileExistsAt checks whether a file exists at the given path.
func fileExistsAt(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
