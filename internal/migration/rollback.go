package migration

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
func FindRollbackCandidates(configPath string) ([]RollbackCandidate, error) {
	stateRoot, err := ensureStateRoot()
	if err != nil {
		return nil, err
	}
	return SelectRollbackCandidates(stateRoot, configPath)
}

// RollbackTransaction restores the original config and Factory bytes from the
// transaction's immutable backups. It uses the default transaction options.
func RollbackTransaction(candidate RollbackCandidate) error {
	return RollbackTransactionImpl(candidate, TransactionOptions{})
}
