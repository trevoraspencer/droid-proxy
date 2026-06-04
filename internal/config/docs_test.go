package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func readRepoFile(t *testing.T, parts ...string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(append([]string{repoRoot(t)}, parts...)...))
	if err != nil {
		t.Fatalf("read %s: %v", filepath.Join(parts...), err)
	}
	return string(raw)
}

func readRepoRel(t *testing.T, rel string) string {
	t.Helper()
	return readRepoFile(t, filepath.FromSlash(rel))
}

func docExampleMarkdownFiles() []string {
	return []string{
		"docs/examples/anthropic.md",
		"docs/examples/codex-oauth.md",
		"docs/examples/deepseek.md",
		"docs/examples/fireworks.md",
		"docs/examples/groq.md",
		"docs/examples/kimi.md",
		"docs/examples/local-ollama.md",
		"docs/examples/local-vllm.md",
		"docs/examples/mimo.md",
		"docs/examples/openai.md",
		"docs/examples/xai-oauth.md",
		"docs/examples/xai.md",
		"docs/examples/zai.md",
	}
}

func TestDocsGoVersionMatchesModule(t *testing.T) {
	mod := readRepoFile(t, "go.mod")
	var goVersion string
	for _, line := range strings.Split(mod, "\n") {
		if fields := strings.Fields(line); len(fields) == 2 && fields[0] == "go" {
			goVersion = fields[1]
			break
		}
	}
	if goVersion == "" {
		t.Fatal("go.mod does not declare a Go version")
	}
	readme := readRepoFile(t, "README.md")
	if !strings.Contains(readme, "Go "+goVersion) {
		t.Fatalf("README.md must mention Go %s to match go.mod", goVersion)
	}
	if strings.Contains(readme, "Go 1.22") {
		t.Fatal("README.md still advertises stale Go 1.22 requirement")
	}
}

func TestDocsFactorySettingsUseCurrentSchema(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(repoRoot(t), "docs", "factory-settings", "*.json"))
	if err != nil {
		t.Fatalf("glob factory settings: %v", err)
	}
	for _, file := range files {
		t.Run(filepath.Base(file), func(t *testing.T) {
			raw, err := os.ReadFile(file)
			if err != nil {
				t.Fatalf("read settings: %v", err)
			}
			validateFactorySettingsJSON(t, raw)
		})
	}

	for _, rel := range append([]string{"README.md"}, docExampleMarkdownFiles()...) {
		t.Run(rel, func(t *testing.T) {
			for _, block := range fencedBlocks(readRepoRel(t, rel), "json") {
				if strings.Contains(block, `"customModels"`) {
					validateFactorySettingsJSON(t, []byte(block))
				}
			}
		})
	}
}

func validateFactorySettingsJSON(t *testing.T, raw []byte) {
	t.Helper()
	var doc struct {
		CustomModels []map[string]any `json:"customModels"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("factory settings JSON must parse: %v\n%s", err, raw)
	}
	if len(doc.CustomModels) == 0 {
		t.Fatalf("customModels must contain at least one model: %s", raw)
	}
	required := []string{"model", "displayName", "provider", "baseUrl", "apiKey", "maxOutputTokens"}
	stale := []string{"id", "model_display_name", "base_url", "api_key", "max_tokens", "modelDisplayName", "maxTokens"}
	for i, model := range doc.CustomModels {
		for _, key := range required {
			if _, ok := model[key]; !ok {
				t.Fatalf("customModels[%d] missing current field %q in %s", i, key, raw)
			}
		}
		for _, key := range stale {
			if _, ok := model[key]; ok {
				t.Fatalf("customModels[%d] contains stale field %q in %s", i, key, raw)
			}
		}
		if got := model["baseUrl"]; got != "http://127.0.0.1:8787" {
			t.Fatalf("customModels[%d].baseUrl = %v, want local proxy URL", i, got)
		}
		if key, _ := model["apiKey"].(string); key == "" || strings.Contains(key, "sk-") {
			t.Fatalf("customModels[%d].apiKey must be a safe downstream placeholder, got %q", i, key)
		}
	}
}

func TestDocsProviderRegistrySynchronized(t *testing.T) {
	providers := readRepoFile(t, "docs/PROVIDERS.md")
	rows := map[string][]string{}
	inProviderMatrix := false
	for _, line := range strings.Split(providers, "\n") {
		if strings.HasPrefix(line, "## Provider matrix") {
			inProviderMatrix = true
			continue
		}
		if inProviderMatrix && strings.HasPrefix(line, "## ") {
			break
		}
		if !inProviderMatrix {
			continue
		}
		if !strings.HasPrefix(line, "| `") {
			continue
		}
		cols := strings.Split(line, "|")
		if len(cols) < 6 {
			continue
		}
		name := strings.Trim(cols[1], " `")
		rows[name] = []string{
			strings.TrimSpace(cols[2]),
			strings.TrimSpace(cols[3]),
			strings.TrimSpace(cols[4]),
		}
	}
	if len(rows) != len(knownAuthRegistry) {
		t.Fatalf("docs provider count = %d, registry count = %d; docs rows=%v", len(rows), len(knownAuthRegistry), rows)
	}
	for name, ka := range knownAuthRegistry {
		row, ok := rows[name]
		if !ok {
			t.Fatalf("known_auth %q missing from docs/PROVIDERS.md", name)
		}
		if got := strings.Trim(row[0], "`"); got != ka.BaseURL {
			t.Fatalf("%s docs base URL = %q, want %q", name, got, ka.BaseURL)
		}
		wantEnv := ka.APIKeyEnv
		if ka.NoAuth {
			wantEnv = "none"
		}
		envCell := strings.ToLower(row[1])
		if wantEnv == "none" {
			if !strings.Contains(envCell, "none") || !strings.Contains(envCell, "no-auth") {
				t.Fatalf("%s docs env cell = %q, want no-auth none marker", name, row[1])
			}
		} else if strings.Trim(row[1], "`") != wantEnv {
			t.Fatalf("%s docs env var = %q, want %q", name, row[1], wantEnv)
		}
		if got := strings.Trim(row[2], "`"); got != string(ka.UpstreamProtocol) {
			t.Fatalf("%s docs upstream = %q, want %q", name, got, ka.UpstreamProtocol)
		}
	}
}

func TestDocsExamplesParseAndLoad(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "dummy-deepseek-key")
	cfg, err := Load(filepath.Join(repoRoot(t), "config.example.yaml"))
	if err != nil {
		t.Fatalf("config.example.yaml must load with dummy DeepSeek key: %v", err)
	}
	if len(cfg.Models) == 0 || cfg.Models[0].Alias != "deepseek-v4-flash" {
		t.Fatalf("config.example.yaml should default to current DeepSeek alias, got %+v", cfg.Models)
	}

	for _, rel := range append([]string{
		"README.md",
		"docs/CONFIG.md",
		"docs/PROVIDERS.md",
		"docs/SMOKE.md",
		"docs/OAUTH.md",
		"docs/CLI.md",
	}, docExampleMarkdownFiles()...) {
		t.Run(rel, func(t *testing.T) {
			body := readRepoRel(t, rel)
			for _, block := range fencedBlocks(body, "json") {
				var v any
				if err := json.Unmarshal([]byte(block), &v); err != nil {
					t.Fatalf("JSON fenced block must parse: %v\n%s", err, block)
				}
			}
			for _, block := range fencedBlocks(body, "yaml") {
				var v any
				if err := yaml.Unmarshal([]byte(block), &v); err != nil {
					t.Fatalf("YAML fenced block must parse: %v\n%s", err, block)
				}
			}
		})
	}
}

func TestDocsConfigDocumentsCapsAndServerTimeouts(t *testing.T) {
	body := readRepoFile(t, "docs", "CONFIG.md")
	for _, want := range []string{
		"server",
		"request_body_max_bytes",
		"read_header_timeout",
		"read_timeout",
		"write_timeout",
		"idle_timeout",
		"shutdown_timeout",
		"response_body_max_bytes",
		"error_body_max_bytes",
		"factory_reasoning",
		"0s` opts out",
		"`0` opts out",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("docs/CONFIG.md must document %q", want)
		}
	}
}

func TestDocsDeepSeek2026Guidance(t *testing.T) {
	for _, rel := range []string{"README.md", "config.example.yaml", "docs/factory-settings/generic.json"} {
		body := readRepoRel(t, rel)
		for _, legacy := range []string{"deepseek-chat", "deepseek-reasoner", "DeepSeek V3", "droid-deepseek-v3"} {
			if strings.Contains(body, legacy) {
				t.Fatalf("%s presents legacy DeepSeek name %q in an authoritative default", rel, legacy)
			}
		}
	}
	deepseekDoc := readRepoFile(t, "docs/examples/deepseek.md")
	if !strings.Contains(deepseekDoc, "deepseek-v4-flash") {
		t.Fatal("DeepSeek example must recommend current 2026 model naming")
	}
	if strings.Contains(deepseekDoc, "deepseek-chat") && !strings.Contains(strings.ToLower(deepseekDoc), "legacy") {
		t.Fatal("DeepSeek legacy names must be explicitly labeled as legacy compatibility")
	}
}

func TestDocsDoNotDescribeImplementedTranslatorsAs501(t *testing.T) {
	for _, rel := range []string{"README.md", "docs/PROVIDERS.md"} {
		body := readRepoRel(t, rel)
		for _, bad := range []string{
			"returns 501",
			"HTTP 501",
			"not yet supported in this build",
			"Chat→Responses translator is a roadmap",
			"Chat→Anthropic translator is a roadmap",
		} {
			if strings.Contains(body, bad) {
				t.Fatalf("%s still describes implemented translator path as %q", rel, bad)
			}
		}
	}
}

func TestDocsREADMELinksToDocsIndex(t *testing.T) {
	readme := readRepoFile(t, "README.md")
	if !strings.Contains(readme, "docs/README.md") {
		t.Fatal("README.md must link to docs/README.md documentation hub")
	}
}

func TestDocsCLIDocumented(t *testing.T) {
	cli := readRepoFile(t, "docs", "CLI.md")
	for _, cmd := range []string{
		"config",
		"onboard",
		"start",
		"stop",
		"status",
		"restart",
		"logs",
		"service install",
		"service uninstall",
		"update",
		"auth codex",
		"auth xai",
		"auth status",
		"auth enable",
		"auth disable",
		"auth logout",
		"--env-file",
		"--no-browser",
	} {
		if !strings.Contains(cli, cmd) {
			t.Fatalf("docs/CLI.md must document %q", cmd)
		}
	}
}

func TestDocsConfigDocumentsLayeredEnv(t *testing.T) {
	body := readRepoFile(t, "docs", "CONFIG.md")
	for _, want := range []string{
		"~/.droid-proxy/env",
		"layer",
		".env.local",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("docs/CONFIG.md must document layered env loading (%q)", want)
		}
	}
	if strings.Contains(body, "pick the first existing file") {
		t.Fatal("docs/CONFIG.md still describes stale first-existing env resolution")
	}
	for _, rel := range []string{"docs/CONFIG.md", "docs/CLI.md"} {
		if strings.Contains(readRepoRel(t, rel), ".env.live-e2e.local") {
			t.Fatalf("%s must not document .env.live-e2e.local as a runtime env default", rel)
		}
	}
}

func TestDocsFactoryDocumentsClientAuthSync(t *testing.T) {
	body := readRepoFile(t, "docs", "FACTORY.md")
	for _, want := range []string{
		"droid-proxy config",
		"client_auth",
		"apiKey",
		"env-expanded",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("docs/FACTORY.md must document client_auth Factory sync (%q)", want)
		}
	}
}

func TestDocsExamplePagesExistForKnownAuth(t *testing.T) {
	providers := readRepoFile(t, "docs/PROVIDERS.md")
	wantExample := map[string]string{
		"deepseek":            "examples/deepseek.md",
		"openai":              "examples/openai.md",
		"anthropic":           "examples/anthropic.md",
		"xai":                 "examples/xai.md",
		"kimi":                "examples/kimi.md",
		"groq":                "examples/groq.md",
		"fireworks":           "examples/fireworks.md",
		"zai":                 "examples/zai.md",
		"zai-main-api":        "examples/zai.md",
		"zai-coding-api":      "examples/zai.md",
		"mimo":                "examples/mimo.md",
		"mimo-token-plan-cn":  "examples/mimo.md",
		"mimo-token-plan-sgp": "examples/mimo.md",
		"mimo-token-plan-ams": "examples/mimo.md",
		"ollama":              "examples/local-ollama.md",
		"vllm":                "examples/local-vllm.md",
	}
	for name, example := range wantExample {
		if !strings.Contains(providers, example) {
			t.Fatalf("docs/PROVIDERS.md must link to %s for known_auth %q", example, name)
		}
	}
	for name := range knownAuthRegistry {
		if _, ok := wantExample[name]; !ok {
			t.Fatalf("docs test missing example mapping for known_auth %q", name)
		}
	}
}

func fencedBlocks(markdown, lang string) []string {
	re := regexp.MustCompile("(?sm)^[ \t]*```" + regexp.QuoteMeta(lang) + `[ \t]*\n(.*?)\n[ \t]*` + "```")
	matches := re.FindAllStringSubmatch(markdown, -1)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		block := dedent(strings.Trim(m[1], "\n"))
		if block != "" && !slices.Contains(out, block) {
			out = append(out, block)
		}
	}
	return out
}

func dedent(s string) string {
	lines := strings.Split(s, "\n")
	minIndent := -1
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " \t"))
		if minIndent == -1 || indent < minIndent {
			minIndent = indent
		}
	}
	if minIndent <= 0 {
		return strings.TrimSpace(s)
	}
	for i, line := range lines {
		if len(line) >= minIndent {
			lines[i] = line[minIndent:]
		}
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// VAL-CONFIG-006: Example config and docs describe the same load-balancing contract.

func TestDocsConfigExampleDocumentsLoadBalancing(t *testing.T) {
	body := readRepoRel(t, "config.example.yaml")
	for _, want := range []string{
		"load_balancing",
		"strategy",
		"max_failovers",
		"rate_limit_cooldown",
		"error_cooldown",
		"round-robin",
		"sticky",
		"fill-first",
		"least-connections",
		"random",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("config.example.yaml must mention %q", want)
		}
	}
	// Codex-only scope note must be present.
	if !strings.Contains(body, "Codex") && !strings.Contains(body, "codex") {
		t.Fatal("config.example.yaml load_balancing comment must mention Codex-only scope")
	}
	// xAI must not be claimed to be pooled within the load_balancing section.
	// Explicit exclusion wording like "does not apply to xAI" is fine.
	lbSection := body[strings.Index(body, "load_balancing"):]
	for _, bad := range []string{
		"xAI pool",
		"xAI pooling",
		"xAI load_balancing",
	} {
		if strings.Contains(lbSection, bad) {
			t.Fatalf("config.example.yaml load_balancing section must not claim xAI pooling (found %q)", bad)
		}
	}
}

func TestDocsConfigMDocumentsLoadBalancing(t *testing.T) {
	body := readRepoRel(t, "docs/CONFIG.md")
	for _, want := range []string{
		"oauth.load_balancing",
		"strategy",
		"max_failovers",
		"rate_limit_cooldown",
		"error_cooldown",
		"round-robin",
		"sticky",
		"fill-first",
		"least-connections",
		"random",
		"60s",
		"30s",
		"max_failovers=0",
		"0s",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("docs/CONFIG.md must document %q", want)
		}
	}
	// Must state Codex-only scope.
	if !strings.Contains(body, "Codex") && !strings.Contains(body, "codex") {
		t.Fatal("docs/CONFIG.md oauth.load_balancing section must mention Codex-only scope")
	}
	// Must state xAI is unaffected / single-account.
	if !strings.Contains(body, "xAI") && !strings.Contains(body, "xai") {
		t.Fatal("docs/CONFIG.md oauth.load_balancing section must mention xAI is unaffected")
	}
}

func TestDocsOAuthMentionsLoadBalancingAndCodexPool(t *testing.T) {
	body := readRepoRel(t, "docs/OAUTH.md")
	// Must reference the load_balancing config.
	if !strings.Contains(body, "load_balancing") {
		t.Fatal("docs/OAUTH.md must reference oauth.load_balancing")
	}
	// Must mention Codex account pool.
	if !strings.Contains(body, "pool") && !strings.Contains(body, "Pool") {
		t.Fatal("docs/OAUTH.md must mention the Codex account pool")
	}
	// Must NOT claim xAI pooling.
	for _, bad := range []string{
		"xAI pool",
		"xAI pooling",
		"xAI load_balancing",
		"xAI load-balancing",
		"xAI pool selection",
		"not used for load balancing yet",
	} {
		if strings.Contains(body, bad) {
			t.Fatalf("docs/OAUTH.md must not contain stale/incorrect claim %q", bad)
		}
	}
	// Must NOT contain stale "first valid account" unqualified wording.
	// The only remaining "first valid" references should be xAI-specific.
	for _, block := range fencedBlocks(body, "yaml") {
		if strings.Contains(block, "first valid") {
			t.Fatalf("docs/OAUTH.md YAML blocks must not contain stale 'first valid' wording:\n%s", block)
		}
	}
}

func TestDocsProvidersMentionsCodexPoolNotXaiPool(t *testing.T) {
	body := readRepoRel(t, "docs/PROVIDERS.md")
	// Must mention load_balancing or account pool for Codex.
	if !strings.Contains(body, "load_balancing") && !strings.Contains(body, "account pool") {
		t.Fatal("docs/PROVIDERS.md must reference load_balancing or account pool for Codex")
	}
	// Must NOT claim xAI pooling.
	for _, bad := range []string{
		"xAI pool",
		"xAI pooling",
		"xAI load_balancing",
		"not used for load balancing yet",
	} {
		if strings.Contains(body, bad) {
			t.Fatalf("docs/PROVIDERS.md must not contain stale/incorrect claim %q", bad)
		}
	}
}

func TestDocsCodexOAuthExampleDocumentsLoadBalancing(t *testing.T) {
	body := readRepoRel(t, "docs/examples/codex-oauth.md")
	for _, want := range []string{
		"load_balancing",
		"strategy",
		"round-robin",
		"sticky",
		"max_failovers",
		"rate_limit_cooldown",
		"error_cooldown",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("docs/examples/codex-oauth.md must mention %q", want)
		}
	}
	// Must state Codex-only scope.
	if !strings.Contains(body, "Codex only") && !strings.Contains(body, "Codex-only") {
		t.Fatal("docs/examples/codex-oauth.md must state load balancing is Codex-only")
	}
}

func TestDocsNoStaleQuotaNotUsedForLoadBalancing(t *testing.T) {
	// All docs must not claim quota hints are "not used for load balancing"
	// or contain "yet" qualifiers that suggest future pooling.
	for _, rel := range []string{
		"README.md",
		"docs/README.md",
		"docs/CONFIG.md",
		"docs/OAUTH.md",
		"docs/PROVIDERS.md",
		"docs/CLI.md",
		"docs/SMOKE.md",
		"docs/examples/codex-oauth.md",
		"docs/examples/xai-oauth.md",
		"config.example.yaml",
	} {
		body := readRepoRel(t, rel)
		for _, stale := range []string{
			"not used for load balancing",
			"not used for load-balancing",
			"load balancing yet",
			"pooling yet",
		} {
			if strings.Contains(body, stale) {
				t.Fatalf("%s contains stale wording %q", rel, stale)
			}
		}
	}
}

func TestDocsNoStaleFirstValidOnlyUnpinnedCodex(t *testing.T) {
	// Codex docs must not say unpinned OAuth "always uses the first valid account".
	// xAI docs may still say "first valid" since xAI is single-account.
	codexDocs := []string{
		"docs/OAUTH.md",
		"docs/PROVIDERS.md",
		"docs/examples/codex-oauth.md",
	}
	for _, rel := range codexDocs {
		body := readRepoRel(t, rel)
		// Check for "first valid account" in a Codex context (not xAI).
		// Allow it only in xAI-qualified sentences.
		for _, line := range strings.Split(body, "\n") {
			lower := strings.ToLower(line)
			if strings.Contains(lower, "first valid") {
				// Must be qualified with xAI.
				if !strings.Contains(lower, "xai") {
					t.Fatalf("%s: stale unqualified 'first valid' in Codex context: %q", rel, strings.TrimSpace(line))
				}
			}
		}
	}
}

func TestDocsXaiOAuthDoesNotClaimPooling(t *testing.T) {
	body := readRepoRel(t, "docs/examples/xai-oauth.md")
	for _, bad := range []string{
		"load_balancing",
		"load-balancing",
		"pool",
		"pooling",
		"failover",
	} {
		if strings.Contains(body, bad) {
			t.Fatalf("docs/examples/xai-oauth.md must not reference %q (xAI is single-account)", bad)
		}
	}
}

func TestDocsConfigExamplesParseable(t *testing.T) {
	// Verify config.example.yaml still parses correctly with load_balancing comments.
	t.Setenv("DEEPSEEK_API_KEY", "dummy-deepseek-key")
	cfg, err := Load(filepath.Join(repoRoot(t), "config.example.yaml"))
	if err != nil {
		t.Fatalf("config.example.yaml must load: %v", err)
	}
	// Load-balancing defaults must be applied.
	if cfg.OAuth.LoadBalancing.Strategy != LoadBalancingSticky {
		t.Fatalf("default strategy = %q, want sticky", cfg.OAuth.LoadBalancing.Strategy)
	}
	if cfg.OAuth.LoadBalancing.MaxFailovers != 2 {
		t.Fatalf("default max_failovers = %d, want 2", cfg.OAuth.LoadBalancing.MaxFailovers)
	}
	if cfg.OAuth.LoadBalancing.RateLimitCooldown != 60*time.Second {
		t.Fatalf("default rate_limit_cooldown = %v, want 60s", cfg.OAuth.LoadBalancing.RateLimitCooldown)
	}
	if cfg.OAuth.LoadBalancing.ErrorCooldown != 30*time.Second {
		t.Fatalf("default error_cooldown = %v, want 30s", cfg.OAuth.LoadBalancing.ErrorCooldown)
	}
}

func TestDocsFencedYAMLExamplesParseable(t *testing.T) {
	// All fenced YAML blocks in docs mentioning load_balancing must parse.
	for _, rel := range append([]string{
		"docs/CONFIG.md",
		"docs/OAUTH.md",
		"docs/PROVIDERS.md",
		"docs/examples/codex-oauth.md",
	}, docExampleMarkdownFiles()...) {
		t.Run(rel, func(t *testing.T) {
			body := readRepoRel(t, rel)
			for _, block := range fencedBlocks(body, "yaml") {
				var v any
				if err := yaml.Unmarshal([]byte(block), &v); err != nil {
					t.Fatalf("YAML fenced block must parse in %s: %v\n%s", rel, err, block)
				}
			}
		})
	}
}

// --- VAL-CROSS-002: Donor reference cleanliness ---

func TestDocsNoDonorReferencesOutsideImplementationPlan(t *testing.T) {
	// Programmatic equivalent of the mission donor-denylist gate.
	// Denylist patterns are loaded from a separate file to avoid the test
	// source itself matching the grep.
	denylistFile := filepath.Join(repoRoot(t), "internal", "config", "testdata", "donor_denylist.txt")
	raw, err := os.ReadFile(denylistFile)
	if err != nil {
		t.Fatalf("read denylist: %v", err)
	}
	var denylist []string
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		denylist = append(denylist, line)
	}
	if len(denylist) == 0 {
		t.Fatal("denylist is empty")
	}

	allowedFile := filepath.FromSlash("docs/IMPLEMENTATION_PLAN.md")
	exts := map[string]bool{".go": true, ".md": true, ".yaml": true, ".yml": true}

	pattern := ""
	for i, s := range denylist {
		if i > 0 {
			pattern += "|"
		}
		pattern += regexp.QuoteMeta(s)
	}
	re := regexp.MustCompile("(?i)(" + pattern + ")")

	root := repoRoot(t)
	walkErr := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// Skip .git and vendor directories.
			name := d.Name()
			if name == ".git" || name == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if !exts[filepath.Ext(path)] {
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return rerr
		}
		if rel == allowedFile {
			return nil
		}
		// Skip this test file itself: the denylist patterns appear here
		// as validation strings, not as actual donor references.
		if strings.HasSuffix(rel, "docs_test.go") {
			return nil
		}
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			t.Logf("skip unreadable %s: %v", rel, rerr)
			return nil
		}
		if loc := re.FindIndex(raw); loc != nil {
			line := strings.Count(string(raw[:loc[0]]), "\n") + 1
			t.Fatalf("%s:%d contains denied donor reference %q", rel, line, string(raw[loc[0]:loc[1]]))
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk: %v", walkErr)
	}
}

// --- VAL-CROSS-007: Skipped feature surfaces are absent or rejected ---

func TestDocsDoNotClaimSkippedFeatures(t *testing.T) {
	// Docs must not claim support for session affinity, priority tiers,
	// or add-account mutation endpoints.
	skippedPhrases := []string{
		"session affinity",
		"session_affinity",
		"priority tier",
		"priority_tier",
		"add-account endpoint",
		"add_account endpoint",
	}
	docsToCheck := []string{
		"README.md",
		"docs/README.md",
		"docs/CONFIG.md",
		"docs/OAUTH.md",
		"docs/PROVIDERS.md",
		"docs/CLI.md",
		"docs/SMOKE.md",
		"docs/examples/codex-oauth.md",
		"docs/examples/xai-oauth.md",
		"config.example.yaml",
	}
	for _, rel := range docsToCheck {
		body := readRepoRel(t, rel)
		for _, phrase := range skippedPhrases {
			if strings.Contains(strings.ToLower(body), strings.ToLower(phrase)) {
				t.Fatalf("%s must not claim support for skipped feature %q", rel, phrase)
			}
		}
	}
}

func TestConfigRejectsSessionAffinityKey(t *testing.T) {
	// session_affinity is a skipped feature; strict YAML must reject it.
	_, err := parse([]byte(`
oauth:
  load_balancing:
    session_affinity: true
models:
  - alias: m1
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    base_url: http://127.0.0.1:1/v1
    api_key_env: TEST_KEY
`))
	if err == nil || !strings.Contains(err.Error(), "field session_affinity not found") {
		t.Fatalf("expected strict YAML rejection for session_affinity, got: %v", err)
	}
}

func TestConfigRejectsAddAccountKey(t *testing.T) {
	// add_account is a skipped feature; strict YAML must reject it.
	_, err := parse([]byte(`
oauth:
  load_balancing:
    add_account: true
models:
  - alias: m1
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    base_url: http://127.0.0.1:1/v1
    api_key_env: TEST_KEY
`))
	if err == nil || !strings.Contains(err.Error(), "field add_account not found") {
		t.Fatalf("expected strict YAML rejection for add_account, got: %v", err)
	}
}
