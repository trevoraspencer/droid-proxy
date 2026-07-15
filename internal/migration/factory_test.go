package migration

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trevoraspencer/droid-proxy/internal/config"
)

func writeFactoryFile(t *testing.T, dir, body string) string {
	t.Helper()
	p := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func model(alias string, provider config.FactoryProvider) *config.Model {
	return &config.Model{Alias: alias, FactoryProvider: provider}
}

// --- Duplicate detection tests ---

func TestDetectJSONDuplicatesNone(t *testing.T) {
	raw := []byte(`{"a": 1, "b": 2, "customModels": [{"model": "m"}]}`)
	dupes, err := detectJSONDuplicates(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(dupes) != 0 {
		t.Fatalf("expected no duplicates, got %v", dupes)
	}
}

func TestDetectJSONDuplicatesTopLevel(t *testing.T) {
	raw := []byte(`{"customModels": [], "customModels": []}`)
	dupes, _ := detectJSONDuplicates(raw)
	if len(dupes) == 0 {
		t.Fatal("expected duplicate at top level")
	}
}

func TestDetectJSONDuplicatesIdentityField(t *testing.T) {
	raw := []byte(`{"customModels": [{"model": "a", "model": "b"}]}`)
	dupes, _ := detectJSONDuplicates(raw)
	if len(dupes) == 0 {
		t.Fatal("expected duplicate model field")
	}
}

func TestDetectJSONDuplicatesNestedObject(t *testing.T) {
	raw := []byte(`{"customModels": [{"model": "a", "config": {"x": 1, "x": 2}}]}`)
	dupes, _ := detectJSONDuplicates(raw)
	if len(dupes) == 0 {
		t.Fatal("expected duplicate in nested object")
	}
}

func TestDetectJSONDuplicatesEscapedEquivalent(t *testing.T) {
	// \u0063ustomModels is escaped form of customModels.
	raw := []byte(`{"customModels": [], "\u0063ustomModels": []}`)
	dupes, _ := detectJSONDuplicates(raw)
	if len(dupes) == 0 {
		t.Fatal("expected escaped-equivalent duplicate")
	}
}

// --- Factory eligibility tests ---

func TestAnalyzeFactoryEligibleEntry(t *testing.T) {
	raw := []byte(`{
  "customModels": [
    {"model": "m", "displayName": "M", "provider": "generic-chat-completion-api", "baseUrl": "http://127.0.0.1:8787"}
  ]
}`)
	models := []*config.Model{model("m", config.FactoryProviderGeneric)}
	a, err := AnalyzeFactory(raw, models, "127.0.0.1", 8787, 9787)
	if err != nil {
		t.Fatal(err)
	}
	if a.Unsafe {
		t.Fatalf("unexpected unsafe: %s", a.Reason)
	}
	if len(a.Changes) != 1 {
		t.Fatalf("expected 1 change, got %d", len(a.Changes))
	}
	ch := a.Changes[0]
	if ch.Model != "m" {
		t.Fatalf("model = %q, want m", ch.Model)
	}
	if ch.OldOrigin != "http://127.0.0.1:8787" {
		t.Fatalf("oldOrigin = %q", ch.OldOrigin)
	}
	if ch.NewOrigin != "http://127.0.0.1:9787" {
		t.Fatalf("newOrigin = %q", ch.NewOrigin)
	}
}

func TestAnalyzeFactoryIPv6BracketedURL(t *testing.T) {
	raw := []byte(`{
  "customModels": [
    {"model": "m", "provider": "generic-chat-completion-api", "baseUrl": "http://[::1]:8787"}
  ]
}`)
	models := []*config.Model{model("m", config.FactoryProviderGeneric)}
	a, _ := AnalyzeFactory(raw, models, "::1", 8787, 9787)
	if len(a.Changes) != 1 {
		t.Fatalf("expected 1 change for ::1 bracketed URL, got %d (reason: %s)", len(a.Changes), a.Reason)
	}
	if a.Changes[0].OldOrigin != "http://[::1]:8787" {
		t.Fatalf("oldOrigin = %q", a.Changes[0].OldOrigin)
	}
}

func TestAnalyzeFactoryLocalhostURL(t *testing.T) {
	raw := []byte(`{
  "customModels": [
    {"model": "m", "provider": "generic-chat-completion-api", "baseUrl": "http://localhost:8787"}
  ]
}`)
	models := []*config.Model{model("m", config.FactoryProviderGeneric)}
	a, _ := AnalyzeFactory(raw, models, "localhost", 8787, 9787)
	if len(a.Changes) != 1 {
		t.Fatalf("expected 1 change for localhost URL")
	}
}

func TestAnalyzeFactoryAliasOnlyNotChanged(t *testing.T) {
	// Alias matches but provider doesn't.
	raw := []byte(`{
  "customModels": [
    {"model": "m", "provider": "openai", "baseUrl": "http://127.0.0.1:8787"}
  ]
}`)
	models := []*config.Model{model("m", config.FactoryProviderGeneric)}
	a, _ := AnalyzeFactory(raw, models, "127.0.0.1", 8787, 9787)
	if len(a.Changes) != 0 {
		t.Fatalf("expected 0 changes for provider mismatch")
	}
}

func TestAnalyzeFactoryProviderMismatchNotChanged(t *testing.T) {
	raw := []byte(`{
  "customModels": [
    {"model": "m", "provider": "openai", "baseUrl": "http://127.0.0.1:8787"}
  ]
}`)
	models := []*config.Model{model("m", config.FactoryProviderGeneric)}
	a, _ := AnalyzeFactory(raw, models, "127.0.0.1", 8787, 9787)
	if len(a.Changes) != 0 {
		t.Fatalf("expected 0 changes for provider mismatch")
	}
}

func TestAnalyzeFactoryModelMismatchNotChanged(t *testing.T) {
	raw := []byte(`{
  "customModels": [
    {"model": "other", "provider": "generic-chat-completion-api", "baseUrl": "http://127.0.0.1:8787"}
  ]
}`)
	models := []*config.Model{model("m", config.FactoryProviderGeneric)}
	a, _ := AnalyzeFactory(raw, models, "127.0.0.1", 8787, 9787)
	if len(a.Changes) != 0 {
		t.Fatalf("expected 0 changes for model mismatch")
	}
}

func TestAnalyzeFactoryURLWithPathNotChanged(t *testing.T) {
	raw := []byte(`{
  "customModels": [
    {"model": "m", "provider": "generic-chat-completion-api", "baseUrl": "http://127.0.0.1:8787/v1"}
  ]
}`)
	models := []*config.Model{model("m", config.FactoryProviderGeneric)}
	a, _ := AnalyzeFactory(raw, models, "127.0.0.1", 8787, 9787)
	if len(a.Changes) != 0 {
		t.Fatalf("expected 0 changes for URL with path")
	}
}

func TestAnalyzeFactoryURLWithTrailingSlashNotChanged(t *testing.T) {
	raw := []byte(`{
  "customModels": [
    {"model": "m", "provider": "generic-chat-completion-api", "baseUrl": "http://127.0.0.1:8787/"}
  ]
}`)
	models := []*config.Model{model("m", config.FactoryProviderGeneric)}
	a, _ := AnalyzeFactory(raw, models, "127.0.0.1", 8787, 9787)
	if len(a.Changes) != 0 {
		t.Fatalf("expected 0 changes for URL with trailing slash")
	}
}

func TestAnalyzeFactoryURLWithQueryNotChanged(t *testing.T) {
	raw := []byte(`{
  "customModels": [
    {"model": "m", "provider": "generic-chat-completion-api", "baseUrl": "http://127.0.0.1:8787?q=1"}
  ]
}`)
	models := []*config.Model{model("m", config.FactoryProviderGeneric)}
	a, _ := AnalyzeFactory(raw, models, "127.0.0.1", 8787, 9787)
	if len(a.Changes) != 0 {
		t.Fatalf("expected 0 changes for URL with query")
	}
}

func TestAnalyzeFactoryURLWithFragmentNotChanged(t *testing.T) {
	raw := []byte(`{
  "customModels": [
    {"model": "m", "provider": "generic-chat-completion-api", "baseUrl": "http://127.0.0.1:8787#frag"}
  ]
}`)
	models := []*config.Model{model("m", config.FactoryProviderGeneric)}
	a, _ := AnalyzeFactory(raw, models, "127.0.0.1", 8787, 9787)
	if len(a.Changes) != 0 {
		t.Fatalf("expected 0 changes for URL with fragment")
	}
}

func TestAnalyzeFactoryURLWithUserinfoNotChanged(t *testing.T) {
	raw := []byte(`{
  "customModels": [
    {"model": "m", "provider": "generic-chat-completion-api", "baseUrl": "http://user:pass@127.0.0.1:8787"}
  ]
}`)
	models := []*config.Model{model("m", config.FactoryProviderGeneric)}
	a, _ := AnalyzeFactory(raw, models, "127.0.0.1", 8787, 9787)
	if len(a.Changes) != 0 {
		t.Fatalf("expected 0 changes for URL with userinfo")
	}
}

func TestAnalyzeFactoryURLDifferentHostCaseNotChanged(t *testing.T) {
	raw := []byte(`{
  "customModels": [
    {"model": "m", "provider": "generic-chat-completion-api", "baseUrl": "http://LOCALHOST:8787"}
  ]
}`)
	models := []*config.Model{model("m", config.FactoryProviderGeneric)}
	a, _ := AnalyzeFactory(raw, models, "localhost", 8787, 9787)
	if len(a.Changes) != 0 {
		t.Fatalf("expected 0 changes for different host case")
	}
}

func TestAnalyzeFactoryURLDifferentPortNotChanged(t *testing.T) {
	raw := []byte(`{
  "customModels": [
    {"model": "m", "provider": "generic-chat-completion-api", "baseUrl": "http://127.0.0.1:5000"}
  ]
}`)
	models := []*config.Model{model("m", config.FactoryProviderGeneric)}
	a, _ := AnalyzeFactory(raw, models, "127.0.0.1", 8787, 9787)
	if len(a.Changes) != 0 {
		t.Fatalf("expected 0 changes for different port")
	}
}

func TestAnalyzeFactoryURLDifferentSchemeNotChanged(t *testing.T) {
	raw := []byte(`{
  "customModels": [
    {"model": "m", "provider": "generic-chat-completion-api", "baseUrl": "https://127.0.0.1:8787"}
  ]
}`)
	models := []*config.Model{model("m", config.FactoryProviderGeneric)}
	a, _ := AnalyzeFactory(raw, models, "127.0.0.1", 8787, 9787)
	if len(a.Changes) != 0 {
		t.Fatalf("expected 0 changes for different scheme")
	}
}

func TestAnalyzeFactoryMalformedJSONUnsafe(t *testing.T) {
	raw := []byte(`{not valid json`)
	a, _ := AnalyzeFactory(raw, nil, "127.0.0.1", 8787, 9787)
	if !a.Unsafe {
		t.Fatal("expected unsafe for malformed JSON")
	}
}

func TestAnalyzeFactoryDuplicateTopLevelUnsafe(t *testing.T) {
	raw := []byte(`{"customModels": [], "customModels": []}`)
	a, _ := AnalyzeFactory(raw, nil, "127.0.0.1", 8787, 9787)
	if !a.Unsafe {
		t.Fatal("expected unsafe for duplicate top-level key")
	}
}

func TestAnalyzeFactoryDuplicateCustomModelsUnsafe(t *testing.T) {
	raw := []byte(`{
  "customModels": [{"model": "m", "provider": "generic-chat-completion-api", "baseUrl": "http://127.0.0.1:8787"}],
  "customModels": [{"model": "m", "provider": "generic-chat-completion-api", "baseUrl": "http://127.0.0.1:8787"}]
}`)
	models := []*config.Model{model("m", config.FactoryProviderGeneric)}
	a, _ := AnalyzeFactory(raw, models, "127.0.0.1", 8787, 9787)
	if !a.Unsafe {
		t.Fatal("expected unsafe for duplicate customModels")
	}
}

func TestAnalyzeFactoryDuplicateIdentityFieldUnsafe(t *testing.T) {
	raw := []byte(`{
  "customModels": [
    {"model": "m", "model": "m2", "provider": "generic-chat-completion-api", "baseUrl": "http://127.0.0.1:8787"}
  ]
}`)
	models := []*config.Model{model("m", config.FactoryProviderGeneric)}
	a, _ := AnalyzeFactory(raw, models, "127.0.0.1", 8787, 9787)
	if !a.Unsafe {
		t.Fatal("expected unsafe for duplicate model field")
	}
}

// --- Factory optional state tests ---

func TestAnalyzeFactoryEmptyFileNoop(t *testing.T) {
	a, _ := AnalyzeFactory([]byte(""), nil, "127.0.0.1", 8787, 9787)
	if a.Unsafe {
		t.Fatal("expected noop for empty file")
	}
	if !a.Noop {
		t.Fatal("expected noop")
	}
}

func TestAnalyzeFactoryWhitespaceOnlyNoop(t *testing.T) {
	a, _ := AnalyzeFactory([]byte("   \n\t  "), nil, "127.0.0.1", 8787, 9787)
	if a.Unsafe {
		t.Fatal("expected noop for whitespace-only file")
	}
}

func TestAnalyzeFactoryNoCustomModelsNoop(t *testing.T) {
	raw := []byte(`{"someSetting": true}`)
	a, _ := AnalyzeFactory(raw, nil, "127.0.0.1", 8787, 9787)
	if a.Unsafe {
		t.Fatal("expected noop for no customModels")
	}
}

func TestAnalyzeFactoryEmptyCustomModelsNoop(t *testing.T) {
	raw := []byte(`{"customModels": []}`)
	a, _ := AnalyzeFactory(raw, nil, "127.0.0.1", 8787, 9787)
	if a.Unsafe {
		t.Fatal("expected noop for empty customModels")
	}
}

func TestAnalyzeFactoryNullCustomModelsNoop(t *testing.T) {
	raw := []byte(`{"customModels": null}`)
	a, _ := AnalyzeFactory(raw, nil, "127.0.0.1", 8787, 9787)
	if a.Unsafe {
		t.Fatal("expected noop for null customModels")
	}
}

func TestAnalyzeFactoryNonArrayCustomModelsUnsafe(t *testing.T) {
	raw := []byte(`{"customModels": "not an array"}`)
	a, _ := AnalyzeFactory(raw, nil, "127.0.0.1", 8787, 9787)
	if !a.Unsafe {
		t.Fatal("expected unsafe for non-array customModels")
	}
}

// --- Factory rewrite tests ---

func TestRewriteFactorySingleEntry(t *testing.T) {
	raw := []byte(`{
  "customModels": [
    {"model": "m", "displayName": "M", "provider": "generic-chat-completion-api", "baseUrl": "http://127.0.0.1:8787", "apiKey": "secret"}
  ]
}`)
	models := []*config.Model{model("m", config.FactoryProviderGeneric)}
	a, _ := AnalyzeFactory(raw, models, "127.0.0.1", 8787, 9787)
	if len(a.Changes) != 1 {
		t.Fatalf("expected 1 change, got %d", len(a.Changes))
	}

	result, err := RewriteFactory(raw, a.Changes)
	if err != nil {
		t.Fatal(err)
	}

	// Verify only the port changed.
	if !strings.Contains(string(result), ":9787") {
		t.Fatal("new port 9787 not found in result")
	}
	if strings.Contains(string(result), ":8787") {
		t.Fatal("old port 8787 still present in result")
	}
	// Verify other fields preserved.
	if !strings.Contains(string(result), `"secret"`) {
		t.Fatal("apiKey not preserved")
	}
	if !strings.Contains(string(result), `"M"`) {
		t.Fatal("displayName not preserved")
	}
}

func TestRewriteFactoryPreservesUnknownFields(t *testing.T) {
	raw := []byte(`{
  "version": 42,
  "customModels": [
    {"model": "m", "provider": "generic-chat-completion-api", "baseUrl": "http://127.0.0.1:8787", "customField": "preserve-me"}
  ],
  "otherSetting": true
}`)
	models := []*config.Model{model("m", config.FactoryProviderGeneric)}
	a, _ := AnalyzeFactory(raw, models, "127.0.0.1", 8787, 9787)
	result, err := RewriteFactory(raw, a.Changes)
	if err != nil {
		t.Fatal(err)
	}

	// Verify result is valid JSON.
	var obj map[string]any
	if err := json.Unmarshal(result, &obj); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if obj["version"] != float64(42) {
		t.Fatal("version not preserved")
	}
	if obj["otherSetting"] != true {
		t.Fatal("otherSetting not preserved")
	}
	entries := obj["customModels"].([]any)
	entry := entries[0].(map[string]any)
	if entry["customField"] != "preserve-me" {
		t.Fatal("customField not preserved")
	}
	if entry["baseUrl"] != "http://127.0.0.1:9787" {
		t.Fatalf("baseUrl = %v", entry["baseUrl"])
	}
}

func TestRewriteFactoryMultipleEntries(t *testing.T) {
	raw := []byte(`{
  "customModels": [
    {"model": "a", "provider": "generic-chat-completion-api", "baseUrl": "http://127.0.0.1:8787"},
    {"model": "b", "provider": "generic-chat-completion-api", "baseUrl": "http://127.0.0.1:8787"},
    {"model": "c", "provider": "openai", "baseUrl": "http://127.0.0.1:8787"}
  ]
}`)
	models := []*config.Model{
		model("a", config.FactoryProviderGeneric),
		model("b", config.FactoryProviderGeneric),
		// c is not in models, so it won't match
	}
	a, _ := AnalyzeFactory(raw, models, "127.0.0.1", 8787, 9787)
	if len(a.Changes) != 2 {
		t.Fatalf("expected 2 changes, got %d", len(a.Changes))
	}

	result, err := RewriteFactory(raw, a.Changes)
	if err != nil {
		t.Fatal(err)
	}

	// Count occurrences of each port.
	count8787 := strings.Count(string(result), ":8787")
	count9787 := strings.Count(string(result), ":9787")
	if count8787 != 1 {
		t.Fatalf("expected 1 remaining :8787 (entry c), got %d", count8787)
	}
	if count9787 != 2 {
		t.Fatalf("expected 2 :9787 (entries a and b), got %d", count9787)
	}
}

func TestRewriteFactoryPreservesByteFormatting(t *testing.T) {
	// Use non-standard formatting that must be preserved.
	raw := []byte(`{"customModels":[{"model":"m","provider":"generic-chat-completion-api","baseUrl":"http://127.0.0.1:8787"}]}`)
	models := []*config.Model{model("m", config.FactoryProviderGeneric)}
	a, _ := AnalyzeFactory(raw, models, "127.0.0.1", 8787, 9787)
	result, err := RewriteFactory(raw, a.Changes)
	if err != nil {
		t.Fatal(err)
	}
	expected := `{"customModels":[{"model":"m","provider":"generic-chat-completion-api","baseUrl":"http://127.0.0.1:9787"}]}`
	if string(result) != expected {
		t.Fatalf("result = %s\nwant   = %s", result, expected)
	}
}

// --- Factory state check tests ---

func TestCheckFactoryStateAbsent(t *testing.T) {
	dir := t.TempDir()
	state, err := CheckFactoryState(filepath.Join(dir, "missing.json"))
	if err != nil {
		t.Fatal(err)
	}
	if state != FactoryAbsent {
		t.Fatalf("expected FactoryAbsent, got %d", state)
	}
}

func TestCheckFactoryStateEmpty(t *testing.T) {
	dir := t.TempDir()
	p := writeFactoryFile(t, dir, "")
	state, _ := CheckFactoryState(p)
	if state != FactoryEmpty {
		t.Fatalf("expected FactoryEmpty, got %d", state)
	}
}

func TestCheckFactoryStateSafe(t *testing.T) {
	dir := t.TempDir()
	p := writeFactoryFile(t, dir, `{"customModels":[]}`)
	state, _ := CheckFactoryState(p)
	if state != FactorySafe {
		t.Fatalf("expected FactorySafe, got %d", state)
	}
}

func TestCheckFactoryStateUnsafeMalformed(t *testing.T) {
	dir := t.TempDir()
	p := writeFactoryFile(t, dir, `{bad json`)
	state, _ := CheckFactoryState(p)
	if state != FactoryUnsafe {
		t.Fatalf("expected FactoryUnsafe, got %d", state)
	}
}

func TestCheckFactoryStateUnsafeDuplicates(t *testing.T) {
	dir := t.TempDir()
	p := writeFactoryFile(t, dir, `{"customModels":[], "customModels":[]}`)
	state, _ := CheckFactoryState(p)
	if state != FactoryUnsafe {
		t.Fatalf("expected FactoryUnsafe for duplicates, got %d", state)
	}
}

func TestCheckFactoryStateUnsafeNonArray(t *testing.T) {
	dir := t.TempDir()
	p := writeFactoryFile(t, dir, `{"customModels":"not-array"}`)
	state, _ := CheckFactoryState(p)
	if state != FactoryUnsafe {
		t.Fatalf("expected FactoryUnsafe for non-array, got %d", state)
	}
}
