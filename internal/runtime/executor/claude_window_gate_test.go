package executor

import (
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/antidetect/window"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestCheckClaudeWindowAllowsWithoutObservation(t *testing.T) {
	restore := setClaudeWindowStoreForTest(window.NewStore(nil))
	defer restore()

	if errWindow := checkClaudeWindow(&cliproxyauth.Auth{ID: "cred-fresh"}); errWindow != nil {
		t.Fatalf("an unobserved credential must pass: %v", errWindow)
	}
}

func TestCheckClaudeWindowBlocksFullWindow(t *testing.T) {
	store := window.NewStore(nil)
	store.Observe("cred-full", []window.Snapshot{{
		Kind:        window.KindLong,
		Utilization: 0.98,
		ResetsAt:    time.Now().Add(6 * time.Hour),
		Observed:    time.Now(),
	}})
	restore := setClaudeWindowStoreForTest(store)
	defer restore()

	errWindow := checkClaudeWindow(&cliproxyauth.Auth{ID: "cred-full"})
	if errWindow == nil {
		t.Fatal("a full quota window must be refused")
	}
	statusErr, ok := errWindow.(statusErr)
	if !ok {
		t.Fatalf("error %T must be a statusErr", errWindow)
	}
	if statusErr.code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", statusErr.code)
	}
}

func TestCheckClaudeWindowCanBeDisabled(t *testing.T) {
	t.Setenv(WindowGateEnv, "off")

	store := window.NewStore(nil)
	store.Observe("cred-full", []window.Snapshot{{
		Kind:        window.KindLong,
		Utilization: 1,
		ResetsAt:    time.Now().Add(time.Hour),
		Observed:    time.Now(),
	}})
	restore := setClaudeWindowStoreForTest(store)
	defer restore()

	if errWindow := checkClaudeWindow(&cliproxyauth.Auth{ID: "cred-full"}); errWindow != nil {
		t.Fatalf("the gate must be switchable off: %v", errWindow)
	}
}

// The whole point of the gate: response headers become the next request's
// admission decision.
func TestObserveThenGateBlocksNextRequest(t *testing.T) {
	store := window.NewStore(nil)
	restore := setClaudeWindowStoreForTest(store)
	defer restore()

	headers := http.Header{}
	headers.Set("Anthropic-Ratelimit-Unified-5h-Utilization", "0.97")
	headers.Set("Anthropic-Ratelimit-Unified-5h-Reset", time.Now().Add(time.Hour).UTC().Format(time.RFC3339))

	auth := &cliproxyauth.Auth{ID: "cred-observe"}
	observeClaudeWindow(auth, headers)

	if errWindow := checkClaudeWindow(auth); errWindow == nil {
		t.Fatal("a request that reported a full window must be gated next time")
	}
}

func TestObserveClaudeWindowIgnoresEmptyHeaders(t *testing.T) {
	store := window.NewStore(nil)
	restore := setClaudeWindowStoreForTest(store)
	defer restore()

	auth := &cliproxyauth.Auth{ID: "cred-empty"}
	observeClaudeWindow(auth, nil)
	observeClaudeWindow(auth, http.Header{})

	if snapshots := store.Snapshot("cred-empty"); len(snapshots) != 0 {
		t.Fatalf("no headers must produce no snapshot, got %v", snapshots)
	}
}

// A blocked window must tell the caller when to come back; a bare 429 invites
// a retry storm.
func TestBlockedWindowCarriesRetryAfter(t *testing.T) {
	store := window.NewStore(nil)
	resetAt := time.Now().Add(2 * time.Hour)
	store.Observe("cred-reset", []window.Snapshot{{
		Kind:        window.KindShort,
		Utilization: 0.99,
		ResetsAt:    resetAt,
		Observed:    time.Now(),
	}})
	restore := setClaudeWindowStoreForTest(store)
	defer restore()

	errWindow := checkClaudeWindow(&cliproxyauth.Auth{ID: "cred-reset"})
	statusErr, ok := errWindow.(statusErr)
	if !ok {
		t.Fatalf("error %T must be a statusErr", errWindow)
	}
	if statusErr.retryAfter == nil {
		t.Fatal("a blocked window must carry Retry-After")
	}
	if *statusErr.retryAfter <= time.Hour {
		t.Fatalf("retry after %v, want roughly the reset distance", *statusErr.retryAfter)
	}
}

func TestBlockedWindowWithoutResetHasNoRetryAfter(t *testing.T) {
	store := window.NewStore(nil)
	store.Observe("cred-noreset", []window.Snapshot{{
		Kind:        window.KindShort,
		Utilization: 0.99,
		Observed:    time.Now(),
	}})
	restore := setClaudeWindowStoreForTest(store)
	defer restore()

	errWindow := checkClaudeWindow(&cliproxyauth.Auth{ID: "cred-noreset"})
	statusErr, ok := errWindow.(statusErr)
	if !ok {
		t.Fatalf("error %T must be a statusErr", errWindow)
	}
	if statusErr.retryAfter != nil {
		t.Fatalf("no reset time means no Retry-After, got %v", *statusErr.retryAfter)
	}
}
