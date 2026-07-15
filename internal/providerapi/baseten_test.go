package providerapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
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
