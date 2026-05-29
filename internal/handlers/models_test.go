package handlers

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"

	"droid-proxy/internal/config"
	"droid-proxy/internal/upstream"
)

func newModelsTestAPI(t *testing.T, models []*config.Model) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{Models: models}
	router, err := upstream.NewRouter(cfg.Models)
	if err != nil {
		t.Fatal(err)
	}
	logger := logrus.New()
	logger.SetOutput(io.Discard)
	api := &API{Cfg: cfg, Router: router, Logger: logger}
	engine := gin.New()
	engine.GET("/v1/models", api.Models)
	engine.GET("/models", api.Models)
	return engine
}

func TestModels_PreservesOrder(t *testing.T) {
	engine := newModelsTestAPI(t, []*config.Model{
		{Alias: "a", DisplayName: "A", FactoryProvider: config.FactoryProviderGeneric, UpstreamProtocol: config.UpstreamOpenAIChat, BaseURL: "http://x"},
		{Alias: "b", DisplayName: "B", FactoryProvider: config.FactoryProviderOpenAI, UpstreamProtocol: config.UpstreamOpenAIResponses, BaseURL: "http://y"},
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	engine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp struct {
		Object string                   `json:"object"`
		Data   []map[string]interface{} `json:"data"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Object != "list" {
		t.Errorf("expected object=list, got %s", resp.Object)
	}
	if len(resp.Data) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(resp.Data))
	}
	if resp.Data[0]["id"] != "a" || resp.Data[1]["id"] != "b" {
		t.Errorf("order wrong: %v", resp.Data)
	}
	for _, expected := range []string{"display_name", "factory_provider", "upstream_protocol", "capabilities", "agent_ready"} {
		if _, ok := resp.Data[0][expected]; !ok {
			t.Errorf("expected key %s in entry %v", expected, resp.Data[0])
		}
	}
}

func TestModels_AgentReadyFlag(t *testing.T) {
	tests := []struct {
		name  string
		model *config.Model
		ready bool
	}{
		{
			name:  "generic chat supported",
			model: &config.Model{Alias: "ready", FactoryProvider: config.FactoryProviderGeneric, UpstreamProtocol: config.UpstreamOpenAIChat, BaseURL: "http://x"},
			ready: true,
		},
		{
			name: "streaming disabled",
			model: &config.Model{Alias: "limited", FactoryProvider: config.FactoryProviderGeneric, UpstreamProtocol: config.UpstreamOpenAIChat, BaseURL: "http://x",
				Capabilities: config.Capabilities{Streaming: boolPtr(false)}},
		},
		{
			name: "tools disabled",
			model: &config.Model{Alias: "no-tools", FactoryProvider: config.FactoryProviderGeneric, UpstreamProtocol: config.UpstreamOpenAIChat, BaseURL: "http://x",
				Capabilities: config.Capabilities{Tools: boolPtr(false)}},
		},
		{
			name: "tool result disabled",
			model: &config.Model{Alias: "no-tool-results", FactoryProvider: config.FactoryProviderGeneric, UpstreamProtocol: config.UpstreamOpenAIChat, BaseURL: "http://x",
				Capabilities: config.Capabilities{ToolResultSafe: boolPtr(false)}},
		},
		{
			name:  "openai responses native supported",
			model: &config.Model{Alias: "responses", FactoryProvider: config.FactoryProviderOpenAI, UpstreamProtocol: config.UpstreamOpenAIResponses, BaseURL: "http://x"},
			ready: true,
		},
		{
			name:  "openai responses over chat supported",
			model: &config.Model{Alias: "responses-chat", FactoryProvider: config.FactoryProviderOpenAI, UpstreamProtocol: config.UpstreamOpenAIChat, BaseURL: "http://x"},
			ready: true,
		},
		{
			name:  "anthropic messages native supported",
			model: &config.Model{Alias: "messages", FactoryProvider: config.FactoryProviderAnthropic, UpstreamProtocol: config.UpstreamAnthropicMessages, BaseURL: "http://x"},
			ready: true,
		},
		{
			name:  "anthropic messages over chat supported",
			model: &config.Model{Alias: "messages-chat", FactoryProvider: config.FactoryProviderAnthropic, UpstreamProtocol: config.UpstreamOpenAIChat, BaseURL: "http://x"},
			ready: true,
		},
		{
			name:  "unsupported combo is not advertised",
			model: &config.Model{Alias: "unsupported", FactoryProvider: config.FactoryProviderGeneric, UpstreamProtocol: config.UpstreamOpenAIResponses, BaseURL: "http://x"},
			ready: false,
		},
		{
			name:  "unknown combo is not advertised",
			model: &config.Model{Alias: "unknown", FactoryProvider: config.FactoryProvider("unknown"), UpstreamProtocol: config.UpstreamProtocol("unknown"), BaseURL: "http://x"},
			ready: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			engine := newModelsTestAPI(t, []*config.Model{tt.model})
			for _, path := range []string{"/v1/models", "/models"} {
				w := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodGet, path, nil)
				engine.ServeHTTP(w, req)
				var resp struct {
					Data []map[string]any `json:"data"`
				}
				_ = json.NewDecoder(w.Body).Decode(&resp)
				if ready, _ := resp.Data[0]["agent_ready"].(bool); ready != tt.ready {
					t.Errorf("%s agent_ready=%v, want %v", path, ready, tt.ready)
				}
			}
		})
	}
}

func TestModels_EmptyList(t *testing.T) {
	engine := newModelsTestAPI(t, nil)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	engine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp struct {
		Data []any `json:"data"`
	}
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp.Data == nil {
		t.Errorf("expected empty array, not nil")
	}
}

// boolPtr is a test helper.
func boolPtr(b bool) *bool { return &b }
