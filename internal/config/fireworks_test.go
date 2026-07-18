package config

import (
	"strings"
	"testing"
)

func TestFireworksStandardProfileExact(t *testing.T) {
	ka, ok := LookupKnownAuth("fireworks")
	if !ok {
		t.Fatal("fireworks profile missing from registry")
	}
	if ka.Name != "fireworks" {
		t.Errorf("Name = %q, want fireworks", ka.Name)
	}
	if ka.BaseURL != "https://api.fireworks.ai/inference/v1" {
		t.Errorf("BaseURL = %q", ka.BaseURL)
	}
	if ka.APIKeyEnv != "FIREWORKS_API_KEY" {
		t.Errorf("APIKeyEnv = %q, want FIREWORKS_API_KEY", ka.APIKeyEnv)
	}
	if ka.UpstreamProtocol != UpstreamOpenAIChat {
		t.Errorf("UpstreamProtocol = %q, want openai-chat", ka.UpstreamProtocol)
	}
	if ka.AuthHeader != "" {
		t.Errorf("AuthHeader = %q, want empty (defaults to Authorization)", ka.AuthHeader)
	}
	if ka.NoAuth {
		t.Error("NoAuth should be false")
	}
	if ka.Label() != "Fireworks AI" {
		t.Errorf("Label = %q, want Fireworks AI", ka.Label())
	}
}

func TestFireworksFirePassProfileExact(t *testing.T) {
	ka, ok := LookupKnownAuth("fireworks-fire-pass")
	if !ok {
		t.Fatal("fireworks-fire-pass profile missing from registry")
	}
	if ka.Name != "fireworks-fire-pass" {
		t.Errorf("Name = %q, want fireworks-fire-pass", ka.Name)
	}
	if ka.BaseURL != "https://api.fireworks.ai/inference/v1" {
		t.Errorf("BaseURL = %q, want same inference base as Standard", ka.BaseURL)
	}
	if ka.APIKeyEnv != "FIREWORKS_FIRE_PASS_API_KEY" {
		t.Errorf("APIKeyEnv = %q, want FIREWORKS_FIRE_PASS_API_KEY", ka.APIKeyEnv)
	}
	if ka.UpstreamProtocol != UpstreamOpenAIChat {
		t.Errorf("UpstreamProtocol = %q, want openai-chat", ka.UpstreamProtocol)
	}
	if ka.AuthHeader != "" {
		t.Errorf("AuthHeader = %q, want empty (defaults to Authorization)", ka.AuthHeader)
	}
	if ka.NoAuth {
		t.Error("NoAuth should be false")
	}
	if ka.Label() != "Fireworks AI (Fire Pass)" {
		t.Errorf("Label = %q, want 'Fireworks AI (Fire Pass)'", ka.Label())
	}
	// Fire Pass uses static discovery — no remote request.
	if ka.DiscoveryPolicy != DiscoveryStatic {
		t.Errorf("DiscoveryPolicy = %q, want static", ka.DiscoveryPolicy)
	}
}

func TestFireworksProfilesCaseInsensitive(t *testing.T) {
	for _, name := range []string{"fireworks", "Fireworks", "FIREWORKS"} {
		ka, ok := LookupKnownAuth(name)
		if !ok {
			t.Errorf("LookupKnownAuth(%q) not found", name)
			continue
		}
		if ka.Name != "fireworks" {
			t.Errorf("LookupKnownAuth(%q).Name = %q", name, ka.Name)
		}
	}
	for _, name := range []string{"fireworks-fire-pass", "Fireworks-Fire-Pass", "FIREWORKS-FIRE-PASS"} {
		ka, ok := LookupKnownAuth(name)
		if !ok {
			t.Errorf("LookupKnownAuth(%q) not found", name)
			continue
		}
		if ka.Name != "fireworks-fire-pass" {
			t.Errorf("LookupKnownAuth(%q).Name = %q", name, ka.Name)
		}
	}
}

func TestFireworksProfileCountExactlyOne(t *testing.T) {
	all := KnownAuthList()
	var stdCount, fpCount int
	for _, ka := range all {
		switch ka.Name {
		case "fireworks":
			stdCount++
		case "fireworks-fire-pass":
			fpCount++
		}
	}
	if stdCount != 1 {
		t.Errorf("fireworks profile count = %d, want 1", stdCount)
	}
	if fpCount != 1 {
		t.Errorf("fireworks-fire-pass profile count = %d, want 1", fpCount)
	}
}

func TestFireworksHydrationOmittedFields(t *testing.T) {
	m := &Model{
		Alias:     "test-fw",
		KnownAuth: "fireworks",
	}
	if err := HydrateModel(m); err != nil {
		t.Fatal(err)
	}
	if m.BaseURL != "https://api.fireworks.ai/inference/v1" {
		t.Errorf("hydrated BaseURL = %q", m.BaseURL)
	}
	if m.APIKeyEnv != "FIREWORKS_API_KEY" {
		t.Errorf("hydrated APIKeyEnv = %q", m.APIKeyEnv)
	}
	if m.UpstreamProtocol != UpstreamOpenAIChat {
		t.Errorf("hydrated UpstreamProtocol = %q", m.UpstreamProtocol)
	}
	if m.FactoryProvider != "" {
		t.Errorf("FactoryProvider should remain empty (set by TUI), got %q", m.FactoryProvider)
	}
	// No defaults injected by registry.
	if len(m.ExtraArgs) != 0 {
		t.Errorf("ExtraArgs should be empty, got %v", m.ExtraArgs)
	}
	if len(m.ExtraHeaders) != 0 {
		t.Errorf("ExtraHeaders should be empty, got %v", m.ExtraHeaders)
	}
	if m.Capabilities.Reasoning != "" {
		t.Errorf("Reasoning should be empty, got %q", m.Capabilities.Reasoning)
	}
}

func TestFireworksHydrationExplicitOverrides(t *testing.T) {
	m := &Model{
		Alias:            "test-fw-custom",
		KnownAuth:        "fireworks",
		BaseURL:          "https://custom.example.com/v1",
		APIKeyEnv:        "CUSTOM_FW_KEY",
		UpstreamProtocol: UpstreamOpenAIChat,
	}
	if err := HydrateModel(m); err != nil {
		t.Fatal(err)
	}
	if m.BaseURL != "https://custom.example.com/v1" {
		t.Errorf("BaseURL override lost: %q", m.BaseURL)
	}
	if m.APIKeyEnv != "CUSTOM_FW_KEY" {
		t.Errorf("APIKeyEnv override lost: %q", m.APIKeyEnv)
	}
}

func TestFirePassHydrationOmittedFields(t *testing.T) {
	m := &Model{
		Alias:     "test-fp",
		KnownAuth: "fireworks-fire-pass",
	}
	if err := HydrateModel(m); err != nil {
		t.Fatal(err)
	}
	if m.BaseURL != "https://api.fireworks.ai/inference/v1" {
		t.Errorf("hydrated BaseURL = %q", m.BaseURL)
	}
	if m.APIKeyEnv != "FIREWORKS_FIRE_PASS_API_KEY" {
		t.Errorf("hydrated APIKeyEnv = %q", m.APIKeyEnv)
	}
	if m.UpstreamProtocol != UpstreamOpenAIChat {
		t.Errorf("hydrated UpstreamProtocol = %q", m.UpstreamProtocol)
	}
	if len(m.ExtraArgs) != 0 {
		t.Errorf("ExtraArgs should be empty, got %v", m.ExtraArgs)
	}
}

func TestFirePassHydrationExplicitOverrides(t *testing.T) {
	m := &Model{
		Alias:            "test-fp-custom",
		KnownAuth:        "fireworks-fire-pass",
		BaseURL:          "https://custom.example.com/v1",
		APIKeyEnv:        "CUSTOM_FP_KEY",
		UpstreamProtocol: UpstreamOpenAIChat,
	}
	if err := HydrateModel(m); err != nil {
		t.Fatal(err)
	}
	if m.BaseURL != "https://custom.example.com/v1" {
		t.Errorf("BaseURL override lost: %q", m.BaseURL)
	}
	if m.APIKeyEnv != "CUSTOM_FP_KEY" {
		t.Errorf("APIKeyEnv override lost: %q", m.APIKeyEnv)
	}
}

func TestFireworksNoRegistryExtraArgsOrTier(t *testing.T) {
	for _, profile := range []string{"fireworks", "fireworks-fire-pass"} {
		ka, _ := LookupKnownAuth(profile)
		if len(ka.ExtraArgs) != 0 {
			t.Errorf("%s profile has ExtraArgs (should inject none): %v", profile, ka.ExtraArgs)
		}
		for k := range ka.ExtraArgs {
			if strings.Contains(strings.ToLower(k), "tier") {
				t.Errorf("%s profile injects tier-related key %q", profile, k)
			}
		}
	}
}

func TestFirePassStaticCatalogContainsCanonicalRouter(t *testing.T) {
	ka, ok := LookupKnownAuth("fireworks-fire-pass")
	if !ok {
		t.Fatal("fireworks-fire-pass profile missing")
	}
	found := false
	for _, entry := range ka.StaticModels {
		if entry.ID == "accounts/fireworks/routers/glm-5p2-fast" {
			found = true
		}
	}
	if !found {
		t.Error("Fire Pass static catalog must contain accounts/fireworks/routers/glm-5p2-fast")
	}
}

func TestFirePassStaticCatalogNoDuplicates(t *testing.T) {
	ka, ok := LookupKnownAuth("fireworks-fire-pass")
	if !ok {
		t.Fatal("fireworks-fire-pass profile missing")
	}
	seen := map[string]bool{}
	for _, entry := range ka.StaticModels {
		if seen[entry.ID] {
			t.Errorf("duplicate router ID in Fire Pass catalog: %s", entry.ID)
		}
		seen[entry.ID] = true
	}
}

func TestFireworksFastCatalogContainsCanonicalRouter(t *testing.T) {
	catalog := FireworksFastCatalog()
	found := false
	for _, entry := range catalog {
		if entry.ID == "accounts/fireworks/routers/glm-5p2-fast" {
			found = true
		}
	}
	if !found {
		t.Error("Fireworks Fast catalog must contain accounts/fireworks/routers/glm-5p2-fast")
	}
}

func TestFireworksFastCatalogNoDuplicates(t *testing.T) {
	catalog := FireworksFastCatalog()
	seen := map[string]bool{}
	for _, entry := range catalog {
		if seen[entry.ID] {
			t.Errorf("duplicate router ID in Fast catalog: %s", entry.ID)
		}
		seen[entry.ID] = true
	}
}
