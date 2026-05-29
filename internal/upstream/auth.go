package upstream

import (
	"fmt"
	"os"
	"strings"

	"droid-proxy/internal/config"
)

// ResolveAPIKey looks up the API key for a model from its api_key_env (preferred)
// or the env var declared in its known_auth entry. Returns empty string + nil
// when no env var is configured (callers decide whether that's fatal).
func ResolveAPIKey(m *config.Model) (string, error) {
	env := strings.TrimSpace(m.APIKeyEnv)
	if env == "" {
		if ka, ok := config.LookupKnownAuth(m.KnownAuth); ok {
			env = ka.APIKeyEnv
		}
	}
	if env == "" {
		return "", nil
	}
	val := strings.TrimSpace(os.Getenv(env))
	if val == "" {
		return "", fmt.Errorf("model %q: env var %s is empty", m.Alias, env)
	}
	return val, nil
}

// ApplyAuthHeader sets the auth header on req according to the model's known_auth.
// Default: Authorization: Bearer <key>. Anthropic and similar providers can
// override via KnownAuth.AuthHeader / AuthScheme.
func ApplyAuthHeader(req httpHeaderSetter, m *config.Model, apiKey string) {
	if apiKey == "" {
		return
	}
	header := "Authorization"
	scheme := "Bearer"
	if ka, ok := config.LookupKnownAuth(m.KnownAuth); ok {
		if ka.AuthHeader != "" {
			header = ka.AuthHeader
			// AuthScheme = "" means "raw value, no scheme prefix" for providers
			// that use a non-Authorization auth header (Anthropic's x-api-key).
			scheme = ka.AuthScheme
		}
	}
	if scheme == "" {
		req.SetHeader(header, apiKey)
		return
	}
	req.SetHeader(header, scheme+" "+apiKey)
}

// httpHeaderSetter is the tiny interface required by ApplyAuthHeader; an http.Request
// adapter is provided via AdaptRequest.
type httpHeaderSetter interface {
	SetHeader(name, value string)
}
