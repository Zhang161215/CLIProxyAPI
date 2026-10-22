package window

import (
	"net/http"
	"testing"
	"time"
)

var testNow = time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)

func TestEvaluateThresholds(t *testing.T) {
	cases := []struct {
		utilization float64
		want        State
	}{
		{0, StateNormal},
		{0.5, StateNormal},
		{0.849, StateNormal},
		{0.85, StateDegraded},
		{0.899, StateDegraded},
		{0.90, StateBlocked},
		{1.0, StateBlocked},
		{1.4, StateBlocked},
	}
	for _, testCase := range cases {
		got := Evaluate(Snapshot{Utilization: testCase.utilization}, testNow)
		if got != testCase.want {
			t.Fatalf("utilization %.3f -> %s, want %s", testCase.utilization, got, testCase.want)
		}
	}
}

// The upstream can keep reporting utilization=1 with a reset time that has
// already passed. Treating that as current would park a credential that has
// capacity again.
func TestEvaluateTreatsElapsedResetAsNewWindow(t *testing.T) {
	snapshot := Snapshot{
		Utilization: 1,
		ResetsAt:    testNow.Add(-time.Minute),
	}
	if got := Evaluate(snapshot, testNow); got != StateNormal {
		t.Fatalf("state = %s, want normal after the window rolled over", got)
	}
	if got := snapshot.Remaining(testNow); got != 1 {
		t.Fatalf("remaining = %v, want 1", got)
	}
}

func TestEvaluateKeepsFutureReset(t *testing.T) {
	snapshot := Snapshot{Utilization: 0.97, ResetsAt: testNow.Add(time.Hour)}
	if got := Evaluate(snapshot, testNow); got != StateBlocked {
		t.Fatalf("state = %s, want blocked", got)
	}
}

func TestRemainingClamps(t *testing.T) {
	if got := (Snapshot{Utilization: -1}).Remaining(testNow); got != 1 {
		t.Fatalf("remaining = %v, want 1", got)
	}
	if got := (Snapshot{Utilization: 3}).Remaining(testNow); got != 0 {
		t.Fatalf("remaining = %v, want 0", got)
	}
	if got := (Snapshot{Utilization: 0.25}).Remaining(testNow); got != 0.75 {
		t.Fatalf("remaining = %v, want 0.75", got)
	}
}

func TestParseHeadersReadsBothWindows(t *testing.T) {
	headers := http.Header{}
	headers.Set("Anthropic-Ratelimit-Unified-5h-Utilization", "0.42")
	headers.Set("Anthropic-Ratelimit-Unified-5h-Reset", "1760792400")
	headers.Set("Anthropic-Ratelimit-Unified-7d-Utilization", "0.91")
	headers.Set("Anthropic-Ratelimit-Unified-7d-Reset", "2026-10-25T00:00:00Z")

	snapshots := ParseHeaders(headers, testNow)
	if len(snapshots) != 2 {
		t.Fatalf("parsed %d snapshots, want 2", len(snapshots))
	}
	byKind := map[Kind]Snapshot{}
	for _, snapshot := range snapshots {
		byKind[snapshot.Kind] = snapshot
	}
	if got := byKind[KindShort].Utilization; got != 0.42 {
		t.Fatalf("5h utilization = %v", got)
	}
	if byKind[KindShort].ResetsAt.IsZero() {
		t.Fatal("5h reset must parse from an epoch")
	}
	if got := Evaluate(byKind[KindLong], testNow); got != StateBlocked {
		t.Fatalf("7d state = %s, want blocked", got)
	}
}

func TestParseHeadersIgnoresGarbage(t *testing.T) {
	headers := http.Header{}
	headers.Set("Anthropic-Ratelimit-Unified-5h-Utilization", "not-a-number")
	headers.Set("Anthropic-Ratelimit-Unified-7d-Utilization", "0.5")
	headers.Set("Anthropic-Ratelimit-Unified-7d-Reset", "whenever")

	snapshots := ParseHeaders(headers, testNow)
	if len(snapshots) != 1 {
		t.Fatalf("parsed %d snapshots, want only the parsable one", len(snapshots))
	}
	if snapshots[0].Kind != KindLong {
		t.Fatalf("kind = %s, want 7d", snapshots[0].Kind)
	}
	if !snapshots[0].ResetsAt.IsZero() {
		t.Fatal("an unparsable reset must be left unset")
	}
}

func TestParseHeadersNilSafe(t *testing.T) {
	if got := ParseHeaders(nil, testNow); got != nil {
		t.Fatalf("nil headers must yield no snapshots, got %v", got)
	}
}

func TestWorstSelectsStricterState(t *testing.T) {
	if got := Worst(StateNormal, StateDegraded); got != StateDegraded {
		t.Fatalf("worst = %s", got)
	}
	if got := Worst(StateBlocked, StateDegraded); got != StateBlocked {
		t.Fatalf("worst = %s", got)
	}
	if got := Worst(StateNormal, StateNormal); got != StateNormal {
		t.Fatalf("worst = %s", got)
	}
}

// The thresholds decide how close a credential may run to its ceiling, so they
// have to be adjustable without a rebuild.
func TestEvaluateWithCustomThresholds(t *testing.T) {
	snapshot := Snapshot{Utilization: 0.60}
	if got := EvaluateWith(snapshot, testNow, Thresholds{Degraded: 0.55, Blocked: 0.65}); got != StateDegraded {
		t.Fatalf("custom degrade: got %s", got)
	}
	if got := EvaluateWith(Snapshot{Utilization: 0.70}, testNow, Thresholds{Degraded: 0.55, Blocked: 0.65}); got != StateBlocked {
		t.Fatalf("custom block: got %s", got)
	}
}

func TestThresholdsNormalizedRejectsNonsense(t *testing.T) {
	got := Thresholds{Degraded: 0.95, Blocked: 1.5}.Normalized()
	if got.Blocked != BlockedAt {
		t.Fatalf("out-of-range block level kept: %+v", got)
	}
	if got.Degraded >= got.Blocked {
		t.Fatalf("degraded must stay below blocked: %+v", got)
	}
}
