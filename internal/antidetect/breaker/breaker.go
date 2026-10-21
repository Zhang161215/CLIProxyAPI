// Package breaker pauses a credential after a burst of upstream 429s.
//
// A quota window answers "is this credential full". It cannot answer "is this
// credential being throttled right now": upstream can reject request after
// request while the window still reports headroom, and every retry is a fresh
// demand signal. Case 2026-10 showed the shape — 2327 local failures and 302
// upstream 429s inside 36 hours, followed by revocation — so the missing control
// is not another check on the response, it is stopping the retries.
package breaker

import (
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ConfigEnv tunes the breaker. Format: <threshold>/<window>/<cooldown>,
// for example "5/15m/15m". The literal "off" disables it.
const ConfigEnv = "CPA_ANTIDETECT_BREAKER"

// Defaults are deliberately loose: a healthy account never reaches five 429s in
// fifteen minutes, and a struggling one needs the pause before it becomes a
// pattern.
const (
	DefaultThreshold = 5
	DefaultWindow    = 15 * time.Minute
	DefaultCooldown  = 15 * time.Minute
)

// Options configures one breaker.
type Options struct {
	Threshold int
	Window    time.Duration
	Cooldown  time.Duration
	Now       func() time.Time
}

// Breaker tracks recent 429s per credential key.
type Breaker struct {
	mu    sync.Mutex
	opts  Options
	state map[string]*entry
}

type entry struct {
	hits  []time.Time
	until time.Time
}

// New builds a breaker. A nil clock falls back to the wall clock; tests inject
// their own so nothing depends on sleeping.
func New(opts Options) *Breaker {
	if opts.Threshold <= 0 {
		opts.Threshold = DefaultThreshold
	}
	if opts.Window <= 0 {
		opts.Window = DefaultWindow
	}
	if opts.Cooldown <= 0 {
		opts.Cooldown = DefaultCooldown
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Breaker{opts: opts, state: make(map[string]*entry)}
}

// Blocked reports how long a credential still has to wait.
func (b *Breaker) Blocked(key string) (time.Duration, bool) {
	if b == nil || key == "" {
		return 0, false
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	item := b.state[key]
	if item == nil || item.until.IsZero() {
		return 0, false
	}
	remaining := item.until.Sub(b.opts.Now())
	if remaining <= 0 {
		item.until = time.Time{}
		item.hits = nil
		return 0, false
	}
	return remaining, true
}

// Record counts one upstream 429 and reports whether it tripped the breaker.
func (b *Breaker) Record(key string) bool {
	if b == nil || key == "" {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	now := b.opts.Now()
	item := b.state[key]
	if item == nil {
		item = &entry{}
		b.state[key] = item
	}

	// Drop hits that fell out of the window before counting this one.
	kept := item.hits[:0]
	for _, hit := range item.hits {
		if now.Sub(hit) <= b.opts.Window {
			kept = append(kept, hit)
		}
	}
	item.hits = append(kept, now)

	if len(item.hits) < b.opts.Threshold {
		return false
	}
	item.hits = nil
	item.until = now.Add(b.opts.Cooldown)
	return true
}

// Forget removes a credential's state, for example after it is deleted.
func (b *Breaker) Forget(key string) {
	if b == nil || key == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.state, key)
}

// OptionsFromEnv reads ConfigEnv, falling back to the defaults.
func OptionsFromEnv() (Options, bool) {
	raw := strings.TrimSpace(os.Getenv(ConfigEnv))
	if strings.EqualFold(raw, "off") || strings.EqualFold(raw, "0") || strings.EqualFold(raw, "false") {
		return Options{}, false
	}
	opts := Options{Threshold: DefaultThreshold, Window: DefaultWindow, Cooldown: DefaultCooldown}
	if raw == "" {
		return opts, true
	}

	parts := strings.Split(raw, "/")
	if len(parts) != 3 {
		return opts, true
	}
	if value, errParse := strconv.Atoi(strings.TrimSpace(parts[0])); errParse == nil && value > 0 {
		opts.Threshold = value
	}
	if window, errParse := time.ParseDuration(strings.TrimSpace(parts[1])); errParse == nil && window > 0 {
		opts.Window = window
	}
	if cooldown, errParse := time.ParseDuration(strings.TrimSpace(parts[2])); errParse == nil && cooldown > 0 {
		opts.Cooldown = cooldown
	}
	return opts, true
}
