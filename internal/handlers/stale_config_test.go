package handlers

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const staleConfigHintText = "config.yaml changed since the proxy started"

// bindConfigSource points the API's config at a real temp file whose mtime the
// test controls, mimicking a server that loaded its config at loadedAt.
func bindConfigSource(t *testing.T, api *testAPI, loadedAt, onDisk time.Time) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("# test config\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, onDisk, onDisk); err != nil {
		t.Fatal(err)
	}
	api.api.Cfg.SourcePath = path
	api.api.Cfg.SourceModTime = loadedAt
}

func TestChatUnknownModel404CarriesStaleConfigHint(t *testing.T) {
	api := newTestAPI(t, func(http.ResponseWriter, *http.Request) {
		t.Fatal("upstream must not be called")
	}, nil)
	loaded := time.Now().Add(-time.Hour)
	bindConfigSource(t, api, loaded, loaded.Add(30*time.Minute))

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"no-such-alias"}`))
	api.engine.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, staleConfigHintText) || !strings.Contains(body, "restart droid-proxy") {
		t.Fatalf("404 body missing stale-config hint:\n%s", body)
	}
	if !strings.Contains(body, "not configured") {
		t.Fatalf("hint must not replace the original error:\n%s", body)
	}
}

func TestChatUnknownModel404OmitsHintWhenConfigUnchanged(t *testing.T) {
	api := newTestAPI(t, func(http.ResponseWriter, *http.Request) {
		t.Fatal("upstream must not be called")
	}, nil)
	loaded := time.Now().Add(-time.Hour)
	bindConfigSource(t, api, loaded, loaded)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"no-such-alias"}`))
	api.engine.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
	if strings.Contains(w.Body.String(), staleConfigHintText) {
		t.Fatalf("hint must not appear when the file is unchanged:\n%s", w.Body.String())
	}
}

func TestChatUnknownModel404OmitsHintWithoutSourcePath(t *testing.T) {
	api := newTestAPI(t, func(http.ResponseWriter, *http.Request) {
		t.Fatal("upstream must not be called")
	}, nil)
	// No SourcePath bound (config parsed from bytes, e.g. tests or stdin).

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"no-such-alias"}`))
	api.engine.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
	if strings.Contains(w.Body.String(), staleConfigHintText) {
		t.Fatalf("hint must not appear without a source path:\n%s", w.Body.String())
	}
}

func TestMessagesUnknownModel404CarriesStaleConfigHint(t *testing.T) {
	api := newAnthropicTestAPI(t, func(http.ResponseWriter, *http.Request) {
		t.Fatal("upstream must not be called")
	}, nil)
	loaded := time.Now().Add(-time.Hour)
	bindConfigSource(t, api, loaded, loaded.Add(30*time.Minute))

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"no-such-alias","max_tokens":16,"messages":[]}`))
	api.engine.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, staleConfigHintText) {
		t.Fatalf("anthropic 404 body missing stale-config hint:\n%s", body)
	}
	if !strings.Contains(body, "not_found_error") {
		t.Fatalf("anthropic error envelope must be preserved:\n%s", body)
	}
}
