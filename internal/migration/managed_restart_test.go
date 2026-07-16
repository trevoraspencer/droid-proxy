package migration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setupMigrationFixture(t *testing.T) (stateRoot, configPath, factoryPath string) {
	t.Helper()
	stateRoot = t.TempDir()
	home := filepath.Join(stateRoot, "home")
	configDir := filepath.Join(home, ".config", "droid-proxy")
	os.MkdirAll(configDir, 0o700)
	configPath = filepath.Join(configDir, "config.yaml")
	factoryDir := filepath.Join(home, ".factory")
	os.MkdirAll(factoryDir, 0o700)
	factoryPath = filepath.Join(factoryDir, "settings.json")

	t.Setenv("HOME", home)

	// Write a config with explicit old-default port.
	configBody := "listen:\n  host: 127.0.0.1\n  port: 8787\nmodels:\n  - alias: my-model\n    factory_provider: generic-chat-completion-api\n    upstream_protocol: openai-chat\n    upstream_model: gpt-4\n    base_url: http://u/v1\n    api_key_env: TEST_KEY\n"
	if err := os.WriteFile(configPath, []byte(configBody), 0o600); err != nil {
		t.Fatal(err)
	}

	return stateRoot, configPath, factoryPath
}

func setupProvenanceForTest(t *testing.T, stateRoot, configPath, oldBin, newBin string) {
	t.Helper()
	configHash := ReadConfigHashForProvenance(configPath)
	rec := ProvenanceRecord{
		OldBinaryPath:       oldBin,
		OldBinaryHash:       ReadBinaryHashForProvenance(oldBin),
		InstalledBinaryPath: newBin,
		InstalledBinaryHash: ReadBinaryHashForProvenance(newBin),
		ServiceKind:         "background-daemon",
		ConfigPath:          configPath,
		ConfigHash:          configHash,
		CreatedAt:           "2025-01-01T00:00:00Z",
		// Background-daemon conditional provenance: PID and executable.
		BackgroundDaemonPID: 12345,
		BackgroundDaemonExe: newBin,
	}
	if err := WriteProvenance(stateRoot, rec); err != nil {
		t.Fatal(err)
	}
}

func availableDestination() func(string, int) error {
	return func(host string, port int) error { return nil }
}

func TestAttemptDeferredMigrationNoProvenance(t *testing.T) {
	stateRoot, configPath, _ := setupMigrationFixture(t)
	newBin := writeFakeBinary(t, filepath.Join(stateRoot, "new-bin"), "new-binary")

	result, err := AttemptDeferredMigration(ManagedRestartOptions{
		StateRoot:           stateRoot,
		ConfigPath:          configPath,
		InstalledBinaryPath: newBin,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Action != "no-provenance" {
		t.Fatalf("expected action 'no-provenance', got %q", result.Action)
	}
}

func TestAttemptDeferredMigrationNoMigratePort(t *testing.T) {
	stateRoot, configPath, _ := setupMigrationFixture(t)
	oldBin := writeFakeBinary(t, filepath.Join(stateRoot, "old-bin"), "old-binary")
	newBin := writeFakeBinary(t, filepath.Join(stateRoot, "new-bin"), "new-binary")
	setupProvenanceForTest(t, stateRoot, configPath, oldBin, newBin)

	result, err := AttemptDeferredMigration(ManagedRestartOptions{
		StateRoot:           stateRoot,
		ConfigPath:          configPath,
		InstalledBinaryPath: newBin,
		NoMigratePort:       true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Action != "skipped" {
		t.Fatalf("expected action 'skipped', got %q", result.Action)
	}

	// Provenance should still be present (not consumed).
	rec, _ := ReadProvenance(stateRoot)
	if rec == nil {
		t.Fatal("provenance should remain after --no-migrate-port skip")
	}
}

func TestAttemptDeferredMigrationEligibleMigrates(t *testing.T) {
	stateRoot, configPath, _ := setupMigrationFixture(t)
	oldBin := writeFakeBinary(t, filepath.Join(stateRoot, "old-bin"), "old-binary")
	newBin := writeFakeBinary(t, filepath.Join(stateRoot, "new-bin"), "new-binary")
	setupProvenanceForTest(t, stateRoot, configPath, oldBin, newBin)

	result, err := AttemptDeferredMigration(ManagedRestartOptions{
		StateRoot:           stateRoot,
		ConfigPath:          configPath,
		InstalledBinaryPath: newBin,
		DestinationChecker:  availableDestination(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Action != "migrated" {
		t.Fatalf("expected action 'migrated', got %q (reason: %s)", result.Action, result.Reason)
	}

	// Provenance should be consumed.
	rec, _ := ReadProvenance(stateRoot)
	if rec != nil {
		t.Fatal("provenance should be consumed after successful migration")
	}

	// Config should now have the new port.
	data, _ := os.ReadFile(configPath)
	if !strings.Contains(string(data), "9787") {
		t.Fatalf("config was not migrated, still contains old port")
	}
}

func TestAttemptDeferredMigrationConsumedOnce(t *testing.T) {
	stateRoot, configPath, _ := setupMigrationFixture(t)
	oldBin := writeFakeBinary(t, filepath.Join(stateRoot, "old-bin"), "old-binary")
	newBin := writeFakeBinary(t, filepath.Join(stateRoot, "new-bin"), "new-binary")
	setupProvenanceForTest(t, stateRoot, configPath, oldBin, newBin)

	// First call migrates.
	result1, err := AttemptDeferredMigration(ManagedRestartOptions{
		StateRoot:           stateRoot,
		ConfigPath:          configPath,
		InstalledBinaryPath: newBin,
		DestinationChecker:  availableDestination(),
	})
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	if result1.Action != "migrated" {
		t.Fatalf("first call: expected 'migrated', got %q", result1.Action)
	}

	// Second call is a stable no-op (no provenance).
	result2, err := AttemptDeferredMigration(ManagedRestartOptions{
		StateRoot:           stateRoot,
		ConfigPath:          configPath,
		InstalledBinaryPath: newBin,
	})
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if result2.Action != "no-provenance" {
		t.Fatalf("second call: expected 'no-provenance', got %q", result2.Action)
	}
}

func TestAttemptDeferredMigrationStaleProvenanceSkips(t *testing.T) {
	stateRoot, configPath, _ := setupMigrationFixture(t)
	oldBin := writeFakeBinary(t, filepath.Join(stateRoot, "old-bin"), "old-binary")
	newBin := writeFakeBinary(t, filepath.Join(stateRoot, "new-bin"), "new-binary")
	setupProvenanceForTest(t, stateRoot, configPath, oldBin, newBin)

	// Edit the config after provenance was recorded.
	if err := os.WriteFile(configPath, []byte("listen:\n  host: 127.0.0.1\n  port: 8787\nmodels:\n  - alias: changed\n    factory_provider: generic-chat-completion-api\n    upstream_protocol: openai-chat\n    upstream_model: m\n    base_url: http://u/v1\n    api_key_env: TEST_KEY\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := AttemptDeferredMigration(ManagedRestartOptions{
		StateRoot:           stateRoot,
		ConfigPath:          configPath,
		InstalledBinaryPath: newBin,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Action != "skipped" {
		t.Fatalf("expected action 'skipped' for stale provenance, got %q", result.Action)
	}

	// Config should be unchanged.
	data, _ := os.ReadFile(configPath)
	if !strings.Contains(string(data), "changed") {
		t.Fatal("config was modified despite stale provenance")
	}
}

func TestAttemptDeferredMigrationBinaryChangedSkips(t *testing.T) {
	stateRoot, configPath, _ := setupMigrationFixture(t)
	oldBin := writeFakeBinary(t, filepath.Join(stateRoot, "old-bin"), "old-binary")
	newBin := writeFakeBinary(t, filepath.Join(stateRoot, "new-bin"), "new-binary")
	setupProvenanceForTest(t, stateRoot, configPath, oldBin, newBin)

	// Replace the installed binary after provenance was recorded.
	if err := os.WriteFile(newBin, []byte("different-binary-content"), 0o755); err != nil {
		t.Fatal(err)
	}

	result, err := AttemptDeferredMigration(ManagedRestartOptions{
		StateRoot:           stateRoot,
		ConfigPath:          configPath,
		InstalledBinaryPath: newBin,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Action != "skipped" {
		t.Fatalf("expected action 'skipped' for changed binary, got %q", result.Action)
	}
}

func TestAttemptDeferredMigrationOmittedPortIneligible(t *testing.T) {
	stateRoot, configPath, _ := setupMigrationFixture(t)
	oldBin := writeFakeBinary(t, filepath.Join(stateRoot, "old-bin"), "old-binary")
	newBin := writeFakeBinary(t, filepath.Join(stateRoot, "new-bin"), "new-binary")

	// Write a config with omitted port (no listen.port).
	configBody := "listen:\n  host: 127.0.0.1\nmodels:\n  - alias: m\n    factory_provider: generic-chat-completion-api\n    upstream_protocol: openai-chat\n    upstream_model: gpt-4\n    base_url: http://u/v1\n    api_key_env: TEST_KEY\n"
	if err := os.WriteFile(configPath, []byte(configBody), 0o600); err != nil {
		t.Fatal(err)
	}

	setupProvenanceForTest(t, stateRoot, configPath, oldBin, newBin)

	result, err := AttemptDeferredMigration(ManagedRestartOptions{
		StateRoot:           stateRoot,
		ConfigPath:          configPath,
		InstalledBinaryPath: newBin,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Omitted port configs are never rewritten; provenance consumed as no
	// longer applicable.
	if result.Action != "ineligible" {
		t.Fatalf("expected action 'ineligible' for omitted port, got %q", result.Action)
	}

	// Config should be unchanged.
	data, _ := os.ReadFile(configPath)
	if strings.Contains(string(data), "port:") {
		t.Fatal("omitted-port config was modified")
	}

	// Provenance should be consumed.
	rec, _ := ReadProvenance(stateRoot)
	if rec != nil {
		t.Fatal("provenance should be consumed when config is ineligible")
	}
}

func TestAttemptDeferredMigrationRepeatedRestartIsStable(t *testing.T) {
	stateRoot, configPath, _ := setupMigrationFixture(t)
	oldBin := writeFakeBinary(t, filepath.Join(stateRoot, "old-bin"), "old-binary")
	newBin := writeFakeBinary(t, filepath.Join(stateRoot, "new-bin"), "new-binary")
	setupProvenanceForTest(t, stateRoot, configPath, oldBin, newBin)

	// First restart migrates.
	_, err := AttemptDeferredMigration(ManagedRestartOptions{
		StateRoot:           stateRoot,
		ConfigPath:          configPath,
		InstalledBinaryPath: newBin,
		DestinationChecker:  availableDestination(),
	})
	if err != nil {
		t.Fatalf("first restart: %v", err)
	}

	// Subsequent restarts are stable no-ops.
	for i := 0; i < 3; i++ {
		result, err := AttemptDeferredMigration(ManagedRestartOptions{
			StateRoot:           stateRoot,
			ConfigPath:          configPath,
			InstalledBinaryPath: newBin,
		})
		if err != nil {
			t.Fatalf("restart %d: %v", i+2, err)
		}
		if result.Action != "no-provenance" {
			t.Fatalf("restart %d: expected 'no-provenance', got %q", i+2, result.Action)
		}
	}

	// Config should still have the new port (not reverted or re-migrated).
	data, _ := os.ReadFile(configPath)
	if !strings.Contains(string(data), "9787") {
		t.Fatal("config lost its migrated port")
	}
}

func TestAttemptDeferredMigrationCustomPortIneligible(t *testing.T) {
	stateRoot, configPath, _ := setupMigrationFixture(t)
	oldBin := writeFakeBinary(t, filepath.Join(stateRoot, "old-bin"), "old-binary")
	newBin := writeFakeBinary(t, filepath.Join(stateRoot, "new-bin"), "new-binary")

	// Write a config with a custom non-default port.
	configBody := "listen:\n  host: 127.0.0.1\n  port: 5555\nmodels:\n  - alias: m\n    factory_provider: generic-chat-completion-api\n    upstream_protocol: openai-chat\n    upstream_model: gpt-4\n    base_url: http://u/v1\n    api_key_env: TEST_KEY\n"
	if err := os.WriteFile(configPath, []byte(configBody), 0o600); err != nil {
		t.Fatal(err)
	}

	setupProvenanceForTest(t, stateRoot, configPath, oldBin, newBin)

	result, err := AttemptDeferredMigration(ManagedRestartOptions{
		StateRoot:           stateRoot,
		ConfigPath:          configPath,
		InstalledBinaryPath: newBin,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Action != "ineligible" {
		t.Fatalf("expected 'ineligible' for custom port, got %q", result.Action)
	}
}

func TestResolveServiceKind(t *testing.T) {
	tests := []struct {
		launchd, systemd, background bool
		want                         string
	}{
		{true, false, false, "launchd"},
		{false, true, false, "systemd"},
		{false, false, true, "background-daemon"},
		{false, false, false, ""},
	}
	for _, tc := range tests {
		got := ResolveServiceKind(tc.launchd, tc.systemd, tc.background)
		if got != tc.want {
			t.Errorf("ResolveServiceKind(%v, %v, %v) = %q, want %q",
				tc.launchd, tc.systemd, tc.background, got, tc.want)
		}
	}
}
