package migration

import (
	"fmt"
	"os"
	"path/filepath"
)

// migrationDirName is the subdirectory under the droid-proxy state root
// (~/.droid-proxy) that holds migration state.
const migrationDirName = "migration"

// stateRootOverride is a test seam for the migration state root directory.
// When non-empty, it replaces the default ~/.droid-proxy/migration path.
var stateRootOverride string

// StateRoot returns the canonical migration state root directory
// (~/.droid-proxy/migration). The directory is not created by this function;
// callers use ensureStateRoot when they need it to exist.
func StateRoot() string {
	if stateRootOverride != "" {
		return stateRootOverride
	}
	home := os.Getenv("HOME")
	return filepath.Join(home, ".droid-proxy", migrationDirName)
}

// SetStateRootForTest sets a temporary state root for testing and returns a
// cleanup function that restores the previous value.
func SetStateRootForTest(dir string) func() {
	prev := stateRootOverride
	stateRootOverride = dir
	return func() { stateRootOverride = prev }
}

// ensureStateRoot creates the state root directory tree with 0700 permissions
// and verifies that the path components are trusted (no user-replaceable
// symlinks). It returns the absolute state root path.
func ensureStateRoot() (string, error) {
	root := StateRoot()
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve state root: %w", err)
	}
	if err := checkControlPathTrust(absRoot); err != nil {
		return "", err
	}
	for _, sub := range []string{"", "transactions", "backups"} {
		dir := absRoot
		if sub != "" {
			dir = filepath.Join(absRoot, sub)
		}
		if err := mkdirPrivate(dir); err != nil {
			return "", fmt.Errorf("create migration state dir %s: %w", dir, err)
		}
	}
	return absRoot, nil
}

// mkdirPrivate creates a directory with 0700 permissions. If the directory
// already exists, it verifies permissions are no more permissive than 0700
// and that it is user-owned.
func mkdirPrivate(path string) error {
	info, err := os.Stat(path)
	if err == nil {
		// Directory exists. Verify it is a directory, user-owned, and private.
		if !info.IsDir() {
			return fmt.Errorf("path is not a directory: %s", path)
		}
		if err := checkStatOwnership(info, path); err != nil {
			return err
		}
		if info.Mode().Perm()&0o077 != 0 {
			// Permissive mode; tighten to 0700.
			if err := os.Chmod(path, 0o700); err != nil {
				return fmt.Errorf("tighten permissions: %w", err)
			}
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return err
	}
	// Create with 0700. The umask may make it stricter, which is fine.
	return os.MkdirAll(path, 0o700)
}

// writePrivateFile writes data to path with at most 0600 permissions. If the
// file already exists, its current mode is preserved.
func writePrivateFile(path string, data []byte) (err error) {
	mode := os.FileMode(0o600)
	if info, statErr := os.Stat(path); statErr == nil {
		mode = info.Mode().Perm()
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".migration-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		if err != nil {
			os.Remove(tmpName)
		}
	}()
	if _, wErr := tmp.Write(data); wErr != nil {
		tmp.Close()
		return wErr
	}
	if cErr := tmp.Close(); cErr != nil {
		return cErr
	}
	if err = os.Chmod(tmpName, mode); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// checkControlPathTrust verifies that the state root path does not contain
// user-replaceable symlinks. System symlinks (owned by root) are tolerated.
// It also rejects if the state root itself (if it exists) is a user-owned
// symlink or a regular file.
func checkControlPathTrust(absPath string) error {
	// Walk ancestor directories.
	parent := filepath.Dir(absPath)
	for {
		if parent == "/" || parent == "." || parent == "" {
			break
		}
		info, err := os.Lstat(parent)
		if err != nil {
			break // non-existent ancestor; not a concern
		}
		if info.Mode()&os.ModeSymlink != 0 {
			if !isOwnedByRoot(info) {
				return fmt.Errorf("symlink component in state root path at %s", parent)
			}
		}
		next := filepath.Dir(parent)
		if next == parent {
			break
		}
		parent = next
	}
	// Check if the state root path itself exists and is a symlink or file.
	if li, err := os.Lstat(absPath); err == nil {
		if li.Mode()&os.ModeSymlink != 0 && !isOwnedByRoot(li) {
			return fmt.Errorf("state root is a user-owned symlink: %s", absPath)
		}
		if li.Mode().IsRegular() {
			return fmt.Errorf("state root path is a regular file, not a directory: %s", absPath)
		}
	}
	return nil
}
