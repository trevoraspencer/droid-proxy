package secrets

import (
	"os"
	"strings"
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

func TestSetPreservesCommentsAndOtherKeys(t *testing.T) {
	withTempStateDir(t)
	body := "# my header\nexport KEEP_KEY=\"keep\"\n\n# trailing note\nexport OPENAI_API_KEY=\"old\"\n"
	if err := os.WriteFile(Path(), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Set("OPENAI_API_KEY", "new"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	raw, err := os.ReadFile(Path())
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	out := string(raw)
	for _, want := range []string{"# my header", "# trailing note", `export KEEP_KEY="keep"`} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q preserved, file:\n%s", want, out)
		}
	}
	values, _ := Read()
	if values["OPENAI_API_KEY"] != "new" {
		t.Errorf("OPENAI_API_KEY = %q, want new", values["OPENAI_API_KEY"])
	}
	if values["KEEP_KEY"] != "keep" {
		t.Errorf("KEEP_KEY = %q, want keep", values["KEEP_KEY"])
	}
	if strings.Contains(out, `="old"`) {
		t.Errorf("old value should be replaced, file:\n%s", out)
	}
}

func TestDeletePreservesOtherLines(t *testing.T) {
	withTempStateDir(t)
	body := "# header\nexport A=\"1\"\nexport B=\"2\"\n# note\n"
	if err := os.WriteFile(Path(), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Delete("A"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	raw, _ := os.ReadFile(Path())
	out := string(raw)
	if strings.Contains(out, "export A=") {
		t.Errorf("A should be deleted, file:\n%s", out)
	}
	for _, want := range []string{"# header", "# note", `export B="2"`} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q preserved, file:\n%s", want, out)
		}
	}
}

func TestReadSkipsInvalidLines(t *testing.T) {
	withTempStateDir(t)
	if err := os.WriteFile(Path(), []byte("not-an-env-line\nexport VALID=\"ok\"\n=empty\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	values, err := Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got := values["VALID"]; got != "ok" {
		t.Fatalf("VALID = %q, want ok", got)
	}
	if _, ok := values[""]; ok {
		t.Fatal("empty key should be skipped")
	}
}
