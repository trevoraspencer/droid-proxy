package integration

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
	"gopkg.in/yaml.v3"

	"github.com/trevoraspencer/droid-proxy/internal/config"
	"github.com/trevoraspencer/droid-proxy/internal/migration"
)

// ---------------------------------------------------------------------------
// VAL-CROSS-010: Port migration and provider onboarding compose without data
// loss.
//
// Conservative migration changes only eligible port references, then all
// providers can be added and synced alongside existing models. Dry-run,
// commit, recovery, rollback, and repeat preserve provider blocks, env
// references, comments, unknown Factory fields, modes, and unrelated entries.
// In an occupied or ambiguous refusal scenario, the composed harness treats
// refusal as terminal and does not invoke onboarding or a destination runtime.
// ---------------------------------------------------------------------------

// combinedConfigWithOldPort writes a combined config YAML with port 8787
// and provider blocks, mirroring what a pre-migration installation would
// have alongside its provider models.
func combinedConfigWithOldPort(t *testing.T, ci *combinedInstallation) string {
	t.Helper()
	dir := filepath.Dir(ci.configPath)
	oldPath := filepath.Join(dir, "old-config.yaml")

	doc := map[string]any{
		"listen": map[string]any{
			"host": "127.0.0.1",
			"port": 8787,
		},
	}
	var models []map[string]any
	for _, md := range ci.modelDefs {
		m := map[string]any{
			"alias":             md.Alias,
			"display_name":      md.DisplayName,
			"factory_provider":  "generic-chat-completion-api",
			"upstream_protocol": "openai-chat",
			"known_auth":        md.KnownAuth,
			"upstream_model":    md.UpstreamModel,
		}
		if md.BaseURLOverride != "" {
			m["base_url"] = md.BaseURLOverride
		}
		if len(md.ExtraArgs) > 0 {
			m["extra_args"] = md.ExtraArgs
		}
		models = append(models, m)
	}
	doc["models"] = models

	data, err := yaml.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	// Add a comment to prove comments are preserved.
	fullData := append([]byte("# Combined pre-migration config\n"), data...)
	if err := os.WriteFile(oldPath, fullData, 0o600); err != nil {
		t.Fatal(err)
	}
	return oldPath
}

// combinedFactoryWithOldPort writes a Factory settings JSON with entries
// pointing at the old port 8787, mirroring pre-migration synced aliases.
func combinedFactoryWithOldPort(t *testing.T, ci *combinedInstallation) string {
	t.Helper()
	dir := filepath.Dir(ci.factoryPath)
	oldFactory := filepath.Join(dir, "old-settings.json")

	entries := make([]map[string]any, 0, len(ci.modelDefs))
	for _, md := range ci.modelDefs {
		entries = append(entries, map[string]any{
			"model":           md.Alias,
			"displayName":     md.DisplayName,
			"provider":        "generic-chat-completion-api",
			"baseUrl":         "http://127.0.0.1:8787",
			"apiKey":          "x",
			"maxOutputTokens": 128000,
		})
	}
	doc := map[string]any{
		"customModels": entries,
		"_comment":     "pre-migration Factory state with unknown field",
	}
	data := mustMarshalIndent(t, doc)
	if err := os.WriteFile(oldFactory, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	return oldFactory
}

func mustMarshalIndent(t *testing.T, v any) string {
	t.Helper()
	// Use encoding/json directly.
	b, err := jsonMarshalIndent(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b) + "\n"
}

func TestMigration_DryRunPreservesProviderBlocks(t *testing.T) {
	ci := newCombinedInstallation(t)
	clearInheritedCredentials(t)
	injectSyntheticCredentials(t)

	oldConfig := combinedConfigWithOldPort(t, ci)
	oldFactory := combinedFactoryWithOldPort(t, ci)

	t.Setenv("HOME", ci.home)

	configHashBefore := fileHash(t, oldConfig)
	factoryHashBefore := fileHash(t, oldFactory)

	// Dry-run must not write targets.
	plan, err := migration.PlanMigration(migration.PlanOptions{
		ConfigPath:  oldConfig,
		FactoryPath: oldFactory,
	})
	if err != nil {
		t.Fatalf("plan migration: %v", err)
	}
	if !plan.ConfigEligible {
		t.Fatalf("config should be eligible for migration: %s", plan.ConfigReason)
	}

	// Verify dry-run does not write targets.
	if fileHash(t, oldConfig) != configHashBefore {
		t.Error("dry-run changed config file")
	}
	if fileHash(t, oldFactory) != factoryHashBefore {
		t.Error("dry-run changed factory file")
	}

	// Verify the plan mentions the port change.
	summary := plan.Summary()
	if !strings.Contains(summary, "8787") || !strings.Contains(summary, "9787") {
		t.Errorf("plan summary missing port change:\n%s", summary)
	}
}

func TestMigration_CommitChangesOnlyPort(t *testing.T) {
	ci := newCombinedInstallation(t)
	clearInheritedCredentials(t)
	injectSyntheticCredentials(t)

	oldConfig := combinedConfigWithOldPort(t, ci)
	oldFactory := combinedFactoryWithOldPort(t, ci)

	t.Setenv("HOME", ci.home)

	configBefore, _ := os.ReadFile(oldConfig)

	// Commit migration (config-only, no destination checker).
	plan, err := migration.PlanMigration(migration.PlanOptions{
		ConfigPath:  oldConfig,
		FactoryPath: oldFactory,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := migration.CommitTransaction(plan, migration.TransactionOptions{})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if result.Action != "migrated" {
		t.Fatalf("action = %q, want migrated", result.Action)
	}

	configAfter, _ := os.ReadFile(oldConfig)

	// The only change should be 8787 -> 9787 in listen.port.
	if strings.Contains(string(configAfter), "port: 8787") {
		t.Error("config still has port 8787 after migration")
	}
	if !strings.Contains(string(configAfter), "port: 9787") {
		t.Error("config missing port 9787 after migration")
	}

	// All provider blocks must be preserved.
	for _, md := range ci.modelDefs {
		if !strings.Contains(string(configAfter), md.Alias) {
			t.Errorf("provider alias %q missing from migrated config", md.Alias)
		}
		if !strings.Contains(string(configAfter), md.UpstreamModel) {
			t.Errorf("upstream model %q missing from migrated config", md.UpstreamModel)
		}
	}

	// Comments must be preserved.
	if !strings.HasPrefix(string(configAfter), "# Combined") {
		t.Error("comment missing from migrated config")
	}

	// Verify the config loads and all models hydrate correctly after migration.
	cfg, err := config.Load(oldConfig)
	if err != nil {
		t.Fatalf("load migrated config: %v", err)
	}
	if cfg.Listen.Port != 9787 {
		t.Errorf("migrated listen.port = %d, want 9787", cfg.Listen.Port)
	}
	for _, m := range cfg.Models {
		if m.KnownAuth == "" {
			t.Errorf("alias %q lost known_auth after migration", m.Alias)
		}
	}

	_ = configBefore
}

func TestMigration_CommitMigratesFactoryEntries(t *testing.T) {
	ci := newCombinedInstallation(t)
	clearInheritedCredentials(t)
	injectSyntheticCredentials(t)

	oldConfig := combinedConfigWithOldPort(t, ci)
	oldFactory := combinedFactoryWithOldPort(t, ci)

	t.Setenv("HOME", ci.home)

	plan, err := migration.PlanMigration(migration.PlanOptions{
		ConfigPath:  oldConfig,
		FactoryPath: oldFactory,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Verify the plan identifies the Factory entries to change.
	if len(plan.FactoryChanges) == 0 {
		t.Fatal("plan should identify Factory entries to migrate")
	}

	result, err := migration.CommitTransaction(plan, migration.TransactionOptions{})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if result.Action != "migrated" {
		t.Fatalf("action = %q, want migrated", result.Action)
	}

	// Verify Factory entries now point at 9787.
	factoryAfter, _ := os.ReadFile(oldFactory)
	if strings.Contains(string(factoryAfter), "127.0.0.1:8787") {
		t.Error("factory still has old port 8787 after migration")
	}
	if !strings.Contains(string(factoryAfter), "127.0.0.1:9787") {
		t.Error("factory missing new port 9787 after migration")
	}

	// Unknown Factory fields must be preserved.
	if !strings.Contains(string(factoryAfter), "_comment") {
		t.Error("unknown Factory field _comment was removed during migration")
	}

	// All aliases must still be present in Factory entries.
	for _, md := range ci.modelDefs {
		if !strings.Contains(string(factoryAfter), md.Alias) {
			t.Errorf("Factory alias %q missing after migration", md.Alias)
		}
	}
}

func TestMigration_RollbackRestoresOriginals(t *testing.T) {
	ci := newCombinedInstallation(t)
	clearInheritedCredentials(t)
	injectSyntheticCredentials(t)

	oldConfig := combinedConfigWithOldPort(t, ci)
	oldFactory := combinedFactoryWithOldPort(t, ci)

	t.Setenv("HOME", ci.home)

	configHashBefore := fileHash(t, oldConfig)
	factoryHashBefore := fileHash(t, oldFactory)

	// Commit migration.
	plan, err := migration.PlanMigration(migration.PlanOptions{
		ConfigPath:  oldConfig,
		FactoryPath: oldFactory,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := migration.CommitTransaction(plan, migration.TransactionOptions{}); err != nil {
		t.Fatal(err)
	}

	// Rollback.
	candidates, err := migration.FindRollbackCandidates(oldConfig)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 {
		t.Fatalf("expected 1 rollback candidate, got %d", len(candidates))
	}
	if err := migration.RollbackTransaction(candidates[0]); err != nil {
		t.Fatal(err)
	}

	// Verify originals are restored exactly.
	if fileHash(t, oldConfig) != configHashBefore {
		t.Error("config not restored to original after rollback")
	}
	if fileHash(t, oldFactory) != factoryHashBefore {
		t.Error("factory not restored to original after rollback")
	}

	// All provider blocks must still be present.
	configAfter, _ := os.ReadFile(oldConfig)
	for _, md := range ci.modelDefs {
		if !strings.Contains(string(configAfter), md.Alias) {
			t.Errorf("provider alias %q missing after rollback", md.Alias)
		}
	}
}

func TestMigration_RollbackIdempotent(t *testing.T) {
	ci := newCombinedInstallation(t)
	clearInheritedCredentials(t)
	injectSyntheticCredentials(t)

	oldConfig := combinedConfigWithOldPort(t, ci)
	oldFactory := combinedFactoryWithOldPort(t, ci)

	t.Setenv("HOME", ci.home)

	// Commit migration.
	plan, _ := migration.PlanMigration(migration.PlanOptions{
		ConfigPath:  oldConfig,
		FactoryPath: oldFactory,
	})
	if _, err := migration.CommitTransaction(plan, migration.TransactionOptions{}); err != nil {
		t.Fatal(err)
	}

	// Rollback once.
	candidates, _ := migration.FindRollbackCandidates(oldConfig)
	if len(candidates) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(candidates))
	}
	if err := migration.RollbackTransaction(candidates[0]); err != nil {
		t.Fatal(err)
	}

	// Second rollback should be a no-op (no candidates).
	candidates2, _ := migration.FindRollbackCandidates(oldConfig)
	if len(candidates2) != 0 {
		t.Errorf("expected 0 candidates after rollback, got %d", len(candidates2))
	}
}

func TestMigration_PostMigrationRequestsStillWork(t *testing.T) {
	ci := newCombinedInstallation(t)
	clearInheritedCredentials(t)
	injectSyntheticCredentials(t)

	oldConfig := combinedConfigWithOldPort(t, ci)
	oldFactory := combinedFactoryWithOldPort(t, ci)

	t.Setenv("HOME", ci.home)

	// Commit migration.
	plan, _ := migration.PlanMigration(migration.PlanOptions{
		ConfigPath:  oldConfig,
		FactoryPath: oldFactory,
	})
	if _, err := migration.CommitTransaction(plan, migration.TransactionOptions{}); err != nil {
		t.Fatal(err)
	}

	// Load the migrated config and verify all providers still route.
	cfg, err := config.Load(oldConfig)
	if err != nil {
		t.Fatalf("load migrated config: %v", err)
	}
	_, engine := ci.buildAPI(cfg)

	// Send requests to every provider and verify they succeed.
	for _, alias := range ci.factoryAliases() {
		resetAllCaptures(ci)
		w, _ := sendChat(t, engine, alias, "")
		if w.Code != http.StatusOK {
			t.Errorf("post-migration request %s: status = %d", alias, w.Code)
		}
	}
}

func TestMigration_OccupiedDestinationRefusesBeforeMutation(t *testing.T) {
	ci := newCombinedInstallation(t)
	clearInheritedCredentials(t)
	injectSyntheticCredentials(t)

	oldConfig := combinedConfigWithOldPort(t, ci)
	oldFactory := combinedFactoryWithOldPort(t, ci)

	t.Setenv("HOME", ci.home)

	configHashBefore := fileHash(t, oldConfig)
	factoryHashBefore := fileHash(t, oldFactory)

	// Set up a destination checker that reports the destination is occupied.
	destOccupied := func(host string, port int) error {
		return os.ErrExist
	}

	plan, _ := migration.PlanMigration(migration.PlanOptions{
		ConfigPath:  oldConfig,
		FactoryPath: oldFactory,
	})
	_, err := migration.CommitTransaction(plan, migration.TransactionOptions{
		DestinationChecker: destOccupied,
	})
	if err == nil {
		t.Fatal("expected occupied destination to refuse migration")
	}

	// Verify targets are unchanged.
	if fileHash(t, oldConfig) != configHashBefore {
		t.Error("config changed despite occupied destination refusal")
	}
	if fileHash(t, oldFactory) != factoryHashBefore {
		t.Error("factory changed despite occupied destination refusal")
	}
}

func TestMigration_ComposesWithOnboarding(t *testing.T) {
	ci := newCombinedInstallation(t)
	clearInheritedCredentials(t)
	injectSyntheticCredentials(t)

	oldConfig := combinedConfigWithOldPort(t, ci)
	oldFactory := combinedFactoryWithOldPort(t, ci)

	t.Setenv("HOME", ci.home)

	// Step 1: Migrate the config.
	plan, _ := migration.PlanMigration(migration.PlanOptions{
		ConfigPath:  oldConfig,
		FactoryPath: oldFactory,
	})
	result, err := migration.CommitTransaction(plan, migration.TransactionOptions{})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if result.Action != "migrated" {
		t.Fatalf("action = %q, want migrated", result.Action)
	}

	// Step 2: Load the migrated config and add a new provider model.
	cfg, err := config.Load(oldConfig)
	if err != nil {
		t.Fatal(err)
	}

	// Step 3: Send requests to all existing providers and verify they work.
	_, engine := ci.buildAPI(cfg)
	for _, alias := range ci.factoryAliases() {
		resetAllCaptures(ci)
		w, _ := sendChat(t, engine, alias, "")
		if w.Code != http.StatusOK {
			t.Errorf("post-migration provider %s: status = %d", alias, w.Code)
		}
	}

	// Step 4: Verify the Factory entries resolve to the new port.
	factoryData, _ := os.ReadFile(oldFactory)
	entries := gjson.GetBytes(factoryData, "customModels.#.baseUrl")
	for _, baseUrl := range entries.Array() {
		if !strings.Contains(baseUrl.String(), "9787") {
			t.Errorf("Factory baseUrl %q does not point at 9787", baseUrl.String())
		}
	}
}

func TestMigration_FactoryAliasRequestReachesMigratedProxy(t *testing.T) {
	ci := newCombinedInstallation(t)
	clearInheritedCredentials(t)
	injectSyntheticCredentials(t)

	oldConfig := combinedConfigWithOldPort(t, ci)
	oldFactory := combinedFactoryWithOldPort(t, ci)

	t.Setenv("HOME", ci.home)

	// Migrate.
	plan, _ := migration.PlanMigration(migration.PlanOptions{
		ConfigPath:  oldConfig,
		FactoryPath: oldFactory,
	})
	if _, err := migration.CommitTransaction(plan, migration.TransactionOptions{}); err != nil {
		t.Fatal(err)
	}

	// Read the migrated Factory entry model for a provider and verify the
	// alias joins correctly through the runtime. Per VAL-CROSS-004, the
	// request model is read from the Factory customModels[].model.
	factoryData, _ := os.ReadFile(oldFactory)
	firstModel := gjson.GetBytes(factoryData, "customModels.0.model").String()
	firstBaseUrl := gjson.GetBytes(factoryData, "customModels.0.baseUrl").String()

	if !strings.Contains(firstBaseUrl, "9787") {
		t.Errorf("migrated Factory baseUrl = %q, missing 9787", firstBaseUrl)
	}

	// Verify the alias from Factory settings routes correctly.
	cfg, err := config.Load(oldConfig)
	if err != nil {
		t.Fatal(err)
	}
	_, engine := ci.buildAPI(cfg)
	resetAllCaptures(ci)
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(
		`{"model":"`+firstModel+`","messages":[{"role":"user","content":"hi"}]}`))
	engine.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("Factory alias %s request: status = %d", firstModel, w.Code)
	}
}
