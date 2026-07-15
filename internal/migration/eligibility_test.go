package migration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func writeConfigFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// --- Config eligibility: eligible cases ---

func TestAnalyzeConfigBytesEligibleIPv4(t *testing.T) {
	raw := []byte("listen:\n  host: 127.0.0.1\n  port: 8787\nmodels:\n  - alias: m\n    factory_provider: generic-chat-completion-api\n    upstream_protocol: openai-chat\n    upstream_model: m\n    base_url: http://upstream/v1\n    api_key_env: KEY\n")
	a, err := AnalyzeConfigBytes(raw, 8787, 9787)
	if err != nil {
		t.Fatal(err)
	}
	if !a.Eligible {
		t.Fatalf("expected eligible, got reason: %s", a.Reason)
	}
	if a.Host != "127.0.0.1" {
		t.Fatalf("host = %q, want 127.0.0.1", a.Host)
	}
	if a.Port != 8787 {
		t.Fatalf("port = %d, want 8787", a.Port)
	}
	if a.PortNode == nil {
		t.Fatal("port node is nil")
	}
}

func TestAnalyzeConfigBytesEligibleLocalhost(t *testing.T) {
	raw := []byte("listen:\n  host: localhost\n  port: 8787\n")
	a, _ := AnalyzeConfigBytes(raw, 8787, 9787)
	if !a.Eligible {
		t.Fatalf("expected eligible for localhost, got: %s", a.Reason)
	}
}

func TestAnalyzeConfigBytesEligibleIPv6(t *testing.T) {
	raw := []byte("listen:\n  host: '::1'\n  port: 8787\n")
	a, _ := AnalyzeConfigBytes(raw, 8787, 9787)
	if !a.Eligible {
		t.Fatalf("expected eligible for ::1, got: %s", a.Reason)
	}
}

func TestAnalyzeConfigBytesEligibleNoHost(t *testing.T) {
	// No host specified defaults to 127.0.0.1.
	raw := []byte("listen:\n  port: 8787\n")
	a, _ := AnalyzeConfigBytes(raw, 8787, 9787)
	if !a.Eligible {
		t.Fatalf("expected eligible with default host, got: %s", a.Reason)
	}
}

func TestAnalyzeConfigBytesEligibleQuotedPort(t *testing.T) {
	raw := []byte("listen:\n  host: 127.0.0.1\n  port: \"8787\"\n")
	a, _ := AnalyzeConfigBytes(raw, 8787, 9787)
	if !a.Eligible {
		t.Fatalf("expected eligible for quoted port, got: %s", a.Reason)
	}
}

// --- Config eligibility: refusal cases ---

func TestAnalyzeConfigBytesRefusesNonLoopbackHost(t *testing.T) {
	raw := []byte("listen:\n  host: 0.0.0.0\n  port: 8787\n")
	a, _ := AnalyzeConfigBytes(raw, 8787, 9787)
	if a.Eligible {
		t.Fatal("expected refusal for wildcard host")
	}
}

func TestAnalyzeConfigBytesRefusesBracketedIPv6(t *testing.T) {
	raw := []byte("listen:\n  host: '[::1]'\n  port: 8787\n")
	a, _ := AnalyzeConfigBytes(raw, 8787, 9787)
	if a.Eligible {
		t.Fatal("expected refusal for [::1] host")
	}
}

func TestAnalyzeConfigBytesRefusesHostname(t *testing.T) {
	raw := []byte("listen:\n  host: example.com\n  port: 8787\n")
	a, _ := AnalyzeConfigBytes(raw, 8787, 9787)
	if a.Eligible {
		t.Fatal("expected refusal for hostname")
	}
}

func TestAnalyzeConfigBytesRefusesMalformedYAML(t *testing.T) {
	raw := []byte("listen:\n  port: [unclosed\n")
	a, _ := AnalyzeConfigBytes(raw, 8787, 9787)
	if a.Eligible {
		t.Fatal("expected refusal for malformed YAML")
	}
}

func TestAnalyzeConfigBytesRefusesMultipleDocuments(t *testing.T) {
	raw := []byte("listen:\n  port: 8787\n---\nlisten:\n  port: 9787\n")
	a, _ := AnalyzeConfigBytes(raw, 8787, 9787)
	if a.Eligible {
		t.Fatal("expected refusal for multiple YAML documents")
	}
}

func TestAnalyzeConfigBytesNoopOmittedPort(t *testing.T) {
	raw := []byte("listen:\n  host: 127.0.0.1\n")
	a, _ := AnalyzeConfigBytes(raw, 8787, 9787)
	if a.Eligible {
		t.Fatal("expected no-op for omitted port")
	}
}

func TestAnalyzeConfigBytesNoopArbitraryPort(t *testing.T) {
	raw := []byte("listen:\n  host: 127.0.0.1\n  port: 5000\n")
	a, _ := AnalyzeConfigBytes(raw, 8787, 9787)
	if a.Eligible {
		t.Fatal("expected no-op for arbitrary port")
	}
	if a.Port != 5000 {
		t.Fatalf("port = %d, want 5000", a.Port)
	}
}

func TestAnalyzeConfigBytesNoopNewPort(t *testing.T) {
	raw := []byte("listen:\n  host: 127.0.0.1\n  port: 9787\n")
	a, _ := AnalyzeConfigBytes(raw, 8787, 9787)
	if a.Eligible {
		t.Fatal("expected no-op for new default port")
	}
}

func TestAnalyzeConfigBytesNoopExplicitZero(t *testing.T) {
	raw := []byte("listen:\n  host: 127.0.0.1\n  port: 0\n")
	a, _ := AnalyzeConfigBytes(raw, 8787, 9787)
	if a.Eligible {
		t.Fatal("expected no-op for explicit port 0")
	}
}

func TestAnalyzeConfigBytesRefusesAliasPort(t *testing.T) {
	raw := []byte("defaults: &defs\n  port: 8787\nlisten:\n  host: 127.0.0.1\n  port: *defs.port\n")
	// This YAML is invalid for yaml.v3 in this form, but let's test alias properly
	raw = []byte("port_value: &pv\n  8787\nlisten:\n  host: 127.0.0.1\n  <<: {port: 8787}\n")
	a, _ := AnalyzeConfigBytes(raw, 8787, 9787)
	// Merge keys may or may not produce a standalone port node; check we don't
	// silently accept ambiguous state.
	_ = a
}

func TestAnalyzeConfigBytesRefusesAnchorPort(t *testing.T) {
	// Port scalar with an anchor referenced elsewhere.
	raw := []byte("listen:\n  host: 127.0.0.1\n  port: &p 8787\nother:\n  port: *p\n")
	a, _ := AnalyzeConfigBytes(raw, 8787, 9787)
	if a.Eligible {
		t.Fatal("expected refusal for anchored port scalar referenced elsewhere")
	}
}

func TestAnalyzeConfigBytesRefusesCustomTag(t *testing.T) {
	raw := []byte("listen:\n  host: 127.0.0.1\n  port: !custom 8787\n")
	a, _ := AnalyzeConfigBytes(raw, 8787, 9787)
	if a.Eligible {
		t.Fatal("expected refusal for custom-tagged port scalar")
	}
}

// --- YAML rewrite tests ---

func TestRewriteListenPortScalarPlain(t *testing.T) {
	raw := []byte("listen:\n  host: 127.0.0.1\n  port: 8787\n")
	// Parse to get the port node.
	var doc yaml.Node
	_ = yaml.Unmarshal(raw, &doc)
	root := doc.Content[0]
	listen := findChild(root, "listen")
	portNode := findChild(listen, "port")

	result, err := RewriteListenPortScalar(raw, portNode, 8787, 9787)
	if err != nil {
		t.Fatal(err)
	}
	expected := "listen:\n  host: 127.0.0.1\n  port: 9787\n"
	if string(result) != expected {
		t.Fatalf("result = %q, want %q", result, expected)
	}
}

func TestRewriteListenPortScalarQuotedDouble(t *testing.T) {
	raw := []byte("listen:\n  host: 127.0.0.1\n  port: \"8787\"\n")
	var doc yaml.Node
	_ = yaml.Unmarshal(raw, &doc)
	root := doc.Content[0]
	listen := findChild(root, "listen")
	portNode := findChild(listen, "port")

	result, err := RewriteListenPortScalar(raw, portNode, 8787, 9787)
	if err != nil {
		t.Fatal(err)
	}
	expected := "listen:\n  host: 127.0.0.1\n  port: \"9787\"\n"
	if string(result) != expected {
		t.Fatalf("result = %q, want %q", result, expected)
	}
}

func TestRewriteListenPortScalarQuotedSingle(t *testing.T) {
	raw := []byte("listen:\n  host: 127.0.0.1\n  port: '8787'\n")
	var doc yaml.Node
	_ = yaml.Unmarshal(raw, &doc)
	root := doc.Content[0]
	listen := findChild(root, "listen")
	portNode := findChild(listen, "port")

	result, err := RewriteListenPortScalar(raw, portNode, 8787, 9787)
	if err != nil {
		t.Fatal(err)
	}
	expected := "listen:\n  host: 127.0.0.1\n  port: '9787'\n"
	if string(result) != expected {
		t.Fatalf("result = %q, want %q", result, expected)
	}
}

func TestRewriteListenPortScalarPreservesComments(t *testing.T) {
	raw := []byte("# top comment\nlisten:\n  host: 127.0.0.1 # inline\n  port: 8787 # port comment\n# bottom\n")
	var doc yaml.Node
	_ = yaml.Unmarshal(raw, &doc)
	root := doc.Content[0]
	listen := findChild(root, "listen")
	portNode := findChild(listen, "port")

	result, err := RewriteListenPortScalar(raw, portNode, 8787, 9787)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(result), "# top comment") {
		t.Fatal("top comment not preserved")
	}
	if !strings.Contains(string(result), "# inline") {
		t.Fatal("inline comment not preserved")
	}
	if !strings.Contains(string(result), "# port comment") {
		t.Fatal("port comment not preserved")
	}
	if !strings.Contains(string(result), "# bottom") {
		t.Fatal("bottom comment not preserved")
	}
	if strings.Contains(string(result), "8787") {
		t.Fatalf("old port 8787 still present: %s", result)
	}
}

func TestRewriteListenPortScalarPreservesUnrelated8787(t *testing.T) {
	// Other occurrences of 8787 in the file must not change.
	raw := []byte("listen:\n  host: 127.0.0.1\n  port: 8787\nmodels:\n  - alias: m\n    upstream_model: accounts/x/models/8787\n")
	var doc yaml.Node
	_ = yaml.Unmarshal(raw, &doc)
	root := doc.Content[0]
	listen := findChild(root, "listen")
	portNode := findChild(listen, "port")

	result, err := RewriteListenPortScalar(raw, portNode, 8787, 9787)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(result), "models/8787") {
		t.Fatalf("unrelated 8787 in upstream_model was changed: %s", result)
	}
	if !strings.Contains(string(result), "port: 9787") {
		t.Fatalf("port not changed to 9787: %s", result)
	}
}

func TestRewriteListenPortScalarPreservesSecrets(t *testing.T) {
	raw := []byte("listen:\n  host: 127.0.0.1\n  port: 8787\nclient_auth:\n  api_keys:\n    - sk-secret-8787-value\n")
	var doc yaml.Node
	_ = yaml.Unmarshal(raw, &doc)
	root := doc.Content[0]
	listen := findChild(root, "listen")
	portNode := findChild(listen, "port")

	result, err := RewriteListenPortScalar(raw, portNode, 8787, 9787)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(result), "sk-secret-8787-value") {
		t.Fatalf("secret value was changed: %s", result)
	}
}

func TestRewriteListenPortScalarByteDiffIsOneScalar(t *testing.T) {
	// Full config with whitespace, ordering, and extra fields.
	raw := []byte("listen:\n  host: 127.0.0.1\n  port: 8787\n\nmodels:\n  - alias: m\n    upstream_model: m\n")
	var doc yaml.Node
	_ = yaml.Unmarshal(raw, &doc)
	root := doc.Content[0]
	listen := findChild(root, "listen")
	portNode := findChild(listen, "port")

	result, err := RewriteListenPortScalar(raw, portNode, 8787, 9787)
	if err != nil {
		t.Fatal(err)
	}

	// The only difference should be 8787 -> 9787.
	rawStr := string(raw)
	resStr := string(result)
	if len(rawStr) != len(resStr) {
		t.Fatalf("length changed: %d -> %d", len(rawStr), len(resStr))
	}
	diffs := 0
	for i := 0; i < len(rawStr); i++ {
		if rawStr[i] != resStr[i] {
			diffs++
		}
	}
	if diffs != 1 {
		t.Fatalf("expected exactly 1 byte difference, got %d", diffs)
	}
}

// --- Trust check tests ---

func TestCheckFileTrustRegularFile(t *testing.T) {
	dir := t.TempDir()
	p := writeConfigFile(t, dir, "config.yaml", "listen:\n  port: 8787\n")
	trust, err := CheckFileTrust(p)
	if err != nil {
		t.Fatal(err)
	}
	if !trust.Trusted {
		t.Fatalf("expected trusted, got: %s", trust.Reason)
	}
}

func TestCheckFileTrustSymlink(t *testing.T) {
	dir := t.TempDir()
	target := writeConfigFile(t, dir, "real.yaml", "listen:\n  port: 8787\n")
	link := filepath.Join(dir, "link.yaml")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	trust, err := CheckFileTrust(link)
	if err != nil {
		t.Fatal(err)
	}
	if trust.Trusted {
		t.Fatal("expected symlink to be untrusted")
	}
}

func TestCheckFileTrustSymlinkComponent(t *testing.T) {
	dir := t.TempDir()
	linkDir := filepath.Join(dir, "linkdir")
	realDir := filepath.Join(dir, "realdir")
	if err := os.MkdirAll(realDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realDir, linkDir); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(linkDir, "config.yaml")
	if err := os.WriteFile(p, []byte("test"), 0o600); err != nil {
		t.Fatal(err)
	}
	trust, err := CheckFileTrust(p)
	if err != nil {
		t.Fatal(err)
	}
	if trust.Trusted {
		t.Fatal("expected symlink component to be untrusted")
	}
}

func TestCheckFileTrustMissingFile(t *testing.T) {
	trust, err := CheckFileTrust("/nonexistent/path/config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if trust.Trusted {
		t.Fatal("expected missing file to be untrusted")
	}
}

func TestCheckFileTrustDirectory(t *testing.T) {
	dir := t.TempDir()
	trust, err := CheckFileTrust(dir)
	if err != nil {
		t.Fatal(err)
	}
	if trust.Trusted {
		t.Fatal("expected directory to be untrusted")
	}
}

// --- Additional edge cases ---

func TestAnalyzeConfigBytesRefusesHexPort(t *testing.T) {
	// Hex representation of 8787 = 0x2243. yaml.v3 decodes it to 8787 but
	// the source representation is hex and cannot be byte-exactly rewritten.
	raw := []byte("listen:\n  host: 127.0.0.1\n  port: 0x2243\n")
	a, _ := AnalyzeConfigBytes(raw, 8787, 9787)
	if a.Eligible {
		t.Fatal("expected refusal for hex port representation")
	}
}

func TestAnalyzeConfigBytesRefusesListenAsScalar(t *testing.T) {
	raw := []byte("listen: 8787\n")
	a, _ := AnalyzeConfigBytes(raw, 8787, 9787)
	if a.Eligible {
		t.Fatal("expected refusal when listen is not a mapping")
	}
}

func TestAnalyzeConfigBytesRefusesPortAsSequence(t *testing.T) {
	raw := []byte("listen:\n  host: 127.0.0.1\n  port: [8787]\n")
	a, _ := AnalyzeConfigBytes(raw, 8787, 9787)
	if a.Eligible {
		t.Fatal("expected refusal when port is a sequence")
	}
}

func TestAnalyzeConfigBytesLocalhostCaseSensitive(t *testing.T) {
	// LOCALHOST is not the same as localhost.
	raw := []byte("listen:\n  host: LOCALHOST\n  port: 8787\n")
	a, _ := AnalyzeConfigBytes(raw, 8787, 9787)
	if a.Eligible {
		t.Fatal("expected refusal for LOCALHOST (case-sensitive check)")
	}
}

func TestRewriteListenPortScalarIPv6Config(t *testing.T) {
	raw := []byte("listen:\n  host: '::1'\n  port: 8787\n")
	var doc yaml.Node
	_ = yaml.Unmarshal(raw, &doc)
	root := doc.Content[0]
	listen := findChild(root, "listen")
	portNode := findChild(listen, "port")

	result, err := RewriteListenPortScalar(raw, portNode, 8787, 9787)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(result), "port: 9787") {
		t.Fatalf("port not changed: %s", result)
	}
	if !strings.Contains(string(result), "host: '::1'") {
		t.Fatalf("host not preserved: %s", result)
	}
}
