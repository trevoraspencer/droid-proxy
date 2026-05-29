package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"

	"droid-proxy/internal/config"
	"droid-proxy/internal/tokens"
	"droid-proxy/internal/upstream"
)

// CountTokens serves POST /v1/messages/count_tokens. With an Anthropic upstream,
// it forwards to upstream's count_tokens endpoint. Otherwise it counts locally
// with tiktoken (cl100k_base) and returns Anthropic-shaped JSON.
func (a *API) CountTokens(c *gin.Context) {
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
	if m.FactoryProvider != config.FactoryProviderAnthropic {
		BadRequest(c, "model "+m.Alias+" is configured for factory_provider "+string(m.FactoryProvider)+" and does not accept /v1/messages/count_tokens")
		return
	}
	if m.UpstreamProtocol == config.UpstreamAnthropicMessages {
		a.messagesNative(c, m, body, "/v1/messages/count_tokens")
		return
	}
	// Local fallback.
	count, err := countLocally(body)
	if err != nil {
		WriteJSONError(c, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"input_tokens": count})
}

// countLocally extracts text from Anthropic-style messages + system + tools and
// returns a tiktoken-based count.
func countLocally(body []byte) (int, error) {
	messages := []tokens.ChatMessage{}
	if sys := gjson.GetBytes(body, "system"); sys.Exists() {
		messages = append(messages, tokens.ChatMessage{Role: "system", Content: anthropicSystemText(sys)})
	}
	for _, m := range gjson.GetBytes(body, "messages").Array() {
		role := m.Get("role").String()
		messages = append(messages, tokens.ChatMessage{Role: role, Content: anthropicContentText(m.Get("content"))})
	}
	return tokens.CountChatMessages(messages)
}

func anthropicSystemText(v gjson.Result) string {
	if v.Type == gjson.String {
		return v.String()
	}
	// system can be an array of {type:"text", text:"..."} blocks
	var sb strings.Builder
	for _, block := range v.Array() {
		if t := block.Get("text"); t.Exists() {
			if sb.Len() > 0 {
				sb.WriteByte('\n')
			}
			sb.WriteString(t.String())
		}
	}
	return sb.String()
}

func anthropicContentText(v gjson.Result) string {
	if v.Type == gjson.String {
		return v.String()
	}
	var sb strings.Builder
	for _, block := range v.Array() {
		switch block.Get("type").String() {
		case "text":
			if sb.Len() > 0 {
				sb.WriteByte('\n')
			}
			sb.WriteString(block.Get("text").String())
		case "tool_result":
			if c := block.Get("content"); c.Exists() {
				if sb.Len() > 0 {
					sb.WriteByte('\n')
				}
				sb.WriteString(anthropicContentText(c))
			}
		case "tool_use":
			// approximate the function name + arguments as text
			name := block.Get("name").String()
			if name != "" {
				sb.WriteByte('\n')
				sb.WriteString(name)
			}
			if input := block.Get("input"); input.Exists() {
				raw, _ := json.Marshal(input.Raw)
				sb.Write(raw)
			}
		}
	}
	return sb.String()
}
