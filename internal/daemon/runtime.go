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
	EnvFile    string `json:"env_file"`
	WorkDir    string `json:"work_dir"`
	UpdatedAt  string `json:"updated_at"`
}

func RuntimeFile() string {
	return filepath.Join(stateDir, runtimeFileName)
}

func WriteRuntimeMetadata(meta RuntimeMetadata) error {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
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
	return os.WriteFile(RuntimeFile(), raw, 0o600)
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
