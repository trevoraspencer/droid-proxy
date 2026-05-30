package handlers

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"

	"droid-proxy/internal/config"
	"droid-proxy/internal/stream"
	"droid-proxy/internal/upstream"
)

type modelResolveErrors struct {
	Missing  func()
	NotFound func(error)
	Internal func(error)
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
			errs.NotFound(nf)
			return nil, false
		}
		errs.Internal(err)
		return nil, false
	}
	return m, true
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

func (a *API) rawUpstreamSuccessBody(resp *http.Response, writeTooLarge func()) ([]byte, bool) {
	body, ok := a.readUpstreamSuccessBody(resp)
	if !ok {
		writeTooLarge()
		return nil, false
	}
	return body, true
}

func (a *API) rawUpstreamErrorBody(resp *http.Response, writeTooLarge func()) ([]byte, bool) {
	body, ok := a.readUpstreamErrorBody(resp)
	if !ok {
		writeTooLarge()
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
