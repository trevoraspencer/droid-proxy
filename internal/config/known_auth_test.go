package config

import "testing"

func TestKnownAuthAnthropicDiscoveryMetadata(t *testing.T) {
	ka, ok := LookupKnownAuth("anthropic")
	if !ok {
		t.Fatal("anthropic profile missing")
	}
	if ka.ModelsPath != "/v1/models" {
		t.Fatalf("ModelsPath = %q, want /v1/models", ka.ModelsPath)
	}
	if ka.AuthHeader != "x-api-key" {
		t.Fatalf("AuthHeader = %q, want x-api-key", ka.AuthHeader)
	}
	if got := ka.ExtraHeaders["anthropic-version"]; got != "2023-06-01" {
		t.Fatalf("anthropic-version = %q, want 2023-06-01", got)
	}
}
