package tui

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"regexp"
	"strings"

	"droid-proxy/internal/config"
	"droid-proxy/internal/factory"
	"droid-proxy/internal/secrets"
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
			Label:            "Grok 4.3",
			Alias:            "grok-4.3",
			DisplayName:      "Grok 4.3 (xAI OAuth)",
			UpstreamModel:    "grok-4.3",
			MaxOutputTokens:  factory.DefaultMaxOutputTokens,
			MaxContextTokens: 1000000,
		},
	}
}

func xaiOAuthPickItems() []string {
	presets := xaiOAuthPresets()
	out := make([]string, 0, len(presets)+1)
	out = append(out, manualEntryLabel)
	for _, preset := range presets {
		out = append(out, preset.Label)
	}
	return out
}

func xaiOAuthPresetByLabel(label string) (oauthModelPreset, bool) {
	for _, preset := range xaiOAuthPresets() {
		if preset.Label == label {
			return preset, true
		}
	}
	return oauthModelPreset{}, false
}

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
