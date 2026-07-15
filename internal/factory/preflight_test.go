package factory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trevoraspencer/droid-proxy/internal/config"
)

func writeSettings(t *testing.T, dir, body string) string {
	t.Helper()
	p := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCoherencePreflightAbsentFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "missing.json")
	res, err := CoherencePreflight("127.0.0.1", nil, p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Allowed {
		t.Fatalf("expected Allowed=true for absent Factory file, got refusal: %s", res.Reason)
	}
}

func TestCoherencePreflightEmptyFile(t *testing.T) {
	dir := t.TempDir()
	p := writeSettings(t, dir, "")
	res, err := CoherencePreflight("127.0.0.1", nil, p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Allowed {
		t.Fatalf("expected Allowed=true for empty Factory file, got refusal: %s", res.Reason)
	}
}

func TestCoherencePreflightNoCustomModels(t *testing.T) {
	dir := t.TempDir()
	p := writeSettings(t, dir, `{"someSetting": true}`)
	res, err := CoherencePreflight("127.0.0.1", nil, p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Allowed {
		t.Fatalf("expected Allowed=true for no customModels, got refusal: %s", res.Reason)
	}
}

func TestCoherencePreflightUnrelatedEntries(t *testing.T) {
	dir := t.TempDir()
	p := writeSettings(t, dir, `{
  "customModels": [
    {"model": "other", "displayName": "Other", "provider": "openai", "baseUrl": "https://api.openai.com/v1"}
  ]
}`)
	res, err := CoherencePreflight("127.0.0.1", nil, p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Allowed {
		t.Fatalf("expected Allowed=true for unrelated entries, got refusal: %s", res.Reason)
	}
}

func TestCoherencePreflightMatchingOldOriginRefuses(t *testing.T) {
	dir := t.TempDir()
	models := []*config.Model{
		{Alias: "deepseek-v4-flash", FactoryProvider: config.FactoryProviderGeneric},
	}
	p := writeSettings(t, dir, `{
  "customModels": [
    {"model": "deepseek-v4-flash", "displayName": "DeepSeek", "provider": "generic-chat-completion-api", "baseUrl": "http://127.0.0.1:8787"}
  ]
}`)
	res, err := CoherencePreflight("127.0.0.1", models, p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Allowed {
		t.Fatal("expected refusal for matching old-origin entry, got Allowed=true")
	}
	if !strings.Contains(res.Reason, "8787") {
		t.Fatalf("refusal reason should mention the old port 8787: %s", res.Reason)
	}
}

func TestCoherencePreflightMatchingNewOriginAllowed(t *testing.T) {
	dir := t.TempDir()
	models := []*config.Model{
		{Alias: "deepseek-v4-flash", FactoryProvider: config.FactoryProviderGeneric},
	}
	p := writeSettings(t, dir, `{
  "customModels": [
    {"model": "deepseek-v4-flash", "displayName": "DeepSeek", "provider": "generic-chat-completion-api", "baseUrl": "http://127.0.0.1:9787"}
  ]
}`)
	res, err := CoherencePreflight("127.0.0.1", models, p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Allowed {
		t.Fatalf("expected Allowed=true for entry already on new origin, got refusal: %s", res.Reason)
	}
}

func TestCoherencePreflightAliasMismatchAllowed(t *testing.T) {
	dir := t.TempDir()
	models := []*config.Model{
		{Alias: "my-model", FactoryProvider: config.FactoryProviderGeneric},
	}
	p := writeSettings(t, dir, `{
  "customModels": [
    {"model": "different-alias", "displayName": "Different", "provider": "generic-chat-completion-api", "baseUrl": "http://127.0.0.1:8787"}
  ]
}`)
	res, err := CoherencePreflight("127.0.0.1", models, p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Allowed {
		t.Fatalf("expected Allowed=true when alias doesn't match, got refusal: %s", res.Reason)
	}
}

func TestCoherencePreflightProviderMismatchAllowed(t *testing.T) {
	dir := t.TempDir()
	models := []*config.Model{
		{Alias: "deepseek-v4-flash", FactoryProvider: config.FactoryProviderGeneric},
	}
	p := writeSettings(t, dir, `{
  "customModels": [
    {"model": "deepseek-v4-flash", "displayName": "DeepSeek", "provider": "openai", "baseUrl": "http://127.0.0.1:8787"}
  ]
}`)
	res, err := CoherencePreflight("127.0.0.1", models, p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Allowed {
		t.Fatalf("expected Allowed=true when provider doesn't match, got refusal: %s", res.Reason)
	}
}

func TestCoherencePreflightLocalhostHost(t *testing.T) {
	dir := t.TempDir()
	models := []*config.Model{
		{Alias: "m", FactoryProvider: config.FactoryProviderGeneric},
	}
	p := writeSettings(t, dir, `{
  "customModels": [
    {"model": "m", "displayName": "M", "provider": "generic-chat-completion-api", "baseUrl": "http://localhost:8787"}
  ]
}`)
	res, err := CoherencePreflight("localhost", models, p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Allowed {
		t.Fatal("expected refusal for matching localhost old-origin entry")
	}
}

func TestCoherencePreflightIPv6Host(t *testing.T) {
	dir := t.TempDir()
	models := []*config.Model{
		{Alias: "m", FactoryProvider: config.FactoryProviderGeneric},
	}
	p := writeSettings(t, dir, `{
  "customModels": [
    {"model": "m", "displayName": "M", "provider": "generic-chat-completion-api", "baseUrl": "http://[::1]:8787"}
  ]
}`)
	res, err := CoherencePreflight("::1", models, p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Allowed {
		t.Fatal("expected refusal for matching IPv6 old-origin entry")
	}
}

func TestCoherencePreflightMalformedJSONRefuses(t *testing.T) {
	dir := t.TempDir()
	p := writeSettings(t, dir, `{not valid json`)
	res, err := CoherencePreflight("127.0.0.1", nil, p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Allowed {
		t.Fatal("expected refusal for malformed JSON, got Allowed=true")
	}
}

func TestCoherencePreflightEmptyHostDefaultsToIPv4(t *testing.T) {
	dir := t.TempDir()
	models := []*config.Model{
		{Alias: "m", FactoryProvider: config.FactoryProviderGeneric},
	}
	p := writeSettings(t, dir, `{
  "customModels": [
    {"model": "m", "displayName": "M", "provider": "generic-chat-completion-api", "baseUrl": "http://127.0.0.1:8787"}
  ]
}`)
	res, err := CoherencePreflight("", models, p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Allowed {
		t.Fatal("expected refusal when empty host defaults to 127.0.0.1 and entry matches")
	}
}

func TestCoherencePreflightDoesNotMutateFile(t *testing.T) {
	dir := t.TempDir()
	models := []*config.Model{
		{Alias: "m", FactoryProvider: config.FactoryProviderGeneric},
	}
	body := `{
  "customModels": [
    {"model": "m", "displayName": "M", "provider": "generic-chat-completion-api", "baseUrl": "http://127.0.0.1:8787"}
  ]
}`
	p := writeSettings(t, dir, body)
	before, _ := os.ReadFile(p)
	_, _ = CoherencePreflight("127.0.0.1", models, p)
	after, _ := os.ReadFile(p)
	if string(before) != string(after) {
		t.Fatalf("preflight mutated the Factory file:\nbefore: %s\nafter: %s", before, after)
	}
}
