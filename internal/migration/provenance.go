package migration

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const provenanceFileName = "deferred_provenance.json"

// ProvenanceRecord captures the trusted deferred upgrade provenance tuple.
// It is created by a verified binary-only upgrade (release installer or
// migration-aware source updater with --no-restart) and consumed once by the
// next verified controlled restart that performs the deferred migration.
//
// The record never contains file bodies, secrets, or credential values.
type ProvenanceRecord struct {
	OldBinaryPath          string `json:"old_binary_path"`
	OldBinaryHash          string `json:"old_binary_hash"`
	OldBinaryVersion       string `json:"old_binary_version,omitempty"`
	InstalledBinaryPath    string `json:"installed_binary_path"`
	InstalledBinaryHash    string `json:"installed_binary_hash"`
	InstalledBinaryVersion string `json:"installed_binary_version,omitempty"`
	ServiceKind            string `json:"service_kind"`
	ServiceDefPath         string `json:"service_def_path,omitempty"`
	ServiceDefHash         string `json:"service_def_hash,omitempty"`
	BackgroundDaemonPID    int    `json:"background_daemon_pid,omitempty"`
	BackgroundDaemonExe    string `json:"background_daemon_exe,omitempty"`
	ConfigPath             string `json:"config_path"`
	ConfigHash             string `json:"config_hash"`
	CreatedAt              string `json:"created_at"`
}

// provenancePath returns the on-disk path for the deferred provenance record.
func provenancePath(stateRoot string) string {
	return filepath.Join(stateRoot, provenanceFileName)
}

// WriteProvenance writes a deferred provenance record to the trusted state
// root with at most 0600 permissions. It verifies the state root path is
// trusted (no user-replaceable symlinks) before writing.
func WriteProvenance(stateRoot string, rec ProvenanceRecord) error {
	absRoot, err := filepath.Abs(stateRoot)
	if err != nil {
		return fmt.Errorf("resolve state root: %w", err)
	}
	if err := checkControlPathTrust(absRoot); err != nil {
		return err
	}
	if err := mkdirPrivate(absRoot); err != nil {
		return fmt.Errorf("create state root: %w", err)
	}
	if rec.CreatedAt == "" {
		rec.CreatedAt = nowISO()
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal provenance: %w", err)
	}
	return writePrivateFile(provenancePath(absRoot), data)
}

// ReadProvenance reads the deferred provenance record from the trusted state
// root. Returns nil and no error when no record exists.
func ReadProvenance(stateRoot string) (*ProvenanceRecord, error) {
	absRoot, err := filepath.Abs(stateRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve state root: %w", err)
	}
	if err := checkControlPathTrust(absRoot); err != nil {
		return nil, err
	}
	path := provenancePath(absRoot)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read provenance: %w", err)
	}
	// Verify the provenance file itself is trusted (not a user-owned symlink).
	if err := checkTargetTrust(path); err != nil {
		return nil, err
	}
	var rec ProvenanceRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, fmt.Errorf("parse provenance: %w", err)
	}
	return &rec, nil
}

// ConsumeProvenance removes the deferred provenance record after it has been
// used for a successful migration or explicit safe disposition. It is a
// one-time consumption.
func ConsumeProvenance(stateRoot string) error {
	absRoot, err := filepath.Abs(stateRoot)
	if err != nil {
		return fmt.Errorf("resolve state root: %w", err)
	}
	path := provenancePath(absRoot)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("consume provenance: %w", err)
	}
	return nil
}

// ProvenanceValidation checks a provenance record against current state.
type ProvenanceValidation struct {
	// InstalledBinaryPath is the currently installed binary path to match.
	InstalledBinaryPath string
	// ConfigPath is the canonical config path to match.
	ConfigPath string
}

// ValidateProvenance revalidates the provenance tuple against current state
// immediately before mutation. It checks:
//   - the installed binary path and hash match
//   - the config path matches
//   - the config file still has the pre-upgrade hash (no later edits)
//   - the record fields are non-empty and internally consistent
//   - conditional provenance for the service kind is complete:
//   - launchd/systemd: service definition path and hash are present and match
//   - background-daemon: daemon PID and executable are present
//
// Returns an error describing why the record is invalid, or nil if valid.
func ValidateProvenance(rec *ProvenanceRecord, val ProvenanceValidation) error {
	if rec == nil {
		return fmt.Errorf("no provenance record")
	}

	// Required fields must be present.
	if rec.InstalledBinaryPath == "" || rec.InstalledBinaryHash == "" {
		return fmt.Errorf("provenance record is missing installed binary identity")
	}
	if rec.OldBinaryPath == "" || rec.OldBinaryHash == "" {
		return fmt.Errorf("provenance record is missing old binary identity")
	}
	if rec.ConfigPath == "" || rec.ConfigHash == "" {
		return fmt.Errorf("provenance record is missing config identity")
	}
	if rec.CreatedAt == "" {
		return fmt.Errorf("provenance record is missing creation time")
	}
	if rec.ServiceKind == "" {
		return fmt.Errorf("provenance record is missing service kind")
	}

	// Conditional provenance: service kind determines which identity
	// fields are required.
	switch rec.ServiceKind {
	case "launchd", "systemd":
		if rec.ServiceDefPath == "" || rec.ServiceDefHash == "" {
			return fmt.Errorf("provenance record for %s is missing service definition identity", rec.ServiceKind)
		}
		// Revalidate: service definition hash must match current file.
		svcHash, err := hashFile(rec.ServiceDefPath)
		if err != nil {
			return fmt.Errorf("cannot verify service definition: %w", err)
		}
		if svcHash != rec.ServiceDefHash {
			return fmt.Errorf("provenance service definition hash mismatch (service definition changed after record creation)")
		}
	case "background-daemon":
		if rec.BackgroundDaemonPID == 0 || rec.BackgroundDaemonExe == "" {
			return fmt.Errorf("provenance record for background-daemon is missing daemon identity (pid and executable)")
		}
		// Revalidate: the recorded executable must still exist on disk,
		// symmetric with how launchd/systemd revalidates the service
		// definition file hash.
		if _, err := os.Stat(rec.BackgroundDaemonExe); err != nil {
			return fmt.Errorf("provenance background-daemon executable is no longer accessible: %w", err)
		}
	default:
		return fmt.Errorf("provenance record has unknown service kind %q", rec.ServiceKind)
	}

	// Revalidate: installed binary path must match.
	if val.InstalledBinaryPath != "" {
		recAbs, recErr := filepath.Abs(rec.InstalledBinaryPath)
		valAbs, valErr := filepath.Abs(val.InstalledBinaryPath)
		if recErr != nil || valErr != nil || recAbs != valAbs {
			return fmt.Errorf("provenance installed binary path mismatch")
		}
	}

	// Revalidate: installed binary hash must match current file.
	installedHash, err := hashFile(rec.InstalledBinaryPath)
	if err != nil {
		return fmt.Errorf("cannot verify installed binary: %w", err)
	}
	if installedHash != rec.InstalledBinaryHash {
		return fmt.Errorf("provenance installed binary hash mismatch (binary changed after record creation)")
	}

	// Revalidate: config path must match.
	if val.ConfigPath != "" {
		recAbs, recErr := filepath.Abs(rec.ConfigPath)
		valAbs, valErr := filepath.Abs(val.ConfigPath)
		if recErr != nil || valErr != nil || recAbs != valAbs {
			return fmt.Errorf("provenance config path mismatch")
		}
	}

	// Revalidate: config hash must still match (no later edits).
	configHash, err := hashFile(rec.ConfigPath)
	if err != nil {
		return fmt.Errorf("cannot verify config: %w", err)
	}
	if configHash != rec.ConfigHash {
		return fmt.Errorf("provenance config hash mismatch (config changed after record creation)")
	}

	return nil
}
