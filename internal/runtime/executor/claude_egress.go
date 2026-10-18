package executor

import (
	"net/http"
	"strings"
	"sync"

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
	})
	return egressConfig, egressBinder
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
