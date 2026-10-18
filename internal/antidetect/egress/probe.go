package egress

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/proxyutil"
	"golang.org/x/net/proxy"
)

// DefaultEchoURL is the address used to learn an exit's public IP.
//
// It is configurable because a dependency on one third party is a liability:
// the check is useful, not critical, and must never take a request path down.
const DefaultEchoURL = "https://api.ipify.org"

// ProbeFunc returns the public address an exit currently presents.
type ProbeFunc func(ctx context.Context, proxyURL string) (string, error)

// Drift describes an exit whose address changed after it was first seen.
type Drift struct {
	PoolID string
	Before string
	After  string
}

// Watcher remembers the first address it saw per exit and reports changes.
//
// A change is not automatically a fault — an ISP can rotate an address — but it
// is exactly the event that quietly breaks a binding, so it must be visible.
type Watcher struct {
	probe    ProbeFunc
	interval time.Duration
	onDrift  func(Drift)

	mu   sync.Mutex
	seen map[string]string
}

// NewWatcher builds a watcher. A nil probe disables it.
func NewWatcher(probe ProbeFunc, interval time.Duration, onDrift func(Drift)) *Watcher {
	if interval <= 0 {
		interval = 10 * time.Minute
	}
	return &Watcher{
		probe:    probe,
		interval: interval,
		onDrift:  onDrift,
		seen:     make(map[string]string),
	}
}

// CheckOnce probes every configured exit and reports drift.
//
// It is exported separately from the loop so tests and one-shot operator checks
// do not need to start goroutines.
func (w *Watcher) CheckOnce(ctx context.Context, cfg Config) []Drift {
	if w == nil || w.probe == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	var drifts []Drift
	for poolID, pool := range cfg.Pools {
		if strings.TrimSpace(pool.ProxyURL) == "" {
			continue
		}
		probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		address, errProbe := w.probe(probeCtx, pool.ProxyURL)
		cancel()
		if errProbe != nil || strings.TrimSpace(address) == "" {
			// A failed probe says nothing about drift; it must not clear the
			// remembered address either, or the next success looks like a move.
			continue
		}
		address = strings.TrimSpace(address)

		w.mu.Lock()
		previous, exists := w.seen[poolID]
		if !exists {
			w.seen[poolID] = address
			w.mu.Unlock()
			continue
		}
		if previous != address {
			w.seen[poolID] = address
			w.mu.Unlock()
			drift := Drift{PoolID: poolID, Before: previous, After: address}
			drifts = append(drifts, drift)
			if w.onDrift != nil {
				w.onDrift(drift)
			}
			continue
		}
		w.mu.Unlock()
	}
	return drifts
}

// Run probes on an interval until the context is cancelled.
func (w *Watcher) Run(ctx context.Context, cfg Config) {
	if w == nil || w.probe == nil {
		return
	}
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.CheckOnce(ctx, cfg)
		}
	}
}

// HTTPProbe returns a ProbeFunc that asks an echo endpoint for the caller's
// address through the given exit.
func HTTPProbe(echoURL string) ProbeFunc {
	url := strings.TrimSpace(echoURL)
	if url == "" {
		url = DefaultEchoURL
	}
	return func(ctx context.Context, proxyURL string) (string, error) {
		dialer, _, errBuild := proxyutil.BuildDialer(proxyURL)
		if errBuild != nil {
			return "", fmt.Errorf("egress probe: build dialer: %w", errBuild)
		}
		transport := &http.Transport{
			DisableKeepAlives: true,
		}
		if contextDialer, ok := dialer.(proxy.ContextDialer); ok {
			transport.DialContext = contextDialer.DialContext
		} else {
			transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
				return dialer.Dial(network, addr)
			}
		}
		client := &http.Client{Transport: transport, Timeout: 15 * time.Second}
		defer transport.CloseIdleConnections()

		request, errRequest := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if errRequest != nil {
			return "", errRequest
		}
		response, errDo := client.Do(request)
		if errDo != nil {
			return "", errDo
		}
		defer func() {
			if errClose := response.Body.Close(); errClose != nil {
				_ = errClose
			}
		}()
		if response.StatusCode != http.StatusOK {
			return "", fmt.Errorf("egress probe: status %d", response.StatusCode)
		}
		body, errRead := io.ReadAll(io.LimitReader(response.Body, 128))
		if errRead != nil {
			return "", errRead
		}
		return strings.TrimSpace(string(body)), nil
	}
}
