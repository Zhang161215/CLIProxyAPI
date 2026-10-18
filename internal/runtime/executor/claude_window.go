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
)

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
