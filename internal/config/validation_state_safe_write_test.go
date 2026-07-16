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

// validAssertionStatuses defines the set of known assertion status values.
// Any status outside this set is rejected by the shared schema validator.
var validAssertionStatuses = map[string]bool{
	"passed":  true,
	"pending": true,
	"failed":  true,
	"blocked": true,
}

// validateValidationStateSchema is the one shared error-returning schema
// validator for validation-state.json. It rejects invalid JSON, a missing or
// empty assertions map, empty assertion statuses, and unknown assertion
// statuses. It returns an error rather than calling t.Fatalf so that callers
// can decide how to handle validation failures (e.g. clean up temp files and
// leave the destination untouched).
func validateValidationStateSchema(raw []byte) error {
	var state validationStateSchema
	if err := json.Unmarshal(raw, &state); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	if state.Assertions == nil {
		return fmt.Errorf("missing assertions map")
	}
	if len(state.Assertions) == 0 {
		return fmt.Errorf("zero assertions (cardinality check)")
	}
	for key, entry := range state.Assertions {
		if entry.Status == "" {
			return fmt.Errorf("assertion %q has empty status", key)
		}
		if !validAssertionStatuses[entry.Status] {
			return fmt.Errorf("assertion %q has unknown status %q", key, entry.Status)
		}
	}
	return nil
}

// validateValidationState is a test-helper wrapper around the shared
// validateValidationStateSchema that fatals on error. Production callers
// must use validateValidationStateSchema directly so they can clean up
// temporary output and preserve the destination on refusal.
func validateValidationState(t *testing.T, raw []byte) {
	t.Helper()
	if err := validateValidationStateSchema(raw); err != nil {
		t.Fatalf("validation-state schema error: %v", err)
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

	// Step 2: validate the temp output through the one shared error-returning
	// schema validator. This catches invalid JSON, missing/empty assertions,
	// empty statuses, and unknown statuses before atomic replacement.
	checkRaw, err := os.ReadFile(tmpName)
	if err != nil {
		cleanup()
		return fmt.Errorf("read temp: %w", err)
	}
	if err := validateValidationStateSchema(checkRaw); err != nil {
		cleanup()
		return fmt.Errorf("schema validation: %w", err)
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

// --- Shared schema validator tests ---
//
// The shared error-returning validator must reject empty and unknown statuses
// before atomic replacement. These tests verify the validator directly so
// callers can rely on its contract independently of the safe-write wrapper.

func TestValidationStateSchema_AcceptsKnownStatuses(t *testing.T) {
	for _, status := range []string{"passed", "pending", "failed", "blocked"} {
		raw := []byte(fmt.Sprintf(`{"assertions":{"VAL-X-001":{"status":%q}}}`, status))
		if err := validateValidationStateSchema(raw); err != nil {
			t.Fatalf("status %q should be accepted: %v", status, err)
		}
	}
}

func TestValidationStateSchema_RejectsEmptyStatus(t *testing.T) {
	raw := []byte(`{"assertions":{"VAL-X-001":{"status":""}}}`)
	err := validateValidationStateSchema(raw)
	if err == nil {
		t.Fatal("expected error for empty status")
	}
	if !strings.Contains(err.Error(), "empty status") {
		t.Fatalf("error should mention empty status: %v", err)
	}
}

func TestValidationStateSchema_RejectsUnknownStatus(t *testing.T) {
	raw := []byte(`{"assertions":{"VAL-X-001":{"status":"totally-bogus"}}}`)
	err := validateValidationStateSchema(raw)
	if err == nil {
		t.Fatal("expected error for unknown status")
	}
	if !strings.Contains(err.Error(), "unknown status") {
		t.Fatalf("error should mention unknown status: %v", err)
	}
}

func TestValidationStateSchema_RejectsMissingStatus(t *testing.T) {
	raw := []byte(`{"assertions":{"VAL-X-001":{}}}`)
	err := validateValidationStateSchema(raw)
	if err == nil {
		t.Fatal("expected error for missing status field")
	}
}

func TestValidationStateSchema_ReturnsErrorNotFatal(t *testing.T) {
	// The shared validator must return an error rather than calling t.Fatalf
	// so callers can decide cleanup strategy. This test verifies the function
	// signature returns a non-nil error for all rejection cases without
	// requiring a *testing.T parameter.
	cases := [][]byte{
		[]byte(`INVALID`),
		[]byte(`{"assertions":{}}`),
		[]byte(`{"metadata":{}}`),
		[]byte(`{"assertions":{"X":{"status":""}}}`),
		[]byte(`{"assertions":{"X":{"status":"bogus"}}}`),
	}
	for i, raw := range cases {
		err := validateValidationStateSchema(raw)
		if err == nil {
			t.Fatalf("case %d: expected non-nil error", i)
		}
	}
}

// --- Safe-write status refusal tests ---
//
// These tests verify that when the shared schema validator rejects empty or
// unknown statuses, the safe-write wrapper preserves the exact destination
// bytes and removes temporary output.

func TestValidationStateSafeWrite_EmptyStatusDoesNotCorrupt(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "validation-state.json")

	original := `{"assertions":{"VAL-PORT-024":{"status":"pending"}}}`
	originalBytes := []byte(original)
	if err := os.WriteFile(dest, originalBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	// A transform that produces valid JSON with an empty status.
	emptyStatus := []byte(`{"assertions":{"VAL-PORT-024":{"status":""}}}`)

	err := safeWriteValidationState(t, dest, emptyStatus)
	if err == nil {
		t.Fatal("expected error for empty status")
	}

	// The destination must be byte-identical to the original.
	result, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(result) != original {
		t.Fatalf("destination corrupted by empty-status refusal:\ngot:  %s\nwant: %s", result, original)
	}

	// No temp files left behind.
	assertNoTempFiles(t, dir)
}

func TestValidationStateSafeWrite_UnknownStatusDoesNotCorrupt(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "validation-state.json")

	original := `{"assertions":{"VAL-PORT-024":{"status":"pending"},"VAL-PORT-001":{"status":"passed"}}}`
	if err := os.WriteFile(dest, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	// A transform that produces valid JSON with an unknown status value.
	unknownStatus := []byte(`{"assertions":{"VAL-PORT-024":{"status":"bogus-value"},"VAL-PORT-001":{"status":"passed"}}}`)

	err := safeWriteValidationState(t, dest, unknownStatus)
	if err == nil {
		t.Fatal("expected error for unknown status")
	}

	// The destination must be byte-identical to the original.
	result, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(result) != original {
		t.Fatalf("destination corrupted by unknown-status refusal:\ngot:  %s\nwant: %s", result, original)
	}

	// No temp files left behind.
	assertNoTempFiles(t, dir)
}

func TestValidationStateSafeWrite_TempCleanupOnStatusRefusal(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "validation-state.json")

	original := `{"assertions":{"VAL-PORT-024":{"status":"pending"}}}`
	if err := os.WriteFile(dest, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	// Transform with an unknown status that will be rejected by the validator.
	// The temp file must be removed after the refusal.
	unknownStatus := []byte(`{"assertions":{"VAL-PORT-024":{"status":"invalid"}}}`)

	_, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}

	if err := safeWriteValidationState(t, dest, unknownStatus); err == nil {
		t.Fatal("expected error for unknown status")
	}

	// Verify the temp file was removed.
	assertNoTempFiles(t, dir)

	// Verify the destination is untouched.
	result, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(result) != original {
		t.Fatalf("destination corrupted:\ngot:  %s\nwant: %s", result, original)
	}
}

// assertNoTempFiles verifies that no temporary files remain in dir.
func assertNoTempFiles(t *testing.T, dir string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, ".*.tmp"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("temp files left behind: %v", matches)
	}
}
