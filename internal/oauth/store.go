package oauth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"droid-proxy/internal/config"
)

func (m *Manager) LoadToken(provider config.OAuthProvider, account string) (*Token, error) {
	tokens, err := m.LoadTokens(provider)
	if err != nil {
		return nil, err
	}
	for i := range tokens {
		if tokens[i].Disabled {
			continue
		}
		if tokens[i].MatchesAccount(account) {
			return tokens[i], nil
		}
	}
	if strings.TrimSpace(account) != "" {
		return nil, fmt.Errorf("no %s OAuth account %q found", provider, account)
	}
	return nil, fmt.Errorf("no %s OAuth accounts found; run droid-proxy auth %s", provider, provider)
}

func (m *Manager) LoadTokens(provider config.OAuthProvider) ([]*Token, error) {
	dir, err := m.AuthDir()
	if err != nil {
		return nil, fmt.Errorf("resolve auth dir: %w", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read auth dir: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	var out []*Token
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read auth token %s: %w", entry.Name(), err)
		}
		var token Token
		if err := json.Unmarshal(raw, &token); err != nil {
			return nil, fmt.Errorf("parse auth token %s: %w", entry.Name(), err)
		}
		token.path = path
		if token.Provider() != provider {
			continue
		}
		out = append(out, &token)
	}
	return out, nil
}

func (m *Manager) SaveToken(token *Token) (string, error) {
	if token == nil {
		return "", fmt.Errorf("token is nil")
	}
	dir, err := m.AuthDir()
	if err != nil {
		return "", fmt.Errorf("resolve auth dir: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create auth dir: %w", err)
	}
	_ = os.Chmod(dir, 0o700)
	path := token.path
	if strings.TrimSpace(path) == "" {
		path = filepath.Join(dir, tokenFileName(token))
	}
	token.path = path
	raw, err := json.MarshalIndent(token, "", "  ")
	if err != nil {
		return "", fmt.Errorf("serialize token: %w", err)
	}
	raw = append(raw, '\n')
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return "", fmt.Errorf("open token file: %w", err)
	}
	if _, err := f.Write(raw); err != nil {
		_ = f.Close()
		return "", fmt.Errorf("write token file: %w", err)
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("close token file: %w", err)
	}
	_ = os.Chmod(path, 0o600)
	return path, nil
}

func tokenFileName(token *Token) string {
	provider := strings.ToLower(strings.TrimSpace(token.Type))
	if provider == "" {
		provider = "oauth"
	}
	for _, v := range []string{token.Email, token.Subject, token.AccountID} {
		if s := sanitizeFileSegment(v); s != "" {
			return provider + "-" + s + ".json"
		}
	}
	return provider + ".json"
}

func sanitizeFileSegment(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '@' || r == '.' || r == '_' || r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-")
}
