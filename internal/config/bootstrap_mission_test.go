//go:build mission_bootstrap

// This file contains mission-local executable bootstrap validation that
// requires the transient gitleaks/Tuistory/Go toolchain provisioned by
// init.sh under /tmp. It is excluded from ordinary `go test ./...` (clean
// GitHub CI) via the "mission_bootstrap" build tag. The mission gate runs:
//
//	go test -tags=mission_bootstrap ./internal/config/...
//
// to execute these tests explicitly after init.sh has been sourced.

package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// bootstrapInitPath wraps resolveBootstrapInitPath and fails the test on
// error. Used only by mission-local bootstrap validation tests.
func bootstrapInitPath(t *testing.T) string {
	t.Helper()
	p, err := resolveBootstrapInitPath()
	if err != nil {
		t.Fatalf("%v", err)
	}
	return p
}

// runBootstrapIsolated executes the bootstrap script under an independent
// temporary root and returns its stdout.
func runBootstrapIsolated(t *testing.T, initPath, repo, label string) string {
	t.Helper()
	isolatedRoot := filepath.Join(t.TempDir(), "bootstrap-"+label)
	cmd := exec.Command("bash", initPath)
	cmd.Env = append(os.Environ(),
		"DROID_PROXY_REPO_ROOT="+repo,
		"DROID_PROXY_BOOTSTRAP_ROOT="+isolatedRoot,
	)
	var stdout strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = nil
	if err := cmd.Run(); err != nil {
		t.Fatalf("bootstrap execution %s failed: %v\nstdout: %s", label, err, stdout.String())
	}
	return stdout.String()
}

// parseBootstrapProjection extracts the deterministic Go, gitleaks, and
// Tuistory version lines from bootstrap stdout.
func parseBootstrapProjection(t *testing.T, output string) string {
	t.Helper()
	var goLine, gitleaksLine, tuistoryLine string
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "go: "):
			goLine = line
		case strings.HasPrefix(line, "gitleaks: "):
			gitleaksLine = line
		case strings.HasPrefix(line, "tuistory: "):
			tuistoryLine = line
		}
	}
	if goLine == "" || gitleaksLine == "" || tuistoryLine == "" {
		t.Fatalf("bootstrap output is missing version projections:\n%s", output)
	}
	return goLine + "\n" + gitleaksLine + "\n" + tuistoryLine
}

// TestBootstrapDeterministicExecution selects the current mission bootstrap
// deterministically (never via ambient globbing or silent skip), executes it
// twice under independent isolated roots, and asserts that both runs produce
// identical Go/gitleaks/Tuistory projections with the exact expected versions.
func TestBootstrapDeterministicExecution(t *testing.T) {
	initPath := bootstrapInitPath(t)

	// Verify script content for isolation and version requirements.
	content, err := os.ReadFile(initPath)
	if err != nil {
		t.Fatalf("read init.sh: %v", err)
	}
	script := string(content)
	if !strings.Contains(script, "DROID_PROXY_BOOTSTRAP_ROOT") {
		t.Fatal("init.sh must support DROID_PROXY_BOOTSTRAP_ROOT for isolated double execution")
	}
	if !strings.Contains(script, "/tmp/droid-proxy-mission-bootstrap") {
		t.Fatal("init.sh must default to the expected bootstrap root pattern")
	}
	if !strings.Contains(script, `export HOME="`) {
		t.Fatal("init.sh must override HOME to an isolated temporary root")
	}
	if !strings.Contains(script, "go.mod") {
		t.Fatal("init.sh must verify Go version from go.mod")
	}
	if !strings.Contains(script, "8.24.2") {
		t.Fatal("init.sh must verify gitleaks 8.24.2")
	}
	if !strings.Contains(script, "0.10.1") {
		t.Fatal("init.sh must verify Tuistory 0.10.1")
	}

	// Execute the bootstrap twice under independent roots.
	repo := repoRoot(t)
	run1 := runBootstrapIsolated(t, initPath, repo, "run-1")
	run2 := runBootstrapIsolated(t, initPath, repo, "run-2")

	// Parse and compare deterministic projections (mismatch coverage).
	proj1 := parseBootstrapProjection(t, run1)
	proj2 := parseBootstrapProjection(t, run2)
	if proj1 != proj2 {
		t.Fatalf("bootstrap projections differ between isolated runs (must be deterministic):\n"+
			"run-1:\n%s\nrun-2:\n%s", proj1, proj2)
	}

	// Assert exact expected versions.
	if !strings.Contains(proj1, "go1.26.4") {
		t.Fatalf("bootstrap must report go1.26.4, got: %s", proj1)
	}
	if !strings.Contains(proj1, "8.24.2") {
		t.Fatalf("bootstrap must report gitleaks 8.24.2, got: %s", proj1)
	}
	if !strings.Contains(proj1, "tuistory/0.10.1") {
		t.Fatalf("bootstrap must report tuistory/0.10.1, got: %s", proj1)
	}
}

// TestBootstrapRefusesBadRepoRoot proves the bootstrap refuses (exits non-zero)
// when its repo root lacks go.mod, rather than silently proceeding.
func TestBootstrapRefusesBadRepoRoot(t *testing.T) {
	initPath := bootstrapInitPath(t)
	badRoot := t.TempDir()
	isolatedRoot := filepath.Join(t.TempDir(), "bad-bootstrap")
	cmd := exec.Command("bash", initPath)
	cmd.Env = append(os.Environ(),
		"DROID_PROXY_REPO_ROOT="+badRoot,
		"DROID_PROXY_BOOTSTRAP_ROOT="+isolatedRoot,
	)
	if err := cmd.Run(); err == nil {
		t.Fatal("bootstrap must refuse (non-zero exit) when repo root lacks go.mod")
	}
}
