//go:build unix

package migration

import (
	"fmt"
	"os"
	"syscall"
)

// checkStatOwnership verifies that the file described by info is owned by the
// current effective user ID.
func checkStatOwnership(info os.FileInfo, path string) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil // cannot determine ownership on this platform
	}
	uid := os.Getuid()
	if uid == 0 {
		return nil // root bypasses ownership check
	}
	if stat.Uid != uint32(uid) {
		return fmt.Errorf("file is not owned by current user (uid %d): %s", uid, path)
	}
	return nil
}

// isOwnedByRoot reports whether the file described by info is owned by root
// (uid 0). System-level symlinks owned by root are tolerated in path
// component checks.
func isOwnedByRoot(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	return stat.Uid == 0
}
