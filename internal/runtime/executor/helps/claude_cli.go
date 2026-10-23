package helps

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// ClaudeCLIWorker runs the official Claude Code binary instead of building an
// HTTP request.
//
// The difference is not cosmetic. Everything the upstream verifies about a
// client — attestation, telemetry, TLS, session state — is produced by the
// binary itself, so the request is native by construction rather than native by
// imitation. The cost is a process per request and a coarser stream: the CLI
// emits whole assistant messages, not tokens.
type ClaudeCLIWorker struct {
	// Binary is the claude executable. Empty resolves to "claude" on PATH.
	Binary string
	// ConfigDir holds the logged-in account for this worker. Each credential
	// needs its own directory; sharing one would put every request on a single
	// account regardless of which credential the pool selected.
	ConfigDir string
	// ExtraArgs appends to the CLI invocation.
	ExtraArgs []string
	// Timeout bounds one invocation. Zero uses the default.
	Timeout time.Duration
}

const ClaudeCLIDefaultTimeout = 10 * time.Minute

// ClaudeCLIEvent is one increment produced while the worker runs.
type ClaudeCLIEvent struct {
	// Text is assistant text. Empty for events that only carry metadata.
	Text string
	// Usage is set once, on the terminal event.
	Usage *ClaudeCLIUsage
	// Err ends the stream with a failure.
	Err error
}

// ClaudeCLIUsage mirrors the token counters the CLI reports.
type ClaudeCLIUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

// Run starts one CLI invocation and streams its events.
//
// The returned channel is closed when the process exits. Cancelling ctx kills
// the process, which is the only way to stop a CLI mid-turn.
func (w ClaudeCLIWorker) Run(ctx context.Context, model, prompt string) (<-chan ClaudeCLIEvent, error) {
	binary := strings.TrimSpace(w.Binary)
	if binary == "" {
		binary = "claude"
	}
	resolved, errLookPath := exec.LookPath(binary)
	if errLookPath != nil {
		return nil, fmt.Errorf("claude CLI not found (%s): %w", binary, errLookPath)
	}

	timeout := w.Timeout
	if timeout <= 0 {
		timeout = ClaudeCLIDefaultTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)

	args := []string{"-p", "--output-format", "stream-json", "--verbose"}
	if trimmed := strings.TrimSpace(model); trimmed != "" {
		args = append(args, "--model", trimmed)
	}
	args = append(args, w.ExtraArgs...)

	cmd := exec.CommandContext(runCtx, resolved, args...)
	cmd.Stdin = strings.NewReader(prompt)
	if dir := strings.TrimSpace(w.ConfigDir); dir != "" {
		cmd.Env = append(os.Environ(), "CLAUDE_CONFIG_DIR="+dir)
	}

	stdout, errPipe := cmd.StdoutPipe()
	if errPipe != nil {
		cancel()
		return nil, fmt.Errorf("claude CLI stdout: %w", errPipe)
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr

	if errStart := cmd.Start(); errStart != nil {
		cancel()
		return nil, fmt.Errorf("start claude CLI: %w", errStart)
	}

	events := make(chan ClaudeCLIEvent, 8)
	go func() {
		defer close(events)
		defer cancel()

		scanner := bufio.NewScanner(stdout)
		// Tool output can be large; the default 64 KB line cap is not enough.
		scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

		var emitted string
		var usage *ClaudeCLIUsage
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			text, lineUsage, isResult, errParse := ParseClaudeCLILine(line)
			if errParse != nil {
				continue
			}
			if lineUsage != nil {
				usage = lineUsage
			}
			if text != "" {
				emitted += text
				select {
				case events <- ClaudeCLIEvent{Text: text}:
				case <-runCtx.Done():
					return
				}
			}
			if isResult {
				break
			}
		}
		if errScan := scanner.Err(); errScan != nil && runCtx.Err() == nil {
			events <- ClaudeCLIEvent{Err: fmt.Errorf("read claude CLI output: %w", errScan)}
			return
		}

		if errWait := cmd.Wait(); errWait != nil && runCtx.Err() == nil {
			msg := strings.TrimSpace(stderr.String())
			if msg == "" {
				msg = errWait.Error()
			}
			events <- ClaudeCLIEvent{Err: fmt.Errorf("claude CLI failed: %s", msg)}
			return
		}
		if runCtx.Err() != nil {
			events <- ClaudeCLIEvent{Err: runCtx.Err()}
			return
		}
		if strings.TrimSpace(emitted) == "" {
			events <- ClaudeCLIEvent{Err: fmt.Errorf("claude CLI produced no assistant text")}
			return
		}
		events <- ClaudeCLIEvent{Usage: usage}
	}()
	return events, nil
}

// ParseClaudeCLILine decodes one stream-json line.
//
// The CLI interleaves several event shapes. Only assistant text and the terminal
// result matter here; the rest (init, tool traffic, thinking) is skipped rather
// than passed through, because the downstream translation expects assistant
// text and nothing else.
func ParseClaudeCLILine(line string) (text string, usage *ClaudeCLIUsage, isResult bool, err error) {
	var envelope struct {
		Type    string `json:"type"`
		Result  string `json:"result"`
		Message struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			Usage *ClaudeCLIUsage `json:"usage"`
		} `json:"message"`
		Usage *ClaudeCLIUsage `json:"usage"`
	}
	if errUnmarshal := json.Unmarshal([]byte(line), &envelope); errUnmarshal != nil {
		return "", nil, false, errUnmarshal
	}

	switch envelope.Type {
	case "assistant":
		var builder strings.Builder
		for _, block := range envelope.Message.Content {
			if block.Type == "text" {
				builder.WriteString(block.Text)
			}
		}
		return builder.String(), envelope.Message.Usage, false, nil
	case "result":
		return envelope.Result, envelope.Usage, true, nil
	default:
		return "", nil, false, nil
	}
}

// BuildClaudeCLIPrompt renders a Messages payload as the single prompt the CLI
// accepts.
//
// History is folded into the prompt instead of being replayed through the CLI's
// own session resume: resume would bind the conversation to one CLI session id,
// and the pool has to stay free to serve the next request from any worker.
func BuildClaudeCLIPrompt(payload []byte) (string, string, error) {
	var body struct {
		System   json.RawMessage `json:"system"`
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(payload, &body); err != nil {
		return "", "", fmt.Errorf("parse messages: %w", err)
	}
	if len(body.Messages) == 0 {
		return "", "", fmt.Errorf("no messages in payload")
	}

	system := FlattenClaudeCLIContent(body.System)
	var turns strings.Builder
	for _, message := range body.Messages {
		text := FlattenClaudeCLIContent(message.Content)
		if strings.TrimSpace(text) == "" {
			continue
		}
		switch message.Role {
		case "assistant":
			turns.WriteString("Assistant: ")
		default:
			turns.WriteString("User: ")
		}
		turns.WriteString(text)
		turns.WriteString("\n\n")
	}
	prompt := strings.TrimSpace(turns.String())
	if prompt == "" {
		return "", "", fmt.Errorf("no text content in messages")
	}
	return system, prompt, nil
}

// FlattenClaudeCLIContent turns a content field into plain text.
//
// Content is either a string or an array of blocks. Tool results are rendered as
// their text so the model still sees them; images and other non-text blocks are
// dropped because the CLI prompt is text.
func FlattenClaudeCLIContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		return asString
	}
	var blocks []struct {
		Type    string          `json:"type"`
		Text    string          `json:"text"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return ""
	}
	var builder strings.Builder
	for _, block := range blocks {
		switch block.Type {
		case "text":
			builder.WriteString(block.Text)
		case "tool_result":
			builder.WriteString(FlattenClaudeCLIContent(block.Content))
		default:
			// Thinking, tool_use and images have no text rendering here.
		}
	}
	return builder.String()
}

// ClaudePayloadModel reads the requested model from a Messages payload.
func ClaudePayloadModel(payload []byte) string {
	var body struct {
		Model string `json:"model"`
	}
	if errUnmarshal := json.Unmarshal(payload, &body); errUnmarshal != nil {
		return ""
	}
	return strings.TrimSpace(body.Model)
}
