package security

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func gitLsFiles(t *testing.T, root string) []string {
	t.Helper()
	out, err := exec.Command("git", "-C", root, "ls-files").CombinedOutput()
	if err != nil {
		t.Fatalf("git ls-files: %v\n%s", err, out)
	}
	var files []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			files = append(files, line)
		}
	}
	return files
}

// TestTrackedFilesExcludeSensitivePaths ensures secret-bearing local files
// never appear in the committed tree.
func TestTrackedFilesExcludeSensitivePaths(t *testing.T) {
	root := repoRoot(t)
	files := gitLsFiles(t, root)

	forbidden := regexp.MustCompile(`(?i)(^|/)(\.env$|\.env\.local$|secrets\.env$|config\.yaml$|config\.local\.yaml$|.*\.pem$|.*\.p12$|.*\.pfx$|id_rsa$|\.key$)`)
	allowed := regexp.MustCompile(`(?i)\.env\.local\.example$`)

	for _, rel := range files {
		if allowed.MatchString(rel) {
			continue
		}
		if forbidden.MatchString(rel) {
			t.Errorf("tracked file matches sensitive pattern: %s", rel)
		}
	}
}

// TestGitignoreCoversLocalSecrets verifies gitignore rules for common local
// secret and runtime artifact paths.
func TestGitignoreCoversLocalSecrets(t *testing.T) {
	root := repoRoot(t)
	required := []string{
		"config.yaml",
		"config.local.yaml",
		".env",
		".env.local",
		".factory/validation/",
		"secrets.env",
	}
	for _, path := range required {
		cmd := exec.Command("git", "-C", root, "check-ignore", "-q", path)
		if err := cmd.Run(); err != nil {
			t.Errorf("git check-ignore expected %q to be ignored", path)
		}
	}
}

// TestGitignoreFileContainsRequiredEntries guards against accidental removal of
// critical ignore rules during repo cleanup.
func TestGitignoreFileContainsRequiredEntries(t *testing.T) {
	root := repoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil {
		t.Fatalf("read .gitignore: %v", err)
	}
	content := string(raw)
	for _, needle := range []string{
		"config.yaml",
		".env.local",
		"/.factory/validation/",
		"secrets.env",
	} {
		if !strings.Contains(content, needle) {
			t.Errorf(".gitignore missing required entry %q", needle)
		}
	}
}

// TestNonTestGoSourcesAvoidRawOpenAIKeys scans committed non-test Go sources for
// raw OpenAI-style key literals outside explicit sentinel fixtures.
func TestNonTestGoSourcesAvoidRawOpenAIKeys(t *testing.T) {
	root := repoRoot(t)
	files := gitLsFiles(t, root)

	keyLike := regexp.MustCompile(`sk-[A-Za-z0-9_\-]{16,}`)
	allowedSentinels := map[string]bool{
		"sk-WORKFLOWSECRET123456": true,
	}

	for _, rel := range files {
		if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		for _, match := range keyLike.FindAllString(string(raw), -1) {
			if allowedSentinels[match] {
				continue
			}
			t.Errorf("%s contains sk- literal %q — use test sentinels in *_test.go only", rel, match)
		}
	}
}

// TestGitleaksConfigPresent ensures the pre-public audit config is committed.
func TestGitleaksConfigPresent(t *testing.T) {
	root := repoRoot(t)
	if _, err := os.Stat(filepath.Join(root, ".gitleaks.toml")); err != nil {
		t.Fatalf(".gitleaks.toml must exist for pre-public audits: %v", err)
	}
}

// TestPublicReleasePhase0Artifacts ensures Phase 0 strategy docs and scripts exist.
func TestPublicReleasePhase0Artifacts(t *testing.T) {
	root := repoRoot(t)
	required := []string{
		"docs/PUBLIC_RELEASE.md",
		"scripts/public-release-preflight.sh",
		"scripts/create-public-history.sh",
	}
	for _, rel := range required {
		path := filepath.Join(root, rel)
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("missing Phase 0 artifact %s: %v", rel, err)
		}
		if info.IsDir() {
			t.Fatalf("expected file, got directory: %s", rel)
		}
	}
}
