package tui

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/trevoraspencer/droid-proxy/internal/config"
	"github.com/trevoraspencer/droid-proxy/internal/configedit"
	"github.com/trevoraspencer/droid-proxy/internal/factory"
)

func TestDefaultAlias(t *testing.T) {
	cases := map[string]string{
		"gpt-4o":                "gpt-4o",
		"openai/gpt-4o-mini":    "gpt-4o-mini",
		"Provider/Model_Name 1": "model_name-1",
		"  Weird**Slug!!  ":     "weird-slug",
		"vendor/Foo.Bar-baz":    "foo.bar-baz",
	}
	for in, want := range cases {
		if got := defaultAlias(in); got != want {
			t.Errorf("defaultAlias(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDefaultDisplay(t *testing.T) {
	if got := defaultDisplay("openai/gpt-4o", "OpenAI"); got != "gpt-4o (OpenAI)" {
		t.Errorf("defaultDisplay with label = %q", got)
	}
	if got := defaultDisplay("openai/gpt-4o", ""); got != "gpt-4o" {
		t.Errorf("defaultDisplay without label = %q", got)
	}
}

func TestIsLoopbackBaseURL(t *testing.T) {
	cases := map[string]bool{
		"http://localhost:8080":   true,
		"http://127.0.0.1:1234":   true,
		"http://[::1]:9000":       true,
		"https://api.example.com": false,
		"http://10.0.0.5":         false,
		"not a url":               false,
	}
	for in, want := range cases {
		if got := isLoopbackBaseURL(in); got != want {
			t.Errorf("isLoopbackBaseURL(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestFactoryProviderFor(t *testing.T) {
	cases := map[config.UpstreamProtocol]config.FactoryProvider{
		config.UpstreamOpenAIResponses:   config.FactoryProviderOpenAI,
		config.UpstreamAnthropicMessages: config.FactoryProviderAnthropic,
		config.UpstreamOpenAIChat:        config.FactoryProviderGeneric,
	}
	for in, want := range cases {
		if got := factoryProviderFor(in); got != want {
			t.Errorf("factoryProviderFor(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestUpstreamForOAuth(t *testing.T) {
	if got := upstreamForOAuth(config.OAuthProviderXAI); got != config.UpstreamXAIResponses {
		t.Errorf("xai upstream = %q", got)
	}
	if got := upstreamForOAuth(config.OAuthProviderCodex); got != config.UpstreamCodexResponses {
		t.Errorf("codex upstream = %q", got)
	}
}

func TestProxyBaseURL(t *testing.T) {
	dir := t.TempDir()
	custom := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(custom, []byte("listen:\n  host: 0.0.0.0\n  port: 9999\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := proxyBaseURL(custom); got != "http://0.0.0.0:9999" {
		t.Errorf("proxyBaseURL = %q", got)
	}
	if got := proxyBaseURL(filepath.Join(dir, "missing.yaml")); got != "http://127.0.0.1:8787" {
		t.Errorf("proxyBaseURL default = %q", got)
	}
}

func TestModelRouteSummaryShowsActualProvider(t *testing.T) {
	got := modelRouteSummary(&config.Model{
		KnownAuth:        "zai-coding-api",
		FactoryProvider:  config.FactoryProviderGeneric,
		UpstreamProtocol: config.UpstreamOpenAIChat,
	})
	want := "Z.AI (GLM Coding Plan) · openai-chat"
	if got != want {
		t.Fatalf("route summary = %q, want %q", got, want)
	}

	got = modelRouteSummary(&config.Model{
		OAuthProvider:    config.OAuthProviderXAI,
		FactoryProvider:  config.FactoryProviderOpenAI,
		UpstreamProtocol: config.UpstreamXAIResponses,
	})
	want = "xAI OAuth · xai-responses"
	if got != want {
		t.Fatalf("oauth route summary = %q, want %q", got, want)
	}

	got = modelRouteSummary(&config.Model{
		BaseURL:          "https://api.example.com/v1",
		FactoryProvider:  config.FactoryProviderGeneric,
		UpstreamProtocol: config.UpstreamOpenAIChat,
	})
	want = "api.example.com · openai-chat"
	if got != want {
		t.Fatalf("custom route summary = %q, want %q", got, want)
	}
}

func TestFactoryAPIKey(t *testing.T) {
	if got := factoryAPIKey(nil); got != "x" {
		t.Errorf("nil config = %q, want x", got)
	}
	disabled := &config.Config{}
	disabled.ClientAuth.APIKeys = []string{"unused"}
	if got := factoryAPIKey(disabled); got != "x" {
		t.Errorf("client auth disabled = %q, want x", got)
	}
	enabled := &config.Config{}
	enabled.ClientAuth.Enabled = true
	enabled.ClientAuth.APIKeys = []string{"  ", "real-key"}
	if got := factoryAPIKey(enabled); got != "real-key" {
		t.Errorf("client auth enabled = %q, want real-key", got)
	}
	blank := &config.Config{}
	blank.ClientAuth.Enabled = true
	if got := factoryAPIKey(blank); got != "x" {
		t.Errorf("client auth enabled without keys = %q, want x", got)
	}
}

func TestBackendDiscoverUsesKnownAuthDiscoveryProfile(t *testing.T) {
	var gotPath, gotVersion, gotKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotVersion = r.Header.Get("anthropic-version")
		gotKey = r.Header.Get("x-api-key")
		_, _ = w.Write([]byte(`{"data":[{"id":"claude-sonnet"}]}`))
	}))
	defer srv.Close()

	be := &backend{}
	ids, err := be.discover(config.KnownAuth{
		Name:       "anthropic-test",
		BaseURL:    srv.URL,
		AuthHeader: "x-api-key",
		ModelsPath: "/v1/models",
		ExtraHeaders: map[string]string{
			"anthropic-version": "2023-06-01",
		},
	}, "", "sk-ant-test")
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if gotPath != "/v1/models" {
		t.Fatalf("path = %q, want /v1/models", gotPath)
	}
	if gotVersion != "2023-06-01" {
		t.Fatalf("anthropic-version = %q, want 2023-06-01", gotVersion)
	}
	if gotKey != "sk-ant-test" {
		t.Fatalf("x-api-key = %q, want raw API key", gotKey)
	}
	if want := []string{"claude-sonnet"}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("ids = %v, want %v", ids, want)
	}
}

func newFormModel(t *testing.T, sel providerChoice, values map[string]string) model {
	t.Helper()
	m := model{sel: sel}
	m.buildForm()
	for k, v := range values {
		m.setFormValue(k, v)
	}
	return m
}

func TestBuildModelFromFormKnownAuth(t *testing.T) {
	ka, ok := config.LookupKnownAuth("deepseek")
	if !ok {
		t.Skip("deepseek known_auth not registered")
	}
	m := newFormModel(t, providerChoice{kind: pkKnown, ka: ka, label: ka.Label()}, map[string]string{
		"upstream_model": "deepseek-chat",
		"alias":          "my-deepseek",
	})
	built, err := m.buildModelFromForm()
	if err != nil {
		t.Fatalf("buildModelFromForm: %v", err)
	}
	if built.KnownAuth != ka.Name {
		t.Errorf("KnownAuth = %q, want %q", built.KnownAuth, ka.Name)
	}
	if built.Alias != "my-deepseek" || built.UpstreamModel != "deepseek-chat" {
		t.Errorf("unexpected built model: %#v", built)
	}
}

func TestBuildModelFromFormOAuth(t *testing.T) {
	m := newFormModel(t, providerChoice{kind: pkOAuth, oauth: config.OAuthProviderXAI, label: "xAI"}, map[string]string{
		"upstream_model": "grok-4.3",
		"alias":          "grok",
		"oauth_account":  "me@example.com",
	})
	built, err := m.buildModelFromForm()
	if err != nil {
		t.Fatalf("buildModelFromForm: %v", err)
	}
	if built.OAuthProvider != config.OAuthProviderXAI {
		t.Errorf("OAuthProvider = %q", built.OAuthProvider)
	}
	if built.FactoryProvider != config.FactoryProviderOpenAI {
		t.Errorf("FactoryProvider = %q", built.FactoryProvider)
	}
	if built.UpstreamProtocol != config.UpstreamXAIResponses {
		t.Errorf("UpstreamProtocol = %q", built.UpstreamProtocol)
	}
	if built.OAuthAccount != "me@example.com" {
		t.Errorf("OAuthAccount = %q", built.OAuthAccount)
	}
	if built.Capabilities.FactoryReasoning != config.FactoryReasoningPassthrough {
		t.Errorf("FactoryReasoning = %q, want passthrough", built.Capabilities.FactoryReasoning)
	}
}

func TestCodexOAuthProviderOpensPresetPicker(t *testing.T) {
	m := model{sel: providerChoice{kind: pkOAuth, oauth: config.OAuthProviderCodex, label: "Codex / ChatGPT (OAuth)"}}
	next, _ := m.afterProviderChosen()
	got := next.(model)
	if got.screen != screenPickModel {
		t.Fatalf("screen = %v, want preset picker", got.screen)
	}
	want := []string{
		manualEntryLabel,
		"GPT-5.6 Sol (Recommended)",
		"GPT-5.6 Sol Fast",
		"GPT-5.6 Terra",
		"GPT-5.6 Terra Fast",
		"GPT-5.6 Luna",
		"GPT-5.6 Luna Fast",
	}
	if !reflect.DeepEqual(got.pickItems, want) {
		t.Fatalf("Codex pick items = %#v, want %#v", got.pickItems, want)
	}
}

func TestCodexOAuthGPT56PresetsBuildExpectedModels(t *testing.T) {
	tests := []struct {
		label    string
		alias    string
		upstream string
		fast     bool
	}{
		{label: "GPT-5.6 Sol (Recommended)", alias: "gpt-5.6", upstream: "gpt-5.6-sol"},
		{label: "GPT-5.6 Sol Fast", alias: "gpt-5.6-fast", upstream: "gpt-5.6-sol", fast: true},
		{label: "GPT-5.6 Terra", alias: "gpt-5.6-terra", upstream: "gpt-5.6-terra"},
		{label: "GPT-5.6 Terra Fast", alias: "gpt-5.6-terra-fast", upstream: "gpt-5.6-terra", fast: true},
		{label: "GPT-5.6 Luna", alias: "gpt-5.6-luna", upstream: "gpt-5.6-luna"},
		{label: "GPT-5.6 Luna Fast", alias: "gpt-5.6-luna-fast", upstream: "gpt-5.6-luna", fast: true},
	}

	seenAliases := map[string]bool{}
	for _, tt := range tests {
		t.Run(tt.label, func(t *testing.T) {
			preset, ok := oauthPresetByLabel(config.OAuthProviderCodex, tt.label)
			if !ok {
				t.Fatalf("missing preset %q", tt.label)
			}
			m := newFormModel(t, providerChoice{kind: pkOAuth, oauth: config.OAuthProviderCodex, label: "Codex / ChatGPT (OAuth)"}, nil)
			m.applyOAuthPreset(preset)
			built, err := m.buildModelFromForm()
			if err != nil {
				t.Fatalf("build preset: %v", err)
			}
			if built.Alias != tt.alias || built.UpstreamModel != tt.upstream {
				t.Fatalf("model identity = %q -> %q, want %q -> %q", built.Alias, built.UpstreamModel, tt.alias, tt.upstream)
			}
			if built.FactoryProvider != config.FactoryProviderOpenAI || built.UpstreamProtocol != config.UpstreamCodexResponses || built.OAuthProvider != config.OAuthProviderCodex {
				t.Fatalf("bad Codex route: %#v", built)
			}
			if built.MaxContextTokens != 1050000 || built.MaxOutputTokens != 128000 {
				t.Fatalf("limits = %d/%d, want 1050000/128000", built.MaxContextTokens, built.MaxOutputTokens)
			}
			caps := built.ResolvedCapabilities()
			if !caps.Streaming || !caps.Tools || !caps.ToolResultSafe || !caps.Images || !caps.JSONMode || !caps.StructuredOutput || !caps.PromptCaching || caps.FactoryReasoning != config.FactoryReasoningPassthrough {
				t.Fatalf("incomplete GPT-5.6 capabilities: %#v", caps)
			}
			if tt.fast {
				if got := built.ExtraArgs["service_tier"]; got != "priority" {
					t.Fatalf("fast service_tier = %#v, want priority", got)
				}
			} else if _, exists := built.ExtraArgs["service_tier"]; exists {
				t.Fatalf("standard preset unexpectedly has service_tier: %#v", built.ExtraArgs)
			}
		})
		if seenAliases[tt.alias] {
			t.Fatalf("duplicate preset alias %q", tt.alias)
		}
		seenAliases[tt.alias] = true
	}

	if _, ok := oauthPresetByLabel(config.OAuthProviderCodex, "GPT-5.6 Sol"); ok {
		t.Fatal("explicit Sol duplicate should not be a separate preset; gpt-5.6 is the recommended Sol alias")
	}
}

func TestCodexOAuthGPT56FastPresetConfigRoundTrip(t *testing.T) {
	preset, ok := oauthPresetByLabel(config.OAuthProviderCodex, "GPT-5.6 Sol Fast")
	if !ok {
		t.Fatal("missing GPT-5.6 Sol Fast preset")
	}
	m := newFormModel(t, providerChoice{kind: pkOAuth, oauth: config.OAuthProviderCodex, label: "Codex / ChatGPT (OAuth)"}, nil)
	m.applyOAuthPreset(preset)
	built, err := m.buildModelFromForm()
	if err != nil {
		t.Fatalf("build preset: %v", err)
	}

	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("models: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	be := &backend{configPath: path}
	if err := be.addModel(built); err != nil {
		t.Fatalf("add preset model: %v", err)
	}
	models, err := configedit.LoadModels(path)
	if err != nil {
		t.Fatalf("load written config: %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("written models = %d, want 1", len(models))
	}
	got := models[0]
	if got.Alias != "gpt-5.6-fast" || got.UpstreamModel != "gpt-5.6-sol" {
		t.Fatalf("round-trip model identity = %q -> %q, want gpt-5.6-fast -> gpt-5.6-sol", got.Alias, got.UpstreamModel)
	}
	if got.ExtraArgs["service_tier"] != "priority" {
		t.Fatalf("round-trip service_tier = %#v", got.ExtraArgs["service_tier"])
	}
	caps := got.ResolvedCapabilities()
	if !caps.Images || !caps.StructuredOutput || !caps.PromptCaching || caps.FactoryReasoning != config.FactoryReasoningPassthrough {
		t.Fatalf("round-trip capabilities = %#v", caps)
	}
}

func TestXAIOAuthPresets(t *testing.T) {
	items := oauthPickItems(config.OAuthProviderXAI)
	if len(items) != 4 || items[0] != manualEntryLabel || items[1] != "Grok Build 0.1" || items[2] != "Composer 2.5 Fast" || items[3] != "Grok 4.3" {
		t.Fatalf("xaiOAuthPickItems = %#v", items)
	}

	build, ok := oauthPresetByLabel(config.OAuthProviderXAI, "Grok Build 0.1")
	if !ok {
		t.Fatal("missing Grok Build preset")
	}
	m := newFormModel(t, providerChoice{kind: pkOAuth, oauth: config.OAuthProviderXAI, label: "xAI OAuth"}, nil)
	m.applyOAuthPreset(build)
	built, err := m.buildModelFromForm()
	if err != nil {
		t.Fatalf("build Grok Build preset: %v", err)
	}
	if built.Alias != "grok-build-0.1" || built.UpstreamModel != "grok-build-0.1" || built.DisplayName != "Grok Build 0.1 (xAI OAuth)" {
		t.Fatalf("bad Grok Build preset model: %#v", built)
	}
	if built.BaseURL != "" {
		t.Fatalf("Grok Build base URL = %q, want provider default", built.BaseURL)
	}
	if built.MaxContextTokens != 256000 {
		t.Fatalf("Grok Build context = %d", built.MaxContextTokens)
	}
	if built.MaxOutputTokens != factory.DefaultMaxOutputTokens {
		t.Fatalf("Grok Build max output = %d", built.MaxOutputTokens)
	}
	if built.Capabilities.FactoryReasoning != config.FactoryReasoningDrop {
		t.Fatalf("Grok Build factory_reasoning = %q", built.Capabilities.FactoryReasoning)
	}

	composer, ok := oauthPresetByLabel(config.OAuthProviderXAI, "Composer 2.5 Fast")
	if !ok {
		t.Fatal("missing Composer preset")
	}
	m = newFormModel(t, providerChoice{kind: pkOAuth, oauth: config.OAuthProviderXAI, label: "xAI OAuth"}, nil)
	m.applyOAuthPreset(composer)
	built, err = m.buildModelFromForm()
	if err != nil {
		t.Fatalf("build Composer preset: %v", err)
	}
	if built.Alias != "grok-composer-2.5-fast" || built.UpstreamModel != "grok-composer-2.5-fast" || built.DisplayName != "Composer 2.5 Fast (xAI OAuth)" {
		t.Fatalf("bad Composer preset model: %#v", built)
	}
	if built.BaseURL != "https://cli-chat-proxy.grok.com/v1" {
		t.Fatalf("Composer base URL = %q", built.BaseURL)
	}
	if built.MaxContextTokens != 200000 {
		t.Fatalf("Composer context = %d", built.MaxContextTokens)
	}
	if built.MaxOutputTokens != factory.DefaultMaxOutputTokens {
		t.Fatalf("Composer max output = %d", built.MaxOutputTokens)
	}
	if built.Capabilities.FactoryReasoning != config.FactoryReasoningDrop {
		t.Fatalf("Composer factory_reasoning = %q", built.Capabilities.FactoryReasoning)
	}

	grok43, ok := oauthPresetByLabel(config.OAuthProviderXAI, "Grok 4.3")
	if !ok {
		t.Fatal("missing Grok 4.3 preset")
	}
	m = newFormModel(t, providerChoice{kind: pkOAuth, oauth: config.OAuthProviderXAI, label: "xAI OAuth"}, nil)
	m.applyOAuthPreset(grok43)
	built, err = m.buildModelFromForm()
	if err != nil {
		t.Fatalf("build Grok 4.3 preset: %v", err)
	}
	if built.Alias != "grok-4.3" || built.UpstreamModel != "grok-4.3" || built.DisplayName != "Grok 4.3 (xAI OAuth)" {
		t.Fatalf("bad Grok 4.3 preset model: %#v", built)
	}
	if built.BaseURL != "" {
		t.Fatalf("Grok 4.3 base URL = %q, want provider default", built.BaseURL)
	}
	if built.MaxContextTokens != 1000000 {
		t.Fatalf("Grok 4.3 context = %d", built.MaxContextTokens)
	}
	if built.MaxOutputTokens != factory.DefaultMaxOutputTokens {
		t.Fatalf("Grok 4.3 max output = %d", built.MaxOutputTokens)
	}
	if built.Capabilities.FactoryReasoning != config.FactoryReasoningPassthrough {
		t.Fatalf("Grok 4.3 factory_reasoning = %q", built.Capabilities.FactoryReasoning)
	}
}

func TestBuildModelFromFormCustomValidation(t *testing.T) {
	custom := func(values map[string]string) (*config.Model, error) {
		m := newFormModel(t, providerChoice{kind: pkCustom, label: "Custom"}, values)
		return m.buildModelFromForm()
	}

	if _, err := custom(map[string]string{
		"base_url":       "https://api.remote.example.com/v1",
		"upstream_model": "m",
		"alias":          "a",
	}); err == nil {
		t.Error("remote endpoint without api_key_env should be rejected")
	}

	built, err := custom(map[string]string{
		"base_url":       "http://127.0.0.1:1234/v1",
		"upstream_model": "m",
		"alias":          "a",
	})
	if err != nil {
		t.Fatalf("loopback custom should be allowed: %v", err)
	}
	if built.BaseURL != "http://127.0.0.1:1234/v1" || built.FactoryProvider != config.FactoryProviderGeneric {
		t.Errorf("unexpected custom model: %#v", built)
	}

	if _, err := custom(map[string]string{
		"base_url":          "https://api.remote.example.com/v1",
		"api_key_env":       "EXAMPLE_API_KEY",
		"upstream_model":    "m",
		"alias":             "a",
		"max_output_tokens": "not-a-number",
	}); err == nil {
		t.Error("non-integer max_output_tokens should be rejected")
	}
}

func TestBuildModelFromFormRequiredFields(t *testing.T) {
	if _, err := newFormModel(t, providerChoice{kind: pkCustom}, map[string]string{
		"base_url":       "http://127.0.0.1:1/v1",
		"upstream_model": "m",
	}).buildModelFromForm(); err == nil {
		t.Error("missing alias should be rejected")
	}
	if _, err := newFormModel(t, providerChoice{kind: pkCustom}, map[string]string{
		"base_url": "http://127.0.0.1:1/v1",
		"alias":    "a",
	}).buildModelFromForm(); err == nil {
		t.Error("missing upstream_model should be rejected")
	}
}
