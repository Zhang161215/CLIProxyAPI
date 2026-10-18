package auth

import (
	"errors"
	"net/http"
	"testing"
	"time"
)

type retryAfterErr struct{ delay time.Duration }

func (e retryAfterErr) Error() string              { return "paced" }
func (e retryAfterErr) RetryAfter() *time.Duration { return &e.delay }

// A caller that computes a retry delay must see it reach the response headers,
// otherwise the delay is silently discarded and the client retries immediately.
func TestSafeResponseHeadersHonoursGenericRetryAfter(t *testing.T) {
	headers := SafeResponseHeaders(retryAfterErr{delay: 90 * time.Second})
	if got := headers.Get("Retry-After"); got != "90" {
		t.Fatalf("Retry-After = %q, want 90", got)
	}
}

func TestSafeResponseHeadersGenericRetryAfterWrapped(t *testing.T) {
	headers := SafeResponseHeaders(errors.Join(errors.New("outer"), retryAfterErr{delay: 2 * time.Second}))
	if got := headers.Get("Retry-After"); got != "2" {
		t.Fatalf("Retry-After = %q, want 2 (wrapped errors must be unwrapped)", got)
	}
}

func TestSafeResponseHeadersNilRetryAfter(t *testing.T) {
	if headers := SafeResponseHeaders(errors.New("plain")); headers != nil {
		t.Fatalf("plain errors must expose no headers, got %v", headers)
	}
}

func TestSafeResponseHeadersZeroRetryAfterIsDropped(t *testing.T) {
	headers := SafeResponseHeaders(retryAfterErr{delay: 0})
	if headers.Get("Retry-After") != "" {
		t.Fatal("a zero delay must not produce a header")
	}
	if headers != nil && headers.Get("Retry-After") != http.CanonicalHeaderKey("") {
		// no assertion beyond the empty header; kept for clarity
		return
	}
}
