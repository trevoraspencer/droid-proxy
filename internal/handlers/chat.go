package handlers

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"droid-proxy/internal/config"
	"droid-proxy/internal/reasoning"
	"droid-proxy/internal/stream"
	"droid-proxy/internal/upstream"
)

// ChatCompletions serves POST /v1/chat/completions. It supports the Factory
// provider modes `generic-chat-completion-api` and `openai` when the upstream
// speaks OpenAI Chat Completions natively.
func (a *API) ChatCompletions(c *gin.Context) {
	body, ok := ReadRequestBody(c)
	if !ok {
		return
	}

	alias := strings.TrimSpace(gjson.GetBytes(body, "model").String())
	if alias == "" {
		BadRequest(c, "request is missing required field: model")
		return
	}

	m, err := a.Router.Resolve(alias)
	if err != nil {
		var nf *upstream.NotFoundError
		if errors.As(err, &nf) {
			WriteJSONError(c, http.StatusNotFound, "model_not_found", nf.Error())
			return
		}
		WriteJSONError(c, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}

	if m.FactoryProvider != config.FactoryProviderGeneric && m.FactoryProvider != config.FactoryProviderOpenAI {
		BadRequest(c, "model "+m.Alias+" is configured for factory_provider "+string(m.FactoryProvider)+" and does not accept /v1/chat/completions")
		return
	}

	if m.UpstreamProtocol != config.UpstreamOpenAIChat {
		// Anthropic-via-Chat-translation lives in a future phase; chat-on-Responses
		// is also a future translator. Surface honestly for now.
		WriteJSONError(c, http.StatusNotImplemented, "not_implemented",
			"chat/completions on upstream_protocol "+string(m.UpstreamProtocol)+" is not yet supported")
		return
	}

	payload := prepareChatPayload(body, m)
	isStream := gjson.GetBytes(payload, "stream").Bool()

	// Optional: DeepSeek reasoning replay. When enabled and we have a cache,
	// patch the outgoing payload with stored reasoning_content and install a
	// stream capture so we keep the cache populated.
	var reasoningScope reasoning.Scope
	useReasoning := a.ReasoningCache != nil && m.Capabilities.Reasoning == config.ReasoningDeepSeek
	if useReasoning {
		reasoningScope = a.buildReasoningScope(c.Request.Header, m, payload)
		payload = reasoning.PatchRequest(payload, reasoningScope, a.ReasoningCache)
	}

	req, err := a.Client.Build(c.Request.Context(), upstream.SendOptions{
		Model:    m,
		Method:   http.MethodPost,
		Path:     "/chat/completions",
		Body:     payload,
		IsStream: isStream,
	})
	if err != nil {
		WriteJSONError(c, http.StatusInternalServerError, "configuration_error", err.Error())
		return
	}

	resp, err := a.Client.Do(req)
	if err != nil {
		WriteJSONError(c, http.StatusBadGateway, "upstream_error", err.Error())
		return
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, ok := a.readUpstreamErrorBody(resp)
		if !ok {
			WriteJSONError(c, http.StatusBadGateway, "upstream_error", "upstream error body too large")
			return
		}
		WriteUpstreamStatusError(c, resp.StatusCode, body, resp.Header.Get("Content-Type"))
		return
	}

	if !isStream {
		respBody, ok := a.readUpstreamSuccessBody(resp)
		if !ok {
			WriteJSONError(c, http.StatusBadGateway, "upstream_error", "upstream response body too large")
			return
		}
		if useReasoning {
			reasoning.CaptureNonStream(respBody, reasoningScope, a.ReasoningCache)
		}
		upstream.CopyHeaders(c.Writer.Header(), resp.Header)
		ct := resp.Header.Get("Content-Type")
		if ct == "" {
			ct = "application/json"
		}
		c.Data(http.StatusOK, ct, respBody)
		return
	}

	// streaming
	upstream.CopyHeaders(c.Writer.Header(), resp.Header)
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.WriteHeader(http.StatusOK)

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		// can't recover meaningfully — gin always supports Flusher in practice
		a.Logger.Warn("response writer does not support flushing")
		return
	}

	var capture *reasoning.StreamCapture
	if useReasoning {
		capture = reasoning.NewStreamCapture(reasoningScope, a.ReasoningCache)
	}
	opts := stream.Options{
		KeepAlive:            a.Cfg.Upstream.StreamKeepAlive,
		IdleTimeout:          a.Cfg.Upstream.HTTPTimeout,
		IsTerminal:           stream.ChatTerminal,
		WriteTruncationError: chatTruncationWriter,
	}
	if capture != nil {
		opts.OnLine = func(b []byte) { capture.ObserveLine(b) }
	}
	streamErr := stream.Forward(c.Request.Context(), c.Writer, flusher, resp.Body, opts)
	if streamErr != nil && !errors.Is(streamErr, c.Request.Context().Err()) {
		a.Logger.WithError(streamErr).Warn("stream forward terminated abnormally")
	}
	if capture != nil && streamErr == nil {
		capture.Commit()
	}
}

func chatTruncationWriter(w io.Writer) error {
	_, err := fmt.Fprint(w, "data: {\"error\":{\"type\":\"upstream_error\",\"code\":\"stream_truncated\",\"message\":\"upstream stream ended before terminal marker\"}}\n\n")
	return err
}

// buildReasoningScope assembles a Scope for the reasoning cache.
func (a *API) buildReasoningScope(headers http.Header, m *config.Model, payload []byte) reasoning.Scope {
	authKey, _ := upstream.ResolveAPIKey(m)
	authHash := reasoning.APIKeyHash(authKey)
	if clientHash := a.clientAuthHash(headers); clientHash != "" {
		authHash += ":" + clientHash
	}
	thinking := ""
	if t := gjson.GetBytes(payload, "thinking").Raw; t != "" {
		thinking = t
	}
	provider := strings.ToLower(strings.TrimSpace(m.KnownAuth))
	if provider == "" {
		provider = strings.ToLower(strings.TrimSpace(m.Alias))
	}
	upstreamModel := m.UpstreamModel
	if strings.TrimSpace(upstreamModel) == "" {
		upstreamModel = m.Alias
	}
	return reasoning.Scope{
		Provider:     provider,
		AuthHash:     authHash,
		Model:        upstreamModel,
		BaseURL:      m.BaseURL,
		Session:      reasoning.SessionID(headers, payload),
		ThinkingMode: thinking,
	}
}

func (a *API) clientAuthHash(headers http.Header) string {
	if a == nil || a.Cfg == nil || !a.Cfg.ClientAuth.Enabled {
		return ""
	}
	header := a.Cfg.ClientAuth.Header
	if strings.TrimSpace(header) == "" {
		header = "Authorization"
	}
	raw := strings.TrimSpace(headers.Get(header))
	if raw == "" {
		return ""
	}
	scheme := strings.TrimSpace(a.Cfg.ClientAuth.Scheme)
	credential := raw
	if scheme != "" {
		prefix := scheme + " "
		if !strings.HasPrefix(raw, prefix) {
			return ""
		}
		credential = strings.TrimSpace(strings.TrimPrefix(raw, prefix))
	}
	if credential == "" {
		return ""
	}
	return reasoning.APIKeyHash(credential)
}

// prepareChatPayload rewrites the inbound payload for the upstream:
//   - sets `model` to m.UpstreamModel when non-empty (otherwise the client's
//     value is left intact, which lets users target an alias that already matches
//     the upstream's model id).
//   - applies top-level extra_args (sjson SetBytes for each key).
func prepareChatPayload(body []byte, m *config.Model) []byte {
	out := body
	if strings.TrimSpace(m.UpstreamModel) != "" {
		if next, err := sjson.SetBytes(out, "model", m.UpstreamModel); err == nil {
			out = next
		}
	}
	for k, v := range m.ExtraArgs {
		if next, err := sjson.SetBytes(out, k, v); err == nil {
			out = next
		}
	}
	return out
}
