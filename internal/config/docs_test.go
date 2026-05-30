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
		"logs",
		"service install",
		"service uninstall",
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
