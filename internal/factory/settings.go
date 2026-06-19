// Package factory reads and writes Factory Droid's ~/.factory/settings.json,
// upserting entries in the customModels array so a model configured in
// droid-proxy shows up in Droid's model picker. It preserves unknown top-level
// keys and unknown per-entry fields.
package factory

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/trevoraspencer/droid-proxy/internal/config"
)

const DefaultMaxOutputTokens = 128000

// DefaultSettingsPath returns ~/.factory/settings.json.
func DefaultSettingsPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".factory", "settings.json")
}

// Entry is the subset of a Factory customModels entry droid-proxy manages.
type Entry struct {
	Model           string
	DisplayName     string
	Provider        string
	BaseURL         string
	APIKey          string
	MaxOutputTokens int
}

// EntryFromModel builds a Factory entry from a configured model. baseURL is the
// proxy listen address (e.g. http://127.0.0.1:8787) and apiKey is the value
// Droid sends (a placeholder unless the proxy enforces client_auth).
func EntryFromModel(m *config.Model, baseURL, apiKey string) Entry {
	maxOut := m.MaxOutputTokens
	if maxOut <= 0 {
		maxOut = DefaultMaxOutputTokens
	}
	display := m.DisplayName
	if strings.TrimSpace(display) == "" {
		display = m.Alias
	}
	if strings.TrimSpace(apiKey) == "" {
		apiKey = "x"
	}
	return Entry{
		Model:           m.Alias,
		DisplayName:     display,
		Provider:        string(m.FactoryProvider),
		BaseURL:         baseURL,
		APIKey:          apiKey,
		MaxOutputTokens: maxOut,
	}
}

// Settings is an editable view of settings.json preserving unknown fields.
type Settings struct {
	path string
	root map[string]json.RawMessage
}

// Load reads settings.json. A missing or empty file yields empty settings.
func Load(path string) (*Settings, error) {
	s := &Settings{path: path, root: map[string]json.RawMessage{}}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, fmt.Errorf("read settings: %w", err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return s, nil
	}
	if err := json.Unmarshal(data, &s.root); err != nil {
		return nil, fmt.Errorf("parse settings: %w", err)
	}
	return s, nil
}

// Path returns the file path backing these settings.
func (s *Settings) Path() string { return s.path }

func (s *Settings) customModels() ([]map[string]json.RawMessage, error) {
	raw, ok := s.root["customModels"]
	if !ok || len(raw) == 0 {
		return nil, nil
	}
	var entries []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, fmt.Errorf("parse customModels: %w", err)
	}
	return entries, nil
}

func (s *Settings) setCustomModels(entries []map[string]json.RawMessage) error {
	raw, err := json.Marshal(entries)
	if err != nil {
		return err
	}
	s.root["customModels"] = raw
	return nil
}

// Models returns the model names currently present in customModels.
func (s *Settings) Models() ([]string, error) {
	entries, err := s.customModels()
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, jsonString(e["model"]))
	}
	return out, nil
}

// Has reports whether a model entry exists.
func (s *Settings) Has(model string) (bool, error) {
	names, err := s.Models()
	if err != nil {
		return false, err
	}
	for _, n := range names {
		if n == model {
			return true, nil
		}
	}
	return false, nil
}

// Upsert inserts or updates the entry keyed by Model, preserving any unknown
// fields on an existing entry.
func (s *Settings) Upsert(e Entry) error {
	entries, err := s.customModels()
	if err != nil {
		return err
	}
	idx := -1
	for i, ent := range entries {
		if jsonString(ent["model"]) == e.Model {
			idx = i
			break
		}
	}
	target := map[string]json.RawMessage{}
	if idx >= 0 {
		target = entries[idx]
	}
	set := func(key string, val any) error {
		raw, mErr := json.Marshal(val)
		if mErr != nil {
			return mErr
		}
		target[key] = raw
		return nil
	}
	for _, kv := range []struct {
		key string
		val any
	}{
		{"model", e.Model},
		{"displayName", e.DisplayName},
		{"provider", e.Provider},
		{"baseUrl", e.BaseURL},
		{"apiKey", e.APIKey},
		{"maxOutputTokens", e.MaxOutputTokens},
	} {
		if err := set(kv.key, kv.val); err != nil {
			return err
		}
	}
	if idx >= 0 {
		entries[idx] = target
	} else {
		entries = append(entries, target)
	}
	return s.setCustomModels(entries)
}

// Remove deletes the entry keyed by model. Returns whether it existed.
func (s *Settings) Remove(model string) (bool, error) {
	entries, err := s.customModels()
	if err != nil {
		return false, err
	}
	for i, ent := range entries {
		if jsonString(ent["model"]) == model {
			entries = append(entries[:i], entries[i+1:]...)
			return true, s.setCustomModels(entries)
		}
	}
	return false, nil
}

// Save writes settings.json (pretty-printed, 0600). When backup is true and a
// file already exists, it is copied to a single rolling settings.json.bak
// first; a subsequent save overwrites that backup.
func (s *Settings) Save(backup bool) error {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if backup {
		if existing, err := os.ReadFile(s.path); err == nil {
			bak := s.path + ".bak"
			if err := os.WriteFile(bak, existing, 0o600); err != nil {
				return fmt.Errorf("backup settings: %w", err)
			}
		}
	}
	out, err := json.MarshalIndent(s.root, "", "  ")
	if err != nil {
		return err
	}
	out = append(out, '\n')
	tmp, err := os.CreateTemp(dir, ".settings-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(out); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return err
	}
	return os.Rename(tmpName, s.path)
}

func jsonString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s
}
