package migration

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Transaction status values.
const (
	StatusInProgress = "in_progress"
	StatusCompleted  = "completed"
	StatusRolledBack = "rolled_back"
	StatusAborted    = "aborted"
)

// TargetRecord describes one migration target file within a transaction.
type TargetRecord struct {
	Path       string `json:"path"`
	OldHash    string `json:"old_hash"`
	NewHash    string `json:"new_hash"`
	BackupPath string `json:"backup_path"`
	StagedPath string `json:"staged_path"`
	Mode       string `json:"mode"`
	Committed  bool   `json:"committed"`
}

// TransactionRecord is the on-disk journal for one migration transaction.
// It is serialized as JSON and stored under the transactions/ directory.
// It never contains file bodies, secrets, or credential values.
type TransactionRecord struct {
	ID           string        `json:"id"`
	Status       string        `json:"status"`
	CreatedAt    string        `json:"created_at"`
	CompletedAt  string        `json:"completed_at,omitempty"`
	AbortedAt    string        `json:"aborted_at,omitempty"`
	RolledBackAt string        `json:"rolled_back_at,omitempty"`
	OldPort      int           `json:"old_port"`
	NewPort      int           `json:"new_port"`
	Host         string        `json:"host"`
	Config       *TargetRecord `json:"config,omitempty"`
	Factory      *TargetRecord `json:"factory,omitempty"`
}

// ConfigPath returns the canonical config path for this transaction.
func (r *TransactionRecord) ConfigPath() string {
	if r.Config != nil {
		return r.Config.Path
	}
	return ""
}

// HasFactory reports whether this transaction includes a Factory target.
func (r *TransactionRecord) HasFactory() bool {
	return r.Factory != nil
}

// targets returns all non-nil target records in commit order (config, factory).
func (r *TransactionRecord) targets() []*TargetRecord {
	var out []*TargetRecord
	if r.Config != nil {
		out = append(out, r.Config)
	}
	if r.Factory != nil {
		out = append(out, r.Factory)
	}
	return out
}

// journalPath returns the on-disk path for a transaction journal file.
func journalPath(stateRoot, id string) string {
	return filepath.Join(stateRoot, "transactions", id+".json")
}

// writeJournal writes a transaction record to its journal file with at most
// 0600 permissions.
func writeJournal(stateRoot string, rec *TransactionRecord) error {
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal journal: %w", err)
	}
	return writePrivateFile(journalPath(stateRoot, rec.ID), data)
}

// readJournal reads and parses a transaction record from a journal file.
func readJournal(path string) (*TransactionRecord, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var rec TransactionRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, fmt.Errorf("parse journal %s: %w", filepath.Base(path), err)
	}
	return &rec, nil
}

// listJournals reads all transaction journals from the state root, sorted by
// ID (which encodes a timestamp prefix, so chronological order). A malformed
// or untrusted journal causes an error rather than being silently skipped,
// so that migration, recovery, and rollback selection all fail closed when
// state is corrupted.
func listJournals(stateRoot string) ([]*TransactionRecord, error) {
	txDir := filepath.Join(stateRoot, "transactions")
	entries, err := os.ReadDir(txDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var records []*TransactionRecord
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		jPath := filepath.Join(txDir, entry.Name())
		// Verify journal file trust before reading.
		if err := checkTargetTrust(jPath); err != nil {
			return nil, fmt.Errorf("untrusted journal %s: %w", entry.Name(), err)
		}
		rec, err := readJournal(jPath)
		if err != nil {
			return nil, fmt.Errorf("malformed journal %s: %w", entry.Name(), err)
		}
		records = append(records, rec)
	}
	sort.Slice(records, func(i, j int) bool {
		return records[i].ID < records[j].ID
	})
	return records, nil
}

// findJournalByConfig returns all transaction records matching the given
// canonical config path. If configPath is empty, all records are returned.
func findJournalsByConfig(stateRoot, configPath string) ([]*TransactionRecord, error) {
	all, err := listJournals(stateRoot)
	if err != nil {
		return nil, err
	}
	if configPath == "" {
		return all, nil
	}
	var matched []*TransactionRecord
	for _, rec := range all {
		if rec.ConfigPath() == configPath {
			matched = append(matched, rec)
		}
	}
	return matched, nil
}

// generateTxID produces a unique, sortable transaction identifier from the
// current time and a short hash of the config path for disambiguation.
// Nanosecond precision ensures uniqueness within the same second.
func generateTxID(configPath string) string {
	now := time.Now().UTC()
	// Short hash of the config path for uniqueness.
	h := shortHash([]byte(configPath))
	// Use nanoseconds for sub-second disambiguation.
	return fmt.Sprintf("%s-%s-%010d", now.Format("20060102-150405"), h, now.Nanosecond())
}

// nowISO returns the current time in RFC 3339 format.
func nowISO() string {
	return time.Now().UTC().Format(time.RFC3339)
}
