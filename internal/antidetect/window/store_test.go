package window

import (
	"testing"
	"time"
)

func TestStoreReportsNormalWithoutObservation(t *testing.T) {
	store := NewStore(func() time.Time { return testNow })
	if got := store.State("cred-1"); got != StateNormal {
		t.Fatalf("state = %s, want normal", got)
	}
}

func TestStoreReportsBlockedAfterFullObservation(t *testing.T) {
	store := NewStore(func() time.Time { return testNow })
	store.Observe("cred-1", []Snapshot{{
		Kind:        KindShort,
		Utilization: 0.97,
		ResetsAt:    testNow.Add(time.Hour),
		Observed:    testNow,
	}})
	if got := store.State("cred-1"); got != StateBlocked {
		t.Fatalf("state = %s, want blocked", got)
	}
}

// The stricter of the two windows wins: a full weekly window matters even when
// the five-hour window is empty.
func TestStoreTakesWorstOfBothWindows(t *testing.T) {
	store := NewStore(func() time.Time { return testNow })
	store.Observe("cred-1", []Snapshot{
		{Kind: KindShort, Utilization: 0.1, ResetsAt: testNow.Add(time.Hour), Observed: testNow},
		{Kind: KindLong, Utilization: 0.96, ResetsAt: testNow.Add(24 * time.Hour), Observed: testNow},
	})
	if got := store.State("cred-1"); got != StateBlocked {
		t.Fatalf("state = %s, want blocked", got)
	}
}

// Stale data must not park a credential that may have rolled over since.
func TestStoreIgnoresStaleObservations(t *testing.T) {
	now := testNow
	store := NewStore(func() time.Time { return now })
	store.Observe("cred-1", []Snapshot{{
		Kind:        KindShort,
		Utilization: 0.99,
		ResetsAt:    testNow.Add(time.Hour),
		Observed:    testNow,
	}})
	if got := store.State("cred-1"); got != StateBlocked {
		t.Fatalf("fresh observation must block, got %s", got)
	}

	now = testNow.Add(ObservationTTL + time.Minute)
	if got := store.State("cred-1"); got != StateNormal {
		t.Fatalf("stale observation must not block, got %s", got)
	}
}

// A reset that has already happened means the window rolled over, even inside
// the TTL.
func TestStoreRespectsElapsedReset(t *testing.T) {
	now := testNow
	store := NewStore(func() time.Time { return now })
	store.Observe("cred-1", []Snapshot{{
		Kind:        KindShort,
		Utilization: 1,
		ResetsAt:    testNow.Add(time.Minute),
		Observed:    testNow,
	}})
	if got := store.State("cred-1"); got != StateBlocked {
		t.Fatalf("state = %s, want blocked before the reset", got)
	}

	now = testNow.Add(2 * time.Minute)
	if got := store.State("cred-1"); got != StateNormal {
		t.Fatalf("state = %s, want normal after the reset", got)
	}
}

func TestStoreIsolatesCredentials(t *testing.T) {
	store := NewStore(func() time.Time { return testNow })
	store.Observe("cred-a", []Snapshot{{
		Kind: KindShort, Utilization: 0.99, ResetsAt: testNow.Add(time.Hour), Observed: testNow,
	}})
	if got := store.State("cred-b"); got != StateNormal {
		t.Fatalf("credential b = %s, want normal", got)
	}
}

func TestStoreForgetClearsState(t *testing.T) {
	store := NewStore(func() time.Time { return testNow })
	store.Observe("cred-1", []Snapshot{{
		Kind: KindShort, Utilization: 0.99, ResetsAt: testNow.Add(time.Hour), Observed: testNow,
	}})
	store.Forget("cred-1")
	if got := store.State("cred-1"); got != StateNormal {
		t.Fatalf("state = %s after Forget, want normal", got)
	}
}

func TestStoreObservationReplacesPerWindow(t *testing.T) {
	store := NewStore(func() time.Time { return testNow })
	store.Observe("cred-1", []Snapshot{{Kind: KindShort, Utilization: 0.99, ResetsAt: testNow.Add(time.Hour), Observed: testNow}})
	store.Observe("cred-1", []Snapshot{{Kind: KindShort, Utilization: 0.10, ResetsAt: testNow.Add(time.Hour), Observed: testNow}})
	if got := store.State("cred-1"); got != StateNormal {
		t.Fatalf("state = %s, want the newer observation to win", got)
	}
}

func TestNilStoreIsSafe(t *testing.T) {
	var store *Store
	store.Observe("cred-1", []Snapshot{{Kind: KindShort, Utilization: 1}})
	if got := store.State("cred-1"); got != StateNormal {
		t.Fatalf("nil store state = %s, want normal", got)
	}
	store.Forget("cred-1")
	if store.Snapshot("cred-1") != nil {
		t.Fatal("nil store snapshot must be nil")
	}
}
