package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/trevoraspencer/droid-proxy/internal/daemon"
)

func TestDoctorHealthySourceInstallExitsZero(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := testDoctorGitRepo(t, "module github.com/trevoraspencer/droid-proxy\n")

	var out bytes.Buffer
	res := writeDoctor(&out, repo)
	if len(res.HardIssues) != 0 {
		t.Fatalf("HardIssues = %#v\noutput:\n%s", res.HardIssues, out.String())
	}
	text := out.String()
	for _, want := range []string{
		"executable:",
		"symlink target:",
		"version:",
		"commit:",
		"source HEAD:",
		"source freshness: HEAD matches local origin/main",
		"updater dry-run: ok",
		"daemon: not running",
		"service:",
		"status: ok",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("doctor output missing %q:\n%s", want, text)
		}
	}
}

func TestDoctorReleaseInstallWithoutRepoIsHealthy(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Chdir(t.TempDir())

	var out bytes.Buffer
	res := writeDoctor(&out, "")
	if len(res.HardIssues) != 0 {
		t.Fatalf("HardIssues = %#v\noutput:\n%s", res.HardIssues, out.String())
	}
	text := out.String()
	for _, want := range []string{
		"config: not found at",
		"config load: skipped (run droid-proxy setup or pass --config)",
		"source repo: not found",
		"updater dry-run: skipped (release install)",
		"status: ok",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("doctor output missing %q:\n%s", want, text)
		}
	}
}

func TestDoctorExplicitMissingConfigIsHardIssue(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Chdir(t.TempDir())
	missing := filepath.Join(t.TempDir(), "missing.yaml")

	var out bytes.Buffer
	res := writeDoctorWithOptions(&out, doctorOptions{
		ConfigPath:     missing,
		ConfigExplicit: true,
	})
	if len(res.HardIssues) == 0 {
		t.Fatalf("HardIssues empty, want config issue\noutput:\n%s", out.String())
	}
	text := out.String()
	for _, want := range []string{
		"config: issue: not found at " + missing,
		"config load: skipped",
		"status: 1 issue(s)",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("doctor output missing %q:\n%s", want, text)
		}
	}
}

func TestDoctorValidatesExplicitConfigAndSummarizesModels(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DOCTOR_REMOTE_KEY", "")
	t.Chdir(t.TempDir())
	tmp := t.TempDir()
	managedEnv := filepath.Join(tmp, "managed-env")
	envPath := filepath.Join(tmp, "doctor.env")
	configPath := filepath.Join(tmp, "config.yaml")
	withDoctorEnvHooks(t, managedEnv)

	if err := os.WriteFile(envPath, []byte("DOCTOR_REMOTE_KEY=doctor-secret-sentinel\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rawConfig := `
listen:
  host: 127.0.0.1
  port: 9009
models:
  - alias: remote-ready
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    upstream_model: remote-model
    base_url: https://example.invalid/v1
    api_key_env: DOCTOR_REMOTE_KEY
  - alias: local-tools-off
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    upstream_model: llama
    known_auth: ollama
    capabilities:
      tools: false
`
	if err := os.WriteFile(configPath, []byte(rawConfig), 0o600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	res := writeDoctorWithOptions(&out, doctorOptions{
		ConfigPath:      configPath,
		ConfigExplicit:  true,
		EnvFile:         envPath,
		EnvFileExplicit: true,
	})
	if len(res.HardIssues) != 0 {
		t.Fatalf("HardIssues = %#v\noutput:\n%s", res.HardIssues, out.String())
	}
	text := out.String()
	for _, want := range []string{
		"config: " + configPath,
		"env managed: not found at " + managedEnv,
		"env override (--env-file): found " + envPath,
		"config load: ok",
		"listen: 127.0.0.1:9009",
		"models: 2 configured, 1 agent-ready",
		`model "remote-ready": factory=generic-chat-completion-api upstream=openai-chat auth=api_key_env:DOCTOR_REMOTE_KEY agent_ready=true`,
		`model "local-tools-off": factory=generic-chat-completion-api upstream=openai-chat auth=known_auth:ollama agent_ready=false`,
		"status: ok",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("doctor output missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "doctor-secret-sentinel") {
		t.Fatalf("doctor output leaked env value:\n%s", text)
	}
}

func TestDoctorExplicitMissingEnvFileIsHardIssue(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Chdir(t.TempDir())
	tmp := t.TempDir()
	managedEnv := filepath.Join(tmp, "managed-env")
	missingEnv := filepath.Join(tmp, "missing.env")
	configPath := filepath.Join(tmp, "config.yaml")
	withDoctorEnvHooks(t, managedEnv)

	rawConfig := `
models:
  - alias: local
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    upstream_model: llama
    known_auth: ollama
`
	if err := os.WriteFile(configPath, []byte(rawConfig), 0o600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	res := writeDoctorWithOptions(&out, doctorOptions{
		ConfigPath:      configPath,
		ConfigExplicit:  true,
		EnvFile:         missingEnv,
		EnvFileExplicit: true,
	})
	if len(res.HardIssues) == 0 {
		t.Fatalf("HardIssues empty, want env-file issue\noutput:\n%s", out.String())
	}
	text := out.String()
	for _, want := range []string{
		"env override (--env-file): issue: not found at " + missingEnv,
		"config load: ok",
		"status: 1 issue(s)",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("doctor output missing %q:\n%s", want, text)
		}
	}
}

func TestDoctorEnvLoadIssueDoesNotLeakInvalidLine(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Chdir(t.TempDir())
	tmp := t.TempDir()
	managedEnv := filepath.Join(tmp, "managed-env")
	envPath := filepath.Join(tmp, "bad.env")
	configPath := filepath.Join(tmp, "config.yaml")
	withDoctorEnvHooks(t, managedEnv)

	if err := os.WriteFile(envPath, []byte("not-an-env-line doctor-secret-sentinel\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rawConfig := `
models:
  - alias: local
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    upstream_model: llama
    known_auth: ollama
`
	if err := os.WriteFile(configPath, []byte(rawConfig), 0o600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	res := writeDoctorWithOptions(&out, doctorOptions{
		ConfigPath:      configPath,
		ConfigExplicit:  true,
		EnvFile:         envPath,
		EnvFileExplicit: true,
	})
	if len(res.HardIssues) == 0 {
		t.Fatalf("HardIssues empty, want env load issue\noutput:\n%s", out.String())
	}
	text := out.String()
	for _, want := range []string{
		"env load: issue:",
		"invalid env line",
		"config load: skipped (env file issue)",
		"status: 1 issue(s)",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("doctor output missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "doctor-secret-sentinel") || strings.Contains(text, "not-an-env-line") {
		t.Fatalf("doctor output leaked invalid env line:\n%s", text)
	}
}

func TestDoctorReportsUpdaterModuleIssueAndNonzero(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := testDoctorGitRepo(t, "module example.com/not-droid-proxy\n")

	var out bytes.Buffer
	res := writeDoctor(&out, repo)
	if len(res.HardIssues) == 0 {
		t.Fatalf("HardIssues empty, want updater issue\noutput:\n%s", out.String())
	}
	text := out.String()
	for _, want := range []string{
		"updater dry-run: issue:",
		"go.mod module must be github.com/trevoraspencer/droid-proxy or legacy droid-proxy",
		"status: 1 issue(s)",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("doctor output missing %q:\n%s", want, text)
		}
	}
}

func TestDoctorLaunchdMissingPathsAreSecretSafe(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("launchd plist is only checked on macOS")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := testDoctorGitRepo(t, "module github.com/trevoraspencer/droid-proxy\n")
	envPath := filepath.Join(t.TempDir(), "env")
	if err := os.WriteFile(envPath, []byte("DROID_SENTINEL=doctor-redacted-sentinel\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(envPath); err != nil {
		t.Fatal(err)
	}
	plistDir := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(plistDir, 0o755); err != nil {
		t.Fatal(err)
	}
	plist := filepath.Join(plistDir, "com.droid-proxy.agent.plist")
	raw := `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict><key>ProgramArguments</key><array>
<string>/tmp/droid-proxy</string><string>start</string><string>--foreground</string>
<string>--config</string><string>/tmp/missing-config.yaml</string>
<string>--env-file</string><string>` + envPath + `</string>
</array></dict></plist>`
	if err := os.WriteFile(plist, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	res := writeDoctor(&out, repo)
	if len(res.HardIssues) == 0 {
		t.Fatalf("HardIssues empty, want launchd issues\noutput:\n%s", out.String())
	}
	text := out.String()
	if strings.Contains(text, "doctor-redacted-sentinel") || strings.Contains(text, "DROID_SENTINEL=") {
		t.Fatalf("doctor output leaked env file contents:\n%s", text)
	}
	for _, want := range []string{"missing config path:", "missing env file path:", envPath} {
		if !strings.Contains(text, want) {
			t.Fatalf("doctor output missing %q:\n%s", want, text)
		}
	}
}

func withDoctorEnvHooks(t *testing.T, managedEnv string) {
	t.Helper()
	oldManagedEnvFile := doctorManagedEnvFile
	oldLoadLayeredEnv := doctorLoadLayeredEnv
	doctorManagedEnvFile = func() string { return managedEnv }
	doctorLoadLayeredEnv = func(_ string, explicit string) error {
		return daemon.LoadEnvFiles(managedEnv, explicit)
	}
	t.Cleanup(func() {
		doctorManagedEnvFile = oldManagedEnvFile
		doctorLoadLayeredEnv = oldLoadLayeredEnv
	})
}

func testDoctorGitRepo(t *testing.T, goMod string) string {
	t.Helper()
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "cmd", "droid-proxy"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte(goMod), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "init", "-b", "main")
	runGit(t, repo, "config", "user.email", "doctor@example.com")
	runGit(t, repo, "config", "user.name", "Doctor Test")
	runGit(t, repo, "add", ".")
	runGit(t, repo, "commit", "-m", "init")
	runGit(t, repo, "remote", "add", "origin", "https://github.com/trevoraspencer/droid-proxy.git")
	runGit(t, repo, "update-ref", "refs/remotes/origin/main", "HEAD")
	return repo
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %s: %v", strings.Join(args, " "), out, err)
	}
}
