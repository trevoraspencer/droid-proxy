package upstream

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/trevoraspencer/droid-proxy/internal/config"
)

func TestStreamingRequestDeadlines(t *testing.T) {
	for _, scenario := range []string{"headers", "error_body", "healthy_stream"} {
		t.Run(scenario, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if scenario == "headers" {
					<-r.Context().Done()
					return
				}
				if scenario == "error_body" {
					w.WriteHeader(http.StatusTooManyRequests)
					w.(http.Flusher).Flush()
					<-r.Context().Done()
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				w.(http.Flusher).Flush()
				select {
				case <-time.After(150 * time.Millisecond):
					_, _ = io.WriteString(w, "data: [DONE]\n\n")
				case <-r.Context().Done():
				}
			}))
			defer server.Close()
			client := NewClient(&config.Config{Upstream: config.Upstream{HTTPTimeout: 50 * time.Millisecond}})
			defer client.HTTP.CloseIdleConnections()
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL, nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Accept", "text/event-stream")
			start := time.Now()
			resp, err := client.Do(req)
			if scenario == "headers" {
				if err == nil {
					resp.Body.Close()
					t.Fatal("stalled response headers must time out")
				}
				if time.Since(start) > time.Second {
					t.Fatal("response waited for client cancellation")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			if scenario == "error_body" {
				if err == nil {
					t.Fatal("stalled error body must fail")
				}
				if time.Since(start) > time.Second {
					t.Fatal("error body waited for client cancellation")
				}
			} else if err != nil || string(body) != "data: [DONE]\n\n" {
				t.Fatalf("healthy stream cut off: %q, %v", body, err)
			}
		})
	}
}
