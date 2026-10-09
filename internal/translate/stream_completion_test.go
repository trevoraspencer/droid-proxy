package translate

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/tidwall/gjson"
)

func TestTranslatedResponsesStreamPreservesIncompleteStatus(t *testing.T) {
	for _, reason := range []string{"length", "content_filter"} {
		input := fmt.Sprintf("data: {\"id\":\"chatcmpl-1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"},\"finish_reason\":%q}]}\n\ndata: [DONE]\n\n", reason)
		out, err := ChatStreamToResponsesSSE(strings.NewReader(input), "model")
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(out, []byte("event: response.incomplete\n")) || bytes.Contains(out, []byte("event: response.completed\n")) {
			t.Fatalf("wrong terminal event: %s", out)
		}
		want := reason
		if reason == "length" {
			want = "max_output_tokens"
		}
		for _, line := range strings.Split(string(out), "\n") {
			data := strings.TrimPrefix(line, "data: ")
			if gjson.Get(data, "type").String() == "response.incomplete" {
				if gjson.Get(data, "response.incomplete_details.reason").String() != want || gjson.Get(data, "response.output.0.content.0.text").String() != "partial" {
					t.Fatalf("lost result: %s", data)
				}
			}
		}
	}
}

func TestTranslatedResponsesStreamKeepsOneResponseIdentity(t *testing.T) {
	input := "data: {\"id\":\"chatcmpl-first\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"}}]}\n\n" +
		"data: {\"id\":\"chatcmpl-other\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
	out, err := ChatStreamToResponsesSSE(strings.NewReader(input), "model")
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, line := range strings.Split(string(out), "\n") {
		data := strings.TrimPrefix(line, "data: ")
		if id := gjson.Get(data, "response.id").String(); id != "" {
			count++
			if id != "resp_chatcmpl-first" {
				t.Fatalf("response identity changed: %s", data)
			}
		}
	}
	if count != 2 {
		t.Fatalf("missing created/completed lifecycle: %s", out)
	}
}

func TestTranslatedChatStreamReturnsAtDone(t *testing.T) {
	r, w := io.Pipe()
	defer r.Close()
	defer w.Close()
	go func() {
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	var out bytes.Buffer
	if err := ForwardChatStreamToResponsesWithOptions(r, &out, nil, "model", ChatStreamForwardOptions{Context: ctx}); err != nil {
		t.Fatalf("[DONE] waited for EOF: %v", err)
	}
}

func TestTranslatedResponsesPreservePartialToolArguments(t *testing.T) {
	input := "data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"function\":{\"name\":\"tool\",\"arguments\":\"{\\\"partial\\\":\"}}]},\"finish_reason\":\"length\"}]}\n\ndata: [DONE]\n\n"
	out, err := ChatStreamToResponsesSSE(strings.NewReader(input), "model")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		data := strings.TrimPrefix(line, "data: ")
		if gjson.Get(data, "type").String() == "response.incomplete" {
			if gjson.Get(data, "response.output.0.arguments").String() != `{"partial":` || gjson.Get(data, "response.output.0.status").String() != "incomplete" {
				t.Fatalf("partial tool lost: %s", data)
			}
			return
		}
	}
	t.Fatalf("missing incomplete terminal event: %s", out)
}
