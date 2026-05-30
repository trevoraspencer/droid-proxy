// Package secrets manages the local env file that holds upstream API keys
// written by the interactive config UI. Keys live in ~/.droid-proxy/env with
// 0600 permissions and a format compatible with daemon.LoadEnvFile.
package secrets

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"droid-proxy/internal/daemon"
)

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
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = daemon.ParseEnvValue(val)
		if key == "" {
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

// Set writes or replaces a single key in the managed env file, preserving the
// other entries. The file is created with 0600 permissions if it does not
// exist and is written atomically.
func Set(key, value string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return fmt.Errorf("secrets: empty key")
	}
	values, err := Read()
	if err != nil {
		return err
	}
	values[key] = value
	return writeAll(values)
}

// Delete removes a key from the managed env file. Missing keys are ignored.
func Delete(key string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil
	}
	values, err := Read()
	if err != nil {
		return err
	}
	if _, ok := values[key]; !ok {
		return nil
	}
	delete(values, key)
	return writeAll(values)
}

func writeAll(values map[string]string) error {
	dir := stateDirFn()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	b.WriteString("# Managed by `droid-proxy config`. Edit with care.\n")
	for _, k := range keys {
		fmt.Fprintf(&b, "export %s=%q\n", k, values[k])
	}

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
	if _, err := tmp.WriteString(b.String()); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, Path())
}
