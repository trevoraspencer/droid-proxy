package config

import (
	"strings"
	"testing"
	"time"
)

const minimalValid = `
models:
  - alias: m1
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    base_url: http://127.0.0.1:1/v1
    api_key_env: TEST_KEY
`

func TestLoad_MinimalValidDefaults(t *testing.T) {
	cfg, err := parse([]byte(minimalValid))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Listen.Host != "127.0.0.1" || cfg.Listen.Port != 8787 {
		t.Fatalf("listen defaults wrong: %+v", cfg.Listen)
	}
	if cfg.Server.RequestBodyMaxBytes != 10<<20 {
		t.Fatalf("request_body_max_bytes default wrong: %d", cfg.Server.RequestBodyMaxBytes)
	}
	if cfg.Server.ReadHeaderTimeout != 30*time.Second {
		t.Fatalf("read_header_timeout default wrong: %v", cfg.Server.ReadHeaderTimeout)
	}
	if cfg.Server.ReadTimeout != 60*time.Second {
		t.Fatalf("read_timeout default wrong: %v", cfg.Server.ReadTimeout)
	}
	if cfg.Server.WriteTimeout != 600*time.Second {
		t.Fatalf("write_timeout default wrong: %v", cfg.Server.WriteTimeout)
	}
	if cfg.Server.IdleTimeout != 120*time.Second {
		t.Fatalf("idle_timeout default wrong: %v", cfg.Server.IdleTimeout)
	}
	if cfg.Server.ShutdownTimeout != 5*time.Second {
		t.Fatalf("shutdown_timeout default wrong: %v", cfg.Server.ShutdownTimeout)
	}
	if cfg.Logging.Level != "info" {
		t.Fatalf("logging level default not applied: %q", cfg.Logging.Level)
	}
	if cfg.Upstream.HTTPTimeout != 600*time.Second {
		t.Fatalf("http_timeout default wrong: %v", cfg.Upstream.HTTPTimeout)
	}
	if cfg.ReasoningCache.MaxEntries != 1024 {
		t.Fatalf("reasoning_cache.max_entries default wrong: %d", cfg.ReasoningCache.MaxEntries)
	}
	if !cfg.ReasoningCache.Enabled {
		t.Fatalf("reasoning_cache.enabled default not applied")
	}
	if !cfg.Logging.Redact {
		t.Fatalf("logging.redact default not applied")
	}
	if cfg.Upstream.StreamKeepAlive != 15*time.Second {
		t.Fatalf("stream_keep_alive default wrong: %v", cfg.Upstream.StreamKeepAlive)
	}
	if cfg.Upstream.ResponseBodyMaxBytes != 100<<20 {
		t.Fatalf("response_body_max_bytes default wrong: %d", cfg.Upstream.ResponseBodyMaxBytes)
	}
	if cfg.Upstream.ErrorBodyMaxBytes != 1<<20 {
		t.Fatalf("error_body_max_bytes default wrong: %d", cfg.Upstream.ErrorBodyMaxBytes)
	}
	if cfg.OAuth.AuthDir != "~/.droid-proxy/auth" {
		t.Fatalf("oauth.auth_dir default wrong: %q", cfg.OAuth.AuthDir)
	}
	if cfg.OAuth.CodexCallbackHost != "localhost" || cfg.OAuth.CodexCallbackPort != 1455 {
		t.Fatalf("codex callback defaults wrong: %+v", cfg.OAuth)
	}
	if cfg.OAuth.XAICallbackHost != "127.0.0.1" || cfg.OAuth.XAICallbackPort != 56121 {
		t.Fatalf("xai callback defaults wrong: %+v", cfg.OAuth)
	}
	if cfg.Models[0].Alias != "m1" {
		t.Fatalf("model alias missing")
	}
}

func TestLoad_PresenceAwareDefaultsPreserveOptOuts(t *testing.T) {
	in := `
client_auth:
  enabled: true
  api_keys: ["raw-key"]
  scheme: ""
logging:
  redact: false
reasoning_cache:
  enabled: false
server:
  request_body_max_bytes: 0
  read_header_timeout: 0s
  read_timeout: 0s
  write_timeout: 0s
  idle_timeout: 0s
  shutdown_timeout: 0s
upstream:
  stream_keep_alive: 0s
  response_body_max_bytes: 0
  error_body_max_bytes: 0
models:
  - alias: m
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    base_url: http://127.0.0.1:1/v1
`
	cfg, err := parse([]byte(in))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.ClientAuth.Scheme != "" {
		t.Fatalf("explicit raw auth scheme overwritten: %q", cfg.ClientAuth.Scheme)
	}
	if cfg.Logging.Redact {
		t.Fatalf("explicit logging.redact=false overwritten")
	}
	if cfg.ReasoningCache.Enabled {
		t.Fatalf("explicit reasoning_cache.enabled=false overwritten")
	}
	if cfg.Server.RequestBodyMaxBytes != 0 {
		t.Fatalf("explicit request_body_max_bytes=0 overwritten: %d", cfg.Server.RequestBodyMaxBytes)
	}
	if cfg.Server.ReadHeaderTimeout != 0 {
		t.Fatalf("explicit read_header_timeout=0 overwritten: %v", cfg.Server.ReadHeaderTimeout)
	}
	if cfg.Server.ReadTimeout != 0 {
		t.Fatalf("explicit read_timeout=0 overwritten: %v", cfg.Server.ReadTimeout)
	}
	if cfg.Server.WriteTimeout != 0 {
		t.Fatalf("explicit write_timeout=0 overwritten: %v", cfg.Server.WriteTimeout)
	}
	if cfg.Server.IdleTimeout != 0 {
		t.Fatalf("explicit idle_timeout=0 overwritten: %v", cfg.Server.IdleTimeout)
	}
	if cfg.Server.ShutdownTimeout != 0 {
		t.Fatalf("explicit shutdown_timeout=0 overwritten: %v", cfg.Server.ShutdownTimeout)
	}
	if cfg.Upstream.StreamKeepAlive != 0 {
		t.Fatalf("explicit stream_keep_alive=0 overwritten: %v", cfg.Upstream.StreamKeepAlive)
	}
	if cfg.Upstream.ResponseBodyMaxBytes != 0 {
		t.Fatalf("explicit response_body_max_bytes=0 overwritten: %d", cfg.Upstream.ResponseBodyMaxBytes)
	}
	if cfg.Upstream.ErrorBodyMaxBytes != 0 {
		t.Fatalf("explicit error_body_max_bytes=0 overwritten: %d", cfg.Upstream.ErrorBodyMaxBytes)
	}
}

func TestLoad_InvalidProviderProtocolCombo(t *testing.T) {
	in := `
models:
  - alias: bad
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-responses
    base_url: http://127.0.0.1:1/v1
`
	_, err := parse([]byte(in))
	if err == nil {
		t.Fatal("expected error for generic + openai-responses, got nil")
	}
	if !strings.Contains(err.Error(), "does not support upstream_protocol") {
		t.Fatalf("expected combo error, got: %v", err)
	}
}

func TestLoad_AllowedCombos(t *testing.T) {
	for _, tc := range []struct {
		name string
		fp   string
		up   string
	}{
		{"generic+chat", "generic-chat-completion-api", "openai-chat"},
		{"openai+responses", "openai", "openai-responses"},
		{"openai+chat", "openai", "openai-chat"},
		{"openai+codex oauth", "openai", "codex-responses"},
		{"openai+xai oauth", "openai", "xai-responses"},
		{"anthropic+messages", "anthropic", "anthropic-messages"},
		{"anthropic+chat", "anthropic", "openai-chat"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			oauthProvider := ""
			if tc.up == "codex-responses" {
				oauthProvider = "\n    oauth_provider: codex"
			}
			if tc.up == "xai-responses" {
				oauthProvider = "\n    oauth_provider: xai"
			}
			in := `
models:
  - alias: m
    factory_provider: ` + tc.fp + `
    upstream_protocol: ` + tc.up + `
    base_url: http://127.0.0.1:1/v1` + oauthProvider + `
`
			if _, err := parse([]byte(in)); err != nil {
				t.Fatalf("expected ok, got: %v", err)
			}
		})
	}
}

func TestLoad_OAuthModelDoesNotRequireBaseURLOrAPIKey(t *testing.T) {
	in := `
models:
  - alias: codex
    factory_provider: openai
    upstream_protocol: codex-responses
    oauth_provider: codex
    upstream_model: gpt-5.3-codex
  - alias: grok-build
    factory_provider: openai
    upstream_protocol: xai-responses
    oauth_provider: xai
    upstream_model: grok-build-0.1
`
	cfg, err := parse([]byte(in))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, m := range cfg.Models {
		if m.BaseURL != "" || m.APIKeyEnv != "" || !m.AgentReady() {
			t.Fatalf("oauth model hydrated unexpected fields or not agent ready: %+v", m)
		}
	}
}

func TestLoad_OAuthModelValidation(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantErr string
	}{
		{
			name: "missing oauth_provider",
			body: `
models:
  - alias: m
    factory_provider: openai
    upstream_protocol: codex-responses
`,
			wantErr: "oauth_provider is required",
		},
		{
			name: "mismatched oauth_provider",
			body: `
models:
  - alias: m
    factory_provider: openai
    upstream_protocol: codex-responses
    oauth_provider: xai
`,
			wantErr: `requires oauth_provider "codex"`,
		},
		{
			name: "oauth_provider on non oauth upstream",
			body: `
models:
  - alias: m
    factory_provider: openai
    upstream_protocol: openai-responses
    oauth_provider: codex
    base_url: http://127.0.0.1:1/v1
`,
			wantErr: "oauth_provider is only valid",
		},
		{
			name: "blank auth dir",
			body: `
oauth:
  auth_dir: ""
models:
  - alias: m
    factory_provider: openai
    upstream_protocol: codex-responses
    oauth_provider: codex
`,
			wantErr: "oauth.auth_dir must not be blank",
		},
		{
			name: "bad codex callback port",
			body: `
oauth:
  codex_callback_port: 70000
models:
  - alias: m
    factory_provider: openai
    upstream_protocol: codex-responses
    oauth_provider: codex
`,
			wantErr: "oauth.codex_callback_port",
		},
		{
			name: "bad xai callback port",
			body: `
oauth:
  xai_callback_port: 70000
models:
  - alias: m
    factory_provider: openai
    upstream_protocol: xai-responses
    oauth_provider: xai
`,
			wantErr: "oauth.xai_callback_port",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parse([]byte(tc.body))
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected error containing %q, got: %v", tc.wantErr, err)
			}
		})
	}
}

func TestLoad_DuplicateAliases(t *testing.T) {
	in := `
models:
  - alias: dup
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    base_url: http://127.0.0.1:1/v1
  - alias: dup
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    base_url: http://127.0.0.1:2/v1
`
	_, err := parse([]byte(in))
	if err == nil || !strings.Contains(err.Error(), "duplicate model alias") {
		t.Fatalf("expected duplicate alias error, got: %v", err)
	}
}

func TestLoad_NoModels(t *testing.T) {
	_, err := parse([]byte(`listen: {port: 9000}`))
	if err == nil || !strings.Contains(err.Error(), "at least one model") {
		t.Fatalf("expected no-models error, got: %v", err)
	}
}

func TestLoad_ClientAuthEnabledRequiresKeys(t *testing.T) {
	in := `
client_auth:
  enabled: true
models:
  - alias: m
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    base_url: http://127.0.0.1:1/v1
`
	_, err := parse([]byte(in))
	if err == nil || !strings.Contains(err.Error(), "api_keys entry") {
		t.Fatalf("expected client_auth keys error, got: %v", err)
	}
}

func TestLoad_ClientAuthRejectsBlankExpandedKeys(t *testing.T) {
	in := `
client_auth:
  enabled: true
  api_keys:
    - "${MISSING_TEST_CLIENT_KEY}"
models:
  - alias: m
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    base_url: http://127.0.0.1:1/v1
`
	_, err := parse([]byte(in))
	if err == nil || !strings.Contains(err.Error(), "blank after env expansion") {
		t.Fatalf("expected blank key error, got: %v", err)
	}
}

func TestLoad_NonLoopbackListenRequiresClientAuth(t *testing.T) {
	cases := []struct {
		name    string
		host    string
		auth    string
		wantErr bool
	}{
		{name: "wildcard IPv4 without auth rejected", host: "0.0.0.0", wantErr: true},
		{name: "wildcard IPv6 without auth rejected", host: "::", wantErr: true},
		{name: "remote IPv4 without auth rejected", host: "192.0.2.10", wantErr: true},
		{name: "remote hostname without auth rejected", host: "proxy.example.test", wantErr: true},
		{name: "loopback IPv4 without auth allowed", host: "127.0.0.1"},
		{name: "localhost without auth allowed", host: "localhost"},
		{name: "loopback IPv6 without auth allowed", host: "::1"},
		{
			name: "wildcard with auth allowed",
			host: "0.0.0.0",
			auth: `
client_auth:
  enabled: true
  api_keys: ["test-key"]
`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := `
listen:
  host: "` + tc.host + `"
  port: 8787
` + tc.auth + `
models:
  - alias: m
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    base_url: http://127.0.0.1:1/v1
`
			_, err := parse([]byte(in))
			if tc.wantErr {
				if err == nil || !strings.Contains(err.Error(), "client_auth.enabled: true") {
					t.Fatalf("expected non-loopback client_auth error, got: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("expected ok, got: %v", err)
			}
		})
	}
}

func TestLoad_UpstreamURLValidation(t *testing.T) {
	t.Setenv("REMOTE_KEY", "secret")
	cases := []struct {
		name    string
		baseURL string
		apiEnv  string
		wantErr string
	}{
		{"remote https accepted with key", "https://api.example.test/v1", "REMOTE_KEY", ""},
		{"loopback http accepted without key", "http://127.0.0.1:1234/v1", "", ""},
		{"localhost http accepted without key", "http://localhost:1234/v1", "", ""},
		{"remote http rejected", "http://api.example.test/v1", "REMOTE_KEY", "non-loopback http"},
		{"relative rejected", "/v1", "", "absolute http(s) URL"},
		{"userinfo rejected", "https://user:pass@example.test/v1", "REMOTE_KEY", "userinfo"},
		{"query rejected", "https://example.test/v1?token=x", "REMOTE_KEY", "query or fragment"},
		{"fragment rejected", "https://example.test/v1#frag", "REMOTE_KEY", "query or fragment"},
		{"scheme rejected", "ftp://example.test/v1", "REMOTE_KEY", "scheme"},
		{"remote https missing key source", "https://api.example.test/v1", "", "requires api_key_env"},
		{"remote https blank key", "https://api.example.test/v1", "MISSING_REMOTE_KEY", "env var MISSING_REMOTE_KEY is empty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			api := ""
			if tc.apiEnv != "" {
				api = "\n    api_key_env: " + tc.apiEnv
			}
			in := `
models:
  - alias: m
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    base_url: ` + tc.baseURL + api + `
`
			_, err := parse([]byte(in))
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("expected ok, got: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected error containing %q, got: %v", tc.wantErr, err)
			}
		})
	}
}

func TestLoad_KnownAuthLocalNoAuthAndDeepSeekReasoning(t *testing.T) {
	in := `
models:
  - alias: local
    factory_provider: generic-chat-completion-api
    known_auth: ollama
    upstream_model: llama
  - alias: deepseek
    factory_provider: generic-chat-completion-api
    known_auth: deepseek
    base_url: http://127.0.0.1:1234/v1
    upstream_model: deepseek-test
  - alias: deepseek-no-reasoning
    factory_provider: generic-chat-completion-api
    known_auth: deepseek
    base_url: http://127.0.0.1:1235/v1
    capabilities:
      reasoning: none
`
	cfg, err := parse([]byte(in))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Models[0].APIKeyEnv != "" {
		t.Fatalf("ollama should not hydrate api_key_env, got %q", cfg.Models[0].APIKeyEnv)
	}
	if cfg.Models[1].BaseURL != "http://127.0.0.1:1234/v1" {
		t.Fatalf("known_auth overwrote explicit fake base_url: %q", cfg.Models[1].BaseURL)
	}
	if cfg.Models[1].Capabilities.Reasoning != ReasoningDeepSeek {
		t.Fatalf("deepseek did not hydrate reasoning: %q", cfg.Models[1].Capabilities.Reasoning)
	}
	if cfg.Models[2].Capabilities.Reasoning != ReasoningNone {
		t.Fatalf("explicit reasoning override overwritten: %q", cfg.Models[2].Capabilities.Reasoning)
	}
}

func TestLoad_KnownAuthMimoProfilesHydrate(t *testing.T) {
	for env := range map[string]string{
		"MIMO_API_KEY":                "secret",
		"MIMO_TOKEN_PLAN_CN_API_KEY":  "secret",
		"MIMO_TOKEN_PLAN_SGP_API_KEY": "secret",
		"MIMO_TOKEN_PLAN_AMS_API_KEY": "secret",
	} {
		t.Setenv(env, "secret")
	}

	cases := []struct {
		knownAuth string
		baseURL   string
		apiEnv    string
	}{
		{"mimo", "https://api.xiaomimimo.com/v1", "MIMO_API_KEY"},
		{"mimo-token-plan-cn", "https://token-plan-cn.xiaomimimo.com/v1", "MIMO_TOKEN_PLAN_CN_API_KEY"},
		{"mimo-token-plan-sgp", "https://token-plan-sgp.xiaomimimo.com/v1", "MIMO_TOKEN_PLAN_SGP_API_KEY"},
		{"mimo-token-plan-ams", "https://token-plan-ams.xiaomimimo.com/v1", "MIMO_TOKEN_PLAN_AMS_API_KEY"},
	}
	for _, tc := range cases {
		t.Run(tc.knownAuth, func(t *testing.T) {
			in := `
models:
  - alias: m
    factory_provider: generic-chat-completion-api
    known_auth: ` + tc.knownAuth + `
`
			cfg, err := parse([]byte(in))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			m := cfg.Models[0]
			if m.BaseURL != tc.baseURL {
				t.Fatalf("base_url = %q, want %q", m.BaseURL, tc.baseURL)
			}
			if m.APIKeyEnv != tc.apiEnv {
				t.Fatalf("api_key_env = %q, want %q", m.APIKeyEnv, tc.apiEnv)
			}
			if m.UpstreamProtocol != UpstreamOpenAIChat {
				t.Fatalf("upstream_protocol = %q, want %q", m.UpstreamProtocol, UpstreamOpenAIChat)
			}
			if m.Capabilities.Reasoning != ReasoningDeepSeek {
				t.Fatalf("reasoning = %q, want %q", m.Capabilities.Reasoning, ReasoningDeepSeek)
			}
		})
	}
}

func TestLoad_KnownAuthZAIProfilesHydrate(t *testing.T) {
	t.Setenv("ZAI_API_KEY", "legacy-secret")
	t.Setenv("ZAI_MAIN_API_KEY", "main-secret")
	t.Setenv("ZAI_CODING_API_KEY", "coding-secret")

	cases := []struct {
		knownAuth string
		baseURL   string
		apiEnv    string
	}{
		{"zai", "https://api.z.ai/api/paas/v4", "ZAI_API_KEY"},
		{"zai-main-api", "https://api.z.ai/api/paas/v4", "ZAI_MAIN_API_KEY"},
		{"zai-coding-api", "https://api.z.ai/api/coding/paas/v4", "ZAI_CODING_API_KEY"},
	}
	for _, tc := range cases {
		t.Run(tc.knownAuth, func(t *testing.T) {
			in := `
models:
  - alias: m
    factory_provider: generic-chat-completion-api
    known_auth: ` + tc.knownAuth + `
`
			cfg, err := parse([]byte(in))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			m := cfg.Models[0]
			if m.BaseURL != tc.baseURL {
				t.Fatalf("base_url = %q, want %q", m.BaseURL, tc.baseURL)
			}
			if m.APIKeyEnv != tc.apiEnv {
				t.Fatalf("api_key_env = %q, want %q", m.APIKeyEnv, tc.apiEnv)
			}
			if m.UpstreamProtocol != UpstreamOpenAIChat {
				t.Fatalf("upstream_protocol = %q, want %q", m.UpstreamProtocol, UpstreamOpenAIChat)
			}
		})
	}
}

func TestLoad_KnownAuthMimoReasoningOverridePreserved(t *testing.T) {
	t.Setenv("MIMO_API_KEY", "secret")
	in := `
models:
  - alias: m
    factory_provider: generic-chat-completion-api
    known_auth: mimo
    capabilities:
      reasoning: none
`
	cfg, err := parse([]byte(in))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Models[0].Capabilities.Reasoning != ReasoningNone {
		t.Fatalf("explicit reasoning override overwritten: %q", cfg.Models[0].Capabilities.Reasoning)
	}
}

func TestLoad_KnownAuthKeptProvidersHydrateAndDroppedProvidersFail(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "secret")
	t.Setenv("OPENAI_API_KEY", "secret")
	t.Setenv("ANTHROPIC_API_KEY", "secret")
	t.Setenv("XAI_API_KEY", "secret")
	t.Setenv("MOONSHOT_API_KEY", "secret")
	t.Setenv("GROQ_API_KEY", "secret")
	t.Setenv("FIREWORKS_API_KEY", "secret")
	t.Setenv("ZAI_API_KEY", "secret")
	t.Setenv("ZAI_MAIN_API_KEY", "secret")
	t.Setenv("ZAI_CODING_API_KEY", "secret")
	t.Setenv("MIMO_API_KEY", "secret")
	t.Setenv("MIMO_TOKEN_PLAN_CN_API_KEY", "secret")
	t.Setenv("MIMO_TOKEN_PLAN_SGP_API_KEY", "secret")
	t.Setenv("MIMO_TOKEN_PLAN_AMS_API_KEY", "secret")

	for _, knownAuth := range []string{"deepseek", "openai", "anthropic", "xai", "kimi", "groq", "fireworks", "zai", "zai-main-api", "zai-coding-api", "mimo", "mimo-token-plan-cn", "mimo-token-plan-sgp", "mimo-token-plan-ams", "ollama", "vllm"} {
		t.Run("kept "+knownAuth, func(t *testing.T) {
			fp := "generic-chat-completion-api"
			if knownAuth == "openai" {
				fp = "openai"
			}
			if knownAuth == "anthropic" {
				fp = "anthropic"
			}
			in := `
models:
  - alias: m
    factory_provider: ` + fp + `
    known_auth: ` + knownAuth + `
`
			cfg, err := parse([]byte(in))
			if err != nil {
				t.Fatalf("expected kept known_auth %q to load, got: %v", knownAuth, err)
			}
			if cfg.Models[0].BaseURL == "" || cfg.Models[0].UpstreamProtocol == "" {
				t.Fatalf("known_auth %q did not hydrate model: %+v", knownAuth, cfg.Models[0])
			}
		})
	}

	for _, knownAuth := range []string{"mistral", "iflow", "together"} {
		t.Run("dropped "+knownAuth, func(t *testing.T) {
			in := `
models:
  - alias: m
    factory_provider: generic-chat-completion-api
    known_auth: ` + knownAuth + `
`
			_, err := parse([]byte(in))
			if err == nil || !strings.Contains(err.Error(), "unknown known_auth") {
				t.Fatalf("expected dropped known_auth %q to fail, got: %v", knownAuth, err)
			}
		})
	}
}

func TestLoad_KnownAuthNoAuthExemptionRequiresLoopback(t *testing.T) {
	t.Setenv("REMOTE_LOCAL_PROVIDER_KEY", "secret")
	cases := []struct {
		name      string
		knownAuth string
		baseURL   string
		apiEnv    string
		wantErr   string
	}{
		{
			name:      "ollama loopback without key accepted",
			knownAuth: "ollama",
			baseURL:   "http://127.0.0.1:11434/v1",
		},
		{
			name:      "vllm loopback without key accepted",
			knownAuth: "vllm",
			baseURL:   "http://localhost:8000/v1",
		},
		{
			name:      "ollama remote without key rejected",
			knownAuth: "ollama",
			baseURL:   "https://remote-ollama.example/v1",
			wantErr:   "requires api_key_env",
		},
		{
			name:      "vllm remote without key rejected",
			knownAuth: "vllm",
			baseURL:   "https://remote-vllm.example/v1",
			wantErr:   "requires api_key_env",
		},
		{
			name:      "ollama remote blank key rejected",
			knownAuth: "ollama",
			baseURL:   "https://remote-ollama.example/v1",
			apiEnv:    "MISSING_REMOTE_LOCAL_PROVIDER_KEY",
			wantErr:   "env var MISSING_REMOTE_LOCAL_PROVIDER_KEY is empty",
		},
		{
			name:      "vllm remote explicit key accepted",
			knownAuth: "vllm",
			baseURL:   "https://remote-vllm.example/v1",
			apiEnv:    "REMOTE_LOCAL_PROVIDER_KEY",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			api := ""
			if tc.apiEnv != "" {
				api = "\n    api_key_env: " + tc.apiEnv
			}
			in := `
models:
  - alias: m
    factory_provider: generic-chat-completion-api
    known_auth: ` + tc.knownAuth + `
    base_url: ` + tc.baseURL + api + `
`
			_, err := parse([]byte(in))
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("expected ok, got: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected error containing %q, got: %v", tc.wantErr, err)
			}
		})
	}
}

func TestLoad_InvalidSchemaEnumsAndValues(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantErr string
	}{
		{"unknown key", "bogus: true\nmodels: []\n", "field bogus not found"},
		{"invalid logging level", "logging: {level: verbose}\n", "logging.level"},
		{"invalid logging format", "logging: {format: xml}\n", "logging.format"},
		{"negative reasoning entries", "reasoning_cache: {max_entries: -1}\n", "reasoning_cache.max_entries"},
		{"negative ttl", "reasoning_cache: {ttl: -1s}\n", "reasoning_cache.ttl"},
		{"negative request body cap", "server: {request_body_max_bytes: -1}\n", "server.request_body_max_bytes"},
		{"negative read header timeout", "server: {read_header_timeout: -1s}\n", "server.read_header_timeout"},
		{"negative read timeout", "server: {read_timeout: -1s}\n", "server.read_timeout"},
		{"negative write timeout", "server: {write_timeout: -1s}\n", "server.write_timeout"},
		{"negative idle timeout", "server: {idle_timeout: -1s}\n", "server.idle_timeout"},
		{"negative shutdown timeout", "server: {shutdown_timeout: -1s}\n", "server.shutdown_timeout"},
		{"negative upstream timeout", "upstream: {http_timeout: -1s}\n", "upstream.http_timeout"},
		{"negative keepalive", "upstream: {stream_keep_alive: -1s}\n", "upstream.stream_keep_alive"},
		{"negative upstream response cap", "upstream: {response_body_max_bytes: -1}\n", "upstream.response_body_max_bytes"},
		{"negative upstream error cap", "upstream: {error_body_max_bytes: -1}\n", "upstream.error_body_max_bytes"},
		{"invalid reasoning enum", "models:\n  - alias: m\n    factory_provider: generic-chat-completion-api\n    upstream_protocol: openai-chat\n    base_url: http://127.0.0.1:1/v1\n    capabilities: {reasoning: magic}\n", "capabilities.reasoning"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := tc.body
			if !strings.Contains(body, "models:") {
				body += "models:\n  - alias: m\n    factory_provider: generic-chat-completion-api\n    upstream_protocol: openai-chat\n    base_url: http://127.0.0.1:1/v1\n"
			}
			_, err := parse([]byte(body))
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected error containing %q, got: %v", tc.wantErr, err)
			}
		})
	}
}

func TestExpandEnv(t *testing.T) {
	t.Setenv("FOO", "set-value")
	cases := []struct{ in, want string }{
		{"${FOO}", "set-value"},
		{"${MISSING}", ""},
		{"${MISSING:-default}", "default"},
		{"${MISSING:-}", ""},
		{"prefix-${FOO}-suffix", "prefix-set-value-suffix"},
		{"${FOO}/${MISSING:-fallback}", "set-value/fallback"},
		{"no refs here", "no refs here"},
	}
	for _, c := range cases {
		if got := expandEnv(c.in); got != c.want {
			t.Errorf("expandEnv(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestLoad_EnvExpansionInValues(t *testing.T) {
	t.Setenv("CUSTOM_URL", "http://127.0.0.1:3/v1")
	in := `
models:
  - alias: m
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    base_url: ${CUSTOM_URL}
`
	cfg, err := parse([]byte(in))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Models[0].BaseURL != "http://127.0.0.1:3/v1" {
		t.Fatalf("env expansion failed: %q", cfg.Models[0].BaseURL)
	}
}

func TestResolvedCapabilities_Defaults(t *testing.T) {
	m := &Model{}
	r := m.ResolvedCapabilities()
	if !r.Streaming || !r.Tools || !r.ToolResultSafe {
		t.Fatalf("expected streaming/tools/tool_result_safe defaults true, got %+v", r)
	}
	if r.Images || r.StructuredOutput || r.PromptCaching {
		t.Fatalf("expected images/structured_output/prompt_caching default false, got %+v", r)
	}
	if !r.AgentReady() {
		t.Fatalf("default model should be agent_ready")
	}
}

func TestResolvedCapabilities_Overrides(t *testing.T) {
	m := &Model{Capabilities: Capabilities{
		Streaming:      boolPtr(false),
		Tools:          boolPtr(true),
		ToolResultSafe: boolPtr(true),
	}}
	r := m.ResolvedCapabilities()
	if r.Streaming {
		t.Fatalf("expected streaming=false override")
	}
	if r.AgentReady() {
		t.Fatalf("model with streaming=false should not be agent_ready")
	}
}

func TestModelAgentReadyRequiresSupportedRuntimePath(t *testing.T) {
	for _, tt := range []struct {
		name  string
		model *Model
		want  bool
	}{
		{"generic chat", &Model{FactoryProvider: FactoryProviderGeneric, UpstreamProtocol: UpstreamOpenAIChat}, true},
		{"responses native", &Model{FactoryProvider: FactoryProviderOpenAI, UpstreamProtocol: UpstreamOpenAIResponses}, true},
		{"responses over chat", &Model{FactoryProvider: FactoryProviderOpenAI, UpstreamProtocol: UpstreamOpenAIChat}, true},
		{"codex responses oauth", &Model{FactoryProvider: FactoryProviderOpenAI, UpstreamProtocol: UpstreamCodexResponses}, true},
		{"xai responses oauth", &Model{FactoryProvider: FactoryProviderOpenAI, UpstreamProtocol: UpstreamXAIResponses}, true},
		{"messages native", &Model{FactoryProvider: FactoryProviderAnthropic, UpstreamProtocol: UpstreamAnthropicMessages}, true},
		{"messages over chat", &Model{FactoryProvider: FactoryProviderAnthropic, UpstreamProtocol: UpstreamOpenAIChat}, true},
		{"unsupported provider protocol", &Model{FactoryProvider: FactoryProviderGeneric, UpstreamProtocol: UpstreamOpenAIResponses}, false},
		{"capability disabled", &Model{FactoryProvider: FactoryProviderGeneric, UpstreamProtocol: UpstreamOpenAIChat, Capabilities: Capabilities{ToolResultSafe: boolPtr(false)}}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.model.AgentReady(); got != tt.want {
				t.Fatalf("AgentReady()=%v, want %v", got, tt.want)
			}
		})
	}
}
