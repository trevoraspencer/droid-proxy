package upstream

import (
	"net/http"
	"testing"

	"droid-proxy/internal/config"
)

func TestResolveAPIKey(t *testing.T) {
	t.Setenv("DROID_TEST_KEY", "actual-secret")
	m := &config.Model{Alias: "x", APIKeyEnv: "DROID_TEST_KEY"}
	k, err := ResolveAPIKey(m)
	if err != nil {
		t.Fatal(err)
	}
	if k != "actual-secret" {
		t.Errorf("got %q", k)
	}
}

func TestResolveAPIKey_MissingEnv(t *testing.T) {
	m := &config.Model{Alias: "x", APIKeyEnv: "DROID_TEST_MISSING_KEY_XYZ"}
	_, err := ResolveAPIKey(m)
	if err == nil {
		t.Fatal("expected error for empty env var")
	}
}

func TestResolveAPIKey_NoneConfigured(t *testing.T) {
	m := &config.Model{Alias: "x"}
	k, err := ResolveAPIKey(m)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if k != "" {
		t.Errorf("expected empty key, got %q", k)
	}
}

func TestResolveAPIKey_LocalNoAuthKnownProviders(t *testing.T) {
	for _, knownAuth := range []string{"ollama", "vllm"} {
		t.Run(knownAuth, func(t *testing.T) {
			m := &config.Model{Alias: "x", KnownAuth: knownAuth}
			if err := config.HydrateModel(m); err != nil {
				t.Fatal(err)
			}
			k, err := ResolveAPIKey(m)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if k != "" {
				t.Fatalf("expected empty key, got %q", k)
			}
			req, _ := http.NewRequest("POST", "http://127.0.0.1", nil)
			ApplyAuthHeader(httpReqAdapter{r: req}, m, k)
			if got := req.Header.Get("Authorization"); got != "" {
				t.Fatalf("local no-auth provider got Authorization header %q", got)
			}
		})
	}
}

func TestResolveAPIKey_FromKnownAuthEnv(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "from-known-auth")
	m := &config.Model{Alias: "x", KnownAuth: "deepseek"}
	k, err := ResolveAPIKey(m)
	if err != nil {
		t.Fatal(err)
	}
	if k != "from-known-auth" {
		t.Errorf("expected resolution via known_auth env, got %q", k)
	}
}

func TestApplyAuthHeader_OpenAIDefault(t *testing.T) {
	req, _ := http.NewRequest("POST", "https://x.test", nil)
	m := &config.Model{Alias: "x"}
	ApplyAuthHeader(httpReqAdapter{r: req}, m, "the-key")
	if got := req.Header.Get("Authorization"); got != "Bearer the-key" {
		t.Errorf("Authorization: %q", got)
	}
}

func TestApplyAuthHeader_KnownAuthOpenAICompatibleBearer(t *testing.T) {
	for _, knownAuth := range []string{
		"deepseek",
		"openai",
		"xai",
		"groq",
		"kimi",
		"together",
		"fireworks",
		"mistral",
		"zai",
		"iflow",
		"ollama",
		"vllm",
	} {
		t.Run(knownAuth, func(t *testing.T) {
			req, _ := http.NewRequest("POST", "https://x.test", nil)
			m := &config.Model{Alias: "x", KnownAuth: knownAuth}
			ApplyAuthHeader(httpReqAdapter{r: req}, m, "the-key")
			if got := req.Header.Get("Authorization"); got != "Bearer the-key" {
				t.Errorf("Authorization: %q", got)
			}
			if got := req.Header.Get("x-api-key"); got != "" {
				t.Errorf("x-api-key should be empty for %s, got %q", knownAuth, got)
			}
		})
	}
}

func TestApplyAuthHeader_Anthropic(t *testing.T) {
	req, _ := http.NewRequest("POST", "https://x.test", nil)
	m := &config.Model{Alias: "x", KnownAuth: "anthropic"}
	ApplyAuthHeader(httpReqAdapter{r: req}, m, "sk-ant-test")
	if got := req.Header.Get("x-api-key"); got != "sk-ant-test" {
		t.Errorf("x-api-key: %q", got)
	}
	if got := req.Header.Get("Authorization"); got != "" {
		t.Errorf("Authorization should be empty for anthropic, got %q", got)
	}
}

func TestApplyAuthHeader_EmptyKeyNoOp(t *testing.T) {
	req, _ := http.NewRequest("POST", "https://x.test", nil)
	m := &config.Model{Alias: "x"}
	ApplyAuthHeader(httpReqAdapter{r: req}, m, "")
	if req.Header.Get("Authorization") != "" {
		t.Errorf("expected no Authorization for empty key")
	}
}
