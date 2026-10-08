package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

func TestBrowserOriginsRejectedBeforeInference(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, path := range []string{"/v1/chat/completions", "/v1/responses", "/v1/messages", "/v1/messages/count_tokens"} {
		engine := gin.New()
		engine.Use(BrowserOriginGuard())
		calls := 0
		engine.POST(path, func(c *gin.Context) { calls++; c.Status(http.StatusNoContent) })
		for _, origin := range []string{"https://example.com", "null", "http://127.0.0.1:9787", ""} {
			req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"model":"configured","input":"hello"}`))
			// text/plain can be sent by a browser without a CORS preflight.
			req.Header.Set("Content-Type", "text/plain")
			req.Header["Origin"] = []string{origin}
			w := httptest.NewRecorder()
			engine.ServeHTTP(w, req)
			if w.Code != http.StatusForbidden || calls != 0 {
				t.Fatalf("origin %q reached inference: %d", origin, w.Code)
			}
			if strings.Contains(path, "/messages") && gjson.GetBytes(w.Body.Bytes(), "type").String() != "error" {
				t.Fatal("lost Anthropic error envelope")
			}
		}
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"model":"configured"}`)))
		if w.Code != http.StatusNoContent || calls != 1 {
			t.Fatal("CLI request without Origin was rejected")
		}
	}
}
