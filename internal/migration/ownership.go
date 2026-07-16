package migration

import (
	"os"
)

// checkOwnership verifies that the file at path is owned by the current user.
// On platforms where ownership cannot be determined, it returns nil.
func checkOwnership(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	return checkStatOwnership(info, path)
}
