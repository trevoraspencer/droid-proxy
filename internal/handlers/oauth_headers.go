package handlers

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"github.com/trevoraspencer/droid-proxy/internal/config"
	"github.com/trevoraspencer/droid-proxy/internal/oauth"
)

// codexClientVersion is OpenAI's documented minimum Codex CLI version for GPT-5.6.
const (
	codexClientVersion   = "0.144.0"
	codexUserAgent       = "codex_cli_rs/" + codexClientVersion + " (Mac OS 26.3.1; arm64) droid-proxy"
	xaiGrokClientVersion = "0.2.22"
)

func oauthResponsesURL(m *config.Model, token *oauth.Token) (string, error) {
	baseURL := strings.TrimSpace(m.BaseURL)
	if baseURL == "" {
		baseURL = token.BaseURLForProvider(m.OAuthProvider)
	}
	if baseURL == "" {
		return "", fmt.Errorf("missing OAuth upstream base URL")
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("invalid OAuth upstream base URL: %w", err)
	}
	u.RawQuery = ""
	u.Fragment = ""
	u.User = nil
	u.Path = path.Join(u.Path, "responses")
	if !strings.HasPrefix(u.Path, "/") {
		u.Path = "/" + u.Path
	}
	return u.String(), nil
}

func applyOAuthResponsesHeaders(req *http.Request, downstream http.Header, m *config.Model, token *oauth.Token, payload []byte, installationID, conversationID string) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("Connection", "Keep-Alive")
	req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	switch m.OAuthProvider {
	case config.OAuthProviderCodex:
		req.Header.Set("User-Agent", firstHeader(downstream, "User-Agent", codexUserAgent))
		req.Header.Set("Version", firstHeader(downstream, "Version", codexClientVersion))
		req.Header.Set("Originator", firstHeader(downstream, "Originator", "codex_cli_rs"))
		req.Header.Set("OpenAI-Beta", firstHeader(downstream, "OpenAI-Beta", "responses_websockets=2026-02-06"))
		req.Header.Set("x-openai-internal-codex-residency", firstHeader(downstream, "x-openai-internal-codex-residency", "us"))
		if token.AccountID != "" {
			req.Header.Set("Chatgpt-Account-Id", token.AccountID)
		}
		if installationID != "" {
			req.Header.Set("x-codex-installation-id", installationID)
		}
		if conversationID == "" {
			conversationID = codexConversationID(downstream, payload)
			if conversationID == "" {
				conversationID = "droid-proxy-" + randomHex(16)
			}
		}
		req.Header.Set("x-client-request-id", firstHeader(downstream, "X-Client-Request-Id", conversationID))
		req.Header.Set("session_id", firstHeader(downstream, "session_id", conversationID))
		req.Header.Set("x-codex-window-id", firstHeader(downstream, "x-codex-window-id", conversationID+":0"))
		for _, name := range []string{"X-Codex-Beta-Features", "X-Codex-Turn-Metadata", "X-Codex-Turn-State", "X-Codex-Parent-Thread-Id", "X-ResponsesAPI-Include-Timing-Metrics"} {
			if v := strings.TrimSpace(downstream.Get(name)); v != "" {
				req.Header.Set(name, v)
			}
		}
	case config.OAuthProviderXAI:
		if xaiUsesCLIChatProxy(m, token) {
			if modelOverride := xaiModelOverride(m, payload); modelOverride != "" {
				req.Header.Set("x-grok-model-override", modelOverride)
			}
			req.Header.Set("x-grok-client-version", xaiGrokClientVersion)
		}
		if sessionID := oauthSessionID(downstream, payload); sessionID != "" {
			req.Header.Set("x-grok-conv-id", sessionID)
		}
	}
}

func codexClientMetadata(headers http.Header, installationID, conversationID string) map[string]string {
	metadata := map[string]string{}
	if installationID != "" {
		metadata["x-codex-installation-id"] = installationID
	}
	if conversationID != "" {
		metadata["x-codex-window-id"] = firstHeader(headers, "x-codex-window-id", conversationID+":0")
	}
	for _, name := range []string{"x-codex-turn-metadata", "x-codex-turn-state", "x-codex-parent-thread-id"} {
		if value := strings.TrimSpace(headers.Get(name)); value != "" {
			metadata[name] = value
		}
	}
	return metadata
}

func injectCodexClientMetadata(payload []byte, metadata map[string]string) []byte {
	if len(metadata) == 0 {
		return payload
	}
	out := payload
	for key, value := range metadata {
		if strings.TrimSpace(value) == "" {
			continue
		}
		if existing := gjson.GetBytes(out, "client_metadata."+key); existing.Exists() {
			continue
		}
		if next, err := sjson.SetBytes(out, "client_metadata."+key, value); err == nil {
			out = next
		}
	}
	return out
}

func codexConversationID(h http.Header, payload []byte) string {
	for _, v := range []string{
		h.Get("session_id"),
		h.Get("Session_id"),
		h.Get("X-Codex-Session-Id"),
		h.Get("X-Codex-Conversation-Id"),
		h.Get("X-Client-Request-Id"),
		gjson.GetBytes(payload, "prompt_cache_key").String(),
	} {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func randomHex(n int) string {
	if n <= 0 {
		n = 16
	}
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

func firstHeader(h http.Header, name, fallback string) string {
	if v := h.Get(name); strings.TrimSpace(v) != "" {
		return v
	}
	return fallback
}

func oauthSessionID(h http.Header, payload []byte) string {
	for _, v := range []string{
		h.Get("X-Session-ID"),
		h.Get("Session_id"),
		h.Get("X-Client-Request-Id"),
		gjson.GetBytes(payload, "prompt_cache_key").String(),
	} {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func xaiModelOverride(m *config.Model, payload []byte) string {
	candidates := []string{gjson.GetBytes(payload, "model").String()}
	if m != nil {
		candidates = append(candidates, m.UpstreamModel, m.Alias)
	}
	for _, v := range candidates {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func xaiUsesCLIChatProxy(m *config.Model, token *oauth.Token) bool {
	baseURL := ""
	if m != nil {
		baseURL = strings.TrimSpace(m.BaseURL)
	}
	if baseURL == "" && token != nil {
		baseURL = token.BaseURLForProvider(config.OAuthProviderXAI)
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Hostname(), "cli-chat-proxy.grok.com")
}
