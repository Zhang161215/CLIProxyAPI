// Package throttle paces outbound requests per credential.
//
// Inter-request timing is one of the few signals a proxy cannot hide behind a
// convincing payload: real usage has gaps, bursts, and quiet hours, while a
// proxy drains its queue as fast as the network allows. This package only
// shapes that timing; it never drops work.
package throttle

import (
	"sync"
	"time"
)

// Config describes a per-credential bucket.
type Config struct {
	// Rate is the sustained request rate per second. Zero disables pacing.
	Rate float64
	// Burst is how many requests may leave back to back.
	Burst int
	// Jitter is the maximum random delay added to each admission. The caller
	// supplies it; this package only reports the budget.
	Jitter time.Duration
}

// Limiter is a token bucket per credential key.
//
// The clock is injected so tests do not depend on wall-clock sleeps: platform
// timer granularity (notably on Windows) would otherwise make this flaky.
type Limiter struct {
	config Config
	now    func() time.Time

	mu      sync.Mutex
	buckets map[string]*bucket
}

type bucket struct {
	tokens   float64
	lastFill time.Time
}

// New builds a limiter. A nil clock uses time.Now.
func New(config Config, now func() time.Time) *Limiter {
	if now == nil {
		now = time.Now
	}
	if config.Burst <= 0 {
		config.Burst = 1
	}
	return &Limiter{
		config:  config,
		now:     now,
		buckets: make(map[string]*bucket),
	}
}

// Allow reports whether one request may leave now, and if not, how long the
// caller should wait before asking again.
//
// It consumes a token when it returns true. Callers must not sleep inside this
// call: the returned duration is advisory so the request can be queued rather
// than blocking a goroutine.
func (l *Limiter) Allow(key string) (bool, time.Duration) {
	if l == nil || l.config.Rate <= 0 {
		return true, 0
	}
	now := l.now()

	l.mu.Lock()
	defer l.mu.Unlock()

	current, exists := l.buckets[key]
	if !exists {
		current = &bucket{tokens: float64(l.config.Burst), lastFill: now}
		l.buckets[key] = current
	} else {
		elapsed := now.Sub(current.lastFill).Seconds()
		if elapsed > 0 {
			current.tokens += elapsed * l.config.Rate
			if current.tokens > float64(l.config.Burst) {
				current.tokens = float64(l.config.Burst)
			}
			current.lastFill = now
		}
	}

	if current.tokens >= 1 {
		current.tokens--
		return true, 0
	}

	// Time until the bucket holds one token again.
	deficit := 1 - current.tokens
	wait := time.Duration(deficit / l.config.Rate * float64(time.Second))
	return false, wait
}

// Forgive returns one token to a bucket, for a request that was admitted but
// never reached the upstream.
func (l *Limiter) Forgive(key string) {
	if l == nil || l.config.Rate <= 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	current, exists := l.buckets[key]
	if !exists {
		return
	}
	if current.tokens < float64(l.config.Burst) {
		current.tokens++
	}
}

// Forget drops a bucket, for a credential that was removed.
func (l *Limiter) Forget(key string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.buckets, key)
}
