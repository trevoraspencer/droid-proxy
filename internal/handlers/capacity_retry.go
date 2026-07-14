package handlers

import (
	"bytes"
	"context"
	"net/http"
	"time"

	"github.com/trevoraspencer/droid-proxy/internal/oauth"
)

// Bounded in-proxy backoff for transient upstream capacity rejections on the
// Responses paths. Grok Build in particular answers capacity exhaustion with
// short-lived 429/422 errors ("Some resource has been exhausted", "The service
// is temporarily at capacity"); relaying those immediately kills the client's
// turn — Factory renders the in-stream error frame as an empty response and a
// mission worker dies — while waiting a few seconds rides the blip out.
const (
	capacityRetryMaxAttempts = 2
	capacityRetryMaxDelay    = 10 * time.Second
)

// capacityRetryBaseDelay is a var so tests can shrink the backoff.
var capacityRetryBaseDelay = 2 * time.Second

// upstreamCapacityRejection reports whether a pre-stream upstream error is a
// transient capacity/availability rejection worth retrying in place. 429 and
// 503 always qualify; other 4xx/5xx qualify only when the body carries
// capacity wording (Grok Build has been observed using 422 for exhaustion).
func upstreamCapacityRejection(status int, body []byte) bool {
	switch status {
	case http.StatusTooManyRequests, http.StatusServiceUnavailable:
		return true
	}
	if status < http.StatusBadRequest || status >= 600 {
		return false
	}
	lower := bytes.ToLower(body)
	for _, marker := range [][]byte{
		[]byte("resource has been exhausted"),
		[]byte("at capacity"),
		[]byte("overloaded"),
	} {
		if bytes.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// capacityRetryDelay returns the wait before retry attempt (0-based),
// honoring an upstream Retry-After up to capacityRetryMaxDelay.
func capacityRetryDelay(headers http.Header, attempt int) time.Duration {
	now := time.Now()
	if ra := oauth.RetryAfterTime(headers, now); ra != nil {
		if d := ra.Sub(now); d > 0 {
			if d > capacityRetryMaxDelay {
				return capacityRetryMaxDelay
			}
			return d
		}
	}
	return capacityRetryBaseDelay << attempt
}

// sleepWithContext waits for d or until ctx is cancelled. Returns false on
// cancellation so callers can abandon the retry.
func sleepWithContext(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
