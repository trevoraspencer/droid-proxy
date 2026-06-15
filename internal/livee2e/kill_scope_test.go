package livee2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// selectScriptPath resolves scripts/live-e2e/select-proxy-kills.zsh relative to
// this test file (internal/livee2e/ → repo root → scripts/live-e2e/).
func selectScriptPath(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("..", "..", "scripts", "live-e2e", "select-proxy-kills.zsh"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("selector not found at %s: %v", p, err)
	}
	return p
}

// runSelect pipes a fixture process table through the selector and returns the
// emitted PIDs.
func runSelect(t *testing.T, zshBin, script, table string, env ...string) []string {
	t.Helper()
	cmd := exec.Command(zshBin, script)
	cmd.Stdin = strings.NewReader(table)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.Output()
	if err != nil {
		stderr := ""
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = string(ee.Stderr)
		}
		t.Fatalf("selector failed: %v\nstderr: %s", err, stderr)
	}
	var pids []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			pids = append(pids, line)
		}
	}
	return pids
}

func contains(pids []string, want string) bool {
	for _, p := range pids {
		if p == want {
			return true
		}
	}
	return false
}

// TestProxyKillSelectionScope verifies the scoped cleanup selection: real proxy
// binaries (by executable basename) and proxy-port owners ARE selected, while a
// process matched only by a repo-path/argv substring is NOT, and excluded PIDs
// (current shell + ancestors) are never selected.
func TestProxyKillSelectionScope(t *testing.T) {
	zshBin, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("zsh not on PATH; skipping kill-scope test")
	}
	script := selectScriptPath(t)

	// pid \t comm \t args
	table := strings.Join([]string{
		"4242\tvim\tvim /Users/x/code/droid-proxy/foo.go",                          // unrelated: repo path only in argv
		"5555\t/Users/x/code/droid-proxy/droid-proxy\tdroid-proxy --config c.yaml", // real proxy binary (full path comm)
		"6001\tbash\tbash -c tail -f droid-proxy.log",                              // unrelated: pattern only in argv
		"7000\tcursor-proxy\tcursor-proxy serve",                                   // real proxy binary (bare basename)
		"9999\tsomeproc\tsomeproc",                                                 // proxy-port owner, non-proxy name
		"1234\tdroid-proxy\tdroid-proxy --config x",                                // excluded (simulated self)
	}, "\n") + "\n"

	pids := runSelect(t, zshBin, script, table,
		"PROXY_PORT_OWNER_PIDS=9999",
		"PROXY_EXCLUDE_PIDS=1234",
	)

	if contains(pids, "4242") {
		t.Fatalf("vim with repo path in argv was selected (must not be): %v", pids)
	}
	if contains(pids, "6001") {
		t.Fatalf("process matched only by argv substring was selected (must not be): %v", pids)
	}
	if !contains(pids, "5555") {
		t.Fatalf("real droid-proxy binary (by basename) not selected: %v", pids)
	}
	if !contains(pids, "7000") {
		t.Fatalf("real cursor-proxy binary (by basename) not selected: %v", pids)
	}
	if !contains(pids, "9999") {
		t.Fatalf("proxy-port owner not selected: %v", pids)
	}
	if contains(pids, "1234") {
		t.Fatalf("excluded (self) pid was selected: %v", pids)
	}
}

// TestProxyKillSelectionExcludesSelf verifies that a PID in the exclude set is
// never selected even when it would otherwise match both selection signals
// (proxy binary basename AND port ownership). This is the mechanism the wrapper
// uses to guarantee the current shell ($$) and its ancestors are never killed.
func TestProxyKillSelectionExcludesSelf(t *testing.T) {
	zshBin, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("zsh not on PATH; skipping kill-scope test")
	}
	script := selectScriptPath(t)

	self := strconv.Itoa(os.Getpid())
	table := self + "\tdroid-proxy\tdroid-proxy --config x\n"
	pids := runSelect(t, zshBin, script, table,
		"PROXY_PORT_OWNER_PIDS="+self,
		"PROXY_EXCLUDE_PIDS="+self,
	)
	if contains(pids, self) {
		t.Fatalf("self pid %s was selected despite exclusion: %v", self, pids)
	}
	if len(pids) != 0 {
		t.Fatalf("expected no selections, got %v", pids)
	}
}
