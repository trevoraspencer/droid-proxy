package livee2e

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// mergeFilterPath resolves scripts/live-e2e/merge-custom-models.jq relative to
// this test file (internal/livee2e/ → repo root → scripts/live-e2e/).
func mergeFilterPath(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("..", "..", "scripts", "live-e2e", "merge-custom-models.jq"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("merge filter not found at %s: %v", p, err)
	}
	return p
}

// runMerge applies the jq merge filter to settingsPath with the given e2e set
// (a JSON array string) and returns the decoded customModels entries.
func runMerge(t *testing.T, jqBin, filter, settingsPath, e2e string) []map[string]any {
	t.Helper()
	cmd := exec.Command(jqBin, "--argjson", "e2e", e2e, "-f", filter, settingsPath)
	out, err := cmd.Output()
	if err != nil {
		stderr := ""
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = string(ee.Stderr)
		}
		t.Fatalf("jq merge failed: %v\nstderr: %s", err, stderr)
	}
	var settings struct {
		CustomModels []map[string]any `json:"customModels"`
	}
	if err := json.Unmarshal(out, &settings); err != nil {
		t.Fatalf("merge output is not valid JSON: %v\noutput: %s", err, out)
	}
	return settings.CustomModels
}

func modelIDs(models []map[string]any) map[string]int {
	counts := map[string]int{}
	for _, m := range models {
		if id, ok := m["model"].(string); ok {
			counts[id]++
		}
	}
	return counts
}

// TestMergeCustomModelsPreservesUnrelatedAndUpserts verifies the safe-by-default
// settings merge: an unrelated pre-existing model is preserved, an e2e model id
// that already exists is upserted (replaced, not duplicated, with the e2e
// definition winning), and the new e2e model is added — and a second pass is
// idempotent (same model set, no duplicates).
func TestMergeCustomModelsPreservesUnrelatedAndUpserts(t *testing.T) {
	jqBin, err := exec.LookPath("jq")
	if err != nil {
		t.Skip("jq not on PATH; skipping settings-merge test")
	}
	filter := mergeFilterPath(t)
	dir := t.TempDir()

	// Existing settings: one unrelated model (keep-me) and a stale copy of an
	// e2e model id (gpt-5.2-codex) with an outdated displayName.
	existing := `{
      "someOtherKey": "preserved",
      "customModels": [
        {"model":"keep-me","displayName":"Keep","provider":"x","baseUrl":"https://k","maxOutputTokens":1},
        {"model":"gpt-5.2-codex","displayName":"OLD codex","provider":"openai","baseUrl":"https://old","maxOutputTokens":42}
      ]
    }`
	settingsPath := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(settingsPath, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}

	e2e := `[
      {"model":"gpt-5.2-codex","displayName":"GPT-5.2 Codex (ChatGPT OAuth)","provider":"openai","baseUrl":"http://127.0.0.1:8787","apiKey":"not-required-when-client-auth-disabled","maxOutputTokens":128000},
      {"model":"fireworks-live","displayName":"Fireworks Live","provider":"generic-chat-completion-api","baseUrl":"http://127.0.0.1:8787","apiKey":"not-required-when-client-auth-disabled","maxOutputTokens":8192}
    ]`

	models := runMerge(t, jqBin, filter, settingsPath, e2e)
	counts := modelIDs(models)

	if counts["keep-me"] != 1 {
		t.Fatalf("unrelated model keep-me not preserved exactly once: counts=%v", counts)
	}
	if counts["gpt-5.2-codex"] != 1 {
		t.Fatalf("e2e model gpt-5.2-codex not upserted exactly once (dup or missing): counts=%v", counts)
	}
	if counts["fireworks-live"] != 1 {
		t.Fatalf("new e2e model fireworks-live not added: counts=%v", counts)
	}
	if len(models) != 3 {
		t.Fatalf("expected 3 customModels (keep-me + 2 e2e), got %d: %v", len(models), counts)
	}
	// The upserted entry must carry the e2e definition, not the stale one.
	for _, m := range models {
		if m["model"] == "gpt-5.2-codex" {
			if m["displayName"] != "GPT-5.2 Codex (ChatGPT OAuth)" {
				t.Fatalf("gpt-5.2-codex not replaced with e2e definition: displayName=%v", m["displayName"])
			}
			if m["baseUrl"] != "http://127.0.0.1:8787" {
				t.Fatalf("gpt-5.2-codex baseUrl not from e2e set: %v", m["baseUrl"])
			}
		}
	}

	// Idempotency: feed the merged output back through the same filter; the model
	// set and count must be unchanged.
	merged := map[string]any{"someOtherKey": "preserved", "customModels": models}
	mergedBytes, err := json.Marshal(merged)
	if err != nil {
		t.Fatal(err)
	}
	rerunPath := filepath.Join(dir, "settings.rerun.json")
	if err := os.WriteFile(rerunPath, mergedBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	rerun := runMerge(t, jqBin, filter, rerunPath, e2e)
	rerunCounts := modelIDs(rerun)
	if len(rerun) != len(models) {
		t.Fatalf("merge not idempotent: first=%d second=%d", len(models), len(rerun))
	}
	for id, c := range counts {
		if rerunCounts[id] != c {
			t.Fatalf("merge not idempotent for %q: first=%d second=%d (all: %v)", id, c, rerunCounts[id], rerunCounts)
		}
	}
}

// TestMergeCustomModelsFromEmptyBase verifies a base with no customModels (e.g.
// a fresh settings file) yields exactly the e2e set.
func TestMergeCustomModelsFromEmptyBase(t *testing.T) {
	jqBin, err := exec.LookPath("jq")
	if err != nil {
		t.Skip("jq not on PATH; skipping settings-merge test")
	}
	filter := mergeFilterPath(t)
	dir := t.TempDir()

	settingsPath := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(settingsPath, []byte(`{"customModels":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	e2e := `[{"model":"only-e2e","displayName":"E2E","provider":"openai","baseUrl":"http://127.0.0.1:8787","maxOutputTokens":1}]`

	models := runMerge(t, jqBin, filter, settingsPath, e2e)
	if len(models) != 1 || models[0]["model"] != "only-e2e" {
		t.Fatalf("empty-base merge should yield only the e2e set, got %v", models)
	}
}
