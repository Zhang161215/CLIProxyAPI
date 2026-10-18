package monitor

import (
	"testing"
	"time"
)

var testNow = time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)

func TestSnapshotCountsDistinctValues(t *testing.T) {
	registry := NewRegistry(func() time.Time { return testNow })

	registry.Observe("cred-1", "1.1.1.1", "claude-cli/2.1.280 (external, cli)", "cli", "s1", true)
	registry.Observe("cred-1", "1.1.1.1", "claude-cli/2.1.280 (external, cli)", "cli", "s1", true)
	registry.Observe("cred-1", "2.2.2.2", "claude-cli/2.1.280 (external, sdk-cli)", "sdk-cli", "s2", true)

	snapshots := registry.Snapshots()
	if len(snapshots) != 1 {
		t.Fatalf("got %d snapshots, want 1", len(snapshots))
	}
	snapshot := snapshots[0]
	if snapshot.Requests != 3 || snapshot.Allowed != 3 {
		t.Fatalf("requests=%d allowed=%d", snapshot.Requests, snapshot.Allowed)
	}
	if snapshot.DistinctIPs != 2 {
		t.Fatalf("distinct IPs = %d, want 2", snapshot.DistinctIPs)
	}
	if snapshot.DistinctUAs != 2 {
		t.Fatalf("distinct UAs = %d, want 2", snapshot.DistinctUAs)
	}
	if snapshot.DistinctEntry != 2 {
		t.Fatalf("distinct entrypoints = %d, want 2", snapshot.DistinctEntry)
	}
	if snapshot.DistinctSess != 2 {
		t.Fatalf("distinct sessions = %d, want 2", snapshot.DistinctSess)
	}
}

func TestSnapshotsIsolateCredentials(t *testing.T) {
	registry := NewRegistry(func() time.Time { return testNow })
	registry.Observe("cred-a", "1.1.1.1", "ua", "cli", "s", true)
	registry.Observe("cred-b", "9.9.9.9", "ua", "cli", "s", true)

	snapshots := registry.Snapshots()
	if len(snapshots) != 2 {
		t.Fatalf("got %d snapshots, want 2", len(snapshots))
	}
	if snapshots[0].CredentialID != "cred-a" || snapshots[1].CredentialID != "cred-b" {
		t.Fatalf("snapshots are not ordered by credential: %v", snapshots)
	}
	if snapshots[0].DistinctIPs != 1 {
		t.Fatal("counters must not bleed across credentials")
	}
}

func TestBlockAndPaceCounters(t *testing.T) {
	registry := NewRegistry(func() time.Time { return testNow })
	registry.Observe("cred-1", "1.1.1.1", "ua", "cli", "s", false)
	registry.Block("cred-1", "distill.system_prompt")
	registry.Block("cred-1", "distill.system_prompt")
	registry.Pace("cred-1")
	registry.Utilization("cred-1", 0.97)

	snapshot := registry.Snapshots()[0]
	if snapshot.Blocked != 2 {
		t.Fatalf("blocked = %d, want 2", snapshot.Blocked)
	}
	if snapshot.Paced != 1 {
		t.Fatalf("paced = %d, want 1", snapshot.Paced)
	}
	if snapshot.BlockedByRule["distill.system_prompt"] != 2 {
		t.Fatalf("rule counter = %v", snapshot.BlockedByRule)
	}
	if snapshot.MaxUtilization != 0.97 {
		t.Fatalf("max utilization = %v", snapshot.MaxUtilization)
	}
}

// Utilization keeps the worst value, not the latest: a credential that hit 98%
// an hour ago is still a credential that hit 98%.
func TestUtilizationKeepsMaximum(t *testing.T) {
	registry := NewRegistry(func() time.Time { return testNow })
	registry.Utilization("cred-1", 0.98)
	registry.Utilization("cred-1", 0.40)
	if got := registry.Snapshots()[0].MaxUtilization; got != 0.98 {
		t.Fatalf("max utilization = %v, want 0.98", got)
	}
}

func TestBoundedSetsReportOverflow(t *testing.T) {
	registry := NewRegistry(func() time.Time { return testNow })
	for i := 0; i < maxTrackedValues+5; i++ {
		registry.Observe("cred-1", "ip-"+string(rune('a'+i%26))+string(rune('a'+i/26)), "ua", "cli", "s", true)
	}
	snapshot := registry.Snapshots()[0]
	if !snapshot.Overflow {
		t.Fatal("exceeding the tracked set cap must raise Overflow")
	}
	if snapshot.DistinctIPs > maxTrackedValues {
		t.Fatalf("distinct IPs = %d, want at most %d", snapshot.DistinctIPs, maxTrackedValues)
	}
}

func TestResetClearsProfile(t *testing.T) {
	registry := NewRegistry(func() time.Time { return testNow })
	registry.Observe("cred-1", "1.1.1.1", "ua", "cli", "s", true)
	registry.Reset("cred-1")
	if got := registry.Snapshots(); len(got) != 0 {
		t.Fatalf("reset left %d profiles", len(got))
	}
}

func TestRiskSignalsNameTheMeasurement(t *testing.T) {
	snapshot := Snapshot{
		CredentialID:   "cred-1",
		DistinctIPs:    3,
		DistinctUAs:    2,
		DistinctEntry:  1,
		Blocked:        4,
		MaxUtilization: 0.96,
	}
	signals := snapshot.RiskSignals()
	if len(signals) != 4 {
		t.Fatalf("got %d signals, want 4: %v", len(signals), signals)
	}
}

func TestCleanProfileHasNoSignals(t *testing.T) {
	snapshot := Snapshot{CredentialID: "cred-1", DistinctIPs: 1, DistinctUAs: 1, DistinctEntry: 1, MaxUtilization: 0.2}
	if signals := snapshot.RiskSignals(); len(signals) != 0 {
		t.Fatalf("a clean profile produced signals: %v", signals)
	}
}

func TestNilRegistryIsSafe(t *testing.T) {
	var registry *Registry
	registry.Observe("cred-1", "1.1.1.1", "ua", "cli", "s", true)
	registry.Block("cred-1", "rule")
	registry.Pace("cred-1")
	registry.Utilization("cred-1", 1)
	registry.Reset("cred-1")
	if got := registry.Snapshots(); got != nil {
		t.Fatalf("nil registry snapshots = %v", got)
	}
}
