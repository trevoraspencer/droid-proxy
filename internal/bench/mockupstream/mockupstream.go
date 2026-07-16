// Package mockupstream implements a deterministic mock model provider for
// benchmarking and fidelity testing. It speaks OpenAI Chat Completions,
// OpenAI Responses, and Anthropic Messages (streaming and non-streaming),
// simulates provider-side prompt caching, and captures every request body so
// harnesses can assert exactly what a proxy forwarded upstream.
package mockupstream

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tidwall/gjson"
)

// Options controls simulated provider behavior.
type Options struct {
	// TTFT is the simulated model latency before the first byte of the
	// response body (both streaming and non-streaming).
	TTFT time.Duration
	// InterChunkDelay is the simulated inter-token latency between streamed
	// SSE chunks.
	InterChunkDelay time.Duration
	// StreamChunks is the number of content delta chunks emitted per stream.
	StreamChunks int
	// CaptureLimit bounds the in-memory captured request ring. Zero means 512.
	CaptureLimit int
	// SimulatePromptCache enables provider-style prompt-prefix caching: the
	// conversation prefix (everything except the last message) is hashed, and
	// repeat prefixes report cached tokens in usage.
	SimulatePromptCache bool
}

// maxPrefixEntries bounds the simulated prompt-cache map in a long-lived
// mock process.
const maxPrefixEntries = 100_000

func (o Options) withDefaults() Options {
	if o.StreamChunks <= 0 {
		o.StreamChunks = 40
	}
	if o.CaptureLimit <= 0 {
		o.CaptureLimit = 512
	}
	return o
}

// CapturedRequest is one request the mock received, kept for fidelity checks.
type CapturedRequest struct {
	Seq        int               `json:"seq"`
	Method     string            `json:"method"`
	Path       string            `json:"path"`
	Headers    map[string]string `json:"headers"`
	Body       json.RawMessage   `json:"body"`
	ReceivedAt time.Time         `json:"received_at"`
}

// Usage mirrors the cache-relevant usage counters the mock emits.
type Usage struct {
	PromptTokens     int
	CompletionTokens int
	CachedTokens     int
	CacheWriteTokens int
}

// Server is the mock provider.
type Server struct {
	opts Options

	mu       sync.Mutex
	seq      int
	captures []CapturedRequest
	prefixes map[string]int
}

// New builds a mock provider server.
func New(opts Options) *Server {
	return &Server{opts: opts.withDefaults(), prefixes: map[string]int{}}
}

// EstimateTokens is the deterministic token estimator used for usage numbers:
// one token per four bytes, minimum one for non-empty input. Fidelity checks
// reuse it to recompute expected usage from captured request bodies.
func EstimateTokens(b []byte) int {
	if len(b) == 0 {
		return 0
	}
	return len(b)/4 + 1
}

// Handler returns the HTTP handler for the mock provider.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "service": "mock-upstream"})
	})
	mux.HandleFunc("/__mock/requests", s.handleRequests)
	mux.HandleFunc("/__mock/reset", s.handleReset)
	for _, p := range []string{"/v1/chat/completions", "/chat/completions"} {
		mux.HandleFunc(p, s.handleChat)
	}
	for _, p := range []string{"/v1/messages", "/messages"} {
		mux.HandleFunc(p, s.handleMessages)
	}
	for _, p := range []string{"/v1/responses", "/responses"} {
		mux.HandleFunc(p, s.handleResponses)
	}
	for _, p := range []string{"/v1/messages/count_tokens", "/messages/count_tokens"} {
		mux.HandleFunc(p, s.handleCountTokens)
	}
	return mux
}

func (s *Server) handleReset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	s.mu.Lock()
	s.captures = nil
	s.seq = 0
	s.prefixes = map[string]int{}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"reset": true})
}

func (s *Server) handleRequests(w http.ResponseWriter, r *http.Request) {
	since := 0
	if v := r.URL.Query().Get("since"); v != "" {
		since, _ = strconv.Atoi(v)
	}
	s.mu.Lock()
	out := make([]CapturedRequest, 0, len(s.captures))
	for _, c := range s.captures {
		if c.Seq > since {
			out = append(out, c)
		}
	}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"requests": out})
}

func (s *Server) capture(r *http.Request, body []byte) {
	headers := map[string]string{}
	for _, h := range []string{"Content-Type", "Accept", "User-Agent", "Anthropic-Version", "Anthropic-Beta", "X-Request-Id"} {
		if v := r.Header.Get(h); v != "" {
			headers[strings.ToLower(h)] = v
		}
	}
	// Record auth presence without storing credentials.
	if r.Header.Get("Authorization") != "" {
		headers["authorization"] = "present"
	}
	if r.Header.Get("X-Api-Key") != "" {
		headers["x-api-key"] = "present"
	}
	s.mu.Lock()
	s.seq++
	s.captures = append(s.captures, CapturedRequest{
		Seq:        s.seq,
		Method:     r.Method,
		Path:       r.URL.Path,
		Headers:    headers,
		Body:       append([]byte(nil), body...),
		ReceivedAt: time.Now(),
	})
	if len(s.captures) > s.opts.CaptureLimit {
		// Copy into a fresh slice so evicted bodies actually become
		// collectable instead of staying pinned by the old backing array.
		trimmed := make([]CapturedRequest, s.opts.CaptureLimit)
		copy(trimmed, s.captures[len(s.captures)-s.opts.CaptureLimit:])
		s.captures = trimmed
	}
	s.mu.Unlock()
}

func readBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return nil, false
	}
	defer func() { _ = r.Body.Close() }()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return nil, false
	}
	return body, true
}

// usageFor computes deterministic usage counters for a request body. prefixRaws
// are the serialized cache-relevant prefix segments (all but the final message)
// used for simulated prompt caching.
//
// The cache is modeled the way providers match prompt prefixes: each cumulative
// segment prefix is hashed into a chain, the longest previously-seen chain
// entry counts as a cache read, and everything beyond it counts as a cache
// write. A growing conversation therefore hits on the turns it repeats, and
// any byte drift in earlier turns zeroes the hit — exactly the behavior the
// fidelity checks probe for.
func (s *Server) usageFor(body []byte, prefixRaws []string) Usage {
	u := Usage{
		PromptTokens:     EstimateTokens(body),
		CompletionTokens: s.opts.StreamChunks,
	}
	if !s.opts.SimulatePromptCache || len(prefixRaws) == 0 {
		return u
	}
	h := sha256.New()
	chain := make([]string, 0, len(prefixRaws))
	cumTokens := make([]int, 0, len(prefixRaws))
	tokens := 0
	for _, raw := range prefixRaws {
		h.Write([]byte(raw))
		tokens += EstimateTokens([]byte(raw))
		chain = append(chain, hex.EncodeToString(h.Sum(nil)))
		cumTokens = append(cumTokens, tokens)
	}
	s.mu.Lock()
	for i := len(chain) - 1; i >= 0; i-- {
		if cached, ok := s.prefixes[chain[i]]; ok {
			u.CachedTokens = cached
			break
		}
	}
	// Bound the simulated cache so a long-lived mock process cannot grow
	// without limit; a full reset mimics a provider cache eviction.
	if len(s.prefixes) > maxPrefixEntries {
		s.prefixes = map[string]int{}
	}
	for i, key := range chain {
		if _, ok := s.prefixes[key]; !ok {
			s.prefixes[key] = cumTokens[i]
		}
	}
	s.mu.Unlock()
	if written := cumTokens[len(cumTokens)-1] - u.CachedTokens; written > 0 {
		u.CacheWriteTokens = written
	}
	return u
}

// prefixSegments extracts the cache-relevant prefix of a conversation: every
// serialized message except the last, plus any system/instructions segment.
func prefixSegments(body []byte, systemPath, messagesPath string) []string {
	var raws []string
	if systemPath != "" {
		if sys := gjson.GetBytes(body, systemPath); sys.Exists() {
			raws = append(raws, sys.Raw)
		}
	}
	msgs := gjson.GetBytes(body, messagesPath).Array()
	for i := 0; i < len(msgs)-1; i++ {
		raws = append(raws, msgs[i].Raw)
	}
	return raws
}

func completionText(chunks int) string {
	var b strings.Builder
	for i := 0; i < chunks; i++ {
		fmt.Fprintf(&b, "tok%d ", i)
	}
	return strings.TrimSpace(b.String())
}

func (s *Server) sleepTTFT() {
	if s.opts.TTFT > 0 {
		time.Sleep(s.opts.TTFT)
	}
}

func (s *Server) sleepChunk() {
	if s.opts.InterChunkDelay > 0 {
		time.Sleep(s.opts.InterChunkDelay)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

type sseWriter struct {
	w http.ResponseWriter
	f http.Flusher
}

func newSSEWriter(w http.ResponseWriter) (*sseWriter, bool) {
	f, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return nil, false
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	return &sseWriter{w: w, f: f}, true
}

func (s *sseWriter) event(name string, data any) {
	payload, _ := json.Marshal(data)
	if name != "" {
		fmt.Fprintf(s.w, "event: %s\n", name)
	}
	fmt.Fprintf(s.w, "data: %s\n\n", payload)
	s.f.Flush()
}

func (s *sseWriter) raw(line string) {
	fmt.Fprintf(s.w, "%s\n\n", line)
	s.f.Flush()
}

// --- OpenAI Chat Completions ---

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	s.capture(r, body)
	model := gjson.GetBytes(body, "model").String()
	stream := gjson.GetBytes(body, "stream").Bool()
	usage := s.usageFor(body, prefixSegments(body, "", "messages"))

	if !stream {
		s.sleepTTFT()
		writeJSON(w, http.StatusOK, map[string]any{
			"id":      "chatcmpl-mock",
			"object":  "chat.completion",
			"created": time.Now().Unix(),
			"model":   model,
			"choices": []any{map[string]any{
				"index":         0,
				"message":       map[string]any{"role": "assistant", "content": completionText(s.opts.StreamChunks)},
				"finish_reason": "stop",
			}},
			"usage": chatUsage(usage),
		})
		return
	}

	sw, ok := newSSEWriter(w)
	if !ok {
		return
	}
	s.sleepTTFT()
	chunk := func(delta map[string]any, finish any) map[string]any {
		return map[string]any{
			"id":      "chatcmpl-mock",
			"object":  "chat.completion.chunk",
			"created": time.Now().Unix(),
			"model":   model,
			"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}},
		}
	}
	sw.event("", chunk(map[string]any{"role": "assistant"}, nil))
	for i := 0; i < s.opts.StreamChunks; i++ {
		s.sleepChunk()
		sw.event("", chunk(map[string]any{"content": fmt.Sprintf("tok%d ", i)}, nil))
	}
	sw.event("", chunk(map[string]any{}, "stop"))
	final := map[string]any{
		"id":      "chatcmpl-mock",
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []any{},
		"usage":   chatUsage(usage),
	}
	sw.event("", final)
	sw.raw("data: [DONE]")
}

func chatUsage(u Usage) map[string]any {
	return map[string]any{
		"prompt_tokens":     u.PromptTokens,
		"completion_tokens": u.CompletionTokens,
		"total_tokens":      u.PromptTokens + u.CompletionTokens,
		"prompt_tokens_details": map[string]any{
			"cached_tokens": u.CachedTokens,
		},
	}
}

// --- Anthropic Messages ---

func (s *Server) handleMessages(w http.ResponseWriter, r *http.Request) {
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	s.capture(r, body)
	model := gjson.GetBytes(body, "model").String()
	stream := gjson.GetBytes(body, "stream").Bool()
	usage := s.usageFor(body, prefixSegments(body, "system", "messages"))

	if !stream {
		s.sleepTTFT()
		writeJSON(w, http.StatusOK, map[string]any{
			"id":            "msg_mock",
			"type":          "message",
			"role":          "assistant",
			"model":         model,
			"content":       []any{map[string]any{"type": "text", "text": completionText(s.opts.StreamChunks)}},
			"stop_reason":   "end_turn",
			"stop_sequence": nil,
			"usage":         anthropicUsage(usage),
		})
		return
	}

	sw, ok := newSSEWriter(w)
	if !ok {
		return
	}
	s.sleepTTFT()
	sw.event("message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id": "msg_mock", "type": "message", "role": "assistant", "model": model,
			"content": []any{}, "stop_reason": nil,
			"usage": anthropicUsage(Usage{PromptTokens: usage.PromptTokens, CachedTokens: usage.CachedTokens, CacheWriteTokens: usage.CacheWriteTokens}),
		},
	})
	sw.event("content_block_start", map[string]any{
		"type": "content_block_start", "index": 0,
		"content_block": map[string]any{"type": "text", "text": ""},
	})
	for i := 0; i < s.opts.StreamChunks; i++ {
		s.sleepChunk()
		sw.event("content_block_delta", map[string]any{
			"type": "content_block_delta", "index": 0,
			"delta": map[string]any{"type": "text_delta", "text": fmt.Sprintf("tok%d ", i)},
		})
	}
	sw.event("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
	sw.event("message_delta", map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": "end_turn", "stop_sequence": nil},
		"usage": map[string]any{"output_tokens": usage.CompletionTokens},
	})
	sw.event("message_stop", map[string]any{"type": "message_stop"})
}

func anthropicUsage(u Usage) map[string]any {
	return map[string]any{
		"input_tokens":                u.PromptTokens,
		"output_tokens":               u.CompletionTokens,
		"cache_read_input_tokens":     u.CachedTokens,
		"cache_creation_input_tokens": u.CacheWriteTokens,
	}
}

// --- OpenAI Responses ---

func (s *Server) handleResponses(w http.ResponseWriter, r *http.Request) {
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	s.capture(r, body)
	model := gjson.GetBytes(body, "model").String()
	stream := gjson.GetBytes(body, "stream").Bool()
	usage := s.usageFor(body, prefixSegments(body, "instructions", "input"))

	buildResponse := func(status string) map[string]any {
		return map[string]any{
			"id":     "resp_mock",
			"object": "response",
			"status": status,
			"model":  model,
			"output": []any{map[string]any{
				"type": "message", "id": "msg_mock", "role": "assistant", "status": "completed",
				"content": []any{map[string]any{"type": "output_text", "text": completionText(s.opts.StreamChunks), "annotations": []any{}}},
			}},
			"usage": map[string]any{
				"input_tokens":  usage.PromptTokens,
				"output_tokens": usage.CompletionTokens,
				"total_tokens":  usage.PromptTokens + usage.CompletionTokens,
				"input_tokens_details": map[string]any{
					"cached_tokens": usage.CachedTokens,
				},
			},
		}
	}

	if !stream {
		s.sleepTTFT()
		writeJSON(w, http.StatusOK, buildResponse("completed"))
		return
	}

	sw, ok := newSSEWriter(w)
	if !ok {
		return
	}
	s.sleepTTFT()
	seq := 0
	next := func() int { seq++; return seq }
	sw.event("response.created", map[string]any{
		"type": "response.created", "sequence_number": next(), "response": buildResponse("in_progress"),
	})
	sw.event("response.output_item.added", map[string]any{
		"type": "response.output_item.added", "sequence_number": next(), "output_index": 0,
		"item": map[string]any{"type": "message", "id": "msg_mock", "role": "assistant", "status": "in_progress", "content": []any{}},
	})
	for i := 0; i < s.opts.StreamChunks; i++ {
		s.sleepChunk()
		sw.event("response.output_text.delta", map[string]any{
			"type": "response.output_text.delta", "sequence_number": next(),
			"item_id": "msg_mock", "output_index": 0, "content_index": 0,
			"delta": fmt.Sprintf("tok%d ", i),
		})
	}
	sw.event("response.output_item.done", map[string]any{
		"type": "response.output_item.done", "sequence_number": next(), "output_index": 0,
		"item": map[string]any{
			"type": "message", "id": "msg_mock", "role": "assistant", "status": "completed",
			"content": []any{map[string]any{"type": "output_text", "text": completionText(s.opts.StreamChunks), "annotations": []any{}}},
		},
	})
	sw.event("response.completed", map[string]any{
		"type": "response.completed", "sequence_number": next(), "response": buildResponse("completed"),
	})
}

// --- Anthropic token counting ---

func (s *Server) handleCountTokens(w http.ResponseWriter, r *http.Request) {
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	s.capture(r, body)
	writeJSON(w, http.StatusOK, map[string]any{"input_tokens": EstimateTokens(body)})
}
