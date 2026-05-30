package providerapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestParseModelIDsOpenAIShape(t *testing.T) {
	body := []byte(`{"object":"list","data":[{"id":"gpt-4o"},{"id":"gpt-4o-mini"}]}`)
	got, err := parseModelIDs(body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := []string{"gpt-4o", "gpt-4o-mini"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParseModelIDsModelsShape(t *testing.T) {
	body := []byte(`{"models":[{"name":"llama3"},{"id":"mistral"}]}`)
	got, err := parseModelIDs(body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := []string{"llama3", "mistral"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParseModelIDsBareArray(t *testing.T) {
	body := []byte(`["a","b","a"]`)
	got, err := parseModelIDs(body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := []string{"a", "b"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v (deduped+sorted)", got, want)
	}
}

func TestListModelsSendsAuthAndParses(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"z"},{"id":"a"}]}`))
	}))
	defer srv.Close()

	ids, err := ListModels(context.Background(), srv.URL, "secret", "", "")
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if gotAuth != "Bearer secret" {
		t.Errorf("Authorization = %q, want Bearer secret", gotAuth)
	}
	want := []string{"a", "z"}
	if !reflect.DeepEqual(ids, want) {
		t.Errorf("ids = %v, want %v", ids, want)
	}
}

func TestListModelsErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	if _, err := ListModels(context.Background(), srv.URL, "k", "", ""); err == nil {
		t.Fatal("expected error on 401")
	}
}

func TestListModelsCustomAuthHeader(t *testing.T) {
	var gotKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("x-api-key")
		_, _ = w.Write([]byte(`{"data":[{"id":"m"}]}`))
	}))
	defer srv.Close()
	if _, err := ListModels(context.Background(), srv.URL, "abc", "x-api-key", ""); err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if gotKey != "abc" {
		t.Errorf("x-api-key = %q, want abc (no scheme)", gotKey)
	}
}
