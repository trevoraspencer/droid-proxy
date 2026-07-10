package livee2e

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/trevoraspencer/droid-proxy/internal/config"
	"gopkg.in/yaml.v3"
)

// mergeFilterPath resolves scripts/live-e2e/merge-custom-models.jq relative to
// this test file (internal/livee2e/ → repo root → scripts/live-e2e/).
func mergeFilterPath(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("..", "..", "scripts", "live-e2e", "merge-custom-models.jq"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("merge filter not found at %s: %v", p, err)
	}
	return p
}

// runMerge applies the jq merge filter to settingsPath with the given e2e set
// (a JSON array string) and returns the decoded customModels entries.
func runMerge(t *testing.T, jqBin, filter, settingsPath, e2e string) []map[string]any {
	t.Helper()
	cmd := exec.Command(jqBin, "--argjson", "e2e", e2e, "-f", filter, settingsPath)
	out, err := cmd.Output()
	if err != nil {
		stderr := ""
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = string(ee.Stderr)
		}
		t.Fatalf("jq merge failed: %v\nstderr: %s", err, stderr)
	}
	var settings struct {
		CustomModels []map[string]any `json:"customModels"`
	}
	if err := json.Unmarshal(out, &settings); err != nil {
		t.Fatalf("merge output is not valid JSON: %v\noutput: %s", err, out)
	}
	return settings.CustomModels
}

func modelIDs(models []map[string]any) map[string]int {
	counts := map[string]int{}
	for _, m := range models {
		if id, ok := m["model"].(string); ok {
			counts[id]++
		}
	}
	return counts
}

func TestCodexGPT56LiveE2EDefaultsUseExplicitSol(t *testing.T) {
	configPath, err := filepath.Abs(filepath.Join("..", "..", "docs", "live-e2e", "config.local.yaml.template"))
	if err != nil {
		t.Fatal(err)
	}
	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}

	type liveE2EModel struct {
		Alias         string         `yaml:"alias"`
		UpstreamModel string         `yaml:"upstream_model"`
		ExtraArgs     map[string]any `yaml:"extra_args"`
	}
	var template struct {
		Models []liveE2EModel `yaml:"models"`
	}
	if err := yaml.Unmarshal(configBytes, &template); err != nil {
		t.Fatalf("parse live-E2E config template: %v", err)
	}

	models := make(map[string]liveE2EModel, len(template.Models))
	for _, model := range template.Models {
		models[model.Alias] = model
	}
	const wantUpstream = "gpt-5.6-sol"
	if got := models["gpt-5.6"]; got.UpstreamModel != wantUpstream || got.ExtraArgs["service_tier"] != nil {
		t.Fatalf("standard Codex live-E2E default = %#v, want upstream %q with no service tier", got, wantUpstream)
	}
	if got := models["gpt-5.6-fast"]; got.UpstreamModel != wantUpstream || got.ExtraArgs["service_tier"] != "priority" {
		t.Fatalf("fast Codex live-E2E default = %#v, want upstream %q with priority tier", got, wantUpstream)
	}

	envPath, err := filepath.Abs(filepath.Join("..", "..", "docs", "live-e2e", "env.live-e2e.example"))
	if err != nil {
		t.Fatal(err)
	}
	envBytes, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(envBytes), `export CODEX_UPSTREAM_MODEL=`) {
		t.Fatal("live-E2E env example must not export the retired GPT-5.2 override")
	}

	// Reproduce an upgrade from the previous scaffold: the external env still
	// exports the old model id. The generated GPT-5.6 aliases must ignore it.
	t.Setenv("CODEX_UPSTREAM_MODEL", "gpt-5.2-codex")
	for key, value := range map[string]string{
		"DEEPSEEK_API_KEY":   "test-deepseek",
		"ZAI_CODING_API_KEY": "test-zai",
		"FIREWORKS_API_KEY":  "test-fireworks",
		"FIREWORKS_MODEL":    "accounts/test/models/test",
		"MIMO_API_KEY":       "test-mimo",
		"MIMO_KNOWN_AUTH":    "mimo",
	} {
		t.Setenv(key, value)
	}
	loaded, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("load live-E2E config with legacy env: %v", err)
	}
	for _, alias := range []string{"gpt-5.6", "gpt-5.6-fast"} {
		var got string
		for _, model := range loaded.Models {
			if model.Alias == alias {
				got = model.UpstreamModel
				break
			}
		}
		if got != wantUpstream {
			t.Fatalf("legacy env changed %s upstream to %q, want %q", alias, got, wantUpstream)
		}
	}

	directTestsPath, err := filepath.Abs(filepath.Join("..", "..", "scripts", "live-e2e", "05-direct-provider-tests.sh"))
	if err != nil {
		t.Fatal(err)
	}
	directTests, err := os.ReadFile(directTestsPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`assert_model_mapping "gpt-5.6" "gpt-5.6-sol"`,
		`assert_model_mapping "gpt-5.6-fast" "gpt-5.6-sol"`,
	} {
		if !strings.Contains(string(directTests), want) {
			t.Fatalf("direct live checks must verify loaded mapping %q", want)
		}
	}
}

func TestXAIOAuthLiveE2EMappingsIgnoreRetiredOverrides(t *testing.T) {
	configPath, err := filepath.Abs(filepath.Join("..", "..", "docs", "live-e2e", "config.local.yaml.template"))
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]string{
		"XAI_GROK_BUILD_MODEL": "wrong-build",
		"XAI_COMPOSER_MODEL":   "wrong-composer",
		"XAI_GROK_MODEL":       "wrong-grok",
		"DEEPSEEK_API_KEY":     "test-deepseek",
		"ZAI_CODING_API_KEY":   "test-zai",
		"FIREWORKS_API_KEY":    "test-fireworks",
		"FIREWORKS_MODEL":      "accounts/test/models/test",
		"MIMO_API_KEY":         "test-mimo",
		"MIMO_KNOWN_AUTH":      "mimo",
	} {
		t.Setenv(key, value)
	}
	loaded, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"grok-4.5":               "grok-4.5",
		"grok-build-0.1":         "grok-build-0.1",
		"grok-composer-2.5-fast": "grok-composer-2.5-fast",
		"grok-4.3":               "grok-4.3",
	}
	for _, model := range loaded.Models {
		if upstream, ok := want[model.Alias]; ok {
			if model.UpstreamModel != upstream {
				t.Errorf("retired env changed %s to %q, want %q", model.Alias, model.UpstreamModel, upstream)
			}
			delete(want, model.Alias)
		}
	}
	if len(want) != 0 {
		t.Fatalf("missing fixed xAI aliases: %#v", want)
	}
}

func TestCodexOAuthWalkthroughAndFactorySnippetAliasesMatch(t *testing.T) {
	walkthroughPath, err := filepath.Abs(filepath.Join("..", "..", "docs", "examples", "codex-oauth.md"))
	if err != nil {
		t.Fatal(err)
	}
	walkthroughBytes, err := os.ReadFile(walkthroughPath)
	if err != nil {
		t.Fatal(err)
	}
	walkthrough := string(walkthroughBytes)
	sectionAt := strings.Index(walkthrough, "## config.yaml")
	if sectionAt < 0 {
		t.Fatal("Codex walkthrough is missing config.yaml section")
	}
	fenceStart := strings.Index(walkthrough[sectionAt:], "```yaml\n")
	if fenceStart < 0 {
		t.Fatal("Codex walkthrough is missing YAML config fence")
	}
	fenced := walkthrough[sectionAt+fenceStart+len("```yaml\n"):]
	fenceEnd := strings.Index(fenced, "\n```")
	if fenceEnd < 0 {
		t.Fatal("Codex walkthrough YAML fence is not closed")
	}

	type documentedModel struct {
		Alias         string         `yaml:"alias"`
		UpstreamModel string         `yaml:"upstream_model"`
		ExtraArgs     map[string]any `yaml:"extra_args"`
	}
	var documented struct {
		Models []documentedModel `yaml:"models"`
	}
	if err := yaml.Unmarshal([]byte(fenced[:fenceEnd]), &documented); err != nil {
		t.Fatalf("parse walkthrough config: %v", err)
	}

	snippetPath, err := filepath.Abs(filepath.Join("..", "..", "docs", "factory-settings", "codex-oauth.json"))
	if err != nil {
		t.Fatal(err)
	}
	snippetBytes, err := os.ReadFile(snippetPath)
	if err != nil {
		t.Fatal(err)
	}
	var snippet struct {
		CustomModels []struct {
			Model string `json:"model"`
		} `json:"customModels"`
	}
	if err := json.Unmarshal(snippetBytes, &snippet); err != nil {
		t.Fatalf("parse Factory snippet: %v", err)
	}

	wantUpstreams := map[string]string{
		"gpt-5.6":            "gpt-5.6-sol",
		"gpt-5.6-fast":       "gpt-5.6-sol",
		"gpt-5.6-terra":      "gpt-5.6-terra",
		"gpt-5.6-terra-fast": "gpt-5.6-terra",
		"gpt-5.6-luna":       "gpt-5.6-luna",
		"gpt-5.6-luna-fast":  "gpt-5.6-luna",
	}
	documentedAliases := make(map[string]bool, len(documented.Models))
	for _, model := range documented.Models {
		if documentedAliases[model.Alias] {
			t.Fatalf("duplicate walkthrough alias %q", model.Alias)
		}
		documentedAliases[model.Alias] = true
		if got := model.UpstreamModel; got != wantUpstreams[model.Alias] {
			t.Fatalf("walkthrough %s upstream = %q, want %q", model.Alias, got, wantUpstreams[model.Alias])
		}
		tier, fast := model.ExtraArgs["service_tier"]
		if wantFast := strings.HasSuffix(model.Alias, "-fast"); fast != wantFast {
			t.Fatalf("walkthrough %s service_tier presence = %v, want %v", model.Alias, fast, wantFast)
		}
		if fast && tier != "priority" {
			t.Fatalf("walkthrough %s service_tier = %#v, want priority", model.Alias, tier)
		}
	}
	snippetAliases := make(map[string]bool, len(snippet.CustomModels))
	for _, model := range snippet.CustomModels {
		if snippetAliases[model.Model] {
			t.Fatalf("duplicate Factory snippet alias %q", model.Model)
		}
		snippetAliases[model.Model] = true
	}
	if !reflect.DeepEqual(documentedAliases, snippetAliases) {
		t.Fatalf("walkthrough aliases %#v do not match Factory snippet aliases %#v", documentedAliases, snippetAliases)
	}
	if len(documentedAliases) != len(wantUpstreams) {
		t.Fatalf("walkthrough aliases = %d, want %d GPT-5.6 aliases", len(documentedAliases), len(wantUpstreams))
	}
}

// TestMergeCustomModelsPreservesUnrelatedAndUpserts verifies the safe-by-default
// settings merge: an unrelated pre-existing model is preserved, an e2e model id
// that already exists is upserted (replaced, not duplicated, with the e2e
// definition winning), and the new e2e model is added — and a second pass is
// idempotent (same model set, no duplicates).
func TestMergeCustomModelsPreservesUnrelatedAndUpserts(t *testing.T) {
	jqBin, err := exec.LookPath("jq")
	if err != nil {
		t.Skip("jq not on PATH; skipping settings-merge test")
	}
	filter := mergeFilterPath(t)
	dir := t.TempDir()

	// Existing settings: one unrelated model (keep-me) and a stale copy of an
	// e2e model id (gpt-5.6) with an outdated displayName.
	existing := `{
      "someOtherKey": "preserved",
      "customModels": [
        {"model":"keep-me","displayName":"Keep","provider":"x","baseUrl":"https://k","maxOutputTokens":1},
        {"model":"gpt-5.6","displayName":"OLD codex","provider":"openai","baseUrl":"https://old","maxOutputTokens":42}
      ]
    }`
	settingsPath := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(settingsPath, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}

	e2e := `[
      {"model":"gpt-5.6","displayName":"GPT-5.6 Sol (Codex OAuth)","provider":"openai","baseUrl":"http://127.0.0.1:8787","apiKey":"not-required-when-client-auth-disabled","maxOutputTokens":128000},
      {"model":"fireworks-live","displayName":"Fireworks Live","provider":"generic-chat-completion-api","baseUrl":"http://127.0.0.1:8787","apiKey":"not-required-when-client-auth-disabled","maxOutputTokens":8192}
    ]`

	models := runMerge(t, jqBin, filter, settingsPath, e2e)
	counts := modelIDs(models)

	if counts["keep-me"] != 1 {
		t.Fatalf("unrelated model keep-me not preserved exactly once: counts=%v", counts)
	}
	if counts["gpt-5.6"] != 1 {
		t.Fatalf("e2e model gpt-5.6 not upserted exactly once (dup or missing): counts=%v", counts)
	}
	if counts["fireworks-live"] != 1 {
		t.Fatalf("new e2e model fireworks-live not added: counts=%v", counts)
	}
	if len(models) != 3 {
		t.Fatalf("expected 3 customModels (keep-me + 2 e2e), got %d: %v", len(models), counts)
	}
	// The upserted entry must carry the e2e definition, not the stale one.
	for _, m := range models {
		if m["model"] == "gpt-5.6" {
			if m["displayName"] != "GPT-5.6 Sol (Codex OAuth)" {
				t.Fatalf("gpt-5.6 not replaced with e2e definition: displayName=%v", m["displayName"])
			}
			if m["baseUrl"] != "http://127.0.0.1:8787" {
				t.Fatalf("gpt-5.6 baseUrl not from e2e set: %v", m["baseUrl"])
			}
		}
	}

	// Idempotency: feed the merged output back through the same filter; the model
	// set and count must be unchanged.
	merged := map[string]any{"someOtherKey": "preserved", "customModels": models}
	mergedBytes, err := json.Marshal(merged)
	if err != nil {
		t.Fatal(err)
	}
	rerunPath := filepath.Join(dir, "settings.rerun.json")
	if err := os.WriteFile(rerunPath, mergedBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	rerun := runMerge(t, jqBin, filter, rerunPath, e2e)
	rerunCounts := modelIDs(rerun)
	if len(rerun) != len(models) {
		t.Fatalf("merge not idempotent: first=%d second=%d", len(models), len(rerun))
	}
	for id, c := range counts {
		if rerunCounts[id] != c {
			t.Fatalf("merge not idempotent for %q: first=%d second=%d (all: %v)", id, c, rerunCounts[id], rerunCounts)
		}
	}
}

// TestMergeCustomModelsFromEmptyBase verifies a base with no customModels (e.g.
// a fresh settings file) yields exactly the e2e set.
func TestMergeCustomModelsFromEmptyBase(t *testing.T) {
	jqBin, err := exec.LookPath("jq")
	if err != nil {
		t.Skip("jq not on PATH; skipping settings-merge test")
	}
	filter := mergeFilterPath(t)
	dir := t.TempDir()

	settingsPath := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(settingsPath, []byte(`{"customModels":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	e2e := `[{"model":"only-e2e","displayName":"E2E","provider":"openai","baseUrl":"http://127.0.0.1:8787","maxOutputTokens":1}]`

	models := runMerge(t, jqBin, filter, settingsPath, e2e)
	if len(models) != 1 || models[0]["model"] != "only-e2e" {
		t.Fatalf("empty-base merge should yield only the e2e set, got %v", models)
	}
}

func TestMergeCustomModelsRetiresOnlyLegacyHarnessCodexAlias(t *testing.T) {
	jqBin, err := exec.LookPath("jq")
	if err != nil {
		t.Skip("jq not on PATH; skipping settings-merge test")
	}
	filter := mergeFilterPath(t)
	dir := t.TempDir()

	existing := `{
      "customModels": [
        {"model":"gpt-5.2-codex","displayName":"GPT-5.2 Codex (ChatGPT OAuth)","provider":"openai","baseUrl":"http://127.0.0.1:8787","maxOutputTokens":128000},
        {"model":"gpt-5.2-codex","displayName":"User-managed GPT-5.2","provider":"openai","baseUrl":"https://custom.example.test/v1","maxOutputTokens":64000},
        {"model":"keep-me","displayName":"Keep","provider":"x","baseUrl":"https://keep.example.test","maxOutputTokens":1}
      ]
    }`
	settingsPath := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(settingsPath, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}
	e2e := `[{"model":"gpt-5.6","displayName":"GPT-5.6 Sol (Codex OAuth)","provider":"openai","baseUrl":"http://127.0.0.1:8787","maxOutputTokens":128000}]`

	models := runMerge(t, jqBin, filter, settingsPath, e2e)
	counts := modelIDs(models)
	if counts["gpt-5.2-codex"] != 1 || counts["gpt-5.6"] != 1 || counts["keep-me"] != 1 || len(models) != 3 {
		t.Fatalf("legacy cleanup counts = %v, models=%v", counts, models)
	}
	for _, model := range models {
		if model["model"] == "gpt-5.2-codex" && model["displayName"] != "User-managed GPT-5.2" {
			t.Fatalf("merge removed the user-owned collision instead of the harness entry: %v", model)
		}
	}
}
