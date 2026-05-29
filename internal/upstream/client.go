package upstream

import (
	"net/http"
	"time"

	"droid-proxy/internal/config"
)

// Client wraps an http.Client used for upstream requests.
type Client struct {
	HTTP *http.Client
	Cfg  *config.Config
}

// NewClient builds an HTTP client appropriate for streaming and non-streaming proxying.
func NewClient(cfg *config.Config) *Client {
	timeout := cfg.Upstream.HTTPTimeout
	if timeout <= 0 {
		timeout = 600 * time.Second
	}
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ForceAttemptHTTP2:     true,
	}
	return &Client{
		HTTP: &http.Client{
			Timeout:   timeout,
			Transport: transport,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				// don't auto-follow redirects on streaming endpoints
				return http.ErrUseLastResponse
			},
		},
		Cfg: cfg,
	}
}
