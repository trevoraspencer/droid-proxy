package oauth

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"droid-proxy/internal/config"
)

const (
	ProviderCodex config.OAuthProvider = config.OAuthProviderCodex
	ProviderXAI   config.OAuthProvider = config.OAuthProviderXAI

	CodexAuthURL        = "https://auth.openai.com/oauth/authorize"
	CodexTokenURL       = "https://auth.openai.com/oauth/token"
	CodexClientID       = "app_EMoamEEZ73f0CkXaXp7hrann"
	CodexScope          = "openid profile email offline_access api.connectors.read api.connectors.invoke"
	CodexDefaultBaseURL = "https://chatgpt.com/backend-api/codex"

	XAIIssuer         = "https://auth.x.ai"
	XAIDiscoveryURL   = XAIIssuer + "/.well-known/openid-configuration"
	XAIClientID       = "b1a00492-073a-47ea-816f-4c329264a828"
	XAIScope          = "openid profile email offline_access grok-cli:access api:access"
	XAIDefaultBaseURL = "https://api.x.ai/v1"
)

const refreshLead = 5 * time.Minute

type Token struct {
	Type             string      `json:"type"`
	AccessToken      string      `json:"access_token"`
	RefreshToken     string      `json:"refresh_token,omitempty"`
	IDToken          string      `json:"id_token,omitempty"`
	TokenType        string      `json:"token_type,omitempty"`
	ExpiresIn        int         `json:"expires_in,omitempty"`
	Expired          string      `json:"expired,omitempty"`
	LastRefresh      string      `json:"last_refresh,omitempty"`
	Email            string      `json:"email,omitempty"`
	Subject          string      `json:"sub,omitempty"`
	AccountID        string      `json:"account_id,omitempty"`
	BaseURL          string      `json:"base_url,omitempty"`
	RedirectURI      string      `json:"redirect_uri,omitempty"`
	TokenEndpoint    string      `json:"token_endpoint,omitempty"`
	AuthKind         string      `json:"auth_kind,omitempty"`
	Disabled         bool        `json:"disabled,omitempty"`
	CodexQuota       *CodexQuota `json:"codex_quota,omitempty"`
	RateLimitResetAt string      `json:"rate_limit_reset_at,omitempty"`
	LastSeenAt       string      `json:"last_seen_at,omitempty"`

	path string
}

type CodexQuota struct {
	Primary    *CodexQuotaWindow `json:"primary,omitempty"`
	Secondary  *CodexQuotaWindow `json:"secondary,omitempty"`
	CodeReview *CodexQuotaWindow `json:"code_review,omitempty"`
}

type CodexQuotaWindow struct {
	UsedPercent      float64  `json:"used_percent"`
	RemainingPercent *float64 `json:"remaining_percent,omitempty"`
	WindowMinutes    *float64 `json:"window_minutes,omitempty"`
	ResetAt          *int64   `json:"reset_at,omitempty"`
	LimitReached     bool     `json:"limit_reached,omitempty"`
}

func (t *Token) Provider() config.OAuthProvider {
	return config.OAuthProvider(strings.ToLower(strings.TrimSpace(t.Type)))
}

func (t *Token) BaseURLForProvider(provider config.OAuthProvider) string {
	if strings.TrimSpace(t.BaseURL) != "" {
		return strings.TrimRight(strings.TrimSpace(t.BaseURL), "/")
	}
	switch provider {
	case ProviderCodex:
		return CodexDefaultBaseURL
	case ProviderXAI:
		return XAIDefaultBaseURL
	default:
		return ""
	}
}

func (t *Token) Expiry() (time.Time, bool) {
	raw := strings.TrimSpace(t.Expired)
	if raw == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if tm, err := time.Parse(layout, raw); err == nil {
			return tm, true
		}
	}
	return time.Time{}, false
}

func (t *Token) Path() string {
	if t == nil {
		return ""
	}
	return t.path
}

func (t *Token) AccountSelector() string {
	if t == nil {
		return ""
	}
	for _, v := range []string{t.Email, t.Subject, t.AccountID} {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	if strings.TrimSpace(t.path) != "" {
		return strings.TrimSuffix(filepath.Base(t.path), filepath.Ext(t.path))
	}
	return ""
}

func (t *Token) NeedsRefresh(now time.Time) bool {
	exp, ok := t.Expiry()
	return ok && now.Add(refreshLead).After(exp)
}

func (t *Token) MatchesAccount(account string) bool {
	account = strings.TrimSpace(account)
	if account == "" {
		return true
	}
	values := []string{
		t.Email,
		t.Subject,
		t.AccountID,
		strings.TrimSuffix(filepath.Base(t.path), filepath.Ext(t.path)),
		filepath.Base(t.path),
	}
	for _, v := range values {
		if strings.EqualFold(strings.TrimSpace(v), account) {
			return true
		}
	}
	return false
}

type Manager struct {
	cfg          *config.Config
	mu           sync.Mutex
	refreshLocks map[string]*sync.Mutex
}

func NewManager(cfg *config.Config) *Manager {
	return &Manager{cfg: cfg, refreshLocks: make(map[string]*sync.Mutex)}
}

func (m *Manager) AuthDir() (string, error) {
	dir := "~/.droid-proxy/auth"
	if m != nil && m.cfg != nil && strings.TrimSpace(m.cfg.OAuth.AuthDir) != "" {
		dir = strings.TrimSpace(m.cfg.OAuth.AuthDir)
	}
	return expandUserPath(dir)
}

func (m *Manager) CallbackAddr(provider config.OAuthProvider) string {
	host := "127.0.0.1"
	port := 0
	if m != nil && m.cfg != nil {
		switch provider {
		case ProviderCodex:
			host = firstNonEmpty(m.cfg.OAuth.CodexCallbackHost, "localhost")
			port = m.cfg.OAuth.CodexCallbackPort
		case ProviderXAI:
			host = firstNonEmpty(m.cfg.OAuth.XAICallbackHost, "127.0.0.1")
			port = m.cfg.OAuth.XAICallbackPort
		}
	}
	if port == 0 {
		switch provider {
		case ProviderCodex:
			port = 1455
		case ProviderXAI:
			port = 56121
		}
	}
	return net.JoinHostPort(host, strconv.Itoa(port))
}

func expandUserPath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" || path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		if path == "" || path == "~" {
			return home, nil
		}
		return filepath.Join(home, strings.TrimPrefix(path, "~/")), nil
	}
	return path, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func codexRedirectURI(addr string) string {
	_, port, err := net.SplitHostPort(addr)
	if err != nil || strings.TrimSpace(port) == "" {
		return "http://" + addr + "/auth/callback"
	}
	return "http://localhost:" + port + "/auth/callback"
}

func xaiRedirectURI(addr string) string {
	return "http://" + addr + "/callback"
}

func validateXAIEndpoint(rawURL, field string) (string, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return "", fmt.Errorf("xai discovery %s is empty", field)
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("xai discovery %s is invalid", field)
	}
	if parsed.Scheme != "https" {
		return "", fmt.Errorf("xai discovery %s must use https", field)
	}
	host := strings.ToLower(strings.TrimSpace(parsed.Hostname()))
	if host != "x.ai" && !strings.HasSuffix(host, ".x.ai") {
		return "", fmt.Errorf("xai discovery %s host is not on x.ai", field)
	}
	return rawURL, nil
}

func browserCommand(url string) (string, []string) {
	switch runtime.GOOS {
	case "darwin":
		return "open", []string{url}
	case "windows":
		return "rundll32", []string{"url.dll,FileProtocolHandler", url}
	default:
		return "xdg-open", []string{url}
	}
}
