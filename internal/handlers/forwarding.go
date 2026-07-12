package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"

	"github.com/trevoraspencer/droid-proxy/internal/config"
	"github.com/trevoraspencer/droid-proxy/internal/stream"
	"github.com/trevoraspencer/droid-proxy/internal/upstream"
)

type modelResolveErrors struct {
	Missing  func()
	NotFound func(error)
	Internal func(error)
}

func openAIModelErrors(c *gin.Context) modelResolveErrors {
	return modelResolveErrors{
		Missing: func() {
			BadRequest(c, "request is missing required field: model")
		},
		NotFound: func(err error) {
			WriteJSONError(c, http.StatusNotFound, "model_not_found", err.Error())
		},
		Internal: func(err error) {
			WriteJSONError(c, http.StatusInternalServerError, "internal_error", err.Error())
		},
	}
}

func anthropicModelErrors(c *gin.Context) modelResolveErrors {
	return modelResolveErrors{
		Missing: func() {
			WriteAnthropicError(c, http.StatusBadRequest, "invalid_request_error", "request is missing required field: model")
		},
		NotFound: func(err error) {
			WriteAnthropicError(c, http.StatusNotFound, "not_found_error", err.Error())
		},
		Internal: func(err error) {
			WriteAnthropicError(c, http.StatusInternalServerError, "api_error", err.Error())
		},
	}
}

func (a *API) resolveRequestModel(body []byte, errs modelResolveErrors) (*config.Model, bool) {
	alias := strings.TrimSpace(gjson.GetBytes(body, "model").String())
	if alias == "" {
		errs.Missing()
		return nil, false
	}
	m, err := a.Router.Resolve(alias)
	if err != nil {
		var nf *upstream.NotFoundError
		if errors.As(err, &nf) {
			if hint := a.staleConfigHint(); hint != "" {
				errs.NotFound(fmt.Errorf("%w — %s", nf, hint))
			} else {
				errs.NotFound(nf)
			}
			return nil, false
		}
		errs.Internal(err)
		return nil, false
	}
	return m, true
}

// staleConfigHint reports when the config file changed on disk after this
// server loaded it, so "model not configured" errors can explain themselves.
// It stats the file only on the model-not-found path, never per request.
func (a *API) staleConfigHint() string {
	if a.Cfg == nil || strings.TrimSpace(a.Cfg.SourcePath) == "" {
		return ""
	}
	info, err := os.Stat(a.Cfg.SourcePath)
	if err != nil || !info.ModTime().After(a.Cfg.SourceModTime) {
		return ""
	}
	return "config.yaml changed since the proxy started; restart droid-proxy to apply it"
}

func (a *API) doUpstream(c *gin.Context, opts upstream.SendOptions, writeBuildError, writeDoError func(error)) (*http.Response, bool) {
	req, err := a.Client.Build(c.Request.Context(), opts)
	if err != nil {
		writeBuildError(err)
		return nil, false
	}
	resp, err := a.Client.Do(req)
	if err != nil {
		writeDoError(err)
		return nil, false
	}
	return resp, true
}

// rawUpstreamSuccessBody reads a 2xx upstream body. On failure it calls
// writeReadError with the client-facing message ("too large" only when the
// configured cap was actually exceeded) so the handler can wrap it in its
// protocol-specific error envelope.
func (a *API) rawUpstreamSuccessBody(resp *http.Response, writeReadError func(msg string)) ([]byte, bool) {
	body, err := a.readUpstreamSuccessBody(resp)
	if err != nil {
		writeReadError(upstreamReadFailureMessage(err, "response"))
		return nil, false
	}
	return body, true
}

// rawUpstreamErrorBody is rawUpstreamSuccessBody for non-2xx upstream bodies.
func (a *API) rawUpstreamErrorBody(resp *http.Response, writeReadError func(msg string)) ([]byte, bool) {
	body, err := a.readUpstreamErrorBody(resp)
	if err != nil {
		writeReadError(upstreamReadFailureMessage(err, "error"))
		return nil, false
	}
	return body, true
}

func writeRawUpstreamResponse(c *gin.Context, resp *http.Response, status int, body []byte, defaultContentType string) {
	upstream.CopyHeaders(c.Writer.Header(), resp.Header)
	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = defaultContentType
	}
	c.Data(status, ct, body)
}

func (a *API) forwardRawUpstreamSSE(c *gin.Context, resp *http.Response, opts stream.Options, logMessage string) error {
	upstream.CopyHeaders(c.Writer.Header(), resp.Header)
	flusher, ok := a.beginSSE(c)
	if !ok {
		return nil
	}
	err := stream.Forward(c.Request.Context(), c.Writer, flusher, resp.Body, opts)
	if err != nil && !errors.Is(err, c.Request.Context().Err()) {
		a.Logger.WithError(err).Warn(logMessage)
	}
	return err
}
