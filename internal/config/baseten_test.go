package config

import (
	"strings"
	"testing"
)

// TestBasetenProfileExact verifies the canonical Baseten registry profile has
// the exact base URL, auth env, protocol, label, and no provider-wide defaults.
func TestBasetenProfileExact(t *testing.T) {
	ka, ok := LookupKnownAuth("baseten")
	if !ok {
		t.Fatal("baseten profile missing from registry")
	}
	if ka.Name != "baseten" {
		t.Errorf("Name = %q, want baseten", ka.Name)
	}
	if ka.BaseURL != "https://inference.baseten.co/v1" {
		t.Errorf("BaseURL = %q, want https://inference.baseten.co/v1", ka.BaseURL)
	}
	if ka.APIKeyEnv != "BASETEN_API_KEY" {
		t.Errorf("APIKeyEnv = %q, want BASETEN_API_KEY", ka.APIKeyEnv)
	}
	if ka.UpstreamProtocol != UpstreamOpenAIChat {
		t.Errorf("UpstreamProtocol = %q, want openai-chat", ka.UpstreamProtocol)
	}
	// Bearer auth: empty AuthHeader defaults to "Authorization", empty
	// AuthScheme defaults to "Bearer" at the transport layer.
	if ka.AuthHeader != "" {
		t.Errorf("AuthHeader = %q, want empty (defaults to Authorization)", ka.AuthHeader)
	}
	if ka.AuthScheme != "" {
		t.Errorf("AuthScheme = %q, want empty (defaults to Bearer)", ka.AuthScheme)
	}
	if ka.NoAuth {
		t.Error("NoAuth should be false")
	}
	if ka.Label() != "Baseten" {
		t.Errorf("Label = %q, want Baseten", ka.Label())
	}
	// Remote authenticated discovery is the default.
	if ka.DiscoveryPolicy != DiscoveryRemote {
		t.Errorf("DiscoveryPolicy = %q, want empty (remote authenticated discovery)", ka.DiscoveryPolicy)
	}
}

// TestBasetenProfileCaseInsensitive verifies mixed-case lookups all resolve
// to the canonical baseten profile.
func TestBasetenProfileCaseInsensitive(t *testing.T) {
	for _, name := range []string{"baseten", "Baseten", "BASETEN", "BaseTen"} {
		ka, ok := LookupKnownAuth(name)
		if !ok {
			t.Errorf("LookupKnownAuth(%q) not found", name)
			continue
		}
		if ka.Name != "baseten" {
			t.Errorf("LookupKnownAuth(%q).Name = %q, want baseten", name, ka.Name)
		}
	}
}

// TestBasetenProfileCountExactlyOne verifies exactly one baseten profile.
func TestBasetenProfileCountExactlyOne(t *testing.T) {
	all := KnownAuthList()
	var count int
	for _, ka := range all {
		if ka.Name == "baseten" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("baseten profile count = %d, want 1", count)
	}
}

// TestBasetenHydrationOmittedFields verifies that hydration fills only the
// registry-owned fields (base URL, env, protocol) when they are omitted.
func TestBasetenHydrationOmittedFields(t *testing.T) {
	m := &Model{
		Alias:     "test-baseten",
		KnownAuth: "baseten",
	}
	if err := HydrateModel(m); err != nil {
		t.Fatal(err)
	}
	if m.BaseURL != "https://inference.baseten.co/v1" {
		t.Errorf("hydrated BaseURL = %q", m.BaseURL)
	}
	if m.APIKeyEnv != "BASETEN_API_KEY" {
		t.Errorf("hydrated APIKeyEnv = %q", m.APIKeyEnv)
	}
	if m.UpstreamProtocol != UpstreamOpenAIChat {
		t.Errorf("hydrated UpstreamProtocol = %q", m.UpstreamProtocol)
	}
	// FactoryProvider is set by the TUI, not by hydration.
	if m.FactoryProvider != "" {
		t.Errorf("FactoryProvider should remain empty (set by TUI), got %q", m.FactoryProvider)
	}
}

// TestBasetenHydrationNoDefaults verifies the Baseten profile injects no
// extra_args, extra_headers, reasoning mode, service tier, or capability
// override during hydration.
func TestBasetenHydrationNoDefaults(t *testing.T) {
	m := &Model{
		Alias:     "test-baseten-no-defaults",
		KnownAuth: "baseten",
	}
	if err := HydrateModel(m); err != nil {
		t.Fatal(err)
	}
	if len(m.ExtraArgs) != 0 {
		t.Errorf("ExtraArgs should be empty, got %v", m.ExtraArgs)
	}
	if len(m.ExtraHeaders) != 0 {
		t.Errorf("ExtraHeaders should be empty, got %v", m.ExtraHeaders)
	}
	if m.Capabilities.Reasoning != "" {
		t.Errorf("Capabilities.Reasoning should be empty, got %q", m.Capabilities.Reasoning)
	}
}

// TestBasetenHydrationExplicitOverrides verifies explicit fields on the model
// are preserved rather than overwritten by the registry.
func TestBasetenHydrationExplicitOverrides(t *testing.T) {
	m := &Model{
		Alias:            "test-baseten-custom",
		KnownAuth:        "baseten",
		BaseURL:          "https://custom.example.com/v1",
		APIKeyEnv:        "CUSTOM_BASETEN_KEY",
		UpstreamProtocol: UpstreamOpenAIChat,
	}
	if err := HydrateModel(m); err != nil {
		t.Fatal(err)
	}
	if m.BaseURL != "https://custom.example.com/v1" {
		t.Errorf("BaseURL override lost: %q", m.BaseURL)
	}
	if m.APIKeyEnv != "CUSTOM_BASETEN_KEY" {
		t.Errorf("APIKeyEnv override lost: %q", m.APIKeyEnv)
	}
}

// TestBasetenRegistryNoProviderWideDefaults verifies the registry profile
// itself carries no provider-wide extra_args, headers, reasoning, or
// capability defaults.
func TestBasetenRegistryNoProviderWideDefaults(t *testing.T) {
	ka, _ := LookupKnownAuth("baseten")
	if len(ka.ExtraArgs) != 0 {
		t.Errorf("ExtraArgs should be empty, got %v", ka.ExtraArgs)
	}
	if len(ka.ExtraHeaders) != 0 {
		t.Errorf("ExtraHeaders should be empty, got %v", ka.ExtraHeaders)
	}
	if ka.DefaultReasoning != "" {
		t.Errorf("DefaultReasoning should be empty, got %q", ka.DefaultReasoning)
	}
	for k := range ka.ExtraArgs {
		if strings.Contains(strings.ToLower(k), "tier") {
			t.Errorf("baseten profile injects tier-related key %q", k)
		}
		if strings.Contains(strings.ToLower(k), "reasoning") {
			t.Errorf("baseten profile injects reasoning-related key %q", k)
		}
	}
}

// TestBasetenDiscoveryResolvesToAuthenticatedModels verifies the discovery
// configuration resolves to an authenticated GET /v1/models endpoint. The
// modelsPath defaults to "models", which joins with the base URL
// "https://inference.baseten.co/v1" to produce the correct discovery path.
func TestBasetenDiscoveryResolvesToAuthenticatedModels(t *testing.T) {
	ka, _ := LookupKnownAuth("baseten")
	// AuthHeader/AuthScheme are empty, which means the providerapi layer
	// defaults to "Authorization: Bearer <key>".
	if ka.AuthHeader != "" {
		t.Errorf("AuthHeader should be empty for Bearer default, got %q", ka.AuthHeader)
	}
	if ka.AuthScheme != "" {
		t.Errorf("AuthScheme should be empty for Bearer default, got %q", ka.AuthScheme)
	}
	// ModelsPath is empty, which defaults to "models" in providerapi.
	// This resolves to GET https://inference.baseten.co/v1/models.
	if ka.ModelsPath != "" {
		t.Errorf("ModelsPath should be empty (defaults to models), got %q", ka.ModelsPath)
	}
}

// TestBasetenDiscoveryPolicyRemote verifies the Baseten profile uses remote
// (authenticated) discovery, not static or manual.
func TestBasetenDiscoveryPolicyRemote(t *testing.T) {
	ka, _ := LookupKnownAuth("baseten")
	if ka.DiscoveryPolicy != DiscoveryRemote {
		t.Errorf("DiscoveryPolicy = %q, want %q (remote authenticated)", ka.DiscoveryPolicy, DiscoveryRemote)
	}
	// StaticModels should be nil/empty for remote discovery.
	if len(ka.StaticModels) != 0 {
		t.Errorf("StaticModels should be empty for remote discovery, got %v", ka.StaticModels)
	}
}
