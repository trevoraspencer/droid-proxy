package oauth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sirupsen/logrus"

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

func (m *Manager) FindToken(provider config.OAuthProvider, account string, includeDisabled bool) (*Token, error) {
	if !provider.IsValid() {
		return nil, fmt.Errorf("unsupported oauth provider %q", provider)
	}
	if strings.TrimSpace(account) == "" {
		return nil, fmt.Errorf("%s OAuth account selector is required", provider)
	}
	tokens, err := m.LoadTokens(provider)
	if err != nil {
		return nil, err
	}
	for _, token := range tokens {
		if !includeDisabled && token.Disabled {
			continue
		}
		if token.MatchesAccount(account) {
			return token, nil
		}
	}
	return nil, fmt.Errorf("no %s OAuth account %q found", provider, account)
}

func (m *Manager) SetTokenDisabled(provider config.OAuthProvider, account string, disabled bool) (*Token, error) {
	token, err := m.FindToken(provider, account, true)
	if err != nil {
		return nil, err
	}
	token.Disabled = disabled
	if _, err := m.SaveToken(token); err != nil {
		return nil, err
	}
	return token, nil
}

func (m *Manager) DeleteToken(provider config.OAuthProvider, account string) (string, error) {
	token, err := m.FindToken(provider, account, true)
	if err != nil {
		return "", err
	}
	path := token.Path()
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("%s OAuth account %q has no token path", provider, account)
	}
	if err := os.Remove(path); err != nil {
		return "", fmt.Errorf("delete auth token: %w", err)
	}
	return path, nil
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
		if entry.IsDir() || !IsTokenFileName(entry.Name()) {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		token, err := m.loadTokenPath(path)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", entry.Name(), err)
		}
		if token.Provider() != provider {
			continue
		}
		out = append(out, token)
	}
	return out, nil
}

func (m *Manager) loadTokenPath(path string) (*Token, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read auth token: %w", err)
	}
	var token Token
	if err := json.Unmarshal(raw, &token); err != nil {
		return nil, fmt.Errorf("parse auth token: %w", err)
	}
	token.path = path
	return &token, nil
}

// LoadTokenAtPath loads and parses a single token file at the given path.
// It is exported so that server startup can load individual files while
// tolerating invalid entries.
func (m *Manager) LoadTokenAtPath(path string) (*Token, error) {
	return m.loadTokenPath(path)
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
	if err := chmodSecure(dir, 0o700, "auth dir"); err != nil {
		return "", err
	}
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
	if err := writeFileAtomic(path, raw, 0o600); err != nil {
		return "", err
	}
	if err := chmodSecure(path, 0o600, "token file"); err != nil {
		return "", err
	}
	return path, nil
}

func writeFileAtomic(path string, raw []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	tmp, err := os.CreateTemp(dir, "."+base+".tmp-*")
	if err != nil {
		return fmt.Errorf("create token temp file: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod token temp file: %w", err)
	}
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write token temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync token temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close token temp file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace token file: %w", err)
	}
	cleanup = false
	if dirFile, err := os.Open(dir); err == nil {
		_ = dirFile.Sync()
		_ = dirFile.Close()
	}
	return nil
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

// IsTokenFileName returns true if the filename looks like a token file
// that should be processed. It excludes:
//   - Non-.json files
//   - Hidden files (starting with .)
//   - Lock files (.lock extension)
//   - Atomic-save temp files (.<name>.tmp-* pattern, caught by hidden-file check)
func IsTokenFileName(name string) bool {
	// Must end with .json
	if filepath.Ext(name) != ".json" {
		return false
	}
	// Skip hidden files (e.g. .codex-user.json.tmp-12345)
	if strings.HasPrefix(name, ".") {
		return false
	}
	// Skip lock files
	if strings.HasSuffix(name, ".lock") {
		return false
	}
	return true
}

// LoadCodexTokensFromDir loads Codex tokens from the given directory using the
// provided Manager. It applies consistent file filtering via IsTokenFileName:
// only non-hidden, non-lock, non-temporary .json files are processed.
// Invalid or unparseable files are logged to the provided logger and skipped.
// Missing directories return nil without error.
func LoadCodexTokensFromDir(mgr *Manager, dir string, logger *logrus.Logger) ([]*Token, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var tokens []*Token
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !IsTokenFileName(name) {
			continue
		}
		path := filepath.Join(dir, name)
		tok, err := mgr.loadTokenPath(path)
		if err != nil {
			if logger != nil {
				logger.WithError(err).WithField("file", name).Warn("skipping invalid token file")
			}
			continue
		}
		if tok.Provider() == ProviderCodex {
			tokens = append(tokens, tok)
		}
	}
	return tokens, nil
}
