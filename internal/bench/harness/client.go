package harness

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/tidwall/gjson"
)

// Usage aggregates the cache-relevant usage counters across protocols.
type Usage struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	CachedTokens     int64 `json:"cached_tokens"`
	CacheWriteTokens int64 `json:"cache_write_tokens"`
}

// Sample is one measured request.
type Sample struct {
	Err    string        `json:"err,omitempty"`
	Status int           `json:"status,omitempty"`
	TTFB   time.Duration `json:"ttfb_ns"`
	// TTFT is time to the first content token (streaming only; zero otherwise).
	TTFT  time.Duration `json:"ttft_ns,omitempty"`
	Total time.Duration `json:"total_ns"`
	// Chunks counts streamed content-delta events observed by the client.
	Chunks int   `json:"chunks,omitempty"`
	Bytes  int64 `json:"bytes"`
	// MaxGap and MeanGap describe inter-chunk pacing (streaming only).
	MaxGap  time.Duration `json:"max_gap_ns,omitempty"`
	MeanGap time.Duration `json:"mean_gap_ns,omitempty"`
	Usage   Usage         `json:"usage"`
}

func (s Sample) ok() bool { return s.Err == "" && s.Status >= 200 && s.Status < 300 }

// NewHTTPClient builds the shared client used for all measurements. One client
// per target keeps connection reuse comparable across targets.
func NewHTTPClient() *http.Client {
	transport := &http.Transport{
		MaxIdleConns:        256,
		MaxIdleConnsPerHost: 64,
		IdleConnTimeout:     90 * time.Second,
		ForceAttemptHTTP2:   true,
	}
	return &http.Client{Transport: transport}
}

func applyAuth(req *http.Request, t Target, p Protocol) {
	if t.APIKey != "" {
		if p == ProtocolAnthropicMessages {
			req.Header.Set("x-api-key", t.APIKey)
		} else {
			req.Header.Set("Authorization", "Bearer "+t.APIKey)
		}
	}
	if p == ProtocolAnthropicMessages {
		req.Header.Set("anthropic-version", "2023-06-01")
	}
	for k, v := range t.Headers {
		req.Header.Set(k, v)
	}
}

// runOne executes a single request against target and measures it.
func runOne(ctx context.Context, client *http.Client, t Target, sc Scenario, body []byte) Sample {
	ctx, cancel := context.WithTimeout(ctx, sc.timeout())
	defer cancel()

	url := t.BaseURL + sc.Protocol.Path()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return Sample{Err: err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	if sc.Stream {
		req.Header.Set("Accept", "text/event-stream")
	} else {
		req.Header.Set("Accept", "application/json")
	}
	applyAuth(req, t, sc.Protocol)

	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return Sample{Err: err.Error(), Total: time.Since(start)}
	}
	defer func() { _ = resp.Body.Close() }()

	s := Sample{Status: resp.StatusCode, TTFB: time.Since(start)}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		buf := make([]byte, 2048)
		n, _ := resp.Body.Read(buf)
		s.Err = fmt.Sprintf("http %d: %s", resp.StatusCode, bytes.TrimSpace(buf[:n]))
		s.Total = time.Since(start)
		return s
	}

	if sc.Stream {
		readStream(resp, sc.Protocol, start, &s)
	} else {
		readNonStream(resp, sc.Protocol, start, &s)
	}
	return s
}

func readNonStream(resp *http.Response, p Protocol, start time.Time, s *Sample) {
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		s.Err = "read body: " + err.Error()
		return
	}
	s.Total = time.Since(start)
	s.Bytes = int64(buf.Len())
	s.Usage = extractUsage(buf.Bytes(), p)
}

// readStream consumes an SSE response line by line, timing the first content
// token and inter-chunk gaps, and extracting usage from terminal events.
func readStream(resp *http.Response, p Protocol, start time.Time, s *Sample) {
	reader := bufio.NewReaderSize(resp.Body, 64*1024)
	var (
		lastChunk time.Time
		gapSum    time.Duration
		gapCount  int64
	)
	for {
		line, err := reader.ReadBytes('\n')
		s.Bytes += int64(len(line))
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) > 0 && bytes.HasPrefix(trimmed, []byte("data:")) {
			data := bytes.TrimSpace(trimmed[len("data:"):])
			if !bytes.Equal(data, []byte("[DONE]")) {
				if isContentDelta(data, p) {
					now := time.Now()
					if s.Chunks == 0 {
						s.TTFT = now.Sub(start)
					} else {
						gap := now.Sub(lastChunk)
						gapSum += gap
						gapCount++
						if gap > s.MaxGap {
							s.MaxGap = gap
						}
					}
					lastChunk = now
					s.Chunks++
				}
				mergeStreamUsage(data, p, &s.Usage)
			}
		}
		if err != nil {
			break
		}
	}
	s.Total = time.Since(start)
	if gapCount > 0 {
		s.MeanGap = time.Duration(int64(gapSum) / gapCount)
	}
}

// isContentDelta reports whether an SSE data payload carries assistant content.
func isContentDelta(data []byte, p Protocol) bool {
	switch p {
	case ProtocolOpenAIChat:
		return gjson.GetBytes(data, "choices.0.delta.content").Exists()
	case ProtocolAnthropicMessages:
		return gjson.GetBytes(data, "type").String() == "content_block_delta"
	case ProtocolOpenAIResponses:
		return gjson.GetBytes(data, "type").String() == "response.output_text.delta"
	}
	return false
}

// mergeStreamUsage accumulates usage counters from streaming events.
func mergeStreamUsage(data []byte, p Protocol, u *Usage) {
	switch p {
	case ProtocolOpenAIChat:
		if usage := gjson.GetBytes(data, "usage"); usage.Exists() && usage.IsObject() {
			*u = openAIUsage(usage)
		}
	case ProtocolAnthropicMessages:
		switch gjson.GetBytes(data, "type").String() {
		case "message_start":
			usage := gjson.GetBytes(data, "message.usage")
			u.PromptTokens = usage.Get("input_tokens").Int()
			u.CachedTokens = usage.Get("cache_read_input_tokens").Int()
			u.CacheWriteTokens = usage.Get("cache_creation_input_tokens").Int()
		case "message_delta":
			u.CompletionTokens = gjson.GetBytes(data, "usage.output_tokens").Int()
		}
	case ProtocolOpenAIResponses:
		if gjson.GetBytes(data, "type").String() == "response.completed" {
			usage := gjson.GetBytes(data, "response.usage")
			u.PromptTokens = usage.Get("input_tokens").Int()
			u.CompletionTokens = usage.Get("output_tokens").Int()
			u.CachedTokens = usage.Get("input_tokens_details.cached_tokens").Int()
		}
	}
}

// extractUsage pulls usage counters from a non-streaming response body.
func extractUsage(body []byte, p Protocol) Usage {
	switch p {
	case ProtocolOpenAIChat:
		return openAIUsage(gjson.GetBytes(body, "usage"))
	case ProtocolAnthropicMessages:
		usage := gjson.GetBytes(body, "usage")
		return Usage{
			PromptTokens:     usage.Get("input_tokens").Int(),
			CompletionTokens: usage.Get("output_tokens").Int(),
			CachedTokens:     usage.Get("cache_read_input_tokens").Int(),
			CacheWriteTokens: usage.Get("cache_creation_input_tokens").Int(),
		}
	case ProtocolOpenAIResponses:
		usage := gjson.GetBytes(body, "usage")
		return Usage{
			PromptTokens:     usage.Get("input_tokens").Int(),
			CompletionTokens: usage.Get("output_tokens").Int(),
			CachedTokens:     usage.Get("input_tokens_details.cached_tokens").Int(),
		}
	}
	return Usage{}
}

func openAIUsage(usage gjson.Result) Usage {
	return Usage{
		PromptTokens:     usage.Get("prompt_tokens").Int(),
		CompletionTokens: usage.Get("completion_tokens").Int(),
		CachedTokens:     usage.Get("prompt_tokens_details.cached_tokens").Int(),
	}
}
