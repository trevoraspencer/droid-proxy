package fidelity

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/sirupsen/logrus"

	"github.com/trevoraspencer/droid-proxy/internal/bench/mockupstream"
	"github.com/trevoraspencer/droid-proxy/internal/config"
	"github.com/trevoraspencer/droid-proxy/internal/server"
)

// TestFidelityAgainstRealProxy runs the full fidelity suite against an
// in-process droid-proxy wired to an in-process mock upstream, so CI catches
// any change that breaks prompt-cache fidelity or usage passthrough.
func TestFidelityAgainstRealProxy(t *testing.T) {
	mock := httptest.NewServer(mockupstream.New(mockupstream.Options{
		StreamChunks:        6,
		SimulatePromptCache: true,
	}).Handler())
	defer mock.Close()

	cfgYAML := `
listen:
  host: 127.0.0.1
  port: 0
logging:
  level: warn
oauth:
  auth_dir: ` + filepath.Join(t.TempDir(), "auth") + `
models:
  - alias: bench-chat
    display_name: Bench Chat
    factory_provider: generic-chat-completion-api
    upstream_protocol: openai-chat
    upstream_model: mock-model
    base_url: ` + mock.URL + `
    extra_args:
      reasoning_effort: high
      thinking:
        type: enabled
  - alias: bench-anthropic
    display_name: Bench Anthropic
    factory_provider: anthropic
    upstream_protocol: anthropic-messages
    upstream_model: mock-claude
    base_url: ` + mock.URL + `
  - alias: bench-anthropic-xlat
    display_name: Bench Anthropic Translated
    factory_provider: anthropic
    upstream_protocol: openai-chat
    upstream_model: mock-model
    base_url: ` + mock.URL + `
`
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(cfgYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	logger := logrus.New()
	logger.SetLevel(logrus.WarnLevel)
	srv, err := server.New(cfg, logger)
	if err != nil {
		t.Fatalf("build server: %v", err)
	}
	proxy := httptest.NewServer(srv.Engine())
	defer proxy.Close()

	results, err := Run(context.Background(), Options{
		ProxyBase:                proxy.URL,
		MockBase:                 mock.URL,
		ChatModel:                "bench-chat",
		AnthropicModel:           "bench-anthropic",
		AnthropicTranslatedModel: "bench-anthropic-xlat",
	})
	if err != nil {
		t.Fatalf("fidelity run: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("no fidelity checks ran")
	}
	for _, r := range results {
		if !r.Pass {
			t.Errorf("fidelity check failed: %s — %s", r.Name, r.Detail)
		} else {
			t.Logf("PASS %s — %s", r.Name, r.Detail)
		}
	}
}
