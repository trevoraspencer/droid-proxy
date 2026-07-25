package handlers

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"

	"github.com/trevoraspencer/droid-proxy/internal/config"
	"github.com/trevoraspencer/droid-proxy/internal/reasoning"
	"github.com/trevoraspencer/droid-proxy/internal/stream"
	"github.com/trevoraspencer/droid-proxy/internal/upstream"
)

// ChatCompletions serves POST /v1/chat/completions. It supports the Factory
// provider modes `generic-chat-completion-api` and `openai` when the upstream
// speaks OpenAI Chat Completions natively.
func (a *API) ChatCompletions(c *gin.Context) {
	body, ok := ReadRequestBody(c)
	if !ok {
		return
	}

	m, ok := a.resolveRequestModel(body, openAIModelErrors(c))
	if !ok {
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

	payload := applyUpstreamPayloadOverrides(body, m)
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

	resp, ok := a.doUpstream(c, upstream.SendOptions{
		Model:    m,
		Method:   http.MethodPost,
		Path:     "/chat/completions",
		Body:     payload,
		IsStream: isStream,
	}, func(err error) {
		WriteJSONError(c, http.StatusInternalServerError, "configuration_error", err.Error())
	}, func(err error) {
		WriteJSONError(c, http.StatusBadGateway, "upstream_error", err.Error())
	})
	if !ok {
		return
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, ok := a.rawUpstreamErrorBody(resp, func(msg string) {
			WriteJSONError(c, http.StatusBadGateway, "upstream_error", msg)
		})
		if !ok {
			return
		}
		WriteUpstreamStatusError(c, resp.StatusCode, body, resp.Header.Get("Content-Type"))
		return
	}

	if !isStream {
		respBody, ok := a.rawUpstreamSuccessBody(resp, func(msg string) {
			WriteJSONError(c, http.StatusBadGateway, "upstream_error", msg)
		})
		if !ok {
			return
		}
		if useReasoning {
			reasoning.CaptureNonStream(respBody, reasoningScope, a.ReasoningCache)
		}
		writeRawUpstreamResponse(c, resp, http.StatusOK, respBody, "application/json")
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
	streamErr := a.forwardRawUpstreamSSE(c, resp, opts, "stream forward terminated abnormally")
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
