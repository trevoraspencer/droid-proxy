package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateSetupServiceConfigRejectsSeedOnlyConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("models: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := validateSetupServiceConfig(path)
	if err == nil {
		t.Fatal("validateSetupServiceConfig error = nil, want config-not-ready error")
	}
	msg := err.Error()
	for _, want := range []string{"config is not ready to run", "at least one model", "droid-proxy config"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error missing %q:\n%s", want, msg)
		}
	}
}
