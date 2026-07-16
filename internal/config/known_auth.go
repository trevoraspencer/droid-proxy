package config

import (
	"fmt"
	"sort"
	"strings"
)

// DiscoveryPolicy controls how the TUI discovers available models for a
// provider profile.
type DiscoveryPolicy string

const (
	// DiscoveryRemote calls the configured model-list endpoint and falls back
	// to manual entry. This is the default when the field is empty.
	DiscoveryRemote DiscoveryPolicy = ""
	// DiscoveryStatic presents curated catalog choices without a network call,
	// always alongside manual entry.
	DiscoveryStatic DiscoveryPolicy = "static"
	// DiscoveryManual skips discovery and goes directly to manual entry.
	DiscoveryManual DiscoveryPolicy = "manual"
)

// CatalogEntry is a curated model or router ID with an optional display label.
type CatalogEntry struct {
	ID    string
	Label string
}

// KnownAuth describes a canonical provider's defaults: base URL, env var that
// holds the API key, and the default upstream protocol it speaks. These are
// used by config loading to fill in fields the user did not specify explicitly.
type KnownAuth struct {
	Name             string
	BaseURL          string
	APIKeyEnv        string
	UpstreamProtocol UpstreamProtocol
	NoAuth           bool
	DefaultReasoning ReasoningMode
	// ModelsPath is appended to BaseURL for interactive model discovery.
	// Empty means "models", which fits OpenAI-compatible /v1 base URLs.
	ModelsPath string
	// ExtraArgs are merged into every outgoing request to this provider unless
	// the model config already sets that top-level key.
	ExtraArgs map[string]any
	// AuthHeader and AuthScheme override the defaults (Authorization / Bearer)
	// used for OpenAI-compatible providers. Empty AuthScheme means raw header value.
	AuthHeader string
	AuthScheme string
	// ExtraHeaders are appended to every outgoing request to this provider.
	ExtraHeaders map[string]string
	// DiscoveryPolicy controls TUI model discovery behavior for this profile.
	// Empty means remote best-effort discovery (the default).
	DiscoveryPolicy DiscoveryPolicy
	// StaticModels is the curated catalog used when DiscoveryPolicy is static.
	StaticModels []CatalogEntry
}

// knownAuthRegistry holds canonical defaults for providers droid-proxy ships
// support for. Adding a provider here only changes defaults; users can override
// every field in config.yaml. OpenRouter is intentionally excluded per project policy.
var knownAuthRegistry = map[string]KnownAuth{
	"deepseek": {
		Name: "deepseek", BaseURL: "https://api.deepseek.com/v1",
		APIKeyEnv: "DEEPSEEK_API_KEY", UpstreamProtocol: UpstreamOpenAIChat,
		DefaultReasoning: ReasoningDeepSeek,
		ExtraArgs: map[string]any{
			"thinking":         map[string]any{"type": "enabled"},
			"reasoning_effort": "high",
		},
	},
	"openai": {
		Name: "openai", BaseURL: "https://api.openai.com/v1",
		APIKeyEnv: "OPENAI_API_KEY", UpstreamProtocol: UpstreamOpenAIResponses,
	},
	"anthropic": {
		Name: "anthropic", BaseURL: "https://api.anthropic.com",
		APIKeyEnv: "ANTHROPIC_API_KEY", UpstreamProtocol: UpstreamAnthropicMessages,
		AuthHeader: "x-api-key",
		ModelsPath: "/v1/models",
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
	"fireworks": {
		Name: "fireworks", BaseURL: "https://api.fireworks.ai/inference/v1",
		APIKeyEnv: "FIREWORKS_API_KEY", UpstreamProtocol: UpstreamOpenAIChat,
	},
	"fireworks-fire-pass": {
		Name: "fireworks-fire-pass", BaseURL: "https://api.fireworks.ai/inference/v1",
		APIKeyEnv:        "FIREWORKS_FIRE_PASS_API_KEY",
		UpstreamProtocol: UpstreamOpenAIChat,
		DiscoveryPolicy:  DiscoveryStatic,
		StaticModels:     fireworksFirePassCatalog(),
	},
	"baseten": {
		Name: "baseten", BaseURL: "https://inference.baseten.co/v1",
		APIKeyEnv: "BASETEN_API_KEY", UpstreamProtocol: UpstreamOpenAIChat,
	},
	"zai": {
		Name: "zai", BaseURL: "https://api.z.ai/api/paas/v4",
		APIKeyEnv: "ZAI_API_KEY", UpstreamProtocol: UpstreamOpenAIChat,
	},
	"zai-main-api": {
		Name: "zai-main-api", BaseURL: "https://api.z.ai/api/paas/v4",
		APIKeyEnv: "ZAI_MAIN_API_KEY", UpstreamProtocol: UpstreamOpenAIChat,
	},
	"zai-coding-api": {
		Name: "zai-coding-api", BaseURL: "https://api.z.ai/api/coding/paas/v4",
		APIKeyEnv: "ZAI_CODING_API_KEY", UpstreamProtocol: UpstreamOpenAIChat,
	},
	"mimo": {
		Name: "mimo", BaseURL: "https://api.xiaomimimo.com/v1",
		APIKeyEnv: "MIMO_API_KEY", UpstreamProtocol: UpstreamOpenAIChat,
		DefaultReasoning: ReasoningDeepSeek,
		AuthHeader:       "api-key",
		ExtraArgs:        thinkingEnabledExtraArgs(),
	},
	"mimo-token-plan-cn": {
		Name: "mimo-token-plan-cn", BaseURL: "https://token-plan-cn.xiaomimimo.com/v1",
		APIKeyEnv: "MIMO_TOKEN_PLAN_CN_API_KEY", UpstreamProtocol: UpstreamOpenAIChat,
		DefaultReasoning: ReasoningDeepSeek,
		AuthHeader:       "api-key",
		ExtraArgs:        thinkingEnabledExtraArgs(),
	},
	"mimo-token-plan-sgp": {
		Name: "mimo-token-plan-sgp", BaseURL: "https://token-plan-sgp.xiaomimimo.com/v1",
		APIKeyEnv: "MIMO_TOKEN_PLAN_SGP_API_KEY", UpstreamProtocol: UpstreamOpenAIChat,
		DefaultReasoning: ReasoningDeepSeek,
		AuthHeader:       "api-key",
		ExtraArgs:        thinkingEnabledExtraArgs(),
	},
	"mimo-token-plan-ams": {
		Name: "mimo-token-plan-ams", BaseURL: "https://token-plan-ams.xiaomimimo.com/v1",
		APIKeyEnv: "MIMO_TOKEN_PLAN_AMS_API_KEY", UpstreamProtocol: UpstreamOpenAIChat,
		DefaultReasoning: ReasoningDeepSeek,
		AuthHeader:       "api-key",
		ExtraArgs:        thinkingEnabledExtraArgs(),
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

func thinkingEnabledExtraArgs() map[string]any {
	return map[string]any{"thinking": map[string]any{"type": "enabled"}}
}

// knownAuthLabels maps registry keys to human-friendly labels for pickers.
// Missing entries fall back to the registry key.
var knownAuthLabels = map[string]string{
	"deepseek":            "DeepSeek",
	"openai":              "OpenAI",
	"anthropic":           "Anthropic",
	"xai":                 "xAI (Grok, API key)",
	"kimi":                "Kimi (Moonshot)",
	"groq":                "Groq",
	"fireworks":           "Fireworks AI",
	"fireworks-fire-pass": "Fireworks AI (Fire Pass)",
	"baseten":             "Baseten",
	"zai":                 "Z.AI (main, legacy alias)",
	"zai-main-api":        "Z.AI (main API)",
	"zai-coding-api":      "Z.AI (GLM Coding Plan)",
	"mimo":                "Xiaomi MiMo",
	"mimo-token-plan-cn":  "Xiaomi MiMo (Token Plan, CN)",
	"mimo-token-plan-sgp": "Xiaomi MiMo (Token Plan, SGP)",
	"mimo-token-plan-ams": "Xiaomi MiMo (Token Plan, AMS)",
	"ollama":              "Ollama (local)",
	"vllm":                "vLLM (local)",
}

// Label returns a human-friendly display name for the provider.
func (k KnownAuth) Label() string {
	if l, ok := knownAuthLabels[k.Name]; ok {
		return l
	}
	return k.Name
}

// LookupKnownAuth returns a known auth descriptor by lowercase name.
func LookupKnownAuth(name string) (KnownAuth, bool) {
	a, ok := knownAuthRegistry[strings.ToLower(strings.TrimSpace(name))]
	return a, ok
}

// KnownAuthList returns all registered providers sorted by name. Used by the
// interactive config UI to render a provider picker.
func KnownAuthList() []KnownAuth {
	out := make([]KnownAuth, 0, len(knownAuthRegistry))
	for _, ka := range knownAuthRegistry {
		out = append(out, ka)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
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
	if ka.DefaultReasoning != "" && m.Capabilities.Reasoning == "" {
		m.Capabilities.Reasoning = ka.DefaultReasoning
	}
	if len(ka.ExtraArgs) > 0 {
		if m.ExtraArgs == nil {
			m.ExtraArgs = make(map[string]any, len(ka.ExtraArgs))
		}
		for k, v := range ka.ExtraArgs {
			if _, set := m.ExtraArgs[k]; !set {
				m.ExtraArgs[k] = v
			}
		}
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
