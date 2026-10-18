package executor

import (
	"context"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/antidetect/audit"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/antidetect/egress"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

var (
	egressOnce   sync.Once
	egressConfig egress.Config
	egressBinder *egress.Binder
)

// claudeEgressPolicy loads the operator's egress policy once.
func claudeEgressPolicy() (egress.Config, *egress.Binder) {
	egressOnce.Do(func() {
		cfg, errConfig := egress.ConfigFromEnv()
		if errConfig != nil {
			// A malformed policy disables enforcement instead of stopping the
			// service; the warning is the operator's signal to fix it.
			log.Warnf("antidetect: %v (egress enforcement disabled)", errConfig)
			egressConfig = egress.Config{}
		} else {
			egressConfig = cfg
		}
		egressBinder = egress.NewBinder()
		startEgressWatcher(egressConfig)
	})
	return egressConfig, egressBinder
}

// EgressEchoEnv overrides the address used to learn an exit's public IP.
const EgressEchoEnv = "CPA_ANTIDETECT_EGRESS_ECHO"

// startEgressWatcher records each exit's address and reports changes.
//
// A binding is only as good as the address behind it: when an ISP or a proxy
// vendor rotates it, the credential keeps working and quietly stops matching the
// exit it was bound to. Watching for the change is the only way to notice.
func startEgressWatcher(cfg egress.Config) {
	if len(cfg.Pools) == 0 {
		return
	}
	watcher := egress.NewWatcher(egress.HTTPProbe(os.Getenv(EgressEchoEnv)), 10*time.Minute, recordEgressDrift)
	go func() {
		// Establish the baseline immediately, then keep watching.
		watcher.CheckOnce(context.Background(), cfg)
		watcher.Run(context.Background(), cfg)
	}()
}

// recordEgressDrift writes one audit line per changed exit.
func recordEgressDrift(drift egress.Drift) {
	audit.Log(audit.Record{
		Event:    "egress_drift",
		Decision: audit.DecisionDrift,
		Detail: "pool " + drift.PoolID + " moved from " + audit.ID(drift.Before) +
			" to " + audit.ID(drift.After),
	})
}

// setClaudeEgressPolicyForTest swaps the policy and returns a restore func.
func setClaudeEgressPolicyForTest(cfg egress.Config, binder *egress.Binder) func() {
	previousConfig, previousBinder := claudeEgressPolicy()
	egressConfig, egressBinder = cfg, binder
	egressOnce.Do(func() {})
	return func() { egressConfig, egressBinder = previousConfig, previousBinder }
}

// declaredEgressID reads the credential's exit binding.
//
// Attributes hold operator configuration; metadata holds what the login flow
// persisted. Attributes win, because an operator overriding a stored value is
// making an explicit decision.
func declaredEgressID(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return ""
	}
	if value, ok := auth.Attributes[egress.BindingEnv]; ok {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	if auth.Metadata != nil {
		if value, ok := auth.Metadata[egress.BindingEnv].(string); ok {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// checkClaudeEgress validates the credential's exit binding before the request
// leaves. Under enforcement an unbound credential is refused rather than sent
// direct: a silent direct connection is exactly the linkage this prevents.
func checkClaudeEgress(auth *cliproxyauth.Auth) error {
	cfg, binder := claudeEgressPolicy()
	if !cfg.Enforce {
		return nil
	}
	credentialID := authCredentialID(auth)
	declared := declaredEgressID(auth)

	if _, errCheck := egress.Check(cfg, binder, credentialID, declared); errCheck != nil {
		audit.Log(audit.Record{
			Event:        "egress_binding",
			Decision:     audit.DecisionBlocked,
			CredentialID: audit.ID(credentialID),
			Detail:       errCheck.Error(),
		})
		return statusErr{code: http.StatusConflict, msg: errCheck.Error()}
	}
	return nil
}
