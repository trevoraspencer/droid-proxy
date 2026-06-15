package security

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var markdownLinkRe = regexp.MustCompile(`\[[^\]]*\]\(([^)]+)\)`)

func TestDocsContributorFilesPresent(t *testing.T) {
	root := repoRoot(t)
	required := []string{
		"CONTRIBUTING.md",
		"CHANGELOG.md",
	}
	for _, rel := range required {
		if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
			t.Fatalf("missing contributor doc %s: %v", rel, err)
		}
	}
}

func TestREADMEPublicDocSections(t *testing.T) {
	root := repoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		t.Fatalf("read README.md: %v", err)
	}
	content := string(raw)
	for _, heading := range []string{
		"## Install",
		"## Quickstart",
		"## Troubleshooting",
		"## Disclaimer",
		"## License",
	} {
		if !strings.Contains(content, heading) {
			t.Fatalf("README.md missing section %q", heading)
		}
	}
	if !strings.Contains(content, "CONTRIBUTING.md") {
		t.Fatal("README.md should link to CONTRIBUTING.md")
	}
	if !strings.Contains(content, "CHANGELOG.md") {
		t.Fatal("README.md should link to CHANGELOG.md")
	}
}

func TestDocsRelativeLinksResolve(t *testing.T) {
	root := repoRoot(t)
	var files []string
	err := filepath.WalkDir(filepath.Join(root, "docs"), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if strings.HasSuffix(path, ".md") {
			rel, rerr := filepath.Rel(root, path)
			if rerr != nil {
				return rerr
			}
			files = append(files, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk docs: %v", err)
	}
	files = append(files, "README.md", "CONTRIBUTING.md", "CHANGELOG.md")

	for _, rel := range files {
		raw, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		baseDir := filepath.Dir(rel)
		for _, match := range markdownLinkRe.FindAllStringSubmatch(string(raw), -1) {
			target := strings.TrimSpace(match[1])
			if target == "" ||
				strings.HasPrefix(target, "http://") ||
				strings.HasPrefix(target, "https://") ||
				strings.HasPrefix(target, "mailto:") ||
				strings.HasPrefix(target, "#") {
				continue
			}
			pathPart := target
			if i := strings.Index(pathPart, "#"); i >= 0 {
				pathPart = pathPart[:i]
			}
			if pathPart == "" {
				continue
			}
			resolved := filepath.Clean(filepath.Join(baseDir, filepath.FromSlash(pathPart)))
			if _, err := os.Stat(filepath.Join(root, resolved)); err != nil {
				t.Errorf("%s: broken relative link %q -> %s", rel, target, resolved)
			}
		}
	}
}

func TestChangelogHasUnreleasedSection(t *testing.T) {
	root := repoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, "CHANGELOG.md"))
	if err != nil {
		t.Fatalf("read CHANGELOG.md: %v", err)
	}
	if !strings.Contains(string(raw), "## [Unreleased]") {
		t.Fatal("CHANGELOG.md must have an [Unreleased] section")
	}
}
