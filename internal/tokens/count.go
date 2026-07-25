// Package tokens provides a local tokenizer fallback for endpoints that count tokens
// against a non-Anthropic upstream. It uses tiktoken's cl100k_base by default,
// which approximates Anthropic and OpenAI token counts closely enough for client
// display purposes.
package tokens

import (
	"strings"
	"sync"

	"github.com/tiktoken-go/tokenizer"
)

var (
	once   sync.Once
	codec  tokenizer.Codec
	codecE error
)

// Count returns an approximate token count for text using cl100k_base.
// Empty strings count as zero. Errors loading the tokenizer surface to the caller.
func Count(text string) (int, error) {
	if text == "" {
		return 0, nil
	}
	once.Do(func() {
		codec, codecE = tokenizer.Get(tokenizer.Cl100kBase)
	})
	if codecE != nil {
		return 0, codecE
	}
	ids, _, err := codec.Encode(text)
	if err != nil {
		return 0, err
	}
	return len(ids), nil
}

// CountChatMessages sums tokens across a simple list of role+content strings.
// Each message contributes a small per-message overhead (4 tokens) to roughly
// approximate OpenAI's documented counting rules.
func CountChatMessages(messages []ChatMessage) (int, error) {
	total := 0
	for _, m := range messages {
		n, err := Count(strings.TrimSpace(m.Role + " " + m.Content))
		if err != nil {
			return 0, err
		}
		total += n + 4
	}
	if total > 0 {
		total += 2 // every reply primed with <assistant>
	}
	return total, nil
}

// ChatMessage is the minimal subset needed for counting.
type ChatMessage struct {
	Role    string
	Content string
}
