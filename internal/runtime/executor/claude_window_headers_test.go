package executor

import (
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/antidetect/window"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// A response without quota headers must not pass silently: if the upstream
// renames them, the gate keeps running and never blocks, which looks healthy
// and is not.
func TestMissingWindowHeadersAreReportedOnce(t *testing.T) {
	restore := setClaudeWindowStoreForTest(window.NewStore(nil))
	defer restore()

	missingHeaderMu.Lock()
	missingHeaderWarnedAt = make(map[string]time.Time)
	missingHeaderMu.Unlock()

	auth := &cliproxyauth.Auth{ID: "cred-no-headers"}
	observeClaudeWindow(auth, http.Header{})

	missingHeaderMu.Lock()
	first, seen := missingHeaderWarnedAt["cred-no-headers"]
	missingHeaderMu.Unlock()
	if !seen {
		t.Fatal("a response without quota headers must be reported")
	}

	// A second response inside the interval must not report again.
	observeClaudeWindow(auth, http.Header{})
	missingHeaderMu.Lock()
	second := missingHeaderWarnedAt["cred-no-headers"]
	missingHeaderMu.Unlock()
	if !second.Equal(first) {
		t.Fatal("the warning must be rate limited per credential")
	}
}

func TestMissingWindowHeadersRecoverAfterInterval(t *testing.T) {
	restore := setClaudeWindowStoreForTest(window.NewStore(nil))
	defer restore()

	missingHeaderMu.Lock()
	missingHeaderWarnedAt = map[string]time.Time{"cred-2": time.Now().Add(-MissingHeaderWarnInterval - time.Minute)}
	missingHeaderMu.Unlock()

	observeClaudeWindow(&cliproxyauth.Auth{ID: "cred-2"}, http.Header{})

	missingHeaderMu.Lock()
	updated := missingHeaderWarnedAt["cred-2"]
	missingHeaderMu.Unlock()
	if time.Since(updated) > time.Minute {
		t.Fatal("the warning must be reissued after the interval elapses")
	}
}

// Headers that are present must not trigger the warning.
func TestPresentWindowHeadersDoNotWarn(t *testing.T) {
	restore := setClaudeWindowStoreForTest(window.NewStore(nil))
	defer restore()

	missingHeaderMu.Lock()
	missingHeaderWarnedAt = make(map[string]time.Time)
	missingHeaderMu.Unlock()

	headers := http.Header{}
	headers.Set("Anthropic-Ratelimit-Unified-5h-Utilization", "0.42")
	observeClaudeWindow(&cliproxyauth.Auth{ID: "cred-with-headers"}, headers)

	missingHeaderMu.Lock()
	_, warned := missingHeaderWarnedAt["cred-with-headers"]
	missingHeaderMu.Unlock()
	if warned {
		t.Fatal("a parsed response must not report missing headers")
	}
}
