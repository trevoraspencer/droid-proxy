package security

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var staleImportLine = regexp.MustCompile(`(?m)^\s*"droid-proxy/`)

const publicModulePath = "github.com/trevoraspencer/droid-proxy"

func TestGoModUsesPublicModulePath(t *testing.T) {
	root := repoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	first := strings.TrimSpace(strings.Split(string(raw), "\n")[0])
	want := "module " + publicModulePath
	if first != want {
		t.Fatalf("go.mod first line = %q, want %q", first, want)
	}
}

func TestGoSourcesUsePublicModuleImports(t *testing.T) {
	root := repoRoot(t)
	files := gitLsFiles(t, root)
	for _, rel := range files {
		if !strings.HasSuffix(rel, ".go") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		if staleImportLine.Match(raw) {
			t.Errorf("%s still imports local module path droid-proxy/", rel)
		}
	}
}

func TestCIWorkflowPresent(t *testing.T) {
	root := repoRoot(t)
	required := []string{
		".github/workflows/ci.yml",
		".github/dependabot.yml",
	}
	for _, rel := range required {
		if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
			t.Fatalf("missing CI artifact %s: %v", rel, err)
		}
	}
}

// TestCIWorkflowRunsOnMain verifies the CI workflow triggers on push and pull
// requests and exercises a build and the test suite. The repository's workflow
// drives these through Make targets (make build / make test), so this test
// accepts either the Make targets or direct `go build`/`go test` invocations.
func TestCIWorkflowRunsOnMain(t *testing.T) {
	root := repoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatalf("read ci.yml: %v", err)
	}
	content := string(raw)

	for _, needle := range []string{"push:", "pull_request:"} {
		if !strings.Contains(content, needle) {
			t.Fatalf("ci.yml missing trigger %q", needle)
		}
	}

	requirements := []struct {
		desc        string
		anyOfNeedle []string
	}{
		{"a build step", []string{"make build", "go build"}},
		{"a test step", []string{"make test", "go test"}},
	}
	for _, req := range requirements {
		matched := false
		for _, needle := range req.anyOfNeedle {
			if strings.Contains(content, needle) {
				matched = true
				break
			}
		}
		if !matched {
			t.Fatalf("ci.yml missing %s (expected one of %v)", req.desc, req.anyOfNeedle)
		}
	}
}

func TestCIWorkflowRunsPinnedAnalysisGates(t *testing.T) {
	root := repoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatalf("read ci.yml: %v", err)
	}
	content := string(raw)
	for _, target := range []string{"make lint", "make vulncheck"} {
		if !strings.Contains(content, target) {
			t.Fatalf("ci.yml must run %q", target)
		}
	}

	goMod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	for _, toolPath := range []string{
		"golang.org/x/vuln/cmd/govulncheck",
		"honnef.co/go/tools/cmd/staticcheck",
	} {
		if !strings.Contains(string(goMod), toolPath) {
			t.Fatalf("go.mod must pin tool %s", toolPath)
		}
	}
}

func TestGitleaksBootstrapIsPinnedAndVerifyBeforeExtract(t *testing.T) {
	root := repoRoot(t)
	want := map[string]string{
		"gitleaks_8.24.2_darwin_arm64.tar.gz": "90d13686937ac7429b97a3acbf1e1d0ce90d92ae2d0cf46a690bd8ae5230bea0",
		"gitleaks_8.24.2_darwin_x64.tar.gz":   "bc3c46f8039ba716ba8461fa6745c9d1cfb90ca2f5f881d8d0cf66b7ba7b742c",
		"gitleaks_8.24.2_linux_arm64.tar.gz":  "574a6d52573c61173add7ddb5e3cc68c0e82cb0735818a1eeb9a0a2de1643fbc",
		"gitleaks_8.24.2_linux_x64.tar.gz":    "fa0500f6b7e41d28791ebc680f5dd9899cd42b58629218a5f041efa899151a8e",
	}
	manifest, err := os.ReadFile(filepath.Join(root, "scripts", "gitleaks-8.24.2-checksums.txt"))
	if err != nil {
		t.Fatalf("read Gitleaks checksums: %v", err)
	}
	got := make(map[string]string)
	for _, line := range strings.Split(strings.TrimSpace(string(manifest)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			t.Fatalf("malformed Gitleaks checksum line: %q", line)
		}
		if _, duplicate := got[fields[1]]; duplicate {
			t.Fatalf("duplicate Gitleaks checksum entry: %s", fields[1])
		}
		got[fields[1]] = fields[0]
	}
	if len(got) != len(want) {
		t.Fatalf("Gitleaks checksum entry count = %d, want %d", len(got), len(want))
	}
	for asset, checksum := range want {
		if got[asset] != checksum {
			t.Errorf("checksum for %s = %q, want %q", asset, got[asset], checksum)
		}
	}

	securityScript, err := os.ReadFile(filepath.Join(root, "scripts", "security-audit.sh"))
	if err != nil {
		t.Fatalf("read security audit: %v", err)
	}
	script := string(securityScript)
	verifyAt := strings.Index(script, "bash scripts/verify-gitleaks-archive.sh")
	extractAt := strings.Index(script, "tar -xzf")
	if verifyAt < 0 || extractAt < 0 || verifyAt >= extractAt {
		t.Fatal("security audit must verify the downloaded Gitleaks archive before extraction")
	}
	logicalScript := strings.ReplaceAll(script, "\\\n", " ")
	if regexp.MustCompile(`(?m)(curl|wget)[^|\n]*\|[ \t]*[^|\n]`).MatchString(logicalScript) {
		t.Fatal("security audit must not pipe a network response into another process")
	}
	if !strings.Contains(script, `gitleaks version 2>/dev/null`) {
		t.Fatal("security audit must only reuse the pinned Gitleaks version")
	}

	workflow, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatalf("read ci.yml: %v", err)
	}
	if !strings.Contains(string(workflow), "make security-audit") {
		t.Fatal("CI must run the shared security-audit bootstrap path")
	}
}

func TestReleaseAuditDetectsNetworkPipelinesAcrossContinuations(t *testing.T) {
	root := repoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, "scripts", "release-audit.sh"))
	if err != nil {
		t.Fatalf("read release audit: %v", err)
	}
	script := string(raw)
	start := strings.Index(script, "has_network_pipeline() {")
	if start < 0 {
		t.Fatal("release audit is missing has_network_pipeline")
	}
	const endMarker = "\n}\n\ntar_contains() {"
	end := strings.Index(script[start:], endMarker)
	if end < 0 {
		t.Fatal("could not isolate has_network_pipeline")
	}
	function := script[start : start+end+2]
	tmp := t.TempDir()
	harness := filepath.Join(tmp, "network-pipeline-check.sh")
	harnessBody := "#!/usr/bin/env bash\nset -euo pipefail\n" + function + "\nhas_network_pipeline \"$1\"\n"
	if err := os.WriteFile(harness, []byte(harnessBody), 0o700); err != nil {
		t.Fatal(err)
	}

	unsafe := map[string]string{
		"same-line":           "curl -fsSL https://example.invalid/tool | tar -xz\n",
		"backslash-continued": "curl -fsSL \\\n  https://example.invalid/tool \\\n  | tar -xz\n",
		"bare-pipe-continued": "curl -fsSL https://example.invalid/tool |\n  tar -xz\n",
	}
	for name, content := range unsafe {
		t.Run(name, func(t *testing.T) {
			fixture := filepath.Join(tmp, name+".sh")
			if err := os.WriteFile(fixture, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if output, err := exec.Command("bash", harness, fixture).CombinedOutput(); err != nil {
				t.Fatalf("unsafe pipeline was not detected: %v\n%s", err, output)
			}
		})
	}

	safe := filepath.Join(tmp, "download-to-file.sh")
	if err := os.WriteFile(safe, []byte("curl -fsSL --output tool.tar.gz https://example.invalid/tool\ntar -xzf tool.tar.gz\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("bash", harness, safe).Run(); err == nil {
		t.Fatal("download-to-file sequence was incorrectly classified as a pipeline")
	}
}

func TestGitleaksArchiveVerifierRejectsMismatch(t *testing.T) {
	root := repoRoot(t)
	tmp := t.TempDir()
	asset := "gitleaks_8.24.2_linux_x64.tar.gz"
	archive := filepath.Join(tmp, asset)
	payload := []byte("not a real archive, only checksum test data")
	if err := os.WriteFile(archive, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(tmp, "checksums.txt")
	if err := os.WriteFile(manifest, []byte(strings.Repeat("0", 64)+"  "+asset+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	verifier := filepath.Join(root, "scripts", "verify-gitleaks-archive.sh")
	output, err := exec.Command("bash", verifier, manifest, asset, archive).CombinedOutput()
	if err == nil {
		t.Fatal("checksum mismatch unexpectedly passed verification")
	}
	if !strings.Contains(string(output), "checksum mismatch") {
		t.Fatalf("mismatch diagnostic = %q, want checksum mismatch", output)
	}

	digest := sha256.Sum256(payload)
	validEntry := fmt.Sprintf("%x  %s\n", digest, asset)
	if err := os.WriteFile(manifest, []byte(validEntry), 0o600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("bash", verifier, manifest, asset, archive).CombinedOutput(); err != nil {
		t.Fatalf("valid checksum rejected: %v\n%s", err, output)
	}
}

func TestCIAuditScriptPresent(t *testing.T) {
	root := repoRoot(t)
	info, err := os.Stat(filepath.Join(root, "scripts", "ci-audit.sh"))
	if err != nil {
		t.Fatalf("scripts/ci-audit.sh must exist: %v", err)
	}
	if info.Mode()&0o111 == 0 {
		t.Fatal("scripts/ci-audit.sh must be executable")
	}
}
