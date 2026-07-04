// Package translate hosts the protocol translators used by the Responses,
// Messages, and Chat Completions handlers. Functions in this package are
// pure (no I/O) so they can be unit-tested without httptest.
package translate

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

type responsesStreamErrorChunk struct {
	Type           string `json:"type"`
	Code           string `json:"code"`
	Message        string `json:"message"`
	SequenceNumber int    `json:"sequence_number"`
}

func responsesStreamErrorCode(status int) string {
	switch status {
	case http.StatusUnauthorized:
		return "invalid_api_key"
	case http.StatusForbidden:
		return "insufficient_quota"
	case http.StatusTooManyRequests:
		return "rate_limit_exceeded"
	case http.StatusNotFound:
		return "model_not_found"
	case http.StatusRequestTimeout:
		return "request_timeout"
	default:
		if status >= http.StatusInternalServerError {
			return "internal_server_error"
		}
		if status >= http.StatusBadRequest {
			return "invalid_request_error"
		}
		return "unknown_error"
	}
}

func ResponsesErrorCode(status int) string {
	return responsesStreamErrorCode(status)
}

func ExtractErrorMessage(body []byte, fallback string) string {
	message := strings.TrimSpace(string(body))
	if message == "" {
		message = fallback
	}
	if json.Valid(body) {
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err == nil {
			if e, ok := payload["error"].(map[string]any); ok {
				if m, ok := e["message"].(string); ok && strings.TrimSpace(m) != "" {
					message = strings.TrimSpace(m)
				}
			}
			if m, ok := payload["message"].(string); ok && strings.TrimSpace(m) != "" {
				message = strings.TrimSpace(m)
			}
		}
	}
	if strings.TrimSpace(message) == "" {
		return "upstream error"
	}
	return message
}

// BuildResponsesStreamErrorChunk builds an OpenAI Responses streaming error chunk.
//
// OpenAI's HTTP error bodies are shaped like {"error":{...}}; those are valid
// for non-streaming responses but streaming clients validate SSE data payloads
// against a union of chunks that requires a top-level `type` field — hence this
// helper.
func BuildResponsesStreamErrorChunk(status int, errText string, sequenceNumber int) []byte {
	if status <= 0 {
		status = http.StatusInternalServerError
	}
	if sequenceNumber < 0 {
		sequenceNumber = 0
	}
	message := strings.TrimSpace(errText)
	if message == "" {
		message = http.StatusText(status)
	}
	code := responsesStreamErrorCode(status)

	trimmed := strings.TrimSpace(errText)
	if trimmed != "" && json.Valid([]byte(trimmed)) {
		var payload map[string]any
		if err := json.Unmarshal([]byte(trimmed), &payload); err == nil {
			if t, ok := payload["type"].(string); ok && strings.TrimSpace(t) == "error" {
				if m, ok := payload["message"].(string); ok && strings.TrimSpace(m) != "" {
					message = strings.TrimSpace(m)
				}
				if v, ok := payload["code"]; ok && v != nil {
					if c, ok := v.(string); ok && strings.TrimSpace(c) != "" {
						code = strings.TrimSpace(c)
					} else {
						code = strings.TrimSpace(fmt.Sprint(v))
					}
				}
				if v, ok := payload["sequence_number"].(float64); ok && sequenceNumber == 0 {
					sequenceNumber = int(v)
				}
			}
			if e, ok := payload["error"].(map[string]any); ok {
				if m, ok := e["message"].(string); ok && strings.TrimSpace(m) != "" {
					message = strings.TrimSpace(m)
				}
				if v, ok := e["code"]; ok && v != nil {
					if c, ok := v.(string); ok && strings.TrimSpace(c) != "" {
						code = strings.TrimSpace(c)
					} else {
						code = strings.TrimSpace(fmt.Sprint(v))
					}
				}
			}
		}
	}

	if strings.TrimSpace(code) == "" {
		code = "unknown_error"
	}

	data, err := json.Marshal(responsesStreamErrorChunk{
		Type:           "error",
		Code:           code,
		Message:        message,
		SequenceNumber: sequenceNumber,
	})
	if err == nil {
		return data
	}
	return []byte(`{"type":"error","code":"internal_server_error","message":"internal error","sequence_number":0}`)
}
