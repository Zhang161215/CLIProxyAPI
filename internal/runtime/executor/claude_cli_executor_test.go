package executor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func cliModeExecutor(t *testing.T, script string) *ClaudeExecutor {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "claude")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatalf("write fake cli: %v", err)
	}
	cfg := &config.Config{}
	cfg.Claude.CLI.Enabled = true
	cfg.Claude.CLI.Binary = bin
	return NewClaudeExecutor(cfg)
}

const happyCLI = `cat > /dev/null
printf '%s\n' '{"type":"assistant","message":{"content":[{"type":"text","text":"ok from cli"}]}}'
printf '%s\n' '{"type":"result","result":"","usage":{"input_tokens":3,"output_tokens":2}}'
`

func TestExecuteViaClaudeCLIReturnsMessage(t *testing.T) {
	executor := cliModeExecutor(t, happyCLI)
	auth := &cliproxyauth.Auth{Attributes: map[string]string{}}
	req := cliproxyexecutor.Request{
		Model:   "claude-sonnet-5",
		Payload: []byte(`{"model":"claude-sonnet-5","messages":[{"role":"user","content":"hi"}]}`),
	}

	resp, errExec := executor.Execute(context.Background(), auth, req, cliproxyexecutor.Options{})
	if errExec != nil {
		t.Fatalf("Execute: %v", errExec)
	}
	body := string(resp.Payload)
	if !strings.Contains(body, "ok from cli") {
		t.Fatalf("payload missing assistant text: %s", body)
	}
	if !strings.Contains(body, `"output_tokens":2`) {
		t.Fatalf("payload missing usage: %s", body)
	}
}

func TestExecuteStreamViaClaudeCLIEmitsAnthropicEvents(t *testing.T) {
	executor := cliModeExecutor(t, happyCLI)
	auth := &cliproxyauth.Auth{Attributes: map[string]string{}}
	req := cliproxyexecutor.Request{
		Model:   "claude-sonnet-5",
		Payload: []byte(`{"model":"claude-sonnet-5","messages":[{"role":"user","content":"hi"}]}`),
	}

	result, errStream := executor.ExecuteStream(context.Background(), auth, req, cliproxyexecutor.Options{})
	if errStream != nil {
		t.Fatalf("ExecuteStream: %v", errStream)
	}

	var builder strings.Builder
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream error: %v", chunk.Err)
		}
		builder.Write(chunk.Payload)
	}
	stream := builder.String()
	for _, want := range []string{
		"event: message_start",
		"event: content_block_start",
		`"text":"ok from cli"`,
		"event: content_block_stop",
		"event: message_delta",
		"event: message_stop",
	} {
		if !strings.Contains(stream, want) {
			t.Fatalf("stream missing %q:\n%s", want, stream)
		}
	}
}

// With the switch off the executor must keep using the HTTP path, so an
// unusable binary cannot break a normal deployment.
func TestCLIModeOffByDefault(t *testing.T) {
	executor := NewClaudeExecutor(&config.Config{})
	if _, enabled := executor.claudeCLIWorker(&cliproxyauth.Auth{}); enabled {
		t.Fatal("CLI mode must be opt-in")
	}
}

// A credential may point at its own logged-in directory.
func TestCLIModeUsesCredentialConfigDir(t *testing.T) {
	cfg := &config.Config{}
	cfg.Claude.CLI.Enabled = true
	cfg.Claude.CLI.ConfigDir = "/global"
	executor := NewClaudeExecutor(cfg)

	worker, enabled := executor.claudeCLIWorker(&cliproxyauth.Auth{
		Attributes: map[string]string{"cli_config_dir": "/credential"},
	})
	if !enabled {
		t.Fatal("CLI mode should be enabled")
	}
	if worker.ConfigDir != "/credential" {
		t.Fatalf("config dir = %q, want the credential override", worker.ConfigDir)
	}
}
