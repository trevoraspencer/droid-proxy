package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trevoraspencer/droid-proxy/internal/daemon"
)

func TestPrintMigratePortUsageAdvertisesAllForms(t *testing.T) {
	var buf bytes.Buffer
	printMigratePortUsage(&buf)
	text := buf.String()

	required := []string{
		"migrate-port --dry-run --config <path>",
		"migrate-port --config <path>",
		"migrate-port --rollback",
		"--no-migrate-port",
		"dry-run",
		"rollback",
		"--config",
	}
	for _, want := range required {
		if !strings.Contains(text, want) {
			t.Fatalf("help text missing %q:\n%s", want, text)
		}
	}
}

func TestPrintMigratePortUsageStatesFactoryScope(t *testing.T) {
	var buf bytes.Buffer
	printMigratePortUsage(&buf)
	text := buf.String()
	if !strings.Contains(text, "default Factory settings") {
		t.Fatalf("help should mention default Factory settings")
	}
	if !strings.Contains(text, "No alternate Factory") {
		t.Fatalf("help should state no alternate Factory file is scanned")
	}
}

func TestPrintMigratePortUsageStatesNoMigratePortScope(t *testing.T) {
	var buf bytes.Buffer
	printMigratePortUsage(&buf)
	text := buf.String()
	if !strings.Contains(text, "takes no value") {
		t.Fatalf("help should state --no-migrate-port takes no value")
	}
	if !strings.Contains(text, "invocation") {
		t.Fatalf("help should state opt-out is invocation-scoped")
	}
}

// --- Functional tests using temp HOME ---

func setupMigrationTestEnv(t *testing.T) (homeDir, configPath, factoryPath string) {
	t.Helper()
	homeDir = t.TempDir()
	configDir := filepath.Join(homeDir, ".config", "droid-proxy")
	os.MkdirAll(configDir, 0o700)
	configPath = filepath.Join(configDir, "config.yaml")

	factoryDir := filepath.Join(homeDir, ".factory")
	os.MkdirAll(factoryDir, 0o700)
	factoryPath = filepath.Join(factoryDir, "settings.json")

	// Set HOME for the test.
	oldHome := os.Getenv("HOME")
	t.Setenv("HOME", homeDir)
	_ = oldHome

	return homeDir, configPath, factoryPath
}

func TestMigratePortDryRunDoesNotMutate(t *testing.T) {
	homeDir, configPath, factoryPath := setupMigrationTestEnv(t)
	_ = homeDir

	configContent := "listen:\n  host: 127.0.0.1\n  port: 8787\nmodels:\n  - alias: m\n    factory_provider: generic-chat-completion-api\n    upstream_protocol: openai-chat\n    upstream_model: m\n    base_url: http://u/v1\n    api_key_env: KEY\n"
	os.WriteFile(configPath, []byte(configContent), 0o600)
	factoryContent := `{"customModels":[{"model":"m","provider":"generic-chat-completion-api","baseUrl":"http://127.0.0.1:8787"}]}`
	os.WriteFile(factoryPath, []byte(factoryContent), 0o600)

	configBefore, _ := os.ReadFile(configPath)
	factoryBefore, _ := os.ReadFile(factoryPath)

	// Run dry-run.
	stdout, stderr, _ := captureMigratePortOutputSafely([]string{"--dry-run", "--config", configPath})

	if stderr != "" {
		t.Fatalf("unexpected stderr: %s", stderr)
	}
	if !strings.Contains(stdout, "dry-run") {
		t.Fatalf("expected dry-run indicator in output:\n%s", stdout)
	}
	if !strings.Contains(stdout, "8787") || !strings.Contains(stdout, "9787") {
		t.Fatalf("expected port info in output:\n%s", stdout)
	}

	configAfter, _ := os.ReadFile(configPath)
	factoryAfter, _ := os.ReadFile(factoryPath)
	if string(configBefore) != string(configAfter) {
		t.Fatal("config was mutated during dry-run")
	}
	if string(factoryBefore) != string(factoryAfter) {
		t.Fatal("factory was mutated during dry-run")
	}
}

func TestMigratePortCommitMigrates(t *testing.T) {
	_, configPath, factoryPath := setupMigrationTestEnv(t)

	configContent := "listen:\n  host: 127.0.0.1\n  port: 8787\nmodels:\n  - alias: m\n    factory_provider: generic-chat-completion-api\n    upstream_protocol: openai-chat\n    upstream_model: m\n    base_url: http://u/v1\n    api_key_env: KEY\n"
	os.WriteFile(configPath, []byte(configContent), 0o600)
	factoryContent := `{"customModels":[{"model":"m","provider":"generic-chat-completion-api","baseUrl":"http://127.0.0.1:8787"}]}`
	os.WriteFile(factoryPath, []byte(factoryContent), 0o600)

	stdout, _, _ := captureMigratePortOutputSafely([]string{"--config", configPath})

	if !strings.Contains(stdout, "complete") {
		t.Fatalf("expected completion message:\n%s", stdout)
	}

	cfgData, _ := os.ReadFile(configPath)
	if !strings.Contains(string(cfgData), "port: 9787") {
		t.Fatalf("config not migrated:\n%s", cfgData)
	}
	facData, _ := os.ReadFile(factoryPath)
	if !strings.Contains(string(facData), ":9787") {
		t.Fatalf("factory not migrated:\n%s", facData)
	}
}

func TestMigratePortNoopForNewPort(t *testing.T) {
	_, configPath, _ := setupMigrationTestEnv(t)
	os.WriteFile(configPath, []byte("listen:\n  host: 127.0.0.1\n  port: 9787\n"), 0o600)

	stdout, _, _ := captureMigratePortOutputSafely([]string{"--config", configPath})
	if !strings.Contains(stdout, "no eligible") {
		t.Fatalf("expected no-eligible message:\n%s", stdout)
	}
}

func TestMigratePortRollbackNoTransactions(t *testing.T) {
	_, configPath, _ := setupMigrationTestEnv(t)
	os.WriteFile(configPath, []byte("listen:\n  host: 127.0.0.1\n  port: 9787\n"), 0o600)

	stdout, _, _ := captureMigratePortOutputSafely([]string{"--rollback", "--config", configPath})
	if !strings.Contains(stdout, "no completed migration transactions") {
		t.Fatalf("expected no-transactions message:\n%s", stdout)
	}
}

func TestMigratePortRefusesNonLoopback(t *testing.T) {
	_, configPath, _ := setupMigrationTestEnv(t)
	os.WriteFile(configPath, []byte("listen:\n  host: 0.0.0.0\n  port: 8787\n"), 0o600)

	stdout, _, _ := captureMigratePortOutputSafely([]string{"--config", configPath})
	if !strings.Contains(stdout, "no eligible") {
		t.Fatalf("expected no-eligible for non-loopback:\n%s", stdout)
	}
}

func TestMigratePortDryRunReportNoSecrets(t *testing.T) {
	_, configPath, factoryPath := setupMigrationTestEnv(t)
	configContent := "listen:\n  host: 127.0.0.1\n  port: 8787\nclient_auth:\n  enabled: true\n  api_keys:\n    - sk-test-secret\nmodels:\n  - alias: m\n    factory_provider: generic-chat-completion-api\n    upstream_protocol: openai-chat\n    upstream_model: m\n    base_url: http://u/v1\n    api_key_env: KEY\n"
	os.WriteFile(configPath, []byte(configContent), 0o600)
	os.WriteFile(factoryPath, []byte(`{"customModels":[{"model":"m","provider":"generic-chat-completion-api","baseUrl":"http://127.0.0.1:8787","apiKey":"sk-fac-secret"}]}`), 0o600)

	stdout, _, _ := captureMigratePortOutputSafely([]string{"--dry-run", "--config", configPath})
	if strings.Contains(stdout, "sk-test-secret") {
		t.Fatalf("dry-run leaked config secret:\n%s", stdout)
	}
	if strings.Contains(stdout, "sk-fac-secret") {
		t.Fatalf("dry-run leaked factory secret:\n%s", stdout)
	}
}

// TestMigratePortRefusesSameConfigRunningDaemon verifies that explicit
// migration detects when a running daemon uses the same config, which causes
// refusal with stop-and-retry guidance (VAL-PORT-005). We test
// configUsesThisConfig directly because runMigratePortCommit calls os.Exit
// on refusal.
func TestMigratePortRefusesSameConfigRunningDaemon(t *testing.T) {
	homeDir, configPath, _ := setupMigrationTestEnv(t)
	_ = homeDir

	configContent := "listen:\n  host: 127.0.0.1\n  port: 8787\nmodels:\n  - alias: m\n    factory_provider: generic-chat-completion-api\n    upstream_protocol: openai-chat\n    upstream_model: m\n    base_url: http://u/v1\n    api_key_env: KEY\n"
	os.WriteFile(configPath, []byte(configContent), 0o600)

	// Simulate a running daemon that uses the same config.
	absConfig, _ := filepath.Abs(configPath)
	exe, _ := os.Executable()
	droidProxyDir := filepath.Join(homeDir, ".droid-proxy")
	os.MkdirAll(droidProxyDir, 0o700)

	// Write PID file with current process PID (alive).
	pid := os.Getpid()
	pidStr := strings.TrimSpace(fmt.Sprintf("%d", pid))
	os.WriteFile(filepath.Join(droidProxyDir, "droid-proxy.pid"), []byte(pidStr), 0o600)

	// Write runtime metadata pointing to the same config.
	meta := daemon.RuntimeMetadata{
		PID:        pid,
		Executable: exe,
		ConfigPath: absConfig,
	}
	metaData, _ := json.MarshalIndent(meta, "", "  ")
	os.WriteFile(filepath.Join(droidProxyDir, "runtime.json"), metaData, 0o600)

	// configUsesThisConfig should detect the running daemon.
	foundPID, inUse := configUsesThisConfig(configPath)
	if !inUse {
		t.Fatal("expected configUsesThisConfig to detect running daemon using same config")
	}
	if foundPID != pid {
		t.Fatalf("found PID %d, want %d", foundPID, pid)
	}

	// Clean up daemon state.
	os.Remove(filepath.Join(droidProxyDir, "droid-proxy.pid"))
	os.Remove(filepath.Join(droidProxyDir, "runtime.json"))
}

// TestMigratePortAllowsDifferentConfigRunningDaemon verifies that explicit
// migration proceeds when a running daemon uses a different config, leaving
// the running daemon's config untouched (VAL-PORT-005).
func TestMigratePortAllowsDifferentConfigRunningDaemon(t *testing.T) {
	homeDir, configPath, _ := setupMigrationTestEnv(t)
	_ = homeDir

	configContent := "listen:\n  host: 127.0.0.1\n  port: 8787\nmodels:\n  - alias: m\n    factory_provider: generic-chat-completion-api\n    upstream_protocol: openai-chat\n    upstream_model: m\n    base_url: http://u/v1\n    api_key_env: KEY\n"
	os.WriteFile(configPath, []byte(configContent), 0o600)

	// Simulate a running daemon that uses a DIFFERENT config.
	otherConfig := filepath.Join(filepath.Dir(configPath), "other.yaml")
	os.WriteFile(otherConfig, []byte("listen:\n  host: 127.0.0.1\n  port: 8787\n"), 0o600)

	absOther, _ := filepath.Abs(otherConfig)
	exe, _ := os.Executable()
	droidProxyDir := filepath.Join(homeDir, ".droid-proxy")
	os.MkdirAll(droidProxyDir, 0o700)

	pid := os.Getpid()
	pidStr := strings.TrimSpace(fmt.Sprintf("%d", pid))
	os.WriteFile(filepath.Join(droidProxyDir, "droid-proxy.pid"), []byte(pidStr), 0o600)

	meta := daemon.RuntimeMetadata{
		PID:        pid,
		Executable: exe,
		ConfigPath: absOther,
	}
	metaData, _ := json.MarshalIndent(meta, "", "  ")
	os.WriteFile(filepath.Join(droidProxyDir, "runtime.json"), metaData, 0o600)

	// configUsesThisConfig should NOT detect the daemon (different config).
	_, inUse := configUsesThisConfig(configPath)
	if inUse {
		t.Fatal("expected configUsesThisConfig to not detect different-config daemon")
	}

	// Clean up daemon state.
	os.Remove(filepath.Join(droidProxyDir, "droid-proxy.pid"))
	os.Remove(filepath.Join(droidProxyDir, "runtime.json"))
}
func captureMigratePortOutputSafely(args []string) (stdout, stderr string, exitCode int) {
	oldStdout := os.Stdout
	oldStderr := os.Stderr
	rOut, wOut, _ := os.Pipe()
	rErr, wErr, _ := os.Pipe()
	os.Stdout = wOut
	os.Stderr = wErr

	defer func() {
		os.Stdout = oldStdout
		os.Stderr = oldStderr
	}()

	exitCode = -1
	func() {
		defer func() {
			recover()
		}()
		runMigratePort(args)
		exitCode = 0
	}()

	wOut.Close()
	wErr.Close()
	var bufOut, bufErr bytes.Buffer
	bufOut.ReadFrom(rOut)
	bufErr.ReadFrom(rErr)
	return bufOut.String(), bufErr.String(), exitCode
}
