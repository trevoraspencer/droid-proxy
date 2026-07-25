package setup

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

//go:embed install_config.yaml
var installConfigTemplate []byte

type SeedResult struct {
	Path    string
	Created bool
}

func DefaultConfigPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("user config dir: %w", err)
	}
	if strings.TrimSpace(dir) == "" {
		return "", fmt.Errorf("user config dir is empty")
	}
	return filepath.Join(dir, "droid-proxy", "config.yaml"), nil
}

func InstallConfigTemplate() []byte {
	out := make([]byte, len(installConfigTemplate))
	copy(out, installConfigTemplate)
	return out
}

func EnsureConfig(path string) (SeedResult, error) {
	if strings.TrimSpace(path) == "" {
		defaultPath, err := DefaultConfigPath()
		if err != nil {
			return SeedResult{}, err
		}
		path = defaultPath
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return SeedResult{}, fmt.Errorf("config path: %w", err)
	}
	if info, err := os.Stat(abs); err == nil {
		if info.IsDir() {
			return SeedResult{}, fmt.Errorf("config path %s is a directory", abs)
		}
		return SeedResult{Path: abs, Created: false}, nil
	} else if !os.IsNotExist(err) {
		return SeedResult{}, fmt.Errorf("config path %s: %w", abs, err)
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		return SeedResult{}, fmt.Errorf("creating config directory: %w", err)
	}
	f, err := os.OpenFile(abs, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return SeedResult{Path: abs, Created: false}, nil
		}
		return SeedResult{}, fmt.Errorf("creating config: %w", err)
	}
	if _, err := f.Write(installConfigTemplate); err != nil {
		_ = f.Close()
		_ = os.Remove(abs)
		return SeedResult{}, fmt.Errorf("writing config: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(abs)
		return SeedResult{}, fmt.Errorf("closing config: %w", err)
	}
	return SeedResult{Path: abs, Created: true}, nil
}
