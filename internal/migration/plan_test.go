package migration

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trevoraspencer/droid-proxy/internal/config"
)

func sha256sum(t *testing.T, data []byte) string {
	t.Helper()
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func fileSHA256(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return sha256sum(t, data)
}

// --- Plan: dry-run tests ---

func TestPlanMigrationDryRunNonMutating(t *testing.T) {
	dir := t.TempDir()
	configPath := writeConfigFile(t, dir, "config.yaml",
		"listen:\n  host: 127.0.0.1\n  port: 8787\nmodels:\n  - alias: m\n    factory_provider: generic-chat-completion-api\n    upstream_protocol: openai-chat\n    upstream_model: m\n    base_url: http://upstream/v1\n    api_key_env: KEY\n")
	factoryPath := filepath.Join(dir, "settings.json")
	writeFactoryFile(t, dir, `{"customModels":[{"model":"m","provider":"generic-chat-completion-api","baseUrl":"http://127.0.0.1:8787"}]}`)

	configBefore := fileSHA256(t, configPath)
	factoryBefore := fileSHA256(t, factoryPath)

	plan, err := PlanMigration(PlanOptions{
		ConfigPath:  configPath,
		FactoryPath: factoryPath,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Plan should be eligible.
	if !plan.ConfigEligible {
		t.Fatalf("config not eligible: %s", plan.ConfigReason)
	}
	if len(plan.FactoryChanges) != 1 {
		t.Fatalf("expected 1 factory change, got %d", len(plan.FactoryChanges))
	}

	// Verify dry-run does not mutate.
	_ = plan // dry-run is just the plan itself

	configAfter := fileSHA256(t, configPath)
	factoryAfter := fileSHA256(t, factoryPath)
	if configBefore != configAfter {
		t.Fatal("config was mutated during planning")
	}
	if factoryBefore != factoryAfter {
		t.Fatal("factory was mutated during planning")
	}
}

func TestPlanMigrationNoopForNewPort(t *testing.T) {
	dir := t.TempDir()
	configPath := writeConfigFile(t, dir, "config.yaml", "listen:\n  host: 127.0.0.1\n  port: 9787\n")

	plan, err := PlanMigration(PlanOptions{
		ConfigPath:  configPath,
		FactoryPath: filepath.Join(dir, "settings.json"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.HasChanges() {
		t.Fatal("expected no changes for new port")
	}
}

func TestPlanMigrationNoopForArbitraryPort(t *testing.T) {
	dir := t.TempDir()
	configPath := writeConfigFile(t, dir, "config.yaml", "listen:\n  host: 127.0.0.1\n  port: 5000\n")

	plan, _ := PlanMigration(PlanOptions{
		ConfigPath:  configPath,
		FactoryPath: filepath.Join(dir, "settings.json"),
	})
	if plan.HasChanges() {
		t.Fatal("expected no changes for arbitrary port")
	}
}

func TestPlanMigrationNoopForOmittedPort(t *testing.T) {
	dir := t.TempDir()
	configPath := writeConfigFile(t, dir, "config.yaml", "listen:\n  host: 127.0.0.1\n")

	plan, _ := PlanMigration(PlanOptions{
		ConfigPath:  configPath,
		FactoryPath: filepath.Join(dir, "settings.json"),
	})
	if plan.HasChanges() {
		t.Fatal("expected no changes for omitted port")
	}
}

func TestPlanMigrationAbsentFactory(t *testing.T) {
	dir := t.TempDir()
	configPath := writeConfigFile(t, dir, "config.yaml",
		"listen:\n  host: 127.0.0.1\n  port: 8787\nmodels:\n  - alias: m\n    factory_provider: generic-chat-completion-api\n    upstream_protocol: openai-chat\n    upstream_model: m\n    base_url: http://u/v1\n    api_key_env: KEY\n")

	plan, err := PlanMigration(PlanOptions{
		ConfigPath:  configPath,
		FactoryPath: filepath.Join(dir, "nonexistent.json"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.ConfigEligible {
		t.Fatalf("config not eligible: %s", plan.ConfigReason)
	}
	if plan.FactoryPresent {
		t.Fatal("expected factory absent")
	}
	if len(plan.FactoryChanges) != 0 {
		t.Fatal("expected 0 factory changes for absent factory")
	}
}

func TestPlanMigrationUnsafeFactoryAborts(t *testing.T) {
	dir := t.TempDir()
	configPath := writeConfigFile(t, dir, "config.yaml",
		"listen:\n  host: 127.0.0.1\n  port: 8787\nmodels:\n  - alias: m\n    factory_provider: generic-chat-completion-api\n    upstream_protocol: openai-chat\n    upstream_model: m\n    base_url: http://u/v1\n    api_key_env: KEY\n")
	factoryPath := writeFactoryFile(t, dir, `{bad json`)

	plan, err := PlanMigration(PlanOptions{
		ConfigPath:  configPath,
		FactoryPath: factoryPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.FactoryUnsafe {
		t.Fatal("expected factory unsafe")
	}
}

// --- Plan: commit tests ---

func TestCommitPlanRewritesConfigAndFactory(t *testing.T) {
	dir := t.TempDir()
	configContent := "listen:\n  host: 127.0.0.1\n  port: 8787\nmodels:\n  - alias: m\n    factory_provider: generic-chat-completion-api\n    upstream_protocol: openai-chat\n    upstream_model: m\n    base_url: http://u/v1\n    api_key_env: KEY\n"
	configPath := writeConfigFile(t, dir, "config.yaml", configContent)
	factoryContent := `{"customModels":[{"model":"m","provider":"generic-chat-completion-api","baseUrl":"http://127.0.0.1:8787"}]}`
	factoryPath := writeFactoryFile(t, dir, factoryContent)

	plan, err := PlanMigration(PlanOptions{
		ConfigPath:  configPath,
		FactoryPath: factoryPath,
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := CommitPlan(plan); err != nil {
		t.Fatal(err)
	}

	// Verify config port changed.
	cfgData, _ := os.ReadFile(configPath)
	if !strings.Contains(string(cfgData), "port: 9787") {
		t.Fatalf("config port not changed: %s", cfgData)
	}
	if strings.Contains(string(cfgData), "port: 8787") {
		t.Fatalf("old port still in config: %s", cfgData)
	}

	// Verify factory baseUrl changed.
	facData, _ := os.ReadFile(factoryPath)
	if !strings.Contains(string(facData), ":9787") {
		t.Fatalf("factory baseUrl not changed: %s", facData)
	}
	if strings.Contains(string(facData), ":8787") {
		t.Fatalf("old port still in factory: %s", facData)
	}
}

func TestCommitPlanPreservesMode(t *testing.T) {
	dir := t.TempDir()
	configPath := writeConfigFile(t, dir, "config.yaml",
		"listen:\n  host: 127.0.0.1\n  port: 8787\nmodels:\n  - alias: m\n    factory_provider: generic-chat-completion-api\n    upstream_protocol: openai-chat\n    upstream_model: m\n    base_url: http://u/v1\n    api_key_env: KEY\n")
	// Set stricter permissions.
	os.Chmod(configPath, 0o640)
	factoryPath := writeFactoryFile(t, dir, `{"customModels":[{"model":"m","provider":"generic-chat-completion-api","baseUrl":"http://127.0.0.1:8787"}]}`)
	os.Chmod(factoryPath, 0o600)

	plan, _ := PlanMigration(PlanOptions{
		ConfigPath:  configPath,
		FactoryPath: factoryPath,
	})
	if err := CommitPlan(plan); err != nil {
		t.Fatal(err)
	}

	cfgInfo, _ := os.Stat(configPath)
	if cfgInfo.Mode().Perm() != 0o640 {
		t.Fatalf("config mode changed: %v", cfgInfo.Mode().Perm())
	}
	facInfo, _ := os.Stat(factoryPath)
	if facInfo.Mode().Perm() != 0o600 {
		t.Fatalf("factory mode changed: %v", facInfo.Mode().Perm())
	}
}

func TestCommitPlanRefusesUnsafeFactory(t *testing.T) {
	dir := t.TempDir()
	configPath := writeConfigFile(t, dir, "config.yaml",
		"listen:\n  host: 127.0.0.1\n  port: 8787\nmodels:\n  - alias: m\n    factory_provider: generic-chat-completion-api\n    upstream_protocol: openai-chat\n    upstream_model: m\n    base_url: http://u/v1\n    api_key_env: KEY\n")
	factoryPath := writeFactoryFile(t, dir, `{bad json`)

	plan, _ := PlanMigration(PlanOptions{
		ConfigPath:  configPath,
		FactoryPath: factoryPath,
	})

	err := CommitPlan(plan)
	if err == nil {
		t.Fatal("expected error for unsafe factory")
	}

	// Config should not be mutated.
	cfgData, _ := os.ReadFile(configPath)
	if !strings.Contains(string(cfgData), "port: 8787") {
		t.Fatal("config was mutated despite unsafe factory")
	}
}

func TestCommitPlanNoChangesIsNoop(t *testing.T) {
	dir := t.TempDir()
	configPath := writeConfigFile(t, dir, "config.yaml", "listen:\n  host: 127.0.0.1\n  port: 9787\n")

	plan, _ := PlanMigration(PlanOptions{
		ConfigPath:  configPath,
		FactoryPath: filepath.Join(dir, "settings.json"),
	})
	if plan.HasChanges() {
		t.Fatal("expected no changes")
	}
	if err := CommitPlan(plan); err != nil {
		t.Fatal(err)
	}
}

func TestCommitPlanConfigOnlyMigration(t *testing.T) {
	dir := t.TempDir()
	configPath := writeConfigFile(t, dir, "config.yaml",
		"listen:\n  host: 127.0.0.1\n  port: 8787\nmodels:\n  - alias: m\n    factory_provider: generic-chat-completion-api\n    upstream_protocol: openai-chat\n    upstream_model: m\n    base_url: http://u/v1\n    api_key_env: KEY\n")
	// No Factory file.
	plan, _ := PlanMigration(PlanOptions{
		ConfigPath:  configPath,
		FactoryPath: filepath.Join(dir, "absent.json"),
	})
	if !plan.ConfigEligible {
		t.Fatalf("config not eligible: %s", plan.ConfigReason)
	}
	if err := CommitPlan(plan); err != nil {
		t.Fatal(err)
	}

	cfgData, _ := os.ReadFile(configPath)
	if !strings.Contains(string(cfgData), "port: 9787") {
		t.Fatalf("config not migrated: %s", cfgData)
	}

	// Factory file should not be created.
	if _, err := os.Stat(filepath.Join(dir, "absent.json")); !os.IsNotExist(err) {
		t.Fatal("factory file was created during config-only migration")
	}
}

// --- Plan: injected destination ports ---

func TestPlanMigrationWithInjectedPorts(t *testing.T) {
	dir := t.TempDir()
	configPath := writeConfigFile(t, dir, "config.yaml", "listen:\n  host: 127.0.0.1\n  port: 8787\nmodels:\n  - alias: m\n    factory_provider: generic-chat-completion-api\n    upstream_protocol: openai-chat\n    upstream_model: m\n    base_url: http://u/v1\n    api_key_env: KEY\n")
	factoryPath := writeFactoryFile(t, dir, `{"customModels":[{"model":"m","provider":"generic-chat-completion-api","baseUrl":"http://127.0.0.1:8787"}]}`)

	// Use injected ports: 8787 -> 9999.
	plan, err := PlanMigration(PlanOptions{
		ConfigPath:  configPath,
		FactoryPath: factoryPath,
		OldPort:     8787,
		NewPort:     9999,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.ConfigEligible {
		t.Fatalf("config not eligible")
	}
	if len(plan.FactoryChanges) != 1 {
		t.Fatalf("expected 1 factory change")
	}
	if plan.FactoryChanges[0].NewOrigin != "http://127.0.0.1:9999" {
		t.Fatalf("new origin = %s", plan.FactoryChanges[0].NewOrigin)
	}

	if err := CommitPlan(plan); err != nil {
		t.Fatal(err)
	}

	cfgData, _ := os.ReadFile(configPath)
	if !strings.Contains(string(cfgData), "port: 9999") {
		t.Fatalf("config not migrated to injected port: %s", cfgData)
	}
}

// --- Plan: summary sanitization ---

func TestPlanSummaryNoSecrets(t *testing.T) {
	dir := t.TempDir()
	configPath := writeConfigFile(t, dir, "config.yaml",
		"listen:\n  host: 127.0.0.1\n  port: 8787\nclient_auth:\n  enabled: true\n  api_keys:\n    - sk-super-secret-key\nmodels:\n  - alias: m\n    factory_provider: generic-chat-completion-api\n    upstream_protocol: openai-chat\n    upstream_model: m\n    base_url: http://u/v1\n    api_key_env: KEY\n")
	factoryPath := writeFactoryFile(t, dir, `{"customModels":[{"model":"m","provider":"generic-chat-completion-api","baseUrl":"http://127.0.0.1:8787","apiKey":"sk-factory-secret"}]}`)

	plan, _ := PlanMigration(PlanOptions{
		ConfigPath:  configPath,
		FactoryPath: factoryPath,
	})

	summary := plan.Summary()
	if strings.Contains(summary, "sk-super-secret-key") {
		t.Fatalf("summary leaked config secret: %s", summary)
	}
	if strings.Contains(summary, "sk-factory-secret") {
		t.Fatalf("summary leaked factory secret: %s", summary)
	}
}

func TestPlanSummaryContainsPathAndPort(t *testing.T) {
	dir := t.TempDir()
	configPath := writeConfigFile(t, dir, "config.yaml", "listen:\n  host: 127.0.0.1\n  port: 8787\n")

	plan, _ := PlanMigration(PlanOptions{
		ConfigPath:  configPath,
		FactoryPath: filepath.Join(dir, "settings.json"),
	})
	summary := plan.Summary()
	if !strings.Contains(summary, configPath) {
		t.Fatalf("summary missing config path: %s", summary)
	}
	if !strings.Contains(summary, "8787") {
		t.Fatalf("summary missing old port: %s", summary)
	}
	if !strings.Contains(summary, "9787") {
		t.Fatalf("summary missing new port: %s", summary)
	}
}

// --- Plan: config with models for Factory matching ---

func TestPlanMigrationFactoryMatchesByAliasAndProvider(t *testing.T) {
	dir := t.TempDir()
	configPath := writeConfigFile(t, dir, "config.yaml",
		"listen:\n  host: 127.0.0.1\n  port: 8787\n"+
			"models:\n"+
			"  - alias: my-model\n"+
			"    factory_provider: generic-chat-completion-api\n"+
			"    upstream_protocol: openai-chat\n"+
			"    upstream_model: upstream-m\n"+
			"    base_url: http://u/v1\n"+
			"    api_key_env: KEY\n")
	factoryPath := writeFactoryFile(t, dir, `{
  "customModels": [
    {"model": "my-model", "provider": "generic-chat-completion-api", "baseUrl": "http://127.0.0.1:8787"},
    {"model": "other-model", "provider": "generic-chat-completion-api", "baseUrl": "http://127.0.0.1:8787"}
  ]
}`)

	plan, _ := PlanMigration(PlanOptions{
		ConfigPath:  configPath,
		FactoryPath: factoryPath,
	})
	if len(plan.FactoryChanges) != 1 {
		t.Fatalf("expected 1 factory change (only my-model), got %d", len(plan.FactoryChanges))
	}
	if plan.FactoryChanges[0].Model != "my-model" {
		t.Fatalf("model = %s, want my-model", plan.FactoryChanges[0].Model)
	}
}

func TestPlanMigrationFactoryDoesNotMatchByAliasOnly(t *testing.T) {
	dir := t.TempDir()
	configPath := writeConfigFile(t, dir, "config.yaml",
		"listen:\n  host: 127.0.0.1\n  port: 8787\n"+
			"models:\n"+
			"  - alias: m\n"+
			"    factory_provider: openai\n"+
			"    upstream_protocol: openai-responses\n"+
			"    upstream_model: m\n")
	factoryPath := writeFactoryFile(t, dir, `{
  "customModels": [
    {"model": "m", "provider": "generic-chat-completion-api", "baseUrl": "http://127.0.0.1:8787"}
  ]
}`)

	plan, _ := PlanMigration(PlanOptions{
		ConfigPath:  configPath,
		FactoryPath: factoryPath,
	})
	// Model "m" is configured with provider "openai" but Factory entry has
	// provider "generic-chat-completion-api". No match.
	if len(plan.FactoryChanges) != 0 {
		t.Fatalf("expected 0 factory changes for provider mismatch, got %d", len(plan.FactoryChanges))
	}
}

// Verify config.DefaultListenPort is still correct.
func TestDefaultPortConstants(t *testing.T) {
	if config.DefaultListenPort != 9787 {
		t.Fatalf("DefaultListenPort = %d, want 9787", config.DefaultListenPort)
	}
	if config.OldDefaultListenPort != 8787 {
		t.Fatalf("OldDefaultListenPort = %d, want 8787", config.OldDefaultListenPort)
	}
}
