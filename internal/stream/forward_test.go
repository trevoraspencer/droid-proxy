package stream

import (
	"bytes"
	"context"
	"errors"
	"io"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

type captureWriter struct {
	mu      sync.Mutex
	buf     bytes.Buffer
	flushes int
}

func (w *captureWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}
func (w *captureWriter) Flush() {
	w.mu.Lock()
	w.flushes++
	w.mu.Unlock()
}
func (w *captureWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

func TestForward_BasicSSE(t *testing.T) {
	src := strings.NewReader("data: {\"a\":1}\n\ndata: {\"b\":2}\n\ndata: [DONE]\n\n")
	w := &captureWriter{}
	err := Forward(context.Background(), w, w, src, Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := w.String()
	if !strings.Contains(out, `data: {"a":1}`) || !strings.Contains(out, `data: {"b":2}`) || !strings.Contains(out, "[DONE]") {
		t.Errorf("missing chunks: %q", out)
	}
	if w.flushes < 3 {
		t.Errorf("expected at least 3 flushes, got %d", w.flushes)
	}
}

type slowReader struct {
	pieces chan string
	done   bool
}

func (r *slowReader) Read(p []byte) (int, error) {
	piece, ok := <-r.pieces
	if !ok {
		return 0, io.EOF
	}
	n := copy(p, []byte(piece))
	return n, nil
}

func TestForward_KeepAliveOnIdle(t *testing.T) {
	pieces := make(chan string, 4)
	pieces <- "data: {\"a\":1}\n\n"
	// no more pieces yet — reader will block

	src := &slowReader{pieces: pieces}
	w := &captureWriter{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- Forward(ctx, w, w, src, Options{KeepAlive: 30 * time.Millisecond}) }()

	// Wait long enough for at least one keep-alive after the initial chunk.
	time.Sleep(120 * time.Millisecond)
	out := w.String()
	if !strings.Contains(out, "data: {\"a\":1}") {
		t.Errorf("missing initial chunk: %q", out)
	}
	if !strings.Contains(out, "keep-alive") {
		t.Errorf("expected keep-alive comment, got %q", out)
	}

	close(pieces)
	if err := <-done; err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestForward_ContextCancellation(t *testing.T) {
	pieces := make(chan string, 4)
	src := &slowReader{pieces: pieces}
	w := &captureWriter{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Forward(ctx, w, w, src, Options{}) }()
	cancel()
	select {
	case err := <-done:
		if err == nil || !errors.Is(err, context.Canceled) {
			t.Errorf("expected context.Canceled, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Forward did not return after cancel")
	}
}

type errReader struct{}

func (errReader) Read(p []byte) (int, error) { return 0, errors.New("upstream boom") }

func TestForward_PropagatesScannerError(t *testing.T) {
	w := &captureWriter{}
	// scanner reports the underlying error only at .Err() after Scan() returns false.
	err := Forward(context.Background(), w, w, errReader{}, Options{})
	if err == nil || !strings.Contains(err.Error(), "upstream boom") {
		t.Errorf("expected upstream boom error, got %v", err)
	}
}

type errAfterDataReader struct {
	sent bool
}

func (r *errAfterDataReader) Read(p []byte) (int, error) {
	if !r.sent {
		r.sent = true
		return copy(p, "data: {\"a\":1}\n\n"), nil
	}
	return 0, errors.New("upstream reset")
}

func TestForward_ScannerErrorBeforeTerminalEmitsProtocolError(t *testing.T) {
	w := &captureWriter{}
	err := Forward(context.Background(), w, w, &errAfterDataReader{}, Options{
		IsTerminal: ChatTerminal,
		WriteTruncationError: func(dst io.Writer) error {
			_, err := dst.Write([]byte("data: {\"error\":{\"code\":\"stream_truncated\"}}\n\n"))
			return err
		},
	})
	if !errors.Is(err, ErrTruncated) {
		t.Fatalf("expected ErrTruncated, got %v", err)
	}
	if !strings.Contains(w.String(), `"stream_truncated"`) {
		t.Fatalf("scanner error did not emit protocol truncation frame: %q", w.String())
	}
}

func TestForward_OnLineHookFires(t *testing.T) {
	src := strings.NewReader("data: hello\n\ndata: world\n\n")
	w := &captureWriter{}
	seen := []string{}
	var mu sync.Mutex
	err := Forward(context.Background(), w, w, src, Options{
		OnLine: func(b []byte) {
			mu.Lock()
			seen = append(seen, string(b))
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) == 0 {
		t.Fatal("OnLine never fired")
	}
	joined := strings.Join(seen, "|")
	if !strings.Contains(joined, "data: hello") || !strings.Contains(joined, "data: world") {
		t.Errorf("OnLine missed lines: %v", seen)
	}
}

func TestForward_TerminalMarkerAvoidsTruncationError(t *testing.T) {
	src := strings.NewReader("data: {\"a\":1}\n\ndata: [DONE]\n\n")
	w := &captureWriter{}
	err := Forward(context.Background(), w, w, src, Options{
		IsTerminal: ChatTerminal,
		WriteTruncationError: func(dst io.Writer) error {
			_, err := dst.Write([]byte("data: error\n\n"))
			return err
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(w.String(), "data: error") {
		t.Fatalf("terminal stream got truncation error: %q", w.String())
	}
}

func TestForward_PendingTerminalEOFCompletesFrame(t *testing.T) {
	src := strings.NewReader("data: [DONE]\n")
	w := &captureWriter{}
	err := Forward(context.Background(), w, w, src, Options{
		IsTerminal: ChatTerminal,
		WriteTruncationError: func(dst io.Writer) error {
			_, err := dst.Write([]byte("data: error\n\n"))
			return err
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := w.String(); got != "data: [DONE]\n\n" {
		t.Fatalf("expected terminal event to be completed, got %q", got)
	}
}

func TestForward_PendingNonTerminalEOFFlushesBeforeTruncation(t *testing.T) {
	src := strings.NewReader("data: {\"a\":1}\n")
	w := &captureWriter{}
	err := Forward(context.Background(), w, w, src, Options{
		IsTerminal: ChatTerminal,
		WriteTruncationError: func(dst io.Writer) error {
			_, err := dst.Write([]byte("data: {\"error\":{\"code\":\"stream_truncated\"}}\n\n"))
			return err
		},
	})
	if !errors.Is(err, ErrTruncated) {
		t.Fatalf("expected ErrTruncated, got %v", err)
	}
	want := "data: {\"a\":1}\n\n"
	if got := w.String(); !strings.HasPrefix(got, want) || !strings.Contains(got, `"stream_truncated"`) {
		t.Fatalf("expected pending event before truncation frame, got %q", got)
	}
}

func TestForward_TruncationEmitsProtocolError(t *testing.T) {
	src := strings.NewReader("data: {\"a\":1}\n\n")
	w := &captureWriter{}
	err := Forward(context.Background(), w, w, src, Options{
		IsTerminal: ChatTerminal,
		WriteTruncationError: func(dst io.Writer) error {
			_, err := dst.Write([]byte("data: {\"error\":{\"code\":\"stream_truncated\"}}\n\n"))
			return err
		},
	})
	if !errors.Is(err, ErrTruncated) {
		t.Fatalf("expected ErrTruncated, got %v", err)
	}
	if !strings.Contains(w.String(), `"stream_truncated"`) {
		t.Fatalf("missing truncation frame: %q", w.String())
	}
}

func TestForward_IdleTimeoutIgnoresDownstreamKeepAlive(t *testing.T) {
	pr, pw := io.Pipe()
	t.Cleanup(func() { _ = pw.Close() })
	go func() { _, _ = pw.Write([]byte("data: {\"a\":1}\n\n")) }()
	w := &captureWriter{}
	err := Forward(context.Background(), w, w, pr, Options{
		KeepAlive:   15 * time.Millisecond,
		IdleTimeout: 60 * time.Millisecond,
		IsTerminal:  ChatTerminal,
		WriteTruncationError: func(dst io.Writer) error {
			_, err := dst.Write([]byte("data: {\"error\":{\"code\":\"stream_truncated\"}}\n\n"))
			return err
		},
	})
	if !errors.Is(err, ErrTruncated) {
		t.Fatalf("expected idle truncation, got %v", err)
	}
	out := w.String()
	if !strings.Contains(out, ": keep-alive\n\n") {
		t.Fatalf("expected keepalive comments during stall, got %q", out)
	}
	if !strings.Contains(out, `"stream_truncated"`) {
		t.Fatalf("idle timeout did not emit terminal error: %q", out)
	}
}

type failingWriter struct {
	writes int
}

func (w *failingWriter) Write(p []byte) (int, error) {
	w.writes++
	if w.writes > 1 {
		return 0, errors.New("downstream closed")
	}
	return len(p), nil
}
func (w *failingWriter) Flush() {}

func TestForward_DownstreamWriteFailureStops(t *testing.T) {
	src := strings.NewReader("data: {\"a\":1}\n\ndata: [DONE]\n\n")
	w := &failingWriter{}
	err := Forward(context.Background(), w, w, src, Options{IsTerminal: ChatTerminal})
	if err == nil || !strings.Contains(err.Error(), "downstream closed") {
		t.Fatalf("expected downstream write error, got %v", err)
	}
}

type failAllWriter struct{}

func (failAllWriter) Write([]byte) (int, error) { return 0, errors.New("downstream gone") }
func (failAllWriter) Flush()                    {}

func TestForward_KeepAliveWriteFailureStopsWithoutIdleTimeout(t *testing.T) {
	pieces := make(chan string)
	src := &slowReader{pieces: pieces}
	w := failAllWriter{}
	done := make(chan error, 1)
	go func() {
		done <- Forward(context.Background(), w, w, src, Options{KeepAlive: 10 * time.Millisecond})
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "downstream gone") {
			t.Fatalf("expected downstream keepalive write error, got %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Forward waited despite keepalive write failure and disabled idle timeout")
	}
	close(pieces)
}

func TestForward_StreamPathsDoNotLeakGoroutines(t *testing.T) {
	baseline := runtime.NumGoroutine()
	const loops = 20
	for i := 0; i < loops; i++ {
		t.Run("success", func(t *testing.T) {
			src := strings.NewReader("data: {\"a\":1}\n\ndata: [DONE]\n\n")
			w := &captureWriter{}
			if err := Forward(context.Background(), w, w, src, Options{IsTerminal: ChatTerminal}); err != nil {
				t.Fatalf("success stream error: %v", err)
			}
		})
		t.Run("truncation", func(t *testing.T) {
			src := strings.NewReader("data: {\"a\":1}\n\n")
			w := &captureWriter{}
			err := Forward(context.Background(), w, w, src, Options{
				IsTerminal: ChatTerminal,
				WriteTruncationError: func(dst io.Writer) error {
					_, err := dst.Write([]byte("data: {\"error\":{\"code\":\"stream_truncated\"}}\n\n"))
					return err
				},
			})
			if !errors.Is(err, ErrTruncated) {
				t.Fatalf("truncation error=%v", err)
			}
		})
		t.Run("cancellation", func(t *testing.T) {
			pr, pw := io.Pipe()
			w := &captureWriter{}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- Forward(ctx, w, w, pr, Options{IsTerminal: ChatTerminal}) }()
			cancel()
			select {
			case err := <-done:
				if err == nil || !errors.Is(err, context.Canceled) {
					t.Fatalf("cancel error=%v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("Forward did not return after cancellation")
			}
			_ = pw.Close()
		})
		t.Run("write_failure", func(t *testing.T) {
			pr, pw := io.Pipe()
			doneWriting := make(chan struct{})
			go func() {
				defer close(doneWriting)
				_, _ = pw.Write([]byte("data: {\"a\":1}\n\ndata: [DONE]\n\n"))
				_ = pw.Close()
			}()
			w := &failingWriter{}
			err := Forward(context.Background(), w, w, pr, Options{IsTerminal: ChatTerminal})
			if err == nil || !strings.Contains(err.Error(), "downstream closed") {
				t.Fatalf("write failure error=%v", err)
			}
			select {
			case <-doneWriting:
			case <-time.After(time.Second):
				t.Fatal("upstream writer did not unblock after downstream write failure")
			}
		})
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		runtime.GC()
		got := runtime.NumGoroutine()
		if got <= baseline+8 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("goroutines did not return near baseline: baseline=%d got=%d tolerance=8", baseline, got)
		}
		time.Sleep(25 * time.Millisecond)
	}
}
