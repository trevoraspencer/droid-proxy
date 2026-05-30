package configedit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"droid-proxy/internal/config"
)

const sampleConfig = `# top comment
listen:
  host: 127.0.0.1
  port: 8787

models:
  # deepseek block
  - alias: deepseek-v4-flash
    display_name: "DeepSeek V4 Flash (DeepSeek)"
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    known_auth: deepseek
    upstream_model: deepseek-v4-flash
`

func writeTemp(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write temp: %v", err)
	}
	return path
}

func TestUpsertAddPreservesComments(t *testing.T) {
	path := writeTemp(t, sampleConfig)
	doc, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	m := &config.Model{
		Alias:            "deepseek-v4-pro",
		DisplayName:      "DeepSeek V4 Pro (Fireworks)",
		FactoryProvider:  config.FactoryProviderGeneric,
		UpstreamProtocol: config.UpstreamOpenAIChat,
		KnownAuth:        "fireworks",
		UpstreamModel:    "accounts/fireworks/models/deepseek-v4-pro",
	}
	if err := doc.Upsert(m); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := doc.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	out, _ := os.ReadFile(path)
	s := string(out)
	if !strings.Contains(s, "# top comment") {
		t.Error("top comment not preserved")
	}
	if !strings.Contains(s, "# deepseek block") {
		t.Error("inline model comment not preserved")
	}
	if !strings.Contains(s, "deepseek-v4-pro") {
		t.Error("new model alias not written")
	}

	models, err := LoadModels(path)
	if err != nil {
		t.Fatalf("LoadModels: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("got %d models, want 2", len(models))
	}
}

func TestUpsertReplacesExisting(t *testing.T) {
	path := writeTemp(t, sampleConfig)
	doc, _ := Load(path)
	m := &config.Model{
		Alias:            "deepseek-v4-flash",
		DisplayName:      "Renamed",
		FactoryProvider:  config.FactoryProviderGeneric,
		UpstreamProtocol: config.UpstreamOpenAIChat,
		KnownAuth:        "deepseek",
		UpstreamModel:    "deepseek-v4-flash",
	}
	if err := doc.Upsert(m); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	_ = doc.Save()

	models, _ := LoadModels(path)
	if len(models) != 1 {
		t.Fatalf("got %d models, want 1 (replace, not append)", len(models))
	}
	if models[0].DisplayName != "Renamed" {
		t.Errorf("display_name = %q, want Renamed", models[0].DisplayName)
	}
}

func TestRemove(t *testing.T) {
	path := writeTemp(t, sampleConfig)
	doc, _ := Load(path)
	removed, err := doc.Remove("deepseek-v4-flash")
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if !removed {
		t.Fatal("expected removed=true")
	}
	_ = doc.Save()
	models, _ := LoadModels(path)
	if len(models) != 0 {
		t.Fatalf("got %d models, want 0", len(models))
	}
}

func TestUpsertOAuthModel(t *testing.T) {
	path := writeTemp(t, sampleConfig)
	doc, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	m := &config.Model{
		Alias:            "grok",
		DisplayName:      "Grok 4",
		FactoryProvider:  config.FactoryProviderOpenAI,
		UpstreamProtocol: config.UpstreamXAIResponses,
		OAuthProvider:    config.OAuthProviderXAI,
		UpstreamModel:    "grok-4",
	}
	if err := doc.Upsert(m); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := doc.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	out, _ := os.ReadFile(path)
	if !strings.Contains(string(out), "oauth_provider: xai") {
		t.Errorf("oauth_provider not written:\n%s", out)
	}

	models, err := LoadModels(path)
	if err != nil {
		t.Fatalf("LoadModels: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("got %d models, want 2", len(models))
	}
	var got *config.Model
	for _, mm := range models {
		if mm.Alias == "grok" {
			got = mm
		}
	}
	if got == nil {
		t.Fatal("oauth model not found after round-trip")
	}
	if got.OAuthProvider != config.OAuthProviderXAI || got.UpstreamProtocol != config.UpstreamXAIResponses {
		t.Errorf("round-trip mismatch: %#v", got)
	}
}

func TestUpsertRejectsInvalid(t *testing.T) {
	path := writeTemp(t, sampleConfig)
	doc, _ := Load(path)
	// Missing factory_provider / known_auth / base_url -> invalid.
	err := doc.Upsert(&config.Model{Alias: "broken"})
	if err == nil {
		t.Fatal("expected validation error for incomplete model")
	}
}
