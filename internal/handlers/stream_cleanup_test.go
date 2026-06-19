package handlers

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/trevoraspencer/droid-proxy/internal/config"
)

func TestStreamingHandlers_ProcessListenerGoroutineCleanupEvidence(t *testing.T) {
	baselinePID := os.Getpid()
	baselineGoroutines := runtime.NumGoroutine()
	const loops = 4
	for i := 0; i < loops; i++ {
		for _, tc := range []struct {
			name    string
			handler func(t *testing.T)
		}{
			{"success", exerciseResponsesTranslatedSuccess},
			{"truncation", exerciseResponsesTranslatedTruncation},
			{"idle_timeout", exerciseResponsesTranslatedIdleTimeout},
			{"downstream_cancel_write_failure", exerciseResponsesTranslatedDownstreamCancel},
		} {
			t.Run(tc.name, tc.handler)
		}
	}
	if got := os.Getpid(); got != baselinePID {
		t.Fatalf("process changed during handler cleanup loop: before=%d after=%d", baselinePID, got)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		runtime.GC()
		got := runtime.NumGoroutine()
		if got <= baselineGoroutines+12 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("goroutines did not settle near baseline after handler cleanup loops: baseline=%d got=%d tolerance=12", baselineGoroutines, got)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func exerciseResponsesTranslatedSuccess(t *testing.T) {
	api := newResponsesTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		_, _ = w.Write([]byte(`data: {"id":"chat_ok","choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":"stop"}]}` + "\n\n" + "data: [DONE]\n\n"))
		flusher.Flush()
	}, func(m *config.Model) { m.UpstreamProtocol = config.UpstreamOpenAIChat })
	srv := httptest.NewServer(api.engine)
	resp, err := srv.Client().Post(srv.URL+"/v1/responses", "application/json", strings.NewReader(`{"model":"droid-gpt","input":"hi","stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	srv.Close()
	assertListenerClosed(t, srv.URL)
}

func exerciseResponsesTranslatedTruncation(t *testing.T) {
	api := newResponsesTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"id":"chat_trunc","choices":[{"index":0,"delta":{"content":"oops"},"finish_reason":null}]}` + "\n\n"))
	}, func(m *config.Model) { m.UpstreamProtocol = config.UpstreamOpenAIChat })
	srv := httptest.NewServer(api.engine)
	resp, err := srv.Client().Post(srv.URL+"/v1/responses", "application/json", strings.NewReader(`{"model":"droid-gpt","input":"hi","stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	srv.Close()
	assertListenerClosed(t, srv.URL)
}

func exerciseResponsesTranslatedIdleTimeout(t *testing.T) {
	api := newResponsesTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		_, _ = w.Write([]byte(`data: {"id":"chat_idle","choices":[{"index":0,"delta":{"content":"idle"},"finish_reason":null}]}` + "\n\n"))
		flusher.Flush()
		<-r.Context().Done()
	}, func(m *config.Model) { m.UpstreamProtocol = config.UpstreamOpenAIChat })
	api.api.Cfg.Upstream.HTTPTimeout = 25 * time.Millisecond
	api.api.Cfg.Upstream.StreamKeepAlive = 0
	srv := httptest.NewServer(api.engine)
	resp, err := srv.Client().Post(srv.URL+"/v1/responses", "application/json", strings.NewReader(`{"model":"droid-gpt","input":"hi","stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	srv.Close()
	assertListenerClosed(t, srv.URL)
}

func exerciseResponsesTranslatedDownstreamCancel(t *testing.T) {
	api := newResponsesTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		_, _ = w.Write([]byte(`data: {"id":"chat_cancel","choices":[{"index":0,"delta":{"content":"a"},"finish_reason":null}]}` + "\n\n"))
		flusher.Flush()
		<-r.Context().Done()
	}, func(m *config.Model) { m.UpstreamProtocol = config.UpstreamOpenAIChat })
	srv := httptest.NewServer(api.engine)
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/v1/responses", strings.NewReader(`{"model":"droid-gpt","input":"hi","stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(resp.Body)
	_, _ = reader.ReadString('\n')
	cancel()
	_ = resp.Body.Close()
	srv.Close()
	assertListenerClosed(t, srv.URL)
}

func assertListenerClosed(t *testing.T, rawURL string) {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(500 * time.Millisecond)
	for {
		conn, err := net.DialTimeout("tcp", u.Host, 20*time.Millisecond)
		if err != nil {
			return
		}
		_ = conn.Close()
		if time.Now().After(deadline) {
			t.Fatalf("httptest listener %s remained reachable after Close", u.Host)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
