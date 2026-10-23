package executor

import (
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/antidetect/audit"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/antidetect/breaker"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

var (
	claudeBreakerOnce sync.Once
	claudeBreakerPtr  *breaker.Breaker
)

// claudeBreaker returns the process-wide 429 breaker.
//
// A quota window answers "is this credential full". This answers "is upstream
// rejecting it right now", which is a different question and, per case 2026-10,
// the one that decides whether an account survives.
func claudeBreaker() *breaker.Breaker {
	claudeBreakerOnce.Do(func() {
		opts, enabled := breaker.OptionsFromEnv()
		if !enabled {
			return
		}
		claudeBreakerPtr = breaker.New(opts)
	})
	return claudeBreakerPtr
}

// awaitClaudeBreaker refuses a request while the credential is cooling down.
//
// The refusal is local: nothing reaches upstream, so a credential that is being
// throttled stops producing the retry storm that turns throttling into a ban.
func awaitClaudeBreaker(auth *cliproxyauth.Auth) error {
	key := authCredentialID(auth)
	if key == "" {
		return nil
	}
	remaining, blocked := claudeBreaker().Blocked(key)
	if !blocked {
		return nil
	}
	audit.Log(audit.Record{
		Event:        "breaker",
		Decision:     audit.DecisionBlocked,
		CredentialID: audit.ID(key),
		Detail:       "cooling down after repeated upstream 429s, " + remaining.Round(time.Second).String() + " remaining",
	})
	return statusErr{
		code:       http.StatusTooManyRequests,
		msg:        fmt.Sprintf("antidetect: this credential is cooling down after repeated upstream 429s (%s remaining)", remaining.Round(time.Second)),
		retryAfter: &remaining,
	}
}

// recordClaudeBreakerTraffic notes an upstream 429 and reports whether it tripped.
func recordClaudeBreakerTraffic(auth *cliproxyauth.Auth) {
	key := authCredentialID(auth)
	if key == "" {
		return
	}
	if !claudeBreaker().Record(key) {
		return
	}
	audit.Log(audit.Record{
		Event:        "breaker",
		Decision:     audit.DecisionBlocked,
		CredentialID: audit.ID(key),
		Detail:       "tripped after repeated upstream 429s; pausing this credential",
	})
}
