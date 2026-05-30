// Package secrets manages the local env file that holds upstream API keys
// written by the interactive config UI. Keys live in ~/.droid-proxy/env with
// 0600 permissions and a format compatible with daemon.LoadEnvFile.
package secrets

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"droid-proxy/internal/daemon"
)

const managedHeader = "# Managed by `droid-proxy config`. Edit with care."

// stateDirFn resolves the directory holding the managed env file. It is a
// variable so tests can redirect writes away from the real state dir.
var stateDirFn = daemon.StateDir

// Path returns the location of the managed env file (~/.droid-proxy/env).
func Path() string {
	return filepath.Join(stateDirFn(), "env")
}

// Read parses the managed env file into a key/value map. A missing file is not
// an error and yields an empty map.
func Read() (map[string]string, error) {
	return readFile(Path())
}

func readFile(path string) (map[string]string, error) {
	out := map[string]string{}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return nil, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		key, val, ok, err := daemon.ParseEnvLine(line)
		if err != nil || !ok {
			continue
		}
		out[key] = val
	}
	return out, nil
}

// Has reports whether key is present and non-empty in the managed env file.
func Has(key string) (bool, error) {
	values, err := Read()
	if err != nil {
		return false, err
	}
	v, ok := values[strings.TrimSpace(key)]
	return ok && strings.TrimSpace(v) != "", nil
}

// Set writes or replaces a single key in the managed env file, preserving all
// other lines (comments, blanks, unrelated keys, and their ordering). The file
// is created with 0600 permissions if it does not exist and is written
// atomically.
func Set(key, value string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return fmt.Errorf("secrets: empty key")
	}
	lines, existed, err := readLines()
	if err != nil {
		return err
	}
	newLine := fmt.Sprintf("export %s=%q", key, value)
	out := make([]string, 0, len(lines)+2)
	replaced := false
	for _, line := range lines {
		if k, _, ok, perr := daemon.ParseEnvLine(line); perr == nil && ok && k == key {
			if !replaced {
				out = append(out, newLine)
				replaced = true
			}
			continue
		}
		out = append(out, line)
	}
	if !replaced {
		if !existed && len(out) == 0 {
			out = append(out, managedHeader)
		}
		out = append(out, newLine)
	}
	return writeRaw(out)
}

// Delete removes a key from the managed env file, preserving all other lines.
// Missing keys (and a missing file) are ignored.
func Delete(key string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil
	}
	lines, existed, err := readLines()
	if err != nil || !existed {
		return err
	}
	out := make([]string, 0, len(lines))
	changed := false
	for _, line := range lines {
		if k, _, ok, perr := daemon.ParseEnvLine(line); perr == nil && ok && k == key {
			changed = true
			continue
		}
		out = append(out, line)
	}
	if !changed {
		return nil
	}
	return writeRaw(out)
}

// readLines returns the managed env file's lines (trailing blank line trimmed)
// and whether the file currently exists.
func readLines() (lines []string, existed bool, err error) {
	data, err := os.ReadFile(Path())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	trimmed := strings.TrimRight(string(data), "\n")
	if trimmed == "" {
		return nil, true, nil
	}
	return strings.Split(trimmed, "\n"), true, nil
}

func writeRaw(lines []string) error {
	dir := stateDirFn()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	content := strings.Join(lines, "\n") + "\n"

	tmp, err := os.CreateTemp(dir, "env-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, Path())
}
