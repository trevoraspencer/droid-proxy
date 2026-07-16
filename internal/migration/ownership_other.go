//go:build !unix

package migration

import (
	"os"
)

// checkStatOwnership is a no-op on non-Unix platforms where file ownership is
// not represented by a POSIX uid.
func checkStatOwnership(_ os.FileInfo, _ string) error {
	return nil
}

// isOwnedByRoot returns false on non-Unix platforms.
func isOwnedByRoot(_ os.FileInfo) bool {
	return false
}
