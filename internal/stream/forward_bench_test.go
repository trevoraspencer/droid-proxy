package stream

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"testing"
)

type nopFlusher struct{}

func (nopFlusher) Flush() {}

type countingWriter struct{ n int64 }

func (w *countingWriter) Write(b []byte) (int, error) {
	w.n += int64(len(b))
	return len(b), nil
}

// BenchmarkForward measures the raw SSE pass-through pump — the hot path for
// every native streaming request. Run with:
//
//	go test -bench=. -benchmem -run='^$' ./internal/stream/
func BenchmarkForward(b *testing.B) {
	var src bytes.Buffer
	for i := 0; i < 200; i++ {
		fmt.Fprintf(&src, `data: {"id":"c","choices":[{"index":0,"delta":{"content":"tok%d "},"finish_reason":null}]}`+"\n\n", i)
	}
	src.WriteString("data: [DONE]\n\n")
	payload := src.Bytes()
	b.SetBytes(int64(len(payload)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w := &countingWriter{}
		err := Forward(context.Background(), w, nopFlusher{}, bytes.NewReader(payload), Options{
			IsTerminal:           ChatTerminal,
			WriteTruncationError: func(io.Writer) error { return nil },
		})
		if err != nil {
			b.Fatal(err)
		}
	}
}
