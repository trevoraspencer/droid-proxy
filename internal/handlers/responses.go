package handlers

import (
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"

	"github.com/trevoraspencer/droid-proxy/internal/config"
	"github.com/trevoraspencer/droid-proxy/internal/stream"
	"github.com/trevoraspencer/droid-proxy/internal/translate"
	"github.com/trevoraspencer/droid-proxy/internal/upstream"
)

// Responses serves POST /v1/responses — the OpenAI Responses API surface.
// When upstream_protocol == openai-responses, the call is forwarded byte-for-byte.
// When upstream_protocol == openai-chat, non-streaming core requests are
// translated to Chat Completions and back to Responses shape.
func (a *API) Responses(c *gin.Context) {
	body, ok := ReadRequestBody(c)
	if !ok {
		return
	}
	m, ok := a.resolveRequestModel(body, openAIModelErrors(c))
	if !ok {
		return
	}
	if m.FactoryProvider != config.FactoryProviderOpenAI {
		BadRequest(c, "model "+m.Alias+" is configured for factory_provider "+string(m.FactoryProvider)+" and does not accept /v1/responses")
		return
	}
	switch m.UpstreamProtocol {
	case config.UpstreamOpenAIResponses:
		a.responsesNative(c, m, body)
	case config.UpstreamOpenAIChat:
		a.responsesViaChat(c, m, body)
	case config.UpstreamCodexResponses, config.UpstreamXAIResponses:
		a.responsesViaOAuth(c, m, body)
	default:
		WriteJSONError(c, http.StatusNotImplemented, "not_implemented",
			"unsupported upstream_protocol "+string(m.UpstreamProtocol))
	}
}

func (a *API) responsesViaChat(c *gin.Context, m *config.Model, body []byte) {
	// Chat upstreams cannot consume Responses reasoning items, and mixed-model
	// Factory threads replay them onto this path (a chat-backed model never
	// mints its own, so any reasoning item here is foreign by construction).
	// Drop the items and Factory's reasoning include marker before translation
	// instead of failing the request; other include values still validate.
	body = dropEncryptedReasoningInclude(body)
	if stripped, changed := stripReasoningInputItems(body); changed {
		body = stripped
		a.Logger.WithField("model", m.Alias).Info("dropped reasoning input items for chat upstream")
	}
	payload, err := translate.ResponsesToChatRequest(body, m.UpstreamModel, m.ExtraArgs)
	if err != nil {
		WriteJSONError(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	isStream := gjson.GetBytes(payload, "stream").Bool()

	resp, ok := a.doUpstream(c, upstream.SendOptions{
		Model:    m,
		Method:   http.MethodPost,
		Path:     "/chat/completions",
		Body:     payload,
		IsStream: isStream,
	}, func(err error) {
		WriteJSONError(c, http.StatusInternalServerError, "configuration_error", err.Error())
	}, func(err error) {
		WriteJSONError(c, http.StatusBadGateway, "upstream_error", safeErrorMessage(err.Error()))
	})
	if !ok {
		return
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, ok := a.rawUpstreamErrorBody(resp, func(msg string) {
			WriteJSONError(c, http.StatusBadGateway, "upstream_error", msg)
		})
		if !ok {
			return
		}
		if isStream {
			a.writeResponsesStreamError(c, resp.StatusCode, raw)
			return
		}
		WriteJSONError(c, resp.StatusCode, translate.ResponsesErrorCode(resp.StatusCode), translate.ExtractErrorMessage(raw, http.StatusText(resp.StatusCode)))
		return
	}

	if isStream {
		flusher, ok := a.beginSSE(c)
		if !ok {
			return
		}
		if err := translate.ForwardChatStreamToResponsesWithOptions(resp.Body, c.Writer, flusher.Flush, m.UpstreamModel, translate.ChatStreamForwardOptions{
			Context:     c.Request.Context(),
			KeepAlive:   a.Cfg.Upstream.StreamKeepAlive,
			IdleTimeout: a.Cfg.Upstream.HTTPTimeout,
		}); err != nil && !errors.Is(err, c.Request.Context().Err()) {
			_ = a.writeResponsesStreamErrorFrame(c, http.StatusBadGateway, []byte(err.Error()))
			a.Logger.WithError(err).Warn("translated responses stream terminated abnormally")
		}
		return
	}

	raw, ok := a.rawUpstreamSuccessBody(resp, func(msg string) {
		WriteJSONError(c, http.StatusBadGateway, "upstream_error", msg)
	})
	if !ok {
		return
	}
	translated, err := translate.ChatToResponsesResponse(raw, m.UpstreamModel)
	if err != nil {
		WriteJSONError(c, http.StatusBadGateway, "upstream_error", err.Error())
		return
	}
	c.Data(http.StatusOK, "application/json", translated)
}

func (a *API) responsesNative(c *gin.Context, m *config.Model, body []byte) {
	payload := applyUpstreamPayloadOverrides(body, m)
	isStream := gjson.GetBytes(payload, "stream").Bool()

	// One replay is allowed when the upstream rejects encrypted reasoning it
	// cannot decrypt (foreign items from a mixed-model Factory thread); the
	// retry resends the same request with reasoning input items stripped.
	var resp *http.Response
	strippedReasoning := false
	for {
		r, ok := a.doUpstream(c, upstream.SendOptions{
			Model:    m,
			Method:   http.MethodPost,
			Path:     "/responses",
			Body:     payload,
			IsStream: isStream,
		}, func(err error) {
			WriteJSONError(c, http.StatusInternalServerError, "configuration_error", err.Error())
		}, func(err error) {
			WriteJSONError(c, http.StatusBadGateway, "upstream_error", safeErrorMessage(err.Error()))
		})
		if !ok {
			return
		}

		if r.StatusCode >= 200 && r.StatusCode < 300 {
			resp = r
			break
		}

		raw, ok := a.rawUpstreamErrorBody(r, func(msg string) {
			WriteJSONError(c, http.StatusBadGateway, "upstream_error", msg)
		})
		_ = r.Body.Close()
		if !ok {
			return
		}
		if !strippedReasoning && reasoningStripRetryEligible(r.StatusCode, raw) {
			if stripped, changed := stripReasoningInputItems(payload); changed {
				strippedReasoning = true
				payload = stripped
				a.Logger.WithFields(map[string]any{
					"model":           m.Alias,
					"upstream_status": r.StatusCode,
				}).Warn("upstream rejected encrypted reasoning items; retrying once without them")
				continue
			}
		}
		if isStream {
			// stream callers don't get a structured non-stream error from the upstream;
			// the upstream returned 4xx/5xx BEFORE emitting any SSE.  Send a single SSE
			// error event so the client's parser doesn't trip.
			a.writeResponsesStreamError(c, r.StatusCode, raw)
			return
		}
		WriteUpstreamStatusError(c, r.StatusCode, raw, r.Header.Get("Content-Type"))
		return
	}
	defer func() { _ = resp.Body.Close() }()

	if !isStream {
		raw, ok := a.rawUpstreamSuccessBody(resp, func(msg string) {
			WriteJSONError(c, http.StatusBadGateway, "upstream_error", msg)
		})
		if !ok {
			return
		}
		writeRawUpstreamResponse(c, resp, http.StatusOK, raw, "application/json")
		return
	}

	_ = a.forwardRawUpstreamSSE(c, resp, stream.Options{
		KeepAlive:            a.Cfg.Upstream.StreamKeepAlive,
		IdleTimeout:          a.Cfg.Upstream.HTTPTimeout,
		IsTerminal:           stream.ResponsesTerminal,
		WriteTruncationError: a.responsesTruncationWriter(http.StatusBadGateway, "upstream stream ended before terminal marker"),
	}, "responses stream terminated abnormally")
}

// writeResponsesStreamError emits an SSE error chunk in the OpenAI Responses
// streaming shape. Used when upstream returns a non-2xx status BEFORE any SSE
// has been sent. Logged at warn level here — the SSE headers commit an HTTP
// 200, so without this line a relayed upstream failure is indistinguishable
// from a success in the access log.
func (a *API) writeResponsesStreamError(c *gin.Context, status int, body []byte) {
	a.Logger.WithFields(map[string]any{
		"upstream_status": status,
		"request_id":      c.GetString("request_id"),
	}).Warn("relaying upstream error to streaming client")
	writeSSEHeaders(c)
	_ = a.writeResponsesStreamErrorFrame(c, status, body)
}

func (a *API) writeResponsesStreamErrorFrame(c *gin.Context, status int, body []byte) error {
	chunk := translate.BuildResponsesStreamErrorChunk(status, string(body), 0)
	if _, err := fmt.Fprintf(c.Writer, "event: error\ndata: %s\n\n", chunk); err != nil {
		a.Logger.WithError(err).Warn("write responses stream error chunk")
		return err
	}
	if f, ok := c.Writer.(http.Flusher); ok {
		f.Flush()
	}
	return nil
}

func (a *API) responsesTruncationWriter(status int, msg string) func(io.Writer) error {
	return func(w io.Writer) error {
		chunk := translate.BuildResponsesStreamErrorChunk(status, msg, 0)
		_, err := fmt.Fprintf(w, "event: error\ndata: %s\n\n", chunk)
		return err
	}
}
