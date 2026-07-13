// Package handlers contains the HTTP handlers for droid-proxy's endpoints.
package handlers

import (
	"errors"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/sjson"

	"github.com/trevoraspencer/droid-proxy/internal/config"
	"github.com/trevoraspencer/droid-proxy/internal/logging"
	"github.com/trevoraspencer/droid-proxy/internal/upstream"
)

const TraceRequestBodyKey = "trace_request_body"

// ErrorBody is the OpenAI-shaped error envelope. Anthropic and Responses payloads
// use their own envelopes (built in their respective translators); use this only
// for chat/completions and /v1/models style errors.
type ErrorBody struct {
	Error ErrorDetail `json:"error"`
}

type ErrorDetail struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code,omitempty"`
}

// WriteJSONError writes an OpenAI-shaped error envelope with status.
func WriteJSONError(c *gin.Context, status int, errType, msg string) {
	c.JSON(status, ErrorBody{Error: ErrorDetail{Message: msg, Type: errType}})
}

// WritePayloadTooLarge writes the deterministic bounded 413 body used for
// request body limit enforcement. It never includes rejected body content.
func WritePayloadTooLarge(c *gin.Context) {
	WriteJSONError(c, http.StatusRequestEntityTooLarge, "request_too_large", "request body too large")
}

// ReadRequestBody reads a request body and converts MaxBytesReader limit errors
// into the exact 413 response expected by the server security boundary.
func ReadRequestBody(c *gin.Context) ([]byte, bool) {
	body, err := io.ReadAll(c.Request.Body)
	if err == nil {
		c.Set(TraceRequestBodyKey, body)
		return body, true
	}
	var maxBytesErr *http.MaxBytesError
	if errors.As(err, &maxBytesErr) {
		WritePayloadTooLarge(c)
		return nil, false
	}
	BadRequest(c, "could not read request body")
	return nil, false
}

func (a *API) readUpstreamSuccessBody(resp *http.Response) ([]byte, error) {
	return a.readUpstreamBody(resp, a.Cfg.Upstream.ResponseBodyMaxBytes)
}

func (a *API) readUpstreamErrorBody(resp *http.Response) ([]byte, error) {
	return a.readUpstreamBody(resp, a.Cfg.Upstream.ErrorBodyMaxBytes)
}

// readUpstreamBody reads and closes an upstream response body, enforcing
// limit. The returned error preserves upstream.ErrBodyTooLarge so callers can
// distinguish a size-cap violation from a transport read failure.
func (a *API) readUpstreamBody(resp *http.Response, limit int64) ([]byte, error) {
	body, err := upstream.ReadAllAndCloseLimit(resp.Body, limit)
	if err == nil {
		return body, nil
	}
	if errors.Is(err, upstream.ErrBodyTooLarge) {
		a.Logger.WithField("status", resp.StatusCode).Warn("upstream body exceeded configured cap")
	} else {
		a.Logger.WithError(err).Warn("read upstream body failed")
	}
	return nil, err
}

// upstreamReadFailureMessage converts a body-read error into the client-facing
// message: the size-cap message only when the body actually exceeded the
// configured cap, a generic read-failure message otherwise. kind names the
// body being read ("response" or "error").
func upstreamReadFailureMessage(err error, kind string) string {
	if errors.Is(err, upstream.ErrBodyTooLarge) {
		return "upstream " + kind + " body too large"
	}
	return "failed to read upstream " + kind + " body"
}

// applyUpstreamPayloadOverrides rewrites native provider payloads with the
// configured upstream model and static extra_args. extra_args are applied in
// sorted key order so identical client requests always produce byte-identical
// upstream bodies (map iteration order would otherwise vary per request).
func applyUpstreamPayloadOverrides(body []byte, m *config.Model) []byte {
	out := body
	if strings.TrimSpace(m.UpstreamModel) != "" {
		if next, err := sjson.SetBytes(out, "model", m.UpstreamModel); err == nil {
			out = next
		}
	}
	keys := make([]string, 0, len(m.ExtraArgs))
	for k := range m.ExtraArgs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if next, err := sjson.SetBytes(out, k, m.ExtraArgs[k]); err == nil {
			out = next
		}
	}
	return out
}

func writeSSEHeaders(c *gin.Context) {
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.WriteHeader(http.StatusOK)
}

func (a *API) beginSSE(c *gin.Context) (http.Flusher, bool) {
	writeSSEHeaders(c)
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		a.Logger.Warn("response writer does not support flushing")
		return nil, false
	}
	// Clear the server's absolute WriteTimeout for this response. That deadline
	// covers the whole response and is not reset by keep-alive frames, so a
	// long-running stream would otherwise be truncated mid-flight even while the
	// upstream is healthy. Idle/stall protection is handled by the stream pump's
	// IdleTimeout instead. Best-effort: ignored if the writer can't be unwrapped.
	if err := http.NewResponseController(c.Writer).SetWriteDeadline(time.Time{}); err != nil {
		a.Logger.WithError(err).Debug("could not clear SSE write deadline")
	}
	return flusher, true
}

func safeErrorMessage(msg string) string {
	msg = strings.TrimSpace(logging.Redact(msg))
	if msg == "" {
		return "upstream error"
	}
	if len(msg) > 4096 {
		msg = msg[:4096] + "…"
	}
	return msg
}

// WriteUpstreamStatusError relays a non-2xx upstream response body to the client
// with the same status code.
func WriteUpstreamStatusError(c *gin.Context, status int, body []byte, contentType string) {
	if contentType == "" {
		contentType = "application/json"
	}
	c.Data(status, contentType, body)
}

// BadRequest is a shorthand for the most common error path.
func BadRequest(c *gin.Context, msg string) {
	WriteJSONError(c, http.StatusBadRequest, "invalid_request_error", msg)
}
