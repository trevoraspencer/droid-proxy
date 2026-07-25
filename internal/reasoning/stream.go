package reasoning

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"
)

// StreamCapture accumulates reasoning + tool_calls from an SSE stream so that
// when the stream completes the reasoning can be cached for replay on the next turn.
type StreamCapture struct {
	scope     Scope
	cache     *Cache
	choices   map[int]*streamChoice
	failed    bool
	committed bool
}

type streamChoice struct {
	reasoning strings.Builder
	content   strings.Builder
	tools     map[int]*streamToolCall
	failed    bool
}

type streamToolCall struct {
	id        strings.Builder
	typ       strings.Builder
	name      strings.Builder
	arguments strings.Builder
	idSeen    bool
	nameSeen  bool
	argsSeen  bool
}

// NewStreamCapture creates a fresh capture bound to scope+cache. Returns nil
// when cache is nil so callers can no-op without a nil check.
func NewStreamCapture(scope Scope, cache *Cache) *StreamCapture {
	if cache == nil {
		return nil
	}
	return &StreamCapture{scope: scope, cache: cache, choices: make(map[int]*streamChoice)}
}

// ObserveLine is called with each SSE line read from the upstream. Non-data lines
// are ignored. Callers must invoke Commit only after the surrounding stream
// forwarder has verified a valid terminal completion and all downstream writes
// succeeded. In particular, observing [DONE] alone is not sufficient because
// the terminal frame may still fail to reach the downstream client.
func (c *StreamCapture) ObserveLine(line []byte) {
	if c == nil || c.failed || c.committed {
		return
	}
	trimmed := bytes.TrimSpace(line)
	if !bytes.HasPrefix(trimmed, []byte("data:")) {
		return
	}
	data := bytes.TrimSpace(bytes.TrimPrefix(trimmed, []byte("data:")))
	if len(data) == 0 {
		return
	}
	if bytes.Equal(data, []byte("[DONE]")) {
		return
	}
	var root map[string]any
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&root); err != nil {
		c.failed = true
		return
	}
	if _, has := root["error"]; has {
		c.failed = true
		return
	}
	choices, ok := root["choices"].([]any)
	if !ok || len(choices) == 0 {
		return
	}
	for _, rc := range choices {
		choice, ok := rc.(map[string]any)
		if !ok {
			c.failed = true
			return
		}
		ix, ok := numberIndex(choice["index"])
		if !ok || ix < 0 {
			c.failed = true
			return
		}
		delta, ok := choice["delta"].(map[string]any)
		if !ok {
			continue
		}
		state := c.choice(ix)
		if rr, ex := delta["reasoning_content"]; ex {
			if rs, ok := rr.(string); ok {
				state.reasoning.WriteString(rs)
			} else {
				state.failed = true
			}
		}
		if cs, ok := delta["content"].(string); ok {
			state.content.WriteString(cs)
		}
		rawTools, ok := delta["tool_calls"].([]any)
		if !ok {
			continue
		}
		for _, rt := range rawTools {
			tool, ok := rt.(map[string]any)
			if !ok {
				state.failed = true
				continue
			}
			ti, ok := numberIndex(tool["index"])
			if !ok || ti < 0 {
				state.failed = true
				continue
			}
			tstate := state.tool(ti)
			if id, ok := tool["id"].(string); ok {
				tstate.id.WriteString(id)
				tstate.idSeen = true
			}
			if typ, ok := tool["type"].(string); ok {
				tstate.typ.WriteString(typ)
			}
			if fn, ok := tool["function"].(map[string]any); ok {
				if name, ok := fn["name"].(string); ok {
					tstate.name.WriteString(name)
					tstate.nameSeen = true
				}
				if args, ok := fn["arguments"].(string); ok {
					tstate.arguments.WriteString(args)
					tstate.argsSeen = true
				}
			}
		}
	}
}

// Commit assembles the captured tool_calls into a message and stores reasoning
// under the appropriate key. Idempotent.
func (c *StreamCapture) Commit() {
	if c == nil || c.failed || c.committed {
		return
	}
	c.committed = true
	for _, choice := range c.choices {
		if choice == nil || choice.failed || strings.TrimSpace(choice.reasoning.String()) == "" || len(choice.tools) == 0 {
			continue
		}
		indexes := make([]int, 0, len(choice.tools))
		for i := range choice.tools {
			indexes = append(indexes, i)
		}
		sort.Ints(indexes)
		toolCalls := make([]any, 0, len(indexes))
		for _, i := range indexes {
			t := choice.tools[i]
			if t == nil || !t.idSeen || !t.nameSeen || !t.argsSeen ||
				strings.TrimSpace(t.id.String()) == "" || strings.TrimSpace(t.name.String()) == "" {
				toolCalls = nil
				break
			}
			ttype := t.typ.String()
			if ttype == "" {
				ttype = "function"
			}
			toolCalls = append(toolCalls, map[string]any{
				"id":   t.id.String(),
				"type": ttype,
				"function": map[string]any{
					"name":      t.name.String(),
					"arguments": t.arguments.String(),
				},
			})
		}
		if len(toolCalls) == 0 {
			continue
		}
		c.cache.Store(KeyForMessage(c.scope, map[string]any{
			"role":       "assistant",
			"content":    choice.content.String(),
			"tool_calls": toolCalls,
		}), choice.reasoning.String())
	}
}

func (c *StreamCapture) choice(index int) *streamChoice {
	if cc := c.choices[index]; cc != nil {
		return cc
	}
	cc := &streamChoice{tools: make(map[int]*streamToolCall)}
	c.choices[index] = cc
	return cc
}

func (c *streamChoice) tool(index int) *streamToolCall {
	if t := c.tools[index]; t != nil {
		return t
	}
	t := &streamToolCall{}
	c.tools[index] = t
	return t
}

func numberIndex(raw any) (int, bool) {
	switch v := raw.(type) {
	case json.Number:
		i, err := v.Int64()
		if err != nil {
			return 0, false
		}
		return int(i), true
	case float64:
		i := int(v)
		return i, float64(i) == v
	case int:
		return v, true
	}
	return 0, false
}
