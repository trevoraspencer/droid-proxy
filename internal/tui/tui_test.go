package tui

import (
	"os"
	"path/filepath"
	"testing"

	"droid-proxy/internal/config"
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
		"upstream_model": "grok-4",
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
