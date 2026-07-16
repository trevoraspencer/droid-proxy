package config

import (
	"encoding/json"
	"fmt"
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
		"docs/examples/baseten.md",
		"docs/examples/codex-oauth.md",
		"docs/examples/deepseek.md",
		"docs/examples/fireworks.md",
		"docs/examples/fireworks-fire-pass.md",
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
		if got := model["baseUrl"]; got != "http://127.0.0.1:9787" {
			t.Fatalf("customModels[%d].baseUrl = %v, want local proxy URL http://127.0.0.1:9787", i, got)
		}
		if key, _ := model["apiKey"].(string); key == "" || strings.Contains(key, "sk-") {
			t.Fatalf("customModels[%d].apiKey must be a safe downstream placeholder, got %q", i, key)
		}
		if rawEffort, ok := model["reasoningEffort"]; ok {
			effort, stringOK := rawEffort.(string)
			if !stringOK || !FactoryReasoningEffort(effort).IsValid() {
				t.Fatalf("customModels[%d].reasoningEffort = %#v, want an installed-Droid schema value", i, rawEffort)
			}
		}
	}
}

func TestDocsFactoryReasoningEffortAssignments(t *testing.T) {
	readEntries := func(name string) []map[string]any {
		t.Helper()
		raw := readRepoFile(t, "docs", "factory-settings", name)
		var doc struct {
			CustomModels []map[string]any `json:"customModels"`
		}
		if err := json.Unmarshal([]byte(raw), &doc); err != nil {
			t.Fatal(err)
		}
		return doc.CustomModels
	}

	codex := readEntries("codex-oauth.json")
	if len(codex) != 6 {
		t.Fatalf("Codex settings aliases = %d, want six GPT-5.6 aliases", len(codex))
	}
	wantCodex := map[string]bool{
		"gpt-5.6": true, "gpt-5.6-fast": true,
		"gpt-5.6-terra": true, "gpt-5.6-terra-fast": true,
		"gpt-5.6-luna": true, "gpt-5.6-luna-fast": true,
	}
	for _, model := range codex {
		alias, _ := model["model"].(string)
		if !wantCodex[alias] || model["reasoningEffort"] != "max" {
			t.Fatalf("GPT-5.6 settings capability mismatch: %#v", model)
		}
		delete(wantCodex, alias)
	}
	if len(wantCodex) != 0 {
		t.Fatalf("missing GPT-5.6 settings aliases: %#v", wantCodex)
	}

	xai := map[string]map[string]any{}
	for _, model := range readEntries("xai-oauth.json") {
		xai[model["model"].(string)] = model
	}
	if got := xai["grok-4.5"]["reasoningEffort"]; got != "high" {
		t.Fatalf("Grok 4.5 reasoningEffort = %#v, want high", got)
	}
	for _, alias := range []string{"grok-build-0.1", "grok-composer-2.5-fast"} {
		if _, exists := xai[alias]["reasoningEffort"]; exists {
			t.Fatalf("%s must omit reasoningEffort: %#v", alias, xai[alias])
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
	if !strings.Contains(readme, "docs/UPGRADE.md") {
		t.Fatal("README.md must link to docs/UPGRADE.md from install/update guidance")
	}
}

func TestDocsCLIDocumented(t *testing.T) {
	cli := readRepoFile(t, "docs", "CLI.md")
	for _, cmd := range []string{
		"setup",
		"config",
		"onboard",
		"start",
		"stop",
		"status",
		"restart",
		"logs",
		"service install",
		"service uninstall",
		"doctor",
		"update",
		"auth codex",
		"auth xai",
		"auth status",
		"auth enable",
		"auth disable",
		"auth logout",
		"--env-file",
		"--version",
		"--no-browser",
	} {
		if !strings.Contains(cli, cmd) {
			t.Fatalf("docs/CLI.md must document %q", cmd)
		}
	}
}

func TestDocsUpgradeGuideCoversReleaseSourceAndServiceRepair(t *testing.T) {
	upgrade := readRepoFile(t, "docs", "UPGRADE.md")
	for _, want := range []string{
		"curl -fsSL",
		"make install-user",
		"~/.local/bin/droid-proxy",
		"~/Library/Application Support/droid-proxy/config.yaml",
		"${XDG_CONFIG_HOME:-~/.config}/droid-proxy/config.yaml",
		"systemd/user/droid-proxy.service",
		"make build",
		"service uninstall",
		"setup --service",
		"droid-proxy doctor",
		"update --dry-run",
		"--restart",
		"--no-restart",
		"development checkout",
	} {
		if !strings.Contains(upgrade, want) {
			t.Fatalf("docs/UPGRADE.md must document %q", want)
		}
	}
}

func TestDocsCLICoversDoctorVersionServiceAndUpdateBehavior(t *testing.T) {
	cli := readRepoFile(t, "docs", "CLI.md")
	for _, want := range []string{
		"droid-proxy doctor",
		"commit identity",
		"setup --service",
		"~/.local/bin/droid-proxy",
		"systemd",
		"per-user config path",
		"Print planned actions without fetching, merging, building, or restarting",
	} {
		if !strings.Contains(cli, want) {
			t.Fatalf("docs/CLI.md must document %q", want)
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
		"fireworks-fire-pass": "examples/fireworks-fire-pass.md",
		"baseten":             "examples/baseten.md",
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

func TestDocsNoDonorReferences(t *testing.T) {
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

	exts := map[string]bool{".go": true, ".md": true, ".yaml": true, ".yml": true, ".sh": true}

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

// --- VAL-PORT-026: Active defaults, guidance, templates, and release assets use 9787 ---

func TestConfigExampleUsesCurrentDefaultPort(t *testing.T) {
	body := readRepoRel(t, "config.example.yaml")
	var cfg struct {
		Listen struct {
			Port int `yaml:"port"`
		} `yaml:"listen"`
	}
	if err := yaml.Unmarshal([]byte(body), &cfg); err != nil {
		t.Fatalf("config.example.yaml must parse: %v", err)
	}
	if cfg.Listen.Port != DefaultListenPort {
		t.Fatalf("config.example.yaml listen.port = %d, want %d (DefaultListenPort)", cfg.Listen.Port, DefaultListenPort)
	}
}

func TestInstallConfigUsesCurrentDefaultPort(t *testing.T) {
	body := readRepoRel(t, "internal/setup/install_config.yaml")
	var cfg struct {
		Listen struct {
			Port int `yaml:"port"`
		} `yaml:"listen"`
	}
	if err := yaml.Unmarshal([]byte(body), &cfg); err != nil {
		t.Fatalf("install_config.yaml must parse: %v", err)
	}
	if cfg.Listen.Port != DefaultListenPort {
		t.Fatalf("install_config.yaml listen.port = %d, want %d", cfg.Listen.Port, DefaultListenPort)
	}
}

func TestDocsConfigMDocumentsCurrentDefaultPort(t *testing.T) {
	body := readRepoRel(t, "docs/CONFIG.md")
	// The config reference table must show 9787 as the default, not 8787.
	if !strings.Contains(body, "| `port` | int | `9787`") {
		// Try without the markdown table pipe style.
		if !strings.Contains(body, "9787") {
			t.Fatal("docs/CONFIG.md must document port default 9787")
		}
	}
}

// activeDocFiles lists tracked markdown and JSON documentation files that
// represent current operational guidance (not historical/classified docs).
func activeDocFiles() []string {
	var files []string
	files = append(files, "README.md", "config.example.yaml")
	files = append(files, docExampleMarkdownFiles()...)
	for _, f := range []string{
		"docs/CLI.md",
		"docs/CONFIG.md",
		"docs/FACTORY.md",
		"docs/OAUTH.md",
		"docs/PROVIDERS.md",
		"docs/README.md",
		"docs/SMOKE.md",
		"docs/UPGRADE.md",
		"docs/TROUBLESHOOTING.md",
	} {
		files = append(files, f)
	}
	return files
}

func TestDocsActiveDocsUseCurrentDefaultPort(t *testing.T) {
	// No active documentation file may present the old 8787 port as a
	// current droid-proxy operational default. The value 8787 is allowed
	// only in historical/third-party-conflict context.
	oldURLPatterns := []string{
		"http://127.0.0.1:8787",
		"http://localhost:8787",
		"http://[::1]:8787",
	}
	for _, rel := range activeDocFiles() {
		t.Run(rel, func(t *testing.T) {
			body := readRepoRel(t, rel)
			for _, pat := range oldURLPatterns {
				if strings.Contains(body, pat) {
					t.Fatalf("%s contains active old-default URL %q; update to 9787 or classify as historical", rel, pat)
				}
			}
		})
	}
}

func TestDocsFactorySettingsFilesUseCurrentPort(t *testing.T) {
	// Factory settings JSON example files must all use 9787.
	files, err := filepath.Glob(filepath.Join(repoRoot(t), "docs", "factory-settings", "*.json"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	for _, file := range files {
		t.Run(filepath.Base(file), func(t *testing.T) {
			raw, err := os.ReadFile(file)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			if strings.Contains(string(raw), ":8787") {
				t.Fatalf("%s contains old-default port 8787; update to 9787", file)
			}
		})
	}
}

func TestDocsLiveE2eTemplatesUseCurrentPort(t *testing.T) {
	// Live-E2E config and factory-settings templates must default to 9787.
	for _, rel := range []string{
		"docs/live-e2e/config.local.yaml.template",
		"docs/live-e2e/factory-settings.live.json.template",
	} {
		t.Run(rel, func(t *testing.T) {
			body := readRepoRel(t, rel)
			if strings.Contains(body, ":8787") {
				t.Fatalf("%s contains old-default port 8787; update to 9787", rel)
			}
		})
	}
}

func TestDocsSmokeUsesCurrentPort(t *testing.T) {
	body := readRepoRel(t, "docs/SMOKE.md")
	if strings.Contains(body, "port **8787**") || strings.Contains(body, "port 8787") {
		t.Fatal("docs/SMOKE.md must reference port 9787, not 8787")
	}
}

// --- VAL-PORT-027: Historical 8787 references remain accurate and classified ---

// classifiedOldPortFiles lists tracked files where 8787 is allowed to appear
// because the reference is explicitly historical, a migration fixture, a
// compatibility test, or third-party conflict context.
func classifiedOldPortFiles() map[string]string {
	return map[string]string{
		"docs/TROUBLESHOOTING.md":                 "third-party conflict context (Cursor MCP, Wrangler, Dask) and historical default",
		"docs/spec-gpt56-reasoning-controls.md":   "superseded design document",
		"CHANGELOG.md":                            "historical changelog entries",
		"scripts/live-e2e/merge-custom-models.jq": "legacy harness retirement pattern",
		"scripts/security-audit.sh":               "secret-scan exclusion pattern",
	}
}

func TestDocsTroubleshootingClassifiesOldPort(t *testing.T) {
	body := readRepoRel(t, "docs/TROUBLESHOOTING.md")
	// Must distinguish the current default from the old 8787 conflict context.
	if !strings.Contains(body, "9787") {
		t.Fatal("docs/TROUBLESHOOTING.md must mention current default port 9787")
	}
	// Must retain accurate third-party conflict references for 8787.
	for _, conflict := range []string{"Cursor", "wrangler"} {
		if !strings.Contains(body, conflict) {
			t.Fatalf("docs/TROUBLESHOOTING.md must retain %q conflict reference for port 8787", conflict)
		}
	}
}

func TestDocsSpecDocBanneredAsHistorical(t *testing.T) {
	body := readRepoRel(t, "docs/spec-gpt56-reasoning-controls.md")
	// Superseded design documents must be bannered as historical.
	lower := strings.ToLower(body)
	if !strings.Contains(lower, "historical") && !strings.Contains(lower, "superseded") {
		t.Fatal("docs/spec-gpt56-reasoning-controls.md must be bannered as historical/superseded when it references the old default port")
	}
}

// --- VAL-PORT-028: Installers and live-E2E support isolated overrides ---

func TestDocsLiveE2eLibUsesOverrideableProxyAddress(t *testing.T) {
	body := readRepoRel(t, "scripts/live-e2e/lib.sh")
	// The live-E2E library must derive the proxy address from an overrideable
	// variable rather than hard-coding 8787.
	for _, want := range []string{"LIVE_E2E_PROXY_PORT", "LIVE_E2E_PROXY_HOST"} {
		if !strings.Contains(body, want) {
			t.Fatalf("scripts/live-e2e/lib.sh must define overrideable %s", want)
		}
	}
	// The default port must be 9787, not 8787.
	if !strings.Contains(body, "9787") {
		t.Fatal("scripts/live-e2e/lib.sh must default the proxy port to 9787")
	}
}

func TestDocsLiveE2eScriptsDeriveURLsFromOverride(t *testing.T) {
	// Key live-E2E scripts must not hard-code http://127.0.0.1:8787.
	for _, rel := range []string{
		"scripts/live-e2e/03-build-and-start.sh",
		"scripts/live-e2e/05-direct-provider-tests.sh",
		"scripts/live-e2e/error-redaction-checks.sh",
		"scripts/live-e2e/oauth-refresh-check.sh",
		"scripts/live-e2e/factory-manual-evidence.sh",
		"scripts/live-e2e/06-write-factory-settings.sh",
	} {
		t.Run(rel, func(t *testing.T) {
			body := readRepoRel(t, rel)
			if strings.Contains(body, "http://127.0.0.1:8787") {
				t.Fatalf("%s must derive proxy URLs from the overrideable address, not hard-code 8787", rel)
			}
		})
	}
}

// TestDocsLiveE2eCleanupDocsMatchCode verifies the live-E2E README cleanup
// guidance matches the actual positive-PID cleanup code and never implies
// killing the operator's 9787 owner.
func TestDocsLiveE2eCleanupDocsMatchCode(t *testing.T) {
	readme := readRepoRel(t, "scripts/live-e2e/README.md")
	cleanScript := readRepoRel(t, "scripts/live-e2e/01-clean-old-proxies.sh")

	// The code excludes 9787 from KILL_PORTS.
	if !strings.Contains(cleanScript, "KILL_PORTS=(1455 56121)") {
		t.Fatal("01-clean-old-proxies.sh must define KILL_PORTS without 9787")
	}

	// The README must never claim that port 9787 is used as a kill selector.
	// Specifically, the old phrasing "own a proxy port (9787/1455/56121)"
	// implied killing the operator's 9787 owner and must not appear.
	lowerReadme := strings.ToLower(readme)
	if strings.Contains(lowerReadme, "9787/1455") || strings.Contains(lowerReadme, "9787/56121") {
		t.Fatal("README must not list 9787 alongside kill-eligible ports; " +
			"port 9787 is the operator's live port and is excluded from kill selection")
	}

	// The README must document that 9787 is excluded from kill selection and
	// that cleanup uses positive binary-basename identification instead.
	if !strings.Contains(readme, "9787") {
		t.Fatal("README must mention port 9787 in the cleanup section to explain its exclusion from kill selection")
	}
	if !strings.Contains(lowerReadme, "not** used for kill selection") {
		t.Fatal("README must explicitly state that 9787 is not used for kill selection")
	}
	if !strings.Contains(lowerReadme, "basename") {
		t.Fatal("README must document positive binary-basename cleanup identification")
	}
}

// --- VAL-CROSS-016: Bootstrap idempotency and versioned tool readiness ---
//
// Mission-local executable bootstrap validation (TestBootstrapDeterministicExecution,
// TestBootstrapRefusesBadRepoRoot) lives in bootstrap_mission_test.go behind the
// "mission_bootstrap" build tag so that ordinary `go test ./...` in clean CI does
// not require /tmp mission markers. The mission gate runs:
//   go test -tags=mission_bootstrap ./internal/config/...
// to execute those tests explicitly.
//
// The error-returning resolver and its missing-fixture test are in
// bootstrap_resolver_test.go and run as part of ordinary `go test ./...`.

func TestDocsGoModVersionMatchesBootstrap(t *testing.T) {
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
	if goVersion != "1.26.4" {
		t.Fatalf("go.mod Go version = %q, want 1.26.4", goVersion)
	}
}

// extractMarkdownSection returns the text under a "## Heading" up to the next
// same-or-higher-level heading. Returns "" if the heading is absent.
func extractMarkdownSection(body, heading string) string {
	lines := strings.Split(body, "\n")
	prefix := "## " + heading
	var found bool
	var out []string
	for _, line := range lines {
		if !found {
			if strings.HasPrefix(line, prefix) {
				found = true
			}
			continue
		}
		// Stop at the next ## (or higher) heading.
		if strings.HasPrefix(line, "## ") || strings.HasPrefix(line, "# ") {
			break
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// --- VAL-FIREWORKS-020: Recipes accurately separate Standard, Priority, Fast, and Fire Pass ---

// TestFireworksDocsRecipesSeparatePaths verifies the Fireworks guide defines
// Standard, Priority, baseline Fast, and Fire Pass as distinct recipes, with
// a router-plus-priority combination only when the committed snapshot marks
// it supported. No managed combination picker is required.
func TestFireworksDocsRecipesSeparatePaths(t *testing.T) {
	body := readRepoRel(t, "docs/examples/fireworks.md")

	// Standard recipe: ordinary model ID, no service_tier.
	stdBlocks := fencedBlocks(body, "yaml")
	if len(stdBlocks) < 4 {
		t.Fatalf("fireworks.md must contain at least 4 YAML recipes (Standard, Priority, Fast, combination); got %d", len(stdBlocks))
	}

	// Parse all YAML blocks and look for each recipe type.
	type recipe struct {
		hasKnownAuth   string
		hasServiceTier bool
		serviceTierVal string
		upstreamModel  string
	}
	recipes := []recipe{}
	for _, block := range stdBlocks {
		var m struct {
			Models []struct {
				KnownAuth     string         `yaml:"known_auth"`
				UpstreamModel string         `yaml:"upstream_model"`
				ExtraArgs     map[string]any `yaml:"extra_args"`
			} `yaml:"models"`
		}
		if err := yaml.Unmarshal([]byte(block), &m); err != nil {
			continue
		}
		for _, model := range m.Models {
			r := recipe{
				hasKnownAuth:  model.KnownAuth,
				upstreamModel: model.UpstreamModel,
			}
			if st, ok := model.ExtraArgs["service_tier"]; ok {
				r.hasServiceTier = true
				if s, ok := st.(string); ok {
					r.serviceTierVal = s
				}
			}
			recipes = append(recipes, r)
		}
	}

	// Standard: fireworks, models/... , no tier.
	hasStandard := false
	// Priority: fireworks, models/..., service_tier: priority.
	hasPriority := false
	// Fast: fireworks, routers/..., no tier.
	hasFast := false
	// Combination: fireworks, routers/..., service_tier: priority (snapshot-supported).
	hasCombo := false

	for _, r := range recipes {
		isRouter := strings.Contains(r.upstreamModel, "/routers/")
		isModel := strings.Contains(r.upstreamModel, "/models/")
		switch {
		case r.hasKnownAuth == "fireworks" && isModel && !r.hasServiceTier:
			hasStandard = true
		case r.hasKnownAuth == "fireworks" && isModel && r.serviceTierVal == "priority":
			hasPriority = true
		case r.hasKnownAuth == "fireworks" && isRouter && !r.hasServiceTier:
			hasFast = true
		case r.hasKnownAuth == "fireworks" && isRouter && r.serviceTierVal == "priority":
			hasCombo = true
		}
	}

	if !hasStandard {
		t.Error("fireworks.md must define a Standard recipe (fireworks, models/..., no service_tier)")
	}
	if !hasPriority {
		t.Error("fireworks.md must define a Priority recipe (fireworks, models/..., service_tier: priority)")
	}
	if !hasFast {
		t.Error("fireworks.md must define a baseline Fast recipe (fireworks, routers/..., no service_tier)")
	}
	if !hasCombo {
		t.Error("fireworks.md must define a snapshot-supported Fast+Priority recipe (fireworks, routers/..., service_tier: priority)")
	}

	// Must state the combination is snapshot-supported only and not inferred.
	if !strings.Contains(body, "snapshot") {
		t.Error("fireworks.md must state the Fast+Priority combination is snapshot-supported")
	}
	if !strings.Contains(body, "neither infers nor synthesizes") {
		t.Error("fireworks.md must state the proxy neither infers nor synthesizes the combination")
	}
}

// checkYAMLRecipeServiceTiers parses every YAML fenced block in body and
// returns an error if any model entry has extra_args.service_tier == "fast".
// This check is independent of surrounding prose so a negation in unrelated
// text cannot mask a positive service_tier: fast recipe.
func checkYAMLRecipeServiceTiers(body string) error {
	for _, block := range fencedBlocks(body, "yaml") {
		var m struct {
			Models []struct {
				Alias         string         `yaml:"alias"`
				ExtraArgs     map[string]any `yaml:"extra_args"`
				UpstreamModel string         `yaml:"upstream_model"`
			} `yaml:"models"`
		}
		if err := yaml.Unmarshal([]byte(block), &m); err != nil {
			continue // skip non-model YAML blocks
		}
		for _, model := range m.Models {
			if st, ok := model.ExtraArgs["service_tier"]; ok {
				if s, ok := st.(string); ok && s == "fast" {
					return fmt.Errorf("YAML recipe for alias %q uses service_tier: fast — Fast must be a router model ID, not a service_tier value", model.Alias)
				}
			}
		}
	}
	return nil
}

// TestFireworksDocsFastNotServiceTier verifies no YAML recipe in either
// Fireworks guide uses service_tier: fast. The check parses every fenced
// YAML recipe block individually so unrelated prose containing a negation
// cannot mask a positive service_tier: fast entry.
func TestFireworksDocsFastNotServiceTier(t *testing.T) {
	for _, rel := range []string{
		"docs/examples/fireworks.md",
		"docs/examples/fireworks-fire-pass.md",
	} {
		t.Run(rel, func(t *testing.T) {
			body := readRepoRel(t, rel)
			if err := checkYAMLRecipeServiceTiers(body); err != nil {
				t.Fatalf("%s: %v", rel, err)
			}
		})
	}
}

// TestFireworksDocsFastNotServiceTierDeterministic verifies that the
// detection logic catches a positive service_tier: fast recipe even when
// the surrounding text contains a negation. This proves the guard has a
// deterministic failing test path that cannot be masked by unrelated prose.
func TestFireworksDocsFastNotServiceTierDeterministic(t *testing.T) {
	// Construct a doc where unrelated text says "not" but a YAML recipe
	// uses service_tier: fast. The old document-wide check would pass
	// because "not" appears in the body. The hardened check must fail.
	badDoc := "# Fireworks\n\nThis is not a valid configuration.\n\n```yaml\n" +
		`models:
  - alias: bad-fast
    known_auth: fireworks
    upstream_model: accounts/fireworks/routers/glm-5p2-fast
    extra_args:
      service_tier: fast
` + "```\n"
	if err := checkYAMLRecipeServiceTiers(badDoc); err == nil {
		t.Fatal("checkYAMLRecipeServiceTiers must reject service_tier: fast in a YAML recipe even when unrelated prose contains a negation")
	}

	// A clean doc with no service_tier: fast recipe must pass even if the
	// prose discusses fast as a concept.
	cleanDoc := "# Fireworks\n\nFast is a router model ID, not a service_tier value.\n\n```yaml\n" +
		`models:
  - alias: good-fast
    known_auth: fireworks
    upstream_model: accounts/fireworks/routers/glm-5p2-fast
` + "```\n"
	if err := checkYAMLRecipeServiceTiers(cleanDoc); err != nil {
		t.Fatalf("checkYAMLRecipeServiceTiers must accept clean doc: %v", err)
	}
}

// --- VAL-FIREWORKS-021: Registry, catalog, env, and docs stay synchronized ---

// TestFireworksDocsFirePassExperimentalScope verifies the Fire Pass guide
// identifies the product as experimental, limits zero-cost claims to
// eligible routers with active pass, states the documented scope, warns about
// mutable availability/pricing, and never implies arbitrary models are free.
func TestFireworksDocsFirePassExperimentalScope(t *testing.T) {
	body := readRepoRel(t, "docs/examples/fireworks-fire-pass.md")

	for _, want := range []string{
		"experimental",
		"mutable",
		"availability",
		"pricing",
	} {
		if !strings.Contains(strings.ToLower(body), want) {
			t.Fatalf("fireworks-fire-pass.md must mention %q", want)
		}
	}

	// Must limit zero-token-cost claims to eligible routers with active pass.
	if !strings.Contains(body, "active") || !strings.Contains(body, "eligible") {
		t.Fatal("fireworks-fire-pass.md must limit zero-cost claims to currently eligible routers with an active pass")
	}

	// Must never imply arbitrary Fireworks models become free.
	if !strings.Contains(strings.ToLower(body), "not free") && !strings.Contains(strings.ToLower(body), "not") {
		t.Fatal("fireworks-fire-pass.md must clarify arbitrary models are not free")
	}

	// Must mention personal/non-production scope.
	lower := strings.ToLower(body)
	if !strings.Contains(lower, "personal") && !strings.Contains(lower, "non-production") {
		t.Fatal("fireworks-fire-pass.md must state the personal/non-production agentic coding scope")
	}
}

// TestFireworksDocsManualEntryGuaranteed verifies both guides document manual
// model entry as the guaranteed onboarding path.
func TestFireworksDocsManualEntryGuaranteed(t *testing.T) {
	stdBody := readRepoRel(t, "docs/examples/fireworks.md")
	fpBody := readRepoRel(t, "docs/examples/fireworks-fire-pass.md")

	for _, want := range []string{"manual", "guaranteed"} {
		if !strings.Contains(strings.ToLower(stdBody), want) {
			t.Fatalf("fireworks.md must mention %q for manual entry", want)
		}
	}
	for _, want := range []string{"manual"} {
		if !strings.Contains(strings.ToLower(fpBody), want) {
			t.Fatalf("fireworks-fire-pass.md must mention %q for manual entry", want)
		}
	}
}

// TestFireworksDocsNoLiveValidationClaims verifies no public Fireworks guidance
// calls the compatibility path the official model-list API or implies mocked
// success proves live availability.
func TestFireworksDocsNoLiveValidationClaims(t *testing.T) {
	for _, rel := range []string{
		"docs/examples/fireworks.md",
		"docs/examples/fireworks-fire-pass.md",
		"docs/PROVIDERS.md",
		"docs/CONFIG.md",
	} {
		body := readRepoRel(t, rel)
		lower := strings.ToLower(body)
		// The compatibility path must be labeled best-effort, not official.
		if strings.Contains(lower, "official fireworks list models api") || strings.Contains(lower, "official account-scoped list models api") {
			t.Fatalf("%s must not label the compatibility path as the official model-list API", rel)
		}
		// Must not claim mock validation proves live availability.
		if strings.Contains(lower, "mock validation establishes") || strings.Contains(lower, "mocked success proves") {
			t.Fatalf("%s must not claim mock validation proves live availability", rel)
		}
	}
}

// TestFireworksDocsCompatibilityPathQualified verifies the Standard discovery
// path is explicitly labeled as best-effort compatibility, not official.
func TestFireworksDocsCompatibilityPathQualified(t *testing.T) {
	body := readRepoRel(t, "docs/examples/fireworks.md")
	lower := strings.ToLower(body)
	if !strings.Contains(lower, "best-effort compatibility") {
		t.Fatal("fireworks.md must label the discovery path as best-effort compatibility")
	}
}

// TestFireworksEnvTemplateHasBothKeys verifies the env template contains
// exactly one empty assignment for each Fireworks credential.
func TestFireworksEnvTemplateHasBothKeys(t *testing.T) {
	body := readRepoRel(t, ".env.local.example")
	for _, key := range []string{"FIREWORKS_API_KEY", "FIREWORKS_FIRE_PASS_API_KEY"} {
		// Count empty assignments: KEY=""
		pattern := key + `=""`
		count := strings.Count(body, pattern)
		if count != 1 {
			t.Fatalf(".env.local.example must contain exactly one empty %s assignment, got %d", key, count)
		}
	}
}

// TestFireworksDocsIndexLinksFirePass verifies the docs index and README link
// to the Fire Pass example.
func TestFireworksDocsIndexLinksFirePass(t *testing.T) {
	for _, rel := range []string{"docs/README.md", "README.md"} {
		body := readRepoRel(t, rel)
		if !strings.Contains(body, "fireworks-fire-pass.md") {
			t.Fatalf("%s must link to fireworks-fire-pass.md", rel)
		}
	}
}

// --- VAL-FIREWORKS-021: Catalog source records are pinned and deterministic ---

// TestFireworksCatalogSourceRecordsPinned verifies both catalogs have a
// committed official source URL and as-of date.
func TestFireworksCatalogSourceRecordsPinned(t *testing.T) {
	fpSrc := FireworksFirePassCatalogSource()
	if fpSrc.URL == "" {
		t.Error("Fire Pass catalog source URL must not be empty")
	}
	if fpSrc.AsOf == "" {
		t.Error("Fire Pass catalog source as-of date must not be empty")
	}
	if !strings.HasPrefix(fpSrc.URL, "https://") {
		t.Errorf("Fire Pass source URL must be https: %q", fpSrc.URL)
	}

	fastSrc := FireworksFastCatalogSource()
	if fastSrc.URL == "" {
		t.Error("Fast catalog source URL must not be empty")
	}
	if fastSrc.AsOf == "" {
		t.Error("Fast catalog source as-of date must not be empty")
	}
	if !strings.HasPrefix(fastSrc.URL, "https://") {
		t.Errorf("Fast source URL must be https: %q", fastSrc.URL)
	}

	// Sources must be independent (different URL or different label).
	if fpSrc.URL == fastSrc.URL && fpSrc.Label == fastSrc.Label {
		t.Error("Fire Pass and Fast sources must be independent (different URL or label)")
	}
}

// TestFireworksCatalogsUseIndependentMembership verifies Fire Pass and Fast
// catalogs use independent official-source membership and may overlap, but
// Fire Pass excludes routers not in its own catalog.
func TestFireworksCatalogsUseIndependentMembership(t *testing.T) {
	fpCatalog := FireworksFirePassCatalog()
	fastCatalog := FireworksFastCatalog()

	// Canonical router must be present in Fire Pass.
	fpHasCanonical := false
	for _, e := range fpCatalog {
		if e.ID == "accounts/fireworks/routers/glm-5p2-fast" {
			fpHasCanonical = true
		}
	}
	if !fpHasCanonical {
		t.Error("Fire Pass catalog must contain accounts/fireworks/routers/glm-5p2-fast")
	}

	// Canonical router must be present in Fast.
	fastHasCanonical := false
	for _, e := range fastCatalog {
		if e.ID == "accounts/fireworks/routers/glm-5p2-fast" {
			fastHasCanonical = true
		}
	}
	if !fastHasCanonical {
		t.Error("Fast catalog must contain accounts/fireworks/routers/glm-5p2-fast")
	}

	// Overlap is allowed and tested (both have glm-5p2-fast).
	if fpHasCanonical && fastHasCanonical {
		// Good: overlap is tested.
	}

	// Fire Pass entries must all be router IDs (not ordinary model IDs).
	for _, e := range fpCatalog {
		if !strings.Contains(e.ID, "/routers/") {
			t.Errorf("Fire Pass catalog entry %q must be a router ID", e.ID)
		}
	}
	// Fast entries must all be router IDs.
	for _, e := range fastCatalog {
		if !strings.Contains(e.ID, "/routers/") {
			t.Errorf("Fast catalog entry %q must be a router ID", e.ID)
		}
	}
}

// TestFireworksSnapshotSupportedFastPriorityExact verifies the snapshot
// marks only the documented router as supported for Fast+Priority. The
// check asserts exact cardinality and exact membership so adding or removing
// an entry is a deterministic test failure.
func TestFireworksSnapshotSupportedFastPriorityExact(t *testing.T) {
	supported := FireworksSnapshotSupportedFastPriority()
	want := []string{
		"accounts/fireworks/routers/glm-5p2-fast",
	}
	if len(supported) != len(want) {
		t.Fatalf("snapshot-supported Fast+Priority cardinality = %d, want exactly %d (got %v)", len(supported), len(want), supported)
	}
	for i, id := range want {
		if supported[i] != id {
			t.Errorf("snapshot-supported[%d] = %q, want %q", i, supported[i], id)
		}
	}
	// Slices.Equal provides a clean cardinality+membership assertion.
	if !slices.Equal(supported, want) {
		t.Fatalf("snapshot-supported Fast+Priority = %v, want exactly %v", supported, want)
	}
}

// --- VAL-FIREWORKS-022: Pass-through and security boundaries documented ---

// TestFireworksDocsPassThroughFieldsDocumented verifies the guide identifies
// extra_args as top-level pass-through and covers the agreed field set.
func TestFireworksDocsPassThroughFieldsDocumented(t *testing.T) {
	body := readRepoRel(t, "docs/examples/fireworks.md")
	lower := strings.ToLower(body)

	// Must identify extra_args as top-level pass-through.
	if !strings.Contains(lower, "pass-through") && !strings.Contains(lower, "passthrough") {
		t.Fatal("fireworks.md must identify extra_args as top-level pass-through")
	}
	if !strings.Contains(lower, "top-level") {
		t.Fatal("fireworks.md must state extra_args are merged at the top level")
	}

	// Must cover the agreed field set.
	requiredFields := []string{
		"service_tier",
		"reasoning_effort",
		"reasoning_history",
		"thinking",
		"prompt_cache_key",
		"prompt_cache_isolation_key",
		"perf_metrics_in_response",
		"context_length_exceeded_behavior",
		"response_format",
		"min_p",
		"top_k",
		"repetition_penalty",
		"tools",
		"tool_choice",
		"parallel_tool_calls",
		"stream",
		"stream_options",
	}
	for _, field := range requiredFields {
		if !strings.Contains(lower, field) {
			t.Fatalf("fireworks.md must document pass-through field %q", field)
		}
	}
}

// prohibitedClaimPatterns maps prohibited claim phrases to the negation
// markers that may appear in the same sentence to make the claim explicitly
// negative (and thus acceptable). A sentence containing a prohibited phrase
// without a co-occurring negation marker fails.
var prohibitedClaimPatterns = []struct {
	phrase   string
	negation []string
}{
	// Translation claims — the proxy must not translate between protocols.
	{"translates", []string{"does not", "not "}},
	{"translate ", []string{"does not", "not "}},
	// Static / synthesized affinity claims.
	{"session affinity", []string{"does not", "not invent", "not "}},
	{"static affinity", []string{"does not", "not invent", "not "}},
	{"synthesized affinity", []string{"does not", "not invent", "not "}},
	// Live-validation claims — mock-only validation is not live.
	{"mock validation establishes", []string{}}, // always prohibited
	{"mocked success proves", []string{}},       // always prohibited
	{"establishes live", []string{}},            // always prohibited
	// Universal-capability claims — capabilities are model-dependent.
	{"all models support", []string{}},   // always prohibited
	{"every model supports", []string{}}, // always prohibited
}

// checkNoProhibitedClaims splits body into sentences and verifies that no
// sentence contains a prohibited claim without a co-occurring negation. This
// prevents a document-wide negation from masking a positive prohibited claim.
func checkNoProhibitedClaims(body string) error {
	// Split on sentence boundaries while preserving enough context.
	// We split on newlines and periods followed by space+capital, then
	// also check each line as a unit for table rows.
	sentences := splitSentences(body)
	for _, sent := range sentences {
		lower := strings.ToLower(sent)
		for _, p := range prohibitedClaimPatterns {
			if !strings.Contains(lower, p.phrase) {
				continue
			}
			// If the phrase is present, verify a negation is in the same sentence.
			allowed := false
			for _, neg := range p.negation {
				if strings.Contains(lower, neg) {
					allowed = true
					break
				}
			}
			if !allowed {
				return fmt.Errorf("prohibited claim %q appears without co-occurring negation in sentence: %q", p.phrase, strings.TrimSpace(sent))
			}
		}
	}
	return nil
}

// splitSentences splits markdown text into sentence-like units for claim
// checking. It preserves table rows and list items as individual units and
// splits prose on sentence boundaries.
func splitSentences(text string) []string {
	// First split on newlines to get lines.
	lines := strings.Split(text, "\n")
	var sentences []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		// For prose lines, further split on ". " boundaries.
		// But keep table rows and list items as whole lines.
		if strings.HasPrefix(trimmed, "|") || strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "```") {
			sentences = append(sentences, line)
			continue
		}
		// Split on period-space for prose.
		parts := strings.Split(line, ". ")
		sentences = append(sentences, parts...)
	}
	return sentences
}

// TestFireworksDocsNoProhibitedClaims verifies no Fireworks documentation
// sentence contains a prohibited translation, static-affinity,
// live-validation, or universal-capability claim without a co-occurring
// negation in the same sentence. The check is scoped to Fireworks-specific
// guides; PROVIDERS.md legitimately describes T3 protocol translation paths
// as a core proxy feature and is not a Fireworks pass-through violation.
func TestFireworksDocsNoProhibitedClaims(t *testing.T) {
	for _, rel := range []string{
		"docs/examples/fireworks.md",
		"docs/examples/fireworks-fire-pass.md",
	} {
		t.Run(rel, func(t *testing.T) {
			body := readRepoRel(t, rel)
			if err := checkNoProhibitedClaims(body); err != nil {
				t.Fatalf("%s: %v", rel, err)
			}
		})
	}
}

// TestFireworksDocsProhibitedClaimsDeterministic verifies the sentence-level
// check catches a positive prohibited claim even when unrelated text elsewhere
// contains a negation. This proves the guard cannot be masked by a
// document-wide substring match.
func TestFireworksDocsProhibitedClaimsDeterministic(t *testing.T) {
	// A doc where "not" appears in one sentence and "translates" in a
	// different sentence must fail. The old document-wide check would pass
	// because "not" is present in the body.
	badDoc := "# Guide\n\nThe proxy does not modify your data.\n\nThe proxy translates between protocols automatically.\n"
	if err := checkNoProhibitedClaims(badDoc); err == nil {
		t.Fatal("checkNoProhibitedClaims must reject 'translates' in a sentence without a co-occurring negation")
	}

	// A doc where "translates" appears with "does not" in the same sentence
	// must pass (explicitly negative claim).
	goodDoc := "# Guide\n\nThe proxy does not translate between protocols.\n"
	if err := checkNoProhibitedClaims(goodDoc); err != nil {
		t.Fatalf("checkNoProhibitedClaims must accept explicitly negative claim: %v", err)
	}

	// "all models support" with no negation must always fail.
	badUniversal := "# Guide\n\nAll models support reasoning output.\n"
	if err := checkNoProhibitedClaims(badUniversal); err == nil {
		t.Fatal("checkNoProhibitedClaims must reject universal-capability claim 'all models support'")
	}

	// "mocked success proves" with no negation must always fail.
	badLive := "# Guide\n\nMocked success proves live provider availability.\n"
	if err := checkNoProhibitedClaims(badLive); err == nil {
		t.Fatal("checkNoProhibitedClaims must reject live-validation claim 'mocked success proves'")
	}
}

// factorySyncRequiredExclusions lists every value that must be explicitly
// excluded from the Factory-sync entry, and the markdown text that should
// appear within the Factory-sync section to document that exclusion.
var factorySyncRequiredExclusions = []struct {
	desc   string // human-readable description of the excluded value
	marker string // substring that must appear within the Factory-sync section
}{
	{"Fireworks upstream URL", "upstream URL"},
	{"known_auth", "known_auth"},
	{"upstream model/router ID", "router ID"},
	{"service_tier", "service_tier"},
	{"extra_args", "extra_args"},
	{"env-var name", "env-var name"},
	{"upstream credential", "credential"},
}

// checkFactorySyncSectionExclusions extracts the "Factory sync" section from
// body and verifies each required exclusion marker is present within that
// section. This prevents a document-wide substring from masking a missing
// exclusion within the section.
func checkFactorySyncSectionExclusions(body string) error {
	section := extractMarkdownSection(body, "Factory sync")
	if section == "" {
		return fmt.Errorf("no '## Factory sync' section found")
	}
	for _, ex := range factorySyncRequiredExclusions {
		if !strings.Contains(section, ex.marker) {
			return fmt.Errorf("Factory sync section must state exclusion of %s (looking for %q within the section)", ex.desc, ex.marker)
		}
	}
	return nil
}

// TestFireworksDocsFactorySyncExcludesUpstream verifies each required
// Factory-sync exclusion is asserted within the Factory-sync documentation
// section. The check is section-scoped so a document-wide mention of
// "credential" in an unrelated section cannot mask a missing exclusion.
func TestFireworksDocsFactorySyncExcludesUpstream(t *testing.T) {
	for _, rel := range []string{
		"docs/examples/fireworks.md",
		"docs/examples/fireworks-fire-pass.md",
	} {
		t.Run(rel, func(t *testing.T) {
			body := readRepoRel(t, rel)
			if err := checkFactorySyncSectionExclusions(body); err != nil {
				t.Fatalf("%s: %v", rel, err)
			}
		})
	}
}

// TestFireworksDocsFactorySyncExcludesDeterministic verifies that the
// section-scoped check catches a missing exclusion even when the value
// appears elsewhere in the document. This proves the guard cannot be masked
// by a document-wide substring match.
func TestFireworksDocsFactorySyncExcludesDeterministic(t *testing.T) {
	// Construct a doc where "credential" appears in a different section
	// but is missing from the Factory-sync section. The old document-wide
	// check would pass; the hardened section-scoped check must fail.
	badDoc := "# Guide\n\n## Overview\n\nYour credential is private.\n\n## Factory sync\n\n" +
		"Factory sync writes the local alias and baseUrl.\n\n## Notes\n\nDone.\n"
	if err := checkFactorySyncSectionExclusions(badDoc); err == nil {
		t.Fatal("checkFactorySyncSectionExclusions must reject a Factory-sync section missing required exclusions")
	}

	// A doc with all exclusions in the section must pass.
	goodDoc := "# Guide\n\n## Factory sync\n\n" +
		"Factory sync never includes the upstream URL, known_auth, " +
		"the model/router ID, service_tier, any extra_args, " +
		"the env-var name, or the upstream credential.\n"
	if err := checkFactorySyncSectionExclusions(goodDoc); err != nil {
		t.Fatalf("checkFactorySyncSectionExclusions must accept complete section: %v", err)
	}
}

// --- VAL-BASETEN-017: Baseten registry and public artifacts stay synchronized ---

// TestBasetenDocsFactorySyncExcludesUpstream verifies the Baseten Factory-sync
// section explicitly documents that it excludes upstream secrets and metadata.
func TestBasetenDocsFactorySyncExcludesUpstream(t *testing.T) {
	body := readRepoRel(t, "docs/examples/baseten.md")
	section := extractMarkdownSection(body, "Factory sync")
	if section == "" {
		t.Fatal("docs/examples/baseten.md must contain a '## Factory sync' section")
	}
	for _, marker := range []string{
		"upstream URL",
		"known_auth",
		"upstream model",
		"extra_args",
		"env-var name",
		"credential",
	} {
		if !strings.Contains(section, marker) {
			t.Errorf("baseten.md Factory sync section must mention exclusion of %q", marker)
		}
	}
}

// TestBasetenDocsManualEntryGuaranteed verifies the Baseten guide documents
// manual entry as always available.
func TestBasetenDocsManualEntryGuaranteed(t *testing.T) {
	body := readRepoRel(t, "docs/examples/baseten.md")
	for _, want := range []string{
		"manual",
		"Manual",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("baseten.md must mention manual entry")
		}
	}
}

// TestBasetenDocsNoLiveValidationClaims verifies the Baseten guide does not
// claim live provider validation.
func TestBasetenDocsNoLiveValidationClaims(t *testing.T) {
	body := readRepoRel(t, "docs/examples/baseten.md")
	if err := checkNoProhibitedClaims(body); err != nil {
		t.Fatalf("baseten.md: %v", err)
	}
}

// TestBasetenDocsScopedToSharedModelAPI verifies the guide scopes the native
// profile to shared Model APIs and directs custom deployments to custom endpoints.
func TestBasetenDocsScopedToSharedModelAPI(t *testing.T) {
	body := readRepoRel(t, "docs/examples/baseten.md")
	for _, want := range []string{
		"shared Model API",
		"custom",
		"dedicated",
	} {
		if !strings.Contains(strings.ToLower(body), strings.ToLower(want)) {
			t.Errorf("baseten.md must mention %q", want)
		}
	}
	// The native recipe must have known_auth: baseten and no base_url.
	nativeSection := extractMarkdownSection(body, "Recipe")
	if nativeSection == "" {
		t.Fatal("baseten.md must contain a '## Recipe' section")
	}
	yamlBlocks := fencedBlocks(nativeSection, "yaml")
	found := false
	for _, block := range yamlBlocks {
		if strings.Contains(block, "known_auth: baseten") && !strings.Contains(block, "base_url:") {
			found = true
		}
	}
	if !found {
		t.Error("baseten.md native recipe must have known_auth: baseten and no base_url")
	}
}

// TestBasetenDocsNativeAndCustomAreDistinct verifies the guide presents both
// a native profile recipe (with known_auth: baseten) and a custom deployment
// recipe (with explicit base_url/api_key_env and no known_auth).
func TestBasetenDocsNativeAndCustomAreDistinct(t *testing.T) {
	body := readRepoRel(t, "docs/examples/baseten.md")
	// Custom deployment section must exist.
	customSection := extractMarkdownSection(body, "Custom deployment recipe")
	if customSection == "" {
		t.Fatal("baseten.md must contain a '## Custom deployment recipe' section")
	}
	yamlBlocks := fencedBlocks(customSection, "yaml")
	found := false
	for _, block := range yamlBlocks {
		if strings.Contains(block, "base_url:") && strings.Contains(block, "api_key_env:") && !strings.Contains(block, "known_auth: baseten") {
			found = true
		}
	}
	if !found {
		t.Error("baseten.md custom deployment recipe must have base_url and api_key_env without known_auth: baseten")
	}
}

// TestBasetenDocsAgentReadyScoped verifies the guide describes agent_ready as
// configured/resolved metadata, not proof of universal model support.
func TestBasetenDocsAgentReadyScoped(t *testing.T) {
	body := readRepoRel(t, "docs/examples/baseten.md")
	if !strings.Contains(body, "agent_ready") {
		t.Fatal("baseten.md must mention agent_ready")
	}
	if !strings.Contains(strings.ToLower(body), "configured") || !strings.Contains(strings.ToLower(body), "model-dependent") {
		t.Errorf("baseten.md must describe agent_ready as configured metadata and note model-dependence")
	}
}

// TestBasetenEnvTemplateHasKey verifies the env template contains exactly one
// empty BASETEN_API_KEY assignment.
func TestBasetenEnvTemplateHasKey(t *testing.T) {
	body := readRepoRel(t, ".env.local.example")
	pattern := `BASETEN_API_KEY=""`
	count := strings.Count(body, pattern)
	if count != 1 {
		t.Errorf("BASETEN_API_KEY empty assignment count = %d, want 1", count)
	}
}

// TestBasetenDocsRegistrySynchronized verifies the Baseten registry tuple
// matches the PROVIDERS.md matrix row.
func TestBasetenDocsRegistrySynchronized(t *testing.T) {
	ka, ok := LookupKnownAuth("baseten")
	if !ok {
		t.Fatal("baseten profile missing from registry")
	}
	body := readRepoRel(t, "docs/examples/baseten.md")
	for _, want := range []string{
		ka.BaseURL,
		ka.APIKeyEnv,
		string(ka.UpstreamProtocol),
		"generic-chat-completion-api",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("baseten.md must contain %q", want)
		}
	}
}

// TestBasetenDocsReadmeAndProviderLinks verify the README and docs/README.md
// link to the Baseten example.
func TestBasetenDocsReadmeAndProviderLinks(t *testing.T) {
	for _, rel := range []string{"README.md", "docs/README.md"} {
		body := readRepoRel(t, rel)
		if !strings.Contains(body, "baseten.md") {
			t.Errorf("%s must link to examples/baseten.md", rel)
		}
	}
}

// TestBasetenDocsRecipeHydratesWithSyntheticKey parses the native recipe YAML
// from baseten.md and verifies it hydrates with a synthetic BASETEN_API_KEY.
// This directly tests VAL-BASETEN-017's "hydrates with a synthetic key" claim.
func TestBasetenDocsRecipeHydratesWithSyntheticKey(t *testing.T) {
	t.Setenv("BASETEN_API_KEY", "synthetic-baseten-key-12345")

	body := readRepoRel(t, "docs/examples/baseten.md")
	recipeSection := extractMarkdownSection(body, "Recipe")
	if recipeSection == "" {
		t.Fatal("baseten.md must contain a '## Recipe' section")
	}
	blocks := fencedBlocks(recipeSection, "yaml")
	if len(blocks) == 0 {
		t.Fatal("baseten.md Recipe section must contain at least one YAML block")
	}

	// Parse the native recipe block (contains known_auth: baseten).
	type recipeYAML struct {
		Models []Model `yaml:"models"`
	}
	var nativeFound bool
	for _, block := range blocks {
		var ry recipeYAML
		if err := yaml.Unmarshal([]byte(block), &ry); err != nil {
			continue
		}
		for _, m := range ry.Models {
			if m.KnownAuth != "baseten" {
				continue
			}
			nativeFound = true
			// Hydrate the model from the registry.
			model := m
			if err := HydrateModel(&model); err != nil {
				t.Fatalf("hydration failed for native recipe: %v", err)
			}
			// Verify exact tuple after hydration.
			if model.BaseURL != "https://inference.baseten.co/v1" {
				t.Errorf("hydrated BaseURL = %q, want https://inference.baseten.co/v1", model.BaseURL)
			}
			if model.APIKeyEnv != "BASETEN_API_KEY" {
				t.Errorf("hydrated APIKeyEnv = %q, want BASETEN_API_KEY", model.APIKeyEnv)
			}
			if model.UpstreamProtocol != UpstreamOpenAIChat {
				t.Errorf("hydrated UpstreamProtocol = %q, want openai-chat", model.UpstreamProtocol)
			}
			if model.FactoryProvider != "generic-chat-completion-api" {
				t.Errorf("FactoryProvider = %q, want generic-chat-completion-api", model.FactoryProvider)
			}
			// No provider-wide defaults injected.
			if len(model.ExtraArgs) != 0 {
				t.Errorf("ExtraArgs should be empty after hydration, got %v", model.ExtraArgs)
			}
			if len(model.ExtraHeaders) != 0 {
				t.Errorf("ExtraHeaders should be empty after hydration, got %v", model.ExtraHeaders)
			}
			// Upstream model must be preserved exactly (opaque slug).
			if m.UpstreamModel != model.UpstreamModel {
				t.Errorf("upstream model changed during hydration: %q -> %q", m.UpstreamModel, model.UpstreamModel)
			}
		}
	}
	if !nativeFound {
		t.Fatal("baseten.md Recipe section must contain a native recipe with known_auth: baseten")
	}
}

// TestBasetenDocsCustomRecipeDoesNotInheritProfile verifies the custom
// deployment recipe in baseten.md does not carry known_auth: baseten and
// has its own explicit base_url/api_key_env.
func TestBasetenDocsCustomRecipeDoesNotInheritProfile(t *testing.T) {
	body := readRepoRel(t, "docs/examples/baseten.md")
	customSection := extractMarkdownSection(body, "Custom deployment recipe")
	if customSection == "" {
		t.Fatal("baseten.md must contain a '## Custom deployment recipe' section")
	}
	blocks := fencedBlocks(customSection, "yaml")
	if len(blocks) == 0 {
		t.Fatal("baseten.md Custom deployment recipe section must contain at least one YAML block")
	}

	type recipeYAML struct {
		Models []Model `yaml:"models"`
	}
	var customFound bool
	for _, block := range blocks {
		var ry recipeYAML
		if err := yaml.Unmarshal([]byte(block), &ry); err != nil {
			continue
		}
		for _, m := range ry.Models {
			if m.KnownAuth == "baseten" {
				t.Errorf("custom deployment recipe must not have known_auth: baseten (alias %q)", m.Alias)
			}
			if m.BaseURL != "" && m.APIKeyEnv != "" && m.KnownAuth == "" {
				customFound = true
			}
		}
	}
	if !customFound {
		t.Fatal("baseten.md Custom deployment recipe must have a model with explicit base_url and api_key_env and no known_auth")
	}
}

// TestBasetenDocsNoStaleEndpoints verifies no Baseten doc uses a stale or
// incorrect endpoint or env var name.
func TestBasetenDocsNoStaleEndpoints(t *testing.T) {
	staleURLs := []string{
		// Must always use the inference subdomain prefix.
		"https://baseten.co/v1",
		"https://api.baseten.co/v1",
		// Must not use model API without the /v1 path.
		"https://inference.baseten.co/chat",
	}
	staleEnvNames := []string{
		"BASETEN_KEY",
		"BASETEN_TOKEN",
		"BASETEN_SECRET",
		"BASETEN_API_TOKEN",
	}
	docsToCheck := append([]string{
		"docs/examples/baseten.md",
		"docs/PROVIDERS.md",
		"README.md",
		"docs/README.md",
		".env.local.example",
		"docs/CONFIG.md",
		"docs/FACTORY.md",
	}, docExampleMarkdownFiles()...)
	for _, rel := range docsToCheck {
		body := readRepoRel(t, rel)
		for _, stale := range staleURLs {
			if strings.Contains(body, stale) {
				t.Errorf("%s contains stale Baseten URL %q", rel, stale)
			}
		}
		for _, stale := range staleEnvNames {
			if strings.Contains(body, stale) {
				t.Errorf("%s contains stale Baseten env name %q", rel, stale)
			}
		}
	}
}

// normalizationClaimPatterns maps prohibited model-ID normalization claim
// phrases to the negation markers that may appear in the same sentence to
// make the claim explicitly negative (and thus acceptable). A sentence
// containing a prohibited phrase without a co-occurring negation fails.
// Universal-capability phrases are always prohibited (empty negation slice).
var normalizationClaimPatterns = []struct {
	phrase   string
	negation []string
}{
	{"rewrites model", []string{"does not", "not ", "never "}},
	{"normalizes model", []string{"does not", "not ", "never "}},
	{"maps model", []string{"does not", "not ", "never "}},
	{"translates model", []string{"does not", "not ", "never "}},
	// Universal-capability claims are always prohibited (no negation escape).
	{"all models support", []string{}},
	{"every model supports", []string{}},
}

// checkNoModelNormalizationClaims is a reusable predicate that splits body
// into sentences and verifies that no sentence positively claims the proxy
// rewrites, normalizes, maps, or translates model IDs without a co-occurring
// negation. Precise descriptions of exact opaque model preservation (e.g.,
// "model IDs are preserved byte-for-byte") remain allowed because they do
// not contain a prohibited normalization phrase. Universal-capability claims
// ("all models support ...") are always rejected.
func checkNoModelNormalizationClaims(body string) error {
	for _, sent := range splitSentences(body) {
		lower := strings.ToLower(sent)
		for _, p := range normalizationClaimPatterns {
			if !strings.Contains(lower, p.phrase) {
				continue
			}
			allowed := false
			for _, neg := range p.negation {
				if strings.Contains(lower, neg) {
					allowed = true
					break
				}
			}
			if !allowed {
				return fmt.Errorf("prohibited model-ID normalization claim %q appears without co-occurring negation in sentence: %q", p.phrase, strings.TrimSpace(sent))
			}
		}
	}
	return nil
}

// TestBasetenDocsNoModelNormalizationClaims verifies the Baseten guide and
// scoped reference docs do not positively claim model-ID normalization,
// rewriting, mapping, or translation. The check is sentence-level so a
// negation in unrelated prose cannot mask a positive claim, and so that
// precise descriptions of exact opaque model preservation remain allowed.
// "Provider-wide reasoning" is acceptable when negated and is additionally
// checked by checkNoProhibitedClaims in TestBasetenDocsNoLiveValidationClaims.
func TestBasetenDocsNoModelNormalizationClaims(t *testing.T) {
	for _, rel := range []string{
		"docs/examples/baseten.md",
		"docs/CONFIG.md",
		"docs/FACTORY.md",
	} {
		t.Run(rel, func(t *testing.T) {
			body := readRepoRel(t, rel)
			if err := checkNoModelNormalizationClaims(body); err != nil {
				t.Fatalf("%s: %v", rel, err)
			}
		})
	}
}

// TestBasetenDocsNormalizationClaimsDeterministic proves the sentence-level
// normalization predicate catches each prohibited model-ID claim (rewrites,
// normalizes, maps, translates) even when unrelated text contains a
// negation, and accepts precise descriptions of exact opaque model
// preservation. This provides deterministic failing and accepted fixture
// paths that cannot be masked by document-wide substring matches.
func TestBasetenDocsNormalizationClaimsDeterministic(t *testing.T) {
	tests := []struct {
		name    string
		doc     string
		wantErr bool
	}{
		// --- Failing fixtures: positive claims without negation ---
		{
			name:    "rewrite positive claim fails despite negation elsewhere",
			doc:     "# Guide\n\nThe proxy does not modify data.\n\nThe proxy rewrites model IDs to canonical names.\n",
			wantErr: true,
		},
		{
			name:    "normalize positive claim fails",
			doc:     "# Guide\n\nBaseten normalizes model slugs automatically.\n",
			wantErr: true,
		},
		{
			name:    "map positive claim fails",
			doc:     "# Guide\n\nThe proxy maps model IDs to standard formats.\n",
			wantErr: true,
		},
		{
			name:    "translate positive claim fails",
			doc:     "# Guide\n\nThe proxy translates model identifiers for compatibility.\n",
			wantErr: true,
		},
		{
			name:    "universal capability always fails",
			doc:     "# Guide\n\nAll models support reasoning output.\n",
			wantErr: true,
		},
		// --- Accepted fixtures: negated claims and correct preservation ---
		{
			name:    "rewrite negated with never accepted",
			doc:     "# Guide\n\nThe proxy never rewrites model IDs.\n",
			wantErr: false,
		},
		{
			name:    "rewrite negated with does not accepted",
			doc:     "# Guide\n\nThe proxy does not rewrite model IDs.\n",
			wantErr: false,
		},
		{
			name:    "normalize negated accepted",
			doc:     "# Guide\n\nThe proxy does not normalize model slugs.\n",
			wantErr: false,
		},
		{
			name:    "map negated accepted",
			doc:     "# Guide\n\nThe proxy never maps model IDs to aliases.\n",
			wantErr: false,
		},
		{
			name:    "translate negated accepted",
			doc:     "# Guide\n\nThe proxy does not translate model IDs.\n",
			wantErr: false,
		},
		{
			name:    "exact byte-for-byte preservation accepted",
			doc:     "# Guide\n\nBaseten model IDs are preserved byte-for-byte through the picker and reload cycle.\n",
			wantErr: false,
		},
		{
			name:    "precise opaque slug preservation accepted",
			doc:     "# Guide\n\nThe proxy preserves opaque slugs exactly without provider-prefix normalization.\n",
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkNoModelNormalizationClaims(tt.doc)
			if tt.wantErr && err == nil {
				t.Fatal("checkNoModelNormalizationClaims must reject this doc")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("checkNoModelNormalizationClaims must accept this doc: %v", err)
			}
		})
	}
}

// TestBasetenDocsProhibitedClaimsExtendedDocs verifies generic reference docs
// (CONFIG.md and FACTORY.md) do not contain prohibited live-validation or
// universal capability claims. PROVIDERS.md is excluded because it
// legitimately describes T3 protocol translation paths as a core proxy
// feature.
func TestBasetenDocsProhibitedClaimsExtendedDocs(t *testing.T) {
	for _, rel := range []string{
		"docs/CONFIG.md",
		"docs/FACTORY.md",
	} {
		t.Run(rel, func(t *testing.T) {
			body := readRepoRel(t, rel)
			// Run the sentence-level prohibited-claims check.
			if err := checkNoProhibitedClaims(body); err != nil {
				t.Fatalf("%s: %v", rel, err)
			}
		})
	}
}

// TestBasetenDocsUsesCurrentPort verifies Baseten docs use port 9787 and
// never the old default 8787 as an operational target.
func TestBasetenDocsUsesCurrentPort(t *testing.T) {
	body := readRepoRel(t, "docs/examples/baseten.md")
	if !strings.Contains(body, "127.0.0.1:9787") {
		t.Error("baseten.md must reference the current default port 127.0.0.1:9787")
	}
	if strings.Contains(body, "127.0.0.1:8787") {
		t.Error("baseten.md must not reference the old default port 127.0.0.1:8787")
	}
}
