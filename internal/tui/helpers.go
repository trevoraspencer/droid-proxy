package tui

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"regexp"
	"strings"

	"github.com/trevoraspencer/droid-proxy/internal/config"
	"github.com/trevoraspencer/droid-proxy/internal/factory"
	"github.com/trevoraspencer/droid-proxy/internal/secrets"
)

func (b *backend) keyEnvSet(env string) bool {
	return strings.TrimSpace(os.Getenv(strings.TrimSpace(env))) != ""
}

func buildProviderChoices() []providerChoice {
	var out []providerChoice
	for _, ka := range config.KnownAuthList() {
		out = append(out, providerChoice{kind: pkKnown, ka: ka, label: ka.Label()})
	}
	out = append(out,
		providerChoice{kind: pkOAuth, oauth: config.OAuthProviderCodex, label: "Codex / ChatGPT (OAuth)"},
		providerChoice{kind: pkOAuth, oauth: config.OAuthProviderXAI, label: "xAI OAuth"},
		providerChoice{kind: pkCustom, label: "Custom OpenAI-compatible endpoint"},
	)
	return out
}

func codexOAuthPresets() []oauthModelPreset {
	return []oauthModelPreset{
		codexGPT56Preset("GPT-5.6 Sol (Recommended)", "gpt-5.6", "GPT-5.6 Sol (Codex OAuth)", "gpt-5.6-sol", false),
		codexGPT56Preset("GPT-5.6 Sol Fast", "gpt-5.6-fast", "GPT-5.6 Sol Fast (Codex OAuth)", "gpt-5.6-sol", true),
		codexGPT56Preset("GPT-5.6 Terra", "gpt-5.6-terra", "GPT-5.6 Terra (Codex OAuth)", "gpt-5.6-terra", false),
		codexGPT56Preset("GPT-5.6 Terra Fast", "gpt-5.6-terra-fast", "GPT-5.6 Terra Fast (Codex OAuth)", "gpt-5.6-terra", true),
		codexGPT56Preset("GPT-5.6 Luna", "gpt-5.6-luna", "GPT-5.6 Luna (Codex OAuth)", "gpt-5.6-luna", false),
		codexGPT56Preset("GPT-5.6 Luna Fast", "gpt-5.6-luna-fast", "GPT-5.6 Luna Fast (Codex OAuth)", "gpt-5.6-luna", true),
	}
}

func codexGPT56Preset(label, alias, displayName, upstreamModel string, fast bool) oauthModelPreset {
	preset := oauthModelPreset{
		Label:            label,
		Alias:            alias,
		DisplayName:      displayName,
		UpstreamModel:    upstreamModel,
		MaxOutputTokens:  128000,
		MaxContextTokens: 1050000,
		Capabilities: config.Capabilities{
			Streaming:        boolValue(true),
			Tools:            boolValue(true),
			ToolResultSafe:   boolValue(true),
			Images:           boolValue(true),
			JSONMode:         boolValue(true),
			StructuredOutput: boolValue(true),
			FactoryReasoning: config.FactoryReasoningPassthrough,
			PromptCaching:    boolValue(true),
		},
	}
	if fast {
		preset.ExtraArgs = map[string]any{"service_tier": "priority"}
	}
	return preset
}

func xaiOAuthPresets() []oauthModelPreset {
	return []oauthModelPreset{
		{
			Label:            "Grok Build 0.1",
			Alias:            "grok-build-0.1",
			DisplayName:      "Grok Build 0.1 (xAI OAuth)",
			UpstreamModel:    "grok-build-0.1",
			MaxOutputTokens:  factory.DefaultMaxOutputTokens,
			MaxContextTokens: 256000,
		},
		{
			Label:            "Composer 2.5 Fast",
			Alias:            "grok-composer-2.5-fast",
			DisplayName:      "Composer 2.5 Fast (xAI OAuth)",
			UpstreamModel:    "grok-composer-2.5-fast",
			BaseURL:          "https://cli-chat-proxy.grok.com/v1",
			MaxOutputTokens:  factory.DefaultMaxOutputTokens,
			MaxContextTokens: 200000,
		},
		{
			Label:            "Grok 4.3",
			Alias:            "grok-4.3",
			DisplayName:      "Grok 4.3 (xAI OAuth)",
			UpstreamModel:    "grok-4.3",
			MaxOutputTokens:  factory.DefaultMaxOutputTokens,
			MaxContextTokens: 1000000,
		},
	}
}

func oauthModelPresets(provider config.OAuthProvider) []oauthModelPreset {
	switch provider {
	case config.OAuthProviderCodex:
		return codexOAuthPresets()
	case config.OAuthProviderXAI:
		return xaiOAuthPresets()
	default:
		return nil
	}
}

func oauthPickItems(provider config.OAuthProvider) []string {
	presets := oauthModelPresets(provider)
	out := make([]string, 0, len(presets)+1)
	out = append(out, manualEntryLabel)
	for _, preset := range presets {
		out = append(out, preset.Label)
	}
	return out
}

func oauthPresetByLabel(provider config.OAuthProvider, label string) (oauthModelPreset, bool) {
	for _, preset := range oauthModelPresets(provider) {
		if preset.Label == label {
			return preset, true
		}
	}
	return oauthModelPreset{}, false
}

func cloneOAuthPreset(p oauthModelPreset) oauthModelPreset {
	p.ExtraArgs = cloneAnyMap(p.ExtraArgs)
	p.Capabilities = cloneCapabilities(p.Capabilities)
	return p
}

func cloneAnyMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func cloneCapabilities(in config.Capabilities) config.Capabilities {
	in.Streaming = cloneBool(in.Streaming)
	in.Tools = cloneBool(in.Tools)
	in.ToolResultSafe = cloneBool(in.ToolResultSafe)
	in.Images = cloneBool(in.Images)
	in.JSONMode = cloneBool(in.JSONMode)
	in.StructuredOutput = cloneBool(in.StructuredOutput)
	in.PromptCaching = cloneBool(in.PromptCaching)
	return in
}

func cloneBool(in *bool) *bool {
	if in == nil {
		return nil
	}
	return boolValue(*in)
}

func boolValue(value bool) *bool { return &value }

func factoryReasoningForOAuthModel(provider config.OAuthProvider, upstreamModel string) config.FactoryReasoningMode {
	if provider != config.OAuthProviderXAI {
		return ""
	}
	if strings.EqualFold(strings.TrimSpace(upstreamModel), "grok-4.3") {
		return config.FactoryReasoningPassthrough
	}
	return config.FactoryReasoningDrop
}

func factoryProviderFor(up config.UpstreamProtocol) config.FactoryProvider {
	switch up {
	case config.UpstreamOpenAIResponses:
		return config.FactoryProviderOpenAI
	case config.UpstreamAnthropicMessages:
		return config.FactoryProviderAnthropic
	default:
		return config.FactoryProviderGeneric
	}
}

func upstreamForOAuth(p config.OAuthProvider) config.UpstreamProtocol {
	if p == config.OAuthProviderXAI {
		return config.UpstreamXAIResponses
	}
	return config.UpstreamCodexResponses
}

var aliasSanitize = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

func defaultAlias(modelID string) string {
	id := modelID
	if i := strings.LastIndex(id, "/"); i >= 0 {
		id = id[i+1:]
	}
	id = aliasSanitize.ReplaceAllString(id, "-")
	return strings.Trim(strings.ToLower(id), "-")
}

func defaultDisplay(modelID, label string) string {
	base := modelID
	if i := strings.LastIndex(base, "/"); i >= 0 {
		base = base[i+1:]
	}
	if label == "" {
		return base
	}
	return fmt.Sprintf("%s (%s)", base, label)
}

func isLoopbackBaseURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host == "localhost" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

func secretsPathHint() string {
	return secrets.Path()
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
