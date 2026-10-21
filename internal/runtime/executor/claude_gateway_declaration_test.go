package executor

import (
	"net/http"
	"testing"
)

// A gateway header must be dropped by meaning. The allowlist alone is not
// enough: any x-claude-code-* name would slip through it.
func TestGatewayHeadersDroppedByMeaning(t *testing.T) {
	resetGatewayDeclarationLog()
	cases := []string{
		"x-gateway-model",
		"x-gateway-upstream",
		"x-cc-gateway-version",
		"x-claude-gateway-user-email",
		"x-claude-code-gateway-hint", // would pass the x-claude-code- prefix
	}
	src := http.Header{}
	for _, name := range cases {
		src.Set(name, "value")
	}
	src.Set("x-claude-code-session-id", "keep-me")

	dst := http.Header{}
	copyClaudeCallerFingerprintHeaders(dst, src, true)

	for _, name := range cases {
		if got := dst.Get(name); got != "" {
			t.Fatalf("%s was forwarded: %q", name, got)
		}
	}
	if got := dst.Get("x-claude-code-session-id"); got != "keep-me" {
		t.Fatalf("legitimate header dropped: %q", got)
	}
}

// Normal headers must not be caught by the substring rule.
func TestNonGatewayHeadersSurvive(t *testing.T) {
	src := http.Header{}
	src.Set("x-stainless-lang", "js")
	src.Set("anthropic-version", "2023-06-01")
	dst := http.Header{}
	copyClaudeCallerFingerprintHeaders(dst, src, true)
	if dst.Get("x-stainless-lang") != "js" || dst.Get("anthropic-version") != "2023-06-01" {
		t.Fatalf("ordinary headers were dropped: %v", dst)
	}
}

func TestGatewayDeclarationIsLoggedOncePerChannel(t *testing.T) {
	resetGatewayDeclarationLog()
	recordGatewayDeclaration("header:x-gateway-model")
	recordGatewayDeclaration("header:x-gateway-model")
	gatewayDeclarationMu.Lock()
	count := len(gatewayDeclarationSeen)
	gatewayDeclarationMu.Unlock()
	if count != 1 {
		t.Fatalf("dedupe set size = %d, want 1", count)
	}
}
