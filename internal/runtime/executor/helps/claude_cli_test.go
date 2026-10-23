package helps

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeCLI writes a stand-in for the claude binary that prints the stream-json
// shapes the real one produces, so the runner is testable without a logged-in
// account or an upstream call.
func fakeCLI(t *testing.T, script string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "claude")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatalf("write fake cli: %v", err)
	}
	return path
}

func TestRunnerStreamsAssistantText(t *testing.T) {
	bin := fakeCLI(t, `cat > /dev/null
printf '%s\n' '{"type":"system","subtype":"init","session_id":"s1"}'
printf '%s\n' '{"type":"assistant","message":{"content":[{"type":"text","text":"hello "}]}}'
printf '%s\n' '{"type":"assistant","message":{"content":[{"type":"text","text":"world"}]}}'
printf '%s\n' '{"type":"result","result":"","usage":{"input_tokens":11,"output_tokens":7}}'
`)

	events, errRun := ClaudeCLIWorker{Binary: bin, Timeout: 30 * time.Second}.Run(context.Background(), "claude-sonnet-5", "hi")
	if errRun != nil {
		t.Fatalf("Run: %v", errRun)
	}

	var text strings.Builder
	var usage *ClaudeCLIUsage
	for event := range events {
		if event.Err != nil {
			t.Fatalf("stream error: %v", event.Err)
		}
		text.WriteString(event.Text)
		if event.Usage != nil {
			usage = event.Usage
		}
	}
	if text.String() != "hello world" {
		t.Fatalf("text = %q", text.String())
	}
	if usage == nil || usage.InputTokens != 11 || usage.OutputTokens != 7 {
		t.Fatalf("usage = %#v", usage)
	}
}

// A non-zero exit has to surface as an error rather than a silent empty reply.
func TestRunnerSurfacesFailure(t *testing.T) {
	bin := fakeCLI(t, "echo 'bad flag' >&2\nexit 3\n")
	events, errRun := ClaudeCLIWorker{Binary: bin, Timeout: 30 * time.Second}.Run(context.Background(), "", "hi")
	if errRun != nil {
		t.Fatalf("Run: %v", errRun)
	}
	var gotErr error
	for event := range events {
		if event.Err != nil {
			gotErr = event.Err
		}
	}
	if gotErr == nil {
		t.Fatal("expected an error from a failing CLI")
	}
	if !strings.Contains(gotErr.Error(), "bad flag") {
		t.Fatalf("error lost the CLI's message: %v", gotErr)
	}
}

// A missing binary is a configuration error, not a request error.
func TestRunnerReportsMissingBinary(t *testing.T) {
	_, errRun := ClaudeCLIWorker{Binary: "/nonexistent/claude"}.Run(context.Background(), "", "hi")
	if errRun == nil {
		t.Fatal("expected an error for a missing binary")
	}
}

func TestBuildPromptFoldsHistoryAndSystem(t *testing.T) {
	payload := []byte(`{
		"system":"be terse",
		"messages":[
			{"role":"user","content":"first"},
			{"role":"assistant","content":[{"type":"text","text":"reply one"}]},
			{"role":"user","content":[{"type":"text","text":"second"},{"type":"image","source":{}}]}
		]}`)

	system, prompt, errBuild := BuildClaudeCLIPrompt(payload)
	if errBuild != nil {
		t.Fatalf("BuildClaudeCLIPrompt: %v", errBuild)
	}
	if system != "be terse" {
		t.Fatalf("system = %q", system)
	}
	for _, want := range []string{"User: first", "Assistant: reply one", "User: second"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "[image]") {
		t.Fatal("image blocks should not be rendered into the prompt")
	}
}

func TestBuildPromptRejectsEmptyPayload(t *testing.T) {
	if _, _, errBuild := BuildClaudeCLIPrompt([]byte(`{"messages":[]}`)); errBuild == nil {
		t.Fatal("expected an error for a payload with no messages")
	}
}

func TestPayloadModel(t *testing.T) {
	if got := ClaudePayloadModel([]byte(`{"model":"claude-opus-4-20250514"}`)); got != "claude-opus-4-20250514" {
		t.Fatalf("model = %q", got)
	}
	if got := ClaudePayloadModel([]byte(`{}`)); got != "" {
		t.Fatalf("model = %q, want empty", got)
	}
}
