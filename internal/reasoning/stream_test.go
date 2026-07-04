package reasoning

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/trevoraspencer/droid-proxy/internal/stream"
)

func TestStreamCapture_AccumulatesAndStores(t *testing.T) {
	c := NewCache(8, time.Minute)
	scope := Scope{Provider: "deepseek", AuthHash: "h", Model: "m", BaseURL: "https://api.deepseek.com", Session: "s"}
	cap := NewStreamCapture(scope, c)
	if cap == nil {
		t.Fatal("expected non-nil capture")
	}
	chunks := [][]byte{
		[]byte(`data: {"choices":[{"index":0,"delta":{"reasoning_content":"thinking "}}]}`),
		[]byte(`data: {"choices":[{"index":0,"delta":{"reasoning_content":"about it"}}]}`),
		[]byte(`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_w","type":"function","function":{"name":"f","arguments":""}}]}}]}`),
		[]byte(`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{}"}}]}}]}`),
		[]byte(`data: [DONE]`),
	}
	for _, c := range chunks {
		cap.ObserveLine(c)
	}
	cap.Commit()
	if c.Len() != 1 {
		t.Fatalf("expected 1 stored reasoning, got %d", c.Len())
	}
	v, ok := c.Lookup(Key{Scope: scope, ToolCallIDs: "call_w"})
	if !ok || v != "thinking about it" {
		t.Fatalf("expected stored reasoning, got %q ok=%v", v, ok)
	}
}

func TestStreamCapture_NilCacheNoOp(t *testing.T) {
	cap := NewStreamCapture(Scope{Provider: "p"}, nil)
	if cap != nil {
		t.Fatalf("expected nil capture when cache is nil")
	}
}

func TestStreamCapture_IgnoresMissingToolCallID(t *testing.T) {
	c := NewCache(8, time.Minute)
	scope := Scope{Provider: "deepseek", AuthHash: "h", Model: "m", BaseURL: "https://api.deepseek.com", Session: "s"}
	cap := NewStreamCapture(scope, c)
	chunks := [][]byte{
		[]byte(`data: {"choices":[{"index":0,"delta":{"reasoning_content":"hmm"}}]}`),
		// tool_call without id
		[]byte(`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":"f","arguments":"{}"}}]}}]}`),
		[]byte(`data: [DONE]`),
	}
	for _, ch := range chunks {
		cap.ObserveLine(ch)
	}
	cap.Commit()
	if c.Len() != 0 {
		t.Fatalf("expected 0 stored, got %d", c.Len())
	}
}

func TestStreamCapture_DoneDoesNotAutoCommit(t *testing.T) {
	c := NewCache(8, time.Minute)
	scope := Scope{Provider: "deepseek", AuthHash: "h", Model: "m", BaseURL: "https://api.deepseek.com", Session: "s"}
	cap := NewStreamCapture(scope, c)
	chunks := [][]byte{
		[]byte(`data: {"choices":[{"index":0,"delta":{"reasoning_content":"thinking"}}]}`),
		[]byte(`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_done","type":"function","function":{"name":"f","arguments":"{}"}}]}}]}`),
		[]byte(`data: [DONE]`),
	}
	for _, ch := range chunks {
		cap.ObserveLine(ch)
	}
	if c.Len() != 0 {
		t.Fatalf("observing [DONE] must not commit until forwarding succeeds, got %d entries", c.Len())
	}
	cap.Commit()
	if c.Len() != 1 {
		t.Fatalf("explicit commit after successful forwarding should store reasoning, got %d entries", c.Len())
	}
}

type terminalFailWriter struct {
	writes int
}

func (w *terminalFailWriter) Write(p []byte) (int, error) {
	w.writes++
	if strings.Contains(string(p), "[DONE]") {
		return 0, errors.New("downstream terminal write failed")
	}
	return len(p), nil
}

func (w *terminalFailWriter) Flush() {}

func TestStreamCapture_DoesNotCommitWhenTerminalWriteFails(t *testing.T) {
	c := NewCache(8, time.Minute)
	scope := Scope{Provider: "deepseek", AuthHash: "h", Model: "m", BaseURL: "https://api.deepseek.com", Session: "s"}
	cap := NewStreamCapture(scope, c)
	src := strings.NewReader(strings.Join([]string{
		`data: {"choices":[{"index":0,"delta":{"reasoning_content":"partial reasoning"}}]}`,
		``,
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_failed_done","type":"function","function":{"name":"f","arguments":"{}"}}]}}]}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n"))
	w := &terminalFailWriter{}
	err := stream.Forward(context.Background(), w, w, src, stream.Options{
		OnLine:     func(line []byte) { cap.ObserveLine(line) },
		IsTerminal: stream.ChatTerminal,
		WriteTruncationError: func(dst io.Writer) error {
			_, err := dst.Write([]byte(`data: {"error":{"code":"stream_truncated"}}` + "\n\n"))
			return err
		},
	})
	if err == nil || !strings.Contains(err.Error(), "downstream terminal write failed") {
		t.Fatalf("expected downstream terminal write failure, got %v", err)
	}
	if c.Len() != 0 {
		t.Fatalf("terminal observation before failed downstream write must not commit reasoning, got %d entries", c.Len())
	}
}

func TestStreamCapture_ErrorChunkFails(t *testing.T) {
	c := NewCache(8, time.Minute)
	scope := Scope{Provider: "deepseek", AuthHash: "h", Model: "m", BaseURL: "https://api.deepseek.com", Session: "s"}
	cap := NewStreamCapture(scope, c)
	cap.ObserveLine([]byte(`data: {"choices":[{"index":0,"delta":{"reasoning_content":"x"}}]}`))
	cap.ObserveLine([]byte(`data: {"error":{"message":"boom"}}`))
	cap.ObserveLine([]byte(`data: [DONE]`))
	cap.Commit()
	if c.Len() != 0 {
		t.Fatalf("expected nothing stored after error, got %d", c.Len())
	}
}
