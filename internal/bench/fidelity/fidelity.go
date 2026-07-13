// Package fidelity verifies the properties of a proxy that make provider-side
// prompt caching and usage accounting work, by comparing what a client sent
// with what the mock upstream actually received and what the client got back.
//
// The checks encode the failure modes that silently destroy caching or
// observability in LLM proxies:
//
//   - upstream request bodies must be deterministic (same client request →
//     byte-identical upstream request), or implicit prefix caches and request
//     dedup keyed on body bytes can miss;
//   - conversation prefixes must be byte-stable as turns are appended, or
//     provider prompt caches (Anthropic explicit, OpenAI/DeepSeek implicit)
//     never hit past turn one;
//   - cache_control, prompt_cache_key, and stream_options must survive the
//     proxy unchanged;
//   - usage counters (cached/prompt/completion tokens) must round-trip to the
//     client so cache behavior stays observable in Droid.
package fidelity

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	"github.com/trevoraspencer/droid-proxy/internal/bench/mockupstream"
)

// Options configures a fidelity run.
type Options struct {
	// ProxyBase is the base URL of the proxy under test.
	ProxyBase string
	// MockBase is the base URL of a droid-bench mock upstream that the proxy's
	// model aliases point at (capture enabled).
	MockBase string
	// ChatModel is a proxy model alias for factory_provider generic/openai with
	// upstream_protocol openai-chat. Empty skips chat checks.
	ChatModel string
	// AnthropicModel is a proxy alias for anthropic → anthropic-messages
	// (native passthrough). Empty skips those checks.
	AnthropicModel string
	// AnthropicTranslatedModel is a proxy alias for anthropic → openai-chat
	// (T3 translation). Empty skips those checks.
	AnthropicTranslatedModel string
	// ClientAPIKey is sent to the proxy when its client_auth is enabled.
	ClientAPIKey string
	// Repeats for the determinism check. Defaults to 3.
	Repeats int
}

// CheckResult is one named assertion outcome.
type CheckResult struct {
	Name   string `json:"name"`
	Pass   bool   `json:"pass"`
	Detail string `json:"detail"`
}

type runner struct {
	opts    Options
	client  *http.Client
	lastSeq int
	results []CheckResult
}

// Run executes all applicable checks and returns their results.
func Run(ctx context.Context, opts Options) ([]CheckResult, error) {
	if opts.Repeats <= 0 {
		opts.Repeats = 3
	}
	opts.ProxyBase = strings.TrimRight(opts.ProxyBase, "/")
	opts.MockBase = strings.TrimRight(opts.MockBase, "/")
	r := &runner{opts: opts, client: &http.Client{Timeout: 60 * time.Second}}
	if err := r.resetMock(ctx); err != nil {
		return nil, fmt.Errorf("mock upstream not reachable at %s: %w", opts.MockBase, err)
	}
	if opts.ChatModel != "" {
		r.checkChatPassthrough(ctx)
		r.checkChatDeterminism(ctx)
		r.checkPrefixStability(ctx, "chat", "/v1/chat/completions", opts.ChatModel, buildChatTurn)
		r.checkChatUsagePassthrough(ctx)
		r.checkChatStreamIntegrity(ctx)
	}
	if opts.AnthropicModel != "" {
		r.checkAnthropicCacheControl(ctx)
		r.checkPrefixStability(ctx, "anthropic-native", "/v1/messages", opts.AnthropicModel, buildAnthropicTurn)
		r.checkAnthropicUsagePassthrough(ctx)
		r.checkAnthropicStreamIntegrity(ctx)
	}
	if opts.AnthropicTranslatedModel != "" {
		r.checkPrefixStability(ctx, "anthropic-translated", "/v1/messages", opts.AnthropicTranslatedModel, buildAnthropicTurn)
		r.checkTranslatedDeterminism(ctx)
	}
	return r.results, nil
}

// Passed reports whether every check passed.
func Passed(results []CheckResult) bool {
	for _, r := range results {
		if !r.Pass {
			return false
		}
	}
	return len(results) > 0
}

// Print writes PASS/FAIL lines to w.
func Print(w io.Writer, results []CheckResult) {
	for _, r := range results {
		status := "PASS"
		if !r.Pass {
			status = "FAIL"
		}
		fmt.Fprintf(w, "[fidelity] %s: %s — %s\n", status, r.Name, r.Detail)
	}
}

func (r *runner) add(name string, pass bool, detail string) {
	r.results = append(r.results, CheckResult{Name: name, Pass: pass, Detail: detail})
}

// --- mock control ---

func (r *runner) resetMock(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.opts.MockBase+"/__mock/reset", nil)
	if err != nil {
		return err
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	r.lastSeq = 0
	return nil
}

// captured returns requests the mock received since the previous call.
func (r *runner) captured(ctx context.Context) ([]mockupstream.CapturedRequest, error) {
	url := fmt.Sprintf("%s/__mock/requests?since=%d", r.opts.MockBase, r.lastSeq)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var payload struct {
		Requests []mockupstream.CapturedRequest `json:"requests"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	if n := len(payload.Requests); n > 0 {
		r.lastSeq = payload.Requests[n-1].Seq
	}
	return payload.Requests, nil
}

// --- proxy calls ---

func (r *runner) proxyPost(ctx context.Context, path string, body []byte, stream bool) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.opts.ProxyBase+path, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if stream {
		req.Header.Set("Accept", "text/event-stream")
	}
	if r.opts.ClientAPIKey != "" {
		req.Header.Set("Authorization", "Bearer "+r.opts.ClientAPIKey)
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(bufio.NewReader(resp.Body))
	return resp.StatusCode, raw, err
}

// oneUpstream sends body via the proxy and returns the single upstream request
// the mock captured for it.
func (r *runner) oneUpstream(ctx context.Context, path string, body []byte, stream bool) (mockupstream.CapturedRequest, []byte, error) {
	status, respBody, err := r.proxyPost(ctx, path, body, stream)
	if err != nil {
		return mockupstream.CapturedRequest{}, nil, err
	}
	if status != http.StatusOK {
		return mockupstream.CapturedRequest{}, nil, fmt.Errorf("proxy returned %d: %s", status, truncate(respBody, 300))
	}
	caps, err := r.captured(ctx)
	if err != nil {
		return mockupstream.CapturedRequest{}, nil, err
	}
	if len(caps) != 1 {
		return mockupstream.CapturedRequest{}, nil, fmt.Errorf("expected 1 upstream request, mock saw %d", len(caps))
	}
	return caps[0], respBody, nil
}

func truncate(b []byte, n int) string {
	s := strings.TrimSpace(string(b))
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// --- request builders (deterministic, Droid-shaped) ---

const systemText = "You are Droid, an agentic software engineering assistant. Follow the workspace rules precisely and prefer minimal diffs."

// buildChatTurn builds an OpenAI chat request with `turns` completed user/
// assistant exchanges plus a final user message.
func buildChatTurn(model string, turns int, stream bool) []byte {
	messages := []any{map[string]any{"role": "system", "content": systemText}}
	for i := 0; i < turns; i++ {
		messages = append(messages,
			map[string]any{"role": "user", "content": fmt.Sprintf("turn %d: list the files in module %d", i, i)},
			map[string]any{"role": "assistant", "content": fmt.Sprintf("turn %d: the module contains main.go and main_test.go", i)},
		)
	}
	messages = append(messages, map[string]any{"role": "user", "content": "final: run the tests and summarize failures"})
	body := map[string]any{
		"model":      model,
		"messages":   messages,
		"max_tokens": 64,
	}
	if stream {
		body["stream"] = true
		body["stream_options"] = map[string]any{"include_usage": true}
	}
	raw, _ := json.Marshal(body)
	return raw
}

// buildAnthropicTurn builds an Anthropic Messages request with cache_control
// markers on the system block.
func buildAnthropicTurn(model string, turns int, stream bool) []byte {
	var messages []any
	for i := 0; i < turns; i++ {
		messages = append(messages,
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "text", "text": fmt.Sprintf("turn %d: list the files in module %d", i, i)},
			}},
			map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "text", "text": fmt.Sprintf("turn %d: the module contains main.go and main_test.go", i)},
			}},
		)
	}
	messages = append(messages, map[string]any{"role": "user", "content": []any{
		map[string]any{"type": "text", "text": "final: run the tests and summarize failures"},
	}})
	body := map[string]any{
		"model": model,
		"system": []any{map[string]any{
			"type": "text", "text": systemText,
			"cache_control": map[string]any{"type": "ephemeral"},
		}},
		"messages":   messages,
		"max_tokens": 64,
	}
	if stream {
		body["stream"] = true
	}
	raw, _ := json.Marshal(body)
	return raw
}

// --- checks ---

// checkChatPassthrough: every field except `model` must reach the upstream
// byte-for-byte, including cache-relevant fields the proxy does not interpret.
func (r *runner) checkChatPassthrough(ctx context.Context) {
	const name = "chat-field-passthrough"
	body := buildChatTurn(r.opts.ChatModel, 2, false)
	// Add cache-relevant and unknown fields a well-behaved proxy must preserve.
	augmented := map[string]any{}
	_ = json.Unmarshal(body, &augmented)
	augmented["temperature"] = 0.2
	augmented["prompt_cache_key"] = "pck-droid-bench-1"
	augmented["x_droid_bench_custom"] = map[string]any{"keep": true}
	raw, _ := json.Marshal(augmented)

	capture, _, err := r.oneUpstream(ctx, "/v1/chat/completions", raw, false)
	if err != nil {
		r.add(name, false, err.Error())
		return
	}
	var missing []string
	for _, key := range []string{"messages", "max_tokens", "temperature", "prompt_cache_key", "x_droid_bench_custom"} {
		want := gjson.GetBytes(raw, key).Raw
		got := gjson.GetBytes(capture.Body, key).Raw
		if want != got {
			missing = append(missing, fmt.Sprintf("%s (want %s, got %s)", key, truncate([]byte(want), 60), truncate([]byte(got), 60)))
		}
	}
	if len(missing) > 0 {
		r.add(name, false, "fields altered or dropped upstream: "+strings.Join(missing, "; "))
		return
	}
	r.add(name, true, "messages, sampling params, prompt_cache_key, and unknown fields reached upstream byte-identical")
}

// checkChatDeterminism: N identical client requests must produce N
// byte-identical upstream bodies. Nondeterministic serialization (e.g. map
// iteration order when applying extra_args) breaks body-keyed dedup/caching
// and makes upstream requests impossible to diff.
func (r *runner) checkChatDeterminism(ctx context.Context) {
	const name = "chat-upstream-determinism"
	body := buildChatTurn(r.opts.ChatModel, 3, false)
	var first []byte
	for i := 0; i < r.opts.Repeats; i++ {
		capture, _, err := r.oneUpstream(ctx, "/v1/chat/completions", body, false)
		if err != nil {
			r.add(name, false, err.Error())
			return
		}
		if i == 0 {
			first = capture.Body
			continue
		}
		if !bytes.Equal(first, capture.Body) {
			r.add(name, false, fmt.Sprintf("repeat %d produced different upstream bytes (extra_args applied in nondeterministic order?)", i+1))
			return
		}
	}
	r.add(name, true, fmt.Sprintf("%d identical requests produced byte-identical upstream bodies", r.opts.Repeats))
}

// checkTranslatedDeterminism: the anthropic→chat translation must be
// deterministic as well.
func (r *runner) checkTranslatedDeterminism(ctx context.Context) {
	const name = "translated-upstream-determinism"
	body := buildAnthropicTurn(r.opts.AnthropicTranslatedModel, 3, false)
	var first []byte
	for i := 0; i < r.opts.Repeats; i++ {
		capture, _, err := r.oneUpstream(ctx, "/v1/messages", body, false)
		if err != nil {
			r.add(name, false, err.Error())
			return
		}
		if i == 0 {
			first = capture.Body
			continue
		}
		if !bytes.Equal(first, capture.Body) {
			r.add(name, false, fmt.Sprintf("repeat %d produced different upstream bytes", i+1))
			return
		}
	}
	r.add(name, true, fmt.Sprintf("%d identical translated requests produced byte-identical upstream bodies", r.opts.Repeats))
}

// checkAnthropicCacheControl: cache_control blocks must survive the native
// anthropic passthrough unchanged — they are how Droid engages Anthropic's
// prompt cache.
func (r *runner) checkAnthropicCacheControl(ctx context.Context) {
	const name = "anthropic-cache-control-passthrough"
	body := buildAnthropicTurn(r.opts.AnthropicModel, 2, false)
	capture, _, err := r.oneUpstream(ctx, "/v1/messages", body, false)
	if err != nil {
		r.add(name, false, err.Error())
		return
	}
	want := gjson.GetBytes(body, "system.0.cache_control").Raw
	got := gjson.GetBytes(capture.Body, "system.0.cache_control").Raw
	if want == "" {
		r.add(name, false, "test bug: request lacked cache_control")
		return
	}
	if want != got {
		r.add(name, false, fmt.Sprintf("system cache_control changed upstream: want %s, got %s", want, truncate([]byte(got), 80)))
		return
	}
	if sys := gjson.GetBytes(capture.Body, "system").Raw; sys != gjson.GetBytes(body, "system").Raw {
		r.add(name, false, "system block bytes changed through the proxy")
		return
	}
	r.add(name, true, "cache_control and system block bytes preserved through native passthrough")
}

// checkPrefixStability: as a conversation grows, the serialized form of
// earlier messages must stay byte-identical upstream. Prefix drift silently
// zeroes provider cache hits on every turn of an agent session.
func (r *runner) checkPrefixStability(ctx context.Context, label, path, model string, build func(string, int, bool) []byte) {
	name := label + "-prefix-stability"
	turnsA, turnsB := 2, 4
	capA, _, err := r.oneUpstream(ctx, path, build(model, turnsA, false), false)
	if err != nil {
		r.add(name, false, err.Error())
		return
	}
	capB, _, err := r.oneUpstream(ctx, path, build(model, turnsB, false), false)
	if err != nil {
		r.add(name, false, err.Error())
		return
	}
	msgsA := gjson.GetBytes(capA.Body, "messages").Array()
	msgsB := gjson.GetBytes(capB.Body, "messages").Array()
	if len(msgsA) == 0 || len(msgsB) <= len(msgsA) {
		r.add(name, false, fmt.Sprintf("unexpected message counts upstream: %d then %d", len(msgsA), len(msgsB)))
		return
	}
	// All shared turns except the final user message of request A must be
	// byte-identical in request B.
	shared := len(msgsA) - 1
	for i := 0; i < shared; i++ {
		if msgsA[i].Raw != msgsB[i].Raw {
			r.add(name, false, fmt.Sprintf("message %d changed bytes between turns: %s → %s",
				i, truncate([]byte(msgsA[i].Raw), 80), truncate([]byte(msgsB[i].Raw), 80)))
			return
		}
	}
	if sysA, sysB := gjson.GetBytes(capA.Body, "system").Raw, gjson.GetBytes(capB.Body, "system").Raw; sysA != sysB {
		r.add(name, false, "system segment changed bytes between turns")
		return
	}
	r.add(name, true, fmt.Sprintf("conversation prefix (%d messages) stayed byte-stable as turns were appended", shared))
}

// checkChatUsagePassthrough: cached-token accounting must reach the client
// unchanged (non-streaming).
func (r *runner) checkChatUsagePassthrough(ctx context.Context) {
	const name = "chat-usage-passthrough"
	body := buildChatTurn(r.opts.ChatModel, 2, false)
	// Prime the mock's simulated prompt cache, then measure the repeat.
	if _, _, err := r.oneUpstream(ctx, "/v1/chat/completions", body, false); err != nil {
		r.add(name, false, err.Error())
		return
	}
	capture, respBody, err := r.oneUpstream(ctx, "/v1/chat/completions", body, false)
	if err != nil {
		r.add(name, false, err.Error())
		return
	}
	cached := gjson.GetBytes(respBody, "usage.prompt_tokens_details.cached_tokens")
	if !cached.Exists() {
		r.add(name, false, "usage.prompt_tokens_details.cached_tokens missing from client response")
		return
	}
	if cached.Int() <= 0 {
		r.add(name, false, "repeat request reported zero cached tokens — proxy altered the cacheable prefix")
		return
	}
	prompt := gjson.GetBytes(respBody, "usage.prompt_tokens").Int()
	expect := int64(mockupstream.EstimateTokens(capture.Body))
	if prompt != expect {
		r.add(name, false, fmt.Sprintf("usage.prompt_tokens altered in flight: upstream computed %d, client saw %d", expect, prompt))
		return
	}
	r.add(name, true, fmt.Sprintf("cached_tokens (%d) and prompt_tokens round-tripped to the client", cached.Int()))
}

// checkAnthropicUsagePassthrough mirrors the chat usage check for the
// Anthropic cache counters.
func (r *runner) checkAnthropicUsagePassthrough(ctx context.Context) {
	const name = "anthropic-usage-passthrough"
	body := buildAnthropicTurn(r.opts.AnthropicModel, 2, false)
	if _, _, err := r.oneUpstream(ctx, "/v1/messages", body, false); err != nil {
		r.add(name, false, err.Error())
		return
	}
	_, respBody, err := r.oneUpstream(ctx, "/v1/messages", body, false)
	if err != nil {
		r.add(name, false, err.Error())
		return
	}
	cached := gjson.GetBytes(respBody, "usage.cache_read_input_tokens")
	if !cached.Exists() {
		r.add(name, false, "usage.cache_read_input_tokens missing from client response")
		return
	}
	if cached.Int() <= 0 {
		r.add(name, false, "repeat request reported zero cache_read_input_tokens — proxy altered the cacheable prefix")
		return
	}
	r.add(name, true, fmt.Sprintf("cache_read_input_tokens (%d) round-tripped to the client", cached.Int()))
}

// checkChatStreamIntegrity: streamed chunks must arrive complete, in order,
// with the terminal [DONE] marker and usage frame intact.
func (r *runner) checkChatStreamIntegrity(ctx context.Context) {
	const name = "chat-stream-integrity"
	body := buildChatTurn(r.opts.ChatModel, 1, true)
	status, respBody, err := r.proxyPost(ctx, "/v1/chat/completions", body, true)
	if err != nil {
		r.add(name, false, err.Error())
		return
	}
	if status != http.StatusOK {
		r.add(name, false, fmt.Sprintf("proxy returned %d", status))
		return
	}
	if _, err := r.captured(ctx); err != nil {
		r.add(name, false, err.Error())
		return
	}
	var contentChunks int
	var sawDone, sawUsage bool
	var tokens []string
	for _, line := range bytes.Split(respBody, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		data := bytes.TrimSpace(line[len("data:"):])
		if bytes.Equal(data, []byte("[DONE]")) {
			sawDone = true
			continue
		}
		if c := gjson.GetBytes(data, "choices.0.delta.content"); c.Exists() {
			contentChunks++
			tokens = append(tokens, c.String())
		}
		if gjson.GetBytes(data, "usage").IsObject() {
			sawUsage = true
		}
	}
	if !sawDone {
		r.add(name, false, "terminal [DONE] frame missing from proxied stream")
		return
	}
	if !sawUsage {
		r.add(name, false, "usage frame missing from proxied stream")
		return
	}
	for i, tok := range tokens {
		if want := fmt.Sprintf("tok%d ", i); tok != want {
			r.add(name, false, fmt.Sprintf("chunk %d out of order or altered: got %q want %q", i, tok, want))
			return
		}
	}
	r.add(name, true, fmt.Sprintf("%d content chunks in order, usage frame and [DONE] preserved", contentChunks))
}

// checkAnthropicStreamIntegrity validates the anthropic SSE event sequence
// through the proxy.
func (r *runner) checkAnthropicStreamIntegrity(ctx context.Context) {
	const name = "anthropic-stream-integrity"
	body := buildAnthropicTurn(r.opts.AnthropicModel, 1, true)
	status, respBody, err := r.proxyPost(ctx, "/v1/messages", body, true)
	if err != nil {
		r.add(name, false, err.Error())
		return
	}
	if status != http.StatusOK {
		r.add(name, false, fmt.Sprintf("proxy returned %d", status))
		return
	}
	if _, err := r.captured(ctx); err != nil {
		r.add(name, false, err.Error())
		return
	}
	var deltas int
	var sawStart, sawStop bool
	for _, line := range bytes.Split(respBody, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		data := bytes.TrimSpace(line[len("data:"):])
		switch gjson.GetBytes(data, "type").String() {
		case "message_start":
			sawStart = true
		case "content_block_delta":
			deltas++
		case "message_stop":
			sawStop = true
		}
	}
	if !sawStart || !sawStop {
		r.add(name, false, fmt.Sprintf("event sequence incomplete: message_start=%v message_stop=%v", sawStart, sawStop))
		return
	}
	if deltas == 0 {
		r.add(name, false, "no content_block_delta events observed")
		return
	}
	r.add(name, true, fmt.Sprintf("message_start … %d deltas … message_stop preserved through proxy", deltas))
}
