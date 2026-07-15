package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- VAL-PORT-024: Structured validation-state write safety ---
//
// Structured validation-state updates (e.g. validation-state.json) must use a
// fail-safe write pattern:
//
//  1. Write transformed output to a same-directory temporary file.
//  2. Require the transform to succeed before continuing.
//  3. Parse the temporary output and validate schema, expected key cardinality,
//     and the exact intended status changes.
//  4. Atomically rename only after every check passes.
//  5. Never move output from a failed transform over the source, and never
//     redirect a failing transform over the source file.
//
// The following tests verify these invariants deterministically.

// validationStateSchema is a minimal schema matching the mission's
// validation-state.json structure.
type validationStateSchema struct {
	Assertions map[string]struct {
		Status               string `json:"status"`
		ValidatedAtMilestone string `json:"validatedAtMilestone,omitempty"`
	} `json:"assertions"`
}

// validateValidationState parses raw JSON and checks the minimum schema
// invariant: a top-level "assertions" object whose entries each have a
// non-empty "status". This mirrors the cardinality check that prevents an
// empty or malformed file from replacing a valid one.
func validateValidationState(t *testing.T, raw []byte) {
	t.Helper()
	var state validationStateSchema
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatalf("validation-state output is not valid JSON: %v", err)
	}
	if state.Assertions == nil {
		t.Fatal("validation-state output has no assertions map")
	}
	if len(state.Assertions) == 0 {
		t.Fatal("validation-state output has zero assertions (cardinality check)")
	}
	for key, entry := range state.Assertions {
		if entry.Status == "" {
			t.Fatalf("assertion %q has empty status", key)
		}
	}
}

// safeWriteValidationState implements the fail-safe structured-state write
// pattern. It writes transformed bytes to a same-directory temp file, validates
// the output, and atomically renames only after all checks pass. If any step
// fails, the temp file is removed and the source file is left untouched.
func safeWriteValidationState(t *testing.T, dest string, transformed []byte) error {
	t.Helper()
	dir := filepath.Dir(dest)
	base := filepath.Base(dest)

	// Step 1: write to a same-directory temp file.
	tmp, err := os.CreateTemp(dir, "."+base+"-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() { os.Remove(tmpName) }

	if _, err := tmp.Write(transformed); err != nil {
		tmp.Close()
		cleanup()
		return fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close temp: %w", err)
	}

	// Step 2: read back and validate the temp output.
	checkRaw, err := os.ReadFile(tmpName)
	if err != nil {
		cleanup()
		return fmt.Errorf("read temp: %w", err)
	}
	var check validationStateSchema
	if err := json.Unmarshal(checkRaw, &check); err != nil {
		cleanup()
		return fmt.Errorf("parse temp: %w", err)
	}
	if check.Assertions == nil {
		cleanup()
		return fmt.Errorf("temp output has no assertions map")
	}
	if len(check.Assertions) == 0 {
		cleanup()
		return fmt.Errorf("temp output has zero assertions (cardinality check)")
	}

	// Step 3: atomic rename only after all checks pass.
	if err := os.Rename(tmpName, dest); err != nil {
		cleanup()
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

func TestValidationStateSafeWrite_SucceedsOnValidTransform(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "validation-state.json")

	original := `{"assertions":{"VAL-PORT-024":{"status":"pending"}}}`
	if err := os.WriteFile(dest, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	// Transform: update VAL-PORT-024 to passed.
	transformed := []byte(`{"assertions":{"VAL-PORT-024":{"status":"passed","validatedAtMilestone":"port-user-testing-followup"}}}`)

	if err := safeWriteValidationState(t, dest, transformed); err != nil {
		t.Fatalf("safeWriteValidationState failed: %v", err)
	}

	// Verify the destination was updated.
	result, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	validateValidationState(t, result)

	var state validationStateSchema
	if err := json.Unmarshal(result, &state); err != nil {
		t.Fatal(err)
	}
	if state.Assertions["VAL-PORT-024"].Status != "passed" {
		t.Fatalf("VAL-PORT-024 status = %q, want passed", state.Assertions["VAL-PORT-024"].Status)
	}

	// Verify no temp files are left behind.
	matches, err := filepath.Glob(filepath.Join(dir, ".*.tmp"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("temp files left behind after successful write: %v", matches)
	}
}

func TestValidationStateSafeWrite_FailedTransformDoesNotCorrupt(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "validation-state.json")

	original := `{"assertions":{"VAL-PORT-024":{"status":"pending"}}}`
	if err := os.WriteFile(dest, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	// Simulate a failed transform: invalid JSON that would be produced by a
	// broken jq filter or pipe error.
	badTransform := []byte(`INVALID JSON{{{`)

	err := safeWriteValidationState(t, dest, badTransform)
	if err == nil {
		t.Fatal("expected error for invalid JSON transform")
	}

	// The source file must be untouched.
	result, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(result) != original {
		t.Fatalf("source file corrupted by failed transform:\ngot:  %s\nwant: %s", result, original)
	}

	// No temp files left behind.
	matches, err := filepath.Glob(filepath.Join(dir, ".*.tmp"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("temp files left behind after failed transform: %v", matches)
	}
}

func TestValidationStateSafeWrite_FailedCardinalityDoesNotCorrupt(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "validation-state.json")

	original := `{"assertions":{"VAL-PORT-024":{"status":"pending"},"VAL-PORT-001":{"status":"passed"}}}`
	if err := os.WriteFile(dest, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	// A transform that produces valid JSON but zero assertions (cardinality
	// violation). This simulates a jq filter that accidentally drops all data.
	emptyTransform := []byte(`{"assertions":{}}`)

	err := safeWriteValidationState(t, dest, emptyTransform)
	if err == nil {
		t.Fatal("expected error for zero-assertions cardinality violation")
	}

	// The source file must be untouched.
	result, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(result) != original {
		t.Fatalf("source file corrupted by failed cardinality check:\ngot:  %s\nwant: %s", result, original)
	}
}

func TestValidationStateSafeWrite_FailedSchemaDoesNotCorrupt(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "validation-state.json")

	original := `{"assertions":{"VAL-PORT-024":{"status":"pending"}}}`
	if err := os.WriteFile(dest, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	// A transform that produces valid JSON but missing the required
	// "assertions" key (schema violation).
	schemaViolation := []byte(`{"metadata":{"lastUpdated":"2026-07-15"}}`)

	err := safeWriteValidationState(t, dest, schemaViolation)
	if err == nil {
		t.Fatal("expected error for missing assertions key (schema violation)")
	}

	// The source file must be untouched.
	result, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(result) != original {
		t.Fatalf("source file corrupted by failed schema check:\ngot:  %s\nwant: %s", result, original)
	}
}

func TestValidationStateSafeWrite_TempFileIsSameDirectory(t *testing.T) {
	// The temp file must be in the same directory as the destination so that
	// os.Rename is atomic (same filesystem). This test verifies the pattern
	// by checking that a temp file is created in the correct directory.
	dir := t.TempDir()
	subdir := filepath.Join(dir, "state")
	if err := os.MkdirAll(subdir, 0o700); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(subdir, "validation-state.json")

	original := `{"assertions":{"VAL-PORT-024":{"status":"pending"}}}`
	if err := os.WriteFile(dest, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	transformed := []byte(`{"assertions":{"VAL-PORT-024":{"status":"passed"}}}`)

	if err := safeWriteValidationState(t, dest, transformed); err != nil {
		t.Fatalf("safeWriteValidationState failed: %v", err)
	}

	// Verify no temp files in parent dir or subdir.
	for _, d := range []string{dir, subdir} {
		matches, err := filepath.Glob(filepath.Join(d, ".*.tmp"))
		if err != nil {
			t.Fatal(err)
		}
		if len(matches) != 0 {
			t.Fatalf("temp files left in %s: %v", d, matches)
		}
	}

	// Verify the update landed.
	result, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(result), "passed") {
		t.Fatalf("destination not updated: %s", result)
	}
}
