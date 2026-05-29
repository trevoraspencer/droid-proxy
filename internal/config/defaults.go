package config

import "time"

// applyDefaults fills in zero-valued fields with sensible defaults.
func (c *Config) applyDefaults() {
	if c.Listen.Host == "" {
		c.Listen.Host = "127.0.0.1"
	}
	if c.Listen.Port == 0 {
		c.Listen.Port = 8787
	}
	if !c.wasPresent("server.request_body_max_bytes") && c.Server.RequestBodyMaxBytes == 0 {
		c.Server.RequestBodyMaxBytes = 10 << 20
	}
	if !c.wasPresent("server.read_header_timeout") && c.Server.ReadHeaderTimeout == 0 {
		c.Server.ReadHeaderTimeout = 30 * time.Second
	}
	if !c.wasPresent("server.read_timeout") && c.Server.ReadTimeout == 0 {
		c.Server.ReadTimeout = 60 * time.Second
	}
	if !c.wasPresent("server.write_timeout") && c.Server.WriteTimeout == 0 {
		c.Server.WriteTimeout = 600 * time.Second
	}
	if !c.wasPresent("server.idle_timeout") && c.Server.IdleTimeout == 0 {
		c.Server.IdleTimeout = 120 * time.Second
	}
	if !c.wasPresent("server.shutdown_timeout") && c.Server.ShutdownTimeout == 0 {
		c.Server.ShutdownTimeout = 5 * time.Second
	}
	if c.ClientAuth.Header == "" {
		c.ClientAuth.Header = "Authorization"
	}
	if !c.wasPresent("client_auth.scheme") && c.ClientAuth.Scheme == "" {
		c.ClientAuth.Scheme = "Bearer"
	}
	if c.Logging.Level == "" {
		c.Logging.Level = "info"
	}
	if c.Logging.Format == "" {
		c.Logging.Format = "text"
	}
	if !c.wasPresent("logging.redact") {
		c.Logging.Redact = true
	}
	if !c.wasPresent("reasoning_cache.enabled") {
		c.ReasoningCache.Enabled = true
	}
	if !c.wasPresent("reasoning_cache.max_entries") && c.ReasoningCache.MaxEntries == 0 {
		c.ReasoningCache.MaxEntries = 1024
	}
	if !c.wasPresent("reasoning_cache.ttl") && c.ReasoningCache.TTL == 0 {
		c.ReasoningCache.TTL = 30 * time.Minute
	}
	if !c.wasPresent("upstream.http_timeout") && c.Upstream.HTTPTimeout == 0 {
		c.Upstream.HTTPTimeout = 600 * time.Second
	}
	if !c.wasPresent("upstream.stream_keep_alive") && c.Upstream.StreamKeepAlive == 0 {
		c.Upstream.StreamKeepAlive = 15 * time.Second
	}
	if !c.wasPresent("upstream.response_body_max_bytes") && c.Upstream.ResponseBodyMaxBytes == 0 {
		c.Upstream.ResponseBodyMaxBytes = 100 << 20
	}
	if !c.wasPresent("upstream.error_body_max_bytes") && c.Upstream.ErrorBodyMaxBytes == 0 {
		c.Upstream.ErrorBodyMaxBytes = 1 << 20
	}
	if !c.wasPresent("oauth.auth_dir") && c.OAuth.AuthDir == "" {
		c.OAuth.AuthDir = "~/.droid-proxy/auth"
	}
	if c.OAuth.CodexCallbackHost == "" {
		c.OAuth.CodexCallbackHost = "localhost"
	}
	if c.OAuth.CodexCallbackPort == 0 {
		c.OAuth.CodexCallbackPort = 1455
	}
	if c.OAuth.XAICallbackHost == "" {
		c.OAuth.XAICallbackHost = "127.0.0.1"
	}
	if c.OAuth.XAICallbackPort == 0 {
		c.OAuth.XAICallbackPort = 56121
	}
}

func (c *Config) wasPresent(path string) bool {
	return c != nil && c.present != nil && c.present[path]
}
