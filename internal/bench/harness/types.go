// Package harness drives comparative latency/throughput benchmarks against
// any OpenAI- or Anthropic-compatible endpoint: droid-proxy, a provider
// directly ("native"), or alternative proxies. All targets receive identical
// deterministic workloads so results are comparable.
package harness

import (
	"fmt"
	"strings"
	"time"
)

// Protocol selects the client-side API surface a scenario speaks.
type Protocol string

const (
	ProtocolOpenAIChat        Protocol = "openai-chat"
	ProtocolAnthropicMessages Protocol = "anthropic-messages"
	ProtocolOpenAIResponses   Protocol = "openai-responses"
)

func (p Protocol) valid() bool {
	switch p {
	case ProtocolOpenAIChat, ProtocolAnthropicMessages, ProtocolOpenAIResponses:
		return true
	}
	return false
}

// Path returns the endpoint path for the protocol.
func (p Protocol) Path() string {
	switch p {
	case ProtocolAnthropicMessages:
		return "/v1/messages"
	case ProtocolOpenAIResponses:
		return "/v1/responses"
	default:
		return "/v1/chat/completions"
	}
}

// Target is one endpoint under test.
type Target struct {
	Name    string            `yaml:"name"`
	BaseURL string            `yaml:"base_url"`
	Model   string            `yaml:"model"`
	APIKey  string            `yaml:"api_key"`
	Headers map[string]string `yaml:"headers"`
	// Baseline marks the target other targets are compared against (typically
	// the direct/native connection). At most one target should be baseline.
	Baseline bool `yaml:"baseline"`
	// ModelByProtocol optionally overrides Model per protocol, so one target
	// can expose different aliases per API surface. A target with no Model and
	// only ModelByProtocol entries is skipped for protocols it has no model
	// for — that is how a target is restricted to a subset of scenarios.
	ModelByProtocol map[Protocol]string `yaml:"model_by_protocol"`
}

func (t Target) modelFor(p Protocol) string {
	if m, ok := t.ModelByProtocol[p]; ok && strings.TrimSpace(m) != "" {
		return m
	}
	return t.Model
}

// Scenario is a deterministic workload shape.
type Scenario struct {
	Name     string   `yaml:"name"`
	Protocol Protocol `yaml:"protocol"`
	Stream   bool     `yaml:"stream"`
	// Requests is the measured request count (after warmup).
	Requests    int `yaml:"requests"`
	Concurrency int `yaml:"concurrency"`
	Warmup      int `yaml:"warmup"`
	// Workload shape.
	SystemPromptBytes int  `yaml:"system_prompt_bytes"`
	UserMessageBytes  int  `yaml:"user_message_bytes"`
	HistoryTurns      int  `yaml:"history_turns"`
	IncludeTools      bool `yaml:"include_tools"`
	// CacheControl adds Anthropic cache_control breakpoints (system + last
	// history turn) so explicit prompt caching can engage on live providers.
	CacheControl bool `yaml:"cache_control"`
	MaxTokens    int  `yaml:"max_tokens"`
	// UniquePrompts salts each request's final user message so live providers
	// cannot serve it from a prompt cache. Leave false for cache-hit scenarios.
	UniquePrompts bool `yaml:"unique_prompts"`
	// GrowingConversation makes request i carry i history turns (up to
	// HistoryTurns), reusing earlier turns verbatim — the agentic-session shape
	// that exercises prompt-prefix caching.
	GrowingConversation bool `yaml:"growing_conversation"`
	// TimeoutSeconds bounds one request. Zero means 120s.
	TimeoutSeconds int `yaml:"timeout_seconds"`
}

func (s *Scenario) applyDefaults() {
	if s.Requests <= 0 {
		s.Requests = 20
	}
	if s.Concurrency <= 0 {
		s.Concurrency = 1
	}
	if s.Warmup < 0 {
		s.Warmup = 0
	}
	if s.MaxTokens <= 0 {
		s.MaxTokens = 256
	}
	if s.SystemPromptBytes <= 0 {
		s.SystemPromptBytes = 2048
	}
	if s.UserMessageBytes <= 0 {
		s.UserMessageBytes = 256
	}
	if s.TimeoutSeconds <= 0 {
		s.TimeoutSeconds = 120
	}
}

func (s *Scenario) timeout() time.Duration {
	return time.Duration(s.TimeoutSeconds) * time.Second
}

// Config is the YAML file layout accepted by `droid-bench run`.
type Config struct {
	Targets   []Target   `yaml:"targets"`
	Scenarios []Scenario `yaml:"scenarios"`
}

// Validate checks the config for the mistakes that would produce garbage data.
func (c *Config) Validate() error {
	if len(c.Targets) == 0 {
		return fmt.Errorf("config has no targets")
	}
	if len(c.Scenarios) == 0 {
		return fmt.Errorf("config has no scenarios")
	}
	baselines := 0
	names := map[string]bool{}
	for i := range c.Targets {
		t := &c.Targets[i]
		if strings.TrimSpace(t.Name) == "" {
			return fmt.Errorf("target %d: name is required", i)
		}
		if names[t.Name] {
			return fmt.Errorf("duplicate target name %q", t.Name)
		}
		names[t.Name] = true
		if strings.TrimSpace(t.BaseURL) == "" {
			return fmt.Errorf("target %q: base_url is required", t.Name)
		}
		t.BaseURL = strings.TrimRight(t.BaseURL, "/")
		if strings.TrimSpace(t.Model) == "" && len(t.ModelByProtocol) == 0 {
			return fmt.Errorf("target %q: model is required", t.Name)
		}
		if t.Baseline {
			baselines++
		}
	}
	if baselines > 1 {
		return fmt.Errorf("at most one target may set baseline: true")
	}
	seen := map[string]bool{}
	for i := range c.Scenarios {
		sc := &c.Scenarios[i]
		if strings.TrimSpace(sc.Name) == "" {
			return fmt.Errorf("scenario %d: name is required", i)
		}
		if seen[sc.Name] {
			return fmt.Errorf("duplicate scenario name %q", sc.Name)
		}
		seen[sc.Name] = true
		if !sc.Protocol.valid() {
			return fmt.Errorf("scenario %q: protocol must be one of openai-chat, anthropic-messages, openai-responses", sc.Name)
		}
		sc.applyDefaults()
	}
	return nil
}
