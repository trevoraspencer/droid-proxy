package translate

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

type chatStreamChunk struct {
	ID      string             `json:"id"`
	Model   string             `json:"model"`
	Choices []chatStreamChoice `json:"choices"`
	Usage   map[string]any     `json:"usage"`
}

type chatStreamChoice struct {
	Index        int            `json:"index"`
	Delta        map[string]any `json:"delta"`
	FinishReason any            `json:"finish_reason"`
}

type ChatStreamTarget string

const (
	ChatStreamTargetResponses ChatStreamTarget = "responses"
	ChatStreamTargetAnthropic ChatStreamTarget = "anthropic"
)

func ChatStreamToResponsesSSE(r io.Reader, model string) ([]byte, error) {
	var out bytes.Buffer
	if err := ForwardChatStreamToResponses(r, &out, nil, model); err != nil {
		return responsesStreamErrorFrame(http.StatusBadGateway, err.Error()), nil
	}
	return out.Bytes(), nil
}

func ForwardChatStreamToResponses(r io.Reader, w io.Writer, flush func(), model string) error {
	return ForwardChatStreamToResponsesWithOptions(r, w, flush, model, ChatStreamForwardOptions{})
}

type ChatStreamForwardOptions struct {
	Context     context.Context
	KeepAlive   time.Duration
	IdleTimeout time.Duration
}

func ForwardChatStreamToResponsesWithOptions(r io.Reader, w io.Writer, flush func(), model string, opts ChatStreamForwardOptions) error {
	state := newResponsesStreamState(w, model)
	if state.err != nil {
		return state.err
	}
	return readChatStreamEventsIncremental(r, opts, w, flush, func(ev chatStreamChunk) error {
		if err := state.observe(ev); err != nil {
			return err
		}
		if flush != nil {
			flush()
		}
		return nil
	}, func() error {
		if err := state.complete(); err != nil {
			return err
		}
		if flush != nil {
			flush()
		}
		return nil
	})
}

type responsesStreamState struct {
	w           io.Writer
	model       string
	respID      string
	textStarted bool
	text        string
	tools       map[int]*responsesStreamTool
	toolOffset  int
	finished    bool
	usage       map[string]any
	err         error
}

type responsesStreamTool struct {
	ID          string
	Name        string
	Arguments   string
	OutputIndex int
}

func newResponsesStreamState(w io.Writer, model string) *responsesStreamState {
	s := &responsesStreamState{w: w, model: model, respID: "resp_chat", tools: map[int]*responsesStreamTool{}}
	s.err = writeSSETo(s.w, "response.created", map[string]any{"type": "response.created", "response": map[string]any{"id": s.respID, "object": "response", "status": "in_progress", "model": model}})
	return s
}

func (s *responsesStreamState) observe(ev chatStreamChunk) error {
	if len(ev.Choices) > 1 {
		return errors.New("Chat stream contains multiple choices, which this translator does not merge")
	}
	if ev.Usage != nil {
		s.usage = ev.Usage
	}
	if len(ev.Choices) == 0 {
		// Tolerate zero-choice chunks: Azure OpenAI emits a leading
		// prompt-filter-results chunk with empty choices, and
		// stream_options.include_usage makes the final usage chunk carry no
		// choices. Their usage (captured above) is the only content they have.
		return nil
	}
	ch := ev.Choices[0]
	if ch.Index != 0 {
		return errors.New("Chat stream contains non-zero choice index, which this translator does not merge")
	}
	if ev.ID != "" {
		s.respID = "resp_" + ev.ID
	}
	if text := stringValue(ch.Delta["content"]); text != "" {
		if !s.textStarted {
			if len(s.tools) > 0 {
				return errors.New("Chat stream emitted text after tool calls, which this translator cannot reorder")
			}
			s.toolOffset = 1
			if err := writeSSETo(s.w, "response.output_item.added", map[string]any{"type": "response.output_item.added", "output_index": 0, "item": map[string]any{"type": "message", "id": "msg_0", "role": "assistant", "content": []any{}}}); err != nil {
				return err
			}
			s.textStarted = true
		}
		s.text += text
		if err := writeSSETo(s.w, "response.output_text.delta", map[string]any{"type": "response.output_text.delta", "output_index": 0, "content_index": 0, "delta": text}); err != nil {
			return err
		}
	}
	if rawTools, ok := ch.Delta["tool_calls"].([]any); ok {
		for _, raw := range rawTools {
			tc, _ := raw.(map[string]any)
			idx := intNumber(tc["index"])
			outputIndex := idx + s.toolOffset
			fn, _ := tc["function"].(map[string]any)
			tool := s.tools[idx]
			if tool == nil {
				id := firstNonEmpty(stringValue(tc["id"]), fmt.Sprintf("call_%d", idx))
				if err := writeSSETo(s.w, "response.output_item.added", map[string]any{"type": "response.output_item.added", "output_index": outputIndex, "item": map[string]any{"type": "function_call", "id": id, "call_id": id, "name": stringValue(fn["name"]), "arguments": ""}}); err != nil {
					return err
				}
				tool = &responsesStreamTool{ID: id, Name: stringValue(fn["name"]), OutputIndex: outputIndex}
				s.tools[idx] = tool
			}
			if name := stringValue(fn["name"]); name != "" {
				tool.Name = name
			}
			if args := stringValue(fn["arguments"]); args != "" {
				tool.Arguments += args
				if err := writeSSETo(s.w, "response.function_call_arguments.delta", map[string]any{"type": "response.function_call_arguments.delta", "output_index": outputIndex, "delta": args}); err != nil {
					return err
				}
			}
		}
	}
	if ch.FinishReason != nil {
		s.finished = true
	}
	return nil
}

func (s *responsesStreamState) complete() error {
	if !s.finished {
		return errors.New("Chat stream ended before terminal finish_reason")
	}
	if err := validateResponsesStreamToolArguments(s.tools); err != nil {
		return err
	}
	output := []any{}
	if s.textStarted {
		textItem := map[string]any{
			"type":   "message",
			"id":     "msg_0",
			"status": "completed",
			"role":   "assistant",
			"content": []any{map[string]any{
				"type":        "output_text",
				"text":        s.text,
				"annotations": []any{},
			}},
		}
		if err := writeSSETo(s.w, "response.output_text.done", map[string]any{"type": "response.output_text.done", "output_index": 0, "content_index": 0, "text": s.text}); err != nil {
			return err
		}
		if err := writeSSETo(s.w, "response.output_item.done", map[string]any{"type": "response.output_item.done", "output_index": 0, "item": textItem}); err != nil {
			return err
		}
		output = append(output, textItem)
	}
	for _, idx := range sortedResponseStreamToolIndexes(s.tools) {
		tool := s.tools[idx]
		args := strings.TrimSpace(tool.Arguments)
		if args == "" {
			args = "{}"
		}
		toolItem := map[string]any{
			"type":      "function_call",
			"id":        tool.ID,
			"call_id":   tool.ID,
			"name":      tool.Name,
			"arguments": args,
			"status":    "completed",
		}
		if err := writeSSETo(s.w, "response.function_call_arguments.done", map[string]any{"type": "response.function_call_arguments.done", "output_index": tool.OutputIndex, "arguments": args}); err != nil {
			return err
		}
		if err := writeSSETo(s.w, "response.output_item.done", map[string]any{"type": "response.output_item.done", "output_index": tool.OutputIndex, "item": toolItem}); err != nil {
			return err
		}
		output = append(output, toolItem)
	}
	response := map[string]any{"id": s.respID, "object": "response", "status": "completed", "model": s.model, "output": output}
	if u, ok := chatUsageToResponsesUsage(s.usage); ok && len(u) > 0 {
		response["usage"] = u
	}
	return writeSSETo(s.w, "response.completed", map[string]any{"type": "response.completed", "response": response})
}

func ChatStreamToAnthropicSSE(r io.Reader, model string) ([]byte, error) {
	var out bytes.Buffer
	if err := ForwardChatStreamToAnthropic(r, &out, nil, model); err != nil {
		return anthropicStreamErrorFrame(err.Error()), nil
	}
	return out.Bytes(), nil
}

func ForwardChatStreamToAnthropic(r io.Reader, w io.Writer, flush func(), model string) error {
	return ForwardChatStreamToAnthropicWithOptions(r, w, flush, model, ChatStreamForwardOptions{})
}

func ForwardChatStreamToAnthropicWithOptions(r io.Reader, w io.Writer, flush func(), model string, opts ChatStreamForwardOptions) error {
	state := newAnthropicStreamState(w, model)
	if state.err != nil {
		return state.err
	}
	return readChatStreamEventsIncremental(r, opts, w, flush, func(ev chatStreamChunk) error {
		if err := state.observe(ev); err != nil {
			return err
		}
		if flush != nil {
			flush()
		}
		return nil
	}, func() error {
		if err := state.complete(); err != nil {
			return err
		}
		if flush != nil {
			flush()
		}
		return nil
	})
}

type anthropicStreamState struct {
	w          io.Writer
	model      string
	msgID      string
	blockOpen  bool
	toolBlocks map[int]bool
	toolArgs   map[int]string
	toolOffset int
	finished   bool
	stopReason string
	usage      map[string]any
	err        error
}

func newAnthropicStreamState(w io.Writer, model string) *anthropicStreamState {
	s := &anthropicStreamState{w: w, model: model, msgID: "msg_chat", toolBlocks: map[int]bool{}, toolArgs: map[int]string{}, stopReason: "end_turn"}
	s.err = writeSSETo(s.w, "message_start", map[string]any{"type": "message_start", "message": map[string]any{"id": s.msgID, "type": "message", "role": "assistant", "model": model, "content": []any{}, "stop_reason": nil}})
	return s
}

func (s *anthropicStreamState) observe(ev chatStreamChunk) error {
	if len(ev.Choices) > 1 {
		return errors.New("Chat stream contains multiple choices, which this translator does not merge")
	}
	if ev.Usage != nil {
		s.usage = ev.Usage
	}
	if len(ev.Choices) == 0 {
		// Tolerate zero-choice chunks: Azure OpenAI emits a leading
		// prompt-filter-results chunk with empty choices, and
		// stream_options.include_usage makes the final usage chunk carry no
		// choices. Their usage (captured above) is the only content they have.
		return nil
	}
	ch := ev.Choices[0]
	if ch.Index != 0 {
		return errors.New("Chat stream contains non-zero choice index, which this translator does not merge")
	}
	if ev.ID != "" {
		s.msgID = "msg_" + ev.ID
	}
	if text := stringValue(ch.Delta["content"]); text != "" {
		if !s.blockOpen {
			if len(s.toolBlocks) > 0 {
				return errors.New("Chat stream emitted text after tool calls, which this translator cannot reorder")
			}
			s.toolOffset = 1
			if err := writeSSETo(s.w, "content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": ""}}); err != nil {
				return err
			}
			s.blockOpen = true
		}
		if err := writeSSETo(s.w, "content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": text}}); err != nil {
			return err
		}
	}
	if rawTools, ok := ch.Delta["tool_calls"].([]any); ok {
		for _, raw := range rawTools {
			tc, _ := raw.(map[string]any)
			idx := intNumber(tc["index"])
			blockIndex := idx + s.toolOffset
			fn, _ := tc["function"].(map[string]any)
			if !s.toolBlocks[idx] {
				id := firstNonEmpty(stringValue(tc["id"]), fmt.Sprintf("call_%d", idx))
				if err := writeSSETo(s.w, "content_block_start", map[string]any{"type": "content_block_start", "index": blockIndex, "content_block": map[string]any{"type": "tool_use", "id": id, "name": stringValue(fn["name"]), "input": map[string]any{}}}); err != nil {
					return err
				}
				s.toolBlocks[idx] = true
			}
			if args := stringValue(fn["arguments"]); args != "" {
				s.toolArgs[idx] += args
				if err := writeSSETo(s.w, "content_block_delta", map[string]any{"type": "content_block_delta", "index": blockIndex, "delta": map[string]any{"type": "input_json_delta", "partial_json": args}}); err != nil {
					return err
				}
			}
		}
	}
	if ch.FinishReason != nil {
		s.finished = true
		s.stopReason = chatFinishReasonToAnthropicStopReason(fmt.Sprint(ch.FinishReason))
	}
	return nil
}

func (s *anthropicStreamState) complete() error {
	if !s.finished {
		return errors.New("Chat stream ended before terminal finish_reason")
	}
	if err := validateAccumulatedToolArguments(s.toolBlocks, s.toolArgs); err != nil {
		return err
	}
	if s.blockOpen {
		if err := writeSSETo(s.w, "content_block_stop", map[string]any{"type": "content_block_stop", "index": 0}); err != nil {
			return err
		}
	}
	for _, idx := range sortedIntKeys(s.toolBlocks) {
		if err := writeSSETo(s.w, "content_block_stop", map[string]any{"type": "content_block_stop", "index": idx + s.toolOffset}); err != nil {
			return err
		}
		s.stopReason = "tool_use"
	}
	deltaEvent := map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": s.stopReason, "stop_sequence": nil}}
	// Relay upstream usage (captured from the include_usage final chunk) so
	// token and cache accounting stays observable through the translation.
	if u, ok := chatUsageToAnthropicUsage(s.usage); ok && len(u) > 0 {
		deltaEvent["usage"] = u
	}
	if err := writeSSETo(s.w, "message_delta", deltaEvent); err != nil {
		return err
	}
	return writeSSETo(s.w, "message_stop", map[string]any{"type": "message_stop"})
}

func readChatStreamEventsIncremental(r io.Reader, opts ChatStreamForwardOptions, w io.Writer, flushWriter func(), onEvent func(chatStreamChunk) error, onDone func() error) error {
	ctx := opts.Context
	if ctx == nil {
		ctx = context.Background()
	}
	var data strings.Builder
	sawData := false
	sawDone := false
	flush := func() error {
		s := strings.TrimSpace(data.String())
		data.Reset()
		if s == "" {
			return nil
		}
		if s == "[DONE]" {
			sawDone = true
			return onDone()
		}
		var ev chatStreamChunk
		if err := json.Unmarshal([]byte(s), &ev); err != nil {
			return fmt.Errorf("invalid Chat stream JSON: %w", err)
		}
		sawData = true
		return onEvent(ev)
	}

	lines := make(chan string, 32)
	errs := make(chan error, 1)
	scanCtx, scanCancel := context.WithCancel(ctx)
	defer scanCancel()
	closeReader := func() {
		if closer, ok := r.(io.Closer); ok {
			_ = closer.Close()
		}
	}
	defer closeReader()
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(r)
		scanner.Buffer(make([]byte, 64*1024), 50*1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			select {
			case lines <- line:
			case <-scanCtx.Done():
				return
			}
		}
		if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) && scanCtx.Err() == nil {
			errs <- err
		}
	}()
	var keepTicker *time.Ticker
	var keepCh <-chan time.Time
	if opts.KeepAlive > 0 {
		keepTicker = time.NewTicker(opts.KeepAlive)
		defer keepTicker.Stop()
		keepCh = keepTicker.C
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
	for {
		select {
		case <-ctx.Done():
			scanCancel()
			closeReader()
			return ctx.Err()
		case line, ok := <-lines:
			if !ok {
				select {
				case err := <-errs:
					return err
				default:
				}
				if data.Len() > 0 {
					if err := flush(); err != nil {
						return err
					}
				}
				if !sawData {
					return errors.New("Chat stream contained no data events")
				}
				if !sawDone {
					return errors.New("Chat stream ended before [DONE]")
				}
				return nil
			}
			if strings.TrimSpace(line) == "" {
				if err := flush(); err != nil {
					scanCancel()
					closeReader()
					return err
				}
				continue
			}
			if strings.HasPrefix(line, "data:") {
				if data.Len() > 0 {
					data.WriteByte('\n')
				}
				data.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
				resetIdle()
				if keepTicker != nil {
					keepTicker.Reset(opts.KeepAlive)
				}
			}
		case <-keepCh:
			if w != nil {
				if _, err := io.WriteString(w, ": keep-alive\n\n"); err != nil {
					scanCancel()
					closeReader()
					return err
				}
				if flushWriter != nil {
					flushWriter()
				}
			}
		case <-idleCh:
			scanCancel()
			closeReader()
			return errors.New("Chat stream idle timeout")
		}
	}
}

func validateAccumulatedToolArguments(started map[int]bool, args map[int]string) error {
	for _, idx := range sortedIntKeys(started) {
		raw := strings.TrimSpace(args[idx])
		if raw == "" {
			raw = "{}"
		}
		var v any
		if err := json.Unmarshal([]byte(raw), &v); err != nil {
			return fmt.Errorf("Chat stream tool_call arguments for index %d are not valid JSON: %w", idx, err)
		}
	}
	return nil
}

func validateResponsesStreamToolArguments(tools map[int]*responsesStreamTool) error {
	for _, idx := range sortedResponseStreamToolIndexes(tools) {
		raw := strings.TrimSpace(tools[idx].Arguments)
		if raw == "" {
			raw = "{}"
		}
		var v any
		if err := json.Unmarshal([]byte(raw), &v); err != nil {
			return fmt.Errorf("Chat stream tool_call arguments for index %d are not valid JSON: %w", idx, err)
		}
	}
	return nil
}

func sortedResponseStreamToolIndexes(tools map[int]*responsesStreamTool) []int {
	keys := make([]int, 0, len(tools))
	for k := range tools {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	return keys
}

func sortedIntKeys(m map[int]bool) []int {
	keys := make([]int, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	return keys
}

func writeSSETo(w io.Writer, event string, payload any) error {
	b, _ := json.Marshal(payload)
	_, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
	return err
}

func responsesStreamErrorFrame(status int, msg string) []byte {
	var out bytes.Buffer
	fmt.Fprintf(&out, "event: error\ndata: %s\n\n", BuildResponsesStreamErrorChunk(status, msg, 0))
	return out.Bytes()
}

func anthropicStreamErrorFrame(msg string) []byte {
	var out bytes.Buffer
	payload, _ := json.Marshal(map[string]any{"type": "error", "error": map[string]any{"type": "api_error", "message": strings.TrimSpace(msg)}})
	fmt.Fprintf(&out, "event: error\ndata: %s\n\n", payload)
	return out.Bytes()
}

func intNumber(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	default:
		return 0
	}
}
