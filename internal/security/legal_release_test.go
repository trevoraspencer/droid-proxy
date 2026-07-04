package security

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var permissiveLicenses = map[string]bool{
	"MIT": true, "Apache-2.0": true, "BSD-2-Clause": true, "BSD-3-Clause": true,
	"ISC": true, "MPL-2.0": true, "Unlicense": true, "0BSD": true,
}

var copyleftLicenses = map[string]bool{
	"GPL-2.0": true, "GPL-2.0-only": true, "GPL-2.0-or-later": true,
	"GPL-3.0": true, "GPL-3.0-only": true, "GPL-3.0-or-later": true,
	"AGPL-3.0": true, "AGPL-3.0-only": true, "AGPL-3.0-or-later": true,
	"LGPL-2.1": true, "LGPL-2.1-only": true, "LGPL-2.1-or-later": true,
	"LGPL-3.0": true, "LGPL-3.0-only": true, "LGPL-3.0-or-later": true,
}

func TestLegalDocumentsPresent(t *testing.T) {
	root := repoRoot(t)
	required := []string{
		"LICENSE",
		"NOTICE",
		"SECURITY.md",
		"CODE_OF_CONDUCT.md",
		"docs/THIRD_PARTY_LICENSES.md",
	}
	for _, rel := range required {
		if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
			t.Fatalf("missing legal document %s: %v", rel, err)
		}
	}
}

func TestLicenseIsMIT(t *testing.T) {
	root := repoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, "LICENSE"))
	if err != nil {
		t.Fatalf("read LICENSE: %v", err)
	}
	content := string(raw)
	if !strings.Contains(content, "MIT License") {
		t.Fatal("LICENSE must be MIT")
	}
	if !strings.Contains(content, "Copyright (c)") {
		t.Fatal("LICENSE must include a copyright line")
	}
	if !strings.Contains(content, "Trevor Spencer") {
		t.Fatal("LICENSE copyright holder must match project owner")
	}
}

func TestREADMEContainsPublicDisclaimer(t *testing.T) {
	root := repoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		t.Fatalf("read README.md: %v", err)
	}
	content := strings.ToLower(string(raw))
	for _, phrase := range []string{
		"not affiliated",
		"independent open-source",
	} {
		if !strings.Contains(content, phrase) {
			t.Fatalf("README.md missing public disclaimer phrase %q", phrase)
		}
	}
}

func TestDirectDependencyLicensesPermissive(t *testing.T) {
	root := repoRoot(t)
	licenses := loadDirectDependencyLicenses(t, root)
	direct := directGoModules(t, root)

	for _, mod := range direct {
		spdx, ok := licenses[mod]
		if !ok {
			t.Errorf("direct dependency %q missing from internal/security/testdata/direct_dependency_licenses.json — add its SPDX ID", mod)
			continue
		}
		if copyleftLicenses[spdx] {
			t.Errorf("direct dependency %q uses copyleft license %q", mod, spdx)
		}
		if !permissiveLicenses[spdx] {
			t.Errorf("direct dependency %q uses unrecognized license %q — review and allowlist if permissive", mod, spdx)
		}
	}

	for mod := range licenses {
		found := false
		for _, directMod := range direct {
			if directMod == mod {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("direct_dependency_licenses.json contains stale module %q not in go.mod direct requires", mod)
		}
	}
}

func TestSecurityPolicyReferencesGitHubAdvisories(t *testing.T) {
	root := repoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, "SECURITY.md"))
	if err != nil {
		t.Fatalf("read SECURITY.md: %v", err)
	}
	if !strings.Contains(string(raw), "security/advisories") {
		t.Fatal("SECURITY.md should direct reporters to GitHub Security Advisories")
	}
}

func TestNoticeReferencesThirdPartyDoc(t *testing.T) {
	root := repoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, "NOTICE"))
	if err != nil {
		t.Fatalf("read NOTICE: %v", err)
	}
	if !strings.Contains(string(raw), "docs/THIRD_PARTY_LICENSES.md") {
		t.Fatal("NOTICE should reference docs/THIRD_PARTY_LICENSES.md")
	}
}

func loadDirectDependencyLicenses(t *testing.T, root string) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, "internal", "security", "testdata", "direct_dependency_licenses.json"))
	if err != nil {
		t.Fatalf("read direct_dependency_licenses.json: %v", err)
	}
	out := map[string]string{}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("parse direct_dependency_licenses.json: %v", err)
	}
	return out
}

func directGoModules(t *testing.T, root string) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	var mods []string
	inBlock := false
	for _, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "//") {
			continue
		}
		if strings.HasPrefix(trimmed, "require (") {
			inBlock = true
			continue
		}
		if inBlock {
			if trimmed == ")" {
				inBlock = false
			} else if !strings.Contains(trimmed, "// indirect") {
				fields := strings.Fields(trimmed)
				if len(fields) >= 1 {
					mods = append(mods, fields[0])
				}
			}
			continue
		}
		if strings.HasPrefix(trimmed, "require ") && !strings.Contains(trimmed, "// indirect") {
			fields := strings.Fields(trimmed)
			if len(fields) >= 2 {
				mods = append(mods, fields[1])
			}
		}
	}
	return mods
}

func TestLegalAuditScriptPresent(t *testing.T) {
	root := repoRoot(t)
	info, err := os.Stat(filepath.Join(root, "scripts", "legal-audit.sh"))
	if err != nil {
		t.Fatalf("scripts/legal-audit.sh must exist: %v", err)
	}
	if info.Mode()&0o111 == 0 {
		t.Fatal("scripts/legal-audit.sh must be executable")
	}
}

func TestReadmeLicenseLinkPresent(t *testing.T) {
	root := repoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		t.Fatalf("read README.md: %v", err)
	}
	if !regexp.MustCompile(`(?i)\[.?LICENSE.?\]\(LICENSE\)`).Match(raw) {
		t.Fatal("README.md should link to LICENSE")
	}
}
