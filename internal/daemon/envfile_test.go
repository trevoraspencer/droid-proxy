package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func withTempStateDir(t *testing.T) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	return stateDir()
}

func TestLoadEnvFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "env")
	if err := os.WriteFile(path, []byte("export FOO=\"bar\"\n# comment\nBAZ=qux\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := LoadEnvFile(path); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("FOO") != "bar" || os.Getenv("BAZ") != "qux" {
		t.Fatalf("env = FOO:%q BAZ:%q", os.Getenv("FOO"), os.Getenv("BAZ"))
	}
}

func TestLoadEnvFileMissing(t *testing.T) {
	if err := LoadEnvFile(filepath.Join(t.TempDir(), "missing")); err != nil {
		t.Fatal(err)
	}
}

func TestLoadEnvFileInvalidLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "env")
	if err := os.WriteFile(path, []byte("not-an-env-line\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := LoadEnvFile(path)
	if err == nil {
		t.Fatal("expected invalid env line error")
	}
	if want := path + ":1:"; !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %q, want file and line context %q", err, want)
	}
}

func TestParseEnvLineValidKeys(t *testing.T) {
	tests := []struct {
		name      string
		line      string
		wantKey   string
		wantValue string
	}{
		{name: "uppercase", line: "API_KEY=value", wantKey: "API_KEY", wantValue: "value"},
		{name: "leading underscore", line: "_PRIVATE=secret", wantKey: "_PRIVATE", wantValue: "secret"},
		{name: "lowercase and digits", line: "key2=value", wantKey: "key2", wantValue: "value"},
		{name: "export with spaces", line: `export  QUOTED="line\nvalue"`, wantKey: "QUOTED", wantValue: "line\nvalue"},
		{name: "export with tab", line: "export\tTAB_KEY='tab value'", wantKey: "TAB_KEY", wantValue: "tab value"},
		{name: "key named export", line: "export=value", wantKey: "export", wantValue: "value"},
		{name: "export prefix in key", line: "exportFOO=value", wantKey: "exportFOO", wantValue: "value"},
		{name: "equals in value", line: "TOKEN=left=right", wantKey: "TOKEN", wantValue: "left=right"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key, value, ok, err := ParseEnvLine(tt.line)
			if err != nil {
				t.Fatalf("ParseEnvLine: %v", err)
			}
			if !ok || key != tt.wantKey || value != tt.wantValue {
				t.Fatalf("ParseEnvLine = (%q, %q, %v), want (%q, %q, true)", key, value, ok, tt.wantKey, tt.wantValue)
			}
		})
	}
}

func TestParseEnvLineRejectsMalformedForms(t *testing.T) {
	tests := []struct {
		name string
		line string
	}{
		{name: "empty", line: "=value"},
		{name: "missing assignment", line: "KEY"},
		{name: "leading digit", line: "1KEY=value"},
		{name: "embedded space", line: "BAD KEY=value"},
		{name: "space before assignment", line: "KEY =value"},
		{name: "tab before assignment", line: "KEY\t=value"},
		{name: "hyphen", line: "BAD-KEY=value"},
		{name: "period", line: "BAD.KEY=value"},
		{name: "shell punctuation", line: "BAD$KEY=value"},
		{name: "unicode", line: "KÉY=value"},
		{name: "stray export", line: "export export KEY=value"},
		{name: "export without assignment", line: "export KEY"},
		{name: "export empty key", line: "export =value"},
		{name: "export invalid key", line: "export\t1KEY=value"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key, value, ok, err := ParseEnvLine(tt.line)
			if err == nil {
				t.Fatalf("ParseEnvLine(%q) = (%q, %q, %v), want error", tt.line, key, value, ok)
			}
			if ok {
				t.Fatalf("ParseEnvLine(%q) returned ok=true with error %v", tt.line, err)
			}
		})
	}
}

func TestLoadEnvFileInvalidKeyPreservesSequentialBehavior(t *testing.T) {
	t.Setenv("VALID_BEFORE", "original")
	t.Setenv("BAD KEY", "invalid-original")
	t.Setenv("VALID_AFTER", "after-original")

	dir := t.TempDir()
	path := filepath.Join(dir, "env")
	contents := "VALID_BEFORE=updated\nBAD KEY=invalid-update\nVALID_AFTER=after-update\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}

	err := LoadEnvFile(path)
	if err == nil {
		t.Fatal("expected invalid env key error")
	}
	if want := path + ":2:"; !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %q, want file and line context %q", err, want)
	}
	if !strings.Contains(err.Error(), `invalid env key "BAD KEY"`) {
		t.Fatalf("error = %q, want invalid key context", err)
	}
	if got := os.Getenv("VALID_BEFORE"); got != "updated" {
		t.Fatalf("prior valid line = %q, want sequential update", got)
	}
	if got := os.Getenv("BAD KEY"); got != "invalid-original" {
		t.Fatalf("invalid key mutated to %q", got)
	}
	if got := os.Getenv("VALID_AFTER"); got != "after-original" {
		t.Fatalf("line after invalid key mutated to %q", got)
	}
}

func TestResolveEnvFileIgnoresLiveE2EFile(t *testing.T) {
	state := withTempStateDir(t)
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, ".env.live-e2e.local"), []byte("FOO=live\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	got := ResolveEnvFile(workDir)
	want := filepath.Join(state, "env")
	if got != want {
		t.Fatalf("ResolveEnvFile = %q, want %q", got, want)
	}
}

func TestResolveEnvFileSelectsEnvLocal(t *testing.T) {
	withTempStateDir(t)
	workDir := t.TempDir()
	want := filepath.Join(workDir, ".env.local")
	if err := os.WriteFile(filepath.Join(workDir, ".env.live-e2e.local"), []byte("FOO=live\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(want, []byte("FOO=local\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if got := ResolveEnvFile(workDir); got != want {
		t.Fatalf("ResolveEnvFile = %q, want %q", got, want)
	}
}

func TestLoadLayeredEnvLoadsManagedThenExplicit(t *testing.T) {
	state := withTempStateDir(t)
	t.Setenv("FOO", "")
	t.Setenv("BAR", "")
	managed := filepath.Join(state, "env")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(managed, []byte("FOO=managed\nBAR=managed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	workDir := t.TempDir()
	explicit := filepath.Join(workDir, "explicit.env")
	if err := os.WriteFile(explicit, []byte("FOO=explicit\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := LoadLayeredEnv(workDir, explicit); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("FOO") != "explicit" || os.Getenv("BAR") != "managed" {
		t.Fatalf("env = FOO:%q BAR:%q", os.Getenv("FOO"), os.Getenv("BAR"))
	}
}
