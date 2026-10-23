package executor

import (
	"net/http"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/antidetect/window"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// Utilization is the only real measure of remaining headroom, so the block level
// has to be adjustable: a deployment that spent a day at an average of 0.92 was
// put on hold, one at 0.57 was not.
func TestWindowGateUsesConfiguredThreshold(t *testing.T) {
	restore := setClaudeWindowStoreForTest(window.NewStore(nil))
	defer restore()
	resetWindowThresholdsForTest(t, 0.60, 0.70)

	auth := &cliproxyauth.Auth{ID: "cred-threshold"}
	headers := http.Header{}
	headers.Set("anthropic-ratelimit-unified-5h-utilization", "0.72")
	headers.Set("anthropic-ratelimit-unified-5h-reset", "9999999999")
	observeClaudeWindow(auth, headers)

	errGate := checkClaudeWindow(auth)
	if errGate == nil {
		t.Fatal("0.72 against a 0.70 block level must be refused")
	}
	if status, ok := errGate.(interface{ StatusCode() int }); !ok || status.StatusCode() != http.StatusTooManyRequests {
		t.Fatalf("error = %v, want 429", errGate)
	}
}

func TestWindowGateAllowsBelowConfiguredThreshold(t *testing.T) {
	restore := setClaudeWindowStoreForTest(window.NewStore(nil))
	defer restore()
	resetWindowThresholdsForTest(t, 0.60, 0.70)

	auth := &cliproxyauth.Auth{ID: "cred-threshold-ok"}
	headers := http.Header{}
	headers.Set("anthropic-ratelimit-unified-5h-utilization", "0.55")
	headers.Set("anthropic-ratelimit-unified-5h-reset", "9999999999")
	observeClaudeWindow(auth, headers)

	if errGate := checkClaudeWindow(auth); errGate != nil {
		t.Fatalf("0.55 is below the block level: %v", errGate)
	}
}

func TestWindowThresholdsFromEnv(t *testing.T) {
	t.Setenv(WindowBlockEnv, "0.80")
	t.Setenv(WindowDegradeEnv, "0.65")
	resetWindowThresholdsForTest(t, 0, 0)
	got := windowThresholds()
	if got.Blocked != 0.80 || got.Degraded != 0.65 {
		t.Fatalf("thresholds = %+v", got)
	}
}

// resetWindowThresholdsForTest re-seeds the cached thresholds. A zero pair means
// "read the environment".
func resetWindowThresholdsForTest(t *testing.T, degraded, blocked float64) {
	t.Helper()
	claudeWindowOnce = sync.Once{}
	if blocked > 0 {
		claudeWindowThresholds = window.Thresholds{Degraded: degraded, Blocked: blocked}
	}
}
