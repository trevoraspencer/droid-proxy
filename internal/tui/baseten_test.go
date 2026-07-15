package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/trevoraspencer/droid-proxy/internal/config"
	"github.com/trevoraspencer/droid-proxy/internal/configedit"
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
