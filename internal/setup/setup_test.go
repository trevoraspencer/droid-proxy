package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureConfigSeedsPerUserConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "droid-proxy", "config.yaml")

	res, err := EnsureConfig(path)
	if err != nil {
		t.Fatalf("EnsureConfig: %v", err)
	}
	if !res.Created {
		t.Fatal("Created = false, want true")
	}
	if res.Path != path {
		t.Fatalf("Path = %q, want %q", res.Path, path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, want := range []string{
		"Run `droid-proxy config`",
		"listen:",
		"models: []",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("seeded config missing %q:\n%s", want, body)
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode = %o, want 0600", got)
	}
}

func TestEnsureConfigDoesNotOverwriteExistingConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	original := []byte("sentinel: keep\n")
	if err := os.WriteFile(path, original, 0o640); err != nil {
		t.Fatal(err)
	}

	res, err := EnsureConfig(path)
	if err != nil {
		t.Fatalf("EnsureConfig: %v", err)
	}
	if res.Created {
		t.Fatal("Created = true, want false")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != string(original) {
		t.Fatalf("config overwritten:\n%s", raw)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o640 {
		t.Fatalf("mode = %o, want preserved 0640", got)
	}
}

func TestInstallConfigTemplateReturnsCopy(t *testing.T) {
	first := InstallConfigTemplate()
	first[0] = 'X'
	second := InstallConfigTemplate()
	if len(second) == 0 || second[0] == 'X' {
		t.Fatal("InstallConfigTemplate returned mutable shared slice")
	}
}
