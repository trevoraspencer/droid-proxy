package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Load reads a YAML config file from path, expands ${VAR} and ${VAR:-default}
// references in string fields, applies defaults, and validates the result.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	return parse(raw)
}

func parse(raw []byte) (*Config, error) {
	expanded := expandEnv(string(raw))
	cfg := &Config{}
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(expanded), &root); err != nil {
		return nil, fmt.Errorf("parse yaml: %w", err)
	}
	cfg.present = collectPresence(&root)
	dec := yaml.NewDecoder(strings.NewReader(expanded))
	dec.KnownFields(true)
	if err := dec.Decode(cfg); err != nil {
		return nil, fmt.Errorf("parse yaml: %w", err)
	}
	cfg.present = collectPresence(&root)
	cfg.applyDefaults()
	for _, m := range cfg.Models {
		if err := HydrateModel(m); err != nil {
			return nil, err
		}
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func collectPresence(root *yaml.Node) map[string]bool {
	present := map[string]bool{}
	if root == nil {
		return present
	}
	n := root
	if n.Kind == yaml.DocumentNode && len(n.Content) > 0 {
		n = n.Content[0]
	}
	collectPresenceNode(n, nil, present)
	return present
}

func collectPresenceNode(n *yaml.Node, path []string, present map[string]bool) {
	if n == nil {
		return
	}
	switch n.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			key := n.Content[i].Value
			childPath := append(append([]string{}, path...), key)
			present[strings.Join(childPath, ".")] = true
			collectPresenceNode(n.Content[i+1], childPath, present)
		}
	case yaml.SequenceNode:
		for _, child := range n.Content {
			collectPresenceNode(child, path, present)
		}
	}
}

// envRef matches ${VAR} or ${VAR:-default}.
var envRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(:-([^}]*))?\}`)

func expandEnv(s string) string {
	return envRef.ReplaceAllStringFunc(s, func(match string) string {
		m := envRef.FindStringSubmatch(match)
		// m[1]=name, m[2]=":-default" or "", m[3]=default value if present
		name := m[1]
		if val, ok := os.LookupEnv(name); ok {
			return val
		}
		if m[2] != "" {
			return m[3]
		}
		return ""
	})
}

func (c *Config) Validate() error {
	var errs []string
	if c.Listen.Port < 0 || c.Listen.Port > 65535 {
		errs = append(errs, fmt.Sprintf("listen.port %d out of range", c.Listen.Port))
	}
	if !c.ClientAuth.Enabled && !isLoopbackHost(c.Listen.Host) {
		errs = append(errs, fmt.Sprintf("listen.host %q is not loopback; set client_auth.enabled: true before binding to non-loopback or wildcard addresses", c.Listen.Host))
	}
	if !validLogLevel(c.Logging.Level) {
		errs = append(errs, fmt.Sprintf("logging.level %q is invalid (must be one of: trace, debug, info, warn, error)", c.Logging.Level))
	}
	if c.Logging.Format != "text" && c.Logging.Format != "json" {
		errs = append(errs, fmt.Sprintf("logging.format %q is invalid (must be text or json)", c.Logging.Format))
	}
	if c.ClientAuth.Enabled && len(c.ClientAuth.APIKeys) == 0 {
		errs = append(errs, "client_auth.enabled requires at least one api_keys entry")
	}
	if c.ClientAuth.Enabled {
		for i, key := range c.ClientAuth.APIKeys {
			if strings.TrimSpace(key) == "" {
				errs = append(errs, fmt.Sprintf("client_auth.api_keys[%d] is blank after env expansion", i))
			}
		}
	}
	if c.ReasoningCache.MaxEntries <= 0 {
		errs = append(errs, "reasoning_cache.max_entries must be positive")
	}
	if c.ReasoningCache.TTL <= 0 {
		errs = append(errs, "reasoning_cache.ttl must be positive")
	}
	if c.Server.RequestBodyMaxBytes < 0 {
		errs = append(errs, "server.request_body_max_bytes must not be negative")
	}
	if c.Server.ReadHeaderTimeout < 0 {
		errs = append(errs, "server.read_header_timeout must not be negative")
	}
	if c.Server.ReadTimeout < 0 {
		errs = append(errs, "server.read_timeout must not be negative")
	}
	if c.Server.WriteTimeout < 0 {
		errs = append(errs, "server.write_timeout must not be negative")
	}
	if c.Server.IdleTimeout < 0 {
		errs = append(errs, "server.idle_timeout must not be negative")
	}
	if c.Server.ShutdownTimeout < 0 {
		errs = append(errs, "server.shutdown_timeout must not be negative")
	}
	if c.Upstream.HTTPTimeout <= 0 {
		errs = append(errs, "upstream.http_timeout must be positive")
	}
	if c.Upstream.StreamKeepAlive < 0 {
		errs = append(errs, "upstream.stream_keep_alive must not be negative")
	}
	if c.Upstream.ResponseBodyMaxBytes < 0 {
		errs = append(errs, "upstream.response_body_max_bytes must not be negative")
	}
	if c.Upstream.ErrorBodyMaxBytes < 0 {
		errs = append(errs, "upstream.error_body_max_bytes must not be negative")
	}
	if strings.TrimSpace(c.OAuth.AuthDir) == "" {
		errs = append(errs, "oauth.auth_dir must not be blank")
	}
	if c.OAuth.CodexCallbackPort < 0 || c.OAuth.CodexCallbackPort > 65535 {
		errs = append(errs, fmt.Sprintf("oauth.codex_callback_port %d out of range", c.OAuth.CodexCallbackPort))
	}
	if c.OAuth.XAICallbackPort < 0 || c.OAuth.XAICallbackPort > 65535 {
		errs = append(errs, fmt.Sprintf("oauth.xai_callback_port %d out of range", c.OAuth.XAICallbackPort))
	}
	if !isLoopbackHost(c.OAuth.CodexCallbackHost) {
		errs = append(errs, fmt.Sprintf("oauth.codex_callback_host %q is not loopback", c.OAuth.CodexCallbackHost))
	}
	if !isLoopbackHost(c.OAuth.XAICallbackHost) {
		errs = append(errs, fmt.Sprintf("oauth.xai_callback_host %q is not loopback", c.OAuth.XAICallbackHost))
	}
	if lb := c.OAuth.LoadBalancing; lb.Strategy != "" && !lb.Strategy.IsValid() {
		errs = append(errs, fmt.Sprintf("oauth.load_balancing.strategy %q is invalid (must be one of: round-robin, fill-first, least-connections, random, sticky)", lb.Strategy))
	}
	if c.OAuth.LoadBalancing.MaxFailovers < 0 {
		errs = append(errs, "oauth.load_balancing.max_failovers must not be negative")
	}
	if c.OAuth.LoadBalancing.RateLimitCooldown < 0 {
		errs = append(errs, "oauth.load_balancing.rate_limit_cooldown must not be negative")
	}
	if c.OAuth.LoadBalancing.ErrorCooldown < 0 {
		errs = append(errs, "oauth.load_balancing.error_cooldown must not be negative")
	}
	if c.OAuth.LoadBalancing.QuotaSoftCapPercent < 0 || c.OAuth.LoadBalancing.QuotaSoftCapPercent > 100 {
		errs = append(errs, "oauth.load_balancing.quota_soft_cap_percent must be between 0 and 100")
	}
	if c.OAuth.LoadBalancing.AffinityMaxEntries < 0 {
		errs = append(errs, "oauth.load_balancing.affinity_max_entries must not be negative")
	}
	if c.OAuth.LoadBalancing.AffinityTTL < 0 {
		errs = append(errs, "oauth.load_balancing.affinity_ttl must not be negative")
	}
	if len(c.Models) == 0 {
		errs = append(errs, "at least one model must be configured")
	}
	seen := make(map[string]struct{}, len(c.Models))
	for _, m := range c.Models {
		if _, dup := seen[m.Alias]; dup && m.Alias != "" {
			errs = append(errs, fmt.Sprintf("duplicate model alias %q", m.Alias))
			continue
		}
		seen[m.Alias] = struct{}{}
		if err := m.Validate(); err != nil {
			errs = append(errs, err.Error())
		}
		if requiresAPIKey(m) && strings.TrimSpace(m.APIKeyEnv) == "" {
			errs = append(errs, fmt.Sprintf("model %q: remote upstream requires api_key_env or known_auth API key source", m.Alias))
		}
		if requiresAPIKey(m) && strings.TrimSpace(m.APIKeyEnv) != "" {
			if strings.TrimSpace(os.Getenv(strings.TrimSpace(m.APIKeyEnv))) == "" {
				errs = append(errs, fmt.Sprintf("model %q: env var %s is empty", m.Alias, strings.TrimSpace(m.APIKeyEnv)))
			}
		}
	}
	if len(errs) == 0 {
		return nil
	}
	return errors.New("config invalid:\n  - " + strings.Join(errs, "\n  - "))
}

func validLogLevel(level string) bool {
	switch level {
	case "trace", "debug", "info", "warn", "error":
		return true
	}
	return false
}

func requiresAPIKey(m *Model) bool {
	if m == nil || strings.TrimSpace(m.BaseURL) == "" {
		return false
	}
	if isOAuthUpstream(m.UpstreamProtocol) {
		return false
	}
	if ka, ok := LookupKnownAuth(m.KnownAuth); ok && ka.NoAuth {
		if isLoopbackURL(m.BaseURL) {
			return strings.TrimSpace(m.APIKeyEnv) != ""
		}
		return true
	}
	if strings.HasPrefix(strings.TrimSpace(m.BaseURL), "http://") {
		return !isLoopbackURL(m.BaseURL)
	}
	return true
}

func isLoopbackURL(raw string) bool {
	u, err := urlParse(raw)
	if err != nil {
		return false
	}
	return isLoopbackHost(u)
}

func urlParse(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	return u.Hostname(), nil
}
