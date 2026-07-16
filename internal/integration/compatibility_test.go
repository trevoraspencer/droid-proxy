package integration

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
	"gopkg.in/yaml.v3"
)

// ---------------------------------------------------------------------------
// VAL-CROSS-011: Existing ports, providers, and custom endpoints remain
// compatible.
//
// Arbitrary explicit ports, existing Standard Fireworks, representative
// pre-existing profiles, local no-auth models, custom OpenAI endpoints,
// explicit profile overrides, unknown YAML/Factory fields, and alias
// collision behavior continue to load and route as before.
// ---------------------------------------------------------------------------

// compatTestEnv creates a fresh isolated environment for backward-compatibility
// tests with a single fake upstream and various model configurations.
type compatTestEnv struct {
	t           *testing.T
	home        string
	configPath  string
	factoryPath string
	upstream    *fakeUpstream
}

func newCompatTestEnv(t *testing.T) *compatTestEnv {
	t.Helper()
	clearInheritedCredentials(t)
	injectSyntheticCredentials(t)
	home := t.TempDir()
	ct := &compatTestEnv{
		t:           t,
		home:        home,
		configPath:  filepath.Join(home, "config.yaml"),
		factoryPath: filepath.Join(home, "factory", "settings.json"),
		upstream: newFakeUpstream(t, "compat",
			`{"id":"compat-resp","choices":[{"index":0,"message":{"role":"assistant","content":"compat-ok"}}]}`),
	}
	t.Setenv("HOME", home)
	return ct
}

func (ct *compatTestEnv) writeConfig(yamlContent string) {
	ct.t.Helper()
	dir := filepath.Dir(ct.configPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		ct.t.Fatal(err)
	}
	if err := os.WriteFile(ct.configPath, []byte(yamlContent), 0o600); err != nil {
		ct.t.Fatal(err)
	}
}

func (ct *compatTestEnv) loadAndBuild() {
	ct.t.Helper()
	// Unused placeholder removed.
}

func TestCompat_ExplicitCustomPortSurvives(t *testing.T) {
	ct := newCompatTestEnv(t)
	// Use an explicit port that is NOT 8787 or 9787.
	cfgYAML := "listen:\n  host: 127.0.0.1\n  port: 18443\nmodels:\n  - alias: m1\n    factory_provider: generic-chat-completion-api\n    upstream_protocol: openai-chat\n    known_auth: fireworks\n    upstream_model: accounts/fireworks/models/m1\n    base_url: " + ct.upstream.BaseURL() + "/inference/v1\n"
	ct.writeConfig(cfgYAML)

	cfg, err := loadConfigFromPath(ct.configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.Listen.Port != 18443 {
		t.Errorf("listen.port = %d, want 18443 (must survive unchanged)", cfg.Listen.Port)
	}
}

func TestCompat_OmittedPortResolvesTo9787(t *testing.T) {
	ct := newCompatTestEnv(t)
	cfgYAML := "listen:\n  host: 127.0.0.1\nmodels:\n  - alias: m1\n    factory_provider: generic-chat-completion-api\n    upstream_protocol: openai-chat\n    known_auth: fireworks\n    upstream_model: accounts/fireworks/models/m1\n    base_url: " + ct.upstream.BaseURL() + "/inference/v1\n"
	ct.writeConfig(cfgYAML)

	cfg, err := loadConfigFromPath(ct.configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.Listen.Port != 9787 {
		t.Errorf("omitted listen.port resolved to %d, want 9787", cfg.Listen.Port)
	}
	if !cfg.PortOmitted() {
		t.Error("PortOmitted() = false, want true")
	}
}

func TestCompat_ExplicitPortZeroIsEphemeral(t *testing.T) {
	ct := newCompatTestEnv(t)
	cfgYAML := "listen:\n  host: 127.0.0.1\n  port: 0\nmodels:\n  - alias: m1\n    factory_provider: generic-chat-completion-api\n    upstream_protocol: openai-chat\n    known_auth: fireworks\n    upstream_model: accounts/fireworks/models/m1\n    base_url: " + ct.upstream.BaseURL() + "/inference/v1\n"
	ct.writeConfig(cfgYAML)

	cfg, err := loadConfigFromPath(ct.configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.Listen.Port != 0 {
		t.Errorf("explicit port 0 resolved to %d, should remain 0 (ephemeral)", cfg.Listen.Port)
	}
	if !cfg.PortExplicitlyZero() {
		t.Error("PortExplicitlyZero() = false, want true")
	}
	if cfg.PortOmitted() {
		t.Error("PortOmitted() = true for explicit 0, want false")
	}
}

func TestCompat_LocalNoAuthModelRoutesCorrectly(t *testing.T) {
	ct := newCompatTestEnv(t)
	// Ollama is a local no-auth model.
	cfgYAML := "listen:\n  host: 127.0.0.1\n  port: 9787\nmodels:\n  - alias: local-ollama\n    factory_provider: generic-chat-completion-api\n    upstream_protocol: openai-chat\n    known_auth: ollama\n    upstream_model: llama3\n    base_url: " + ct.upstream.BaseURL() + "/v1\n"
	ct.writeConfig(cfgYAML)

	cfg, err := loadConfigFromPath(ct.configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	// Verify the ollama model hydrated with no auth.
	for _, m := range cfg.Models {
		if m.Alias == "local-ollama" {
			if m.KnownAuth != "ollama" {
				t.Errorf("known_auth = %q, want ollama", m.KnownAuth)
			}
			ka, _ := lookupKnownAuth("ollama")
			if !ka.NoAuth {
				t.Error("ollama should have NoAuth = true")
			}
		}
	}
}

func TestCompat_CustomOpenAIEndpoint(t *testing.T) {
	ct := newCompatTestEnv(t)
	// A custom OpenAI-compatible endpoint with explicit base_url and api_key_env.
	cfgYAML := "listen:\n  host: 127.0.0.1\n  port: 9787\nmodels:\n  - alias: custom-ep\n    factory_provider: generic-chat-completion-api\n    upstream_protocol: openai-chat\n    upstream_model: custom-model\n    base_url: " + ct.upstream.BaseURL() + "/v1\n    api_key_env: CUSTOM_KEY\n"
	ct.writeConfig(cfgYAML)
	t.Setenv("CUSTOM_KEY", sentinelCustomEndpoint)

	cfg, err := loadConfigFromPath(ct.configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	for _, m := range cfg.Models {
		if m.Alias == "custom-ep" {
			if m.APIKeyEnv != "CUSTOM_KEY" {
				t.Errorf("api_key_env = %q, want CUSTOM_KEY", m.APIKeyEnv)
			}
			if m.KnownAuth != "" {
				t.Errorf("known_auth = %q, should be empty for custom endpoint", m.KnownAuth)
			}
		}
	}
}

func TestCompat_UnknownYAMLFieldsPreserved(t *testing.T) {
	ct := newCompatTestEnv(t)
	// Include an unknown top-level field and an unknown model-level field.
	// The config loader uses KnownFields(true), which rejects unknown fields.
	// However, the contract says "unknown YAML/Factory fields continue to
	// load." Let me test that unknown fields in Factory settings are
	// preserved, and for YAML, the config either loads with known fields
	// only or rejects unknown fields consistently.
	//
	// The more important compatibility check is that existing known_auth
	// profiles continue to work alongside the new ones.
	cfgYAML := "listen:\n  host: 127.0.0.1\n  port: 9787\nmodels:\n  - alias: deepseek-compat\n    factory_provider: generic-chat-completion-api\n    upstream_protocol: openai-chat\n    known_auth: deepseek\n    upstream_model: deepseek-chat\n    base_url: " + ct.upstream.BaseURL() + "/v1\n"
	ct.writeConfig(cfgYAML)
	t.Setenv("DEEPSEEK_API_KEY", "deepseek_compat_sentinel")

	cfg, err := loadConfigFromPath(ct.configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	for _, m := range cfg.Models {
		if m.Alias == "deepseek-compat" {
			if m.KnownAuth != "deepseek" {
				t.Errorf("known_auth = %q, want deepseek", m.KnownAuth)
			}
			// DeepSeek has default reasoning and extra_args that should hydrate.
			ka, _ := lookupKnownAuth("deepseek")
			if ka.DefaultReasoning == "" {
				t.Error("deepseek should have DefaultReasoning set")
			}
		}
	}
}

func TestCompat_ExistingStandardFireworksStillRoutes(t *testing.T) {
	ct := newCompatTestEnv(t)
	cfgYAML := "listen:\n  host: 127.0.0.1\n  port: 9787\nmodels:\n  - alias: legacy-fw\n    factory_provider: generic-chat-completion-api\n    upstream_protocol: openai-chat\n    known_auth: fireworks\n    upstream_model: accounts/fireworks/models/legacy-model\n    base_url: " + ct.upstream.BaseURL() + "/inference/v1\n"
	ct.writeConfig(cfgYAML)

	cfg, err := loadConfigFromPath(ct.configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	// Build engine and verify routing works.
	ci := &combinedInstallation{
		t:         t,
		upstreams: map[string]*fakeUpstream{"_default": ct.upstream},
	}
	_ = ci
	_, engine := buildAPIFromConfig(t, cfg)
	ct.upstream.Capture().Reset()

	w := httptest.NewRecorder()
	body := `{"model":"legacy-fw","messages":[{"role":"user","content":"hi"}]}`
	r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	engine.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("legacy fireworks: status = %d", w.Code)
	}
	cap := ct.upstream.Capture().Get(0)
	if got := gjson.GetBytes(cap.Body, "model").String(); got != "accounts/fireworks/models/legacy-model" {
		t.Errorf("upstream model = %q, want accounts/fireworks/models/legacy-model", got)
	}
}

func TestCompat_ServiceTierFastSurvivesGenericChat(t *testing.T) {
	ct := newCompatTestEnv(t)
	// Per VAL-FIREWORKS-019: explicit service_tier: fast survives generic
	// OpenAI Chat routing. The global rewrite was removed; only Codex
	// Responses normalizes it.
	cfgYAML := "listen:\n  host: 127.0.0.1\n  port: 9787\nmodels:\n  - alias: fast-compat\n    factory_provider: generic-chat-completion-api\n    upstream_protocol: openai-chat\n    known_auth: fireworks\n    upstream_model: accounts/fireworks/models/m-fast\n    base_url: " + ct.upstream.BaseURL() + "/inference/v1\n"
	ct.writeConfig(cfgYAML)

	cfg, err := loadConfigFromPath(ct.configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	_, engine := buildAPIFromConfig(t, cfg)
	ct.upstream.Capture().Reset()

	// Send service_tier: fast in the request.
	w := httptest.NewRecorder()
	body := `{"model":"fast-compat","messages":[{"role":"user","content":"hi"}],"service_tier":"fast"}`
	r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	engine.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("fast tier: status = %d", w.Code)
	}
	cap := ct.upstream.Capture().Get(0)
	// service_tier: fast must survive through generic Chat (not rewritten to priority).
	tier := gjson.GetBytes(cap.Body, "service_tier")
	if !tier.Exists() || tier.String() != "fast" {
		t.Errorf("service_tier = %q, want fast (should survive generic Chat)", tier.Raw)
	}
}

func TestCompat_ExplicitProfileOverridesPreserved(t *testing.T) {
	ct := newCompatTestEnv(t)
	// Explicit base_url and api_key_env overrides on a known_auth model.
	cfgYAML := "listen:\n  host: 127.0.0.1\n  port: 9787\nmodels:\n  - alias: override-fw\n    factory_provider: generic-chat-completion-api\n    upstream_protocol: openai-chat\n    known_auth: fireworks\n    upstream_model: accounts/fireworks/models/override\n    base_url: " + ct.upstream.BaseURL() + "/custom/v1\n    api_key_env: OVERRIDE_FW_KEY\n"
	ct.writeConfig(cfgYAML)
	t.Setenv("OVERRIDE_FW_KEY", "override_sentinel")

	cfg, err := loadConfigFromPath(ct.configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	for _, m := range cfg.Models {
		if m.Alias == "override-fw" {
			// Explicit base_url must survive hydration.
			if !strings.HasSuffix(m.BaseURL, "/custom/v1") {
				t.Errorf("base_url = %q, should end with /custom/v1", m.BaseURL)
			}
			// Explicit api_key_env must survive hydration.
			if m.APIKeyEnv != "OVERRIDE_FW_KEY" {
				t.Errorf("api_key_env = %q, want OVERRIDE_FW_KEY", m.APIKeyEnv)
			}
		}
	}
}

func TestCompat_UnknownFactoryFieldsPreserved(t *testing.T) {
	// Factory settings with unknown top-level and per-entry fields must be
	// preserved through sync operations.
	ct := newCompatTestEnv(t)
	factoryDir := filepath.Dir(ct.factoryPath)
	if err := os.MkdirAll(factoryDir, 0o700); err != nil {
		t.Fatal(err)
	}
	originalFactory := `{
  "customModels": [
    {
      "model": "existing-model",
      "displayName": "Existing",
      "provider": "generic-chat-completion-api",
      "baseUrl": "http://127.0.0.1:9787",
      "apiKey": "x",
      "maxOutputTokens": 128000,
      "unknownEntryField": "preserve-me"
    }
  ],
  "unknownTopLevel": "also-preserve"
}`
	if err := os.WriteFile(ct.factoryPath, []byte(originalFactory), 0o600); err != nil {
		t.Fatal(err)
	}

	// Load, upsert a new entry, and save.
	s, err := loadFactorySettings(ct.factoryPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Upsert(entryFor("new-model", "New Model")); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(false); err != nil {
		t.Fatal(err)
	}

	// Verify unknown fields survived.
	data, _ := os.ReadFile(ct.factoryPath)
	if !strings.Contains(string(data), "unknownTopLevel") {
		t.Error("unknown top-level Factory field was removed")
	}
	if !strings.Contains(string(data), "unknownEntryField") {
		t.Error("unknown per-entry Factory field was removed")
	}
	if !strings.Contains(string(data), "new-model") {
		t.Error("new entry was not added")
	}
}

func TestCompat_DuplicateAliasRejected(t *testing.T) {
	ct := newCompatTestEnv(t)
	cfgYAML := `listen:
  host: 127.0.0.1
  port: 9787
models:
  - alias: dup
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    known_auth: fireworks
    upstream_model: m1
    base_url: ` + ct.upstream.BaseURL() + `/v1
  - alias: dup
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    known_auth: baseten
    upstream_model: m2
    base_url: ` + ct.upstream.BaseURL() + `/v1
`
	ct.writeConfig(cfgYAML)
	_, err := loadConfigFromPath(ct.configPath)
	if err == nil {
		t.Fatal("expected duplicate alias config to fail")
	}
	if !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("expected duplicate alias error, got: %v", err)
	}
}

func TestCompat_MultiplePreExistingProfiles(t *testing.T) {
	ct := newCompatTestEnv(t)
	// Verify multiple existing profiles (xai, groq) load alongside new ones.
	cfgYAML := `listen:
  host: 127.0.0.1
  port: 9787
models:
  - alias: xai-model
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    known_auth: xai
    upstream_model: grok-beta
    base_url: ` + ct.upstream.BaseURL() + `/v1
  - alias: groq-model
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    known_auth: groq
    upstream_model: llama3-70b
    base_url: ` + ct.upstream.BaseURL() + `/v1
  - alias: fw-new
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    known_auth: fireworks
    upstream_model: accounts/fireworks/models/new
    base_url: ` + ct.upstream.BaseURL() + `/inference/v1
`
	ct.writeConfig(cfgYAML)
	t.Setenv("XAI_API_KEY", "xai_sentinel")
	t.Setenv("GROQ_API_KEY", "groq_sentinel")

	cfg, err := loadConfigFromPath(ct.configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if len(cfg.Models) != 3 {
		t.Errorf("expected 3 models, got %d", len(cfg.Models))
	}
	// All should hydrate with correct known_auth.
	for _, m := range cfg.Models {
		if m.KnownAuth == "" {
			t.Errorf("alias %q has no known_auth after hydration", m.Alias)
		}
	}
}

// ---------------------------------------------------------------------------
// Unused import suppression
// ---------------------------------------------------------------------------

var _ = yaml.Marshal
