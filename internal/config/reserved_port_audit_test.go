package config

import (
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// reservedPorts are the runtime ports that validation must never bind,
// connect to, probe, reserve, owner-inspect, stop, or select for cleanup.
const reservedPortA = "8787"
const reservedPortB = "9787"

// reservedPortLiteral matches either reserved port as a standalone token.
var reservedPortLiteral = regexp.MustCompile(`\b(8787|9787)\b`)

// gitTrackedFiles returns the set of repository-tracked files.
func gitTrackedFiles(t *testing.T) []string {
	t.Helper()
	out, err := exec.Command("git", "-C", repoRoot(t), "ls-files").CombinedOutput()
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

// readTrackedLines reads a tracked file relative to the repo root and returns
// its lines. It skips files that cannot be read (e.g. binary artifacts).
func readTrackedLines(t *testing.T, rel string) []string {
	t.Helper()
	raw := readRepoRel(t, rel)
	return strings.Split(raw, "\n")
}

// --- VAL-PORT-024: Executable validation never touches reserved ports ---
//
// The following tests implement the automated audit required by VAL-PORT-024.
// They scan tracked repository files for executable network or process actions
// that target port 8787 or 9787. Reserved ports may appear in fixture bytes,
// expected strings, parsed config, source constants, and sanitized diagnostics,
// but must never be the target of a bind, connect, probe, reserve,
// owner-inspect, stop, or cleanup-selected action.

// TestReservedPortAudit_KillPortsExcludesReserved verifies that no shell
// kill/cleanup port array in the repository includes a reserved port.
func TestReservedPortAudit_KillPortsExcludesReserved(t *testing.T) {
	files := gitTrackedFiles(t)

	// Pattern: an array assignment whose name suggests kill/cleanup selection.
	killPortArray := regexp.MustCompile(`(?i)(KILL_PORTS|KILL_PORT|STOP_PORTS|CLEANUP_PORTS)\s*[=(]\s*([^)]*)`)

	for _, rel := range files {
		ext := filepath.Ext(rel)
		if ext != ".sh" && ext != ".zsh" && ext != ".bash" {
			continue
		}
		lines := readTrackedLines(t, rel)
		for i, line := range lines {
			m := killPortArray.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			arrayContents := m[2]
			if reservedPortLiteral.MatchString(arrayContents) {
				t.Fatalf("%s:%d: kill/cleanup port array %s contains a reserved port (8787/9787): %s",
					rel, i+1, m[1], strings.TrimSpace(line))
			}
		}
	}
}

// TestReservedPortAudit_NoPIDOnlyLsofOnReserved verifies that no shell script
// uses lsof with PID-only output flags (-t) against a literal reserved port.
// PID-only lsof (lsof -ti tcp:PORT) is used to find process owners for
// killing. Read-only listing (lsof -nP -iTCP:PORT) is allowed for snapshots.
func TestReservedPortAudit_NoPIDOnlyLsofOnReserved(t *testing.T) {
	files := gitTrackedFiles(t)

	// Match lsof with -t flag (PID-only output) and a literal reserved port.
	// The -t flag produces only PIDs, which is the kill-selection pattern.
	pidOnlyLsof := regexp.MustCompile(`lsof\s+-[a-zA-Z]*t[a-zA-Z]*`)

	for _, rel := range files {
		ext := filepath.Ext(rel)
		if ext != ".sh" && ext != ".zsh" && ext != ".bash" {
			continue
		}
		lines := readTrackedLines(t, rel)
		for i, line := range lines {
			if !pidOnlyLsof.MatchString(line) {
				continue
			}
			// This line has PID-only lsof. Check if a literal reserved port
			// appears on the same line. Variable references ($port) are fine
			// because the variable value is controlled elsewhere (KILL_PORTS).
			if reservedPortLiteral.MatchString(line) {
				t.Fatalf("%s:%d: PID-only lsof (kill selection) with literal reserved port: %s",
					rel, i+1, strings.TrimSpace(line))
			}
		}
	}
}

// TestReservedPortAudit_NoShellCurlOnReserved verifies that no shell script
// uses curl/wget with a literal reserved-port URL. Scripts that use variables
// (e.g. $LIVE_E2E_PROXY_URL) are allowed because the operator overrides the
// port for validation.
func TestReservedPortAudit_NoShellCurlOnReserved(t *testing.T) {
	files := gitTrackedFiles(t)

	curlOrWget := regexp.MustCompile(`\b(curl|wget)\b`)
	reservedURL := regexp.MustCompile(`https?://[^\s"']*:(8787|9787)\b`)

	for _, rel := range files {
		ext := filepath.Ext(rel)
		if ext != ".sh" && ext != ".zsh" && ext != ".bash" {
			continue
		}
		lines := readTrackedLines(t, rel)
		for i, line := range lines {
			if !curlOrWget.MatchString(line) {
				continue
			}
			if reservedURL.MatchString(line) {
				t.Fatalf("%s:%d: curl/wget with literal reserved-port URL: %s",
					rel, i+1, strings.TrimSpace(line))
			}
		}
	}
}

// TestReservedPortAudit_NoGoNetworkActionOnReserved verifies that no Go file
// uses a network function to bind, connect, or dial a literal reserved port.
// This covers net.Listen, net.Dial, http.ListenAndServe, and http client
// methods with literal :8787 or :9787 in the address argument.
func TestReservedPortAudit_NoGoNetworkActionOnReserved(t *testing.T) {
	files := gitTrackedFiles(t)

	// Patterns where a Go network function call and a literal reserved port
	// appear on the same line, indicating the function is called with the
	// reserved port as an argument.
	patterns := []struct {
		name string
		re   *regexp.Regexp
	}{
		{"net.Listen with reserved port",
			regexp.MustCompile(`net\.Listen\([^)]*:(8787|9787)`)},
		{"net.Dial with reserved port",
			regexp.MustCompile(`net\.Dial\([^)]*:(8787|9787)`)},
		{"http.ListenAndServe with reserved port",
			regexp.MustCompile(`ListenAndServe\([^)]*:(8787|9787)`)},
		{"http.Get with reserved port URL",
			regexp.MustCompile(`http\.Get\(\s*"[^"]*:(8787|9787)`)},
		{"http.Post with reserved port URL",
			regexp.MustCompile(`http\.Post\(\s*"[^"]*:(8787|9787)`)},
		{"http.Head with reserved port URL",
			regexp.MustCompile(`http\.Head\(\s*"[^"]*:(8787|9787)`)},
		{"exec.Command curl with reserved port URL",
			regexp.MustCompile(`exec\.Command\(\s*"curl"[^)]*:(8787|9787)`)},
	}

	for _, rel := range files {
		if filepath.Ext(rel) != ".go" {
			continue
		}
		lines := readTrackedLines(t, rel)
		for i, line := range lines {
			for _, p := range patterns {
				if p.re.MatchString(line) {
					t.Fatalf("%s:%d: %s: %s",
						rel, i+1, p.name, strings.TrimSpace(line))
				}
			}
		}
	}
}

// TestReservedPortAudit_LiveE2eCleanupExcludesReserved explicitly verifies the
// live-E2E cleanup pattern: KILL_PORTS must not include either reserved port.
// This is the positive complement to the general kill-ports scan above.
func TestReservedPortAudit_LiveE2eCleanupExcludesReserved(t *testing.T) {
	script := readRepoRel(t, "scripts/live-e2e/01-clean-old-proxies.sh")
	if !strings.Contains(script, "KILL_PORTS=(1455 56121)") {
		t.Fatal("scripts/live-e2e/01-clean-old-proxies.sh must define KILL_PORTS=(1455 56121) " +
			"without any reserved port")
	}
	for _, port := range []string{reservedPortA, reservedPortB} {
		for _, line := range strings.Split(script, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "KILL_PORTS=") &&
				strings.Contains(line, port) {
				t.Fatalf("KILL_PORTS line contains reserved port %s: %s", port, line)
			}
		}
	}
}
