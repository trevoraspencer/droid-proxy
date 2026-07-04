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

func TestCount_StableRepresentativeInputs(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
		want int
	}{
		{"plain", "Hello, world!", 4},
		{"jsonish", `{"city":"Paris","unit":"c"}`, 9},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Count(tc.text)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("Count(%q)=%d want %d", tc.text, got, tc.want)
			}
		})
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

func TestCountChatMessages_StableRepresentativeConversation(t *testing.T) {
	msgs := []ChatMessage{
		{Role: "system", Content: "Be concise."},
		{Role: "user", Content: "Weather in Paris?"},
		{Role: "assistant", Content: "I will check."},
	}
	n, err := CountChatMessages(msgs)
	if err != nil {
		t.Fatal(err)
	}
	if n != 28 {
		t.Fatalf("CountChatMessages=%d want 28", n)
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
