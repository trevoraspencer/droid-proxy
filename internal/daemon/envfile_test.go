package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

func withTempStateDir(t *testing.T) string {
	t.Helper()
	oldStateDir := stateDir
	oldPIDFile := pidFile
	home := t.TempDir()
	stateDir = filepath.Join(home, dirName)
	pidFile = filepath.Join(stateDir, "droid-proxy.pid")
	t.Cleanup(func() {
		stateDir = oldStateDir
		pidFile = oldPIDFile
	})
	return stateDir
}

func TestLoadEnvFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "env")
	if err := os.WriteFile(path, []byte("export FOO=\"bar\"\n# comment\nBAZ=qux\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := LoadEnvFile(path); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("FOO") != "bar" || os.Getenv("BAZ") != "qux" {
		t.Fatalf("env = FOO:%q BAZ:%q", os.Getenv("FOO"), os.Getenv("BAZ"))
	}
}

func TestLoadEnvFileMissing(t *testing.T) {
	if err := LoadEnvFile(filepath.Join(t.TempDir(), "missing")); err != nil {
		t.Fatal(err)
	}
}

func TestLoadEnvFileInvalidLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "env")
	if err := os.WriteFile(path, []byte("not-an-env-line\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := LoadEnvFile(path); err == nil {
		t.Fatal("expected invalid env line error")
	}
}

func TestResolveEnvFileIgnoresLiveE2EFile(t *testing.T) {
	state := withTempStateDir(t)
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, ".env.live-e2e.local"), []byte("FOO=live\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	got := ResolveEnvFile(workDir)
	want := filepath.Join(state, "env")
	if got != want {
		t.Fatalf("ResolveEnvFile = %q, want %q", got, want)
	}
}

func TestResolveEnvFileSelectsEnvLocal(t *testing.T) {
	withTempStateDir(t)
	workDir := t.TempDir()
	want := filepath.Join(workDir, ".env.local")
	if err := os.WriteFile(filepath.Join(workDir, ".env.live-e2e.local"), []byte("FOO=live\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(want, []byte("FOO=local\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if got := ResolveEnvFile(workDir); got != want {
		t.Fatalf("ResolveEnvFile = %q, want %q", got, want)
	}
}

func TestLoadLayeredEnvLoadsManagedThenExplicit(t *testing.T) {
	state := withTempStateDir(t)
	t.Setenv("FOO", "")
	t.Setenv("BAR", "")
	managed := filepath.Join(state, "env")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(managed, []byte("FOO=managed\nBAR=managed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	workDir := t.TempDir()
	explicit := filepath.Join(workDir, "explicit.env")
	if err := os.WriteFile(explicit, []byte("FOO=explicit\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := LoadLayeredEnv(workDir, explicit); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("FOO") != "explicit" || os.Getenv("BAR") != "managed" {
		t.Fatalf("env = FOO:%q BAR:%q", os.Getenv("FOO"), os.Getenv("BAR"))
	}
}
