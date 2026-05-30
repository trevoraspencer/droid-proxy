package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ParseEnvValue decodes a raw env value. Double-quoted values are unescaped via
// strconv.Unquote (so they round-trip with values written using %q); on failure
// or for unquoted/single-quoted values it falls back to trimming surrounding
// quotes, preserving the behavior of hand-written env files.
func ParseEnvValue(raw string) string {
	v := strings.TrimSpace(raw)
	if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
		if unq, err := strconv.Unquote(v); err == nil {
			return unq
		}
	}
	return strings.Trim(v, `"'`)
}

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
		val = ParseEnvValue(val)
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

// ManagedEnvFile is the path to the secrets file written by `droid-proxy
// config` (~/.droid-proxy/env).
func ManagedEnvFile() string {
	return filepath.Join(StateDir(), "env")
}

// LoadEnvFiles loads multiple env files in order. Later files override earlier
// ones (each call uses os.Setenv). Empty or missing paths are skipped.
func LoadEnvFiles(paths ...string) error {
	for _, p := range paths {
		if err := LoadEnvFile(p); err != nil {
			return err
		}
	}
	return nil
}

// LoadLayeredEnv loads the managed secrets file (~/.droid-proxy/env) as the
// base layer, then an optional explicit env file that overrides it. When
// explicit is empty, the resolved repo env file (.env.local etc.) is used so
// keys onboarded via `droid-proxy config` are always available regardless of
// whether a repo .env.local also exists.
func LoadLayeredEnv(workDir, explicit string) error {
	managed := ManagedEnvFile()
	if err := LoadEnvFile(managed); err != nil {
		return err
	}
	override := explicit
	if override == "" {
		override = ResolveEnvFile(workDir)
	}
	if override != "" && override != managed {
		if err := LoadEnvFile(override); err != nil {
			return err
		}
	}
	return nil
}
