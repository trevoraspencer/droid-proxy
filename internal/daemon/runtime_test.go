package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRuntimeMetadataRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	want := RuntimeMetadata{
		PID:           123,
		Executable:    "/tmp/droid-proxy",
		ConfigPath:    "/tmp/config.yaml",
		ConfigModTime: "2026-07-12T19:17:00Z",
		EnvFile:       "/tmp/.env.local",
		WorkDir:       "/tmp",
	}
	if err := WriteRuntimeMetadata(want); err != nil {
		t.Fatalf("WriteRuntimeMetadata: %v", err)
	}
	got, err := ReadRuntimeMetadata()
	if err != nil {
		t.Fatalf("ReadRuntimeMetadata: %v", err)
	}
	if got.PID != want.PID || got.Executable != want.Executable || got.ConfigPath != want.ConfigPath || got.EnvFile != want.EnvFile || got.WorkDir != want.WorkDir {
		t.Fatalf("metadata = %+v, want %+v", got, want)
	}
	if got.ConfigModTime != want.ConfigModTime {
		t.Fatalf("ConfigModTime = %q, want %q", got.ConfigModTime, want.ConfigModTime)
	}
	if got.UpdatedAt == "" {
		t.Fatal("UpdatedAt should be populated")
	}

	// Old runtime.json files predate config_mtime and must still parse.
	legacy := []byte(`{"pid":7,"executable":"/tmp/droid-proxy","config_path":"/tmp/config.yaml","updated_at":"2026-07-10T00:00:00Z"}`)
	if err := os.WriteFile(RuntimeFile(), legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	old, err := ReadRuntimeMetadata()
	if err != nil {
		t.Fatalf("legacy runtime.json must parse: %v", err)
	}
	if old.ConfigModTime != "" {
		t.Fatalf("legacy ConfigModTime = %q, want empty", old.ConfigModTime)
	}

	RemoveRuntimeMetadata()
	if _, err := os.Stat(RuntimeFile()); !os.IsNotExist(err) {
		t.Fatalf("runtime metadata still exists or stat failed: %v", err)
	}
}

func TestRuntimeEnvFileForConfig(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	envPath := filepath.Join(dir, ".env.live")
	if err := WriteRuntimeMetadata(RuntimeMetadata{ConfigPath: configPath, EnvFile: envPath}); err != nil {
		t.Fatalf("WriteRuntimeMetadata: %v", err)
	}
	if got := RuntimeEnvFileForConfig(configPath); got != envPath {
		t.Fatalf("RuntimeEnvFileForConfig = %q, want %q", got, envPath)
	}
	if got := RuntimeEnvFileForConfig(filepath.Join(dir, "other.yaml")); got != "" {
		t.Fatalf("RuntimeEnvFileForConfig for other config = %q, want empty", got)
	}
}
