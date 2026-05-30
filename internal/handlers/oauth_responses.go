package handlers

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"droid-proxy/internal/config"
	"droid-proxy/internal/oauth"
	"droid-proxy/internal/stream"
)

const (
	codexUserAgent           = "codex_cli_rs/0.118.0 (Mac OS 26.3.1; arm64) droid-proxy"
	codexDefaultInstructions = "You are Codex, a concise coding assistant."
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
	defer resp.Body.Close()
	if m.OAuthProvider == config.OAuthProviderCodex {
		quota, resetAt := oauth.ParseCodexRateLimitHeaders(resp.Header)
		a.recordCodexUsage(token, quota, resetAt)
	}

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
		return
	}

	raw, ok := a.readUpstreamSuccessBody(resp)
	if !ok {
		WriteJSONError(c, http.StatusBadGateway, "upstream_error", "upstream response body too large")
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

func prepareOAuthResponsesPayload(body []byte, m *config.Model, stream bool, downstream http.Header) []byte {
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
	if m.OAuthProvider == config.OAuthProviderCodex {
		out = prepareCodexResponsesPayload(out)
	}
	for _, field := range []string{"previous_response_id", "prompt_cache_retention", "safety_identifier", "stream_options"} {
		if next, err := sjson.DeleteBytes(out, field); err == nil {
			out = next
		}
	}
	if m.OAuthProvider == config.OAuthProviderXAI {
		out = prepareXAIResponsesPayload(out, downstream)
	}
	return out
}

func prepareCodexResponsesPayload(body []byte) []byte {
	out := body
	if strings.TrimSpace(gjson.GetBytes(out, "instructions").String()) == "" {
		if next, err := sjson.SetBytes(out, "instructions", codexDefaultInstructions); err == nil {
			out = next
		}
	}
	if next, err := sjson.SetBytes(out, "store", false); err == nil {
		out = next
	}
	input := gjson.GetBytes(out, "input")
	if input.Type == gjson.String {
		normalized := []map[string]string{{
			"role":    "user",
			"content": input.String(),
		}}
		if next, err := sjson.SetBytes(out, "input", normalized); err == nil {
			out = next
		}
	}
	return out
}

const xaiEncryptedReasoningInclude = "reasoning.encrypted_content"

func prepareXAIResponsesPayload(body []byte, downstream http.Header) []byte {
	var root map[string]any
	if err := json.Unmarshal(body, &root); err != nil {
		return body
	}
	delete(root, "service_tier")
	if strings.TrimSpace(stringValue(root["prompt_cache_key"])) == "" {
		if sessionID := xaiSessionID(downstream, root); sessionID != "" {
			root["prompt_cache_key"] = sessionID
		}
	}
	if tools, ok := root["tools"].([]any); ok {
		root["tools"] = normalizeXAITools(tools)
	}
	if xaiReasoningPresent(root) {
		root["include"] = includeXAIEncryptedReasoning(root["include"])
	}
	out, err := json.Marshal(root)
	if err != nil {
		return body
	}
	return out
}

func xaiSessionID(h http.Header, root map[string]any) string {
	for _, v := range []string{
		h.Get("X-Session-ID"),
		h.Get("Session_id"),
		h.Get("session_id"),
		h.Get("X-Client-Request-Id"),
		stringValue(root["prompt_cache_key"]),
	} {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func normalizeXAITools(tools []any) []any {
	out := make([]any, 0, len(tools))
	for _, rawTool := range tools {
		tool, ok := rawTool.(map[string]any)
		if !ok {
			continue
		}
		toolType := strings.ToLower(strings.TrimSpace(stringValue(tool["type"])))
		if toolType == "namespace" || toolType == "namespace_tool" {
			if nested, ok := tool["tools"].([]any); ok {
				out = append(out, normalizeXAITools(nested)...)
			}
			continue
		}
		normalized, keep := normalizeXAITool(tool)
		if keep {
			out = append(out, normalized)
		}
	}
	return out
}

func normalizeXAITool(tool map[string]any) (map[string]any, bool) {
	toolType := strings.ToLower(strings.TrimSpace(stringValue(tool["type"])))
	name := xaiToolName(tool)
	if unsupportedXAITool(toolType, name) {
		return nil, false
	}
	if strings.HasPrefix(toolType, "web_search") || toolType == "web_search" {
		stripXAIWebSearchFields(tool)
		return sanitizeXAIToolFields(tool), true
	}
	if toolType == "custom" {
		tool["type"] = "function"
		toolType = "function"
		if _, ok := tool["parameters"]; !ok {
			if schema, ok := tool["input_schema"]; ok {
				tool["parameters"] = schema
			}
		}
		delete(tool, "input_schema")
	}
	if toolType == "function" {
		ensureXAIFunctionParameters(tool)
	}
	return sanitizeXAIToolFields(tool), true
}

func xaiToolName(tool map[string]any) string {
	if name := strings.TrimSpace(stringValue(tool["name"])); name != "" {
		return strings.ToLower(name)
	}
	if function, ok := tool["function"].(map[string]any); ok {
		return strings.ToLower(strings.TrimSpace(stringValue(function["name"])))
	}
	return ""
}

func unsupportedXAITool(toolType, name string) bool {
	switch toolType {
	case "tool_search", "image_generation", "apply_patch":
		return true
	}
	switch name {
	case "tool_search", "image_generation", "apply_patch":
		return true
	}
	return strings.HasSuffix(name, ".apply_patch") || strings.HasSuffix(name, "/apply_patch")
}

func stripXAIWebSearchFields(tool map[string]any) {
	for _, field := range []string{
		"allowed_domains",
		"blocked_domains",
		"filters",
		"ranking_options",
		"search_context_size",
		"site_search",
		"user_location",
	} {
		delete(tool, field)
	}
}

func ensureXAIFunctionParameters(tool map[string]any) {
	if function, ok := tool["function"].(map[string]any); ok {
		if _, ok := function["parameters"].(map[string]any); !ok {
			function["parameters"] = map[string]any{}
		}
		function["parameters"] = sanitizeXAIToolSchema(function["parameters"])
		return
	}
	if _, ok := tool["parameters"].(map[string]any); !ok {
		tool["parameters"] = map[string]any{}
	}
	tool["parameters"] = sanitizeXAIToolSchema(tool["parameters"])
}

func sanitizeXAIToolFields(tool map[string]any) map[string]any {
	for key, value := range tool {
		switch key {
		case "parameters", "input_schema":
			tool[key] = sanitizeXAIToolSchema(value)
		case "function":
			if function, ok := value.(map[string]any); ok {
				for fk, fv := range function {
					if fk == "parameters" {
						function[fk] = sanitizeXAIToolSchema(fv)
					}
				}
			}
		}
	}
	return tool
}

func sanitizeXAIToolSchema(value any) any {
	switch v := value.(type) {
	case map[string]any:
		for key, child := range v {
			switch key {
			case "pattern", "format":
				delete(v, key)
			case "enum":
				filtered := sanitizeXAIEnum(child)
				if len(filtered) == 0 {
					delete(v, key)
				} else {
					v[key] = filtered
				}
			default:
				v[key] = sanitizeXAIToolSchema(child)
			}
		}
		return v
	case []any:
		for i, child := range v {
			v[i] = sanitizeXAIToolSchema(child)
		}
		return v
	default:
		return value
	}
}

func sanitizeXAIEnum(value any) []any {
	values, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]any, 0, len(values))
	for _, v := range values {
		if s, ok := v.(string); ok && strings.Contains(s, "/") {
			continue
		}
		out = append(out, v)
	}
	return out
}

func xaiReasoningPresent(root map[string]any) bool {
	if _, ok := root["reasoning"]; ok {
		return true
	}
	return xaiValueContainsReasoning(root["input"])
}

func xaiValueContainsReasoning(value any) bool {
	switch v := value.(type) {
	case map[string]any:
		if strings.EqualFold(stringValue(v["type"]), "reasoning") {
			return true
		}
		if strings.TrimSpace(stringValue(v["encrypted_content"])) != "" {
			return true
		}
		for _, child := range v {
			if xaiValueContainsReasoning(child) {
				return true
			}
		}
	case []any:
		for _, child := range v {
			if xaiValueContainsReasoning(child) {
				return true
			}
		}
	}
	return false
}

func includeXAIEncryptedReasoning(value any) any {
	appendIfMissing := func(values []any) []any {
		for _, v := range values {
			if stringValue(v) == xaiEncryptedReasoningInclude {
				return values
			}
		}
		return append(values, xaiEncryptedReasoningInclude)
	}
	switch v := value.(type) {
	case nil:
		return []string{xaiEncryptedReasoningInclude}
	case []any:
		return appendIfMissing(v)
	case []string:
		values := make([]any, 0, len(v)+1)
		for _, s := range v {
			values = append(values, s)
		}
		return appendIfMissing(values)
	case string:
		values := []any{v}
		return appendIfMissing(values)
	default:
		return []string{xaiEncryptedReasoningInclude}
	}
}

func stringValue(value any) string {
	if s, ok := value.(string); ok {
		return s
	}
	return ""
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

func applyOAuthResponsesHeaders(req *http.Request, downstream http.Header, m *config.Model, token *oauth.Token, payload []byte, installationID, conversationID string) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("Connection", "Keep-Alive")
	req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	switch m.OAuthProvider {
	case config.OAuthProviderCodex:
		req.Header.Set("User-Agent", firstHeader(downstream, "User-Agent", codexUserAgent))
		req.Header.Set("Originator", firstHeader(downstream, "Originator", "codex_cli_rs"))
		req.Header.Set("OpenAI-Beta", firstHeader(downstream, "OpenAI-Beta", "responses_websockets=2026-02-06"))
		req.Header.Set("x-openai-internal-codex-residency", firstHeader(downstream, "x-openai-internal-codex-residency", "us"))
		if token.AccountID != "" {
			req.Header.Set("Chatgpt-Account-Id", token.AccountID)
		}
		if installationID != "" {
			req.Header.Set("x-codex-installation-id", installationID)
		}
		if conversationID == "" {
			conversationID = codexConversationID(downstream, payload)
			if conversationID == "" {
				conversationID = "droid-proxy-" + randomHex(16)
			}
		}
		req.Header.Set("x-client-request-id", firstHeader(downstream, "X-Client-Request-Id", conversationID))
		req.Header.Set("session_id", firstHeader(downstream, "session_id", conversationID))
		req.Header.Set("x-codex-window-id", firstHeader(downstream, "x-codex-window-id", conversationID+":0"))
		for _, name := range []string{"Version", "X-Codex-Beta-Features", "X-Codex-Turn-Metadata", "X-Codex-Turn-State", "X-Codex-Parent-Thread-Id", "X-ResponsesAPI-Include-Timing-Metrics"} {
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

func codexClientMetadata(headers http.Header, installationID, conversationID string) map[string]string {
	metadata := map[string]string{}
	if installationID != "" {
		metadata["x-codex-installation-id"] = installationID
	}
	if conversationID != "" {
		metadata["x-codex-window-id"] = firstHeader(headers, "x-codex-window-id", conversationID+":0")
	}
	for _, name := range []string{"x-codex-turn-metadata", "x-codex-turn-state", "x-codex-parent-thread-id"} {
		if value := strings.TrimSpace(headers.Get(name)); value != "" {
			metadata[name] = value
		}
	}
	return metadata
}

func injectCodexClientMetadata(payload []byte, metadata map[string]string) []byte {
	if len(metadata) == 0 {
		return payload
	}
	out := payload
	for key, value := range metadata {
		if strings.TrimSpace(value) == "" {
			continue
		}
		if existing := gjson.GetBytes(out, "client_metadata."+key); existing.Exists() {
			continue
		}
		if next, err := sjson.SetBytes(out, "client_metadata."+key, value); err == nil {
			out = next
		}
	}
	return out
}

func codexConversationID(h http.Header, payload []byte) string {
	for _, v := range []string{
		h.Get("session_id"),
		h.Get("Session_id"),
		h.Get("X-Codex-Session-Id"),
		h.Get("X-Codex-Conversation-Id"),
		h.Get("X-Client-Request-Id"),
		gjson.GetBytes(payload, "prompt_cache_key").String(),
	} {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func randomHex(n int) string {
	if n <= 0 {
		n = 16
	}
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
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

type responsesSSERepairWriter struct {
	dst    io.Writer
	framer responsesSSERepairFramer
}

func newResponsesSSERepairWriter(dst io.Writer) *responsesSSERepairWriter {
	return &responsesSSERepairWriter{dst: dst, framer: responsesSSERepairFramer{
		outputItemsByIndex: map[int64][]byte{},
	}}
}

func (w *responsesSSERepairWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if err := w.framer.WriteChunk(w.dst, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (w *responsesSSERepairWriter) Flush() error {
	return w.framer.Flush(w.dst)
}

type responsesSSERepairFramer struct {
	pending             []byte
	outputItemsByIndex  map[int64][]byte
	outputItemsFallback [][]byte
}

func (f *responsesSSERepairFramer) WriteChunk(dst io.Writer, chunk []byte) error {
	f.pending = append(f.pending, chunk...)
	for {
		frameEnd, ok := responsesSSEFrameEnd(f.pending)
		if !ok {
			return nil
		}
		frame := append([]byte(nil), f.pending[:frameEnd]...)
		f.pending = f.pending[frameEnd:]
		if err := writeAll(dst, f.repairFrame(frame)); err != nil {
			return err
		}
	}
}

func (f *responsesSSERepairFramer) Flush(dst io.Writer) error {
	if len(f.pending) == 0 {
		return nil
	}
	frame := append([]byte(nil), f.pending...)
	f.pending = nil
	if !bytes.HasSuffix(frame, []byte("\n\n")) && !bytes.HasSuffix(frame, []byte("\r\n\r\n")) {
		frame = append(frame, '\n', '\n')
	}
	return writeAll(dst, f.repairFrame(frame))
}

func (f *responsesSSERepairFramer) repairFrame(frame []byte) []byte {
	data := responsesSSEData(frame)
	if len(data) == 0 || bytes.Equal(bytes.TrimSpace(data), []byte("[DONE]")) {
		return frame
	}
	switch gjson.GetBytes(data, "type").String() {
	case "response.output_item.done":
		collectOAuthOutputItem(data, f.outputItemsByIndex, &f.outputItemsFallback)
	case "response.completed":
		patched := patchOAuthCompletedOutput(data, f.outputItemsByIndex, f.outputItemsFallback)
		if !bytes.Equal(patched, data) {
			return responsesSSEReplaceData(frame, patched)
		}
	}
	return frame
}

func responsesSSEFrameEnd(data []byte) (int, bool) {
	lf := bytes.Index(data, []byte("\n\n"))
	crlf := bytes.Index(data, []byte("\r\n\r\n"))
	switch {
	case lf < 0 && crlf < 0:
		return 0, false
	case lf < 0:
		return crlf + len("\r\n\r\n"), true
	case crlf < 0:
		return lf + len("\n\n"), true
	case lf < crlf:
		return lf + len("\n\n"), true
	default:
		return crlf + len("\r\n\r\n"), true
	}
}

func responsesSSEData(frame []byte) []byte {
	var parts [][]byte
	for _, line := range bytes.Split(frame, []byte("\n")) {
		line = bytes.TrimRight(line, "\r")
		line = bytes.TrimSpace(line)
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		parts = append(parts, bytes.TrimSpace(line[len("data:"):]))
	}
	return bytes.Join(parts, []byte("\n"))
}

func responsesSSEEvent(frame []byte) string {
	for _, line := range bytes.Split(frame, []byte("\n")) {
		line = bytes.TrimRight(line, "\r")
		line = bytes.TrimSpace(line)
		if bytes.HasPrefix(line, []byte("event:")) {
			return strings.TrimSpace(string(line[len("event:"):]))
		}
	}
	return ""
}

func responsesSSEReplaceData(frame, data []byte) []byte {
	var buf bytes.Buffer
	if event := responsesSSEEvent(frame); event != "" {
		buf.WriteString("event: ")
		buf.WriteString(event)
		buf.WriteByte('\n')
	}
	buf.WriteString("data: ")
	buf.Write(data)
	buf.WriteString("\n\n")
	return buf.Bytes()
}

func oauthResponsesTerminal(ev stream.Event) bool {
	if stream.ResponsesTerminal(ev) {
		return true
	}
	switch gjson.Get(ev.Data, "type").String() {
	case "response.completed", "response.failed", "response.incomplete", "error":
		return true
	default:
		return false
	}
}

func writeAll(dst io.Writer, data []byte) error {
	if len(data) == 0 {
		return nil
	}
	n, err := dst.Write(data)
	if err != nil {
		return err
	}
	if n != len(data) {
		return io.ErrShortWrite
	}
	return nil
}

func codexQuotaFromSSEBody(body []byte) *oauth.CodexQuota {
	var out *oauth.CodexQuota
	for _, line := range bytes.Split(body, []byte("\n")) {
		if quota := codexQuotaFromSSELine(line); quota != nil {
			out = quota
		}
	}
	return out
}

func codexQuotaFromSSELine(line []byte) *oauth.CodexQuota {
	line = bytes.TrimSpace(line)
	if !bytes.HasPrefix(line, []byte("data:")) {
		return nil
	}
	data := bytes.TrimSpace(line[len("data:"):])
	if len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) {
		return nil
	}
	return oauth.ParseCodexRateLimitsEvent(data)
}

func (a *API) recordCodexUsage(token *oauth.Token, quota *oauth.CodexQuota, resetAt *time.Time) {
	if a == nil || a.OAuth == nil || token == nil || token.Provider() != config.OAuthProviderCodex {
		return
	}
	if err := a.OAuth.RecordCodexUsage(token, quota, resetAt); err != nil {
		a.Logger.WithError(err).Warn("could not record codex quota metadata")
	}
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
