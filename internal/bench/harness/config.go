package harness

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"regexp"

	"gopkg.in/yaml.v3"
)

var envPattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(?::-([^}]*))?\}`)

// expandEnv substitutes ${VAR} and ${VAR:-default} references (the same forms
// droid-proxy's own config supports) so bench configs never need plaintext
// API keys on disk.
func expandEnv(s string) string {
	return envPattern.ReplaceAllStringFunc(s, func(m string) string {
		parts := envPattern.FindStringSubmatch(m)
		if v := os.Getenv(parts[1]); v != "" {
			return v
		}
		return parts[2]
	})
}

// LoadConfig reads and validates a bench config YAML file.
func LoadConfig(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("parse %s: multiple YAML documents are not supported", path)
		}
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	for i := range cfg.Targets {
		cfg.Targets[i].APIKey = expandEnv(cfg.Targets[i].APIKey)
		cfg.Targets[i].BaseURL = expandEnv(cfg.Targets[i].BaseURL)
		for k, v := range cfg.Targets[i].Headers {
			cfg.Targets[i].Headers[k] = expandEnv(v)
		}
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &cfg, nil
}

// ExampleConfig is a ready-to-edit bench config comparing droid-proxy against
// a direct provider connection and an alternative proxy.
const ExampleConfig = `# droid-bench run configuration.
# ${VAR} in api_key/base_url/headers expands from the environment.

targets:
  # Direct provider connection — the "native" baseline.
  - name: native-deepseek
    base_url: https://api.deepseek.com
    model: deepseek-chat
    api_key: ${DEEPSEEK_API_KEY}
    baseline: true

  # droid-proxy with an equivalent model alias configured.
  - name: droid-proxy
    base_url: http://127.0.0.1:9787
    model: deepseek-v4-flash
    api_key: x

  # Any other OpenAI/Anthropic-compatible proxy (ProxyPilot, vibeproxy, ...).
  # - name: alt-proxy
  #   base_url: http://127.0.0.1:8317
  #   model: deepseek-chat
  #   api_key: ${ALT_PROXY_KEY}

scenarios:
  # Cold-ish single requests: proxy overhead shows up in ttfb/total deltas.
  - name: chat-small-nonstream
    protocol: openai-chat
    requests: 30
    warmup: 3
    system_prompt_bytes: 2048
    user_message_bytes: 256
    max_tokens: 128
    unique_prompts: true

  # The agentic Droid shape: large system prompt, tool history, streaming.
  - name: chat-agentic-stream
    protocol: openai-chat
    stream: true
    requests: 20
    warmup: 2
    system_prompt_bytes: 16384
    user_message_bytes: 1024
    history_turns: 8
    include_tools: true
    max_tokens: 512

  # Prompt-cache exercise: growing conversation with a stable prefix.
  # cache hit % (from usage counters) shows whether caching engages.
  - name: chat-cache-growth
    protocol: openai-chat
    stream: true
    requests: 16
    warmup: 0
    system_prompt_bytes: 16384
    user_message_bytes: 512
    history_turns: 16
    growing_conversation: true
    max_tokens: 128

  # Parallel load: throughput and tail latency under concurrency.
  - name: chat-concurrent-stream
    protocol: openai-chat
    stream: true
    requests: 32
    warmup: 4
    concurrency: 8
    system_prompt_bytes: 8192
    user_message_bytes: 512
    history_turns: 4
    max_tokens: 256
`
