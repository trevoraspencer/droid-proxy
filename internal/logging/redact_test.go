package logging

import (
	"strings"
	"testing"
)

func TestRedact(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    string
		mustNot string
	}{
		{
			name:    "auth bearer",
			in:      "Authorization: Bearer sk-abc123def456ghi789jkl",
			mustNot: "sk-abc123def456ghi789jkl",
		},
		{
			name:    "x-api-key",
			in:      "x-api-key: my-secret-value-here-long",
			mustNot: "my-secret-value-here-long",
		},
		{
			name:    "anthropic header",
			in:      "anthropic-api-key: sk-ant-abc123def456ghi789",
			mustNot: "sk-ant-abc123def456ghi789",
		},
		{
			name:    "json api_key",
			in:      `{"api_key":"super-secret-token-xxxxxxxx","other":1}`,
			mustNot: "super-secret-token-xxxxxxxx",
		},
		{
			name:    "json apiKey camel",
			in:      `{"apiKey":"super-secret-token-xxxxxxxx"}`,
			mustNot: "super-secret-token-xxxxxxxx",
		},
		{
			name:    "query string api_key",
			in:      "GET /v1/models?api_key=secret-token-value-1234",
			mustNot: "secret-token-value-1234",
		},
		{
			name:    "loose sk- token",
			in:      "got sk-abcdefghijklmnopqrst in body",
			mustNot: "sk-abcdefghijklmnopqrst",
		},
		// Fireworks credential shapes (fw_ and fpk_) must be redacted in
		// Authorization: Bearer context. The proxy does not route by prefix,
		// but redaction must recognize both shapes as opaque secrets.
		{
			name:    "fireworks standard fw_ bearer",
			in:      "Authorization: Bearer fw_standard_secret_123",
			mustNot: "fw_standard_secret_123",
		},
		{
			name:    "fireworks fire pass fpk_ bearer",
			in:      "Authorization: Bearer fpk_firepass_secret_456",
			mustNot: "fpk_firepass_secret_456",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Redact(c.in)
			if c.mustNot != "" && strings.Contains(got, c.mustNot) {
				t.Errorf("Redact(%q) = %q; must not contain %q", c.in, got, c.mustNot)
			}
			if !strings.Contains(got, "***") {
				t.Errorf("Redact(%q) = %q; expected *** placeholder", c.in, got)
			}
			if c.want != "" && got != c.want {
				t.Errorf("Redact(%q) = %q; want %q", c.in, got, c.want)
			}
		})
	}
}

func TestRedact_QueryCredentialParameters(t *testing.T) {
	cases := []struct {
		name        string
		in          string
		mustNot     []string
		mustContain []string
	}{
		{
			name:    "all supported query credential names",
			in:      "/v1/chat/completions?token=tok-secret&access_token=access-secret&refresh_token=refresh-secret&id_token=id-secret&auth=auth-secret&authorization=authorization-secret&key=key-secret&api_key=api-key-secret&apiKey=api-key-camel-secret&credential=credential-secret&secret=secret-secret&password=password-secret",
			mustNot: []string{"tok-secret", "access-secret", "refresh-secret", "id-secret", "auth-secret", "authorization-secret", "key-secret", "api-key-secret", "api-key-camel-secret", "credential-secret", "secret-secret", "password-secret"},
			mustContain: []string{
				"token=***",
				"access_token=***",
				"refresh_token=***",
				"id_token=***",
				"auth=***",
				"authorization=***",
				"key=***",
				"api_key=***",
				"apiKey=***",
				"credential=***",
				"secret=***",
				"password=***",
			},
		},
		{
			name:        "case insensitive repeated and url encoded values",
			in:          "/v1/models?ToKeN=first-secret&TOKEN=second%2Fsecret%3Dvalue&Password=p%40ss%26word&model=keep-me",
			mustNot:     []string{"first-secret", "second%2Fsecret%3Dvalue", "p%40ss%26word"},
			mustContain: []string{"ToKeN=***", "TOKEN=***", "Password=***", "model=keep-me"},
		},
		{
			name:        "raw query and fragment",
			in:          "token=raw-secret&debug=true#access_token=fragment-secret",
			mustNot:     []string{"raw-secret", "fragment-secret"},
			mustContain: []string{"token=***", "debug=true", "access_token=***"},
		},
		{
			name:        "benign query parameters are preserved",
			in:          "/v1/chat/completions?model=droid&max_tokens=123&tool_choice=auto&trace_id=debug-token-looking-value",
			mustContain: []string{"model=droid", "max_tokens=123", "tool_choice=auto", "trace_id=debug-token-looking-value"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Redact(c.in)
			for _, needle := range c.mustNot {
				if strings.Contains(got, needle) {
					t.Fatalf("Redact(%q) = %q; must not contain %q", c.in, got, needle)
				}
			}
			for _, needle := range c.mustContain {
				if !strings.Contains(got, needle) {
					t.Fatalf("Redact(%q) = %q; must contain %q", c.in, got, needle)
				}
			}
		})
	}
}

func TestRedact_JSONCredentialFields(t *testing.T) {
	in := `{"access_token":"ya29.generic-access","refresh_token":"rt-refresh","id_token":"header.payload.sig","token":"opaque-token","secret":"client-secret","authorization":"Bearer generic-token","credential":"oauth-credential","model":"keep-me","trace_id":"debug-token-looking-value"}`
	got := Redact(in)
	for _, needle := range []string{
		"ya29.generic-access",
		"rt-refresh",
		"header.payload.sig",
		"opaque-token",
		"client-secret",
		"Bearer generic-token",
		"oauth-credential",
	} {
		if strings.Contains(got, needle) {
			t.Fatalf("Redact(%q) = %q; must not contain %q", in, got, needle)
		}
	}
	for _, needle := range []string{
		`"access_token":"***"`,
		`"refresh_token":"***"`,
		`"id_token":"***"`,
		`"token":"***"`,
		`"secret":"***"`,
		`"authorization":"***"`,
		`"credential":"***"`,
		`"model":"keep-me"`,
		`"trace_id":"debug-token-looking-value"`,
	} {
		if !strings.Contains(got, needle) {
			t.Fatalf("Redact(%q) = %q; must contain %q", in, got, needle)
		}
	}
}

func TestRedact_LeavesNonSecretsAlone(t *testing.T) {
	in := "GET /v1/chat/completions HTTP/1.1\r\nUser-Agent: droid\r\n"
	if got := Redact(in); got != in {
		t.Errorf("Redact altered non-secret text: %q -> %q", in, got)
	}
}

func TestRedactBytes_Empty(t *testing.T) {
	if got := RedactBytes(nil); got != nil {
		t.Errorf("expected nil for nil input, got %v", got)
	}
	if got := RedactBytes([]byte{}); len(got) != 0 {
		t.Errorf("expected empty for empty input, got %v", got)
	}
}
