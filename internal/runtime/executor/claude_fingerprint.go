package executor

import (
	"strings"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/antidetect/audit"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/antidetect/fingerprint"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

var (
	fingerprintOnce     sync.Once
	fingerprintRegistry *fingerprint.Registry
)

func claudeFingerprints() *fingerprint.Registry {
	fingerprintOnce.Do(func() {
		fingerprintRegistry = fingerprint.NewRegistry()
	})
	return fingerprintRegistry
}

// setClaudeFingerprintRegistryForTest swaps the registry and returns a restore func.
func setClaudeFingerprintRegistryForTest(registry *fingerprint.Registry) func() {
	previous := claudeFingerprints()
	fingerprintRegistry = registry
	fingerprintOnce.Do(func() {})
	return func() { fingerprintRegistry = previous }
}

// recordClaudeFingerprint hashes the workspace fragments inside a request and
// warns when another credential has produced the same ones.
//
// It never blocks and never stores raw values: sharing a workspace is not
// misconduct, but a fleet where every credential serves one repository is a
// fleet that reads as one person running many accounts.
func recordClaudeFingerprint(auth *cliproxyauth.Auth, payload []byte) {
	credentialID := authCredentialID(auth)
	if credentialID == "" {
		return
	}
	for _, collision := range claudeFingerprints().Observe(credentialID, payload) {
		audit.Log(audit.Record{
			Event:        "content_fingerprint",
			Decision:     audit.DecisionCollision,
			CredentialID: audit.ID(credentialID),
			Detail: "shared workspace fingerprint " + collision.Fingerprint +
				" also seen on " + strings.Join(hashedIDs(collision.Others), ","),
		})
	}
}

// hashedIDs keeps raw credential ids out of the log line.
func hashedIDs(ids []string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, audit.ID(id))
	}
	return out
}
