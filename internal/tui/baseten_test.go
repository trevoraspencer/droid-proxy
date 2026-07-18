package tui

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"gopkg.in/yaml.v3"

	"github.com/trevoraspencer/droid-proxy/internal/config"
	"github.com/trevoraspencer/droid-proxy/internal/configedit"
	"github.com/trevoraspencer/droid-proxy/internal/factory"
)

// TestBasetenAppearsOnceInProviderChoices verifies Baseten appears exactly
// once in the provider picker with the correct credential env.
func TestBasetenAppearsOnceInProviderChoices(t *testing.T) {
	choices := buildProviderChoices()
	var count int
	var choice *providerChoice
	for i := range choices {
		if choices[i].kind == pkKnown && choices[i].ka.Name == "baseten" {
			count++
			choice = &choices[i]
		}
	}
	if count != 1 {
		t.Fatalf("baseten provider choice count = %d, want 1", count)
	}
	if choice.label != "Baseten" {
		t.Errorf("label = %q, want Baseten", choice.label)
	}
	if choice.ka.APIKeyEnv != "BASETEN_API_KEY" {
		t.Errorf("APIKeyEnv = %q, want BASETEN_API_KEY", choice.ka.APIKeyEnv)
	}
}

// TestBasetenRoutesToKeyInputWhenKeyAbsent verifies that selecting Baseten
// without a key present goes to the key input screen.
func TestBasetenRoutesToKeyInputWhenKeyAbsent(t *testing.T) {
	os.Unsetenv("BASETEN_API_KEY")
	ka, _ := config.LookupKnownAuth("baseten")
	m := model{
		be:  &backend{},
		sel: providerChoice{kind: pkKnown, ka: ka, label: ka.Label()},
	}
	next, _ := m.afterProviderChosen()
	got := next.(model)
	if got.screen != screenAddKey {
		t.Fatalf("screen = %v, want screenAddKey", got.screen)
	}
	if got.keyEnv != "BASETEN_API_KEY" {
		t.Errorf("keyEnv = %q, want BASETEN_API_KEY", got.keyEnv)
	}
}

// TestBasetenRoutesToDiscoverWhenKeySet verifies that selecting Baseten when
// the key is already set goes directly to the discovery screen.
func TestBasetenRoutesToDiscoverWhenKeySet(t *testing.T) {
	t.Setenv("BASETEN_API_KEY", "baseten-test-key")
	ka, _ := config.LookupKnownAuth("baseten")
	m := model{
		be:  &backend{},
		sel: providerChoice{kind: pkKnown, ka: ka, label: ka.Label()},
	}
	next, cmd := m.afterProviderChosen()
	got := next.(model)
	if got.screen != screenDiscover {
		t.Fatalf("screen = %v, want screenDiscover", got.screen)
	}
	if cmd == nil {
		t.Error("expected non-nil cmd for discovery")
	}
}

// TestBasetenAfterKeySavedGoesToDiscover verifies that after saving a key
// during Baseten onboarding, the flow proceeds to discovery.
func TestBasetenAfterKeySavedGoesToDiscover(t *testing.T) {
	ka, _ := config.LookupKnownAuth("baseten")
	m := model{
		be:  &backend{},
		sel: providerChoice{kind: pkKnown, ka: ka, label: ka.Label()},
	}
	next, cmd := m.afterKeySaved()
	got := next.(model)
	if got.screen != screenDiscover {
		t.Errorf("screen = %v, want screenDiscover", got.screen)
	}
	if cmd == nil {
		t.Error("expected non-nil cmd for discovery")
	}
}

// TestBasetenBuildModelPersistsProfile verifies that a Baseten model built
// from the form carries known_auth: baseten, generic-chat-completion-api,
// openai-chat, and no extra_args or capability defaults.
func TestBasetenBuildModelPersistsProfile(t *testing.T) {
	ka, _ := config.LookupKnownAuth("baseten")
	m := newFormModel(t, providerChoice{kind: pkKnown, ka: ka, label: ka.Label()}, map[string]string{
		"upstream_model":    "org/model-a",
		"alias":             "baseten-model",
		"display_name":      "Model A (Baseten)",
		"max_output_tokens": "128000",
	})
	built, err := m.buildModelFromForm()
	if err != nil {
		t.Fatalf("buildModelFromForm: %v", err)
	}
	if built.KnownAuth != "baseten" {
		t.Errorf("KnownAuth = %q, want baseten", built.KnownAuth)
	}
	if built.FactoryProvider != config.FactoryProviderGeneric {
		t.Errorf("FactoryProvider = %q, want generic-chat-completion-api", built.FactoryProvider)
	}
	if built.UpstreamProtocol != config.UpstreamOpenAIChat {
		t.Errorf("UpstreamProtocol = %q, want openai-chat", built.UpstreamProtocol)
	}
	if len(built.ExtraArgs) != 0 {
		t.Errorf("ExtraArgs should be empty, got %v", built.ExtraArgs)
	}
	if built.UpstreamModel != "org/model-a" {
		t.Errorf("UpstreamModel = %q", built.UpstreamModel)
	}
	// Native profile does not persist base_url or api_key_env.
	if built.BaseURL != "" {
		t.Errorf("BaseURL should be empty (inherited from known_auth), got %q", built.BaseURL)
	}
	if built.APIKeyEnv != "" {
		t.Errorf("APIKeyEnv should be empty (inherited from known_auth), got %q", built.APIKeyEnv)
	}
}

// TestBasetenModelRoundTripPersistsProfile verifies a Baseten model saves
// and reloads with the correct known_auth and profile fields.
func TestBasetenModelRoundTripPersistsProfile(t *testing.T) {
	ka, _ := config.LookupKnownAuth("baseten")
	m := newFormModel(t, providerChoice{kind: pkKnown, ka: ka, label: ka.Label()}, map[string]string{
		"upstream_model":    "org/model-a",
		"alias":             "baseten-model",
		"display_name":      "Model A (Baseten)",
		"max_output_tokens": "128000",
	})
	built, err := m.buildModelFromForm()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("models: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	be := &backend{configPath: path}
	if err := be.addModel(built); err != nil {
		t.Fatalf("addModel: %v", err)
	}
	models, err := configedit.LoadModels(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("expected 1 model, got %d", len(models))
	}
	got := models[0]
	if got.KnownAuth != "baseten" {
		t.Errorf("KnownAuth = %q, want baseten", got.KnownAuth)
	}
	if got.FactoryProvider != config.FactoryProviderGeneric {
		t.Errorf("FactoryProvider = %q, want generic", got.FactoryProvider)
	}
	if got.UpstreamProtocol != config.UpstreamOpenAIChat {
		t.Errorf("UpstreamProtocol = %q", got.UpstreamProtocol)
	}
	if got.UpstreamModel != "org/model-a" {
		t.Errorf("UpstreamModel = %q, want org/model-a", got.UpstreamModel)
	}
	// After hydration, base_url and api_key_env are filled from the registry.
	if got.BaseURL != "https://inference.baseten.co/v1" {
		t.Errorf("BaseURL = %q, want https://inference.baseten.co/v1", got.BaseURL)
	}
	if got.APIKeyEnv != "BASETEN_API_KEY" {
		t.Errorf("APIKeyEnv = %q, want BASETEN_API_KEY", got.APIKeyEnv)
	}
}

// TestBasetenManualSlugRoundTrip verifies that an absent-from-catalog opaque
// slug is preserved byte-for-byte through save and reload.
func TestBasetenManualSlugRoundTrip(t *testing.T) {
	opaqueSlug := "org/private:custom-deploy-v2"
	ka, _ := config.LookupKnownAuth("baseten")
	m := newFormModel(t, providerChoice{kind: pkKnown, ka: ka, label: ka.Label()}, map[string]string{
		"upstream_model": opaqueSlug,
		"alias":          "custom-baseten",
	})
	built, err := m.buildModelFromForm()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if built.UpstreamModel != opaqueSlug {
		t.Fatalf("built UpstreamModel = %q, want %q", built.UpstreamModel, opaqueSlug)
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("models: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	be := &backend{configPath: path}
	if err := be.addModel(built); err != nil {
		t.Fatalf("addModel: %v", err)
	}
	models, err := configedit.LoadModels(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	got := models[0]
	if got.UpstreamModel != opaqueSlug {
		t.Errorf("reloaded UpstreamModel = %q, want %q (byte-exact)", got.UpstreamModel, opaqueSlug)
	}
}

// TestBasetenDiscoveryCancellationIgnoresLateResult verifies that cancelling
// discovery increments the generation counter so a late result cannot alter
// current state.
func TestBasetenDiscoveryCancellationIgnoresLateResult(t *testing.T) {
	ka, _ := config.LookupKnownAuth("baseten")
	m := model{
		screen:             screenDiscover,
		discoverGeneration: 1,
		sel:                providerChoice{kind: pkKnown, ka: ka, label: ka.Label()},
	}
	// Simulate Escape during discovery — cancels and invalidates generation.
	next, _ := m.onKey(tea.KeyMsg{Type: tea.KeyEsc})
	got := next.(model)
	if got.screen != screenDashboard {
		t.Errorf("screen = %v, want screenDashboard after cancel", got.screen)
	}
	if got.discoverGeneration != 2 {
		t.Errorf("discoverGeneration = %d, want 2 (incremented to invalidate)", got.discoverGeneration)
	}
	// A late result with the old generation must be ignored.
	msg := discoverMsg{
		ids:        []string{"org/late-model"},
		generation: 1, // stale
	}
	next2, cmd := got.onDiscover(msg)
	got2 := next2.(model)
	if got2.screen != screenDashboard {
		t.Errorf("screen = %v, want screenDashboard (late result ignored)", got2.screen)
	}
	if cmd != nil {
		t.Error("expected nil cmd for stale result")
	}
}

// TestBasetenDiscoveryFailureShowsManualForm verifies that when Baseten
// discovery fails, the TUI presents a usable manual form without invented
// models.
func TestBasetenDiscoveryFailureShowsManualForm(t *testing.T) {
	ka, _ := config.LookupKnownAuth("baseten")
	m := model{
		discoverGeneration: 1,
		sel:                providerChoice{kind: pkKnown, ka: ka, label: ka.Label()},
	}
	msg := discoverMsg{
		err:        fmt.Errorf("provider returned 401 Unauthorized"),
		generation: 1,
	}
	next, cmd := m.onDiscover(msg)
	got := next.(model)
	if got.screen != screenForm {
		t.Fatalf("screen = %v, want screenForm (manual fallback)", got.screen)
	}
	if cmd == nil {
		t.Error("expected non-nil cmd for form blink")
	}
	if got.discoverFeedback == "" {
		t.Error("expected non-empty discoverFeedback for error case")
	}
	// No models should be invented.
	if len(got.pickItems) != 0 {
		t.Errorf("pickItems should be empty, got %v", got.pickItems)
	}
}

// TestBasetenDiscoveryEmptyResultsShowsManualForm verifies that an empty
// discovery result still presents a usable manual form.
func TestBasetenDiscoveryEmptyResultsShowsManualForm(t *testing.T) {
	ka, _ := config.LookupKnownAuth("baseten")
	m := model{
		discoverGeneration: 1,
		sel:                providerChoice{kind: pkKnown, ka: ka, label: ka.Label()},
	}
	msg := discoverMsg{
		ids:        []string{},
		generation: 1,
	}
	next, _ := m.onDiscover(msg)
	got := next.(model)
	if got.screen != screenForm {
		t.Fatalf("screen = %v, want screenForm", got.screen)
	}
	if got.discoverFeedback == "" {
		t.Error("expected non-empty discoverFeedback for empty results")
	}
}

// TestBasetenManualEntryAfterSuccessOpensEmptyForm verifies that selecting
// manual entry after a successful discovery opens an empty Baseten form
// without a second discovery request.
func TestBasetenManualEntryAfterSuccessOpensEmptyForm(t *testing.T) {
	ka, _ := config.LookupKnownAuth("baseten")
	m := model{
		discoverGeneration: 1,
		sel:                providerChoice{kind: pkKnown, ka: ka, label: ka.Label()},
	}
	// Simulate successful discovery.
	next, _ := m.onDiscover(discoverMsg{
		ids:        []string{"org/model-a", "org/model-b"},
		generation: 1,
	})
	got := next.(model)
	if got.screen != screenPickModel {
		t.Fatalf("screen = %v, want screenPickModel", got.screen)
	}
	if got.pickItems[0] != manualEntryLabel {
		t.Errorf("first item = %q, want manual entry label", got.pickItems[0])
	}
	// Select manual entry (cursor 0).
	got.pickCursor = 0
	next2, _ := got.keyPickModel(tea.KeyMsg{Type: tea.KeyEnter})
	got2 := next2.(model)
	if got2.screen != screenForm {
		t.Fatalf("screen = %v, want screenForm", got2.screen)
	}
	// Upstream model should be empty for manual entry.
	um := got2.formValue("upstream_model")
	if um != "" {
		t.Errorf("upstream_model should be empty for manual entry, got %q", um)
	}
}

// TestBasetenFormEscapeGoesToDashboard verifies Escape from the Baseten form
// returns to the dashboard without writes.
func TestBasetenFormEscapeGoesToDashboard(t *testing.T) {
	ka, _ := config.LookupKnownAuth("baseten")
	sel := providerChoice{kind: pkKnown, ka: ka, label: ka.Label()}
	m := newFormModel(t, sel, map[string]string{
		"upstream_model": "org/model-a",
		"alias":          "test",
	})
	next, _ := m.keyForm(tea.KeyMsg{Type: tea.KeyEsc})
	got := next.(model)
	if got.screen != screenDashboard {
		t.Errorf("screen = %v, want screenDashboard", got.screen)
	}
}

// TestBasetenPickerEscapeGoesToProviderPicker verifies Escape from the
// Baseten model picker returns to the provider picker (no serving path).
func TestBasetenPickerEscapeGoesToProviderPicker(t *testing.T) {
	ka, _ := config.LookupKnownAuth("baseten")
	m := model{
		be:        &backend{},
		sel:       providerChoice{kind: pkKnown, ka: ka, label: ka.Label()},
		screen:    screenPickModel,
		pickItems: []string{manualEntryLabel, "org/model-a"},
	}
	next, _ := m.keyPickModel(tea.KeyMsg{Type: tea.KeyEsc})
	got := next.(model)
	if got.screen != screenAddProvider {
		t.Errorf("screen = %v, want screenAddProvider", got.screen)
	}
}

// TestBasetenModelRouteSummary verifies the dashboard route summary shows
// "Baseten · openai-chat" for a hydrated Baseten model.
func TestBasetenModelRouteSummary(t *testing.T) {
	m := &config.Model{
		Alias:            "test-baseten",
		KnownAuth:        "baseten",
		UpstreamProtocol: config.UpstreamOpenAIChat,
	}
	summary := modelRouteSummary(m)
	if !strings.Contains(summary, "Baseten") {
		t.Errorf("route summary should contain 'Baseten', got %q", summary)
	}
	if !strings.Contains(summary, "openai-chat") {
		t.Errorf("route summary should contain 'openai-chat', got %q", summary)
	}
}

// TestBasetenBuildModelRejectsBlankAlias verifies the form validation rejects
// blank aliases without partial writes.
func TestBasetenBuildModelRejectsBlankAlias(t *testing.T) {
	ka, _ := config.LookupKnownAuth("baseten")
	m := newFormModel(t, providerChoice{kind: pkKnown, ka: ka, label: ka.Label()}, map[string]string{
		"upstream_model": "org/model-a",
		"alias":          "",
	})
	_, err := m.buildModelFromForm()
	if err == nil {
		t.Fatal("expected error for blank alias")
	}
}

// TestBasetenBuildModelRejectsBlankUpstream verifies the form validation
// rejects blank upstream model IDs.
func TestBasetenBuildModelRejectsBlankUpstream(t *testing.T) {
	ka, _ := config.LookupKnownAuth("baseten")
	m := newFormModel(t, providerChoice{kind: pkKnown, ka: ka, label: ka.Label()}, map[string]string{
		"upstream_model": "",
		"alias":          "baseten-test",
	})
	_, err := m.buildModelFromForm()
	if err == nil {
		t.Fatal("expected error for blank upstream model")
	}
}

// TestBasetenBuildModelRejectsDuplicateAlias verifies the addModel call
// rejects a duplicate alias without partial writes.
func TestBasetenBuildModelRejectsDuplicateAlias(t *testing.T) {
	ka, _ := config.LookupKnownAuth("baseten")
	m := newFormModel(t, providerChoice{kind: pkKnown, ka: ka, label: ka.Label()}, map[string]string{
		"upstream_model": "org/model-a",
		"alias":          "baseten-dup",
	})
	built, err := m.buildModelFromForm()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("models: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	be := &backend{configPath: path}
	if err := be.addModel(built); err != nil {
		t.Fatalf("first addModel: %v", err)
	}
	// Second add with same alias must fail.
	built2, _ := m.buildModelFromForm()
	err = be.addModel(built2)
	if err == nil {
		t.Fatal("expected error for duplicate alias")
	}
	// Only one model should be in the config.
	models, _ := configedit.LoadModels(path)
	if len(models) != 1 {
		t.Errorf("expected 1 model after duplicate rejection, got %d", len(models))
	}
}

// TestBasetenNativeInheritsSharedModelAPIBase verifies that selecting Baseten
// from the provider picker inherits the shared Model API base URL without
// asking for a deployment URL (unlike custom endpoints).
func TestBasetenNativeInheritsSharedModelAPIBase(t *testing.T) {
	ka, _ := config.LookupKnownAuth("baseten")
	m := newFormModel(t, providerChoice{kind: pkKnown, ka: ka, label: ka.Label()}, map[string]string{
		"upstream_model": "org/model-a",
		"alias":          "baseten-native-test",
	})
	built, err := m.buildModelFromForm()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	// Native profile: no explicit base_url on the model; it is inherited
	// from the registry during hydration.
	if built.BaseURL != "" {
		t.Errorf("Native Baseten should not persist base_url, got %q", built.BaseURL)
	}
	if built.APIKeyEnv != "" {
		t.Errorf("Native Baseten should not persist api_key_env, got %q", built.APIKeyEnv)
	}
	if built.KnownAuth != "baseten" {
		t.Errorf("KnownAuth = %q, want baseten", built.KnownAuth)
	}
}

// TestBasetenCustomEndpointIsDistinct verifies a custom endpoint model
// persists explicit base_url/api_key_env without known_auth: baseten.
func TestBasetenCustomEndpointIsDistinct(t *testing.T) {
	sel := providerChoice{kind: pkCustom, label: "Custom OpenAI-compatible endpoint"}
	m := newFormModel(t, sel, map[string]string{
		"base_url":       "https://my-deployment.baseten.co/v1",
		"api_key_env":    "CUSTOM_BASETEN_DEPLOY_KEY",
		"upstream_model": "custom-deploy",
		"alias":          "baseten-custom-test",
	})
	built, err := m.buildModelFromForm()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if built.KnownAuth == "baseten" {
		t.Error("Custom endpoint must NOT carry known_auth: baseten")
	}
	if built.BaseURL != "https://my-deployment.baseten.co/v1" {
		t.Errorf("BaseURL = %q, want explicit custom URL", built.BaseURL)
	}
	if built.APIKeyEnv != "CUSTOM_BASETEN_DEPLOY_KEY" {
		t.Errorf("APIKeyEnv = %q", built.APIKeyEnv)
	}
}

// TestBasetenBuildModelDoesNotInjectDefaults verifies no tier, reasoning, or
// capability defaults are injected for Baseten models.
func TestBasetenBuildModelDoesNotInjectDefaults(t *testing.T) {
	ka, _ := config.LookupKnownAuth("baseten")
	m := newFormModel(t, providerChoice{kind: pkKnown, ka: ka, label: ka.Label()}, map[string]string{
		"upstream_model": "org/model-a",
		"alias":          "baseten-no-defaults",
	})
	built, _ := m.buildModelFromForm()
	if _, exists := built.ExtraArgs["service_tier"]; exists {
		t.Error("Baseten must not inject service_tier")
	}
	if _, exists := built.ExtraArgs["reasoning_effort"]; exists {
		t.Error("Baseten must not inject reasoning_effort")
	}
	if built.Capabilities.Reasoning != "" {
		t.Errorf("Baseten must not inject reasoning capability, got %q", built.Capabilities.Reasoning)
	}
	if built.Capabilities.FactoryReasoning != "" {
		t.Errorf("Baseten must not inject factory reasoning, got %q", built.Capabilities.FactoryReasoning)
	}
}

// TestBasetenMaxOutputTokensRoundTrip verifies that a max_output_tokens value
// of 128000 survives form construction, YAML save, known-auth hydration, and
// reload exactly. This closes a VAL-BASETEN-006 evidence gap by proving the
// full persistence round-trip preserves the exact value.
func TestBasetenMaxOutputTokensRoundTrip(t *testing.T) {
	ka, _ := config.LookupKnownAuth("baseten")
	m := newFormModel(t, providerChoice{kind: pkKnown, ka: ka, label: ka.Label()}, map[string]string{
		"upstream_model":    "org/model-a",
		"alias":             "baseten-128k",
		"display_name":      "Model A (Baseten)",
		"max_output_tokens": "128000",
	})
	built, err := m.buildModelFromForm()
	if err != nil {
		t.Fatalf("buildModelFromForm: %v", err)
	}
	// Step 1: Form construction preserves 128000.
	if built.MaxOutputTokens != 128000 {
		t.Fatalf("form construction: MaxOutputTokens = %d, want 128000", built.MaxOutputTokens)
	}
	// Step 2: YAML save preserves 128000.
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("models: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	be := &backend{configPath: path}
	if err := be.addModel(built); err != nil {
		t.Fatalf("addModel: %v", err)
	}
	// Verify the raw YAML contains 128000.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	yamlStr := string(raw)
	if !strings.Contains(yamlStr, "128000") {
		t.Errorf("YAML does not contain 128000:\n%s", yamlStr)
	}
	// Step 3: known-auth hydration + reload preserves 128000.
	models, err := configedit.LoadModels(path)
	if err != nil {
		t.Fatalf("LoadModels: %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("expected 1 model, got %d", len(models))
	}
	got := models[0]
	if got.MaxOutputTokens != 128000 {
		t.Errorf("after reload+hydration: MaxOutputTokens = %d, want 128000", got.MaxOutputTokens)
	}
	// Hydration does not corrupt the value.
	if got.KnownAuth != "baseten" {
		t.Errorf("KnownAuth = %q, want baseten", got.KnownAuth)
	}
	if got.BaseURL != "https://inference.baseten.co/v1" {
		t.Errorf("BaseURL = %q (hydration should fill from registry)", got.BaseURL)
	}
}

// TestBasetenSavedYAMLExactFieldsNoRedundancy verifies that a saved Baseten
// model YAML contains exactly the expected fields and no redundant base_url,
// api_key_env, capability, reasoning, or Baseten defaults. This closes a
// VAL-BASETEN-006 evidence gap.
func TestBasetenSavedYAMLExactFieldsNoRedundancy(t *testing.T) {
	ka, _ := config.LookupKnownAuth("baseten")
	m := newFormModel(t, providerChoice{kind: pkKnown, ka: ka, label: ka.Label()}, map[string]string{
		"upstream_model":    "org/model-a",
		"alias":             "baseten-exact",
		"display_name":      "Model A (Baseten)",
		"max_output_tokens": "128000",
	})
	built, err := m.buildModelFromForm()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("models: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	be := &backend{configPath: path}
	if err := be.addModel(built); err != nil {
		t.Fatalf("addModel: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Models []map[string]any `yaml:"models"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(doc.Models) != 1 {
		t.Fatalf("expected 1 model, got %d", len(doc.Models))
	}
	entry := doc.Models[0]
	// Required fields must be present.
	requiredFields := map[string]any{
		"alias":             "baseten-exact",
		"display_name":      "Model A (Baseten)",
		"factory_provider":  "generic-chat-completion-api",
		"known_auth":        "baseten",
		"upstream_model":    "org/model-a",
		"max_output_tokens": 128000,
	}
	for field, want := range requiredFields {
		got, exists := entry[field]
		if !exists {
			t.Errorf("required field %q missing from YAML", field)
			continue
		}
		if fmt.Sprintf("%v", got) != fmt.Sprintf("%v", want) {
			t.Errorf("field %q = %v, want %v", field, got, want)
		}
	}
	// Redundant fields must NOT be present (inherited from known_auth).
	// Note: upstream_protocol IS expected as "openai-chat" per VAL-BASETEN-006;
	// it is not redundant because it explicitly identifies the transport.
	redundantFields := []string{
		"base_url",
		"api_key_env",
		"extra_args",
		"extra_headers",
		"capabilities",
	}
	for _, field := range redundantFields {
		if _, exists := entry[field]; exists {
			t.Errorf("redundant field %q should not be in saved YAML (inherited from known_auth)", field)
		}
	}
}

// TestBasetenSavePreservesUnrelatedYAMLNodes verifies that adding a Baseten
// model preserves unrelated YAML nodes, values, comments, and file mode.
func TestBasetenSavePreservesUnrelatedYAMLNodes(t *testing.T) {
	existingYAML := `# Top-level comment
listen:
  host: 127.0.0.1
  port: 9787

models:
  - alias: pre-existing-model
    upstream_model: org/existing
    factory_provider: generic-chat-completion-api

# Trailing comment
`
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(existingYAML), 0o600); err != nil {
		t.Fatal(err)
	}

	ka, _ := config.LookupKnownAuth("baseten")
	m := newFormModel(t, providerChoice{kind: pkKnown, ka: ka, label: ka.Label()}, map[string]string{
		"upstream_model": "org/new-baseten",
		"alias":          "new-baseten",
	})
	built, err := m.buildModelFromForm()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	be := &backend{configPath: path}
	if err := be.addModel(built); err != nil {
		t.Fatalf("addModel: %v", err)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	afterStr := string(after)
	// Comments preserved.
	if !strings.Contains(afterStr, "# Top-level comment") {
		t.Error("top-level comment was lost")
	}
	if !strings.Contains(afterStr, "# Trailing comment") {
		t.Error("trailing comment was lost")
	}
	// Unrelated nodes preserved.
	if !strings.Contains(afterStr, "host: 127.0.0.1") {
		t.Error("listen.host was lost")
	}
	if !strings.Contains(afterStr, "port: 9787") {
		t.Error("listen.port was lost")
	}
	// Pre-existing model preserved.
	if !strings.Contains(afterStr, "pre-existing-model") {
		t.Error("pre-existing model alias was lost")
	}
	// New model added.
	if !strings.Contains(afterStr, "new-baseten") {
		t.Error("new Baseten model was not added")
	}
	// File mode preserved.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("file mode = %o, want 0600", info.Mode().Perm())
	}
}

// TestBasetenDiscoveryFeedbackSecretSafe verifies that the TUI's discovery
// failure feedback for Baseten does not leak URLs, status details, response
// bodies, or credential sentinels. This provides VAL-BASETEN-003 secret-safety
// evidence.
func TestBasetenDiscoveryFeedbackSecretSafe(t *testing.T) {
	sensitiveError := fmt.Errorf("provider returned 500 Internal Server Error at https://inference.baseten.co/v1/models with key baseten-secret-123")
	sentinelStrings := []string{
		"https://inference.baseten.co",
		"500",
		"Internal Server Error",
		"/v1/models",
		"baseten-secret-123",
		"BASETEN_API_KEY",
	}

	ka, _ := config.LookupKnownAuth("baseten")

	t.Run("error", func(t *testing.T) {
		m := model{
			discoverGeneration: 1,
			sel:                providerChoice{kind: pkKnown, ka: ka, label: ka.Label()},
		}
		next, _ := m.onDiscover(discoverMsg{err: sensitiveError, generation: 1})
		got := next.(model)
		if got.screen != screenForm {
			t.Fatalf("screen = %v, want screenForm", got.screen)
		}
		if got.discoverFeedback == "" {
			t.Fatal("expected non-empty discoverFeedback")
		}
		for _, s := range sentinelStrings {
			if strings.Contains(got.discoverFeedback, s) {
				t.Errorf("discoverFeedback leaked %q: %q", s, got.discoverFeedback)
			}
		}
		if !strings.Contains(strings.ToLower(got.discoverFeedback), "manual") {
			t.Errorf("discoverFeedback should mention manual entry: %q", got.discoverFeedback)
		}
	})

	t.Run("empty", func(t *testing.T) {
		m := model{
			discoverGeneration: 1,
			sel:                providerChoice{kind: pkKnown, ka: ka, label: ka.Label()},
		}
		next, _ := m.onDiscover(discoverMsg{ids: []string{}, generation: 1})
		got := next.(model)
		if got.screen != screenForm {
			t.Fatalf("screen = %v, want screenForm", got.screen)
		}
		if got.discoverFeedback == "" {
			t.Fatal("expected non-empty discoverFeedback")
		}
		for _, s := range sentinelStrings {
			if strings.Contains(got.discoverFeedback, s) {
				t.Errorf("discoverFeedback leaked %q: %q", s, got.discoverFeedback)
			}
		}
	})
}

// TestBasetenDiscoveryFailureNoPartialWrites verifies that a discovery failure
// leaves config and Factory state unchanged. Before model submission, no
// config or Factory write should occur. This provides VAL-BASETEN-003
// stage-safe evidence.
func TestBasetenDiscoveryFailureNoPartialWrites(t *testing.T) {
	ka, _ := config.LookupKnownAuth("baseten")
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	initialConfig := "models: []\n"
	if err := os.WriteFile(configPath, []byte(initialConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	factoryDir := t.TempDir()
	factoryPath := filepath.Join(factoryDir, "settings.json")

	// Compute initial hashes.
	initialConfigHash := sha256sum(t, configPath)

	// Simulate a discovery failure arriving at the TUI.
	m := model{
		discoverGeneration: 1,
		sel:                providerChoice{kind: pkKnown, ka: ka, label: ka.Label()},
		be:                 &backend{configPath: configPath, factoryPath: factoryPath},
	}
	next, _ := m.onDiscover(discoverMsg{
		err:        fmt.Errorf("provider returned 401 Unauthorized"),
		generation: 1,
	})
	got := next.(model)

	// The TUI should show a manual form, not write any config or Factory state.
	if got.screen != screenForm {
		t.Fatalf("screen = %v, want screenForm", got.screen)
	}

	// Config must be unchanged.
	postConfigHash := sha256sum(t, configPath)
	if initialConfigHash != postConfigHash {
		t.Errorf("config was modified by discovery failure: before=%s after=%s", initialConfigHash, postConfigHash)
	}

	// No Factory file should have been created.
	if _, err := os.Stat(factoryPath); !os.IsNotExist(err) {
		t.Errorf("Factory file should not exist after discovery failure")
	}
}

// TestBasetenFactorySyncIdempotentFromZero verifies that repeated Factory sync
// of a single Baseten model from an empty Factory state produces exactly one
// entry, not duplicates. This provides VAL-BASETEN-006 idempotency evidence.
func TestBasetenFactorySyncIdempotentFromZero(t *testing.T) {
	factoryPath := filepath.Join(t.TempDir(), "settings.json")
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("models: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	ka, _ := config.LookupKnownAuth("baseten")
	m := newFormModel(t, providerChoice{kind: pkKnown, ka: ka, label: ka.Label()}, map[string]string{
		"upstream_model":    "org/model-a",
		"alias":             "baseten-sync-test",
		"display_name":      "Baseten Sync Test",
		"max_output_tokens": "128000",
	})
	built, err := m.buildModelFromForm()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	be := &backend{
		configPath:  configPath,
		factoryPath: factoryPath,
		baseURL:     "http://127.0.0.1:9787",
		factoryKey:  "x",
	}

	// Sync three times.
	for i := 0; i < 3; i++ {
		if err := be.syncFactory([]*config.Model{built}); err != nil {
			t.Fatalf("sync %d: %v", i, err)
		}
	}

	// Verify exactly one entry.
	s, err := factory.Load(factoryPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	names, err := s.Models()
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	if len(names) != 1 {
		t.Fatalf("expected 1 Factory entry after repeated sync, got %d: %v", len(names), names)
	}
	if names[0] != "baseten-sync-test" {
		t.Errorf("entry model = %q, want baseten-sync-test", names[0])
	}
}

// TestBasetenFactorySyncIdempotentFromOne verifies that repeated Factory sync
// of a model that already has one matching alias produces exactly one entry.
func TestBasetenFactorySyncIdempotentFromOne(t *testing.T) {
	factoryPath := filepath.Join(t.TempDir(), "settings.json")
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("models: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Start with one existing entry (matching alias).
	initial := `{"customModels":[{"model":"baseten-sync-test","displayName":"Old","provider":"generic-chat-completion-api","baseUrl":"http://127.0.0.1:8787","apiKey":"x","maxOutputTokens":4096}]}`
	if err := os.WriteFile(factoryPath, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}

	ka, _ := config.LookupKnownAuth("baseten")
	m := newFormModel(t, providerChoice{kind: pkKnown, ka: ka, label: ka.Label()}, map[string]string{
		"upstream_model":    "org/model-a",
		"alias":             "baseten-sync-test",
		"display_name":      "Baseten Sync Test Updated",
		"max_output_tokens": "128000",
	})
	built, err := m.buildModelFromForm()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	be := &backend{
		configPath:  configPath,
		factoryPath: factoryPath,
		baseURL:     "http://127.0.0.1:9787",
		factoryKey:  "x",
	}

	// Sync twice (should upsert, not duplicate).
	for i := 0; i < 2; i++ {
		if err := be.syncFactory([]*config.Model{built}); err != nil {
			t.Fatalf("sync %d: %v", i, err)
		}
	}

	s, err := factory.Load(factoryPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	names, _ := s.Models()
	if len(names) != 1 {
		t.Fatalf("expected 1 Factory entry after repeated sync from 1, got %d: %v", len(names), names)
	}
	if names[0] != "baseten-sync-test" {
		t.Errorf("model = %q", names[0])
	}
}

// TestBasetenFactorySyncByteExactBackup verifies that Factory sync creates a
// byte-identical immediate pre-sync backup when replacing an existing file.
func TestBasetenFactorySyncByteExactBackup(t *testing.T) {
	factoryPath := filepath.Join(t.TempDir(), "settings.json")
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("models: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Pre-existing Factory file.
	preExisting := `{"theme":"dark","customModels":[{"model":"old-entry","provider":"openai","baseUrl":"http://localhost:11434","apiKey":"x","maxOutputTokens":4096}]}`
	if err := os.WriteFile(factoryPath, []byte(preExisting), 0o600); err != nil {
		t.Fatal(err)
	}
	preHash := sha256sum(t, factoryPath)

	ka, _ := config.LookupKnownAuth("baseten")
	m := newFormModel(t, providerChoice{kind: pkKnown, ka: ka, label: ka.Label()}, map[string]string{
		"upstream_model": "org/model-a",
		"alias":          "baseten-backup-test",
	})
	built, err := m.buildModelFromForm()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	be := &backend{
		configPath:  configPath,
		factoryPath: factoryPath,
		baseURL:     "http://127.0.0.1:9787",
		factoryKey:  "x",
	}

	if err := be.syncFactory([]*config.Model{built}); err != nil {
		t.Fatalf("syncFactory: %v", err)
	}

	// The backup must exist and be byte-identical to the pre-sync file.
	bakPath := factoryPath + ".bak"
	bakData, err := os.ReadFile(bakPath)
	if err != nil {
		t.Fatalf("expected backup at %s: %v", bakPath, err)
	}
	bakHash := sha256hex(bakData)
	if bakHash != preHash {
		t.Errorf("backup is not byte-identical to pre-sync file: backup=%s original=%s", bakHash, preHash)
	}
	// Verify the backup content matches exactly.
	if string(bakData) != preExisting {
		t.Errorf("backup content differs from pre-sync content")
	}
}

// TestBasetenFactorySyncRestrictiveModes verifies that Factory settings and
// backup files are kept at 0600 after sync.
func TestBasetenFactorySyncRestrictiveModes(t *testing.T) {
	factoryPath := filepath.Join(t.TempDir(), "settings.json")
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("models: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Pre-existing Factory file at 0600.
	if err := os.WriteFile(factoryPath, []byte(`{"customModels":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}

	ka, _ := config.LookupKnownAuth("baseten")
	m := newFormModel(t, providerChoice{kind: pkKnown, ka: ka, label: ka.Label()}, map[string]string{
		"upstream_model": "org/model-a",
		"alias":          "baseten-modes-test",
	})
	built, _ := m.buildModelFromForm()
	be := &backend{
		configPath:  configPath,
		factoryPath: factoryPath,
		baseURL:     "http://127.0.0.1:9787",
		factoryKey:  "x",
	}

	if err := be.syncFactory([]*config.Model{built}); err != nil {
		t.Fatalf("syncFactory: %v", err)
	}

	// Settings file must be 0600.
	settingsInfo, err := os.Stat(factoryPath)
	if err != nil {
		t.Fatal(err)
	}
	if settingsInfo.Mode().Perm() != 0o600 {
		t.Errorf("settings file mode = %o, want 0600", settingsInfo.Mode().Perm())
	}
	// Backup must be 0600.
	bakInfo, err := os.Stat(factoryPath + ".bak")
	if err != nil {
		t.Fatalf("backup stat: %v", err)
	}
	if bakInfo.Mode().Perm() != 0o600 {
		t.Errorf("backup file mode = %o, want 0600", bakInfo.Mode().Perm())
	}
}

// TestBasetenFactorySyncPreservesUnknownFields verifies that Factory sync
// preserves unknown top-level and per-entry fields.
func TestBasetenFactorySyncPreservesUnknownFields(t *testing.T) {
	factoryPath := filepath.Join(t.TempDir(), "settings.json")
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("models: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	initial := `{
  "theme": "dark",
  "windowSize": {"width": 1200, "height": 800},
  "customModels": [
    {"model": "unrelated-model", "displayName": "Other", "provider": "openai", "baseUrl": "http://x", "apiKey": "x", "maxOutputTokens": 100, "customUserField": "preserved-value"}
  ]
}`
	if err := os.WriteFile(factoryPath, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}

	ka, _ := config.LookupKnownAuth("baseten")
	m := newFormModel(t, providerChoice{kind: pkKnown, ka: ka, label: ka.Label()}, map[string]string{
		"upstream_model": "org/model-a",
		"alias":          "baseten-unknown-test",
	})
	built, _ := m.buildModelFromForm()
	be := &backend{
		configPath:  configPath,
		factoryPath: factoryPath,
		baseURL:     "http://127.0.0.1:9787",
		factoryKey:  "x",
	}

	if err := be.syncFactory([]*config.Model{built}); err != nil {
		t.Fatalf("syncFactory: %v", err)
	}

	raw, err := os.ReadFile(factoryPath)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// Unknown top-level fields preserved.
	if _, ok := root["theme"]; !ok {
		t.Error("unknown top-level key 'theme' not preserved")
	}
	if _, ok := root["windowSize"]; !ok {
		t.Error("unknown top-level key 'windowSize' not preserved")
	}

	var entries []map[string]json.RawMessage
	_ = json.Unmarshal(root["customModels"], &entries)
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries (1 existing + 1 new), got %d", len(entries))
	}
	// Find the pre-existing entry and verify its unknown field.
	for _, e := range entries {
		if jsonString(e["model"]) == "unrelated-model" {
			if jsonString(e["customUserField"]) != "preserved-value" {
				t.Errorf("unknown per-entry field 'customUserField' not preserved: got %q", jsonString(e["customUserField"]))
			}
		}
	}
}

// TestBasetenFactoryProjectionExcludesCredentials verifies that the Factory
// projection for a Baseten model excludes the Baseten API key, upstream URL,
// known_auth metadata, upstream model slug, and provider-only options. The
// entry should contain only managed local proxy fields.
func TestBasetenFactoryProjectionExcludesCredentials(t *testing.T) {
	factoryPath := filepath.Join(t.TempDir(), "settings.json")
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("models: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	ka, _ := config.LookupKnownAuth("baseten")
	m := newFormModel(t, providerChoice{kind: pkKnown, ka: ka, label: ka.Label()}, map[string]string{
		"upstream_model":    "org/secret-baseten-model",
		"alias":             "baseten-projection-test",
		"display_name":      "Projection Test",
		"max_output_tokens": "128000",
	})
	built, err := m.buildModelFromForm()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	be := &backend{
		configPath:  configPath,
		factoryPath: factoryPath,
		baseURL:     "http://127.0.0.1:9787",
		factoryKey:  "x",
	}

	if err := be.syncFactory([]*config.Model{built}); err != nil {
		t.Fatalf("syncFactory: %v", err)
	}

	raw, err := os.ReadFile(factoryPath)
	if err != nil {
		t.Fatal(err)
	}
	var root struct {
		CustomModels []map[string]json.RawMessage `json:"customModels"`
	}
	if err := json.Unmarshal(raw, &root); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(root.CustomModels) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(root.CustomModels))
	}
	entry := root.CustomModels[0]

	// The managed fields that SHOULD be present.
	if jsonString(entry["model"]) != "baseten-projection-test" {
		t.Errorf("model = %q, want baseten-projection-test", jsonString(entry["model"]))
	}
	if jsonString(entry["displayName"]) != "Projection Test" {
		t.Errorf("displayName = %q, want Projection Test", jsonString(entry["displayName"]))
	}
	if jsonString(entry["provider"]) != "generic-chat-completion-api" {
		t.Errorf("provider = %q, want generic-chat-completion-api", jsonString(entry["provider"]))
	}
	if jsonString(entry["baseUrl"]) != "http://127.0.0.1:9787" {
		t.Errorf("baseUrl = %q, want http://127.0.0.1:9787", jsonString(entry["baseUrl"]))
	}
	// maxOutputTokens is a JSON number.
	var maxTokens int
	if err := json.Unmarshal(entry["maxOutputTokens"], &maxTokens); err != nil {
		t.Errorf("maxOutputTokens unmarshal: %v (raw: %s)", err, string(entry["maxOutputTokens"]))
	} else if maxTokens != 128000 {
		t.Errorf("maxOutputTokens = %d, want 128000", maxTokens)
	}

	// Forbidden fields that must NOT appear.
	sentinel := "baseten-secret-key"
	excludedChecks := []struct {
		field string
		desc  string
	}{
		{"known_auth", "known_auth metadata"},
		{"knownAuth", "knownAuth metadata"},
		{"upstream_model", "upstream model slug"},
		{"upstreamModel", "upstream model slug"},
		{"base_url", "upstream Baseten URL"},
		{"extra_args", "provider-only options"},
		{"extraArgs", "provider-only options"},
		{"api_key_env", "Baseten credential env var"},
		{"apiKeyEnv", "Baseten credential env var"},
		{"reasoning", "Baseten defaults"},
		{"capabilities", "Baseten defaults"},
	}
	for _, check := range excludedChecks {
		if _, exists := entry[check.field]; exists {
			t.Errorf("Factory entry must not contain %s (field %q)", check.desc, check.field)
		}
	}

	// Scan the entire file for the upstream URL, the credential env var name,
	// and the upstream model slug.
	fileStr := string(raw)
	forbiddenSubstrings := []string{
		"https://inference.baseten.co",
		"BASETEN_API_KEY",
		"org/secret-baseten-model",
		sentinel,
	}
	for _, s := range forbiddenSubstrings {
		if strings.Contains(fileStr, s) {
			t.Errorf("Factory settings file contains forbidden substring %q", s)
		}
	}

	// The apiKey field should be the proxy placeholder "x", not the Baseten key.
	if jsonString(entry["apiKey"]) != "x" {
		t.Errorf("apiKey = %q, want proxy placeholder x", jsonString(entry["apiKey"]))
	}
}

// --- helpers ---

func sha256sum(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return sha256hex(data)
}

func sha256hex(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// jsonString extracts a string from a json.RawMessage, returning "" on failure
// or when the raw message is empty.
func jsonString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s
}
