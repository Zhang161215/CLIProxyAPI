package executor

import (
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/antidetect/breaker"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// The gate has to return a paced 429 rather than letting the request through: a
// credential upstream is already rejecting must stop producing demand.
func TestAwaitClaudeBreakerRefusesWhileCoolingDown(t *testing.T) {
	installTestBreaker(t, breaker.Options{Threshold: 1, Window: time.Minute, Cooldown: time.Hour})
	auth := &cliproxyauth.Auth{ID: "cred-breaker-1"}

	recordClaudeBreakerTraffic(auth)
	errGate := awaitClaudeBreaker(auth)
	if errGate == nil {
		t.Fatal("cooling credential was allowed through")
	}
	status, ok := errGate.(interface{ StatusCode() int })
	if !ok || status.StatusCode() != http.StatusTooManyRequests {
		t.Fatalf("error = %T %v, want 429", errGate, errGate)
	}
	if rap, ok := errGate.(interface{ RetryAfter() *time.Duration }); !ok || rap.RetryAfter() == nil {
		t.Fatal("refusal must carry a Retry-After")
	}
}

func TestAwaitClaudeBreakerAllowsHealthyCredential(t *testing.T) {
	installTestBreaker(t, breaker.Options{Threshold: 5, Window: time.Minute, Cooldown: time.Hour})
	if errGate := awaitClaudeBreaker(&cliproxyauth.Auth{ID: "cred-breaker-2"}); errGate != nil {
		t.Fatalf("healthy credential refused: %v", errGate)
	}
}

func TestBreakerDisabledByEnv(t *testing.T) {
	t.Setenv(breaker.ConfigEnv, "off")
	resetClaudeBreakerForTest()
	if claudeBreaker() != nil {
		t.Fatal("breaker should be nil when disabled")
	}
	if errGate := awaitClaudeBreaker(&cliproxyauth.Auth{ID: "cred-breaker-3"}); errGate != nil {
		t.Fatalf("disabled breaker refused a request: %v", errGate)
	}
}

// installTestBreaker swaps in a breaker with a deterministic configuration.
func installTestBreaker(t *testing.T, opts breaker.Options) {
	t.Helper()
	resetClaudeBreakerForTest()
	claudeBreakerPtr = breaker.New(opts)
}

func resetClaudeBreakerForTest() {
	claudeBreakerOnce = sync.Once{}
	claudeBreakerOnce.Do(func() {})
	claudeBreakerPtr = nil
}
