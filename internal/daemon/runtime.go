package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const runtimeFileName = "runtime.json"

type RuntimeMetadata struct {
	PID        int    `json:"pid"`
	Executable string `json:"executable"`
	ConfigPath string `json:"config_path"`
	// ConfigModTime is the RFC3339 mtime of ConfigPath when the server loaded
	// it; empty in runtime.json files written by older versions.
	ConfigModTime string `json:"config_mtime,omitempty"`
	EnvFile       string `json:"env_file"`
	WorkDir       string `json:"work_dir"`
	UpdatedAt     string `json:"updated_at"`
}

func RuntimeFile() string {
	return filepath.Join(stateDir(), runtimeFileName)
}

func WriteRuntimeMetadata(meta RuntimeMetadata) error {
	if err := os.MkdirAll(stateDir(), 0o700); err != nil {
		return err
	}
	if meta.PID == 0 {
		meta.PID = os.Getpid()
	}
	if meta.UpdatedAt == "" {
		meta.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	raw, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal runtime metadata: %w", err)
	}
	raw = append(raw, '\n')
	return writeRuntimeMetadataAtomic(RuntimeFile(), raw)
}

func writeRuntimeMetadataAtomic(path string, raw []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create runtime metadata temp file: %w", err)
	}
	tmpPath := tmp.Name()
	tmpClosed := false
	defer func() {
		if !tmpClosed {
			_ = tmp.Close()
		}
		_ = os.Remove(tmpPath)
	}()

	if err := tmp.Chmod(0o600); err != nil {
		return fmt.Errorf("chmod runtime metadata temp file: %w", err)
	}
	if _, err := tmp.Write(raw); err != nil {
		return fmt.Errorf("write runtime metadata temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync runtime metadata temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close runtime metadata temp file: %w", err)
	}
	tmpClosed = true
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace runtime metadata: %w", err)
	}

	// The rename makes readers see either the old complete file or the new
	// complete file. Syncing the containing directory makes that replacement
	// durable across a crash on filesystems that support directory fsync.
	if dirFile, err := os.Open(dir); err == nil {
		_ = dirFile.Sync()
		_ = dirFile.Close()
	}
	return nil
}

func ReadRuntimeMetadata() (RuntimeMetadata, error) {
	raw, err := os.ReadFile(RuntimeFile())
	if err != nil {
		return RuntimeMetadata{}, err
	}
	var meta RuntimeMetadata
	if err := json.Unmarshal(raw, &meta); err != nil {
		return RuntimeMetadata{}, fmt.Errorf("parse runtime metadata: %w", err)
	}
	return meta, nil
}

func RuntimeEnvFileForConfig(configPath string) string {
	meta, err := ReadRuntimeMetadata()
	if err != nil || meta.ConfigPath == "" || meta.EnvFile == "" {
		return ""
	}
	want, err := filepath.Abs(configPath)
	if err != nil {
		return ""
	}
	got, err := filepath.Abs(meta.ConfigPath)
	if err != nil {
		return ""
	}
	if want != got {
		return ""
	}
	return meta.EnvFile
}

func RemoveRuntimeMetadata() {
	_ = os.Remove(RuntimeFile())
}
