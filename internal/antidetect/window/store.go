package window

import (
	"sync"
	"time"
)

// ObservationTTL is how long an observation stays actionable.
//
// A snapshot older than this is treated as unknown rather than as current: the
// upstream reports headers only on the responses that carry them, so an idle
// credential keeps its last snapshot forever. Park a credential on stale data
// and it never comes back.
const ObservationTTL = 10 * time.Minute

// Store keeps the latest snapshot per credential and window.
//
// It is intentionally small and in-process: losing it on restart costs one
// request of headroom, while persisting it would let a stale "full" state
// survive a restart and park a credential that has since reset.
type Store struct {
	mu           sync.Mutex
	byCredential map[string]map[Kind]Snapshot
	now          func() time.Time
}

// NewStore builds an empty store. A nil clock uses time.Now.
func NewStore(now func() time.Time) *Store {
	if now == nil {
		now = time.Now
	}
	return &Store{
		byCredential: make(map[string]map[Kind]Snapshot),
		now:          now,
	}
}

// Observe records snapshots for a credential. Later observations replace
// earlier ones per window; unknown windows are left untouched.
func (s *Store) Observe(credentialID string, snapshots []Snapshot) {
	if s == nil || credentialID == "" || len(snapshots) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	windows, exists := s.byCredential[credentialID]
	if !exists {
		windows = make(map[Kind]Snapshot, len(snapshots))
		s.byCredential[credentialID] = windows
	}
	for _, snapshot := range snapshots {
		windows[snapshot.Kind] = snapshot
	}
}

// State reports the stricter of the credential's two windows.
//
// Unknown or stale observations report StateNormal: the gate exists to stop
// hammering a full window, not to invent one.
func (s *Store) State(credentialID string) State {
	return s.StateWith(credentialID, DefaultThresholds())
}

// StateWith evaluates the stored windows against explicit thresholds.
func (s *Store) StateWith(credentialID string, thresholds Thresholds) State {
	if s == nil || credentialID == "" {
		return StateNormal
	}
	now := s.now()

	s.mu.Lock()
	defer s.mu.Unlock()

	windows, exists := s.byCredential[credentialID]
	if !exists {
		return StateNormal
	}
	state := StateNormal
	for _, snapshot := range windows {
		observed := snapshot.Observed
		if observed.IsZero() {
			observed = now
		}
		if now.Sub(observed) > ObservationTTL {
			continue
		}
		state = Worst(state, EvaluateWith(snapshot, now, thresholds))
	}
	return state
}

// Forget drops a credential's state, for a credential that was removed.
func (s *Store) Forget(credentialID string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.byCredential, credentialID)
}

// Snapshot returns the stored windows for inspection and tests.
func (s *Store) Snapshot(credentialID string) map[Kind]Snapshot {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	windows := s.byCredential[credentialID]
	if windows == nil {
		return nil
	}
	copied := make(map[Kind]Snapshot, len(windows))
	for kind, snapshot := range windows {
		copied[kind] = snapshot
	}
	return copied
}
