// Package userhome resolves the current user's home directory without falling
// back to a process-relative path when service managers scrub HOME.
package userhome

import (
	"os"
	"os/user"
	"path/filepath"
	"strings"
)

// Dir returns an absolute best-effort home directory. The filesystem root is
// the final fail-closed fallback: ordinary users receive a permission error
// instead of silently placing credentials in the process working directory.
func Dir() string {
	if home := strings.TrimSpace(os.Getenv("HOME")); home != "" {
		return home
	}
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		return strings.TrimSpace(home)
	}
	if current, err := user.Current(); err == nil && strings.TrimSpace(current.HomeDir) != "" {
		return strings.TrimSpace(current.HomeDir)
	}
	if volume := filepath.VolumeName(os.TempDir()); volume != "" {
		return volume + string(filepath.Separator)
	}
	return string(filepath.Separator)
}
