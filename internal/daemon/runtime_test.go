package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRuntimeMetadataRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	oldStateDir := stateDir
	oldPIDFile := pidFile
	stateDir = filepath.Join(os.Getenv("HOME"), dirName)
	pidFile = filepath.Join(stateDir, "droid-proxy.pid")
	t.Cleanup(func() {
		stateDir = oldStateDir
		pidFile = oldPIDFile
	})

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
