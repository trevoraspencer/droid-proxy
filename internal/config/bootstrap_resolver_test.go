package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// resolveBootstrapInitPath deterministically resolves the current mission
// bootstrap script path. It never uses ambient mission globbing and never
// silently skips. It checks, in order: the DROID_PROXY_MISSION_INIT env var,
// then a marker file inside the known bootstrap root. If neither yields a
// valid path, it returns an actionable error.
//
// This is the error-returning helper extracted from the former bootstrapInitPath
// test helper so that the missing-fixture error path can be exercised directly
// by ordinary `go test ./...` without requiring mission infrastructure.
func resolveBootstrapInitPath() (string, error) {
	// 1. Deterministic env var (set when init.sh is sourced).
	if p := os.Getenv("DROID_PROXY_MISSION_INIT"); p != "" {
		abs, err := filepath.Abs(p)
		if err != nil {
			return "", &bootstrapResolveError{reason: "resolve DROID_PROXY_MISSION_INIT", path: p, err: err}
		}
		info, err := os.Stat(abs)
		if err != nil || info.IsDir() {
			return "", &bootstrapResolveError{reason: "DROID_PROXY_MISSION_INIT does not point to a regular file", path: abs, err: err}
		}
		return abs, nil
	}

	// 2. Marker file written by init.sh into its isolated bootstrap root.
	bootstrapRoot := os.Getenv("DROID_PROXY_BOOTSTRAP_ROOT")
	if bootstrapRoot == "" {
		bootstrapRoot = "/tmp/droid-proxy-mission-bootstrap"
	}
	markerPath := filepath.Join(bootstrapRoot, "init-source")
	raw, err := os.ReadFile(markerPath)
	if err != nil {
		return "", &bootstrapResolveError{
			reason: "bootstrap fixture unavailable: DROID_PROXY_MISSION_INIT is unset and marker is unreadable",
			path:   markerPath,
			err:    err,
			hint:   "Run the mission init.sh first; this assertion may not be silently skipped.",
		}
	}
	p := strings.TrimSpace(string(raw))
	if p == "" {
		return "", &bootstrapResolveError{reason: "bootstrap marker is empty; init.sh must write its path", path: markerPath}
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", &bootstrapResolveError{reason: "resolve marker path", path: p, err: err}
	}
	info, err := os.Stat(abs)
	if err != nil || info.IsDir() {
		return "", &bootstrapResolveError{reason: "bootstrap marker points to non-existent or non-regular file", path: abs, err: err}
	}
	return abs, nil
}

// bootstrapResolveError provides an actionable error message for fixture
// resolution failures.
type bootstrapResolveError struct {
	reason string
	path   string
	err    error
	hint   string
}

func (e *bootstrapResolveError) Error() string {
	msg := e.reason + ": " + e.path
	if e.err != nil {
		msg += ": " + e.err.Error()
	}
	if e.hint != "" {
		msg += "\n" + e.hint
	}
	return msg
}

// TestResolveBootstrapInitPathMissingFixture proves the error-returning
// resolver directly exercises the missing-fixture error path. When neither
// the env var nor the marker file is available, resolveBootstrapInitPath
// returns a non-empty error. This is the refusal coverage required by the
// contract: a missing fixture must never produce a silent pass.
func TestResolveBootstrapInitPathMissingFixture(t *testing.T) {
	// Use a temporary directory as the bootstrap root so no real marker
	// exists. Ensure DROID_PROXY_MISSION_INIT is not set in this process
	// (it is only set when init.sh is sourced).
	missingRoot := t.TempDir()
	t.Setenv("DROID_PROXY_BOOTSTRAP_ROOT", missingRoot)
	// DROID_PROXY_MISSION_INIT is intentionally not set; clear it if
	// inherited from the environment.
	t.Setenv("DROID_PROXY_MISSION_INIT", "")

	_, err := resolveBootstrapInitPath()
	if err == nil {
		t.Fatal("expected error when no bootstrap fixture is available; resolveBootstrapInitPath must not silently pass")
	}
	if msg := err.Error(); msg == "" {
		t.Fatal("error message must be non-empty and actionable")
	}
}

// TestResolveBootstrapInitPathValidEnvVar proves the resolver returns the
// path when DROID_PROXY_MISSION_INIT points to a real file.
func TestResolveBootstrapInitPathValidEnvVar(t *testing.T) {
	dir := t.TempDir()
	fixture := filepath.Join(dir, "fake-init.sh")
	if err := os.WriteFile(fixture, []byte("#!/usr/bin/env bash\necho ok\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DROID_PROXY_MISSION_INIT", fixture)

	got, err := resolveBootstrapInitPath()
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
	abs, _ := filepath.Abs(fixture)
	if got != abs {
		t.Fatalf("resolveBootstrapInitPath = %q, want %q", got, abs)
	}
}
