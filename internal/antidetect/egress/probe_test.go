package egress

import (
	"context"
	"errors"
	"testing"
	"time"
)

func testConfig() Config {
	return Config{
		Enforce: true,
		Pools: map[string]Pool{
			"hk-1": {ProxyURL: "socks5://h:1080"},
			"us-1": {ProxyURL: "socks5://u:1080"},
		},
	}
}

// sequenceProbe returns canned addresses in order, one per call.
func sequenceProbe(addresses ...string) ProbeFunc {
	index := 0
	return func(context.Context, string) (string, error) {
		if index >= len(addresses) {
			return addresses[len(addresses)-1], nil
		}
		address := addresses[index]
		index++
		return address, nil
	}
}

func TestWatcherRecordsFirstAddressWithoutDrift(t *testing.T) {
	watcher := NewWatcher(sequenceProbe("1.1.1.1"), time.Minute, nil)
	if drifts := watcher.CheckOnce(context.Background(), testConfig()); len(drifts) != 0 {
		t.Fatalf("the first observation is not drift: %v", drifts)
	}
}

func TestWatcherReportsChangedAddress(t *testing.T) {
	var reported []Drift
	watcher := NewWatcher(sequenceProbe("1.1.1.1", "2.2.2.2"), time.Minute, func(drift Drift) {
		reported = append(reported, drift)
	})

	cfg := Config{Pools: map[string]Pool{"hk-1": {ProxyURL: "socks5://h:1080"}}}
	watcher.CheckOnce(context.Background(), cfg)
	drifts := watcher.CheckOnce(context.Background(), cfg)

	if len(drifts) != 1 {
		t.Fatalf("drifts = %v, want one", drifts)
	}
	if drifts[0].Before != "1.1.1.1" || drifts[0].After != "2.2.2.2" {
		t.Fatalf("drift = %+v", drifts[0])
	}
	if len(reported) != 1 {
		t.Fatalf("callback fired %d times, want 1", len(reported))
	}
}

// A failed probe must not clear the remembered address, or the next success
// would look like a move.
func TestWatcherIgnoresProbeFailure(t *testing.T) {
	calls := 0
	probe := func(context.Context, string) (string, error) {
		calls++
		if calls == 2 {
			return "", errors.New("timeout")
		}
		return "1.1.1.1", nil
	}
	watcher := NewWatcher(probe, time.Minute, nil)
	cfg := Config{Pools: map[string]Pool{"hk-1": {ProxyURL: "socks5://h:1080"}}}

	watcher.CheckOnce(context.Background(), cfg)
	if drifts := watcher.CheckOnce(context.Background(), cfg); len(drifts) != 0 {
		t.Fatalf("a failed probe must not report drift: %v", drifts)
	}
	if drifts := watcher.CheckOnce(context.Background(), cfg); len(drifts) != 0 {
		t.Fatalf("the address did not actually change: %v", drifts)
	}
}

func TestWatcherSkipsPoolsWithoutProxy(t *testing.T) {
	probed := 0
	probe := func(context.Context, string) (string, error) {
		probed++
		return "1.1.1.1", nil
	}
	watcher := NewWatcher(probe, time.Minute, nil)
	watcher.CheckOnce(context.Background(), Config{Pools: map[string]Pool{"direct": {}}})
	if probed != 0 {
		t.Fatalf("a pool without a proxy URL must not be probed, probed %d", probed)
	}
}

func TestWatcherTracksPoolsIndependently(t *testing.T) {
	probe := func(_ context.Context, proxyURL string) (string, error) {
		if proxyURL == "socks5://h:1080" {
			return "1.1.1.1", nil
		}
		return "9.9.9.9", nil
	}
	watcher := NewWatcher(probe, time.Minute, nil)
	if drifts := watcher.CheckOnce(context.Background(), testConfig()); len(drifts) != 0 {
		t.Fatalf("first pass reported drift: %v", drifts)
	}
	if drifts := watcher.CheckOnce(context.Background(), testConfig()); len(drifts) != 0 {
		t.Fatalf("stable addresses reported drift: %v", drifts)
	}
}

func TestNilWatcherIsSafe(t *testing.T) {
	var watcher *Watcher
	if drifts := watcher.CheckOnce(context.Background(), testConfig()); drifts != nil {
		t.Fatalf("nil watcher returned %v", drifts)
	}
	watcher.Run(context.Background(), testConfig())
}
