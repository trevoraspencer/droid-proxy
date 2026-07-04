package providerapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
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

func TestParseModelIDsBareObjectArray(t *testing.T) {
	body := []byte(`[{"id":"b"},{"id":"a"},{"name":"c"}]`)
	got, err := parseModelIDs(body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := []string{"a", "b", "c"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParseModelIDsUnrecognizedShape(t *testing.T) {
	for _, body := range []string{`{"foo":1}`, `42`, ``, `   `} {
		if _, err := parseModelIDs([]byte(body)); err == nil {
			t.Errorf("expected error for %q", body)
		}
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

func TestListModelsReadError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := `{"data":[{"id":"partial-but-valid"}]}`
		w.Header().Set("Content-Length", "4096")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	_, err := ListModels(context.Background(), srv.URL, "", "", "")
	if err == nil || !strings.Contains(err.Error(), "read provider models response") {
		t.Fatalf("expected provider response read error, got %v", err)
	}
}

func TestListModelsOversizedBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", (4<<20)+1)))
	}))
	defer srv.Close()

	_, err := ListModels(context.Background(), srv.URL, "", "", "")
	if err == nil || !strings.Contains(err.Error(), "provider models response too large") {
		t.Fatalf("expected provider body too large error, got %v", err)
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
