package main

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/trevoraspencer/droid-proxy/internal/config"
)

const validPoolHealthJSON = `{"strategy":"round_robin","codex_account_count":0,"eligible_count":0,"accounts":[]}`

func TestFetchPoolHealth(t *testing.T) {
	type requestDetails struct {
		auth string
		path string
	}
	requests := make(chan requestDetails, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- requestDetails{
			auth: r.Header.Get("Authorization"),
			path: r.URL.Path,
		}
		_, _ = io.WriteString(w, validPoolHealthJSON)
	}))
	t.Cleanup(srv.Close)

	cfg := &config.Config{ClientAuth: config.ClientAuth{
		Enabled: true,
		APIKeys: []string{"test-key"},
	}}
	out, err := fetchPoolHealth(srv.URL, cfg)
	if err != nil {
		t.Fatalf("fetchPoolHealth: %v", err)
	}
	request := <-requests
	if request.path != "/v1/oauth/pool-health" {
		t.Errorf("request path = %q, want /v1/oauth/pool-health", request.path)
	}
	if request.auth != "Bearer test-key" {
		t.Errorf("Authorization = %q, want Bearer test-key", request.auth)
	}
	if !strings.Contains(out, "strategy: round_robin  accounts: 0  eligible: 0") {
		t.Fatalf("unexpected pool-health output:\n%s", out)
	}
}

func TestFetchPoolHealthAcceptsResponseExactlyAtLimit(t *testing.T) {
	body := append([]byte(validPoolHealthJSON), bytes.Repeat(
		[]byte(" "),
		int(maxPoolHealthResponseBytes)-len(validPoolHealthJSON),
	)...)
	if int64(len(body)) != maxPoolHealthResponseBytes {
		t.Fatalf("test response length = %d, want %d", len(body), maxPoolHealthResponseBytes)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)

	if _, err := fetchPoolHealth(srv.URL, nil); err != nil {
		t.Fatalf("fetchPoolHealth rejected response at limit: %v", err)
	}
}

func TestFetchPoolHealthRejectsOversizedResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, strings.Repeat("x", int(maxPoolHealthResponseBytes)+1))
	}))
	t.Cleanup(srv.Close)

	_, err := fetchPoolHealth(srv.URL, nil)
	if err == nil || !strings.Contains(err.Error(), "pool-health response exceeds 1048576-byte limit") {
		t.Fatalf("fetchPoolHealth oversized response error = %v", err)
	}
}

func TestFetchPoolHealthReturnsTruncatedResponseReadError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(validPoolHealthJSON)+10))
		_, _ = io.WriteString(w, validPoolHealthJSON)
	}))
	t.Cleanup(srv.Close)

	_, err := fetchPoolHealth(srv.URL, nil)
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("fetchPoolHealth truncated response error = %v, want unexpected EOF", err)
	}
}
