package providerapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// TestBasetenDiscoveryAuthenticated verifies the Baseten discovery flow
// performs an authenticated GET /v1/models with Bearer auth and returns
// sorted, de-duplicated opaque model slugs.
func TestBasetenDiscoveryAuthenticated(t *testing.T) {
	var gotPath, gotAuth, gotAccept string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotAccept = r.Header.Get("Accept")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[
			{"id":"org/model-a"},
			{"id":"org/model-b"},
			{"id":"org/model-a"},
			{"id":"org/sub:custom-slug"}
		]}`))
	}))
	defer srv.Close()

	// Simulate the Baseten profile: base URL ending in /v1, empty
	// ModelsPath (defaults to "models"), Bearer auth.
	ids, err := ListModelsWithOptions(context.Background(), ListOptions{
		BaseURL:    srv.URL + "/v1",
		ModelsPath: "", // empty = default "models"
		APIKey:     "baseten-key",
		AuthHeader: "", // empty = Authorization
		AuthScheme: "", // empty = Bearer
	})
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if gotPath != "/v1/models" {
		t.Errorf("path = %q, want /v1/models", gotPath)
	}
	if gotAuth != "Bearer baseten-key" {
		t.Errorf("Authorization = %q, want Bearer baseten-key", gotAuth)
	}
	if gotAccept != "application/json" {
		t.Errorf("Accept = %q, want application/json", gotAccept)
	}
	// Sorted and de-duplicated.
	want := []string{"org/model-a", "org/model-b", "org/sub:custom-slug"}
	if !reflect.DeepEqual(ids, want) {
		t.Errorf("ids = %v, want %v (sorted, deduped)", ids, want)
	}
}

// TestBasetenDiscoveryPreservesOpaqueSlugs verifies that organization
// prefixes, case, dots, hyphens, underscores, colons, and slashes survive
// the discovery parsing without normalization.
func TestBasetenDiscoveryPreservesOpaqueSlugs(t *testing.T) {
	rawIDs := []string{
		"org/DeepSeek-V4.Pro",
		"org/custom-model_v2",
		"org/sub:deploy-1",
		"org/mixed-CASE.Model-Name",
		"org/a/b/c/path-model",
	}
	body := `{"data":[`
	for i, id := range rawIDs {
		if i > 0 {
			body += ","
		}
		body += `{"id":"` + id + `"}`
	}
	body += `]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	ids, err := ListModelsWithOptions(context.Background(), ListOptions{
		BaseURL: srv.URL + "/v1",
		APIKey:  "k",
	})
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	// All IDs must be present (sorted by the parser).
	idSet := map[string]bool{}
	for _, id := range ids {
		idSet[id] = true
	}
	for _, original := range rawIDs {
		if !idSet[original] {
			t.Errorf("opaque slug %q was not preserved in results: %v", original, ids)
		}
	}
}

// TestBasetenDiscoveryFailureNoFallback verifies that a discovery failure
// returns an error without attempting a fallback path.
func TestBasetenDiscoveryFailureNoFallback(t *testing.T) {
	var requestCount int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	_, err := ListModelsWithOptions(context.Background(), ListOptions{
		BaseURL: srv.URL + "/v1",
		APIKey:  "invalid",
	})
	if err == nil {
		t.Fatal("expected error on 401")
	}
	if requestCount != 1 {
		t.Errorf("request count = %d, want 1 (no retry/fallback)", requestCount)
	}
}

// TestBasetenDiscoveryAllFailuresOneRequestNoFallback is a table-driven test
// verifying that each discovery failure mode (401, 500, malformed JSON, empty
// results, and transport failure) makes exactly one authenticated GET /v1/models
// request with no retry or remote fallback path, and returns a non-nil error.
// This provides VAL-BASETEN-003 evidence for all five bounded failure modes.
func TestBasetenDiscoveryAllFailuresOneRequestNoFallback(t *testing.T) {
	// Set up a server that can be closed to simulate transport failure.
	closingSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	closingSrv.Close() // close immediately to force transport failure

	cases := []struct {
		name      string
		body      string
		status    int
		transport bool // true = use the pre-closed server for transport failure
	}{
		{
			name:   "401_unauthorized",
			status: http.StatusUnauthorized,
			body:   `{"error":"unauthorized"}`,
		},
		{
			name:   "500_internal_server_error",
			status: http.StatusInternalServerError,
			body:   `{"error":"internal"}`,
		},
		{
			name:   "malformed_json",
			status: http.StatusOK,
			body:   `{not valid json`,
		},
		{
			name:   "empty_results",
			status: http.StatusOK,
			body:   `{"data":[]}`,
		},
		{
			name:      "transport_failure",
			transport: true,
		},
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
				BaseURL: srv.URL + "/v1",
				APIKey:  "baseten-test-key",
			})
			if err == nil {
				t.Fatalf("expected non-nil error for %s", tc.name)
			}
			if !tc.transport {
				if requestCount != 1 {
					t.Errorf("%s: request count = %d, want 1 (no retry/fallback)", tc.name, requestCount)
				}
				if gotAuth != "Bearer baseten-test-key" {
					t.Errorf("%s: Authorization = %q, want Bearer baseten-test-key", tc.name, gotAuth)
				}
				if gotPath != "/v1/models" {
					t.Errorf("%s: path = %q, want /v1/models", tc.name, gotPath)
				}
				if gotAccept != "application/json" {
					t.Errorf("%s: Accept = %q, want application/json", tc.name, gotAccept)
				}
			}
		})
	}
}

// TestBasetenDiscoveryFailureErrorIsSecretSafe verifies that the error message
// returned by a failed discovery request does not contain the API key or other
// credential sentinels.
func TestBasetenDiscoveryErrorIsSecretSafe(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	apiKey := "baseten-secret-key-12345"
	_, err := ListModelsWithOptions(context.Background(), ListOptions{
		BaseURL: srv.URL + "/v1",
		APIKey:  apiKey,
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), apiKey) {
		t.Errorf("error message leaked API key: %q", err.Error())
	}
}
