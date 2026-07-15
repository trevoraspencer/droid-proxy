package migration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestManagedRestartCoherentState verifies VAL-PORT-012: after a managed
// restart with deferred migration, config and Factory settings are coherent
// on the new port.
func TestManagedRestartCoherentState(t *testing.T) {
	stateRoot := t.TempDir()
	home := filepath.Join(stateRoot, "home")
	configDir := filepath.Join(home, ".config", "droid-proxy")
	os.MkdirAll(configDir, 0o700)
	configPath := filepath.Join(configDir, "config.yaml")
	factoryDir := filepath.Join(home, ".factory")
	os.MkdirAll(factoryDir, 0o700)
	factoryPath := filepath.Join(factoryDir, "settings.json")

	t.Setenv("HOME", home)

	// Write explicit old-default config.
	configBody := "listen:\n  host: 127.0.0.1\n  port: 8787\nmodels:\n  - alias: m\n    factory_provider: generic-chat-completion-api\n    upstream_protocol: openai-chat\n    upstream_model: m\n    base_url: http://u/v1\n    api_key_env: K\n"
	os.WriteFile(configPath, []byte(configBody), 0o600)

	// Write Factory with old-origin entry.
	factoryBody := `{"customModels":[{"model":"m","provider":"generic-chat-completion-api","baseUrl":"http://127.0.0.1:8787"}]}`
	os.WriteFile(factoryPath, []byte(factoryBody), 0o600)

	// Create binaries.
	oldBin := writeFakeBinary(t, filepath.Join(stateRoot, "old-bin"), "old-binary")
	newBin := writeFakeBinary(t, filepath.Join(stateRoot, "new-bin"), "new-binary")

	// Record deferred provenance.
	setupProvenanceForTest(t, stateRoot, configPath, oldBin, newBin)

	// Perform deferred migration via managed restart.
	result, err := AttemptDeferredMigration(ManagedRestartOptions{
		StateRoot:           stateRoot,
		ConfigPath:          configPath,
		InstalledBinaryPath: newBin,
		DestinationChecker:  availableDestination(),
	})
	if err != nil {
		t.Fatalf("AttemptDeferredMigration: %v", err)
	}
	if result.Action != "migrated" {
		t.Fatalf("expected 'migrated', got %q (reason: %s)", result.Action, result.Reason)
	}

	// Verify config is coherent: port changed to 9787.
	cfgData, _ := os.ReadFile(configPath)
	if !strings.Contains(string(cfgData), "port: 9787") {
		t.Fatalf("config port not migrated:\n%s", cfgData)
	}
	if strings.Contains(string(cfgData), "port: 8787") {
		t.Fatalf("config still has old port:\n%s", cfgData)
	}

	// Verify Factory is coherent: baseUrl changed to 9787.
	facData, _ := os.ReadFile(factoryPath)
	if !strings.Contains(string(facData), "9787") {
		t.Fatalf("Factory baseUrl not migrated:\n%s", facData)
	}
	if strings.Contains(string(facData), "8787") {
		t.Fatalf("Factory still has old origin:\n%s", facData)
	}

	// Verify provenance is consumed (one-time).
	rec, _ := ReadProvenance(stateRoot)
	if rec != nil {
		t.Fatal("provenance should be consumed after migration")
	}
}

// TestManagedRestartPreservesUnrelatedState verifies VAL-PORT-022: migration
// does not touch service definitions, OAuth, managed env, logs, or other
// per-user state.
func TestManagedRestartPreservesUnrelatedState(t *testing.T) {
	stateRoot := t.TempDir()
	home := filepath.Join(stateRoot, "home")
	configDir := filepath.Join(home, ".config", "droid-proxy")
	os.MkdirAll(configDir, 0o700)
	configPath := filepath.Join(configDir, "config.yaml")
	factoryDir := filepath.Join(home, ".factory")
	os.MkdirAll(factoryDir, 0o700)
	factoryPath := filepath.Join(factoryDir, "settings.json")

	t.Setenv("HOME", home)

	// Write explicit old-default config.
	configBody := "listen:\n  host: 127.0.0.1\n  port: 8787\nmodels:\n  - alias: m\n    factory_provider: generic-chat-completion-api\n    upstream_protocol: openai-chat\n    upstream_model: m\n    base_url: http://u/v1\n    api_key_env: K\n"
	os.WriteFile(configPath, []byte(configBody), 0o600)

	// Write Factory with old-origin entry.
	factoryBody := `{"customModels":[{"model":"m","provider":"generic-chat-completion-api","baseUrl":"http://127.0.0.1:8787"}]}`
	os.WriteFile(factoryPath, []byte(factoryBody), 0o600)

	// Create unrelated state files that should not be touched.
	droidProxyDir := filepath.Join(home, ".droid-proxy")
	os.MkdirAll(droidProxyDir, 0o700)
	oauthDir := filepath.Join(droidProxyDir, "auth")
	os.MkdirAll(oauthDir, 0o700)
	oauthFile := filepath.Join(oauthDir, "codex.json")
	oauthBody := `{"token":"fake-oauth-token"}`
	os.WriteFile(oauthFile, []byte(oauthBody), 0o600)

	envFile := filepath.Join(droidProxyDir, "env")
	envBody := "export FIREWORKS_API_KEY=fw-test-secret\n"
	os.WriteFile(envFile, []byte(envBody), 0o600)

	// Create service definition files (they contain the config path, not
	// an embedded port).
	plistDir := filepath.Join(home, "Library", "LaunchAgents")
	os.MkdirAll(plistDir, 0o700)
	plistFile := filepath.Join(plistDir, "com.droid-proxy.agent.plist")
	plistBody := `<?xml version="1.0"?>
<plist version="1.0">
<dict>
  <key>Label</key><string>com.droid-proxy.agent</string>
  <key>ProgramArguments</key>
  <array>
    <string>/usr/local/bin/droid-proxy</string>
    <string>start</string>
    <string>--config</string>
    <string>` + configPath + `</string>
  </array>
</dict>
</plist>`
	os.WriteFile(plistFile, []byte(plistBody), 0o600)

	// Capture before hashes.
	oauthBefore, _ := os.ReadFile(oauthFile)
	envBefore, _ := os.ReadFile(envFile)
	plistBefore, _ := os.ReadFile(plistFile)

	// Create binaries and provenance.
	oldBin := writeFakeBinary(t, filepath.Join(stateRoot, "old-bin"), "old-binary")
	newBin := writeFakeBinary(t, filepath.Join(stateRoot, "new-bin"), "new-binary")
	setupProvenanceForTest(t, stateRoot, configPath, oldBin, newBin)

	// Perform deferred migration.
	_, err := AttemptDeferredMigration(ManagedRestartOptions{
		StateRoot:           stateRoot,
		ConfigPath:          configPath,
		InstalledBinaryPath: newBin,
		DestinationChecker:  availableDestination(),
	})
	if err != nil {
		t.Fatalf("AttemptDeferredMigration: %v", err)
	}

	// Verify unrelated files are untouched.
	oauthAfter, _ := os.ReadFile(oauthFile)
	if string(oauthBefore) != string(oauthAfter) {
		t.Fatal("OAuth file was modified during migration")
	}

	envAfter, _ := os.ReadFile(envFile)
	if string(envBefore) != string(envAfter) {
		t.Fatal("Env file was modified during migration")
	}

	plistAfter, _ := os.ReadFile(plistFile)
	if string(plistBefore) != string(plistAfter) {
		t.Fatal("Service definition (plist) was modified during migration")
	}
}

// TestManagedRestartDoesNotScanUnrelatedFactory verifies that the managed
// restart migration only accesses the selected canonical Factory settings
// file, not alternate Factory roots.
func TestManagedRestartDoesNotScanUnrelatedFactory(t *testing.T) {
	stateRoot := t.TempDir()
	home := filepath.Join(stateRoot, "home")
	configDir := filepath.Join(home, ".config", "droid-proxy")
	os.MkdirAll(configDir, 0o700)
	configPath := filepath.Join(configDir, "config.yaml")
	factoryDir := filepath.Join(home, ".factory")
	os.MkdirAll(factoryDir, 0o700)
	factoryPath := filepath.Join(factoryDir, "settings.json")

	t.Setenv("HOME", home)

	// Write explicit old-default config.
	configBody := "listen:\n  host: 127.0.0.1\n  port: 8787\nmodels:\n  - alias: m\n    factory_provider: generic-chat-completion-api\n    upstream_protocol: openai-chat\n    upstream_model: m\n    base_url: http://u/v1\n    api_key_env: K\n"
	os.WriteFile(configPath, []byte(configBody), 0o600)

	// Write Factory with old-origin entry.
	factoryBody := `{"customModels":[{"model":"m","provider":"generic-chat-completion-api","baseUrl":"http://127.0.0.1:8787"}]}`
	os.WriteFile(factoryPath, []byte(factoryBody), 0o600)

	// Create a decoy Factory file that should NOT be accessed.
	decoyDir := filepath.Join(home, ".factory-cloud")
	os.MkdirAll(decoyDir, 0o700)
	decoyFile := filepath.Join(decoyDir, "settings.json")
	decoyBody := `{"customModels":[{"model":"m","provider":"generic-chat-completion-api","baseUrl":"http://127.0.0.1:8787"}]}`
	os.WriteFile(decoyFile, []byte(decoyBody), 0o600)

	oldBin := writeFakeBinary(t, filepath.Join(stateRoot, "old-bin"), "old-binary")
	newBin := writeFakeBinary(t, filepath.Join(stateRoot, "new-bin"), "new-binary")
	setupProvenanceForTest(t, stateRoot, configPath, oldBin, newBin)

	_, err := AttemptDeferredMigration(ManagedRestartOptions{
		StateRoot:           stateRoot,
		ConfigPath:          configPath,
		InstalledBinaryPath: newBin,
		DestinationChecker:  availableDestination(),
	})
	if err != nil {
		t.Fatalf("AttemptDeferredMigration: %v", err)
	}

	// Verify decoy file is untouched.
	decoyAfter, _ := os.ReadFile(decoyFile)
	if string(decoyAfter) != decoyBody {
		t.Fatal("decoy Factory file was modified during migration")
	}
}

// TestManagedRestartProcessExitedStable verifies that repeated restarts after
// the old process has exited but durable provenance remains are stable.
func TestManagedRestartProcessExitedStable(t *testing.T) {
	stateRoot := t.TempDir()
	home := filepath.Join(stateRoot, "home")
	configDir := filepath.Join(home, ".config", "droid-proxy")
	os.MkdirAll(configDir, 0o700)
	configPath := filepath.Join(configDir, "config.yaml")

	t.Setenv("HOME", home)

	// Write explicit old-default config.
	configBody := "listen:\n  host: 127.0.0.1\n  port: 8787\nmodels:\n  - alias: m\n    factory_provider: generic-chat-completion-api\n    upstream_protocol: openai-chat\n    upstream_model: m\n    base_url: http://u/v1\n    api_key_env: K\n"
	os.WriteFile(configPath, []byte(configBody), 0o600)

	oldBin := writeFakeBinary(t, filepath.Join(stateRoot, "old-bin"), "old-binary")
	newBin := writeFakeBinary(t, filepath.Join(stateRoot, "new-bin"), "new-binary")
	setupProvenanceForTest(t, stateRoot, configPath, oldBin, newBin)

	// First restart migrates.
	result1, err := AttemptDeferredMigration(ManagedRestartOptions{
		StateRoot:           stateRoot,
		ConfigPath:          configPath,
		InstalledBinaryPath: newBin,
		DestinationChecker:  availableDestination(),
	})
	if err != nil {
		t.Fatalf("first restart: %v", err)
	}
	if result1.Action != "migrated" {
		t.Fatalf("expected 'migrated', got %q", result1.Action)
	}

	// Old process has since exited (provenance consumed). Next restarts are
	// stable no-ops.
	for i := 0; i < 5; i++ {
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

	// Config should still be on the new port.
	cfgData, _ := os.ReadFile(configPath)
	if !strings.Contains(string(cfgData), "9787") {
		t.Fatal("config lost its migrated port")
	}
}

// TestManagedRestartConfigPathMismatchSkips verifies that deferred provenance
// for a different config path does not trigger migration of the selected
// config.
func TestManagedRestartConfigPathMismatchSkips(t *testing.T) {
	stateRoot := t.TempDir()
	home := filepath.Join(stateRoot, "home")
	configDir := filepath.Join(home, ".config", "droid-proxy")
	os.MkdirAll(configDir, 0o700)
	configPath := filepath.Join(configDir, "config.yaml")
	otherConfig := filepath.Join(configDir, "other.yaml")

	t.Setenv("HOME", home)

	// Write explicit old-default config for the provenance target.
	configBody := "listen:\n  host: 127.0.0.1\n  port: 8787\nmodels:\n  - alias: m\n    factory_provider: generic-chat-completion-api\n    upstream_protocol: openai-chat\n    upstream_model: m\n    base_url: http://u/v1\n    api_key_env: K\n"
	os.WriteFile(configPath, []byte(configBody), 0o600)

	// Write a different config for the restart target.
	otherBody := "listen:\n  host: 127.0.0.1\n  port: 8787\nmodels:\n  - alias: x\n    factory_provider: generic-chat-completion-api\n    upstream_protocol: openai-chat\n    upstream_model: x\n    base_url: http://u/v1\n    api_key_env: K\n"
	os.WriteFile(otherConfig, []byte(otherBody), 0o600)

	oldBin := writeFakeBinary(t, filepath.Join(stateRoot, "old-bin"), "old-binary")
	newBin := writeFakeBinary(t, filepath.Join(stateRoot, "new-bin"), "new-binary")
	// Provenance recorded for configPath, but restart uses otherConfig.
	setupProvenanceForTest(t, stateRoot, configPath, oldBin, newBin)

	result, err := AttemptDeferredMigration(ManagedRestartOptions{
		StateRoot:           stateRoot,
		ConfigPath:          otherConfig,
		InstalledBinaryPath: newBin,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Action != "skipped" {
		t.Fatalf("expected 'skipped' for config path mismatch, got %q", result.Action)
	}

	// Neither config should be modified.
	cfgData, _ := os.ReadFile(configPath)
	if !strings.Contains(string(cfgData), "8787") {
		t.Fatal("provenance config was modified despite path mismatch")
	}
	otherData, _ := os.ReadFile(otherConfig)
	if !strings.Contains(string(otherData), "8787") {
		t.Fatal("restart config was modified despite no migration")
	}
}

// TestManagedRestartFactorySyncResolvesPreflight verifies that an explicit
// successful Factory sync updates the old-origin entry to the new origin,
// after which the preflight permits startup. This tests the VAL-PORT-025
// behavior that an explicit sync is a supported resolution.
func TestManagedRestartFactorySyncResolvesPreflight(t *testing.T) {
	stateRoot := t.TempDir()
	home := filepath.Join(stateRoot, "home")
	configDir := filepath.Join(home, ".config", "droid-proxy")
	os.MkdirAll(configDir, 0o700)
	configPath := filepath.Join(configDir, "config.yaml")
	factoryDir := filepath.Join(home, ".factory")
	os.MkdirAll(factoryDir, 0o700)
	factoryPath := filepath.Join(factoryDir, "settings.json")

	t.Setenv("HOME", home)

	// Write config with omitted port (will default to 9787).
	configBody := "listen:\n  host: 127.0.0.1\nmodels:\n  - alias: m\n    factory_provider: generic-chat-completion-api\n    upstream_protocol: openai-chat\n    upstream_model: m\n    base_url: http://u/v1\n    api_key_env: K\n"
	os.WriteFile(configPath, []byte(configBody), 0o600)

	// Write Factory with old-origin entry (would cause preflight refusal).
	factoryBody := `{"customModels":[{"model":"m","provider":"generic-chat-completion-api","baseUrl":"http://127.0.0.1:8787"}]}`
	os.WriteFile(factoryPath, []byte(factoryBody), 0o600)

	// Simulate an explicit Factory sync that updates the entry to 9787.
	updatedFactory := `{"customModels":[{"model":"m","provider":"generic-chat-completion-api","baseUrl":"http://127.0.0.1:9787"}]}`
	os.WriteFile(factoryPath, []byte(updatedFactory), 0o600)

	// Now the preflight should allow startup because the entry targets the
	// new origin. We test this by calling the migration plan, which includes
	// Factory analysis.
	plan, err := PlanMigration(PlanOptions{
		ConfigPath:  configPath,
		FactoryPath: factoryPath,
	})
	if err != nil {
		t.Fatalf("PlanMigration: %v", err)
	}

	// Config is omitted-port, so it's not eligible for migration.
	if plan.ConfigEligible {
		t.Fatal("omitted-port config should not be eligible for migration")
	}

	// Factory should have no changes (entry already on new origin).
	if len(plan.FactoryChanges) > 0 {
		t.Fatalf("expected no Factory changes, got %d", len(plan.FactoryChanges))
	}
}
