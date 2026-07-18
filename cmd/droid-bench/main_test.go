package main

import "testing"

func TestValidateMockListenAddr(t *testing.T) {
	t.Parallel()

	for _, addr := range []string{"127.0.0.1:18100", "localhost:18100", "[::1]:18100"} {
		addr := addr
		t.Run("accept_"+addr, func(t *testing.T) {
			t.Parallel()
			if err := validateMockListenAddr(addr); err != nil {
				t.Fatalf("validate %q: %v", addr, err)
			}
		})
	}
	for _, addr := range []string{"0.0.0.0:18100", "[::]:18100", "192.168.1.10:18100", ":18100", "not-an-address"} {
		addr := addr
		t.Run("reject_"+addr, func(t *testing.T) {
			t.Parallel()
			if err := validateMockListenAddr(addr); err == nil {
				t.Fatalf("validate %q unexpectedly succeeded", addr)
			}
		})
	}
}
