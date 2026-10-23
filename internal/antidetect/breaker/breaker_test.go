package breaker

import (
	"testing"
	"time"
)

type clock struct{ now time.Time }

func (c *clock) Now() time.Time          { return c.now }
func (c *clock) advance(d time.Duration) { c.now = c.now.Add(d) }

func newTestBreaker(threshold int, window, cooldown time.Duration) (*Breaker, *clock) {
	clk := &clock{now: time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)}
	return New(Options{Threshold: threshold, Window: window, Cooldown: cooldown, Now: clk.Now}), clk
}

func TestTripsAtThreshold(t *testing.T) {
	b, _ := newTestBreaker(3, 15*time.Minute, 15*time.Minute)
	for i := 0; i < 2; i++ {
		if b.Record("cred") {
			t.Fatalf("tripped after %d hits, want 3", i+1)
		}
	}
	if !b.Record("cred") {
		t.Fatal("did not trip on the third hit")
	}
	if remaining, blocked := b.Blocked("cred"); !blocked || remaining <= 0 {
		t.Fatalf("blocked = %v, remaining = %v", blocked, remaining)
	}
}

// Hits outside the window must not accumulate forever: an account that sees one
// 429 a day is throttled, not broken.
func TestOldHitsFallOutOfWindow(t *testing.T) {
	b, clk := newTestBreaker(3, 10*time.Minute, 15*time.Minute)
	b.Record("cred")
	clk.advance(6 * time.Minute)
	b.Record("cred")
	clk.advance(6 * time.Minute) // first hit is now 12m old, outside the 10m window
	if b.Record("cred") {
		t.Fatal("tripped although the first hit had aged out")
	}
}

func TestCooldownExpires(t *testing.T) {
	b, clk := newTestBreaker(2, 15*time.Minute, 10*time.Minute)
	b.Record("cred")
	b.Record("cred")
	if _, blocked := b.Blocked("cred"); !blocked {
		t.Fatal("should be blocked right after tripping")
	}
	clk.advance(11 * time.Minute)
	if remaining, blocked := b.Blocked("cred"); blocked {
		t.Fatalf("still blocked with %v remaining after cooldown", remaining)
	}
}

// A tripped breaker must start counting from zero, otherwise the first 429 after
// recovery trips it again immediately.
func TestTripClearsCounters(t *testing.T) {
	b, _ := newTestBreaker(2, 15*time.Minute, 5*time.Minute)
	b.Record("cred")
	b.Record("cred")
	if b.Record("cred") {
		t.Fatal("counters were not cleared by the trip")
	}
}

func TestCredentialsAreIndependent(t *testing.T) {
	b, _ := newTestBreaker(2, 15*time.Minute, 15*time.Minute)
	b.Record("a")
	b.Record("a")
	if _, blocked := b.Blocked("b"); blocked {
		t.Fatal("one credential's 429s blocked another")
	}
}

func TestForgetClearsState(t *testing.T) {
	b, _ := newTestBreaker(1, 15*time.Minute, 15*time.Minute)
	b.Record("cred")
	b.Forget("cred")
	if _, blocked := b.Blocked("cred"); blocked {
		t.Fatal("Forget left the credential blocked")
	}
}

func TestOptionsFromEnv(t *testing.T) {
	t.Setenv(ConfigEnv, "")
	if opts, enabled := OptionsFromEnv(); !enabled || opts.Threshold != DefaultThreshold {
		t.Fatalf("default: enabled=%v opts=%+v", enabled, opts)
	}

	t.Setenv(ConfigEnv, "off")
	if _, enabled := OptionsFromEnv(); enabled {
		t.Fatal("off must disable the breaker")
	}

	t.Setenv(ConfigEnv, "8/20m/30m")
	opts, enabled := OptionsFromEnv()
	if !enabled || opts.Threshold != 8 || opts.Window != 20*time.Minute || opts.Cooldown != 30*time.Minute {
		t.Fatalf("custom: enabled=%v opts=%+v", enabled, opts)
	}

	t.Setenv(ConfigEnv, "garbage")
	if opts, enabled := OptionsFromEnv(); !enabled || opts.Threshold != DefaultThreshold {
		t.Fatalf("malformed should fall back to defaults: %+v", opts)
	}
}
