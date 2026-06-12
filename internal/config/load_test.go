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
listen:
  port: 0
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
	if cfg.Listen.Port != 0 {
		t.Fatalf("explicit listen.port=0 overwritten: %d", cfg.Listen.Port)
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
  - alias: grok-composer-2.5-fast
    factory_provider: openai
    upstream_protocol: xai-responses
    oauth_provider: xai
    base_url: https://cli-chat-proxy.grok.com/v1
    upstream_model: grok-composer-2.5-fast
  - alias: grok-4.3
    factory_provider: openai
    upstream_protocol: xai-responses
    oauth_provider: xai
    upstream_model: grok-4.3
    capabilities:
      factory_reasoning: passthrough
`
	cfg, err := parse([]byte(in))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, m := range cfg.Models {
		if m.APIKeyEnv != "" || !m.AgentReady() {
			t.Fatalf("oauth model hydrated unexpected fields or not agent ready: %+v", m)
		}
		wantBaseURL := ""
		if m.Alias == "grok-composer-2.5-fast" {
			wantBaseURL = "https://cli-chat-proxy.grok.com/v1"
		}
		if m.BaseURL != wantBaseURL {
			t.Fatalf("%s base URL = %q, want %q", m.Alias, m.BaseURL, wantBaseURL)
		}
	}
	if got := cfg.Models[0].ResolvedCapabilities().FactoryReasoning; got != FactoryReasoningPassthrough {
		t.Fatalf("codex factory_reasoning default = %q, want passthrough", got)
	}
	if got := cfg.Models[1].ResolvedCapabilities().FactoryReasoning; got != FactoryReasoningDrop {
		t.Fatalf("xai factory_reasoning default = %q, want drop", got)
	}
	if got := cfg.Models[2].ResolvedCapabilities().FactoryReasoning; got != FactoryReasoningDrop {
		t.Fatalf("composer factory_reasoning default = %q, want drop", got)
	}
	if got := cfg.Models[3].ResolvedCapabilities().FactoryReasoning; got != FactoryReasoningPassthrough {
		t.Fatalf("explicit xai factory_reasoning = %q, want passthrough", got)
	}
}

func TestLoad_NormalizesServiceTierFastExtraArg(t *testing.T) {
	in := `
models:
  - alias: fast-tier
    factory_provider: openai
    upstream_protocol: openai-responses
    base_url: http://127.0.0.1:1/v1
    api_key_env: TEST_KEY
    extra_args:
      service_tier: " Fast "
  - alias: priority-tier
    factory_provider: openai
    upstream_protocol: openai-responses
    base_url: http://127.0.0.1:1/v1
    api_key_env: TEST_KEY
    extra_args:
      service_tier: priority
  - alias: numeric-tier
    factory_provider: openai
    upstream_protocol: openai-responses
    base_url: http://127.0.0.1:1/v1
    api_key_env: TEST_KEY
    extra_args:
      service_tier: 7
`
	cfg, err := parse([]byte(in))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := cfg.Models[0].ExtraArgs["service_tier"]; got != "priority" {
		t.Fatalf("fast service_tier = %#v, want priority", got)
	}
	if got := cfg.Models[1].ExtraArgs["service_tier"]; got != "priority" {
		t.Fatalf("priority service_tier = %#v, want unchanged priority", got)
	}
	if got := cfg.Models[2].ExtraArgs["service_tier"]; got != 7 {
		t.Fatalf("numeric service_tier = %#v, want unchanged 7", got)
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

func TestLoad_OAuthCallbackHostsMustBeLoopback(t *testing.T) {
	cases := []struct {
		name    string
		field   string
		host    string
		wantErr string
	}{
		{name: "codex localhost allowed", field: "codex_callback_host", host: "localhost"},
		{name: "codex IPv4 loopback allowed", field: "codex_callback_host", host: "127.0.0.1"},
		{name: "codex IPv6 loopback allowed", field: "codex_callback_host", host: "::1"},
		{name: "xai localhost allowed", field: "xai_callback_host", host: "localhost"},
		{name: "xai IPv4 loopback allowed", field: "xai_callback_host", host: "127.0.0.1"},
		{name: "xai IPv6 loopback allowed", field: "xai_callback_host", host: "::1"},
		{name: "codex wildcard rejected", field: "codex_callback_host", host: "0.0.0.0", wantErr: "oauth.codex_callback_host"},
		{name: "codex remote rejected", field: "codex_callback_host", host: "192.0.2.10", wantErr: "oauth.codex_callback_host"},
		{name: "xai wildcard rejected", field: "xai_callback_host", host: "::", wantErr: "oauth.xai_callback_host"},
		{name: "xai remote rejected", field: "xai_callback_host", host: "callback.example.test", wantErr: "oauth.xai_callback_host"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := `
oauth:
  ` + tc.field + `: "` + tc.host + `"
models:
  - alias: m
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    base_url: http://127.0.0.1:1/v1
`
			_, err := parse([]byte(in))
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("expected error containing %q, got: %v", tc.wantErr, err)
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
	if got := nestedString(cfg.Models[1].ExtraArgs, "thinking", "type"); got != "enabled" {
		t.Fatalf("deepseek thinking.type = %q, want enabled", got)
	}
	if got, _ := cfg.Models[1].ExtraArgs["reasoning_effort"].(string); got != "high" {
		t.Fatalf("deepseek reasoning_effort = %q, want high", got)
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
			if got := nestedString(m.ExtraArgs, "thinking", "type"); got != "enabled" {
				t.Fatalf("thinking.type = %q, want enabled", got)
			}
		})
	}
}

func TestLoad_KnownAuthExtraArgsExplicitOverridePreserved(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "secret")
	t.Setenv("MIMO_API_KEY", "secret")

	in := `
models:
  - alias: deepseek
    factory_provider: generic-chat-completion-api
    known_auth: deepseek
    extra_args:
      thinking:
        type: disabled
      reasoning_effort: max
  - alias: mimo
    factory_provider: generic-chat-completion-api
    known_auth: mimo
    extra_args:
      thinking:
        type: disabled
`
	cfg, err := parse([]byte(in))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := nestedString(cfg.Models[0].ExtraArgs, "thinking", "type"); got != "disabled" {
		t.Fatalf("deepseek explicit thinking.type overwritten: %q", got)
	}
	if got, _ := cfg.Models[0].ExtraArgs["reasoning_effort"].(string); got != "max" {
		t.Fatalf("deepseek explicit reasoning_effort overwritten: %q", got)
	}
	if got := nestedString(cfg.Models[1].ExtraArgs, "thinking", "type"); got != "disabled" {
		t.Fatalf("mimo explicit thinking.type overwritten: %q", got)
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
		{"invalid factory reasoning enum", "models:\n  - alias: m\n    factory_provider: openai\n    upstream_protocol: xai-responses\n    oauth_provider: xai\n    capabilities: {factory_reasoning: magic}\n", "capabilities.factory_reasoning"},
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

func nestedString(m map[string]any, key, nested string) string {
	child, _ := m[key].(map[string]any)
	got, _ := child[nested].(string)
	return got
}

// --- VAL-CONFIG-001: Existing configs load with load-balancing defaults ---

func TestLoad_OAuthLoadBalancingDefaults(t *testing.T) {
	cfg, err := parse([]byte(minimalValid))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	lb := cfg.OAuth.LoadBalancing
	if lb.Strategy != LoadBalancingSticky {
		t.Fatalf("default strategy = %q, want sticky", lb.Strategy)
	}
	if lb.MaxFailovers != 2 {
		t.Fatalf("default max_failovers = %d, want 2", lb.MaxFailovers)
	}
	if lb.RateLimitCooldown != 60*time.Second {
		t.Fatalf("default rate_limit_cooldown = %v, want 60s", lb.RateLimitCooldown)
	}
	if lb.ErrorCooldown != 30*time.Second {
		t.Fatalf("default error_cooldown = %v, want 30s", lb.ErrorCooldown)
	}
}

// Codex-only config without load_balancing also gets defaults.
func TestLoad_OAuthLoadBalancingDefaults_CodexConfig(t *testing.T) {
	in := `
models:
  - alias: codex
    factory_provider: openai
    upstream_protocol: codex-responses
    oauth_provider: codex
    upstream_model: gpt-5.3-codex
`
	cfg, err := parse([]byte(in))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	lb := cfg.OAuth.LoadBalancing
	if lb.Strategy != LoadBalancingSticky {
		t.Fatalf("default strategy = %q, want sticky", lb.Strategy)
	}
	if lb.MaxFailovers != 2 {
		t.Fatalf("default max_failovers = %d, want 2", lb.MaxFailovers)
	}
}

// --- VAL-CONFIG-002: Explicit and partial load-balancing blocks parse correctly ---

func TestLoad_OAuthLoadBalancingExplicitFull(t *testing.T) {
	in := `
oauth:
  load_balancing:
    strategy: fill-first
    max_failovers: 5
    rate_limit_cooldown: 120s
    error_cooldown: 45s
models:
  - alias: m1
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    base_url: http://127.0.0.1:1/v1
    api_key_env: TEST_KEY
`
	cfg, err := parse([]byte(in))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	lb := cfg.OAuth.LoadBalancing
	if lb.Strategy != LoadBalancingFillFirst {
		t.Fatalf("strategy = %q, want fill-first", lb.Strategy)
	}
	if lb.MaxFailovers != 5 {
		t.Fatalf("max_failovers = %d, want 5", lb.MaxFailovers)
	}
	if lb.RateLimitCooldown != 120*time.Second {
		t.Fatalf("rate_limit_cooldown = %v, want 120s", lb.RateLimitCooldown)
	}
	if lb.ErrorCooldown != 45*time.Second {
		t.Fatalf("error_cooldown = %v, want 45s", lb.ErrorCooldown)
	}
}

func TestLoad_OAuthLoadBalancingPartialBlocks(t *testing.T) {
	cases := []struct {
		name      string
		yaml      string
		wantStrat LoadBalancingStrategy
		wantMax   int
		wantRLCD  time.Duration
		wantECD   time.Duration
	}{
		{
			name: "only strategy",
			yaml: `
oauth:
  load_balancing:
    strategy: random
models:
  - alias: m1
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    base_url: http://127.0.0.1:1/v1
    api_key_env: TEST_KEY
`,
			wantStrat: LoadBalancingRandom,
			wantMax:   2,
			wantRLCD:  60 * time.Second,
			wantECD:   30 * time.Second,
		},
		{
			name: "only max_failovers",
			yaml: `
oauth:
  load_balancing:
    max_failovers: 3
models:
  - alias: m1
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    base_url: http://127.0.0.1:1/v1
    api_key_env: TEST_KEY
`,
			wantStrat: LoadBalancingSticky,
			wantMax:   3,
			wantRLCD:  60 * time.Second,
			wantECD:   30 * time.Second,
		},
		{
			name: "only rate_limit_cooldown",
			yaml: `
oauth:
  load_balancing:
    rate_limit_cooldown: 90s
models:
  - alias: m1
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    base_url: http://127.0.0.1:1/v1
    api_key_env: TEST_KEY
`,
			wantStrat: LoadBalancingSticky,
			wantMax:   2,
			wantRLCD:  90 * time.Second,
			wantECD:   30 * time.Second,
		},
		{
			name: "only error_cooldown",
			yaml: `
oauth:
  load_balancing:
    error_cooldown: 15s
models:
  - alias: m1
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    base_url: http://127.0.0.1:1/v1
    api_key_env: TEST_KEY
`,
			wantStrat: LoadBalancingSticky,
			wantMax:   2,
			wantRLCD:  60 * time.Second,
			wantECD:   15 * time.Second,
		},
		{
			name: "strategy and error_cooldown",
			yaml: `
oauth:
  load_balancing:
    strategy: least-connections
    error_cooldown: 10s
models:
  - alias: m1
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    base_url: http://127.0.0.1:1/v1
    api_key_env: TEST_KEY
`,
			wantStrat: LoadBalancingLeastConnections,
			wantMax:   2,
			wantRLCD:  60 * time.Second,
			wantECD:   10 * time.Second,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := parse([]byte(tc.yaml))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			lb := cfg.OAuth.LoadBalancing
			if lb.Strategy != tc.wantStrat {
				t.Errorf("strategy = %q, want %q", lb.Strategy, tc.wantStrat)
			}
			if lb.MaxFailovers != tc.wantMax {
				t.Errorf("max_failovers = %d, want %d", lb.MaxFailovers, tc.wantMax)
			}
			if lb.RateLimitCooldown != tc.wantRLCD {
				t.Errorf("rate_limit_cooldown = %v, want %v", lb.RateLimitCooldown, tc.wantRLCD)
			}
			if lb.ErrorCooldown != tc.wantECD {
				t.Errorf("error_cooldown = %v, want %v", lb.ErrorCooldown, tc.wantECD)
			}
		})
	}
}

// Empty load_balancing block gets all defaults.
func TestLoad_OAuthLoadBalancingEmptyBlock(t *testing.T) {
	in := `
oauth:
  load_balancing: {}
models:
  - alias: m1
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    base_url: http://127.0.0.1:1/v1
    api_key_env: TEST_KEY
`
	cfg, err := parse([]byte(in))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	lb := cfg.OAuth.LoadBalancing
	if lb.Strategy != LoadBalancingSticky {
		t.Fatalf("default strategy = %q, want sticky", lb.Strategy)
	}
	if lb.MaxFailovers != 2 {
		t.Fatalf("default max_failovers = %d, want 2", lb.MaxFailovers)
	}
}

// --- VAL-CONFIG-003: Approved strategies are accepted ---

func TestLoad_OAuthLoadBalancingApprovedStrategies(t *testing.T) {
	for _, strat := range []LoadBalancingStrategy{
		LoadBalancingSticky,
		LoadBalancingRoundRobin,
		LoadBalancingFillFirst,
		LoadBalancingLeastConnections,
		LoadBalancingRandom,
	} {
		t.Run(string(strat), func(t *testing.T) {
			in := `
oauth:
  load_balancing:
    strategy: ` + string(strat) + `
models:
  - alias: m1
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    base_url: http://127.0.0.1:1/v1
    api_key_env: TEST_KEY
`
			cfg, err := parse([]byte(in))
			if err != nil {
				t.Fatalf("strategy %q should be accepted, got: %v", strat, err)
			}
			if cfg.OAuth.LoadBalancing.Strategy != strat {
				t.Fatalf("strategy = %q, want %q", cfg.OAuth.LoadBalancing.Strategy, strat)
			}
		})
	}
}

// --- VAL-CONFIG-004: Invalid strategies and scalar values are rejected ---

func TestLoad_OAuthLoadBalancingInvalidValues(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantErr string
	}{
		{
			name: "invalid strategy",
			body: `
oauth:
  load_balancing:
    strategy: weighted
models:
  - alias: m1
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    base_url: http://127.0.0.1:1/v1
    api_key_env: TEST_KEY
`,
			wantErr: "oauth.load_balancing.strategy",
		},
		{
			name: "negative max_failovers",
			body: `
oauth:
  load_balancing:
    max_failovers: -1
models:
  - alias: m1
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    base_url: http://127.0.0.1:1/v1
    api_key_env: TEST_KEY
`,
			wantErr: "oauth.load_balancing.max_failovers must not be negative",
		},
		{
			name: "negative rate_limit_cooldown",
			body: `
oauth:
  load_balancing:
    rate_limit_cooldown: -5s
models:
  - alias: m1
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    base_url: http://127.0.0.1:1/v1
    api_key_env: TEST_KEY
`,
			wantErr: "oauth.load_balancing.rate_limit_cooldown must not be negative",
		},
		{
			name: "negative error_cooldown",
			body: `
oauth:
  load_balancing:
    error_cooldown: -10s
models:
  - alias: m1
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    base_url: http://127.0.0.1:1/v1
    api_key_env: TEST_KEY
`,
			wantErr: "oauth.load_balancing.error_cooldown must not be negative",
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

// max_failovers=0 is accepted and means no additional failover attempts.
func TestLoad_OAuthLoadBalancingMaxFailoversZero(t *testing.T) {
	in := `
oauth:
  load_balancing:
    max_failovers: 0
models:
  - alias: m1
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    base_url: http://127.0.0.1:1/v1
    api_key_env: TEST_KEY
`
	cfg, err := parse([]byte(in))
	if err != nil {
		t.Fatalf("max_failovers=0 should be accepted, got: %v", err)
	}
	if cfg.OAuth.LoadBalancing.MaxFailovers != 0 {
		t.Fatalf("max_failovers = %d, want 0", cfg.OAuth.LoadBalancing.MaxFailovers)
	}
}

// Zero-duration cooldowns are accepted as no-cooldown values.
func TestLoad_OAuthLoadBalancingZeroDurationCooldowns(t *testing.T) {
	in := `
oauth:
  load_balancing:
    rate_limit_cooldown: 0s
    error_cooldown: 0s
models:
  - alias: m1
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    base_url: http://127.0.0.1:1/v1
    api_key_env: TEST_KEY
`
	cfg, err := parse([]byte(in))
	if err != nil {
		t.Fatalf("zero-duration cooldowns should be accepted, got: %v", err)
	}
	if cfg.OAuth.LoadBalancing.RateLimitCooldown != 0 {
		t.Fatalf("rate_limit_cooldown = %v, want 0s", cfg.OAuth.LoadBalancing.RateLimitCooldown)
	}
	if cfg.OAuth.LoadBalancing.ErrorCooldown != 0 {
		t.Fatalf("error_cooldown = %v, want 0s", cfg.OAuth.LoadBalancing.ErrorCooldown)
	}
}

// --- VAL-CONFIG-005: Strict YAML rejects unknown load-balancing keys ---

func TestLoad_OAuthLoadBalancingUnknownKeys(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantErr string
	}{
		{
			name: "unknown key mode",
			body: `
oauth:
  load_balancing:
    mode: active
models:
  - alias: m1
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    base_url: http://127.0.0.1:1/v1
    api_key_env: TEST_KEY
`,
			wantErr: "field mode not found",
		},
		{
			name: "misspelled cooldown field",
			body: `
oauth:
  load_balancing:
    rate_limit_cd: 30s
models:
  - alias: m1
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    base_url: http://127.0.0.1:1/v1
    api_key_env: TEST_KEY
`,
			wantErr: "field rate_limit_cd not found",
		},
		{
			name: "unknown nested key priority",
			body: `
oauth:
  load_balancing:
    priority: high
models:
  - alias: m1
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    base_url: http://127.0.0.1:1/v1
    api_key_env: TEST_KEY
`,
			wantErr: "field priority not found",
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
