// Package monitor keeps a rolling per-credential profile of how a credential
// behaves, so a revocation can be explained after the fact.
//
// The audit trail records individual decisions, which answers "what happened at
// 14:03". It cannot answer "did this credential ever run from two exits", and
// that question is the one that matters when an account is banned: sharing is
// visible as a shape over time, not as a single request.
//
// The profile deliberately keeps counts and small sets, never request bodies.
package monitor

import (
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// maxTrackedValues bounds each set so a hostile or broken client cannot grow
// memory without limit. When the cap is hit the set stops admitting new values
// and reports overflow, which is itself informative.
const maxTrackedValues = 32

// Profile is the rolling view of one credential.
type Profile struct {
	CredentialID string

	Requests int64
	Allowed  int64
	Blocked  int64
	Paced    int64
	// PaceRejected counts requests that gave up waiting for a slot. Paced
	// counts requests that waited and then proceeded; the two were previously
	// conflated, which made a working limiter look disabled.
	PaceRejected int64
	RateLimited  int64

	ClientIPs   map[string]struct{}
	UserAgents  map[string]struct{}
	Entrypoints map[string]struct{}
	Sessions    map[string]struct{}

	BlockedByRule map[string]int64

	MaxUtilization float64
	FirstSeen      time.Time
	LastSeen       time.Time

	Overflow bool
}

// Snapshot is a consistent copy of a profile, safe to log.
type Snapshot struct {
	CredentialID   string
	Requests       int64
	Allowed        int64
	Blocked        int64
	Paced          int64
	PaceRejected   int64
	RateLimited    int64
	DistinctIPs    int
	DistinctUAs    int
	DistinctEntry  int
	DistinctSess   int
	MaxUtilization float64
	BlockedByRule  map[string]int64
	FirstSeen      time.Time
	LastSeen       time.Time
	Overflow       bool
}

// Registry holds profiles for every credential seen.
type Registry struct {
	mu       sync.Mutex
	profiles map[string]*Profile
	now      func() time.Time
}

// NewRegistry builds an empty registry. A nil clock uses time.Now.
func NewRegistry(now func() time.Time) *Registry {
	if now == nil {
		now = time.Now
	}
	return &Registry{profiles: make(map[string]*Profile), now: now}
}

func (r *Registry) profile(credentialID string) *Profile {
	profile, exists := r.profiles[credentialID]
	if !exists {
		now := r.now()
		profile = &Profile{
			CredentialID:  credentialID,
			ClientIPs:     make(map[string]struct{}),
			UserAgents:    make(map[string]struct{}),
			Entrypoints:   make(map[string]struct{}),
			Sessions:      make(map[string]struct{}),
			BlockedByRule: make(map[string]int64),
			FirstSeen:     now,
			LastSeen:      now,
		}
		r.profiles[credentialID] = profile
	}
	return profile
}

func addBounded(set map[string]struct{}, value string) (overflowed bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	if _, exists := set[value]; exists {
		return false
	}
	if len(set) >= maxTrackedValues {
		return true
	}
	set[value] = struct{}{}
	return false
}

// Observe records one request outcome.
func (r *Registry) Observe(credentialID, clientIP, userAgent, entrypoint, sessionID string, allowed bool) {
	if r == nil || credentialID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	profile := r.profile(credentialID)
	profile.Requests++
	if allowed {
		profile.Allowed++
	}
	if addBounded(profile.ClientIPs, clientIP) ||
		addBounded(profile.UserAgents, userAgent) ||
		addBounded(profile.Entrypoints, entrypoint) ||
		addBounded(profile.Sessions, sessionID) {
		profile.Overflow = true
	}
	profile.LastSeen = r.now()
}

// Block records a refusal and the rule that produced it.
func (r *Registry) Block(credentialID, rule string) {
	if r == nil || credentialID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	profile := r.profile(credentialID)
	profile.Blocked++
	if rule = strings.TrimSpace(rule); rule != "" {
		profile.BlockedByRule[rule]++
	}
	profile.LastSeen = r.now()
}

// Pace records a request that waited for a rate slot.
func (r *Registry) Pace(credentialID string) {
	if r == nil || credentialID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	profile := r.profile(credentialID)
	profile.Paced++
	profile.LastSeen = r.now()
}

// PaceRejected records a request that exhausted its wait budget instead of
// waiting for a slot.
func (r *Registry) PaceRejected(credentialID string) {
	if credentialID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	profile := r.profile(credentialID)
	profile.PaceRejected++
	profile.LastSeen = r.now()
}

// RateLimit records an upstream 429 for this credential.
//
// Rate limits are counted separately from pacing: pacing is our own decision,
// while a 429 is the upstream's. A credential that keeps collecting 429s is
// being watched even when the calls eventually succeed.
func (r *Registry) RateLimit(credentialID string) {
	if r == nil || credentialID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	profile := r.profile(credentialID)
	profile.RateLimited++
	profile.LastSeen = r.now()
}

// Utilization records the highest quota-window utilization seen.
func (r *Registry) Utilization(credentialID string, utilization float64) {
	if r == nil || credentialID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	profile := r.profile(credentialID)
	if utilization > profile.MaxUtilization {
		profile.MaxUtilization = utilization
	}
	profile.LastSeen = r.now()
}

// Snapshots returns a copy of every profile, ordered by credential ID.
func (r *Registry) Snapshots() []Snapshot {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	out := make([]Snapshot, 0, len(r.profiles))
	for _, profile := range r.profiles {
		blocked := make(map[string]int64, len(profile.BlockedByRule))
		for rule, count := range profile.BlockedByRule {
			blocked[rule] = count
		}
		out = append(out, Snapshot{
			CredentialID:   profile.CredentialID,
			Requests:       profile.Requests,
			Allowed:        profile.Allowed,
			Blocked:        profile.Blocked,
			Paced:          profile.Paced,
			PaceRejected:   profile.PaceRejected,
			RateLimited:    profile.RateLimited,
			DistinctIPs:    len(profile.ClientIPs),
			DistinctUAs:    len(profile.UserAgents),
			DistinctEntry:  len(profile.Entrypoints),
			DistinctSess:   len(profile.Sessions),
			MaxUtilization: profile.MaxUtilization,
			BlockedByRule:  blocked,
			FirstSeen:      profile.FirstSeen,
			LastSeen:       profile.LastSeen,
			Overflow:       profile.Overflow,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CredentialID < out[j].CredentialID })
	return out
}

// Reset clears a credential's profile after it has been flushed, so the next
// window starts clean and a long-lived credential cannot accumulate forever.
func (r *Registry) Reset(credentialID string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.profiles, credentialID)
}

// RiskSignals ranks the observations that most often precede a revocation.
//
// It reads like a triage note, not a verdict: each signal names the measurement
// and the threshold, so a human can disagree with it.
func (s Snapshot) RiskSignals() []string {
	var signals []string
	if s.DistinctIPs > 1 {
		signals = append(signals, "multiple exits: "+strconv.Itoa(s.DistinctIPs)+" distinct client IPs")
	}
	if s.DistinctUAs > 1 {
		signals = append(signals, "mixed clients: "+strconv.Itoa(s.DistinctUAs)+" distinct user agents")
	}
	if s.DistinctEntry > 1 {
		signals = append(signals, "mixed entrypoints: "+strconv.Itoa(s.DistinctEntry)+" distinct entrypoints")
	}
	if s.RateLimited > 0 {
		signals = append(signals, "upstream rate limits: "+strconv.FormatInt(s.RateLimited, 10)+
			" (a 429 streak often precedes a revocation)")
	}
	if s.Blocked > 0 {
		signals = append(signals, "content blocks: "+strconv.FormatInt(s.Blocked, 10))
	}
	if s.MaxUtilization >= 0.95 {
		signals = append(signals, "quota window saturated: "+strconv.FormatFloat(s.MaxUtilization, 'f', 2, 64))
	}
	if s.Overflow {
		signals = append(signals, "observation overflow: tracked set hit its cap")
	}
	return signals
}
