package oauth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"droid-proxy/internal/config"
)

var (
	codexAuthURL      = CodexAuthURL
	codexTokenURL     = CodexTokenURL
	xaiDiscoveryURL   = XAIDiscoveryURL
	defaultHTTPClient = &http.Client{Timeout: 30 * time.Second}
)

func BuildAuthURL(provider config.OAuthProvider, redirectURI, state, nonce string, pkce *PKCE) (string, error) {
	if pkce == nil {
		return "", fmt.Errorf("pkce is required")
	}
	switch provider {
	case ProviderCodex:
		params := url.Values{
			"client_id":                  {CodexClientID},
			"response_type":              {"code"},
			"redirect_uri":               {redirectURI},
			"scope":                      {"openid email profile offline_access"},
			"state":                      {state},
			"code_challenge":             {pkce.CodeChallenge},
			"code_challenge_method":      {"S256"},
			"prompt":                     {"login"},
			"id_token_add_organizations": {"true"},
			"codex_cli_simplified_flow":  {"true"},
		}
		return codexAuthURL + "?" + params.Encode(), nil
	case ProviderXAI:
		discovery, err := discoverXAI(context.Background())
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(nonce) == "" {
			return "", fmt.Errorf("xai nonce is required")
		}
		params := url.Values{
			"response_type":         {"code"},
			"client_id":             {XAIClientID},
			"redirect_uri":          {redirectURI},
			"scope":                 {XAIScope},
			"code_challenge":        {pkce.CodeChallenge},
			"code_challenge_method": {"S256"},
			"state":                 {state},
			"nonce":                 {nonce},
			"plan":                  {"generic"},
			"referrer":              {"droid-proxy"},
		}
		return discovery.AuthorizationEndpoint + "?" + params.Encode(), nil
	default:
		return "", fmt.Errorf("unsupported oauth provider %q", provider)
	}
}

func (m *Manager) ExchangeCode(ctx context.Context, provider config.OAuthProvider, code, redirectURI string, pkce *PKCE) (*Token, error) {
	if strings.TrimSpace(code) == "" {
		return nil, fmt.Errorf("authorization code is required")
	}
	if pkce == nil {
		return nil, fmt.Errorf("pkce is required")
	}
	switch provider {
	case ProviderCodex:
		return exchangeCodexCode(ctx, code, redirectURI, pkce)
	case ProviderXAI:
		discovery, err := discoverXAI(ctx)
		if err != nil {
			return nil, err
		}
		return exchangeXAICode(ctx, code, redirectURI, pkce, discovery.TokenEndpoint)
	default:
		return nil, fmt.Errorf("unsupported oauth provider %q", provider)
	}
}

func (m *Manager) RefreshIfNeeded(ctx context.Context, token *Token) (*Token, error) {
	if token == nil {
		return nil, fmt.Errorf("token is nil")
	}
	if strings.TrimSpace(token.AccessToken) != "" && !token.NeedsRefresh(time.Now()) {
		return token, nil
	}
	if strings.TrimSpace(token.RefreshToken) == "" {
		return nil, fmt.Errorf("%s OAuth token is expired and has no refresh token", token.Provider())
	}
	var refreshed *Token
	var err error
	switch token.Provider() {
	case ProviderCodex:
		refreshed, err = refreshCodex(ctx, token.RefreshToken)
	case ProviderXAI:
		tokenEndpoint := strings.TrimSpace(token.TokenEndpoint)
		if tokenEndpoint == "" {
			discovery, errDiscover := discoverXAI(ctx)
			if errDiscover != nil {
				return nil, errDiscover
			}
			tokenEndpoint = discovery.TokenEndpoint
		}
		refreshed, err = refreshXAI(ctx, token.RefreshToken, tokenEndpoint)
	default:
		return nil, fmt.Errorf("unsupported oauth provider %q", token.Provider())
	}
	if err != nil {
		return nil, err
	}
	refreshed.path = token.path
	refreshed.Type = string(token.Provider())
	if refreshed.Email == "" {
		refreshed.Email = token.Email
	}
	if refreshed.Subject == "" {
		refreshed.Subject = token.Subject
	}
	if refreshed.AccountID == "" {
		refreshed.AccountID = token.AccountID
	}
	if refreshed.BaseURL == "" {
		refreshed.BaseURL = token.BaseURL
	}
	if refreshed.RedirectURI == "" {
		refreshed.RedirectURI = token.RedirectURI
	}
	if refreshed.TokenEndpoint == "" {
		refreshed.TokenEndpoint = token.TokenEndpoint
	}
	if refreshed.AuthKind == "" {
		refreshed.AuthKind = token.AuthKind
	}
	if _, err := m.SaveToken(refreshed); err != nil {
		return nil, err
	}
	return refreshed, nil
}

type xaiDiscovery struct {
	AuthorizationEndpoint string
	TokenEndpoint         string
}

func discoverXAI(ctx context.Context) (*xaiDiscovery, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, xaiDiscoveryURL, nil)
	if err != nil {
		return nil, fmt.Errorf("xai discovery: create request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := defaultHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("xai discovery request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("xai discovery failed with status %d", resp.StatusCode)
	}
	var payload struct {
		AuthorizationEndpoint string `json:"authorization_endpoint"`
		TokenEndpoint         string `json:"token_endpoint"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("xai discovery: parse response: %w", err)
	}
	authEndpoint, err := validateXAIEndpoint(payload.AuthorizationEndpoint, "authorization_endpoint")
	if err != nil {
		return nil, err
	}
	tokenEndpoint, err := validateXAIEndpoint(payload.TokenEndpoint, "token_endpoint")
	if err != nil {
		return nil, err
	}
	return &xaiDiscovery{AuthorizationEndpoint: authEndpoint, TokenEndpoint: tokenEndpoint}, nil
}

func exchangeCodexCode(ctx context.Context, code, redirectURI string, pkce *PKCE) (*Token, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {CodexClientID},
		"code":          {strings.TrimSpace(code)},
		"redirect_uri":  {strings.TrimSpace(redirectURI)},
		"code_verifier": {pkce.CodeVerifier},
	}
	token, err := postTokenForm(ctx, codexTokenURL, form)
	if err != nil {
		return nil, err
	}
	token.Type = string(ProviderCodex)
	token.BaseURL = CodexDefaultBaseURL
	token.RedirectURI = redirectURI
	token.AuthKind = "oauth"
	token.Email, token.Subject, token.AccountID = parseCodexIdentity(token.IDToken, token.AccessToken)
	return token, nil
}

func exchangeXAICode(ctx context.Context, code, redirectURI string, pkce *PKCE, tokenEndpoint string) (*Token, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {strings.TrimSpace(code)},
		"redirect_uri":  {strings.TrimSpace(redirectURI)},
		"client_id":     {XAIClientID},
		"code_verifier": {pkce.CodeVerifier},
	}
	token, err := postTokenForm(ctx, tokenEndpoint, form)
	if err != nil {
		return nil, err
	}
	token.Type = string(ProviderXAI)
	token.BaseURL = XAIDefaultBaseURL
	token.RedirectURI = redirectURI
	token.TokenEndpoint = tokenEndpoint
	token.AuthKind = "oauth"
	token.Email, token.Subject, _ = parseJWTIdentity(token.IDToken)
	return token, nil
}

func refreshCodex(ctx context.Context, refreshToken string) (*Token, error) {
	form := url.Values{
		"client_id":     {CodexClientID},
		"grant_type":    {"refresh_token"},
		"refresh_token": {strings.TrimSpace(refreshToken)},
		"scope":         {"openid profile email"},
	}
	token, err := postTokenForm(ctx, codexTokenURL, form)
	if err != nil {
		return nil, err
	}
	token.Type = string(ProviderCodex)
	token.BaseURL = CodexDefaultBaseURL
	token.AuthKind = "oauth"
	token.Email, token.Subject, token.AccountID = parseCodexIdentity(token.IDToken, token.AccessToken)
	return token, nil
}

func refreshXAI(ctx context.Context, refreshToken, tokenEndpoint string) (*Token, error) {
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {XAIClientID},
		"refresh_token": {strings.TrimSpace(refreshToken)},
	}
	token, err := postTokenForm(ctx, tokenEndpoint, form)
	if err != nil {
		return nil, err
	}
	token.Type = string(ProviderXAI)
	token.BaseURL = XAIDefaultBaseURL
	token.TokenEndpoint = tokenEndpoint
	token.AuthKind = "oauth"
	token.Email, token.Subject, _ = parseJWTIdentity(token.IDToken)
	return token, nil
}

func postTokenForm(ctx context.Context, endpoint string, form url.Values) (*Token, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("token request: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := defaultHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token request failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("token response: read body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token request failed with status %d", resp.StatusCode)
	}
	var payload struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
		TokenType    string `json:"token_type"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("token response: parse body: %w", err)
	}
	if strings.TrimSpace(payload.AccessToken) == "" {
		return nil, fmt.Errorf("token response missing access_token")
	}
	expire := ""
	if payload.ExpiresIn > 0 {
		expire = time.Now().Add(time.Duration(payload.ExpiresIn) * time.Second).UTC().Format(time.RFC3339)
	}
	return &Token{
		AccessToken:  strings.TrimSpace(payload.AccessToken),
		RefreshToken: strings.TrimSpace(payload.RefreshToken),
		IDToken:      strings.TrimSpace(payload.IDToken),
		TokenType:    strings.TrimSpace(payload.TokenType),
		ExpiresIn:    payload.ExpiresIn,
		Expired:      expire,
		LastRefresh:  time.Now().UTC().Format(time.RFC3339),
	}, nil
}

func parseCodexIDToken(token string) (email, subject, accountID string) {
	email, subject, _ = parseJWTIdentity(token)
	claims := parseJWTPayload(token)
	if claims == nil {
		return email, subject, ""
	}
	if auth, ok := claims["https://api.openai.com/auth"].(map[string]any); ok {
		accountID = stringClaim(auth, "chatgpt_account_id")
		if email == "" {
			email = stringClaim(auth, "chatgpt_email")
		}
	}
	return email, subject, accountID
}

func parseCodexIdentity(idToken, accessToken string) (email, subject, accountID string) {
	email, subject, accountID = parseCodexIDToken(idToken)
	accessEmail, accessSubject, accessAccountID := parseCodexIDToken(accessToken)
	if email == "" {
		email = accessEmail
	}
	if subject == "" {
		subject = accessSubject
	}
	if accountID == "" {
		accountID = accessAccountID
	}
	return email, subject, accountID
}

func parseJWTIdentity(token string) (email, subject, accountID string) {
	claims := parseJWTPayload(token)
	if claims == nil {
		return "", "", ""
	}
	return stringClaim(claims, "email"), stringClaim(claims, "sub"), stringClaim(claims, "account_id")
}

func parseJWTPayload(token string) map[string]any {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		raw, err = base64.URLEncoding.DecodeString(parts[1])
		if err != nil {
			return nil
		}
	}
	var claims map[string]any
	if err := json.Unmarshal(raw, &claims); err != nil {
		return nil
	}
	return claims
}

func stringClaim(claims map[string]any, key string) string {
	v, _ := claims[key].(string)
	return strings.TrimSpace(v)
}
