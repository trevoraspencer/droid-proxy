package handlers

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"time"

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

	// Use pool-based failover for Codex when pool is available.
	// xAI and non-pooled Codex fall through to the single-token path.
	if m.OAuthProvider == config.OAuthProviderCodex && a.Pool != nil {
		a.responsesViaCodexFailover(c, m, payload, downstreamStream, installationID, codexConversation)
		return
	}

	// Single-token path (xAI or Codex without pool).
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

// isRetryableCodexStatus returns true for Codex upstream statuses that trigger
// failover to an alternate account: 429 (rate-limited) and 5xx (server error).
// All other 4xx statuses are non-retryable and are relayed directly.
func isRetryableCodexStatus(status int) bool {
	return status == http.StatusTooManyRequests || status >= 500
}

// responsesViaCodexFailover implements bounded account failover for Codex
// requests using the AccountPool. It replays the prepared payload across
// eligible alternate accounts within the configured max_failovers budget.
//
// Retryable statuses (429, 5xx) mark the failed account and try the next.
// Transport errors mark cooldown and try the next.
// Non-retryable 4xx is relayed directly without failover.
// Budget exhaustion relays the last upstream error.
// No eligible accounts produce a deterministic safe error.
func (a *API) responsesViaCodexFailover(c *gin.Context, m *config.Model, payload []byte, downstreamStream bool, installationID, codexConversation string) {
	lb := a.Cfg.OAuth.LoadBalancing
	maxAttempts := 1 + lb.MaxFailovers // initial attempt + additional failovers
	rateLimitCooldown := lb.RateLimitCooldown
	errorCooldown := lb.ErrorCooldown

	tried := map[string]bool{}
	var lastUpstreamStatus int
	var lastUpstreamBody []byte
	var lastUpstreamContentType string
	var hadUpstreamAttempt bool

	for attempt := 0; attempt < maxAttempts; attempt++ {
		// Select an eligible account excluding already-tried paths.
		entry, selErr := a.Pool.Select(m.OAuthAccount, tried)
		if selErr != nil {
			// No eligible accounts left.
			break
		}

		// Acquire lease for in-flight accounting.
		if beginErr := a.Pool.Begin(entry.Path); beginErr != nil {
			tried[entry.Path] = true
			continue
		}

		// Load token from the selected account's path.
		token, tokErr := a.OAuth.LoadTokenAtPath(entry.Path)
		if tokErr != nil {
			a.Pool.End(entry.Path)
			a.Pool.MarkUnhealthy(entry.Path)
			tried[entry.Path] = true
			continue
		}

		// Refresh the token if expired or close to expiry.
		token, refreshErr := a.OAuth.RefreshIfNeeded(c.Request.Context(), token)
		if refreshErr != nil {
			a.Pool.End(entry.Path)
			a.Pool.MarkUnhealthy(entry.Path)
			tried[entry.Path] = true
			continue
		}

		// Build upstream URL from the selected token.
		upstreamURL, urlErr := oauthResponsesURL(m, token)
		if urlErr != nil {
			a.Pool.End(entry.Path)
			WriteJSONError(c, http.StatusInternalServerError, "configuration_error", urlErr.Error())
			return
		}

		// Build the upstream request with replay-safe payload.
		req, reqErr := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, upstreamURL, bytes.NewReader(payload))
		if reqErr != nil {
			a.Pool.End(entry.Path)
			WriteJSONError(c, http.StatusInternalServerError, "configuration_error", reqErr.Error())
			return
		}
		applyOAuthResponsesHeaders(req, c.Request.Header, m, token, payload, installationID, codexConversation)

		// Execute the upstream request.
		var resp *http.Response
		var doErr error
		if downstreamStream {
			resp, doErr = a.Client.Do(req)
		} else {
			resp, doErr = a.Client.HTTP.Do(req)
		}

		if doErr != nil {
			// Transport error: mark cooldown and try next account.
			a.Pool.End(entry.Path)
			a.Pool.MarkCooldown(entry.Path, time.Now().Add(errorCooldown))
			tried[entry.Path] = true
			continue
		}

		// Record Codex quota from response headers (even for error responses).
		quota, resetAt := oauth.ParseCodexRateLimitHeaders(resp.Header)
		a.recordCodexUsage(token, quota, resetAt)

		hadUpstreamAttempt = true
		lastUpstreamStatus = resp.StatusCode

		// Success path.
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			if downstreamStream {
				a.forwardOAuthResponsesStreamAndRelease(c, m, resp, token, entry.Path)
				return
			}

			raw, ok := a.rawUpstreamSuccessBody(resp, func() {
				WriteJSONError(c, http.StatusBadGateway, "upstream_error", "upstream response body too large")
			})
			_ = resp.Body.Close()
			a.Pool.End(entry.Path)
			if !ok {
				return
			}
			if quota := codexQuotaFromSSEBody(raw); quota != nil {
				a.recordCodexUsage(token, quota, nil)
			}
			translated, err := responseFromResponsesSSE(raw)
			if err != nil {
				WriteJSONError(c, http.StatusBadGateway, "upstream_error", err.Error())
				return
			}
			c.Data(http.StatusOK, "application/json", translated)
			return
		}

		// Error response: read body for potential relay.
		raw, ok := a.rawUpstreamErrorBody(resp, func() {})
		ct := resp.Header.Get("Content-Type")
		_ = resp.Body.Close()
		a.Pool.End(entry.Path)

		if ok {
			lastUpstreamBody = raw
			lastUpstreamContentType = ct
		}

		// Classify: retryable or non-retryable.
		if isRetryableCodexStatus(resp.StatusCode) {
			if resp.StatusCode == http.StatusTooManyRequests {
				a.Pool.MarkRateLimited(entry.Path, time.Now().Add(rateLimitCooldown))
			} else {
				a.Pool.MarkCooldown(entry.Path, time.Now().Add(errorCooldown))
			}
			tried[entry.Path] = true
			continue
		}

		// Non-retryable 4xx: relay directly to the client.
		if downstreamStream {
			a.writeResponsesStreamError(c, resp.StatusCode, lastUpstreamBody)
		} else {
			WriteUpstreamStatusError(c, resp.StatusCode, lastUpstreamBody, lastUpstreamContentType)
		}
		return
	}

	// Budget exhausted or no eligible accounts.
	if hadUpstreamAttempt {
		// Relay the last upstream error in the same downstream shape.
		if downstreamStream {
			a.writeResponsesStreamError(c, lastUpstreamStatus, lastUpstreamBody)
		} else {
			WriteUpstreamStatusError(c, lastUpstreamStatus, lastUpstreamBody, lastUpstreamContentType)
		}
		return
	}

	// No upstream attempt was made (all accounts failed during
	// selection/token-load/refresh). Return a deterministic safe error.
	WriteJSONError(c, http.StatusServiceUnavailable, "authentication_error", "no eligible codex accounts available")
}

// forwardOAuthResponsesStreamAndRelease forwards a streaming Codex response
// and releases the pool lease when streaming completes.
func (a *API) forwardOAuthResponsesStreamAndRelease(c *gin.Context, m *config.Model, resp *http.Response, token *oauth.Token, poolPath string) {
	defer func() {
		_ = resp.Body.Close()
		a.Pool.End(poolPath)
	}()
	a.forwardOAuthResponsesStream(c, m, resp, token)
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
