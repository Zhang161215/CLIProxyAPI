package executor

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/antidetect/throttle"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestAwaitClaudeRateSlotIsNoopByDefault(t *testing.T) {
	restore := setClaudeRateLimiterForTest(throttle.New(throttle.Config{Rate: 0}, time.Now))
	defer restore()

	auth := &cliproxyauth.Auth{ID: "cred-default"}
	for i := 0; i < 50; i++ {
		if errSlot := awaitClaudeRateSlot(context.Background(), auth); errSlot != nil {
			t.Fatalf("disabled pacing returned %v on call %d", errSlot, i)
		}
	}
}

func TestAwaitClaudeRateSlotPacesWithinBurst(t *testing.T) {
	clock := time.Now
	restore := setClaudeRateLimiterForTest(throttle.New(throttle.Config{Rate: 0.001, Burst: 1}, clock))
	defer restore()

	auth := &cliproxyauth.Auth{ID: "cred-paced"}
	if errSlot := awaitClaudeRateSlot(context.Background(), auth); errSlot != nil {
		t.Fatalf("first request must pass within the burst, got %v", errSlot)
	}
}

// Pacing must never become an outage: when the wait budget is exhausted the
// executor answers 429 instead of blocking the request forever.
func TestAwaitClaudeRateSlotAnswersTooManyRequestsWhenBudgetExhausted(t *testing.T) {
	t.Setenv(MaxWaitEnv, "0.05")

	restore := setClaudeRateLimiterForTest(throttle.New(throttle.Config{Rate: 0.001, Burst: 1}, time.Now))
	defer restore()

	auth := &cliproxyauth.Auth{ID: "cred-budget"}
	if errSlot := awaitClaudeRateSlot(context.Background(), auth); errSlot != nil {
		t.Fatalf("first request must pass, got %v", errSlot)
	}

	start := time.Now()
	errSlot := awaitClaudeRateSlot(context.Background(), auth)
	if errSlot == nil {
		t.Fatal("second request must not pass immediately")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("waited %v, want the budget cap to end the wait", elapsed)
	}
	statusErr, ok := errSlot.(statusErr)
	if !ok {
		t.Fatalf("error %T must be a statusErr", errSlot)
	}
	if statusErr.code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", statusErr.code)
	}
}

func TestAwaitClaudeRateSlotHonoursCancellation(t *testing.T) {
	restore := setClaudeRateLimiterForTest(throttle.New(throttle.Config{Rate: 0.001, Burst: 1}, time.Now))
	defer restore()

	auth := &cliproxyauth.Auth{ID: "cred-cancel"}
	if errSlot := awaitClaudeRateSlot(context.Background(), auth); errSlot != nil {
		t.Fatalf("first request must pass, got %v", errSlot)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if errSlot := awaitClaudeRateSlot(ctx, auth); errSlot == nil {
		t.Fatal("a cancelled caller must not be admitted")
	}
}

func TestThrottleKeyFallsBackToIndex(t *testing.T) {
	if got := throttleKey(nil); got != "anonymous" {
		t.Fatalf("nil auth key = %q", got)
	}
	if got := throttleKey(&cliproxyauth.Auth{ID: "abc"}); got != "abc" {
		t.Fatalf("id key = %q", got)
	}
	if got := throttleKey(&cliproxyauth.Auth{Index: "3"}); got != "index:3" {
		t.Fatalf("index key = %q", got)
	}
}
