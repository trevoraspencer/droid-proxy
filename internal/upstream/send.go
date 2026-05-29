package upstream

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"

	"droid-proxy/internal/config"
)

// httpReqAdapter wraps *http.Request to satisfy httpHeaderSetter.
type httpReqAdapter struct{ r *http.Request }

func (a httpReqAdapter) SetHeader(name, value string) { a.r.Header.Set(name, value) }

// AdaptRequest exposes the adapter used by Send/Stream to other packages.
func AdaptRequest(r *http.Request) httpHeaderSetter { return httpReqAdapter{r: r} }

// SendOptions controls a single upstream HTTP call.
type SendOptions struct {
	Model        *config.Model
	Method       string
	Path         string // path appended to model.BaseURL (e.g. "/chat/completions"); leading slash optional
	Body         []byte
	IsStream     bool
	ExtraHeaders map[string]string // request-time overrides (e.g. anthropic-beta from client)
	// AcceptOverride overrides the default Accept header. Default: text/event-stream when IsStream, otherwise application/json.
	AcceptOverride string
}

// Build creates and prepares an upstream request with auth headers, model
// extra_headers, content-type, accept, etc. It does NOT execute the request.
func (c *Client) Build(ctx context.Context, opts SendOptions) (*http.Request, error) {
	if opts.Model == nil {
		return nil, fmt.Errorf("upstream.Build: model is nil")
	}
	upstreamURL, err := buildURL(opts.Model.BaseURL, opts.Path)
	if err != nil {
		return nil, err
	}
	if upstreamURL == "" {
		return nil, fmt.Errorf("model %q: missing base_url", opts.Model.Alias)
	}

	method := opts.Method
	if method == "" {
		method = http.MethodPost
	}

	req, err := http.NewRequestWithContext(ctx, method, upstreamURL, bytes.NewReader(opts.Body))
	if err != nil {
		return nil, err
	}
	if opts.Body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	accept := opts.AcceptOverride
	if accept == "" {
		if opts.IsStream {
			accept = "text/event-stream"
		} else {
			accept = "application/json"
		}
	}
	req.Header.Set("Accept", accept)
	if opts.IsStream {
		req.Header.Set("Cache-Control", "no-cache")
	}
	req.Header.Set("User-Agent", "droid-proxy/"+versionString())

	key, err := ResolveAPIKey(opts.Model)
	if err != nil {
		return nil, err
	}
	ApplyAuthHeader(httpReqAdapter{r: req}, opts.Model, key)

	for k, v := range opts.Model.ExtraHeaders {
		if IsReservedOutboundHeader(k) {
			continue
		}
		req.Header.Set(k, v)
	}
	for k, v := range opts.ExtraHeaders {
		if IsReservedOutboundHeader(k) {
			continue
		}
		req.Header.Set(k, v)
	}
	return req, nil
}

func buildURL(baseURL, endpointPath string) (string, error) {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return "", nil
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		return "", err
	}
	u.RawQuery = ""
	u.Fragment = ""
	u.User = nil
	if endpointPath != "" {
		parts := []string{u.Path}
		for _, p := range strings.Split(endpointPath, "/") {
			if p != "" {
				parts = append(parts, p)
			}
		}
		u.Path = path.Join(parts...)
		if !strings.HasPrefix(u.Path, "/") {
			u.Path = "/" + u.Path
		}
	}
	return u.String(), nil
}

// Do executes a prepared request and returns the response. Stream callers
// must call resp.Body.Close themselves once they are done reading.
func (c *Client) Do(req *http.Request) (*http.Response, error) {
	if strings.Contains(req.Header.Get("Accept"), "text/event-stream") {
		streamClient := *c.HTTP
		streamClient.Timeout = 0
		return streamClient.Do(req)
	}
	return c.HTTP.Do(req)
}

// ReadAllAndClose reads the body fully then closes it.
func ReadAllAndClose(r io.ReadCloser) ([]byte, error) {
	defer func() { _ = r.Close() }()
	return io.ReadAll(r)
}

var ErrBodyTooLarge = errors.New("upstream body too large")

// ReadAllAndCloseLimit reads up to limit+1 bytes and closes the body. A limit of
// zero disables the cap. When the body exceeds the cap, ErrBodyTooLarge is
// returned and the partial bytes are intentionally discarded by callers.
func ReadAllAndCloseLimit(r io.ReadCloser, limit int64) ([]byte, error) {
	defer func() { _ = r.Close() }()
	if limit <= 0 {
		return io.ReadAll(r)
	}
	lr := &io.LimitedReader{R: r, N: limit + 1}
	body, err := io.ReadAll(lr)
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, ErrBodyTooLarge
	}
	return body, nil
}
