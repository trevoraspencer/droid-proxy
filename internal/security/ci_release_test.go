package security

import (
	"os"
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
