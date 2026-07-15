//go:build !unix

package migration

import (
	"fmt"
	"os"
	"path/filepath"
)

// MigrationLock provides exclusive serialization of migration operations.
// On non-Unix platforms, a create-exclusive sentinel file is used.
type MigrationLock struct {
	path string
	f    *os.File
}

// AcquireLock creates and exclusively locks the migration lock file.
func AcquireLock() (*MigrationLock, error) {
	root := StateRoot()
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create state root for lock: %w", err)
	}
	lockPath := filepath.Join(root, "lock")
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return nil, fmt.Errorf("another migration is in progress; wait for it to complete")
		}
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	return &MigrationLock{path: lockPath, f: f}, nil
}

// Release releases the exclusive lock.
func (l *MigrationLock) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f = nil
	os.Remove(l.path)
	return err
}
