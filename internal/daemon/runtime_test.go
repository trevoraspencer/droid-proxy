package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

func TestWriteRuntimeMetadataConcurrentReadersAlwaysSeeValidJSON(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	if err := WriteRuntimeMetadata(RuntimeMetadata{PID: 1, ConfigPath: "/tmp/initial.yaml"}); err != nil {
		t.Fatalf("initial WriteRuntimeMetadata: %v", err)
	}

	stopReaders := make(chan struct{})
	failures := make(chan error, 1)
	reportFailure := func(err error) {
		select {
		case failures <- err:
		default:
		}
	}

	var readers sync.WaitGroup
	for range 4 {
		readers.Go(func() {
			for {
				select {
				case <-stopReaders:
					return
				default:
				}
				raw, err := os.ReadFile(RuntimeFile())
				if err != nil {
					reportFailure(fmt.Errorf("read runtime metadata: %w", err))
					return
				}
				if !json.Valid(raw) {
					reportFailure(fmt.Errorf("runtime metadata was malformed JSON: %q", raw))
					return
				}
			}
		})
	}

	var writers sync.WaitGroup
	for writer := range 4 {
		writers.Go(func() {
			for iteration := range 30 {
				meta := RuntimeMetadata{
					PID:        writer*100 + iteration + 1,
					Executable: "/tmp/droid-proxy",
					ConfigPath: fmt.Sprintf("/tmp/config-%d-%d-%s.yaml", writer, iteration, strings.Repeat("x", 16*1024)),
				}
				if err := WriteRuntimeMetadata(meta); err != nil {
					reportFailure(fmt.Errorf("concurrent WriteRuntimeMetadata: %w", err))
					return
				}
			}
		})
	}

	writers.Wait()
	close(stopReaders)
	readers.Wait()
	select {
	case err := <-failures:
		t.Fatal(err)
	default:
	}

	if _, err := ReadRuntimeMetadata(); err != nil {
		t.Fatalf("final ReadRuntimeMetadata: %v", err)
	}
	assertNoRuntimeMetadataTempFiles(t)
}

func TestWriteRuntimeMetadataKeepsPrivatePermissions(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := os.MkdirAll(stateDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(RuntimeFile(), []byte("old metadata\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(RuntimeFile(), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := WriteRuntimeMetadata(RuntimeMetadata{PID: 42}); err != nil {
		t.Fatalf("WriteRuntimeMetadata: %v", err)
	}
	info, err := os.Stat(RuntimeFile())
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("runtime metadata permissions = %o, want 600", got)
	}
	assertNoRuntimeMetadataTempFiles(t)
}

func TestWriteRuntimeMetadataCleansTempFileAfterRenameFailure(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := os.MkdirAll(RuntimeFile(), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := WriteRuntimeMetadata(RuntimeMetadata{PID: 42}); err == nil {
		t.Fatal("WriteRuntimeMetadata succeeded with a directory at the destination")
	}
	assertNoRuntimeMetadataTempFiles(t)
}

func assertNoRuntimeMetadataTempFiles(t *testing.T) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(stateDir(), "."+runtimeFileName+".tmp-*"))
	if err != nil {
		t.Fatalf("glob runtime metadata temp files: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("runtime metadata temp files remain: %v", matches)
	}
}
