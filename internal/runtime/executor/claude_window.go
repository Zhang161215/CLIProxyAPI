package executor

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/antidetect/audit"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/antidetect/window"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// WindowGateEnv disables the quota-window admission check.
const WindowGateEnv = "CPA_ANTIDETECT_WINDOW_GATE"

var (
	claudeWindowOnce  sync.Once
	claudeWindowStore *window.Store

	// missingHeaderWarned rate-limits the "no quota headers" warning per
	// credential: without it, an upstream that renames the headers would
	// either flood the log or, worse, fail silently.
	missingHeaderMu       sync.Mutex
	missingHeaderWarnedAt = make(map[string]time.Time)
)

// MissingHeaderWarnInterval bounds how often one credential may report that a
// response carried no quota window headers.
const MissingHeaderWarnInterval = 10 * time.Minute

func claudeWindows() *window.Store {
	claudeWindowOnce.Do(func() {
		claudeWindowStore = window.NewStore(nil)
	})
	return claudeWindowStore
}

// setClaudeWindowStoreForTest swaps the store and returns a restore func.
func setClaudeWindowStoreForTest(store *window.Store) func() {
	previous := claudeWindows()
	claudeWindowStore = store
	claudeWindowOnce.Do(func() {})
	return func() { claudeWindowStore = previous }
}

func windowGateEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(WindowGateEnv))) {
	case "off", "0", "false", "disabled":
		return false
	default:
		return true
	}
}

// checkClaudeWindow refuses a request when the credential's own reported quota
// window is effectively full.
//
// This is admission control, not scheduling: the conductor still picks first.
// Refusing here is what stops the hammering loop, because the loop is what turns
// a quota problem into a pattern the upstream reads as non-human traffic. The
// refusal is transient-shaped (429) so the caller retries later instead of
// treating the credential as broken.
func checkClaudeWindow(auth *cliproxyauth.Auth) error {
	if !windowGateEnabled() {
		return nil
	}
	credentialID := authCredentialID(auth)
	if credentialID == "" {
		return nil
	}
	state := claudeWindows().State(credentialID)
	if state != window.StateBlocked {
		return nil
	}
	audit.Log(audit.Record{
		Event:        "quota_window",
		Decision:     audit.DecisionBlocked,
		CredentialID: audit.ID(credentialID),
		Detail:       "window at or above the block threshold",
	})
	return statusErr{
		code: http.StatusTooManyRequests,
		msg:  fmt.Sprintf("antidetect: credential quota window is full; retry after the window resets"),
	}
}

// observeClaudeWindow records how much quota room a credential had when it was
// used, both for the admission check above and for post-mortems: when an account
// is revoked, "was it running at 99% of a rolling window?" is only answerable if
// the answer was written down at the time.
func observeClaudeWindow(auth *cliproxyauth.Auth, headers http.Header) {
	if headers == nil {
		return
	}
	now := time.Now()
	snapshots := window.ParseHeaders(headers, now)
	if len(snapshots) == 0 {
		warnMissingWindowHeaders(auth, now)
		return
	}
	credentialID := authCredentialID(auth)
	claudeWindows().Observe(credentialID, snapshots)
	for _, snapshot := range snapshots {
		recordClaudeUtilization(auth, snapshot.Utilization)
	}

	for _, snapshot := range snapshots {
		state := window.Evaluate(snapshot, now)
		if state == window.StateNormal {
			continue
		}
		audit.Log(audit.Record{
			Event:        "quota_window",
			Decision:     audit.DecisionPaced,
			CredentialID: audit.ID(credentialID),
			Detail:       string(snapshot.Kind) + " " + string(state),
		})
	}
}

// warnMissingWindowHeaders reports that a response carried no usable quota
// window headers.
//
// The admission gate is only as good as those headers: if the upstream renames
// them, the gate keeps running and silently never blocks. A warning turns that
// into something an operator can see, and the per-credential interval keeps it
// from becoming a log flood on a large fleet.
func warnMissingWindowHeaders(auth *cliproxyauth.Auth, now time.Time) {
	credentialID := authCredentialID(auth)
	if credentialID == "" {
		return
	}
	missingHeaderMu.Lock()
	last, seen := missingHeaderWarnedAt[credentialID]
	if seen && now.Sub(last) < MissingHeaderWarnInterval {
		missingHeaderMu.Unlock()
		return
	}
	missingHeaderWarnedAt[credentialID] = now
	missingHeaderMu.Unlock()

	audit.Log(audit.Record{
		Event:        "quota_window_headers",
		Decision:     audit.DecisionMissing,
		CredentialID: audit.ID(credentialID),
		Detail:       "response carried no anthropic-ratelimit-unified-* headers; the window gate cannot act on this credential",
	})
}
