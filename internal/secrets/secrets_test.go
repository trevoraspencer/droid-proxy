package secrets

import (
	"os"
	"testing"
)

func withTempStateDir(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	prev := stateDirFn
	stateDirFn = func() string { return dir }
	t.Cleanup(func() { stateDirFn = prev })
}

func TestSetReadRoundTrip(t *testing.T) {
	withTempStateDir(t)

	if err := Set("FIREWORKS_API_KEY", "fw-secret"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := Set("DEEPSEEK_API_KEY", "ds-secret"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	values, err := Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if values["FIREWORKS_API_KEY"] != "fw-secret" {
		t.Errorf("FIREWORKS_API_KEY = %q, want fw-secret", values["FIREWORKS_API_KEY"])
	}
	if values["DEEPSEEK_API_KEY"] != "ds-secret" {
		t.Errorf("DEEPSEEK_API_KEY = %q, want ds-secret", values["DEEPSEEK_API_KEY"])
	}
}

func TestSetReplacesExisting(t *testing.T) {
	withTempStateDir(t)

	if err := Set("OPENAI_API_KEY", "first"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := Set("OPENAI_API_KEY", "second"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	values, _ := Read()
	if values["OPENAI_API_KEY"] != "second" {
		t.Errorf("OPENAI_API_KEY = %q, want second", values["OPENAI_API_KEY"])
	}
}

func TestFilePermissions(t *testing.T) {
	withTempStateDir(t)

	if err := Set("XAI_API_KEY", "x"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	info, err := os.Stat(Path())
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("perm = %o, want 600", perm)
	}
}

func TestHasAndDelete(t *testing.T) {
	withTempStateDir(t)

	ok, err := Has("GROQ_API_KEY")
	if err != nil {
		t.Fatalf("Has: %v", err)
	}
	if ok {
		t.Fatal("expected Has=false before Set")
	}
	if err := Set("GROQ_API_KEY", "g"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if ok, _ := Has("GROQ_API_KEY"); !ok {
		t.Fatal("expected Has=true after Set")
	}
	if err := Delete("GROQ_API_KEY"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if ok, _ := Has("GROQ_API_KEY"); ok {
		t.Fatal("expected Has=false after Delete")
	}
}

func TestEmptyValueNotPresent(t *testing.T) {
	withTempStateDir(t)
	if err := Set("EMPTY_KEY", ""); err != nil {
		t.Fatalf("Set: %v", err)
	}
	ok, _ := Has("EMPTY_KEY")
	if ok {
		t.Fatal("empty value should report Has=false")
	}
}

func TestSpecialCharValuesRoundTrip(t *testing.T) {
	withTempStateDir(t)
	cases := map[string]string{
		"WITH_QUOTE":     `ab"cd`,
		"WITH_BACKSLASH": `ab\cd`,
		"WITH_SPACE":     "a b c",
		"WITH_EQUALS":    "key=val=ue",
		"WITH_HASH":      "value#notcomment",
	}
	for k, v := range cases {
		if err := Set(k, v); err != nil {
			t.Fatalf("Set %s: %v", k, err)
		}
	}
	values, err := Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	for k, want := range cases {
		if got := values[k]; got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
}
