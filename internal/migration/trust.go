// Package migration implements the exact, conservative port migration
// transaction. It is a dedicated component separate from ordinary config
// loading and TUI model editing.
//
// Migration changes the listen.port scalar from the old default (8787) to the
// new default (9787) in an eligible config file, and updates high-confidence
// matching Factory customModels entries that target the old loopback origin.
// Unrelated bytes, comments, ordering, formatting, credentials, and file
// attributes are preserved exactly.
package migration

import (
	"fmt"
	"os"
	"path/filepath"
)

// TrustResult reports whether a file path is trusted for migration.
type TrustResult struct {
	Trusted bool
	Reason  string // sanitized reason when not trusted
}

// CheckFileTrust verifies that path is a regular, user-owned file that is not
// itself a symlink and whose immediate directory ancestors within the
// user-controlled portion of the path are not replaceable symlinks.
//
// It checks:
//   - The file exists and is a regular file (not a symlink, directory, etc.)
//   - The file is owned by the current user
//   - No symlink component exists in the user-controlled path segments
//
// System-level symlinks (e.g., /var -> /private/var on macOS) are tolerated
// because they are not user-replaceable. The transaction layer rechecks file
// identity (inode) at commit time for full TOCTOU protection.
func CheckFileTrust(path string) (*TrustResult, error) {
	// Lstat the target path to detect if the file itself is a symlink.
	li, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &TrustResult{Trusted: false, Reason: fmt.Sprintf("file does not exist: %s", path)}, nil
		}
		return nil, fmt.Errorf("lstat path: %w", err)
	}
	if li.Mode()&os.ModeSymlink != 0 {
		return &TrustResult{Trusted: false, Reason: fmt.Sprintf("path is a symlink: %s", path)}, nil
	}
	if !li.Mode().IsRegular() {
		return &TrustResult{Trusted: false, Reason: fmt.Sprintf("path is not a regular file: %s", path)}, nil
	}

	// Check each ancestor directory for symlinks, walking up from the file's
	// parent. System-level symlinks (owned by root) are tolerated.
	if err := checkPathComponents(path); err != nil {
		return &TrustResult{Trusted: false, Reason: err.Error()}, nil
	}

	// Must be owned by the current user.
	if err := checkOwnership(path); err != nil {
		return &TrustResult{Trusted: false, Reason: err.Error()}, nil
	}

	return &TrustResult{Trusted: true}, nil
}

// checkPathComponents walks each ancestor directory of path using Lstat. A
// symlink component that is owned by the current user is rejected (it is
// user-replaceable). System symlinks owned by root are tolerated.
func checkPathComponents(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve absolute path: %w", err)
	}
	parent := filepath.Dir(abs)
	for {
		if parent == "/" || parent == "." || parent == "" {
			break
		}
		info, err := os.Lstat(parent)
		if err != nil {
			break // non-existent ancestor; not our concern
		}
		if info.Mode()&os.ModeSymlink != 0 {
			// Check if this symlink is user-owned. System symlinks (owned by
			// root) are tolerated.
			if !isOwnedByRoot(info) {
				return fmt.Errorf("symlink component in path at %s", parent)
			}
		}
		next := filepath.Dir(parent)
		if next == parent {
			break
		}
		parent = next
	}
	return nil
}
