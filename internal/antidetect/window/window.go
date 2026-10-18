// Package window models the upstream rolling quota windows.
//
// The upstream enforces a five-hour and a seven-day budget and reports them in
// response headers. Ignoring those headers turns a quota problem into a
// rate-limit storm: the proxy keeps sending, the upstream keeps refusing, and
// the refusal pattern itself is a signal that no interactive client produces.
//
// The logic here is deliberately stateless: it converts one observation into one
// decision, so the caller owns storage and this package stays testable.
package window

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Kind names a window.
type Kind string

const (
	// KindShort is the rolling five-hour window.
	KindShort Kind = "5h"
	// KindLong is the rolling seven-day window.
	KindLong Kind = "7d"
)

// State is how much room a window leaves.
type State string

const (
	// StateNormal means the window has room.
	StateNormal State = "normal"
	// StateDegraded means the window is close to its limit: admit small work
	// only, because a large request can exhaust the remainder in one call.
	StateDegraded State = "degraded"
	// StateBlocked means the window is effectively full.
	StateBlocked State = "blocked"
)

const (
	// DegradedAt and BlockedAt are utilization thresholds.
	DegradedAt = 0.90
	BlockedAt  = 0.95
)

// Snapshot is one observation of a window.
type Snapshot struct {
	Kind Kind
	// Utilization is 0..1 as reported by the upstream.
	Utilization float64
	// ResetsAt is when the window rolls over. Zero means unknown.
	ResetsAt time.Time
	// Observed is when the snapshot was taken.
	Observed time.Time
}

// Evaluate converts a snapshot into a state.
//
// A reset time in the past means the window already rolled over while the
// upstream still reported the old utilization. Treating that stale "full" value
// as current would park a credential that has capacity again, so an elapsed
// reset resets the utilization to zero.
func Evaluate(snap Snapshot, now time.Time) State {
	if now.IsZero() {
		now = time.Now()
	}
	utilization := snap.Utilization
	if utilization < 0 {
		utilization = 0
	}
	if !snap.ResetsAt.IsZero() && !snap.ResetsAt.After(now) {
		utilization = 0
	}
	switch {
	case utilization >= BlockedAt:
		return StateBlocked
	case utilization >= DegradedAt:
		return StateDegraded
	default:
		return StateNormal
	}
}

// Remaining reports the share of the window still available, clamped to [0,1].
func (s Snapshot) Remaining(now time.Time) float64 {
	utilization := s.Utilization
	if !s.ResetsAt.IsZero() && !s.ResetsAt.After(now) {
		utilization = 0
	}
	if utilization <= 0 {
		return 1
	}
	if utilization >= 1 {
		return 0
	}
	return 1 - utilization
}

// headerNames maps a window to the upstream header names. Only the unified
// family is read: the per-model headers describe a different budget.
var headerNames = map[Kind]struct {
	utilization string
	reset       string
}{
	KindShort: {"anthropic-ratelimit-unified-5h-utilization", "anthropic-ratelimit-unified-5h-reset"},
	KindLong:  {"anthropic-ratelimit-unified-7d-utilization", "anthropic-ratelimit-unified-7d-reset"},
}

// ParseHeaders extracts every window the response reported.
//
// A header that is absent or unparsable is simply omitted: an observation the
// caller cannot trust is worse than no observation.
func ParseHeaders(headers http.Header, observed time.Time) []Snapshot {
	if headers == nil {
		return nil
	}
	if observed.IsZero() {
		observed = time.Now()
	}
	snapshots := make([]Snapshot, 0, len(headerNames))
	for kind, names := range headerNames {
		raw := strings.TrimSpace(headers.Get(names.utilization))
		if raw == "" {
			continue
		}
		utilization, errParse := strconv.ParseFloat(raw, 64)
		if errParse != nil {
			continue
		}
		snapshot := Snapshot{
			Kind:        kind,
			Utilization: utilization,
			Observed:    observed,
		}
		if resetAt, ok := parseReset(headers.Get(names.reset)); ok {
			snapshot.ResetsAt = resetAt
		}
		snapshots = append(snapshots, snapshot)
	}
	return snapshots
}

// parseReset accepts both an RFC3339 timestamp and a Unix epoch in seconds.
func parseReset(raw string) (time.Time, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return time.Time{}, false
	}
	if seconds, errParse := strconv.ParseInt(trimmed, 10, 64); errParse == nil {
		if seconds <= 0 {
			return time.Time{}, false
		}
		return time.Unix(seconds, 0).UTC(), true
	}
	if parsed, errParse := time.Parse(time.RFC3339, trimmed); errParse == nil {
		return parsed, true
	}
	return time.Time{}, false
}

// Worst reports the more constrained of two states.
func Worst(a, b State) State {
	rank := func(state State) int {
		switch state {
		case StateBlocked:
			return 2
		case StateDegraded:
			return 1
		default:
			return 0
		}
	}
	if rank(a) >= rank(b) {
		return a
	}
	return b
}
