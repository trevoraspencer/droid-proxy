package handlers

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"droid-proxy/internal/config"
	"droid-proxy/internal/oauth"
	"droid-proxy/internal/stream"
)

const codexUserAgent = "codex_cli_rs/0.118.0 (Mac OS 26.3.1; arm64) droid-proxy"

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
	payload := prepareOAuthResponsesPayload(body, m, true)
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
	applyOAuthResponsesHeaders(req, c.Request.Header, m, token, payload)

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
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, ok := a.readUpstreamErrorBody(resp)
		if !ok {
			WriteJSONError(c, http.StatusBadGateway, "upstream_error", "upstream error body too large")
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
		c.Writer.Header().Set("Content-Type", "text/event-stream")
		c.Writer.Header().Set("Cache-Control", "no-cache")
		c.Writer.Header().Set("Connection", "keep-alive")
		c.Writer.WriteHeader(http.StatusOK)
		flusher, ok := c.Writer.(http.Flusher)
		if !ok {
			a.Logger.Warn("response writer does not support flushing")
			return
		}
		if err := stream.Forward(c.Request.Context(), c.Writer, flusher, resp.Body, stream.Options{
			KeepAlive:            a.Cfg.Upstream.StreamKeepAlive,
			IdleTimeout:          a.Cfg.Upstream.HTTPTimeout,
			IsTerminal:           stream.ResponsesTerminal,
			WriteTruncationError: a.responsesTruncationWriter(http.StatusBadGateway, "upstream stream ended before terminal marker"),
		}); err != nil && !errors.Is(err, c.Request.Context().Err()) {
			a.Logger.WithError(err).Warn("oauth responses stream terminated abnormally")
		}
		return
	}

	raw, ok := a.readUpstreamSuccessBody(resp)
	if !ok {
		WriteJSONError(c, http.StatusBadGateway, "upstream_error", "upstream response body too large")
		return
	}
	translated, err := responseFromResponsesSSE(raw)
	if err != nil {
		WriteJSONError(c, http.StatusBadGateway, "upstream_error", err.Error())
		return
	}
	c.Data(http.StatusOK, "application/json", translated)
}

func prepareOAuthResponsesPayload(body []byte, m *config.Model, stream bool) []byte {
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
	if next, err := sjson.SetBytes(out, "stream", stream); err == nil {
		out = next
	}
	for _, field := range []string{"previous_response_id", "prompt_cache_retention", "safety_identifier", "stream_options"} {
		if next, err := sjson.DeleteBytes(out, field); err == nil {
			out = next
		}
	}
	return out
}

func oauthResponsesURL(m *config.Model, token *oauth.Token) (string, error) {
	baseURL := strings.TrimSpace(m.BaseURL)
	if baseURL == "" {
		baseURL = token.BaseURLForProvider(m.OAuthProvider)
	}
	if baseURL == "" {
		return "", fmt.Errorf("missing OAuth upstream base URL")
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("invalid OAuth upstream base URL: %w", err)
	}
	u.RawQuery = ""
	u.Fragment = ""
	u.User = nil
	u.Path = path.Join(u.Path, "responses")
	if !strings.HasPrefix(u.Path, "/") {
		u.Path = "/" + u.Path
	}
	return u.String(), nil
}

func applyOAuthResponsesHeaders(req *http.Request, downstream http.Header, m *config.Model, token *oauth.Token, payload []byte) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("Connection", "Keep-Alive")
	req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	switch m.OAuthProvider {
	case config.OAuthProviderCodex:
		req.Header.Set("User-Agent", firstHeader(downstream, "User-Agent", codexUserAgent))
		req.Header.Set("Originator", firstHeader(downstream, "Originator", "codex_cli_rs"))
		if token.AccountID != "" {
			req.Header.Set("Chatgpt-Account-Id", token.AccountID)
		}
		for _, name := range []string{"Version", "X-Codex-Beta-Features", "X-Codex-Turn-Metadata", "X-Client-Request-Id", "Session_id"} {
			if v := strings.TrimSpace(downstream.Get(name)); v != "" {
				req.Header.Set(name, v)
			}
		}
	case config.OAuthProviderXAI:
		if sessionID := oauthSessionID(downstream, payload); sessionID != "" {
			req.Header.Set("x-grok-conv-id", sessionID)
		}
	}
}

func firstHeader(h http.Header, name, fallback string) string {
	if v := strings.TrimSpace(h.Get(name)); v != "" {
		return v
	}
	return fallback
}

func oauthSessionID(h http.Header, payload []byte) string {
	for _, v := range []string{
		h.Get("X-Session-ID"),
		h.Get("Session_id"),
		h.Get("X-Client-Request-Id"),
		gjson.GetBytes(payload, "prompt_cache_key").String(),
	} {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func responseFromResponsesSSE(body []byte) ([]byte, error) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("OAuth upstream returned an empty response")
	}
	if trimmed[0] == '{' {
		if response := gjson.GetBytes(trimmed, "response"); response.Exists() && response.Type == gjson.JSON {
			return []byte(response.Raw), nil
		}
		return trimmed, nil
	}

	outputItemsByIndex := map[int64][]byte{}
	var outputItemsFallback [][]byte
	for _, line := range bytes.Split(body, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		eventData := bytes.TrimSpace(line[len("data:"):])
		if bytes.Equal(eventData, []byte("[DONE]")) {
			continue
		}
		switch gjson.GetBytes(eventData, "type").String() {
		case "response.output_item.done":
			collectOAuthOutputItem(eventData, outputItemsByIndex, &outputItemsFallback)
		case "response.completed":
			completed := patchOAuthCompletedOutput(eventData, outputItemsByIndex, outputItemsFallback)
			response := gjson.GetBytes(completed, "response")
			if !response.Exists() || response.Type != gjson.JSON {
				return nil, fmt.Errorf("OAuth upstream response.completed is missing response")
			}
			return []byte(response.Raw), nil
		case "response.failed", "error":
			return nil, fmt.Errorf("OAuth upstream returned error: %s", gjson.GetBytes(eventData, "error.message").String())
		}
	}
	return nil, fmt.Errorf("OAuth upstream stream ended before response.completed")
}

func collectOAuthOutputItem(eventData []byte, outputItemsByIndex map[int64][]byte, outputItemsFallback *[][]byte) {
	item := gjson.GetBytes(eventData, "item")
	if !item.Exists() || item.Type != gjson.JSON {
		return
	}
	if outputIndex := gjson.GetBytes(eventData, "output_index"); outputIndex.Exists() {
		outputItemsByIndex[outputIndex.Int()] = []byte(item.Raw)
		return
	}
	*outputItemsFallback = append(*outputItemsFallback, []byte(item.Raw))
}

func patchOAuthCompletedOutput(eventData []byte, outputItemsByIndex map[int64][]byte, outputItemsFallback [][]byte) []byte {
	output := gjson.GetBytes(eventData, "response.output")
	shouldPatch := (!output.Exists() || !output.IsArray() || len(output.Array()) == 0) && (len(outputItemsByIndex) > 0 || len(outputItemsFallback) > 0)
	if !shouldPatch {
		return eventData
	}
	indexes := make([]int64, 0, len(outputItemsByIndex))
	for idx := range outputItemsByIndex {
		indexes = append(indexes, idx)
	}
	sort.Slice(indexes, func(i, j int) bool { return indexes[i] < indexes[j] })
	var buf bytes.Buffer
	buf.WriteByte('[')
	wrote := false
	for _, idx := range indexes {
		if wrote {
			buf.WriteByte(',')
		}
		buf.Write(outputItemsByIndex[idx])
		wrote = true
	}
	for _, item := range outputItemsFallback {
		if wrote {
			buf.WriteByte(',')
		}
		buf.Write(item)
		wrote = true
	}
	buf.WriteByte(']')
	patched, err := sjson.SetRawBytes(eventData, "response.output", buf.Bytes())
	if err != nil {
		return eventData
	}
	return patched
}
