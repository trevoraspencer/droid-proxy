package factory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"droid-proxy/internal/config"
)

func tempSettings(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "settings.json")
	if body != "" {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	return path
}

func TestUpsertCreatesAndIsIdempotent(t *testing.T) {
	path := tempSettings(t, "")
	m := &config.Model{
		Alias:           "deepseek-v4-flash",
		DisplayName:     "DeepSeek V4 Flash",
		FactoryProvider: config.FactoryProviderGeneric,
		MaxOutputTokens: 8192,
	}
	for i := 0; i < 3; i++ {
		s, err := Load(path)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if err := s.Upsert(EntryFromModel(m, "http://127.0.0.1:8787", "x")); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
		if err := s.Save(false); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}

	s, _ := Load(path)
	names, _ := s.Models()
	if len(names) != 1 {
		t.Fatalf("got %d entries, want 1 (idempotent upsert)", len(names))
	}
	if names[0] != "deepseek-v4-flash" {
		t.Errorf("model = %q", names[0])
	}
}

func TestUpsertPreservesUnknownTopLevelAndEntryFields(t *testing.T) {
	path := tempSettings(t, `{
  "theme": "dark",
  "customModels": [
    {"model": "existing", "displayName": "Existing", "provider": "openai", "baseUrl": "http://x", "apiKey": "x", "maxOutputTokens": 100, "customField": true}
  ]
}`)
	s, _ := Load(path)
	m := &config.Model{Alias: "existing", DisplayName: "Updated", FactoryProvider: config.FactoryProviderGeneric, MaxOutputTokens: 4096}
	if err := s.Upsert(EntryFromModel(m, "http://127.0.0.1:8787", "x")); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := s.Save(false); err != nil {
		t.Fatalf("Save: %v", err)
	}

	raw, _ := os.ReadFile(path)
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := root["theme"]; !ok {
		t.Error("unknown top-level key 'theme' not preserved")
	}
	var entries []map[string]json.RawMessage
	_ = json.Unmarshal(root["customModels"], &entries)
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	if _, ok := entries[0]["customField"]; !ok {
		t.Error("unknown per-entry field 'customField' not preserved")
	}
	if jsonString(entries[0]["displayName"]) != "Updated" {
		t.Errorf("displayName = %q, want Updated", jsonString(entries[0]["displayName"]))
	}
}

func TestRemove(t *testing.T) {
	path := tempSettings(t, `{"customModels":[{"model":"a"},{"model":"b"}]}`)
	s, _ := Load(path)
	removed, err := s.Remove("a")
	if err != nil || !removed {
		t.Fatalf("Remove: removed=%v err=%v", removed, err)
	}
	_ = s.Save(false)
	s2, _ := Load(path)
	names, _ := s2.Models()
	if len(names) != 1 || names[0] != "b" {
		t.Fatalf("after remove got %v, want [b]", names)
	}
}

func TestSaveBackupIsSingleRollingFile(t *testing.T) {
	path := tempSettings(t, `{"customModels":[{"model":"a"}]}`)
	for i := 0; i < 3; i++ {
		s, err := Load(path)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if err := s.Upsert(EntryFromModel(&config.Model{Alias: "a", FactoryProvider: config.FactoryProviderGeneric}, "http://127.0.0.1:8787", "x")); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
		if err := s.Save(true); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}
	if _, err := os.Stat(path + ".bak"); err != nil {
		t.Fatalf("expected rolling backup %s.bak: %v", path, err)
	}
	matches, _ := filepath.Glob(path + ".bak-*")
	if len(matches) != 0 {
		t.Fatalf("expected no timestamped backups, got %v", matches)
	}
}

func TestEntryFromModelDefaults(t *testing.T) {
	m := &config.Model{Alias: "no-display", FactoryProvider: config.FactoryProviderGeneric}
	e := EntryFromModel(m, "http://127.0.0.1:8787", "")
	if e.DisplayName != "no-display" {
		t.Errorf("DisplayName = %q, want alias fallback", e.DisplayName)
	}
	if e.MaxOutputTokens != defaultMaxOutputTokens {
		t.Errorf("MaxOutputTokens = %d, want default %d", e.MaxOutputTokens, defaultMaxOutputTokens)
	}
	if e.APIKey != "x" {
		t.Errorf("APIKey = %q, want placeholder x", e.APIKey)
	}
}
