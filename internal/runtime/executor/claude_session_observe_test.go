package executor

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// Relay clients never send the session header, so the profile has to fall back
// to the session this fork will actually present upstream. Without that, every
// credential reported sessions=0 and the one number worth watching was invisible.
func TestSessionIDFallsBackToTheOutboundSession(t *testing.T) {
	ctx := context.Background()
	got := sessionIDFromContext(ctx, "key-relay-1")
	if got == "" {
		t.Fatal("no session id recorded for a relay client")
	}
	if again := sessionIDFromContext(ctx, "key-relay-1"); again != got {
		t.Fatalf("session changed between observations: %q vs %q", got, again)
	}
}

// An inbound session header still wins: a real client's own session id is the
// truth, and overriding it would misreport that client.
func TestSessionIDPrefersTheInboundHeader(t *testing.T) {
	header := http.Header{}
	header.Set("X-Claude-Code-Session-Id", "inbound-session")
	ctx := helps.WithIncomingHeaders(context.Background(), header)

	if got := sessionIDFromContext(ctx, "key-relay-2"); got != "inbound-session" {
		t.Fatalf("session = %q, want the inbound header", got)
	}
}

func TestSessionIDIsEmptyWithoutAnAPIKey(t *testing.T) {
	if got := sessionIDFromContext(context.Background(), ""); got != "" {
		t.Fatalf("session = %q, want empty", got)
	}
}

var _ = cliproxyauth.Auth{}
var _ = strings.TrimSpace
