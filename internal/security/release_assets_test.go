package security

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReleaseAuditScriptPresent(t *testing.T) {
	root := repoRoot(t)
	info, err := os.Stat(filepath.Join(root, "scripts", "release-audit.sh"))
	if err != nil {
		t.Fatalf("scripts/release-audit.sh must exist: %v", err)
	}
	if info.IsDir() || info.Mode()&0o111 == 0 {
		t.Fatal("scripts/release-audit.sh must be an executable file")
	}
}

func TestCIAuditRunsReleaseAudit(t *testing.T) {
	root := repoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, "scripts", "ci-audit.sh"))
	if err != nil {
		t.Fatalf("read scripts/ci-audit.sh: %v", err)
	}
	if !strings.Contains(string(raw), "scripts/release-audit.sh") {
		t.Fatal("ci-audit.sh should run scripts/release-audit.sh")
	}
}

func TestMakefileExposesReleaseAudit(t *testing.T) {
	root := repoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		t.Fatalf("read Makefile: %v", err)
	}
	content := string(raw)
	for _, want := range []string{
		"release-audit:",
		"scripts/release-audit.sh",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("Makefile must expose release audit target containing %q", want)
		}
	}
}

func TestReleaseAssetsScriptPackagesRequiredFiles(t *testing.T) {
	root := repoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, "scripts", "release-assets.sh"))
	if err != nil {
		t.Fatalf("read scripts/release-assets.sh: %v", err)
	}
	content := string(raw)
	for _, want := range []string{
		"install_config.yaml",
		"README.md",
		"LICENSE",
		"scripts/install.sh",
		"checksums.txt",
		"-buildvcs=false",
		"sha256sum",
		"shasum -a 256",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("release-assets.sh must preserve release artifact contract containing %q", want)
		}
	}
}

func TestReleaseWorkflowUploadsInstallableArtifacts(t *testing.T) {
	root := repoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "release.yml"))
	if err != nil {
		t.Fatalf("read release.yml: %v", err)
	}
	content := string(raw)
	for _, want := range []string{
		"bash scripts/release-assets.sh",
		"softprops/action-gh-release",
		"dist/droid-proxy_*.tar.gz",
		"dist/checksums.txt",
		"dist/install.sh",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("release workflow must upload installable artifact contract containing %q", want)
		}
	}
}
