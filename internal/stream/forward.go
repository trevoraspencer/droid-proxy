// Package stream provides an SSE pump that copies an upstream event stream
// to a downstream writer while emitting heartbeats on idle and surfacing
// upstream errors as a terminal event.
package stream

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Options controls the Forward pump.
type Options struct {
	// KeepAlive is the comment-frame heartbeat interval. Zero disables heartbeats.
	KeepAlive time.Duration
	// OnLine is called with every non-blank line read from src before the line is
	// written to dst. Use this to observe events (e.g. capture reasoning deltas).
	// May be nil.
	OnLine func([]byte)
	// WriteKeepAlive lets callers customize the heartbeat payload. If nil, a
	// standard SSE comment line is emitted: ": keep-alive\n\n".
	WriteKeepAlive func() error
	// MaxLineBytes caps the bufio scanner buffer. Defaults to 50 MiB.
	MaxLineBytes int
	// MaxBytes caps the total upstream stream size. Zero disables the total
	// cap. The caller can use the same limit as non-stream responses.
	MaxBytes int64
	// IdleTimeout bounds a stalled upstream stream. It is reset only by real
	// upstream lines, not by downstream keep-alive comments. Zero disables it.
	IdleTimeout time.Duration
	// IsTerminal marks a complete SSE event as terminal. When set, EOF before a
	// terminal event emits WriteTruncationError and returns ErrTruncated.
	IsTerminal func(Event) bool
	// WriteTruncationError writes a protocol-shaped terminal error frame.
	WriteTruncationError func(io.Writer) error
}

var ErrTruncated = errors.New("stream ended before terminal marker")

// Event is a parsed SSE event frame.
type Event struct {
	Name string
	Data string
}

type wireLine struct {
	data  []byte
	bytes int64
}

func splitWireLine(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if i := bytes.IndexByte(data, '\n'); i >= 0 {
		return i + 1, data[:i+1], nil
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

func decodeWireLine(raw []byte) []byte {
	line := raw
	if len(line) > 0 && line[len(line)-1] == '\n' {
		line = line[:len(line)-1]
	}
	if len(line) > 0 && line[len(line)-1] == '\r' {
		line = line[:len(line)-1]
	}
	return line
}

func ChatTerminal(ev Event) bool {
	return strings.TrimSpace(ev.Data) == "[DONE]"
}

func AnthropicTerminal(ev Event) bool {
	return ev.Name == "message_stop"
}

func ResponsesTerminal(ev Event) bool {
	switch ev.Name {
	case "response.completed", "response.failed", "response.incomplete", "error":
		return true
	default:
		return false
	}
}

// Forward reads SSE lines from src and writes them to dst, flushing after each
// event. It emits a keep-alive comment frame on idle when KeepAlive > 0.
//
// It returns nil on normal upstream EOF (the upstream emits its own terminal
// chunk, e.g. data: [DONE]). It returns ctx.Err() on cancellation, or the
// upstream scanner error otherwise. The dst writer's response body is left
// open; callers are responsible for any post-stream framing.
func Forward(ctx context.Context, dst io.Writer, flusher http.Flusher, src io.Reader, opts Options) error {
	if flusher == nil {
		return errors.New("stream.Forward: nil flusher")
	}
	maxLine := opts.MaxLineBytes
	if maxLine <= 0 {
		maxLine = 50 * 1024 * 1024
	}

	lines := make(chan wireLine, 1)
	scanErr := make(chan error, 1)
	scanCtx, scanCancel := context.WithCancel(ctx)
	defer scanCancel()
	if closer, ok := src.(io.Closer); ok {
		defer func() { _ = closer.Close() }()
	}

	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(src)
		scanner.Split(splitWireLine)
		scanner.Buffer(make([]byte, 64*1024), maxLine+2)
		for scanner.Scan() {
			raw := append([]byte(nil), scanner.Bytes()...)
			line := wireLine{data: decodeWireLine(raw), bytes: int64(len(raw))}
			select {
			case lines <- line:
			case <-scanCtx.Done():
				return
			}
		}
		if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
			scanErr <- err
		}
	}()

	writeKeepAlive := opts.WriteKeepAlive
	if writeKeepAlive == nil {
		writeKeepAlive = func() error {
			n, err := dst.Write([]byte(": keep-alive\n\n"))
			if err != nil {
				return err
			}
			if n != len(": keep-alive\n\n") {
				return io.ErrShortWrite
			}
			return nil
		}
	}

	var ticker *time.Ticker
	var tickerCh <-chan time.Time
	if opts.KeepAlive > 0 {
		ticker = time.NewTicker(opts.KeepAlive)
		defer ticker.Stop()
		tickerCh = ticker.C
	}
	var idleTimer *time.Timer
	var idleCh <-chan time.Time
	if opts.IdleTimeout > 0 {
		idleTimer = time.NewTimer(opts.IdleTimeout)
		defer idleTimer.Stop()
		idleCh = idleTimer.C
	}

	resetIdle := func() {
		if idleTimer == nil {
			return
		}
		if !idleTimer.Stop() {
			select {
			case <-idleTimer.C:
			default:
			}
		}
		idleTimer.Reset(opts.IdleTimeout)
	}
	write := func(b []byte) error {
		n, err := dst.Write(b)
		if err != nil {
			scanCancel()
			return err
		}
		if n != len(b) {
			scanCancel()
			return io.ErrShortWrite
		}
		return nil
	}
	writeTruncation := func(reason string) error {
		if opts.WriteTruncationError == nil {
			if reason != "" {
				return fmt.Errorf("%w: %s", ErrTruncated, reason)
			}
			return ErrTruncated
		}
		if err := opts.WriteTruncationError(dst); err != nil {
			return err
		}
		flusher.Flush()
		if reason != "" {
			return fmt.Errorf("%w: %s", ErrTruncated, reason)
		}
		return ErrTruncated
	}

	var eventName string
	var eventData strings.Builder
	var eventDataLines int
	var sawTerminal bool
	var totalBytes int64
	flushEvent := func() {
		if opts.IsTerminal != nil && opts.IsTerminal(Event{Name: eventName, Data: eventData.String()}) {
			sawTerminal = true
		}
		eventName = ""
		eventData.Reset()
		eventDataLines = 0
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case scanned, ok := <-lines:
			if !ok {
				if eventName != "" || eventDataLines > 0 {
					if err := write([]byte("\n")); err != nil {
						return err
					}
					flusher.Flush()
					flushEvent()
				}
				select {
				case err := <-scanErr:
					if opts.IsTerminal != nil && !sawTerminal {
						return writeTruncation(err.Error())
					}
					return err
				default:
					if opts.IsTerminal != nil && !sawTerminal {
						return writeTruncation("upstream EOF")
					}
					return nil
				}
			}
			totalBytes += scanned.bytes
			if opts.MaxBytes > 0 && totalBytes > opts.MaxBytes {
				return writeTruncation("upstream stream exceeded configured cap")
			}
			line := scanned.data
			if opts.OnLine != nil {
				opts.OnLine(line)
			}
			s := string(line)
			if strings.TrimSpace(s) == "" {
				flushEvent()
			} else if strings.HasPrefix(s, "event:") {
				eventName = strings.TrimSpace(strings.TrimPrefix(s, "event:"))
				resetIdle()
			} else if strings.HasPrefix(s, "data:") {
				part := strings.TrimSpace(strings.TrimPrefix(s, "data:"))
				added := len(part)
				if eventDataLines > 0 {
					added++
				}
				if eventData.Len()+added > maxLine {
					return writeTruncation("SSE event exceeded maximum size")
				}
				if eventDataLines > 0 {
					eventData.WriteByte('\n')
				}
				eventData.WriteString(part)
				eventDataLines++
				resetIdle()
			} else if !strings.HasPrefix(strings.TrimSpace(s), ":") {
				resetIdle()
			}
			if err := write(line); err != nil {
				return err
			}
			if err := write([]byte("\n")); err != nil {
				return err
			}
			flusher.Flush()
			if ticker != nil {
				ticker.Reset(opts.KeepAlive)
			}
		case <-tickerCh:
			if err := writeKeepAlive(); err != nil {
				scanCancel()
				return err
			}
			flusher.Flush()
		case <-idleCh:
			scanCancel()
			return writeTruncation("idle timeout")
		}
	}
}
