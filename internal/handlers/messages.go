package handlers

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"

	"github.com/trevoraspencer/droid-proxy/internal/config"
	"github.com/trevoraspencer/droid-proxy/internal/stream"
	"github.com/trevoraspencer/droid-proxy/internal/translate"
	"github.com/trevoraspencer/droid-proxy/internal/upstream"
)

// Messages serves POST /v1/messages — the Anthropic Messages API surface.
// When upstream_protocol == anthropic-messages, the call is forwarded byte-for-byte
// to {base_url}/v1/messages. When upstream_protocol == openai-chat,
// non-streaming core requests are translated to Chat Completions and back to
// Anthropic shape.
func (a *API) Messages(c *gin.Context) {
	body, ok := ReadRequestBody(c)
	if !ok {
		return
	}
	m, ok := a.resolveRequestModel(body, anthropicModelErrors(c))
	if !ok {
		return
	}
	if m.FactoryProvider != config.FactoryProviderAnthropic {
		WriteAnthropicError(c, http.StatusBadRequest, "invalid_request_error", "model "+m.Alias+" is configured for factory_provider "+string(m.FactoryProvider)+" and does not accept /v1/messages")
		return
	}
	switch m.UpstreamProtocol {
	case config.UpstreamAnthropicMessages:
		a.messagesNative(c, m, body, "/v1/messages")
	case config.UpstreamOpenAIChat:
		a.messagesViaChat(c, m, body)
	default:
		WriteAnthropicError(c, http.StatusNotImplemented, "not_implemented",
			"unsupported upstream_protocol "+string(m.UpstreamProtocol))
	}
}

func (a *API) messagesViaChat(c *gin.Context, m *config.Model, body []byte) {
	payload, err := translate.AnthropicToChatRequest(body, m.UpstreamModel, m.ExtraArgs)
	if err != nil {
		WriteAnthropicError(c, http.StatusBadRequest, "invalid_request_error", err.Error())
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
		WriteAnthropicError(c, http.StatusInternalServerError, "api_error", err.Error())
	}, func(err error) {
		WriteAnthropicError(c, http.StatusBadGateway, "api_error", safeErrorMessage(err.Error()))
	})
	if !ok {
		return
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, ok := a.rawUpstreamErrorBody(resp, func(msg string) {
			WriteAnthropicError(c, http.StatusBadGateway, "api_error", msg)
		})
		if !ok {
			return
		}
		if isStream {
			a.writeAnthropicStreamError(c, raw)
			return
		}
		WriteAnthropicError(c, resp.StatusCode, translate.ResponsesErrorCode(resp.StatusCode), translate.ExtractErrorMessage(raw, http.StatusText(resp.StatusCode)))
		return
	}

	if isStream {
		flusher, ok := a.beginSSE(c)
		if !ok {
			return
		}
		if err := translate.ForwardChatStreamToAnthropicWithOptions(resp.Body, c.Writer, flusher.Flush, m.UpstreamModel, translate.ChatStreamForwardOptions{
			Context:     c.Request.Context(),
			KeepAlive:   a.Cfg.Upstream.StreamKeepAlive,
			IdleTimeout: a.Cfg.Upstream.HTTPTimeout,
		}); err != nil && !errors.Is(err, c.Request.Context().Err()) {
			_ = a.writeAnthropicStreamErrorFrame(c.Writer, []byte(err.Error()))
			a.Logger.WithError(err).Warn("translated anthropic stream terminated abnormally")
		}
		return
	}

	raw, ok := a.rawUpstreamSuccessBody(resp, func(msg string) {
		WriteAnthropicError(c, http.StatusBadGateway, "api_error", msg)
	})
	if !ok {
		return
	}
	translated, err := translate.ChatToAnthropicResponse(raw, m.UpstreamModel)
	if err != nil {
		WriteAnthropicError(c, http.StatusBadGateway, "api_error", err.Error())
		return
	}
	c.Data(http.StatusOK, "application/json", translated)
}

func WriteAnthropicError(c *gin.Context, status int, typ, msg string) {
	if strings.TrimSpace(typ) == "" {
		typ = "api_error"
	}
	if strings.TrimSpace(msg) == "" {
		msg = http.StatusText(status)
	}
	c.JSON(status, map[string]any{
		"type": "error",
		"error": map[string]any{
			"type":    typ,
			"message": msg,
		},
	})
}

func (a *API) writeAnthropicStreamError(c *gin.Context, body []byte) {
	writeSSEHeaders(c)
	_ = a.writeAnthropicStreamErrorFrame(c.Writer, body)
	if f, ok := c.Writer.(http.Flusher); ok {
		f.Flush()
	}
}

func (a *API) writeAnthropicStreamErrorFrame(w io.Writer, body []byte) error {
	msg := translate.ExtractErrorMessage(body, "upstream error")
	payload, _ := json.Marshal(map[string]any{"type": "error", "error": map[string]any{"type": "api_error", "message": msg}})
	if _, err := w.Write([]byte("event: error\ndata: ")); err != nil {
		return err
	}
	if _, err := w.Write(payload); err != nil {
		return err
	}
	_, err := w.Write([]byte("\n\n"))
	return err
}

// messagesNative forwards an Anthropic Messages request to an Anthropic-protocol upstream.
func (a *API) messagesNative(c *gin.Context, m *config.Model, body []byte, path string) {
	payload := applyUpstreamPayloadOverrides(body, m)
	isStream := gjson.GetBytes(payload, "stream").Bool()

	// Forward Anthropic-specific headers from the client when set.
	clientHeaders := map[string]string{}
	for _, name := range []string{"anthropic-version", "anthropic-beta"} {
		if v := strings.TrimSpace(c.GetHeader(name)); v != "" {
			clientHeaders[name] = v
		}
	}

	resp, ok := a.doUpstream(c, upstream.SendOptions{
		Model:        m,
		Method:       http.MethodPost,
		Path:         path,
		Body:         payload,
		IsStream:     isStream,
		ExtraHeaders: clientHeaders,
	}, func(err error) {
		WriteAnthropicError(c, http.StatusInternalServerError, "api_error", err.Error())
	}, func(err error) {
		WriteAnthropicError(c, http.StatusBadGateway, "api_error", safeErrorMessage(err.Error()))
	})
	if !ok {
		return
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, ok := a.rawUpstreamErrorBody(resp, func(msg string) {
			WriteAnthropicError(c, http.StatusBadGateway, "api_error", msg)
		})
		if !ok {
			return
		}
		WriteUpstreamStatusError(c, resp.StatusCode, raw, resp.Header.Get("Content-Type"))
		return
	}

	if !isStream {
		raw, ok := a.rawUpstreamSuccessBody(resp, func(msg string) {
			WriteAnthropicError(c, http.StatusBadGateway, "api_error", msg)
		})
		if !ok {
			return
		}
		// Anthropic sometimes returns gzipped JSON without Content-Encoding set
		// (their internal LB strips the header). Detect via magic bytes and decompress.
		if maybeGzipped(raw) {
			if decoded, err := gunzip(raw, a.Cfg.Upstream.ResponseBodyMaxBytes); err == nil {
				raw = decoded
			} else {
				WriteAnthropicError(c, http.StatusBadGateway, "api_error", "upstream gzip body too large or invalid")
				return
			}
		}
		writeRawUpstreamResponse(c, resp, http.StatusOK, raw, "application/json")
		return
	}

	_ = a.forwardRawUpstreamSSE(c, resp, stream.Options{
		KeepAlive:   a.Cfg.Upstream.StreamKeepAlive,
		IdleTimeout: a.Cfg.Upstream.HTTPTimeout,
		IsTerminal:  stream.AnthropicTerminal,
		WriteTruncationError: func(w io.Writer) error {
			return a.writeAnthropicStreamErrorFrame(w, []byte("upstream stream ended before terminal marker"))
		},
	}, "anthropic stream terminated abnormally")
}

func maybeGzipped(b []byte) bool {
	return len(b) >= 2 && b[0] == 0x1f && b[1] == 0x8b
}

func gunzip(b []byte, limit int64) ([]byte, error) {
	r, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	defer func() { _ = r.Close() }()
	if limit <= 0 {
		return io.ReadAll(r)
	}
	lr := &io.LimitedReader{R: r, N: limit + 1}
	out, err := io.ReadAll(lr)
	if err != nil {
		return nil, err
	}
	if int64(len(out)) > limit {
		return nil, upstream.ErrBodyTooLarge
	}
	return out, nil
}
