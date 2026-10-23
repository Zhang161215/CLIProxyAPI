package executor

import (
	"net/http"
	"testing"
)

// The allowlist is the set of headers a captured 2.1.280 client actually sends.
// Everything else must be dropped by default, so a future client cannot smuggle
// a new identity header past the proxy just by naming it like the others.
func TestForwardedHeadersCoverTheCapturedSet(t *testing.T) {
	captured := []string{
		"Accept", "Accept-Encoding", "User-Agent", "X-Claude-Code-Session-Id",
		"X-Stainless-Arch", "X-Stainless-Lang", "X-Stainless-OS",
		"X-Stainless-Package-Version", "X-Stainless-Retry-Count",
		"X-Stainless-Runtime", "X-Stainless-Runtime-Version", "X-Stainless-Timeout",
		"anthropic-beta", "anthropic-dangerous-direct-browser-access",
		"anthropic-version", "x-app", "x-claude-code-request-class",
		"x-client-request-id",
	}
	src := http.Header{}
	for _, name := range captured {
		src.Set(name, "v")
	}
	dst := http.Header{}
	copyClaudeCallerFingerprintHeaders(dst, src, true, false)
	for _, name := range captured {
		if dst.Get(name) != "v" {
			t.Fatalf("captured header %q was dropped", name)
		}
	}
}

func TestUnknownClaudeCodeHeaderIsDropped(t *testing.T) {
	src := http.Header{}
	src.Set("x-claude-code-something-new", "payload")
	src.Set("x-claude-code-session-id", "keep")
	dst := http.Header{}
	copyClaudeCallerFingerprintHeaders(dst, src, true, false)
	if got := dst.Get("x-claude-code-something-new"); got != "" {
		t.Fatalf("unreviewed header forwarded: %q", got)
	}
	if dst.Get("x-claude-code-session-id") != "keep" {
		t.Fatal("known header dropped")
	}
}

// A caller that is not recognised as Claude Code must not be able to inject the
// identity headers at all.
func TestUnconfirmedCallerCannotInjectClaudeHeaders(t *testing.T) {
	src := http.Header{}
	src.Set("x-claude-code-session-id", "injected")
	src.Set("x-stainless-lang", "js")
	dst := http.Header{}
	copyClaudeCallerFingerprintHeaders(dst, src, false, false)
	if got := dst.Get("x-claude-code-session-id"); got != "" {
		t.Fatalf("unconfirmed caller injected an identity header: %q", got)
	}
	if dst.Get("x-stainless-lang") != "js" {
		t.Fatal("ordinary header dropped for unconfirmed caller")
	}
}
