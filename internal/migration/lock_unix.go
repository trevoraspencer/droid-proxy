//go:build unix

package migration

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// MigrationLock provides exclusive serialization of migration operations
// within a single HOME state root. It uses an advisory flock on a lock file
// inside the private migration state directory.
type MigrationLock struct {
	path string
	fd   int
}

// AcquireLock creates (if needed) and exclusively locks the migration lock
// file. It returns an error if another migration is in progress. The caller
// must call Release when done.
func AcquireLock() (*MigrationLock, error) {
	root := StateRoot()
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create state root for lock: %w", err)
	}
	lockPath := filepath.Join(root, "lock")
	fd, err := syscall.Open(lockPath, syscall.O_CREAT|syscall.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	// Non-blocking exclusive lock.
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		syscall.Close(fd)
		return nil, fmt.Errorf("another migration is in progress; wait for it to complete")
	}
	return &MigrationLock{path: lockPath, fd: fd}, nil
}

// Release releases the exclusive lock and closes the file descriptor.
func (l *MigrationLock) Release() error {
	if l == nil || l.fd < 0 {
		return nil
	}
	_ = syscall.Flock(l.fd, syscall.LOCK_UN)
	err := syscall.Close(l.fd)
	l.fd = -1
	return err
}
