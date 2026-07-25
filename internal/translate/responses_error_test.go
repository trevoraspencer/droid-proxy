package translate

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestBuildResponsesStreamErrorChunk_StatusCodes(t *testing.T) {
	cases := []struct {
		status int
		code   string
	}{
		{http.StatusUnauthorized, "invalid_api_key"},
		{http.StatusForbidden, "insufficient_quota"},
		{http.StatusTooManyRequests, "rate_limit_exceeded"},
		{http.StatusNotFound, "model_not_found"},
		{http.StatusRequestTimeout, "request_timeout"},
		{http.StatusBadGateway, "internal_server_error"},
		{http.StatusBadRequest, "invalid_request_error"},
	}
	for _, c := range cases {
		got := BuildResponsesStreamErrorChunk(c.status, "boom", 1)
		var parsed map[string]any
		if err := json.Unmarshal(got, &parsed); err != nil {
			t.Fatalf("invalid JSON for status=%d: %v", c.status, err)
		}
		if parsed["type"] != "error" {
			t.Errorf("status=%d type=%v", c.status, parsed["type"])
		}
		if parsed["code"] != c.code {
			t.Errorf("status=%d code=%v want=%s", c.status, parsed["code"], c.code)
		}
	}
}

func TestBuildResponsesStreamErrorChunk_OpenAIErrorBody(t *testing.T) {
	body := `{"error":{"message":"context too long","code":"context_length_exceeded"}}`
	got := BuildResponsesStreamErrorChunk(http.StatusBadRequest, body, 0)
	if !strings.Contains(string(got), "context too long") {
		t.Errorf("expected message preserved: %s", got)
	}
	if !strings.Contains(string(got), "context_length_exceeded") {
		t.Errorf("expected code preserved: %s", got)
	}
}

func TestBuildResponsesStreamErrorChunk_DefaultMessage(t *testing.T) {
	got := BuildResponsesStreamErrorChunk(http.StatusInternalServerError, "", 0)
	var parsed map[string]any
	if err := json.Unmarshal(got, &parsed); err != nil {
		t.Fatal(err)
	}
	if msg, _ := parsed["message"].(string); msg == "" {
		t.Errorf("expected non-empty default message, got %s", got)
	}
}

func TestBuildResponsesStreamErrorChunk_StreamErrorTypeShape(t *testing.T) {
	body := `{"type":"error","message":"upstream went away","code":"timeout","sequence_number":42}`
	got := BuildResponsesStreamErrorChunk(0, body, 0)
	var parsed map[string]any
	if err := json.Unmarshal(got, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed["message"] != "upstream went away" {
		t.Errorf("message: %v", parsed["message"])
	}
	if parsed["code"] != "timeout" {
		t.Errorf("code: %v", parsed["code"])
	}
	if seq, _ := parsed["sequence_number"].(float64); int(seq) != 42 {
		t.Errorf("sequence_number: %v", parsed["sequence_number"])
	}
}
