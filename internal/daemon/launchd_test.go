package daemon

import (
	"os"
	"path/filepath"
	"runtime"
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

func TestValidateLaunchdConfigRejectsMissingWithActionableError(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing-config.yaml")

	_, _, err := ValidateLaunchdConfig(missing)
	if err == nil {
		t.Fatal("ValidateLaunchdConfig error = nil, want missing config error")
	}
	msg := err.Error()
	for _, want := range []string{missing, "droid-proxy config", "--config"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error = %q, want %q", msg, want)
		}
	}
}

func TestInstallLaunchdMissingConfigDoesNotWritePlistOrLoad(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("launchd install is macOS-only")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	withTempStateDir(t)
	calledLoad := false
	oldLoader := launchAgentLoader
	launchAgentLoader = func(path string) error {
		calledLoad = true
		return nil
	}
	t.Cleanup(func() { launchAgentLoader = oldLoader })

	missing := filepath.Join(t.TempDir(), "missing-config.yaml")
	err := InstallLaunchd(missing)
	if err == nil {
		t.Fatal("InstallLaunchd error = nil, want missing config error")
	}
	if calledLoad {
		t.Fatal("launchAgentLoader was called for missing config")
	}
	if _, statErr := os.Stat(plistPath()); !os.IsNotExist(statErr) {
		t.Fatalf("plist was written for missing config: stat err=%v", statErr)
	}
}

func TestInstallLaunchdOmitsMissingEnvFile(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("launchd install is macOS-only")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	withTempStateDir(t)
	oldLoader := launchAgentLoader
	launchAgentLoader = func(path string) error { return nil }
	t.Cleanup(func() { launchAgentLoader = oldLoader })

	workDir := t.TempDir()
	configPath := filepath.Join(workDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("models: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workDir, ".env.live-e2e.local"), []byte("FOO=live\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := InstallLaunchd(configPath); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(plistPath())
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if strings.Contains(text, "--env-file") || strings.Contains(text, ".env.live-e2e.local") {
		t.Fatalf("plist should omit missing env file and live e2e env:\n%s", text)
	}
}

func TestInstallLaunchdIncludesExistingAbsoluteEnvFile(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("launchd install is macOS-only")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	withTempStateDir(t)
	oldLoader := launchAgentLoader
	launchAgentLoader = func(path string) error { return nil }
	t.Cleanup(func() { launchAgentLoader = oldLoader })

	workDir := t.TempDir()
	configPath := filepath.Join(workDir, "config.yaml")
	envPath := filepath.Join(workDir, ".env.local")
	if err := os.WriteFile(configPath, []byte("models: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(envPath, []byte("FOO=local\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := InstallLaunchd(configPath); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(plistPath())
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if !strings.Contains(text, "<string>--env-file</string>") || !strings.Contains(text, "<string>"+envPath+"</string>") {
		t.Fatalf("plist missing existing absolute env file:\n%s", text)
	}
}

func TestCheckLaunchdPlistReportsMissingProgramArgumentPaths(t *testing.T) {
	path := filepath.Join(t.TempDir(), "com.droid-proxy.agent.plist")
	envPath := filepath.Join(t.TempDir(), ".env.live-e2e.local")
	raw := `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict><key>ProgramArguments</key><array>
<string>/tmp/droid-proxy</string><string>start</string><string>--foreground</string>
<string>--config</string><string>/tmp/missing-config.yaml</string>
<string>--env-file</string><string>` + envPath + `</string>
</array><key>Label</key><string>com.droid-proxy.agent</string></dict></plist>`
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}

	check, err := CheckLaunchdPlist(path)
	if err != nil {
		t.Fatal(err)
	}
	if !check.Installed {
		t.Fatal("Installed = false, want true")
	}
	if len(check.ProgramArguments) != 7 {
		t.Fatalf("ProgramArguments = %#v", check.ProgramArguments)
	}
	joined := strings.Join(check.Issues, "\n")
	for _, want := range []string{"missing config path", "missing env file path", ".env.live-e2e.local"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("issues missing %q:\n%s", want, joined)
		}
	}
}
