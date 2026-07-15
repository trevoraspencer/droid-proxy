package tui

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/trevoraspencer/droid-proxy/internal/config"
	"github.com/trevoraspencer/droid-proxy/internal/configedit"
)

func TestFireworksStandardRoutesToServingPathWhenKeySet(t *testing.T) {
	t.Setenv("FIREWORKS_API_KEY", "fw_testkey")
	ka, _ := config.LookupKnownAuth("fireworks")
	m := model{be: &backend{}, sel: providerChoice{kind: pkKnown, ka: ka, label: ka.Label()}}
	next, cmd := m.afterProviderChosen()
	got := next.(model)
	if got.screen != screenServingPath {
		t.Fatalf("screen = %v, want screenServingPath", got.screen)
	}
	if cmd != nil {
		t.Errorf("expected nil cmd for serving path screen, got non-nil")
	}
}

func TestFireworksStandardRoutesToKeyInputWhenKeyAbsent(t *testing.T) {
	ka, _ := config.LookupKnownAuth("fireworks")
	m := model{be: &backend{}, sel: providerChoice{kind: pkKnown, ka: ka, label: ka.Label()}}
	next, _ := m.afterProviderChosen()
	got := next.(model)
	if got.screen != screenAddKey {
		t.Fatalf("screen = %v, want screenAddKey", got.screen)
	}
	if got.keyEnv != "FIREWORKS_API_KEY" {
		t.Errorf("keyEnv = %q, want FIREWORKS_API_KEY", got.keyEnv)
	}
}

func TestFirePassRoutesToStaticCatalogWhenKeySet(t *testing.T) {
	t.Setenv("FIREWORKS_FIRE_PASS_API_KEY", "fpk_testkey")
	ka, _ := config.LookupKnownAuth("fireworks-fire-pass")
	m := model{be: &backend{}, sel: providerChoice{kind: pkKnown, ka: ka, label: ka.Label()}}
	next, _ := m.afterProviderChosen()
	got := next.(model)
	if got.screen != screenPickModel {
		t.Fatalf("screen = %v, want screenPickModel (static catalog)", got.screen)
	}
}

func TestFirePassRoutesToKeyInputWhenKeyAbsent(t *testing.T) {
	ka, _ := config.LookupKnownAuth("fireworks-fire-pass")
	m := model{be: &backend{}, sel: providerChoice{kind: pkKnown, ka: ka, label: ka.Label()}}
	next, _ := m.afterProviderChosen()
	got := next.(model)
	if got.screen != screenAddKey {
		t.Fatalf("screen = %v, want screenAddKey", got.screen)
	}
	if got.keyEnv != "FIREWORKS_FIRE_PASS_API_KEY" {
		t.Errorf("keyEnv = %q, want FIREWORKS_FIRE_PASS_API_KEY", got.keyEnv)
	}
}

func TestFireworksAndFirePassAreDistinctProviderChoices(t *testing.T) {
	choices := buildProviderChoices()
	var fwCount, fpCount int
	var fwChoice, fpChoice *providerChoice
	for i := range choices {
		if choices[i].kind == pkKnown {
			switch choices[i].ka.Name {
			case "fireworks":
				fwCount++
				fwChoice = &choices[i]
			case "fireworks-fire-pass":
				fpCount++
				fpChoice = &choices[i]
			}
		}
	}
	if fwCount != 1 {
		t.Errorf("fireworks choice count = %d, want 1", fwCount)
	}
	if fpCount != 1 {
		t.Errorf("fireworks-fire-pass choice count = %d, want 1", fpCount)
	}
	if fwChoice != nil && fwChoice.label != "Fireworks AI" {
		t.Errorf("fireworks label = %q", fwChoice.label)
	}
	if fpChoice != nil && fpChoice.label != "Fireworks AI (Fire Pass)" {
		t.Errorf("fire pass label = %q", fpChoice.label)
	}
}

func TestFirePassStaticCatalogItems(t *testing.T) {
	ka, _ := config.LookupKnownAuth("fireworks-fire-pass")
	m := model{be: &backend{}, sel: providerChoice{kind: pkKnown, ka: ka, label: ka.Label()}}
	next, _ := m.showStaticCatalog()
	got := next.(model)
	if len(got.pickItems) < 2 {
		t.Fatalf("pickItems = %v, expected at least manual + one router", got.pickItems)
	}
	if got.pickItems[0] != manualEntryLabel {
		t.Errorf("first item = %q, want manual entry label", got.pickItems[0])
	}
	found := false
	for _, item := range got.pickItems[1:] {
		if item == "accounts/fireworks/routers/glm-5p2-fast" {
			found = true
		}
	}
	if !found {
		t.Error("canonical router accounts/fireworks/routers/glm-5p2-fast not in Fire Pass catalog")
	}
}

func TestFastCatalogItems(t *testing.T) {
	m := model{}
	next, _ := m.showFastCatalog()
	got := next.(model)
	if len(got.pickItems) < 2 {
		t.Fatalf("pickItems = %v, expected at least manual + one router", got.pickItems)
	}
	if got.pickItems[0] != manualEntryLabel {
		t.Errorf("first item = %q, want manual entry label", got.pickItems[0])
	}
	found := false
	for _, item := range got.pickItems[1:] {
		if item == "accounts/fireworks/routers/glm-5p2-fast" {
			found = true
		}
	}
	if !found {
		t.Error("canonical router accounts/fireworks/routers/glm-5p2-fast not in Fast catalog")
	}
}

func TestServingPathChoices(t *testing.T) {
	paths := fireworksServingPaths()
	if len(paths) != 3 {
		t.Fatalf("expected 3 serving paths, got %d", len(paths))
	}
	want := []string{"standard", "priority", "fast"}
	for i, p := range paths {
		if p.id != want[i] {
			t.Errorf("path[%d].id = %q, want %q", i, p.id, want[i])
		}
	}
}

func TestServingPathSelectStandard(t *testing.T) {
	t.Setenv("FIREWORKS_API_KEY", "fw_testkey")
	ka, _ := config.LookupKnownAuth("fireworks")
	m := model{
		be:         &backend{},
		sel:        providerChoice{kind: pkKnown, ka: ka, label: ka.Label()},
		screen:     screenServingPath,
		provCursor: 0, // Standard
	}
	next, _ := m.keyServingPath(tea.KeyMsg{Type: tea.KeyEnter})
	got := next.(model)
	if got.servingPath != "standard" {
		t.Errorf("servingPath = %q, want standard", got.servingPath)
	}
	if got.screen != screenDiscover {
		t.Errorf("screen = %v, want screenDiscover", got.screen)
	}
}

func TestServingPathSelectPriority(t *testing.T) {
	t.Setenv("FIREWORKS_API_KEY", "fw_testkey")
	ka, _ := config.LookupKnownAuth("fireworks")
	m := model{
		be:         &backend{},
		sel:        providerChoice{kind: pkKnown, ka: ka, label: ka.Label()},
		screen:     screenServingPath,
		provCursor: 1, // Priority
	}
	next, _ := m.keyServingPath(tea.KeyMsg{Type: tea.KeyEnter})
	got := next.(model)
	if got.servingPath != "priority" {
		t.Errorf("servingPath = %q, want priority", got.servingPath)
	}
	if got.screen != screenDiscover {
		t.Errorf("screen = %v, want screenDiscover", got.screen)
	}
}

func TestServingPathSelectFast(t *testing.T) {
	ka, _ := config.LookupKnownAuth("fireworks")
	m := model{
		be:         &backend{},
		sel:        providerChoice{kind: pkKnown, ka: ka, label: ka.Label()},
		screen:     screenServingPath,
		provCursor: 2, // Fast
	}
	next, _ := m.keyServingPath(tea.KeyMsg{Type: tea.KeyEnter})
	got := next.(model)
	if got.servingPath != "fast" {
		t.Errorf("servingPath = %q, want fast", got.servingPath)
	}
	if got.screen != screenPickModel {
		t.Errorf("screen = %v, want screenPickModel (static Fast catalog)", got.screen)
	}
}

func TestServingPathEscapeGoesToProviderPicker(t *testing.T) {
	ka, _ := config.LookupKnownAuth("fireworks")
	m := model{
		be:     &backend{},
		sel:    providerChoice{kind: pkKnown, ka: ka, label: ka.Label()},
		screen: screenServingPath,
	}
	next, _ := m.keyServingPath(tea.KeyMsg{Type: tea.KeyEsc})
	got := next.(model)
	if got.screen != screenAddProvider {
		t.Errorf("screen = %v, want screenAddProvider", got.screen)
	}
	if got.servingPath != "" {
		t.Errorf("servingPath = %q, want empty after escape", got.servingPath)
	}
}

func TestPickerEscapeGoesToServingPathInFireworksFlow(t *testing.T) {
	ka, _ := config.LookupKnownAuth("fireworks")
	m := model{
		be:          &backend{},
		sel:         providerChoice{kind: pkKnown, ka: ka, label: ka.Label()},
		screen:      screenPickModel,
		servingPath: "fast",
		pickItems:   []string{manualEntryLabel, "accounts/fireworks/routers/glm-5p2-fast"},
	}
	next, _ := m.keyPickModel(tea.KeyMsg{Type: tea.KeyEsc})
	got := next.(model)
	if got.screen != screenServingPath {
		t.Errorf("screen = %v, want screenServingPath", got.screen)
	}
}

func TestPickerEscapeGoesToProviderPickerForFirePass(t *testing.T) {
	ka, _ := config.LookupKnownAuth("fireworks-fire-pass")
	m := model{
		be:     &backend{},
		sel:    providerChoice{kind: pkKnown, ka: ka, label: ka.Label()},
		screen: screenPickModel,
		pickItems: []string{
			manualEntryLabel,
			"accounts/fireworks/routers/glm-5p2-fast",
		},
	}
	next, _ := m.keyPickModel(tea.KeyMsg{Type: tea.KeyEsc})
	got := next.(model)
	if got.screen != screenAddProvider {
		t.Errorf("screen = %v, want screenAddProvider", got.screen)
	}
}

func TestFormEscapeGoesToServingPathInFireworksFlow(t *testing.T) {
	ka, _ := config.LookupKnownAuth("fireworks")
	sel := providerChoice{kind: pkKnown, ka: ka, label: ka.Label()}
	m := newFormModel(t, sel, map[string]string{
		"upstream_model": "accounts/fireworks/models/test",
		"alias":          "test-fw",
	})
	m.servingPath = "standard"
	next, _ := m.keyForm(tea.KeyMsg{Type: tea.KeyEsc})
	got := next.(model)
	if got.screen != screenServingPath {
		t.Errorf("screen = %v, want screenServingPath", got.screen)
	}
}

func TestBuildModelPriorityAddsServiceTier(t *testing.T) {
	ka, _ := config.LookupKnownAuth("fireworks")
	m := newFormModel(t, providerChoice{kind: pkKnown, ka: ka, label: ka.Label()}, map[string]string{
		"upstream_model": "accounts/fireworks/models/deepseek-v4-pro",
		"alias":          "fw-priority-test",
	})
	m.servingPath = "priority"
	built, err := m.buildModelFromForm()
	if err != nil {
		t.Fatalf("buildModelFromForm: %v", err)
	}
	if built.KnownAuth != "fireworks" {
		t.Errorf("KnownAuth = %q", built.KnownAuth)
	}
	if built.FactoryProvider != config.FactoryProviderGeneric {
		t.Errorf("FactoryProvider = %q, want generic-chat-completion-api", built.FactoryProvider)
	}
	if built.ExtraArgs["service_tier"] != "priority" {
		t.Errorf("ExtraArgs.service_tier = %#v, want priority", built.ExtraArgs["service_tier"])
	}
}

func TestBuildModelStandardNoServiceTier(t *testing.T) {
	ka, _ := config.LookupKnownAuth("fireworks")
	m := newFormModel(t, providerChoice{kind: pkKnown, ka: ka, label: ka.Label()}, map[string]string{
		"upstream_model": "accounts/fireworks/models/deepseek-v4-pro",
		"alias":          "fw-standard-test",
	})
	m.servingPath = "standard"
	built, err := m.buildModelFromForm()
	if err != nil {
		t.Fatalf("buildModelFromForm: %v", err)
	}
	if _, exists := built.ExtraArgs["service_tier"]; exists {
		t.Errorf("Standard should not have service_tier, got %v", built.ExtraArgs["service_tier"])
	}
}

func TestBuildModelFastNoServiceTier(t *testing.T) {
	ka, _ := config.LookupKnownAuth("fireworks")
	m := newFormModel(t, providerChoice{kind: pkKnown, ka: ka, label: ka.Label()}, map[string]string{
		"upstream_model": "accounts/fireworks/routers/glm-5p2-fast",
		"alias":          "fw-fast-test",
	})
	m.servingPath = "fast"
	built, err := m.buildModelFromForm()
	if err != nil {
		t.Fatalf("buildModelFromForm: %v", err)
	}
	if _, exists := built.ExtraArgs["service_tier"]; exists {
		t.Errorf("Fast baseline should not have service_tier, got %v", built.ExtraArgs["service_tier"])
	}
	if built.UpstreamModel != "accounts/fireworks/routers/glm-5p2-fast" {
		t.Errorf("UpstreamModel = %q", built.UpstreamModel)
	}
}

func TestBuildModelFirePassProfile(t *testing.T) {
	ka, _ := config.LookupKnownAuth("fireworks-fire-pass")
	m := newFormModel(t, providerChoice{kind: pkKnown, ka: ka, label: ka.Label()}, map[string]string{
		"upstream_model": "accounts/fireworks/routers/glm-5p2-fast",
		"alias":          "fp-test",
	})
	built, err := m.buildModelFromForm()
	if err != nil {
		t.Fatalf("buildModelFromForm: %v", err)
	}
	if built.KnownAuth != "fireworks-fire-pass" {
		t.Errorf("KnownAuth = %q, want fireworks-fire-pass", built.KnownAuth)
	}
	if _, exists := built.ExtraArgs["service_tier"]; exists {
		t.Errorf("Fire Pass should not have service_tier by default")
	}
}

func TestStaleDiscoveryResultIsIgnored(t *testing.T) {
	m := model{
		screen:             screenDashboard,
		discoverGeneration: 2,
	}
	msg := discoverMsg{
		ids:        []string{"some-model"},
		generation: 1, // stale
	}
	next, cmd := m.onDiscover(msg)
	got := next.(model)
	if got.screen != screenDashboard {
		t.Errorf("screen = %v, want screenDashboard (stale ignored)", got.screen)
	}
	if cmd != nil {
		t.Errorf("expected nil cmd for stale discovery")
	}
}

func TestCurrentDiscoveryResultIsApplied(t *testing.T) {
	m := model{
		discoverGeneration: 1,
	}
	msg := discoverMsg{
		ids:        []string{"model-a", "model-b"},
		generation: 1, // current
	}
	next, cmd := m.onDiscover(msg)
	got := next.(model)
	if got.screen != screenPickModel {
		t.Errorf("screen = %v, want screenPickModel", got.screen)
	}
	wantItems := []string{manualEntryLabel, "model-a", "model-b"}
	if !reflect.DeepEqual(got.pickItems, wantItems) {
		t.Errorf("pickItems = %v, want %v", got.pickItems, wantItems)
	}
	if cmd != nil {
		t.Errorf("expected nil cmd for picker display")
	}
}

func TestFailedDiscoveryShowsManualForm(t *testing.T) {
	m := model{
		discoverGeneration: 1,
	}
	msg := discoverMsg{
		err:        nil,
		ids:        nil,
		generation: 1,
	}
	next, cmd := m.onDiscover(msg)
	got := next.(model)
	if got.screen != screenForm {
		t.Errorf("screen = %v, want screenForm", got.screen)
	}
	if cmd == nil {
		t.Errorf("expected non-nil cmd for form blink")
	}
}

func TestFirePassCatalogPickItemsOrder(t *testing.T) {
	items := catalogPickItems(config.FireworksFastCatalog())
	if items[0] != manualEntryLabel {
		t.Errorf("first item = %q, want manual entry", items[0])
	}
	if len(items) < 2 {
		t.Fatalf("expected at least 2 items (manual + catalog), got %d", len(items))
	}
}

func TestFireworksFirePassIndependentCredentials(t *testing.T) {
	fw, _ := config.LookupKnownAuth("fireworks")
	fp, _ := config.LookupKnownAuth("fireworks-fire-pass")
	if fw.APIKeyEnv == fp.APIKeyEnv {
		t.Errorf("Standard and Fire Pass share credential env: %q", fw.APIKeyEnv)
	}
	if fw.APIKeyEnv != "FIREWORKS_API_KEY" {
		t.Errorf("Standard env = %q", fw.APIKeyEnv)
	}
	if fp.APIKeyEnv != "FIREWORKS_FIRE_PASS_API_KEY" {
		t.Errorf("Fire Pass env = %q", fp.APIKeyEnv)
	}
}

func TestAfterKeySavedFireworksGoesToServingPath(t *testing.T) {
	ka, _ := config.LookupKnownAuth("fireworks")
	m := model{
		be:  &backend{},
		sel: providerChoice{kind: pkKnown, ka: ka, label: ka.Label()},
	}
	next, _ := m.afterKeySaved()
	got := next.(model)
	if got.screen != screenServingPath {
		t.Errorf("screen = %v, want screenServingPath", got.screen)
	}
}

func TestAfterKeySavedFirePassGoesToStaticCatalog(t *testing.T) {
	ka, _ := config.LookupKnownAuth("fireworks-fire-pass")
	m := model{
		be:  &backend{},
		sel: providerChoice{kind: pkKnown, ka: ka, label: ka.Label()},
	}
	next, _ := m.afterKeySaved()
	got := next.(model)
	if got.screen != screenPickModel {
		t.Errorf("screen = %v, want screenPickModel (static catalog)", got.screen)
	}
}

func TestFireworksStandardModelRoundTripPersistsProfile(t *testing.T) {
	ka, _ := config.LookupKnownAuth("fireworks")
	m := newFormModel(t, providerChoice{kind: pkKnown, ka: ka, label: ka.Label()}, map[string]string{
		"upstream_model":    "accounts/fireworks/models/deepseek-v4-pro",
		"alias":             "fw-standard",
		"display_name":      "DeepSeek V4 Pro (Fireworks)",
		"max_output_tokens": "128000",
	})
	m.servingPath = "standard"
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
	if got.KnownAuth != "fireworks" {
		t.Errorf("KnownAuth = %q, want fireworks", got.KnownAuth)
	}
	if got.FactoryProvider != config.FactoryProviderGeneric {
		t.Errorf("FactoryProvider = %q, want generic", got.FactoryProvider)
	}
	if got.UpstreamProtocol != config.UpstreamOpenAIChat {
		t.Errorf("UpstreamProtocol = %q", got.UpstreamProtocol)
	}
	if _, exists := got.ExtraArgs["service_tier"]; exists {
		t.Errorf("Standard should not have service_tier")
	}
}

func TestFireworksPriorityModelRoundTripPreservesTier(t *testing.T) {
	ka, _ := config.LookupKnownAuth("fireworks")
	m := newFormModel(t, providerChoice{kind: pkKnown, ka: ka, label: ka.Label()}, map[string]string{
		"upstream_model": "accounts/fireworks/models/deepseek-v4-pro",
		"alias":          "fw-priority",
	})
	m.servingPath = "priority"
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
	got := models[0]
	if got.ExtraArgs["service_tier"] != "priority" {
		t.Errorf("service_tier = %#v, want priority", got.ExtraArgs["service_tier"])
	}
}

func TestFirePassModelRoundTripPersistsProfile(t *testing.T) {
	ka, _ := config.LookupKnownAuth("fireworks-fire-pass")
	m := newFormModel(t, providerChoice{kind: pkKnown, ka: ka, label: ka.Label()}, map[string]string{
		"upstream_model": "accounts/fireworks/routers/glm-5p2-fast",
		"alias":          "fp-glm",
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
	got := models[0]
	if got.KnownAuth != "fireworks-fire-pass" {
		t.Errorf("KnownAuth = %q, want fireworks-fire-pass", got.KnownAuth)
	}
	if got.APIKeyEnv != "FIREWORKS_FIRE_PASS_API_KEY" {
		t.Errorf("APIKeyEnv = %q, want FIREWORKS_FIRE_PASS_API_KEY", got.APIKeyEnv)
	}
	if got.BaseURL != "https://api.fireworks.ai/inference/v1" {
		t.Errorf("BaseURL = %q", got.BaseURL)
	}
}
