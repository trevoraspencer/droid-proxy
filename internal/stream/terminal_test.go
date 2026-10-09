package stream

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"
)

func TestForwardReturnsOnTerminalBeforeUpstreamCloses(t *testing.T) {
	for _, tt := range []struct {
		name     string
		body     string
		terminal func(Event) bool
	}{
		{"chat", "data: [DONE]\n\n", ChatTerminal},
		{"responses_data_only", "data: {\"type\":\"response.completed\"}\n\n", ResponsesTerminal},
		{"anthropic_error", "event: error\ndata: {\"type\":\"error\"}\n\n", AnthropicTerminal},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r, w := io.Pipe()
			defer r.Close()
			defer w.Close()
			go func() { _, _ = io.WriteString(w, tt.body) }()
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			out := &captureWriter{}
			if err := Forward(ctx, out, out, r, Options{IsTerminal: tt.terminal}); err != nil {
				t.Fatalf("terminal marker waited for EOF: %v", err)
			}
			if out.String() != tt.body {
				t.Fatalf("body = %q", out.String())
			}
		})
	}
}

func TestResponsesTerminalDataTypes(t *testing.T) {
	for _, kind := range []string{"response.completed", "response.failed", "response.incomplete", "error"} {
		if !ResponsesTerminal(Event{Data: `{"type":"` + kind + `"}`}) {
			t.Errorf("missed %s", kind)
		}
	}
	if ResponsesTerminal(Event{Data: `{"type":"response.output_text.delta","delta":"response.completed"}`}) {
		t.Fatal("delta mistaken for terminal")
	}
	if ResponsesTerminal(Event{Data: strings.Repeat("x", 10)}) {
		t.Fatal("invalid JSON mistaken for terminal")
	}
}
