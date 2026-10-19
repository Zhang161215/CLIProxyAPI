package executor

import (
	"net/http"
	"testing"
)

// A header that only a newer client emits must not travel with a request that
// announces an older version.
func TestCopyClaudeCallerFingerprintHeadersDropsVersionGatedHeaders(t *testing.T) {
	src := http.Header{}
	src.Set("x-claude-code-wiggly-dove", "sig=abc")
	src.Set("x-claude-code-session-id", "session-1")
	dst := http.Header{}
	copyClaudeCallerFingerprintHeaders(dst, src, true)
	if got := dst.Get("x-claude-code-wiggly-dove"); got != "" {
		t.Fatalf("version-gated header was forwarded: %q", got)
	}
	if got := dst.Get("x-claude-code-session-id"); got != "session-1" {
		t.Fatalf("recognized header was dropped: %q", got)
	}
}
