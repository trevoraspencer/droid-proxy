package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRuntimeMetadataRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	want := RuntimeMetadata{
		PID:        123,
		Executable: "/tmp/droid-proxy",
		ConfigPath: "/tmp/config.yaml",
		EnvFile:    "/tmp/.env.local",
		WorkDir:    "/tmp",
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
	if got.UpdatedAt == "" {
		t.Fatal("UpdatedAt should be populated")
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
