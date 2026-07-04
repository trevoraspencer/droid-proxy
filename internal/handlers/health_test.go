package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/trevoraspencer/droid-proxy/internal/version"
)

func TestHealthIncludesBuildIdentity(t *testing.T) {
	oldVersion, oldCommit := version.Version, version.Commit
	version.Version, version.Commit = "v9.8.7", "commit987"
	t.Cleanup(func() {
		version.Version, version.Commit = oldVersion, oldCommit
	})

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/health", Health)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/health", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"status":  "ok",
		"service": "droid-proxy",
		"version": "v9.8.7",
		"commit":  "commit987",
	} {
		if body[key] != want {
			t.Fatalf("%s = %v, want %q; body=%s", key, body[key], want, w.Body.String())
		}
	}
	if _, ok := body["modified"].(bool); !ok {
		t.Fatalf("modified field missing or non-bool; body=%s", w.Body.String())
	}
}
