package migration

import "fmt"

// RollbackCandidate describes a completed, not-yet-rolled-back migration
// transaction that is eligible for rollback.
type RollbackCandidate struct {
	ConfigPath string
	ID         string
}

// FindRollbackCandidates searches the trusted migration state root for
// completed, not-yet-rolled-back transactions. If configPath is non-empty,
// only transactions for that canonical config path are returned. If
// configPath is empty, all eligible transactions are returned.
//
// The transaction/journal layer is provided by the migration transaction
// feature. Until that layer exists, this returns zero candidates.
func FindRollbackCandidates(configPath string) ([]RollbackCandidate, error) {
	// The transaction state root does not exist yet. When the transaction
	// feature is implemented, this will scan the migration journal for
	// completed, not-yet-rolled-back transactions matching the selector.
	_ = configPath
	return nil, nil
}

// RollbackTransaction restores the original config and Factory bytes from the
// transaction's immutable backups. The transaction layer is provided by the
// migration transaction feature.
func RollbackTransaction(candidate RollbackCandidate) error {
	return fmt.Errorf("rollback transaction layer not yet implemented")
}
