package executor

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/antidetect/audit"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/antidetect/throttle"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const (
	// RateEnv sets the sustained outbound request rate per credential, in
	// requests per second. Unset or 0 disables pacing, which is the default:
	// a fork must not change request timing just by being installed.
	RateEnv = "CPA_ANTIDETECT_RATE"
	// BurstEnv sets how many requests may leave back to back per credential.
	BurstEnv = "CPA_ANTIDETECT_BURST"
	// MaxWaitEnv caps how long a request will wait for a slot before the
	// executor gives up and answers 429. Waiting forever turns pacing into an
	// outage.
	MaxWaitEnv = "CPA_ANTIDETECT_MAX_WAIT"
)

var (
	claudeLimiterOnce sync.Once
	claudeLimiterPtr  *throttle.Limiter
)

// claudeRateLimiter returns the process-wide limiter, configured from the
// environment on first use.
func claudeRateLimiter() *throttle.Limiter {
	claudeLimiterOnce.Do(func() {
		claudeLimiterPtr = throttle.New(throttle.Config{
			Rate:  envFloat(RateEnv, 0),
			Burst: envInt(BurstEnv, 1),
		}, time.Now)
	})
	return claudeLimiterPtr
}

// setClaudeRateLimiterForTest swaps the limiter and returns a restore func.
func setClaudeRateLimiterForTest(limiter *throttle.Limiter) func() {
	previous := claudeLimiterPtr
	claudeLimiterPtr = limiter
	// Mark the once as done so the environment never overrides a test value.
	claudeLimiterOnce.Do(func() {})
	return func() { claudeLimiterPtr = previous }
}

func envFloat(name string, fallback float64) float64 {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	value, errParse := strconv.ParseFloat(raw, 64)
	if errParse != nil || value < 0 {
		log.Warnf("antidetect: ignoring unparsable %s=%q", name, raw)
		return fallback
	}
	return value
}

func envInt(name string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	value, errParse := strconv.Atoi(raw)
	if errParse != nil || value <= 0 {
		log.Warnf("antidetect: ignoring unparsable %s=%q", name, raw)
		return fallback
	}
	return value
}

func maxThrottleWait() time.Duration {
	seconds := envFloat(MaxWaitEnv, 30)
	if seconds <= 0 {
		seconds = 30
	}
	return time.Duration(seconds * float64(time.Second))
}

// throttleKey identifies the bucket for a credential. The auth ID is stable
// across restarts; the label is a fallback for credentials that never got one.
func throttleKey(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return "anonymous"
	}
	if id := strings.TrimSpace(auth.ID); id != "" {
		return id
	}
	if index := strings.TrimSpace(auth.Index); index != "" {
		return "index:" + index
	}
	return "unknown"
}

// awaitClaudeRateSlot paces one upstream request for a credential.
//
// It runs before the request is sent and before any timeout that governs the
// upstream exchange, so it never races the request deadline: pacing is a local
// decision, and a cancelled caller gets its slot back through Forgive.
func awaitClaudeRateSlot(ctx context.Context, auth *cliproxyauth.Auth) error {
	limiter := claudeRateLimiter()
	key := throttleKey(auth)

	deadline := time.Now().Add(maxThrottleWait())
	waited := false
	for {
		allowed, wait := limiter.Allow(key)
		if allowed {
			if waited {
				// The slot was granted only after waiting: that is pacing in the
				// ordinary sense and belongs in the profile.
				recordClaudePace(auth)
			}
			return nil
		}
		if wait <= 0 {
			wait = time.Millisecond
		}
		if time.Now().Add(wait).After(deadline) {
			recordClaudePaceRejected(auth)
			audit.Log(audit.Record{
				Event:        "pacing",
				Decision:     audit.DecisionBlocked,
				CredentialID: audit.ID(key),
				Detail:       "wait budget exhausted: " + maxThrottleWait().String(),
			})
			return statusErr{
				code:       http.StatusTooManyRequests,
				msg:        fmt.Sprintf("antidetect: outbound pacing exceeded %s for this credential", maxThrottleWait()),
				retryAfter: &wait,
			}
		}
		waited = true
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ctx.Err()
		case <-timer.C:
		}
	}
}
