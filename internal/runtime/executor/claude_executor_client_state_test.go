package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

// An entrypoint this build does not recognize must be passed through, even when
// an operator forced cloak_mode=always. Upstream ships new entrypoints
// regularly; rewriting an unrecognized one replaces a possibly genuine client
// fingerprint with a synthetic shape no client emits.
func TestClaudeExecutor_UnknownEntrypointIsNotCloaked(t *testing.T) {
	var seenBody []byte
	var seenHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenBody, _ = io.ReadAll(r.Body)
		seenHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","model":"claude-opus-5","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer server.Close()

	const callerSystem = "caller-system"
	const callerUA = "claude-cli/2.1.280 (external, future-surface, agent-sdk/0.9.9)"

	payload := []byte(`{"model":"claude-opus-5","system":"` + callerSystem + `","messages":[{"role":"user","content":"x"}]}`)
	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":    "key-unknown-entrypoint",
		"base_url":   server.URL,
		"cloak_mode": "always",
	}}
	_, errExecute := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-opus-5",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatClaude,
		OriginalRequest: payload,
		Headers: http.Header{
			"User-Agent":     {callerUA},
			"X-App":          {"cli"},
			"Anthropic-Beta": {"claude-code-20250219"},
		},
	})
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}

	if got := seenHeaders.Get("User-Agent"); got != callerUA {
		t.Fatalf("User-Agent = %q, want the caller value preserved (%q)", got, callerUA)
	}

	// The guarantee is "no fabricated Claude Code identity", not byte identity:
	// benign normalization (system string to block array, cache_control) is
	// allowed. What must not appear is a billing header, the CLI identity line,
	// or an injected conversation turn.
	system := gjson.GetBytes(seenBody, "system")
	if !system.IsArray() && system.Type != gjson.String {
		t.Fatalf("system = %s, want the caller system preserved", system.Raw)
	}
	rawSystem := system.Raw
	if strings.Contains(rawSystem, "x-anthropic-billing-header") {
		t.Fatalf("system = %s, must not gain a billing header for an unrecognized client", rawSystem)
	}
	if strings.Contains(rawSystem, claudeCodeCLIIdentity) {
		t.Fatalf("system = %s, must not gain the CLI identity block", rawSystem)
	}
	if !strings.Contains(rawSystem, callerSystem) {
		t.Fatalf("system = %s, want the caller text %q preserved", rawSystem, callerSystem)
	}
	if got := gjson.GetBytes(seenBody, "messages.#").Int(); got != 1 {
		t.Fatalf("message count = %d, want 1 (no injected turn)", got)
	}
}
