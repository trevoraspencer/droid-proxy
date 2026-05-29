package config

import (
	"fmt"
	"strings"
)

// KnownAuth describes a canonical provider's defaults: base URL, env var that
// holds the API key, and the default upstream protocol it speaks. These are
// used by config loading to fill in fields the user did not specify explicitly.
type KnownAuth struct {
	Name             string
	BaseURL          string
	APIKeyEnv        string
	UpstreamProtocol UpstreamProtocol
	NoAuth           bool
	// AuthHeader and AuthScheme override the defaults (Authorization / Bearer)
	// used for OpenAI-compatible providers. Anthropic uses x-api-key with no scheme.
	AuthHeader string
	AuthScheme string
	// ExtraHeaders are appended to every outgoing request to this provider.
	ExtraHeaders map[string]string
}

// knownAuthRegistry holds canonical defaults for providers droid-proxy ships
// support for. Adding a provider here only changes defaults; users can override
// every field in config.yaml. OpenRouter is intentionally excluded per project policy.
var knownAuthRegistry = map[string]KnownAuth{
	"deepseek": {
		Name: "deepseek", BaseURL: "https://api.deepseek.com/v1",
		APIKeyEnv: "DEEPSEEK_API_KEY", UpstreamProtocol: UpstreamOpenAIChat,
	},
	"openai": {
		Name: "openai", BaseURL: "https://api.openai.com/v1",
		APIKeyEnv: "OPENAI_API_KEY", UpstreamProtocol: UpstreamOpenAIResponses,
	},
	"anthropic": {
		Name: "anthropic", BaseURL: "https://api.anthropic.com",
		APIKeyEnv: "ANTHROPIC_API_KEY", UpstreamProtocol: UpstreamAnthropicMessages,
		AuthHeader: "x-api-key",
		ExtraHeaders: map[string]string{
			"anthropic-version": "2023-06-01",
		},
	},
	"xai": {
		Name: "xai", BaseURL: "https://api.x.ai/v1",
		APIKeyEnv: "XAI_API_KEY", UpstreamProtocol: UpstreamOpenAIChat,
	},
	"kimi": {
		Name: "kimi", BaseURL: "https://api.moonshot.cn/v1",
		APIKeyEnv: "MOONSHOT_API_KEY", UpstreamProtocol: UpstreamOpenAIChat,
	},
	"groq": {
		Name: "groq", BaseURL: "https://api.groq.com/openai/v1",
		APIKeyEnv: "GROQ_API_KEY", UpstreamProtocol: UpstreamOpenAIChat,
	},
	"together": {
		Name: "together", BaseURL: "https://api.together.xyz/v1",
		APIKeyEnv: "TOGETHER_API_KEY", UpstreamProtocol: UpstreamOpenAIChat,
	},
	"fireworks": {
		Name: "fireworks", BaseURL: "https://api.fireworks.ai/inference/v1",
		APIKeyEnv: "FIREWORKS_API_KEY", UpstreamProtocol: UpstreamOpenAIChat,
	},
	"mistral": {
		Name: "mistral", BaseURL: "https://api.mistral.ai/v1",
		APIKeyEnv: "MISTRAL_API_KEY", UpstreamProtocol: UpstreamOpenAIChat,
	},
	"zai": {
		Name: "zai", BaseURL: "https://api.z.ai/api/paas/v4",
		APIKeyEnv: "ZAI_API_KEY", UpstreamProtocol: UpstreamOpenAIChat,
	},
	"iflow": {
		Name: "iflow", BaseURL: "https://apis.iflow.cn/v1",
		APIKeyEnv: "IFLOW_API_KEY", UpstreamProtocol: UpstreamOpenAIChat,
	},
	"ollama": {
		Name: "ollama", BaseURL: "http://127.0.0.1:11434/v1",
		UpstreamProtocol: UpstreamOpenAIChat, NoAuth: true,
	},
	"vllm": {
		Name: "vllm", BaseURL: "http://127.0.0.1:8000/v1",
		UpstreamProtocol: UpstreamOpenAIChat, NoAuth: true,
	},
}

// LookupKnownAuth returns a known auth descriptor by lowercase name.
func LookupKnownAuth(name string) (KnownAuth, bool) {
	a, ok := knownAuthRegistry[strings.ToLower(strings.TrimSpace(name))]
	return a, ok
}

// HydrateModel fills empty fields on m from the known_auth registry (if set).
// Explicit fields on m always win; this only fills the gaps.
func HydrateModel(m *Model) error {
	if m == nil || strings.TrimSpace(m.KnownAuth) == "" {
		return nil
	}
	ka, ok := LookupKnownAuth(m.KnownAuth)
	if !ok {
		return fmt.Errorf("model %q: unknown known_auth %q", m.Alias, m.KnownAuth)
	}
	if m.BaseURL == "" {
		m.BaseURL = ka.BaseURL
	}
	if m.APIKeyEnv == "" && !ka.NoAuth {
		m.APIKeyEnv = ka.APIKeyEnv
	}
	if m.UpstreamProtocol == "" {
		m.UpstreamProtocol = ka.UpstreamProtocol
	}
	if ka.Name == "deepseek" && m.Capabilities.Reasoning == "" {
		m.Capabilities.Reasoning = ReasoningDeepSeek
	}
	if len(ka.ExtraHeaders) > 0 {
		if m.ExtraHeaders == nil {
			m.ExtraHeaders = make(map[string]string, len(ka.ExtraHeaders))
		}
		for k, v := range ka.ExtraHeaders {
			if _, set := m.ExtraHeaders[k]; !set {
				m.ExtraHeaders[k] = v
			}
		}
	}
	return nil
}
