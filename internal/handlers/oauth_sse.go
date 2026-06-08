package handlers

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"droid-proxy/internal/config"
	"droid-proxy/internal/oauth"
	"droid-proxy/internal/stream"
	"droid-proxy/internal/translate"
)

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
	case "error":
		return responsesSSEErrorFrame(data)
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

func responsesSSEErrorFrame(data []byte) []byte {
	chunk := translate.BuildResponsesStreamErrorChunk(http.StatusBadGateway, string(data), 0)
	var buf bytes.Buffer
	buf.WriteString("event: error\n")
	buf.WriteString("data: ")
	buf.Write(chunk)
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
