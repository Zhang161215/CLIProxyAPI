package executor

import (
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/antidetect/audit"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/antidetect/content"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// ContentGateEnv disables the pre-upstream content gate. It exists so an
// operator can turn the gate off without a rebuild if a rule misfires in
// production; production defaults to enabled.
const ContentGateEnv = "CPA_ANTIDETECT_CONTENT_GATE"

// claudeContentBlockError rejects a request before it reaches an upstream.
//
// It carries a status code so the handler layer answers 403 with the rule
// category instead of a generic upstream failure, and it is request-scoped so
// the scheduler does not cool the credential down for a caller's mistake.
type claudeContentBlockError struct {
	verdict content.Verdict
}

func (e *claudeContentBlockError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("request blocked by content policy (%s)", e.verdict.Category)
}

// StatusCode answers 403: the request is refused, the account is fine.
func (e *claudeContentBlockError) StatusCode() int {
	return http.StatusForbidden
}

// IsRequestScoped keeps the block away from the credential cooldown path.
func (e *claudeContentBlockError) IsRequestScoped() bool { return true }

// IsCredentialScoped is false: the credential did nothing wrong.
func (e *claudeContentBlockError) IsCredentialScoped() bool { return false }

// contentGateEnabled reports whether the gate should run.
func contentGateEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(ContentGateEnv))) {
	case "off", "0", "false", "disabled":
		return false
	default:
		return true
	}
}

// gateClaudeContent rejects a payload whose latest user turn matches a content
// rule. It never mutates the payload: rejection is the whole contract, which
// keeps it clear of the payload-rule barrier.
func gateClaudeContent(auth *cliproxyauth.Auth, payload []byte) error {
	if !contentGateEnabled() {
		return nil
	}
	verdict := content.Evaluate(payload)
	if !verdict.Blocked {
		return nil
	}
	recordClaudeBlock(auth, verdict.RuleID)
	audit.Log(audit.Record{
		Event:        "content_gate",
		Decision:     audit.DecisionBlocked,
		CredentialID: audit.ID(authCredentialID(auth)),
		Rule:         verdict.RuleID,
		Category:     string(verdict.Category),
		Excerpt:      verdict.Excerpt,
	})
	return &claudeContentBlockError{verdict: verdict}
}

// authCredentialID returns a stable identifier for audit correlation.
func authCredentialID(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return ""
	}
	if id := strings.TrimSpace(auth.ID); id != "" {
		return id
	}
	return strings.TrimSpace(auth.Index)
}
