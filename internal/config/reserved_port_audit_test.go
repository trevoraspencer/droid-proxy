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

// --- Executable-action classifier framework ---
//
// The following types and functions provide reusable classifiers that detect
// executable reserved-port actions across different executable file types.
// Both file-scanning tests and focused synthetic tests use these classifiers.

// actionClassifier checks a single line for an executable action targeting a
// reserved port and returns true when the line should be refused.
type actionClassifier struct {
	name  string
	check func(line string) bool
}

// classifyLine returns the name of the first matching classifier, or "" if
// no classifier matches.
func classifyLine(line string, classifiers []actionClassifier) string {
	for _, c := range classifiers {
		if c.check(line) {
			return c.name
		}
	}
	return ""
}

// isMakefile returns true for Makefile, makefile, GNUmakefile, or *.mk files.
func isMakefile(rel string) bool {
	switch filepath.Base(rel) {
	case "Makefile", "makefile", "GNUmakefile":
		return true
	}
	return filepath.Ext(rel) == ".mk"
}

// isPythonFile returns true for *.py files.
func isPythonFile(rel string) bool {
	return filepath.Ext(rel) == ".py"
}

// Precompiled patterns shared across shell-like contexts (shell scripts and
// Makefile recipes) and Python classifiers.
var (
	pidOnlyLsofRe   = regexp.MustCompile(`lsof\s+-[a-zA-Z]*t[a-zA-Z]*`)
	curlOrWgetRe    = regexp.MustCompile(`\b(curl|wget)\b`)
	reservedURLRe   = regexp.MustCompile(`https?://[^\s"']*:(8787|9787)\b`)
	killPortArrayRe = regexp.MustCompile(`(?i)(KILL_PORTS|KILL_PORT|STOP_PORTS|CLEANUP_PORTS)\s*[=(]\s*([^)]*)`)
)

// Precompiled Python-specific patterns.
var (
	pythonSocketRe   = regexp.MustCompile(`\.(connect|bind)\s*\(`)
	pythonNetLibRe   = regexp.MustCompile(`\b(requests|urllib)\b`)
	pythonHTTPConnRe = regexp.MustCompile(`HTTPConnection\s*\(`)
	subprocessCallRe = regexp.MustCompile(`subprocess\.\w+\s*\(`)
	curlOrWgetArgRe  = regexp.MustCompile(`["'](curl|wget)["']`)
	lsofArgRe        = regexp.MustCompile(`["']lsof["']`)
	pythonPidFlagRe  = regexp.MustCompile(`["']\-[a-zA-Z]*t[a-zA-Z]*["']`)
)

// makefileClassifiers detects executable reserved-port actions in Makefile
// recipes. Since Make recipes are shell commands, these patterns mirror the
// shell-script audit: curl/wget URLs, PID-only lsof, and kill-port arrays.
func makefileClassifiers() []actionClassifier {
	return []actionClassifier{
		{
			name: "curl/wget with reserved-port URL",
			check: func(line string) bool {
				return curlOrWgetRe.MatchString(line) && reservedURLRe.MatchString(line)
			},
		},
		{
			name: "PID-only lsof with literal reserved port",
			check: func(line string) bool {
				return pidOnlyLsofRe.MatchString(line) && reservedPortLiteral.MatchString(line)
			},
		},
		{
			name: "kill/cleanup port array with reserved port",
			check: func(line string) bool {
				m := killPortArrayRe.FindStringSubmatch(line)
				if m == nil {
					return false
				}
				return reservedPortLiteral.MatchString(m[2])
			},
		},
	}
}

// pythonClassifiers detects executable reserved-port actions in Python files:
// network/socket operations, HTTP library calls, subprocess curl/wget, and
// subprocess PID-only lsof.
func pythonClassifiers() []actionClassifier {
	return []actionClassifier{
		{
			name: "socket connect/bind with reserved port",
			check: func(line string) bool {
				return pythonSocketRe.MatchString(line) && reservedPortLiteral.MatchString(line)
			},
		},
		{
			name: "requests/urllib with reserved-port URL",
			check: func(line string) bool {
				return pythonNetLibRe.MatchString(line) && reservedURLRe.MatchString(line)
			},
		},
		{
			name: "HTTPConnection with reserved port",
			check: func(line string) bool {
				return pythonHTTPConnRe.MatchString(line) && reservedPortLiteral.MatchString(line)
			},
		},
		{
			name: "subprocess curl/wget with reserved port",
			check: func(line string) bool {
				return subprocessCallRe.MatchString(line) &&
					curlOrWgetArgRe.MatchString(line) &&
					reservedPortLiteral.MatchString(line)
			},
		},
		{
			name: "subprocess PID-only lsof with reserved port",
			check: func(line string) bool {
				return subprocessCallRe.MatchString(line) &&
					lsofArgRe.MatchString(line) &&
					pythonPidFlagRe.MatchString(line) &&
					reservedPortLiteral.MatchString(line)
			},
		},
	}
}

// --- Makefile file-scanning audit ---

// TestReservedPortAudit_MakefileExcludesReserved scans all tracked Makefile,
// makefile, GNUmakefile, and *.mk files for executable actions targeting a
// reserved port. Makefile recipes are shell commands, so the same curl/wget,
// PID-only lsof, and kill-port-array patterns apply.
func TestReservedPortAudit_MakefileExcludesReserved(t *testing.T) {
	files := gitTrackedFiles(t)
	classifiers := makefileClassifiers()

	for _, rel := range files {
		if !isMakefile(rel) {
			continue
		}
		lines := readTrackedLines(t, rel)
		for i, line := range lines {
			if matched := classifyLine(line, classifiers); matched != "" {
				t.Fatalf("%s:%d: %s: %s",
					rel, i+1, matched, strings.TrimSpace(line))
			}
		}
	}
}

// --- Python file-scanning audit ---

// TestReservedPortAudit_PythonExcludesReserved scans all tracked Python files
// for executable network/socket, subprocess curl/wget, and PID-only lsof
// actions targeting a reserved port.
func TestReservedPortAudit_PythonExcludesReserved(t *testing.T) {
	files := gitTrackedFiles(t)
	classifiers := pythonClassifiers()

	for _, rel := range files {
		if !isPythonFile(rel) {
			continue
		}
		lines := readTrackedLines(t, rel)
		for i, line := range lines {
			if matched := classifyLine(line, classifiers); matched != "" {
				t.Fatalf("%s:%d: %s: %s",
					rel, i+1, matched, strings.TrimSpace(line))
			}
		}
	}
}

// --- Synthetic classifier tests ---
//
// These focused tests verify the classifiers independently of the repository's
// tracked files. They ensure the patterns correctly refuse executable actions
// targeting reserved ports and allow fixture/comment-only values.

func TestReservedPortAudit_MakefileClassifierSynthetic(t *testing.T) {
	classifiers := makefileClassifiers()

	refuseCases := []string{
		`	curl http://localhost:8787/health`,
		`	wget http://127.0.0.1:9787/`,
		`	lsof -ti tcp:8787 | xargs kill`,
		`	KILL_PORTS=(8787 9787)`,
		`	STOP_PORTS=9787`,
	}
	allowCases := []string{
		`# Default port is 9787`,
		`DEFAULT_PORT ?= 9787`,
		`	@echo "Proxy on port 9787"`,
		`PROXY_PORT ?= 9787`,
		`	curl $(PROXY_URL)`, // variable-based URL is safe
	}

	for _, line := range refuseCases {
		if matched := classifyLine(line, classifiers); matched == "" {
			t.Errorf("refuse line not caught:\n  %s", line)
		}
	}
	for _, line := range allowCases {
		if matched := classifyLine(line, classifiers); matched != "" {
			t.Errorf("allow line falsely caught by %q:\n  %s", matched, line)
		}
	}
}

func TestReservedPortAudit_PythonClassifierSynthetic(t *testing.T) {
	classifiers := pythonClassifiers()

	refuseCases := []string{
		`sock.connect(("127.0.0.1", 8787))`,
		`s.bind(("0.0.0.0", 9787))`,
		`requests.get("http://localhost:8787/health")`,
		`urllib.request.urlopen("http://127.0.0.1:9787")`,
		`http.client.HTTPConnection("localhost", 8787)`,
		`subprocess.run(["curl", "http://localhost:9787"])`,
		`subprocess.check_output(["wget", "http://127.0.0.1:8787"])`,
		`subprocess.run(["lsof", "-ti", "tcp:8787"])`,
		`subprocess.check_output(["lsof", "-t", "-i", ":9787"])`,
	}
	allowCases := []string{
		`# Default port is 9787`,
		`DEFAULT_PORT = "9787"`,
		`expected_url = "http://127.0.0.1:9787"`,
		`assert port == 9787`,
		`# Connect to port 9787 for testing`,
		`subprocess.run(["curl", proxy_url])`, // variable, no literal port
		`requests.get(proxy_url)`,             // variable, no literal port
	}

	for _, line := range refuseCases {
		if matched := classifyLine(line, classifiers); matched == "" {
			t.Errorf("refuse line not caught:\n  %s", line)
		}
	}
	for _, line := range allowCases {
		if matched := classifyLine(line, classifiers); matched != "" {
			t.Errorf("allow line falsely caught by %q:\n  %s", matched, line)
		}
	}
}
