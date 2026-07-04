package reasoning

import (
	"net/http"
	"testing"
)

func TestSessionID_HeaderPriority(t *testing.T) {
	h := http.Header{}
	h.Set("X-Conversation-Id", "conv-abc")
	if got := SessionID(h, nil); got != "conv-abc" {
		t.Errorf("expected conv-abc, got %q", got)
	}
}

func TestSessionID_ConversationFromPayload(t *testing.T) {
	got := SessionID(http.Header{}, []byte(`{"conversation_id":"thread-xyz"}`))
	if got != "thread-xyz" {
		t.Errorf("got %q", got)
	}
}

func TestSessionID_ConversationPrecedesUserMetadata(t *testing.T) {
	got := SessionID(http.Header{}, []byte(`{"conversation_id":"thread-xyz","metadata":{"user_id":"u-1","conversation_id":"metadata-thread"}}`))
	if got != "thread-xyz" {
		t.Errorf("got %q", got)
	}
}

func TestSessionID_MetadataConversation(t *testing.T) {
	got := SessionID(http.Header{}, []byte(`{"metadata":{"user_id":"u-1","conversation_id":"metadata-thread"}}`))
	if got != "metadata-thread" {
		t.Errorf("got %q", got)
	}
}

func TestSessionID_IgnoresWeakUserMetadata(t *testing.T) {
	got := SessionID(http.Header{}, []byte(`{"metadata":{"user_id":"u-1"}}`))
	if got != "" {
		t.Errorf("expected empty for user-only metadata, got %q", got)
	}
}

func TestSessionID_IgnoresFirstMessageHash(t *testing.T) {
	payload := []byte(`{"messages":[{"role":"user","content":"hello there"}]}`)
	got := SessionID(http.Header{}, payload)
	if got != "" {
		t.Fatalf("expected empty for prompt-only weak signal, got %q", got)
	}
}

func TestSessionID_EmptyWhenNoSignal(t *testing.T) {
	if got := SessionID(http.Header{}, []byte(`{}`)); got != "" {
		t.Errorf("expected empty for no signal, got %q", got)
	}
}
