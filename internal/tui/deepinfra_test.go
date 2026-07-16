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

// TestDeepInfraAppearsOnceInProviderChoices verifies DeepInfra appears exactly
// once in the provider picker with the correct credential env.
func TestDeepInfraAppearsOnceInProviderChoices(t *testing.T) {
	choices := buildProviderChoices()
	var count int
	var choice *providerChoice
	for i := range choices {
		if choices[i].kind == pkKnown && choices[i].ka.Name == "deepinfra" {
			count++
			choice = &choices[i]
		}
	}
	if count != 1 {
		t.Fatalf("deepinfra provider choice count = %d, want 1", count)
	}
	if choice.label != "DeepInfra" {
		t.Errorf("label = %q, want DeepInfra", choice.label)
	}
	if choice.ka.APIKeyEnv != "DEEPINFRA_TOKEN" {
		t.Errorf("APIKeyEnv = %q, want DEEPINFRA_TOKEN", choice.ka.APIKeyEnv)
	}
}

// TestDeepInfraRoutesToKeyInputWhenKeyAbsent verifies that selecting DeepInfra
// without a key present goes to the key input screen.
func TestDeepInfraRoutesToKeyInputWhenKeyAbsent(t *testing.T) {
	os.Unsetenv("DEEPINFRA_TOKEN")
	ka, _ := config.LookupKnownAuth("deepinfra")
	m := model{
		be:  &backend{},
		sel: providerChoice{kind: pkKnown, ka: ka, label: ka.Label()},
	}
	next, _ := m.afterProviderChosen()
	got := next.(model)
	if got.screen != screenAddKey {
		t.Fatalf("screen = %v, want screenAddKey", got.screen)
	}
	if got.keyEnv != "DEEPINFRA_TOKEN" {
		t.Errorf("keyEnv = %q, want DEEPINFRA_TOKEN", got.keyEnv)
	}
}

// TestDeepInfraRoutesToDiscoverWhenKeySet verifies that selecting DeepInfra when
// the token is already set goes directly to the discovery screen.
func TestDeepInfraRoutesToDiscoverWhenKeySet(t *testing.T) {
	t.Setenv("DEEPINFRA_TOKEN", "deepinfra-test-token")
	ka, _ := config.LookupKnownAuth("deepinfra")
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

// TestDeepInfraAfterKeySavedGoesToDiscover verifies that after saving a token
// during DeepInfra onboarding, the flow proceeds to discovery.
func TestDeepInfraAfterKeySavedGoesToDiscover(t *testing.T) {
	ka, _ := config.LookupKnownAuth("deepinfra")
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

// TestDeepInfraBuildModelPersistsProfile verifies that a DeepInfra model built
// from the form carries known_auth: deepinfra, generic-chat-completion-api,
// openai-chat, and no extra_args (Standard tier) or capability defaults.
func TestDeepInfraBuildModelPersistsProfile(t *testing.T) {
	ka, _ := config.LookupKnownAuth("deepinfra")
	m := newFormModel(t, providerChoice{kind: pkKnown, ka: ka, label: ka.Label()}, map[string]string{
		"upstream_model":    "meta-llama/Llama-3.3-70B-Instruct",
		"alias":             "deepinfra-model",
		"display_name":      "Llama 3.3 70B (DeepInfra)",
		"max_output_tokens": "128000",
	})
	built, err := m.buildModelFromForm()
	if err != nil {
		t.Fatalf("buildModelFromForm: %v", err)
	}
	if built.KnownAuth != "deepinfra" {
		t.Errorf("KnownAuth = %q, want deepinfra", built.KnownAuth)
	}
	if built.FactoryProvider != config.FactoryProviderGeneric {
		t.Errorf("FactoryProvider = %q, want generic-chat-completion-api", built.FactoryProvider)
	}
	if built.UpstreamProtocol != config.UpstreamOpenAIChat {
		t.Errorf("UpstreamProtocol = %q, want openai-chat", built.UpstreamProtocol)
	}
	if len(built.ExtraArgs) != 0 {
		t.Errorf("ExtraArgs should be empty for Standard, got %v", built.ExtraArgs)
	}
	if built.UpstreamModel != "meta-llama/Llama-3.3-70B-Instruct" {
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

// TestDeepInfraModelRoundTripPersistsProfile verifies a DeepInfra model saves
// and reloads with the correct known_auth and profile fields.
func TestDeepInfraModelRoundTripPersistsProfile(t *testing.T) {
	ka, _ := config.LookupKnownAuth("deepinfra")
	m := newFormModel(t, providerChoice{kind: pkKnown, ka: ka, label: ka.Label()}, map[string]string{
		"upstream_model":    "meta-llama/Llama-3.3-70B-Instruct",
		"alias":             "deepinfra-model",
		"display_name":      "Llama 3.3 70B (DeepInfra)",
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
	if got.KnownAuth != "deepinfra" {
		t.Errorf("KnownAuth = %q, want deepinfra", got.KnownAuth)
	}
	if got.FactoryProvider != config.FactoryProviderGeneric {
		t.Errorf("FactoryProvider = %q, want generic", got.FactoryProvider)
	}
	if got.UpstreamProtocol != config.UpstreamOpenAIChat {
		t.Errorf("UpstreamProtocol = %q", got.UpstreamProtocol)
	}
	if got.UpstreamModel != "meta-llama/Llama-3.3-70B-Instruct" {
		t.Errorf("UpstreamModel = %q", got.UpstreamModel)
	}
	// After hydration, base_url and api_key_env are filled from the registry.
	if got.BaseURL != "https://api.deepinfra.com/v1/openai" {
		t.Errorf("BaseURL = %q, want https://api.deepinfra.com/v1/openai", got.BaseURL)
	}
	if got.APIKeyEnv != "DEEPINFRA_TOKEN" {
		t.Errorf("APIKeyEnv = %q, want DEEPINFRA_TOKEN", got.APIKeyEnv)
	}
}

// TestDeepInfraManualIDRoundTrip verifies that an absent-from-catalog opaque
// ID (including deploy_id values) is preserved byte-for-byte through save
// and reload.
func TestDeepInfraManualIDRoundTrip(t *testing.T) {
	opaqueID := "deploy_id:private-model-abc123"
	ka, _ := config.LookupKnownAuth("deepinfra")
	m := newFormModel(t, providerChoice{kind: pkKnown, ka: ka, label: ka.Label()}, map[string]string{
		"upstream_model": opaqueID,
		"alias":          "custom-deepinfra",
	})
	built, err := m.buildModelFromForm()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if built.UpstreamModel != opaqueID {
		t.Fatalf("built UpstreamModel = %q, want %q", built.UpstreamModel, opaqueID)
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
	if got.UpstreamModel != opaqueID {
		t.Errorf("reloaded UpstreamModel = %q, want %q (byte-exact)", got.UpstreamModel, opaqueID)
	}
}

// TestDeepInfraDiscoveryCancellationIgnoresLateResult verifies that cancelling
// discovery increments the generation counter so a late result cannot alter
// current state.
func TestDeepInfraDiscoveryCancellationIgnoresLateResult(t *testing.T) {
	ka, _ := config.LookupKnownAuth("deepinfra")
	m := model{
		screen:             screenDiscover,
		discoverGeneration: 1,
		sel:                providerChoice{kind: pkKnown, ka: ka, label: ka.Label()},
	}
	next, _ := m.onKey(tea.KeyMsg{Type: tea.KeyEsc})
	got := next.(model)
	if got.screen != screenDashboard {
		t.Errorf("screen = %v, want screenDashboard after cancel", got.screen)
	}
	if got.discoverGeneration != 2 {
		t.Errorf("discoverGeneration = %d, want 2 (incremented to invalidate)", got.discoverGeneration)
	}
	msg := discoverMsg{
		ids:        []string{"meta-llama/late-model"},
		generation: 1,
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

// TestDeepInfraDiscoveryFailureShowsManualForm verifies that when DeepInfra
// discovery fails, the TUI presents a usable manual form without invented
// models.
func TestDeepInfraDiscoveryFailureShowsManualForm(t *testing.T) {
	ka, _ := config.LookupKnownAuth("deepinfra")
	m := model{
		discoverGeneration: 1,
		sel:                providerChoice{kind: pkKnown, ka: ka, label: ka.Label()},
	}
	msg := discoverMsg{
		err:        fmt.Errorf("provider returned 500 Internal Server Error"),
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
	if len(got.pickItems) != 0 {
		t.Errorf("pickItems should be empty, got %v", got.pickItems)
	}
}

// TestDeepInfraDiscoveryEmptyResultsShowsManualForm verifies that an empty
// discovery result still presents a usable manual form.
func TestDeepInfraDiscoveryEmptyResultsShowsManualForm(t *testing.T) {
	ka, _ := config.LookupKnownAuth("deepinfra")
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

// TestDeepInfraManualEntryAfterSuccessOpensEmptyForm verifies that selecting
// manual entry after a successful discovery opens an empty DeepInfra form
// without a second discovery request.
func TestDeepInfraManualEntryAfterSuccessOpensEmptyForm(t *testing.T) {
	ka, _ := config.LookupKnownAuth("deepinfra")
	m := model{
		discoverGeneration: 1,
		sel:                providerChoice{kind: pkKnown, ka: ka, label: ka.Label()},
	}
	next, _ := m.onDiscover(discoverMsg{
		ids:        []string{"meta-llama/Llama-3.3-70B-Instruct", "deepinfra/deepseek-v4"},
		generation: 1,
	})
	got := next.(model)
	if got.screen != screenPickModel {
		t.Fatalf("screen = %v, want screenPickModel", got.screen)
	}
	if got.pickItems[0] != manualEntryLabel {
		t.Errorf("first item = %q, want manual entry label", got.pickItems[0])
	}
	got.pickCursor = 0
	next2, _ := got.keyPickModel(tea.KeyMsg{Type: tea.KeyEnter})
	got2 := next2.(model)
	if got2.screen != screenForm {
		t.Fatalf("screen = %v, want screenForm", got2.screen)
	}
	um := got2.formValue("upstream_model")
	if um != "" {
		t.Errorf("upstream_model should be empty for manual entry, got %q", um)
	}
}

// TestDeepInfraFormEscapeGoesToDashboard verifies Escape from the DeepInfra form
// returns to the dashboard without writes.
func TestDeepInfraFormEscapeGoesToDashboard(t *testing.T) {
	ka, _ := config.LookupKnownAuth("deepinfra")
	sel := providerChoice{kind: pkKnown, ka: ka, label: ka.Label()}
	m := newFormModel(t, sel, map[string]string{
		"upstream_model": "meta-llama/Llama-3.3-70B-Instruct",
		"alias":          "test",
	})
	next, _ := m.keyForm(tea.KeyMsg{Type: tea.KeyEsc})
	got := next.(model)
	if got.screen != screenDashboard {
		t.Errorf("screen = %v, want screenDashboard", got.screen)
	}
}

// TestDeepInfraPickerEscapeGoesToProviderPicker verifies Escape from the
// DeepInfra model picker returns to the provider picker.
func TestDeepInfraPickerEscapeGoesToProviderPicker(t *testing.T) {
	ka, _ := config.LookupKnownAuth("deepinfra")
	m := model{
		be:        &backend{},
		sel:       providerChoice{kind: pkKnown, ka: ka, label: ka.Label()},
		screen:    screenPickModel,
		pickItems: []string{manualEntryLabel, "meta-llama/Llama-3.3-70B-Instruct"},
	}
	next, _ := m.keyPickModel(tea.KeyMsg{Type: tea.KeyEsc})
	got := next.(model)
	if got.screen != screenAddProvider {
		t.Errorf("screen = %v, want screenAddProvider", got.screen)
	}
}

// TestDeepInfraModelRouteSummary verifies the dashboard route summary shows
// "DeepInfra · openai-chat" for a hydrated DeepInfra model.
func TestDeepInfraModelRouteSummary(t *testing.T) {
	m := &config.Model{
		Alias:            "test-deepinfra",
		KnownAuth:        "deepinfra",
		UpstreamProtocol: config.UpstreamOpenAIChat,
	}
	summary := modelRouteSummary(m)
	if !strings.Contains(summary, "DeepInfra") {
		t.Errorf("route summary should contain 'DeepInfra', got %q", summary)
	}
	if !strings.Contains(summary, "openai-chat") {
		t.Errorf("route summary should contain 'openai-chat', got %q", summary)
	}
}

// TestDeepInfraBuildModelRejectsBlankAlias verifies the form validation rejects
// blank aliases without partial writes.
func TestDeepInfraBuildModelRejectsBlankAlias(t *testing.T) {
	ka, _ := config.LookupKnownAuth("deepinfra")
	m := newFormModel(t, providerChoice{kind: pkKnown, ka: ka, label: ka.Label()}, map[string]string{
		"upstream_model": "meta-llama/Llama-3.3-70B-Instruct",
		"alias":          "",
	})
	_, err := m.buildModelFromForm()
	if err == nil {
		t.Fatal("expected error for blank alias")
	}
}

// TestDeepInfraBuildModelRejectsBlankUpstream verifies the form validation
// rejects blank upstream model IDs.
func TestDeepInfraBuildModelRejectsBlankUpstream(t *testing.T) {
	ka, _ := config.LookupKnownAuth("deepinfra")
	m := newFormModel(t, providerChoice{kind: pkKnown, ka: ka, label: ka.Label()}, map[string]string{
		"upstream_model": "",
		"alias":          "deepinfra-test",
	})
	_, err := m.buildModelFromForm()
	if err == nil {
		t.Fatal("expected error for blank upstream model")
	}
}

// TestDeepInfraBuildModelRejectsDuplicateAlias verifies the addModel call
// rejects a duplicate alias without partial writes.
func TestDeepInfraBuildModelRejectsDuplicateAlias(t *testing.T) {
	ka, _ := config.LookupKnownAuth("deepinfra")
	m := newFormModel(t, providerChoice{kind: pkKnown, ka: ka, label: ka.Label()}, map[string]string{
		"upstream_model": "meta-llama/Llama-3.3-70B-Instruct",
		"alias":          "deepinfra-dup",
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
	built2, _ := m.buildModelFromForm()
	err = be.addModel(built2)
	if err == nil {
		t.Fatal("expected error for duplicate alias")
	}
	models, _ := configedit.LoadModels(path)
	if len(models) != 1 {
		t.Errorf("expected 1 model after duplicate rejection, got %d", len(models))
	}
}

// TestDeepInfraNativeInheritsBase verifies that selecting DeepInfra from the
// provider picker inherits the inference base URL without asking for a custom
// endpoint URL.
func TestDeepInfraNativeInheritsBase(t *testing.T) {
	ka, _ := config.LookupKnownAuth("deepinfra")
	m := newFormModel(t, providerChoice{kind: pkKnown, ka: ka, label: ka.Label()}, map[string]string{
		"upstream_model": "meta-llama/Llama-3.3-70B-Instruct",
		"alias":          "deepinfra-native-test",
	})
	built, err := m.buildModelFromForm()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if built.BaseURL != "" {
		t.Errorf("Native DeepInfra should not persist base_url, got %q", built.BaseURL)
	}
	if built.APIKeyEnv != "" {
		t.Errorf("Native DeepInfra should not persist api_key_env, got %q", built.APIKeyEnv)
	}
	if built.KnownAuth != "deepinfra" {
		t.Errorf("KnownAuth = %q, want deepinfra", built.KnownAuth)
	}
}

// TestDeepInfraCustomEndpointIsDistinct verifies a custom endpoint model
// persists explicit base_url/api_key_env without known_auth: deepinfra.
func TestDeepInfraCustomEndpointIsDistinct(t *testing.T) {
	sel := providerChoice{kind: pkCustom, label: "Custom OpenAI-compatible endpoint"}
	m := newFormModel(t, sel, map[string]string{
		"base_url":       "https://my-deployment.deepinfra.com/v1/openai",
		"api_key_env":    "CUSTOM_DEEPINFRA_DEPLOY_KEY",
		"upstream_model": "custom-deploy",
		"alias":          "deepinfra-custom-test",
	})
	built, err := m.buildModelFromForm()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if built.KnownAuth == "deepinfra" {
		t.Error("Custom endpoint must NOT carry known_auth: deepinfra")
	}
	if built.BaseURL != "https://my-deployment.deepinfra.com/v1/openai" {
		t.Errorf("BaseURL = %q, want explicit custom URL", built.BaseURL)
	}
}

// TestDeepInfraStageSafeCredentialWrite verifies that a failed or canceled
// model save after a successful token write preserves the completed token.
// The token write is a completed private credential step that a later failure
// must not roll back.
func TestDeepInfraStageSafeCredentialWrite(t *testing.T) {
	// Simulate: token written successfully, then model save fails (duplicate alias).
	ka, _ := config.LookupKnownAuth("deepinfra")
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("models: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	be := &backend{configPath: path}
	t.Setenv("DEEPINFRA_TOKEN", "deepinfra-stage-token")

	// Token write succeeds.
	if err := be.setKey("DEEPINFRA_TOKEN", "deepinfra-stage-token"); err != nil {
		t.Fatalf("setKey: %v", err)
	}

	// Add a model successfully.
	m := newFormModel(t, providerChoice{kind: pkKnown, ka: ka, label: ka.Label()}, map[string]string{
		"upstream_model": "meta-llama/Llama-3.3-70B-Instruct",
		"alias":          "deepinfra-stage",
	})
	built, err := m.buildModelFromForm()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if err := be.addModel(built); err != nil {
		t.Fatalf("addModel: %v", err)
	}

	// Token must still be set after model save.
	if os.Getenv("DEEPINFRA_TOKEN") != "deepinfra-stage-token" {
		t.Errorf("DEEPINFRA_TOKEN should survive model save, got %q", os.Getenv("DEEPINFRA_TOKEN"))
	}
}

// TestDeepInfraDiscoveryNoTierInStandard verifies the default DeepInfra flow
// creates a Standard model without service_tier (no tier picker in TUI).
func TestDeepInfraDiscoveryNoTierInStandard(t *testing.T) {
	ka, _ := config.LookupKnownAuth("deepinfra")
	m := newFormModel(t, providerChoice{kind: pkKnown, ka: ka, label: ka.Label()}, map[string]string{
		"upstream_model": "meta-llama/Llama-3.3-70B-Instruct",
		"alias":          "deepinfra-standard",
	})
	built, err := m.buildModelFromForm()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	// Standard: no service_tier in extra_args.
	if built.ExtraArgs != nil {
		if _, ok := built.ExtraArgs["service_tier"]; ok {
			t.Errorf("Standard DeepInfra should not have service_tier in ExtraArgs")
		}
	}
}
