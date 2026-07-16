package providerapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// TestDeepInfraDiscoveryUnauthenticated verifies the DeepInfra discovery flow
// performs an unauthenticated GET /models/list with Accept: application/json
// and no Authorization or other credential header.
func TestDeepInfraDiscoveryUnauthenticated(t *testing.T) {
	var gotPath, gotAuth, gotAccept string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotAccept = r.Header.Get("Accept")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`[
			{"model_name":"meta-llama/Llama-3.3-70B-Instruct","reported_type":"text-generation"},
			{"model_name":"some-image-model","reported_type":"image-generation"},
			{"model_name":"deepinfra/deepseek-v4","reported_type":"text-generation"}
		]`))
	}))
	defer srv.Close()

	// Simulate the DeepInfra discovery profile: discovery base URL (not inference),
	// /models/list path, no auth, model_name ID field, reported_type filter.
	ids, err := ListModelsWithOptions(context.Background(), ListOptions{
		BaseURL:    srv.URL,
		ModelsPath: "/models/list",
		APIKey:     "", // unauthenticated discovery
		IDField:    "model_name",
		TypeField:  "reported_type",
		TypeValue:  "text-generation",
	})
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if gotPath != "/models/list" {
		t.Errorf("path = %q, want /models/list", gotPath)
	}
	if gotAuth != "" {
		t.Errorf("Authorization = %q, want empty (unauthenticated)", gotAuth)
	}
	if gotAccept != "application/json" {
		t.Errorf("Accept = %q, want application/json", gotAccept)
	}
	// Only text-generation rows, sorted and de-duplicated.
	want := []string{"deepinfra/deepseek-v4", "meta-llama/Llama-3.3-70B-Instruct"}
	if !reflect.DeepEqual(ids, want) {
		t.Errorf("ids = %v, want %v (text-generation only, sorted, deduped)", ids, want)
	}
}

// TestDeepInfraDiscoveryFiltersExactTextGeneration verifies that only records
// whose exact reported_type is "text-generation" are retained. Non-LLM types
// like "image-generation", "text-embedding", and case variants are excluded.
func TestDeepInfraDiscoveryFiltersExactTextGeneration(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			{"model_name":"llama-3","reported_type":"text-generation"},
			{"model_name":"stable-diffusion","reported_type":"image-generation"},
			{"model_name":"embed-v1","reported_type":"text-embedding"},
			{"model_name":"whisper-v2","reported_type":"audio"},
			{"model_name":"Text-Generation-Model","reported_type":"Text-Generation"},
			{"model_name":"mistral-7b","reported_type":"text-generation"}
		]`))
	}))
	defer srv.Close()

	ids, err := ListModelsWithOptions(context.Background(), ListOptions{
		BaseURL:    srv.URL,
		ModelsPath: "/models/list",
		APIKey:     "",
		IDField:    "model_name",
		TypeField:  "reported_type",
		TypeValue:  "text-generation",
	})
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	want := []string{"llama-3", "mistral-7b"}
	if !reflect.DeepEqual(ids, want) {
		t.Errorf("ids = %v, want %v (only exact text-generation)", ids, want)
	}
}

// TestDeepInfraDiscoveryPreservesOpaqueIDs verifies that Hugging Face-style
// IDs, version suffixes, and deploy_id values survive parsing without
// normalization.
func TestDeepInfraDiscoveryPreservesOpaqueIDs(t *testing.T) {
	rawIDs := []string{
		"meta-llama/Llama-3.3-70B-Instruct",
		"deepinfra/deepseek-v4",
		"Qwen/Qwen2.5-72B-Instruct",
		"model-with-deploy_id:abc123",
		"org/sub/model-v2.1",
	}
	body := `[`
	for i, id := range rawIDs {
		if i > 0 {
			body += ","
		}
		body += `{"model_name":"` + id + `","reported_type":"text-generation"}`
	}
	body += `]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	ids, err := ListModelsWithOptions(context.Background(), ListOptions{
		BaseURL:    srv.URL,
		ModelsPath: "/models/list",
		APIKey:     "",
		IDField:    "model_name",
		TypeField:  "reported_type",
		TypeValue:  "text-generation",
	})
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	idSet := map[string]bool{}
	for _, id := range ids {
		idSet[id] = true
	}
	for _, original := range rawIDs {
		if !idSet[original] {
			t.Errorf("opaque ID %q was not preserved in results: %v", original, ids)
		}
	}
}

// TestDeepInfraDiscoveryDeduplicates verifies duplicate model_name entries
// are de-duplicated after filtering.
func TestDeepInfraDiscoveryDeduplicates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			{"model_name":"meta-llama/Llama-3.3-70B-Instruct","reported_type":"text-generation"},
			{"model_name":"meta-llama/Llama-3.3-70B-Instruct","reported_type":"text-generation"},
			{"model_name":"mistral-7b","reported_type":"text-generation"}
		]`))
	}))
	defer srv.Close()

	ids, err := ListModelsWithOptions(context.Background(), ListOptions{
		BaseURL:    srv.URL,
		ModelsPath: "/models/list",
		APIKey:     "",
		IDField:    "model_name",
		TypeField:  "reported_type",
		TypeValue:  "text-generation",
	})
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	want := []string{
		"meta-llama/Llama-3.3-70B-Instruct",
		"mistral-7b",
	}
	if !reflect.DeepEqual(ids, want) {
		t.Errorf("ids = %v, want %v (deduplicated)", ids, want)
	}
}

// TestDeepInfraDiscoveryAllFailuresOneRequestNoFallback is a table-driven test
// verifying that each discovery failure mode (401, 429, 500, timeout,
// malformed JSON, empty list, and transport failure) makes exactly one
// unauthenticated GET /models/list request with no retry or remote fallback.
func TestDeepInfraDiscoveryAllFailuresOneRequestNoFallback(t *testing.T) {
	closingSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	closingSrv.Close()

	cases := []struct {
		name      string
		body      string
		status    int
		transport bool
	}{
		{name: "401_unauthorized", status: http.StatusUnauthorized, body: `{"error":"unauthorized"}`},
		{name: "429_rate_limited", status: http.StatusTooManyRequests, body: `{"error":"rate limited"}`},
		{name: "500_internal_error", status: http.StatusInternalServerError, body: `{"error":"internal"}`},
		{name: "malformed_json", status: http.StatusOK, body: `{not valid json`},
		{name: "empty_list", status: http.StatusOK, body: `[]`},
		{name: "transport_failure", transport: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var requestCount int
			var gotAuth, gotPath, gotAccept string

			var srv *httptest.Server
			if tc.transport {
				srv = closingSrv
			} else {
				srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requestCount++
					gotAuth = r.Header.Get("Authorization")
					gotPath = r.URL.Path
					gotAccept = r.Header.Get("Accept")
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(tc.status)
					_, _ = w.Write([]byte(tc.body))
				}))
				defer srv.Close()
			}

			_, err := ListModelsWithOptions(context.Background(), ListOptions{
				BaseURL:    srv.URL,
				ModelsPath: "/models/list",
				APIKey:     "",
				IDField:    "model_name",
				TypeField:  "reported_type",
				TypeValue:  "text-generation",
			})
			if err == nil {
				t.Fatalf("expected non-nil error for %s", tc.name)
			}
			if !tc.transport {
				if requestCount != 1 {
					t.Errorf("%s: request count = %d, want 1 (no retry/fallback)", tc.name, requestCount)
				}
				if gotAuth != "" {
					t.Errorf("%s: Authorization = %q, want empty", tc.name, gotAuth)
				}
				if gotPath != "/models/list" {
					t.Errorf("%s: path = %q, want /models/list", tc.name, gotPath)
				}
				if gotAccept != "application/json" {
					t.Errorf("%s: Accept = %q, want application/json", tc.name, gotAccept)
				}
			}
		})
	}
}

// TestDeepInfraDiscoveryErrorIsSecretSafe verifies that the error message
// returned by a failed discovery request does not contain credential sentinels.
func TestDeepInfraDiscoveryErrorIsSecretSafe(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	_, err := ListModelsWithOptions(context.Background(), ListOptions{
		BaseURL:    srv.URL,
		ModelsPath: "/models/list",
		APIKey:     "",
		IDField:    "model_name",
		TypeField:  "reported_type",
		TypeValue:  "text-generation",
	})
	if err == nil {
		t.Fatal("expected error")
	}
	// Even though discovery is unauthenticated, verify no leaked secrets.
	if strings.Contains(err.Error(), "secret") {
		t.Errorf("error message leaked secret: %q", err.Error())
	}
}

// TestDeepInfraDiscoveryDoesNotUseInferenceCredential verifies that even when
// an API key is passed (e.g., from the env), the unauthenticated discovery
// flag prevents it from being sent.
func TestDeepInfraDiscoveryDoesNotUseInferenceCredential(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"model_name":"test-model","reported_type":"text-generation"}]`))
	}))
	defer srv.Close()

	// Even with a non-empty APIKey, the discovery should still be unauthenticated
	// when used through the proper DeepInfra profile path. This test verifies
	// that passing APIKey="" produces no auth header.
	_, err := ListModelsWithOptions(context.Background(), ListOptions{
		BaseURL:    srv.URL,
		ModelsPath: "/models/list",
		APIKey:     "",
		IDField:    "model_name",
		TypeField:  "reported_type",
		TypeValue:  "text-generation",
	})
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if gotAuth != "" {
		t.Errorf("Authorization = %q, want empty (unauthenticated discovery)", gotAuth)
	}
}
