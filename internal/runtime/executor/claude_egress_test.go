package executor

import (
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/antidetect/egress"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestCheckClaudeEgressDisabledAllowsAnything(t *testing.T) {
	restore := setClaudeEgressPolicyForTest(egress.Config{}, egress.NewBinder())
	defer restore()

	auth := &cliproxyauth.Auth{ID: "cred-1"}
	if errEgress := checkClaudeEgress(auth); errEgress != nil {
		t.Fatalf("enforcement off must allow an unbound credential: %v", errEgress)
	}
}

func TestCheckClaudeEgressRefusesUnboundCredential(t *testing.T) {
	cfg := egress.Config{Enforce: true, Pools: map[string]egress.Pool{"p1": {ProxyURL: "socks5://h:1"}}}
	restore := setClaudeEgressPolicyForTest(cfg, egress.NewBinder())
	defer restore()

	auth := &cliproxyauth.Auth{ID: "cred-unbound"}
	errEgress := checkClaudeEgress(auth)
	if errEgress == nil {
		t.Fatal("an unbound credential must be refused under enforcement")
	}
	statusErr, ok := errEgress.(statusErr)
	if !ok {
		t.Fatalf("error %T must be a statusErr", errEgress)
	}
	if statusErr.code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", statusErr.code)
	}
}

func TestCheckClaudeEgressAcceptsBoundCredential(t *testing.T) {
	cfg := egress.Config{Enforce: true, Pools: map[string]egress.Pool{"p1": {ProxyURL: "socks5://h:1"}}}
	restore := setClaudeEgressPolicyForTest(cfg, egress.NewBinder())
	defer restore()

	auth := &cliproxyauth.Auth{
		ID:         "cred-bound",
		Attributes: map[string]string{egress.BindingEnv: "p1"},
	}
	if errEgress := checkClaudeEgress(auth); errEgress != nil {
		t.Fatalf("a bound credential must pass: %v", errEgress)
	}
}

// Reading the binding from metadata keeps credentials imported before the
// attribute existed working without a re-login.
func TestCheckClaudeEgressReadsMetadataFallback(t *testing.T) {
	cfg := egress.Config{Enforce: true, Pools: map[string]egress.Pool{"p1": {ProxyURL: "socks5://h:1"}}}
	restore := setClaudeEgressPolicyForTest(cfg, egress.NewBinder())
	defer restore()

	auth := &cliproxyauth.Auth{
		ID:       "cred-meta",
		Metadata: map[string]any{egress.BindingEnv: "p1"},
	}
	if errEgress := checkClaudeEgress(auth); errEgress != nil {
		t.Fatalf("a metadata binding must be honoured: %v", errEgress)
	}
}

// Two credentials on one exit is the linkage this check exists to stop.
func TestCheckClaudeEgressRefusesSharedExit(t *testing.T) {
	cfg := egress.Config{Enforce: true, Pools: map[string]egress.Pool{"p1": {ProxyURL: "socks5://h:1"}}}
	restore := setClaudeEgressPolicyForTest(cfg, egress.NewBinder())
	defer restore()

	first := &cliproxyauth.Auth{ID: "cred-a", Attributes: map[string]string{egress.BindingEnv: "p1"}}
	if errEgress := checkClaudeEgress(first); errEgress != nil {
		t.Fatalf("first credential must pass: %v", errEgress)
	}

	second := &cliproxyauth.Auth{ID: "cred-b", Attributes: map[string]string{egress.BindingEnv: "p1"}}
	if errEgress := checkClaudeEgress(second); errEgress == nil {
		t.Fatal("a second credential on the same exit must be refused")
	}
}
