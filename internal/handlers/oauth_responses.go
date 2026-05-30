package handlers

import (
	"bytes"
	"errors"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"

	"droid-proxy/internal/config"
	"droid-proxy/internal/oauth"
	"droid-proxy/internal/stream"
)

func (a *API) responsesViaOAuth(c *gin.Context, m *config.Model, body []byte) {
	if a.OAuth == nil {
		WriteJSONError(c, http.StatusInternalServerError, "configuration_error", "oauth manager is not configured")
		return
	}
	token, err := a.OAuth.LoadToken(m.OAuthProvider, m.OAuthAccount)
	if err != nil {
		WriteJSONError(c, http.StatusUnauthorized, "authentication_error", safeErrorMessage(err.Error()))
		return
	}
	token, err = a.OAuth.RefreshIfNeeded(c.Request.Context(), token)
	if err != nil {
		WriteJSONError(c, http.StatusUnauthorized, "authentication_error", safeErrorMessage(err.Error()))
		return
	}

	downstreamStream := gjson.GetBytes(body, "stream").Bool()
	payload := prepareOAuthResponsesPayload(body, m, true, c.Request.Header)
	installationID := ""
	codexConversation := ""
	if m.OAuthProvider == config.OAuthProviderCodex {
		codexConversation = codexConversationID(c.Request.Header, payload)
		if codexConversation == "" {
			codexConversation = "droid-proxy-" + randomHex(16)
		}
		if id, err := a.OAuth.InstallationID(); err == nil {
			installationID = id
			payload = injectCodexClientMetadata(payload, codexClientMetadata(c.Request.Header, installationID, codexConversation))
		} else {
			a.Logger.WithError(err).Warn("could not resolve codex installation id")
		}
	}

	upstreamURL, err := oauthResponsesURL(m, token)
	if err != nil {
		WriteJSONError(c, http.StatusInternalServerError, "configuration_error", err.Error())
		return
	}
	req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, upstreamURL, bytes.NewReader(payload))
	if err != nil {
		WriteJSONError(c, http.StatusInternalServerError, "configuration_error", err.Error())
		return
	}
	applyOAuthResponsesHeaders(req, c.Request.Header, m, token, payload, installationID, codexConversation)

	var resp *http.Response
	if downstreamStream {
		resp, err = a.Client.Do(req)
	} else {
		resp, err = a.Client.HTTP.Do(req)
	}
	if err != nil {
		WriteJSONError(c, http.StatusBadGateway, "upstream_error", safeErrorMessage(err.Error()))
		return
	}
	defer func() { _ = resp.Body.Close() }()

	if m.OAuthProvider == config.OAuthProviderCodex {
		quota, resetAt := oauth.ParseCodexRateLimitHeaders(resp.Header)
		a.recordCodexUsage(token, quota, resetAt)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, ok := a.rawUpstreamErrorBody(resp, func() {
			WriteJSONError(c, http.StatusBadGateway, "upstream_error", "upstream error body too large")
		})
		if !ok {
			return
		}
		if downstreamStream {
			a.writeResponsesStreamError(c, resp.StatusCode, raw)
			return
		}
		WriteUpstreamStatusError(c, resp.StatusCode, raw, resp.Header.Get("Content-Type"))
		return
	}

	if downstreamStream {
		a.forwardOAuthResponsesStream(c, m, resp, token)
		return
	}

	raw, ok := a.rawUpstreamSuccessBody(resp, func() {
		WriteJSONError(c, http.StatusBadGateway, "upstream_error", "upstream response body too large")
	})
	if !ok {
		return
	}
	if m.OAuthProvider == config.OAuthProviderCodex {
		if quota := codexQuotaFromSSEBody(raw); quota != nil {
			a.recordCodexUsage(token, quota, nil)
		}
	}
	translated, err := responseFromResponsesSSE(raw)
	if err != nil {
		WriteJSONError(c, http.StatusBadGateway, "upstream_error", err.Error())
		return
	}
	c.Data(http.StatusOK, "application/json", translated)
}

func (a *API) forwardOAuthResponsesStream(c *gin.Context, m *config.Model, resp *http.Response, token *oauth.Token) {
	flusher, ok := a.beginSSE(c)
	if !ok {
		return
	}
	dst := io.Writer(c.Writer)
	var repair *responsesSSERepairWriter
	if m.OAuthProvider == config.OAuthProviderXAI {
		repair = newResponsesSSERepairWriter(c.Writer)
		dst = repair
	}
	if err := stream.Forward(c.Request.Context(), dst, flusher, resp.Body, stream.Options{
		KeepAlive:   a.Cfg.Upstream.StreamKeepAlive,
		IdleTimeout: a.Cfg.Upstream.HTTPTimeout,
		IsTerminal:  oauthResponsesTerminal,
		OnLine: func(line []byte) {
			if quota := codexQuotaFromSSELine(line); quota != nil {
				a.recordCodexUsage(token, quota, nil)
			}
		},
		WriteTruncationError: a.responsesTruncationWriter(http.StatusBadGateway, "upstream stream ended before terminal marker"),
	}); err != nil && !errors.Is(err, c.Request.Context().Err()) {
		a.Logger.WithError(err).Warn("oauth responses stream terminated abnormally")
	}
	if repair != nil {
		if err := repair.Flush(); err != nil && !errors.Is(err, c.Request.Context().Err()) {
			a.Logger.WithError(err).Warn("could not flush repaired oauth responses stream")
		}
	}
}
