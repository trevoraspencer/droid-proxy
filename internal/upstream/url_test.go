package upstream

import (
	"context"
	"testing"

	"droid-proxy/internal/config"
)

func TestClientBuildURLPathJoining(t *testing.T) {
	c := NewClient(&config.Config{})
	cases := []struct {
		name     string
		baseURL  string
		endpoint string
		want     string
	}{
		{"plain chat", "https://api.example.test", "/chat/completions", "https://api.example.test/chat/completions"},
		{"trailing slash chat", "https://api.example.test/", "chat/completions", "https://api.example.test/chat/completions"},
		{"v1 chat", "https://api.example.test/v1", "/chat/completions", "https://api.example.test/v1/chat/completions"},
		{"v1 trailing responses", "https://api.example.test/v1/", "/responses", "https://api.example.test/v1/responses"},
		{"anthropic messages nested", "https://api.example.test/proxy/v1", "/messages", "https://api.example.test/proxy/v1/messages"},
		{"count tokens", "https://api.example.test/v1", "/messages/count_tokens", "https://api.example.test/v1/messages/count_tokens"},
		{"strips unsafe URL adornments defensively", "https://user:pass@api.example.test/v1?token=x#frag", "/chat/completions", "https://api.example.test/v1/chat/completions"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, err := c.Build(context.Background(), SendOptions{
				Model: &config.Model{Alias: "m", BaseURL: tc.baseURL},
				Path:  tc.endpoint,
			})
			if err != nil {
				t.Fatalf("Build returned error: %v", err)
			}
			if got := req.URL.String(); got != tc.want {
				t.Fatalf("URL = %q, want %q", got, tc.want)
			}
		})
	}
}
