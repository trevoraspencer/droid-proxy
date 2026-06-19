package upstream

import (
	"context"
	"net/http"
	"testing"

	"github.com/trevoraspencer/droid-proxy/internal/config"
)

func TestFilterHeaders_DropsHopByHop(t *testing.T) {
	src := http.Header{
		"Content-Type":      {"application/json"},
		"Connection":        {"keep-alive"},
		"Keep-Alive":        {"timeout=15"},
		"Transfer-Encoding": {"chunked"},
		"Set-Cookie":        {"x=1"},
		"Content-Length":    {"42"},
		"Content-Encoding":  {"gzip"},
	}
	got := FilterHeaders(src)
	if got.Get("Content-Type") != "application/json" {
		t.Errorf("Content-Type dropped: %v", got)
	}
	for _, h := range []string{"Connection", "Keep-Alive", "Transfer-Encoding", "Set-Cookie", "Content-Length", "Content-Encoding"} {
		if got.Get(h) != "" {
			t.Errorf("header %s not dropped: %q", h, got.Get(h))
		}
	}
}

func TestFilterHeaders_DropsGatewayPrefixes(t *testing.T) {
	src := http.Header{
		"X-Litellm-Foo":   {"bar"},
		"Helicone-Cache":  {"hit"},
		"X-Portkey-Trace": {"abc"},
		"Cf-Aig-Hint":     {"y"},
		"X-Kong-Trace":    {"z"},
		"X-Bt-Foo":        {"w"},
		"X-Unrelated":     {"keep"},
	}
	got := FilterHeaders(src)
	if got.Get("X-Unrelated") != "keep" {
		t.Errorf("unrelated header dropped")
	}
	for _, h := range []string{"X-Litellm-Foo", "Helicone-Cache", "X-Portkey-Trace", "Cf-Aig-Hint", "X-Kong-Trace", "X-Bt-Foo"} {
		if got.Get(h) != "" {
			t.Errorf("gateway header %s not dropped", h)
		}
	}
}

func TestFilterHeaders_ConnectionScopedTokens(t *testing.T) {
	src := http.Header{
		"Connection":        {"upgrade, custom-field"},
		"Upgrade":           {"h2c"},
		"Custom-Field":      {"please-drop"},
		"X-Should-Remain":   {"yes"},
		"Sec-Websocket-Key": {"abc"},
	}
	got := FilterHeaders(src)
	if got.Get("Custom-Field") != "" {
		t.Errorf("Connection-scoped header Custom-Field not dropped")
	}
	if got.Get("X-Should-Remain") != "yes" {
		t.Errorf("unrelated header dropped")
	}
}

func TestFilterHeaders_NilAndEmpty(t *testing.T) {
	if FilterHeaders(nil) != nil {
		t.Errorf("nil input should return nil")
	}
	if FilterHeaders(http.Header{}) != nil {
		t.Errorf("empty input should return nil")
	}
	if FilterHeaders(http.Header{"Connection": {"close"}}) != nil {
		t.Errorf("after filtering all headers, expected nil")
	}
}

func TestCopyHeaders_DoesNotOverwrite(t *testing.T) {
	dst := http.Header{"Content-Type": {"text/plain"}}
	src := http.Header{
		"Content-Type": {"application/json"},
		"X-Other":      {"yes"},
	}
	CopyHeaders(dst, src)
	if got := dst.Get("Content-Type"); got != "text/plain" {
		t.Errorf("Content-Type overwritten: %q", got)
	}
	if got := dst.Get("X-Other"); got != "yes" {
		t.Errorf("X-Other not copied: %q", got)
	}
}

func TestBuild_IgnoresReservedOutboundExtraHeaders(t *testing.T) {
	t.Setenv("DROID_PROXY_TEST_KEY", "upstream-secret")
	m := &config.Model{
		Alias:            "m",
		FactoryProvider:  config.FactoryProviderGeneric,
		UpstreamProtocol: config.UpstreamOpenAIChat,
		BaseURL:          "http://127.0.0.1:1/v1",
		APIKeyEnv:        "DROID_PROXY_TEST_KEY",
		ExtraHeaders: map[string]string{
			"Authorization":     "Bearer attacker",
			"x-api-key":         "attacker",
			"Host":              "evil.example",
			"Connection":        "keep-alive",
			"X-Forwarded-For":   "203.0.113.1",
			"X-Allowed-Feature": "ok",
		},
	}
	c := NewClient(&config.Config{Models: []*config.Model{m}})
	req, err := c.Build(context.Background(), SendOptions{
		Model:        m,
		Path:         "/chat/completions",
		Body:         []byte(`{}`),
		ExtraHeaders: map[string]string{"Cookie": "session=secret", "X-Trace-Ok": "yes"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer upstream-secret" {
		t.Fatalf("provider auth was overridden or missing: %q", got)
	}
	for _, h := range []string{"x-api-key", "Connection", "X-Forwarded-For", "Cookie"} {
		if got := req.Header.Get(h); got != "" {
			t.Fatalf("reserved header %s was forwarded: %q", h, got)
		}
	}
	if req.Host == "evil.example" || req.Header.Get("Host") != "" {
		t.Fatalf("Host override leaked: Host=%q header=%q", req.Host, req.Header.Get("Host"))
	}
	if got := req.Header.Get("X-Allowed-Feature"); got != "ok" {
		t.Fatalf("allowed model header missing: %q", got)
	}
	if got := req.Header.Get("X-Trace-Ok"); got != "yes" {
		t.Fatalf("allowed request header missing: %q", got)
	}
}
