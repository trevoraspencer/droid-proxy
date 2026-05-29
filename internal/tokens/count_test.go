package tokens

import "testing"

func TestCount_NonZeroForRealText(t *testing.T) {
	n, err := Count("Hello, world!")
	if err != nil {
		t.Fatal(err)
	}
	if n <= 0 {
		t.Errorf("expected >0 tokens, got %d", n)
	}
}

func TestCount_EmptyReturnsZero(t *testing.T) {
	n, err := Count("")
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("expected 0, got %d", n)
	}
}

func TestCountChatMessages(t *testing.T) {
	msgs := []ChatMessage{
		{Role: "user", Content: "Hi"},
		{Role: "assistant", Content: "Hello."},
	}
	n, err := CountChatMessages(msgs)
	if err != nil {
		t.Fatal(err)
	}
	if n <= 0 {
		t.Errorf("expected >0, got %d", n)
	}
}

func TestCountChatMessages_Empty(t *testing.T) {
	n, err := CountChatMessages(nil)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("expected 0, got %d", n)
	}
}
