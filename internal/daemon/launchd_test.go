package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestWriteLaunchdPlistUsesDeterministicMode(t *testing.T) {
	oldUmask := syscall.Umask(0)
	t.Cleanup(func() {
		syscall.Umask(oldUmask)
	})

	path := filepath.Join(t.TempDir(), "com.droid-proxy.agent.plist")
	err := writeLaunchdPlist(path, plistData{
		Label:      launchdLabel,
		Executable: "/tmp/droid-proxy",
		ConfigPath: "/tmp/config.yaml",
		EnvFile:    "/tmp/env",
		WorkDir:    "/tmp",
		LogDir:     "/tmp/logs",
	})
	if err != nil {
		t.Fatalf("writeLaunchdPlist: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o644 {
		t.Fatalf("plist mode = %o, want 0644", got)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "<string>/tmp/droid-proxy</string>") {
		t.Fatalf("plist did not contain executable path:\n%s", raw)
	}
}
