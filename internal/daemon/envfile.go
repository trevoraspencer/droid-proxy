package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// LoadEnvFile reads KEY=VALUE lines from path into the process environment.
// Supports optional leading "export " and double-quoted values.
// Missing files are ignored.
func LoadEnvFile(path string) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for lineNum, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			return fmt.Errorf("%s:%d: invalid env line %q", path, lineNum+1, line)
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		val = strings.Trim(val, `"'`)
		if key == "" {
			return fmt.Errorf("%s:%d: empty env key", path, lineNum+1)
		}
		if err := os.Setenv(key, val); err != nil {
			return fmt.Errorf("%s:%d: setenv %q: %w", path, lineNum+1, key, err)
		}
	}
	return nil
}

// ResolveEnvFile picks the first existing env file for a config directory.
func ResolveEnvFile(workDir string) string {
	candidates := []string{
		filepath.Join(workDir, ".env.live-e2e.local"),
		filepath.Join(workDir, ".env.local"),
		filepath.Join(StateDir(), "env"),
	}
	for _, path := range candidates {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return filepath.Join(StateDir(), "env")
}
