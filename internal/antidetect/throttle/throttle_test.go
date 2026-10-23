package throttle

import (
	"testing"
	"time"
)

type fakeClock struct{ current time.Time }

func (c *fakeClock) now() time.Time          { return c.current }
func (c *fakeClock) advance(d time.Duration) { c.current = c.current.Add(d) }

func newFakeClock() *fakeClock {
	return &fakeClock{current: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
}

func TestDisabledLimiterAlwaysAllows(t *testing.T) {
	limiter := New(Config{Rate: 0}, newFakeClock().now)
	for i := 0; i < 100; i++ {
		if allowed, _ := limiter.Allow("cred"); !allowed {
			t.Fatal("a zero rate must never pace")
		}
	}
}

func TestBurstThenThrottle(t *testing.T) {
	clock := newFakeClock()
	limiter := New(Config{Rate: 1, Burst: 3}, clock.now)

	for i := 0; i < 3; i++ {
		if allowed, _ := limiter.Allow("cred"); !allowed {
			t.Fatalf("request %d must pass within the burst", i)
		}
	}
	allowed, wait := limiter.Allow("cred")
	if allowed {
		t.Fatal("the fourth request must be paced")
	}
	if wait <= 0 || wait > time.Second {
		t.Fatalf("wait = %v, want (0, 1s]", wait)
	}
}

func TestRefillOverTime(t *testing.T) {
	clock := newFakeClock()
	limiter := New(Config{Rate: 2, Burst: 1}, clock.now)

	if allowed, _ := limiter.Allow("cred"); !allowed {
		t.Fatal("first request must pass")
	}
	if allowed, _ := limiter.Allow("cred"); allowed {
		t.Fatal("second request must be paced")
	}

	clock.advance(500 * time.Millisecond) // 2/s -> one token per 500ms
	if allowed, _ := limiter.Allow("cred"); !allowed {
		t.Fatal("a token must be available after half a second")
	}
}

func TestBucketsAreIsolatedPerCredential(t *testing.T) {
	clock := newFakeClock()
	limiter := New(Config{Rate: 1, Burst: 1}, clock.now)

	if allowed, _ := limiter.Allow("a"); !allowed {
		t.Fatal("credential a must pass")
	}
	if allowed, _ := limiter.Allow("a"); allowed {
		t.Fatal("credential a must be paced")
	}
	if allowed, _ := limiter.Allow("b"); !allowed {
		t.Fatal("credential b must have its own bucket")
	}
}

func TestForgiveReturnsAToken(t *testing.T) {
	clock := newFakeClock()
	limiter := New(Config{Rate: 1, Burst: 1}, clock.now)

	if allowed, _ := limiter.Allow("cred"); !allowed {
		t.Fatal("first request must pass")
	}
	if allowed, _ := limiter.Allow("cred"); allowed {
		t.Fatal("second request must be paced")
	}
	limiter.Forgive("cred")
	if allowed, _ := limiter.Allow("cred"); !allowed {
		t.Fatal("a forgiven token must be reusable")
	}
	// Forgiving restores at most the burst: extra forgiveness must not create
	// capacity beyond it.
	limiter.Forgive("cred")
	limiter.Forgive("cred")
	if allowed, _ := limiter.Allow("cred"); !allowed {
		t.Fatal("restored tokens up to the burst must be usable")
	}
	if allowed, _ := limiter.Allow("cred"); allowed {
		t.Fatal("forgiveness must not exceed the burst")
	}
}

func TestForgetDropsState(t *testing.T) {
	clock := newFakeClock()
	limiter := New(Config{Rate: 1, Burst: 1}, clock.now)

	if allowed, _ := limiter.Allow("cred"); !allowed {
		t.Fatal("first request must pass")
	}
	limiter.Forget("cred")
	if allowed, _ := limiter.Allow("cred"); !allowed {
		t.Fatal("a forgotten credential must start with a full bucket")
	}
}

func TestNilLimiterIsSafe(t *testing.T) {
	var limiter *Limiter
	if allowed, _ := limiter.Allow("cred"); !allowed {
		t.Fatal("a nil limiter must not pace")
	}
	limiter.Forgive("cred")
	limiter.Forget("cred")
}
