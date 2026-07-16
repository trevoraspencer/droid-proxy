package config

import (
	"strings"
	"testing"
)

// TestDeepInfraProfileExact verifies the canonical DeepInfra registry profile
// has the exact inference base URL, auth env, protocol, label, Bearer auth, and
// unauthenticated discovery configuration.
func TestDeepInfraProfileExact(t *testing.T) {
	ka, ok := LookupKnownAuth("deepinfra")
	if !ok {
		t.Fatal("deepinfra profile missing from registry")
	}
	if ka.Name != "deepinfra" {
		t.Errorf("Name = %q, want deepinfra", ka.Name)
	}
	if ka.BaseURL != "https://api.deepinfra.com/v1/openai" {
		t.Errorf("BaseURL = %q, want https://api.deepinfra.com/v1/openai", ka.BaseURL)
	}
	if ka.APIKeyEnv != "DEEPINFRA_TOKEN" {
		t.Errorf("APIKeyEnv = %q, want DEEPINFRA_TOKEN", ka.APIKeyEnv)
	}
	if ka.UpstreamProtocol != UpstreamOpenAIChat {
		t.Errorf("UpstreamProtocol = %q, want openai-chat", ka.UpstreamProtocol)
	}
	// Bearer auth for inference: empty AuthHeader defaults to "Authorization",
	// empty AuthScheme defaults to "Bearer" at the transport layer.
	if ka.AuthHeader != "" {
		t.Errorf("AuthHeader = %q, want empty (defaults to Authorization)", ka.AuthHeader)
	}
	if ka.AuthScheme != "" {
		t.Errorf("AuthScheme = %q, want empty (defaults to Bearer)", ka.AuthScheme)
	}
	if ka.NoAuth {
		t.Error("NoAuth should be false (inference requires Bearer auth)")
	}
	if ka.Label() != "DeepInfra" {
		t.Errorf("Label = %q, want DeepInfra", ka.Label())
	}
	// Remote best-effort discovery is the default policy.
	if ka.DiscoveryPolicy != DiscoveryRemote {
		t.Errorf("DiscoveryPolicy = %q, want empty (remote discovery)", ka.DiscoveryPolicy)
	}
}

// TestDeepInfraDiscoverySeparatedFromInference verifies the discovery
// configuration is separated from the inference base URL and auth:
// discovery uses an unauthenticated GET to a different base/path than inference.
func TestDeepInfraDiscoverySeparatedFromInference(t *testing.T) {
	ka, _ := LookupKnownAuth("deepinfra")
	// Discovery base URL must differ from inference base URL.
	if ka.DiscoveryBaseURL == "" {
		t.Error("DiscoveryBaseURL should be set to separate discovery from inference")
	}
	if ka.DiscoveryBaseURL == ka.BaseURL {
		t.Errorf("DiscoveryBaseURL = BaseURL = %q; discovery must use a different origin", ka.DiscoveryBaseURL)
	}
	// Discovery must be unauthenticated.
	if !ka.DiscoveryNoAuth {
		t.Error("DiscoveryNoAuth should be true (catalog discovery is unauthenticated)")
	}
}

// TestDeepInfraDiscoveryEndpointContract verifies the discovery endpoint
// resolves to GET https://api.deepinfra.com/models/list.
func TestDeepInfraDiscoveryEndpointContract(t *testing.T) {
	ka, _ := LookupKnownAuth("deepinfra")
	// The discovery base should be the API host root.
	if ka.DiscoveryBaseURL != "https://api.deepinfra.com" {
		t.Errorf("DiscoveryBaseURL = %q, want https://api.deepinfra.com", ka.DiscoveryBaseURL)
	}
	// The models path should resolve to /models/list.
	if ka.ModelsPath != "/models/list" {
		t.Errorf("ModelsPath = %q, want /models/list", ka.ModelsPath)
	}
}

// TestDeepInfraDiscoveryResponseShape verifies the discovery response shape
// configuration uses model_name as the ID field and reported_type filtering.
func TestDeepInfraDiscoveryResponseShape(t *testing.T) {
	ka, _ := LookupKnownAuth("deepinfra")
	if ka.DiscoveryIDField != "model_name" {
		t.Errorf("DiscoveryIDField = %q, want model_name", ka.DiscoveryIDField)
	}
	if ka.DiscoveryTypeField != "reported_type" {
		t.Errorf("DiscoveryTypeField = %q, want reported_type", ka.DiscoveryTypeField)
	}
	if ka.DiscoveryTypeValue != "text-generation" {
		t.Errorf("DiscoveryTypeValue = %q, want text-generation", ka.DiscoveryTypeValue)
	}
}

// TestDeepInfraProfileCaseInsensitive verifies mixed-case lookups all resolve
// to the canonical deepinfra profile.
func TestDeepInfraProfileCaseInsensitive(t *testing.T) {
	for _, name := range []string{"deepinfra", "DeepInfra", "DEEPINFRA", "DeepInfra"} {
		ka, ok := LookupKnownAuth(name)
		if !ok {
			t.Errorf("LookupKnownAuth(%q) not found", name)
			continue
		}
		if ka.Name != "deepinfra" {
			t.Errorf("LookupKnownAuth(%q).Name = %q, want deepinfra", name, ka.Name)
		}
	}
}

// TestDeepInfraProfileCountExactlyOne verifies exactly one deepinfra profile.
func TestDeepInfraProfileCountExactlyOne(t *testing.T) {
	all := KnownAuthList()
	var count int
	for _, ka := range all {
		if ka.Name == "deepinfra" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("deepinfra profile count = %d, want 1", count)
	}
}

// TestDeepInfraHydrationOmittedFields verifies that hydration fills only the
// registry-owned fields (base URL, env, protocol) when they are omitted.
func TestDeepInfraHydrationOmittedFields(t *testing.T) {
	m := &Model{
		Alias:     "test-deepinfra",
		KnownAuth: "deepinfra",
	}
	if err := HydrateModel(m); err != nil {
		t.Fatal(err)
	}
	if m.BaseURL != "https://api.deepinfra.com/v1/openai" {
		t.Errorf("hydrated BaseURL = %q", m.BaseURL)
	}
	if m.APIKeyEnv != "DEEPINFRA_TOKEN" {
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

// TestDeepInfraHydrationNoDefaults verifies the DeepInfra profile injects no
// extra_args, extra_headers, reasoning mode, service tier, or capability
// override during hydration.
func TestDeepInfraHydrationNoDefaults(t *testing.T) {
	m := &Model{
		Alias:     "test-deepinfra-no-defaults",
		KnownAuth: "deepinfra",
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

// TestDeepInfraHydrationExplicitOverrides verifies explicit fields on the model
// are preserved rather than overwritten by the registry.
func TestDeepInfraHydrationExplicitOverrides(t *testing.T) {
	m := &Model{
		Alias:            "test-deepinfra-custom",
		KnownAuth:        "deepinfra",
		BaseURL:          "https://custom.example.com/v1/openai",
		APIKeyEnv:        "CUSTOM_DEEPINFRA_TOKEN",
		UpstreamProtocol: UpstreamOpenAIChat,
	}
	if err := HydrateModel(m); err != nil {
		t.Fatal(err)
	}
	if m.BaseURL != "https://custom.example.com/v1/openai" {
		t.Errorf("BaseURL override lost: %q", m.BaseURL)
	}
	if m.APIKeyEnv != "CUSTOM_DEEPINFRA_TOKEN" {
		t.Errorf("APIKeyEnv override lost: %q", m.APIKeyEnv)
	}
}

// TestDeepInfraRegistryNoProviderWideDefaults verifies the registry profile
// itself carries no provider-wide extra_args, headers, reasoning, or
// capability defaults.
func TestDeepInfraRegistryNoProviderWideDefaults(t *testing.T) {
	ka, _ := LookupKnownAuth("deepinfra")
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
			t.Errorf("deepinfra profile injects tier-related key %q", k)
		}
		if strings.Contains(strings.ToLower(k), "reasoning") {
			t.Errorf("deepinfra profile injects reasoning-related key %q", k)
		}
	}
}
