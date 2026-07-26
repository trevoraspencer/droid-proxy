package daemon

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestWriteSystemdUnitQuotesPathsWithSpaces(t *testing.T) {
	oldUmask := syscall.Umask(0)
	t.Cleanup(func() {
		syscall.Umask(oldUmask)
	})

	path := filepath.Join(t.TempDir(), "droid-proxy.service")
	data := systemdUnitData{
		Executable: "/tmp/bin dir/droid-proxy",
		ConfigPath: "/tmp/config dir/config.yaml",
		EnvFile:    "/tmp/env dir/env",
		WorkDir:    "/tmp/config dir",
		LogDir:     "/tmp/log dir",
	}
	if err := writeSystemdUnit(path, data); err != nil {
		t.Fatalf("writeSystemdUnit: %v", err)
	}
	mode, err := systemdUnitMode(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode != 0o644 {
		t.Fatalf("mode = %o, want 0644", mode)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, want := range []string{
		`ExecStart="/tmp/bin dir/droid-proxy" start --foreground --config "/tmp/config dir/config.yaml" --env-file "/tmp/env dir/env"`,
		`WorkingDirectory=/tmp/config\x20dir`,
		`StandardOutput=append:/tmp/log\x20dir/stdout.log`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("unit missing %q:\n%s", want, text)
		}
	}
	args, err := systemdExecStartArguments(raw)
	if err != nil {
		t.Fatalf("systemdExecStartArguments: %v", err)
	}
	wantArgs := []string{data.Executable, "start", "--foreground", "--config", data.ConfigPath, "--env-file", data.EnvFile}
	if strings.Join(args, "\x00") != strings.Join(wantArgs, "\x00") {
		t.Fatalf("args = %#v, want %#v", args, wantArgs)
	}
}

func TestSystemdUnitEscapesControlCharactersWithoutDirectiveInjection(t *testing.T) {
	injected := "/tmp/config\nEnvironment=ATTACKED%instance${HOME}'"
	data := systemdUnitData{
		Executable: "/tmp/droid-proxy%instance${HOME}'",
		ConfigPath: injected,
		WorkDir:    "/tmp/work\rdir",
		LogDir:     "/tmp/log\tdir",
	}
	raw := []byte(systemdUnitContents(data))
	if strings.Contains(string(raw), "\nEnvironment=ATTACKED") {
		t.Fatalf("path injected a systemd directive:\n%s", raw)
	}
	args, err := systemdExecStartArguments(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(args) < 5 || args[4] != injected {
		t.Fatalf("escaped config path did not round-trip: %#v", args)
	}
	if args[0] != data.Executable {
		t.Fatalf("escaped executable did not round-trip: %#v", args)
	}
	text := string(raw)
	if !strings.Contains(text, `WorkingDirectory=/tmp/work\x0ddir`) {
		t.Fatalf("working directory was not directive-escaped:\n%s", text)
	}
	if strings.Contains(text, `WorkingDirectory="`) {
		t.Fatalf("working directory must not be quoted:\n%s", text)
	}
}

func TestGeneratedSystemdUnitPassesSystemdAnalyze(t *testing.T) {
	analyze, err := exec.LookPath("systemd-analyze")
	if err != nil {
		t.Skip("systemd-analyze is not installed")
	}
	root := filepath.Join(t.TempDir(), "root with spaces")
	binDir := filepath.Join(root, "bin")
	configDir := filepath.Join(root, "config")
	logDir := filepath.Join(root, "logs")
	for _, dir := range []string{binDir, configDir, logDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	executable := filepath.Join(binDir, "droid-proxy")
	configPath := filepath.Join(configDir, "config.yaml")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte("models: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	unitPath := filepath.Join(root, "droid-proxy.service")
	if err := writeSystemdUnit(unitPath, systemdUnitData{
		Executable: executable,
		ConfigPath: configPath,
		WorkDir:    configDir,
		LogDir:     logDir,
	}); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(analyze, "verify", unitPath).CombinedOutput(); err != nil {
		t.Fatalf("systemd-analyze verify failed: %v\n%s", err, output)
	}
}

func TestCheckSystemdUnitReportsMissingPathsAndSourceBinary(t *testing.T) {
	dir := t.TempDir()
	repo := filepath.Join(dir, "repo")
	if err := os.MkdirAll(filepath.Join(repo, "cmd", "droid-proxy"), 0o755); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(repo, "droid-proxy")
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module github.com/trevoraspencer/droid-proxy\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	unit := filepath.Join(dir, "droid-proxy.service")
	raw := `[Service]
ExecStart=` + systemdJoinArgs([]string{exe, "start", "--foreground", "--config", filepath.Join(dir, "missing.yaml"), "--env-file", filepath.Join(dir, ".env.live-e2e.local")}) + `
`
	if err := os.WriteFile(unit, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}

	check, err := CheckSystemdUnit(unit)
	if err != nil {
		t.Fatal(err)
	}
	if !check.Installed {
		t.Fatal("Installed = false, want true")
	}
	joined := strings.Join(check.Issues, "\n")
	for _, want := range []string{
		"service executable points at source checkout",
		"missing config path",
		"missing env file path",
		".env.live-e2e.local",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("issues missing %q:\n%s", want, joined)
		}
	}
}
