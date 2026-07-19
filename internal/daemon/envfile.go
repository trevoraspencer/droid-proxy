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

// ParseEnvLine parses a single KEY=VALUE env-file line. Blank lines and
// comments return ok=false; malformed lines return an error.
func ParseEnvLine(line string) (key, value string, ok bool, err error) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return "", "", false, nil
	}
	line = trimEnvExportPrefix(line)
	key, value, ok = strings.Cut(line, "=")
	if !ok {
		return "", "", false, fmt.Errorf("invalid env line %q", line)
	}
	if key == "" {
		return "", "", false, fmt.Errorf("empty env key")
	}
	if !isEnvKey(key) {
		return "", "", false, fmt.Errorf("invalid env key %q", key)
	}
	return key, ParseEnvValue(value), true, nil
}

func trimEnvExportPrefix(line string) string {
	const prefix = "export"
	if !strings.HasPrefix(line, prefix) || len(line) == len(prefix) {
		return line
	}
	if next := line[len(prefix)]; next != ' ' && next != '\t' {
		return line
	}
	return strings.TrimSpace(line[len(prefix)+1:])
}

func isEnvKey(key string) bool {
	if key == "" || !isASCIILetterOrUnderscore(key[0]) {
		return false
	}
	for i := 1; i < len(key); i++ {
		c := key[i]
		if !isASCIILetterOrUnderscore(c) && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}

func isASCIILetterOrUnderscore(c byte) bool {
	return c == '_' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z'
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
		key, val, ok, err := ParseEnvLine(line)
		if err != nil {
			return fmt.Errorf("%s:%d: %w", path, lineNum+1, err)
		}
		if !ok {
			continue
		}
		if err := os.Setenv(key, val); err != nil {
			return fmt.Errorf("%s:%d: setenv %q: %w", path, lineNum+1, key, err)
		}
	}
	return nil
}

// ResolveEnvFile picks the default repo env file for a config directory.
func ResolveEnvFile(workDir string) string {
	candidates := []string{
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
