package config

import (
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
)

// DefaultListenPort is the single exported source of truth for the proxy's
// default listen port. Runtime defaulting, setup-generated configs, Factory
// address projection, TUI fallback, daemon output, and all other compiled
// default consumers must reference this constant rather than an independent
// numeric literal.
const DefaultListenPort = 9787

// OldDefaultListenPort is the pre-migration default. It is referenced by the
// migration component and the omitted-port startup coherence preflight to
// detect Factory entries that still target the old origin.
const OldDefaultListenPort = 8787

type FactoryProvider string

const (
	FactoryProviderAnthropic FactoryProvider = "anthropic"
	FactoryProviderOpenAI    FactoryProvider = "openai"
	FactoryProviderGeneric   FactoryProvider = "generic-chat-completion-api"
)

func (p FactoryProvider) IsValid() bool {
	switch p {
	case FactoryProviderAnthropic, FactoryProviderOpenAI, FactoryProviderGeneric:
		return true
	}
	return false
}

type UpstreamProtocol string

const (
	UpstreamOpenAIChat        UpstreamProtocol = "openai-chat"
	UpstreamOpenAIResponses   UpstreamProtocol = "openai-responses"
	UpstreamAnthropicMessages UpstreamProtocol = "anthropic-messages"
	UpstreamCodexResponses    UpstreamProtocol = "codex-responses"
	UpstreamXAIResponses      UpstreamProtocol = "xai-responses"
)

func (p UpstreamProtocol) IsValid() bool {
	switch p {
	case UpstreamOpenAIChat, UpstreamOpenAIResponses, UpstreamAnthropicMessages, UpstreamCodexResponses, UpstreamXAIResponses:
		return true
	}
	return false
}

type OAuthProvider string

const (
	OAuthProviderCodex OAuthProvider = "codex"
	OAuthProviderXAI   OAuthProvider = "xai"
)

func (p OAuthProvider) IsValid() bool {
	switch p {
	case OAuthProviderCodex, OAuthProviderXAI:
		return true
	}
	return false
}

type ReasoningMode string

const (
	ReasoningNone              ReasoningMode = "none"
	ReasoningDeepSeek          ReasoningMode = "deepseek"
	ReasoningAnthropicThinking ReasoningMode = "anthropic-thinking"
)

func (m ReasoningMode) IsValid() bool {
	switch m {
	case ReasoningNone, ReasoningDeepSeek, ReasoningAnthropicThinking:
		return true
	}
	return false
}

type FactoryReasoningMode string

const (
	FactoryReasoningDrop        FactoryReasoningMode = "drop"
	FactoryReasoningPassthrough FactoryReasoningMode = "passthrough"
)

func (m FactoryReasoningMode) IsValid() bool {
	switch m {
	case FactoryReasoningDrop, FactoryReasoningPassthrough:
		return true
	}
	return false
}

// FactoryReasoningEffort is the reasoningEffort value written to a Factory
// Droid custom-model entry. Keep these values synchronized with Droid's
// customModels schema.
type FactoryReasoningEffort string

const (
	FactoryReasoningEffortNone    FactoryReasoningEffort = "none"
	FactoryReasoningEffortDynamic FactoryReasoningEffort = "dynamic"
	FactoryReasoningEffortOff     FactoryReasoningEffort = "off"
	FactoryReasoningEffortMinimal FactoryReasoningEffort = "minimal"
	FactoryReasoningEffortLow     FactoryReasoningEffort = "low"
	FactoryReasoningEffortMedium  FactoryReasoningEffort = "medium"
	FactoryReasoningEffortHigh    FactoryReasoningEffort = "high"
	FactoryReasoningEffortXHigh   FactoryReasoningEffort = "xhigh"
	FactoryReasoningEffortMax     FactoryReasoningEffort = "max"
)

func (e FactoryReasoningEffort) IsValid() bool {
	switch e {
	case FactoryReasoningEffortNone,
		FactoryReasoningEffortDynamic,
		FactoryReasoningEffortOff,
		FactoryReasoningEffortMinimal,
		FactoryReasoningEffortLow,
		FactoryReasoningEffortMedium,
		FactoryReasoningEffortHigh,
		FactoryReasoningEffortXHigh,
		FactoryReasoningEffortMax:
		return true
	}
	return false
}

type Config struct {
	Listen         Listen         `yaml:"listen"`
	Server         Server         `yaml:"server"`
	ClientAuth     ClientAuth     `yaml:"client_auth"`
	Logging        Logging        `yaml:"logging"`
	ReasoningCache ReasoningCache `yaml:"reasoning_cache"`
	Upstream       Upstream       `yaml:"upstream"`
	OAuth          OAuth          `yaml:"oauth"`
	Models         []*Model       `yaml:"models"`

	// SourcePath and SourceModTime identify the file this config was loaded
	// from, so a running server can detect on-disk edits it has not applied.
	// Zero when the config was parsed from raw bytes.
	SourcePath    string    `yaml:"-"`
	SourceModTime time.Time `yaml:"-"`

	present map[string]bool `yaml:"-"`
}

// PortOmitted reports whether listen.port was absent from the source YAML.
// When true, the port resolves to DefaultListenPort at runtime.
func (c *Config) PortOmitted() bool {
	return !c.wasPresent("listen.port")
}

// PortExplicitlyZero reports whether listen.port was present in the source
// YAML with value 0. An explicit zero requests an OS-assigned ephemeral port
// and must not be treated as an omitted port.
func (c *Config) PortExplicitlyZero() bool {
	return c.wasPresent("listen.port") && c.Listen.Port == 0
}

// HasPresence reports whether this config was parsed with presence tracking
// (i.e. via Load or parse). Configs constructed from a partial fallback parse
// have no presence information.
func (c *Config) HasPresence() bool {
	return c.present != nil
}

// FormatListenURL serializes a listen host and port into an http:// URL.
// IPv6 loopback addresses (e.g. "::1") are wrapped in brackets per RFC 3986;
// the brackets are URL serialization syntax, not part of the configured host
// value.
func FormatListenURL(host string, port int) string {
	host = strings.TrimSpace(host)
	if host == "" {
		host = "127.0.0.1"
	}
	if strings.Contains(host, ":") {
		return fmt.Sprintf("http://[%s]:%d", host, port)
	}
	return fmt.Sprintf("http://%s:%d", host, port)
}

type Listen struct {
	Host string `yaml:"host"`
	Port int    `yaml:"port"`
}

type Server struct {
	RequestBodyMaxBytes int64         `yaml:"request_body_max_bytes"`
	ReadHeaderTimeout   time.Duration `yaml:"read_header_timeout"`
	ReadTimeout         time.Duration `yaml:"read_timeout"`
	WriteTimeout        time.Duration `yaml:"write_timeout"`
	IdleTimeout         time.Duration `yaml:"idle_timeout"`
	ShutdownTimeout     time.Duration `yaml:"shutdown_timeout"`
}

type ClientAuth struct {
	Enabled bool     `yaml:"enabled"`
	APIKeys []string `yaml:"api_keys"`
	Header  string   `yaml:"header"`
	Scheme  string   `yaml:"scheme"`
}

type Logging struct {
	Level         string `yaml:"level"`
	Format        string `yaml:"format"`
	Redact        bool   `yaml:"redact"`
	TraceRequests bool   `yaml:"trace_requests"`
}

type ReasoningCache struct {
	Enabled    bool          `yaml:"enabled"`
	MaxEntries int           `yaml:"max_entries"`
	TTL        time.Duration `yaml:"ttl"`
}

type Upstream struct {
	HTTPTimeout          time.Duration `yaml:"http_timeout"`
	StreamKeepAlive      time.Duration `yaml:"stream_keep_alive"`
	ResponseBodyMaxBytes int64         `yaml:"response_body_max_bytes"`
	ErrorBodyMaxBytes    int64         `yaml:"error_body_max_bytes"`
}

type OAuth struct {
	AuthDir           string        `yaml:"auth_dir"`
	CodexCallbackHost string        `yaml:"codex_callback_host"`
	CodexCallbackPort int           `yaml:"codex_callback_port"`
	XAICallbackHost   string        `yaml:"xai_callback_host"`
	XAICallbackPort   int           `yaml:"xai_callback_port"`
	LoadBalancing     LoadBalancing `yaml:"load_balancing"`
}

// LoadBalancingStrategy enumerates the supported account selection strategies.
type LoadBalancingStrategy string

const (
	LoadBalancingRoundRobin       LoadBalancingStrategy = "round-robin"
	LoadBalancingFillFirst        LoadBalancingStrategy = "fill-first"
	LoadBalancingLeastConnections LoadBalancingStrategy = "least-connections"
	LoadBalancingRandom           LoadBalancingStrategy = "random"
	LoadBalancingSticky           LoadBalancingStrategy = "sticky"
)

// IsValid reports whether s is a recognised load-balancing strategy.
func (s LoadBalancingStrategy) IsValid() bool {
	switch s {
	case LoadBalancingRoundRobin, LoadBalancingFillFirst,
		LoadBalancingLeastConnections, LoadBalancingRandom, LoadBalancingSticky:
		return true
	}
	return false
}

// LoadBalancing holds OAuth multi-account pool configuration.
// It applies only to Codex OAuth accounts.
type LoadBalancing struct {
	Strategy            LoadBalancingStrategy `yaml:"strategy"`
	MaxFailovers        int                   `yaml:"max_failovers"`
	RateLimitCooldown   time.Duration         `yaml:"rate_limit_cooldown"`
	ErrorCooldown       time.Duration         `yaml:"error_cooldown"`
	QuotaSoftCapPercent float64               `yaml:"quota_soft_cap_percent"`
	AffinityPath        string                `yaml:"affinity_path"`
	AffinityMaxEntries  int                   `yaml:"affinity_max_entries"`
	AffinityTTL         time.Duration         `yaml:"affinity_ttl"`
}

type Model struct {
	Alias            string            `yaml:"alias"`
	DisplayName      string            `yaml:"display_name"`
	FactoryProvider  FactoryProvider   `yaml:"factory_provider"`
	UpstreamProtocol UpstreamProtocol  `yaml:"upstream_protocol"`
	OAuthProvider    OAuthProvider     `yaml:"oauth_provider"`
	OAuthAccount     string            `yaml:"oauth_account"`
	UpstreamModel    string            `yaml:"upstream_model"`
	BaseURL          string            `yaml:"base_url"`
	APIKeyEnv        string            `yaml:"api_key_env"`
	KnownAuth        string            `yaml:"known_auth"`
	MaxOutputTokens  int               `yaml:"max_output_tokens"`
	MaxContextTokens int               `yaml:"max_context_tokens"`
	ExtraHeaders     map[string]string `yaml:"extra_headers"`
	ExtraArgs        map[string]any    `yaml:"extra_args"`
	Capabilities     Capabilities      `yaml:"capabilities"`
}

type Capabilities struct {
	Streaming              *bool                  `yaml:"streaming"`
	Tools                  *bool                  `yaml:"tools"`
	ToolResultSafe         *bool                  `yaml:"tool_result_safe"`
	Images                 *bool                  `yaml:"images"`
	JSONMode               *bool                  `yaml:"json_mode"`
	StructuredOutput       *bool                  `yaml:"structured_output"`
	Reasoning              ReasoningMode          `yaml:"reasoning"`
	FactoryReasoning       FactoryReasoningMode   `yaml:"factory_reasoning"`
	FactoryReasoningEffort FactoryReasoningEffort `yaml:"factory_reasoning_effort"`
	PromptCaching          *bool                  `yaml:"prompt_caching"`
}

func boolPtr(b bool) *bool { return &b }

// ResolvedCapabilities returns capabilities with explicit defaults.
// Defaults reflect what most providers do unless the user overrides.
func (m *Model) ResolvedCapabilities() ResolvedCapabilities {
	c := m.Capabilities
	r := ResolvedCapabilities{
		Streaming:              true,
		Tools:                  true,
		ToolResultSafe:         true,
		Images:                 false,
		JSONMode:               true,
		StructuredOutput:       false,
		Reasoning:              c.Reasoning,
		FactoryReasoning:       defaultFactoryReasoning(m.UpstreamProtocol),
		FactoryReasoningEffort: c.FactoryReasoningEffort,
		PromptCaching:          false,
	}
	if r.Reasoning == "" {
		r.Reasoning = ReasoningNone
	}
	if c.FactoryReasoning != "" {
		r.FactoryReasoning = c.FactoryReasoning
	}
	if c.Streaming != nil {
		r.Streaming = *c.Streaming
	}
	if c.Tools != nil {
		r.Tools = *c.Tools
	}
	if c.ToolResultSafe != nil {
		r.ToolResultSafe = *c.ToolResultSafe
	}
	if c.Images != nil {
		r.Images = *c.Images
	}
	if c.JSONMode != nil {
		r.JSONMode = *c.JSONMode
	}
	if c.StructuredOutput != nil {
		r.StructuredOutput = *c.StructuredOutput
	}
	if c.PromptCaching != nil {
		r.PromptCaching = *c.PromptCaching
	}
	return r
}

// ResolvedCapabilities is the fully-resolved capability set with all defaults applied.
type ResolvedCapabilities struct {
	Streaming              bool                   `json:"streaming"`
	Tools                  bool                   `json:"tools"`
	ToolResultSafe         bool                   `json:"tool_result_safe"`
	Images                 bool                   `json:"images"`
	JSONMode               bool                   `json:"json_mode"`
	StructuredOutput       bool                   `json:"structured_output"`
	Reasoning              ReasoningMode          `json:"reasoning"`
	FactoryReasoning       FactoryReasoningMode   `json:"factory_reasoning"`
	FactoryReasoningEffort FactoryReasoningEffort `json:"factory_reasoning_effort,omitempty"`
	PromptCaching          bool                   `json:"prompt_caching"`
}

// AgentReady reports whether a model is safe for agentic tool-using workflows.
// A model is ready iff streaming + tools + tool_result_safe are all on.
func (r ResolvedCapabilities) AgentReady() bool {
	return r.Streaming && r.Tools && r.ToolResultSafe
}

// AgentReady reports whether the configured provider/protocol combination and
// declared capabilities support the full agent text/stream/tool/tool-result path.
func (m *Model) AgentReady() bool {
	if m == nil || !supportsAgentWorkflow(m.FactoryProvider, m.UpstreamProtocol) {
		return false
	}
	return m.ResolvedCapabilities().AgentReady()
}

func defaultFactoryReasoning(up UpstreamProtocol) FactoryReasoningMode {
	if up == UpstreamXAIResponses {
		return FactoryReasoningDrop
	}
	return FactoryReasoningPassthrough
}

func supportsAgentWorkflow(fp FactoryProvider, up UpstreamProtocol) bool {
	switch fp {
	case FactoryProviderGeneric:
		return up == UpstreamOpenAIChat
	case FactoryProviderOpenAI:
		return up == UpstreamOpenAIResponses || up == UpstreamOpenAIChat || up == UpstreamCodexResponses || up == UpstreamXAIResponses
	case FactoryProviderAnthropic:
		return up == UpstreamAnthropicMessages || up == UpstreamOpenAIChat
	default:
		return false
	}
}

// Validate checks the factory_provider × upstream_protocol matrix.
func (m *Model) Validate() error {
	if strings.TrimSpace(m.Alias) == "" {
		return fmt.Errorf("model alias is required")
	}
	if !m.FactoryProvider.IsValid() {
		return fmt.Errorf("model %q: invalid factory_provider %q (must be one of: anthropic, openai, generic-chat-completion-api)", m.Alias, m.FactoryProvider)
	}
	if !m.UpstreamProtocol.IsValid() {
		return fmt.Errorf("model %q: invalid upstream_protocol %q (must be one of: openai-chat, openai-responses, anthropic-messages, codex-responses, xai-responses)", m.Alias, m.UpstreamProtocol)
	}
	allowed := allowedUpstreamFor(m.FactoryProvider)
	ok := false
	for _, a := range allowed {
		if a == m.UpstreamProtocol {
			ok = true
			break
		}
	}
	if !ok {
		return fmt.Errorf("model %q: factory_provider %q does not support upstream_protocol %q (allowed: %v)", m.Alias, m.FactoryProvider, m.UpstreamProtocol, allowed)
	}
	if isOAuthUpstream(m.UpstreamProtocol) {
		if m.FactoryProvider != FactoryProviderOpenAI {
			return fmt.Errorf("model %q: oauth upstream_protocol %q requires factory_provider %q", m.Alias, m.UpstreamProtocol, FactoryProviderOpenAI)
		}
		wantProvider := oauthProviderForUpstream(m.UpstreamProtocol)
		if !m.OAuthProvider.IsValid() {
			return fmt.Errorf("model %q: oauth_provider is required for upstream_protocol %q (must be one of: codex, xai)", m.Alias, m.UpstreamProtocol)
		}
		if m.OAuthProvider != wantProvider {
			return fmt.Errorf("model %q: upstream_protocol %q requires oauth_provider %q", m.Alias, m.UpstreamProtocol, wantProvider)
		}
	} else if m.OAuthProvider != "" {
		return fmt.Errorf("model %q: oauth_provider is only valid with codex-responses or xai-responses upstream_protocol", m.Alias)
	}
	if strings.TrimSpace(m.BaseURL) == "" && strings.TrimSpace(m.KnownAuth) == "" && !isOAuthUpstream(m.UpstreamProtocol) {
		return fmt.Errorf("model %q: base_url or known_auth is required", m.Alias)
	}
	if m.Capabilities.Reasoning != "" && !m.Capabilities.Reasoning.IsValid() {
		return fmt.Errorf("model %q: invalid capabilities.reasoning %q (must be one of: none, deepseek, anthropic-thinking)", m.Alias, m.Capabilities.Reasoning)
	}
	if m.Capabilities.FactoryReasoning != "" && !m.Capabilities.FactoryReasoning.IsValid() {
		return fmt.Errorf("model %q: invalid capabilities.factory_reasoning %q (must be one of: drop, passthrough)", m.Alias, m.Capabilities.FactoryReasoning)
	}
	if effort := m.Capabilities.FactoryReasoningEffort; effort != "" {
		if !effort.IsValid() {
			return fmt.Errorf("model %q: invalid capabilities.factory_reasoning_effort %q (must be one of: none, dynamic, off, minimal, low, medium, high, xhigh, max)", m.Alias, effort)
		}
		if m.ResolvedCapabilities().FactoryReasoning != FactoryReasoningPassthrough {
			return fmt.Errorf("model %q: capabilities.factory_reasoning_effort requires capabilities.factory_reasoning passthrough", m.Alias)
		}
	}
	if err := validateBaseURL(m); err != nil {
		return err
	}
	return nil
}

func allowedUpstreamFor(fp FactoryProvider) []UpstreamProtocol {
	switch fp {
	case FactoryProviderGeneric:
		return []UpstreamProtocol{UpstreamOpenAIChat}
	case FactoryProviderOpenAI:
		return []UpstreamProtocol{UpstreamOpenAIResponses, UpstreamOpenAIChat, UpstreamCodexResponses, UpstreamXAIResponses}
	case FactoryProviderAnthropic:
		return []UpstreamProtocol{UpstreamAnthropicMessages, UpstreamOpenAIChat}
	}
	return nil
}

func isOAuthUpstream(up UpstreamProtocol) bool {
	return up == UpstreamCodexResponses || up == UpstreamXAIResponses
}

func oauthProviderForUpstream(up UpstreamProtocol) OAuthProvider {
	switch up {
	case UpstreamCodexResponses:
		return OAuthProviderCodex
	case UpstreamXAIResponses:
		return OAuthProviderXAI
	default:
		return ""
	}
}

func validateBaseURL(m *Model) error {
	raw := strings.TrimSpace(m.BaseURL)
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("model %q: invalid base_url %q: %w", m.Alias, raw, err)
	}
	if !u.IsAbs() || u.Host == "" {
		return fmt.Errorf("model %q: base_url must be an absolute http(s) URL with a host", m.Alias)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("model %q: base_url scheme %q is not allowed (must be http or https)", m.Alias, u.Scheme)
	}
	if u.User != nil {
		return fmt.Errorf("model %q: base_url must not include userinfo", m.Alias)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("model %q: base_url must not include query or fragment", m.Alias)
	}
	if u.Scheme == "http" && !isLoopbackHost(u.Hostname()) {
		return fmt.Errorf("model %q: non-loopback http base_url %q is not allowed; use https for remote upstreams", m.Alias, raw)
	}
	return nil
}

func isLoopbackHost(host string) bool {
	host = strings.TrimSpace(strings.ToLower(host))
	if host == "localhost" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}
