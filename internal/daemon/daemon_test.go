package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPIDRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	stateDir = filepath.Join(os.Getenv("HOME"), dirName)
	pidFile = filepath.Join(stateDir, "droid-proxy.pid")

	if err := WritePID(); err != nil {
		t.Fatalf("WritePID: %v", err)
	}
	pid, running := IsRunning()
	if !running {
		t.Fatal("expected running after WritePID")
	}
	if pid != os.Getpid() {
		t.Fatalf("pid = %d, want %d", pid, os.Getpid())
	}
	RemovePID()
	if _, running := IsRunning(); running {
		t.Fatal("expected not running after RemovePID")
	}
}
