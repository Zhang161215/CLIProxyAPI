package executor

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/antidetect/fingerprint"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

const sharedWorkspaceBody = `{"system":[{"type":"text","text":"Working directory: /Users/alice/code/billing"}],"messages":[]}`

func TestFingerprintCollisionAcrossCredentials(t *testing.T) {
	registry := fingerprint.NewRegistry()
	restore := setClaudeFingerprintRegistryForTest(registry)
	defer restore()

	recordClaudeFingerprint(&cliproxyauth.Auth{ID: "cred-a"}, []byte(sharedWorkspaceBody))
	if got := registry.ForCredential("cred-a"); len(got) == 0 {
		t.Fatal("the first credential must record its workspace")
	}

	recordClaudeFingerprint(&cliproxyauth.Auth{ID: "cred-b"}, []byte(sharedWorkspaceBody))
	if got := registry.ForCredential("cred-b"); len(got) == 0 {
		t.Fatal("the second credential must record its workspace")
	}
}

func TestFingerprintNeverBlocksAndIgnoresAnonymous(t *testing.T) {
	registry := fingerprint.NewRegistry()
	restore := setClaudeFingerprintRegistryForTest(registry)
	defer restore()

	// No credential id: nothing to attribute, nothing recorded.
	recordClaudeFingerprint(&cliproxyauth.Auth{}, []byte(sharedWorkspaceBody))
	if got := registry.ForCredential(""); len(got) != 0 {
		t.Fatalf("an anonymous credential recorded %v", got)
	}
}

func TestHashedIDsAreOpaque(t *testing.T) {
	got := hashedIDs([]string{"cred-secret"})
	if len(got) != 1 || got[0] == "cred-secret" {
		t.Fatalf("hashedIDs leaked the raw id: %v", got)
	}
}
