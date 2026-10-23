package executor

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// claudeCLIEnabled reports whether this process serves Claude by running the
// official binary instead of building an HTTP request.
//
// The switch exists because the two modes fail in different ways. The HTTP path
// can be shaped byte for byte but is still assembled by this program; the CLI
// path is genuinely native but costs a process per request and streams whole
// messages rather than tokens. Which one a deployment wants depends on whether
// account survival or stream smoothness is the binding constraint.
func (e *ClaudeExecutor) claudeCLIWorker(auth *cliproxyauth.Auth) (helps.ClaudeCLIWorker, bool) {
	if e == nil || e.cfg == nil {
		return helps.ClaudeCLIWorker{}, false
	}
	cfg := e.cfg.Claude.CLI
	if !cfg.Enabled {
		return helps.ClaudeCLIWorker{}, false
	}
	worker := helps.ClaudeCLIWorker{
		Binary:    cfg.Binary,
		ConfigDir: cfg.ConfigDir,
		ExtraArgs: cfg.ExtraArgs,
	}
	if cfg.TimeoutSeconds > 0 {
		worker.Timeout = time.Duration(cfg.TimeoutSeconds) * time.Second
	}
	// A credential may point at its own logged-in directory; without that, every
	// request would run on whichever account the global directory holds.
	if auth != nil && auth.Attributes != nil {
		if dir := strings.TrimSpace(auth.Attributes["cli_config_dir"]); dir != "" {
			worker.ConfigDir = dir
		}
	}
	return worker, true
}

// executeViaClaudeCLI serves a non-streaming request through the binary.
func (e *ClaudeExecutor) executeViaClaudeCLI(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, worker helps.ClaudeCLIWorker) (cliproxyexecutor.Response, bool, error) {
	system, prompt, errPrompt := helps.BuildClaudeCLIPrompt(req.Payload)
	if errPrompt != nil {
		return cliproxyexecutor.Response{}, true, statusErr{code: http.StatusBadRequest, msg: errPrompt.Error()}
	}
	if system != "" {
		prompt = system + "\n\n" + prompt
	}

	model := helps.ClaudePayloadModel(req.Payload)
	events, errRun := worker.Run(ctx, model, prompt)
	if errRun != nil {
		return cliproxyexecutor.Response{}, true, statusErr{code: http.StatusBadGateway, msg: errRun.Error()}
	}

	var text strings.Builder
	usage := helps.ClaudeCLIUsage{}
	for event := range events {
		if event.Err != nil {
			return cliproxyexecutor.Response{}, true, statusErr{code: http.StatusBadGateway, msg: event.Err.Error()}
		}
		if event.Usage != nil {
			usage = *event.Usage
		}
		text.WriteString(event.Text)
	}

	body, errMarshal := json.Marshal(map[string]any{
		"id":          "msg_cli_" + helps.ClaudeDeterministicPromptID(prompt),
		"type":        "message",
		"role":        "assistant",
		"model":       model,
		"content":     []map[string]any{{"type": "text", "text": text.String()}},
		"stop_reason": "end_turn",
		"usage": map[string]any{
			"input_tokens":                usage.InputTokens,
			"output_tokens":               usage.OutputTokens,
			"cache_read_input_tokens":     usage.CacheReadInputTokens,
			"cache_creation_input_tokens": usage.CacheCreationInputTokens,
		},
	})
	if errMarshal != nil {
		return cliproxyexecutor.Response{}, true, statusErr{code: http.StatusInternalServerError, msg: errMarshal.Error()}
	}
	return cliproxyexecutor.Response{Payload: body, Headers: http.Header{}}, true, nil
}

// streamViaClaudeCLI serves a streaming request through the binary.
//
// The CLI reports whole assistant messages, so each one becomes a single
// content_block_delta. Clients see fewer, larger deltas than an HTTP stream
// would produce; the event sequence itself is the standard Anthropic one so
// translators and SDKs need no special case.
func (e *ClaudeExecutor) streamViaClaudeCLI(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, worker helps.ClaudeCLIWorker) (*cliproxyexecutor.StreamResult, bool, error) {
	system, prompt, errPrompt := helps.BuildClaudeCLIPrompt(req.Payload)
	if errPrompt != nil {
		return nil, true, statusErr{code: http.StatusBadRequest, msg: errPrompt.Error()}
	}
	if system != "" {
		prompt = system + "\n\n" + prompt
	}

	model := helps.ClaudePayloadModel(req.Payload)
	events, errRun := worker.Run(ctx, model, prompt)
	if errRun != nil {
		return nil, true, statusErr{code: http.StatusBadGateway, msg: errRun.Error()}
	}

	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		emit := func(payload string) bool {
			select {
			case out <- cliproxyexecutor.StreamChunk{Payload: []byte(payload)}:
				return true
			case <-ctx.Done():
				return false
			}
		}

		if !emit(`event: message_start` + "\n" + `data: {"type":"message_start","message":{"id":"msg_cli","type":"message","role":"assistant","model":` + claudeCLIJSONQuote(model) + `,"content":[],"stop_reason":null,"usage":{"input_tokens":0,"output_tokens":0}}}` + "\n\n") {
			return
		}
		emit(`event: content_block_start` + "\n" + `data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n")

		var usage helps.ClaudeCLIUsage
		for event := range events {
			if event.Err != nil {
				select {
				case out <- cliproxyexecutor.StreamChunk{Err: event.Err}:
				case <-ctx.Done():
				}
				return
			}
			if event.Usage != nil {
				usage = *event.Usage
			}
			if event.Text != "" {
				if !emit(`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":` + claudeCLIJSONQuote(event.Text) + `}}` + "\n\n") {
					return
				}
			}
		}

		emit(`event: content_block_stop` + "\n" + `data: {"type":"content_block_stop","index":0}` + "\n\n")
		emit(`event: message_delta` + "\n" + `data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":` + strconv.Itoa(usage.OutputTokens) + `}}` + "\n\n")
		emit(`event: message_stop` + "\n" + `data: {"type":"message_stop"}` + "\n\n")
	}()
	return &cliproxyexecutor.StreamResult{Headers: http.Header{}, Chunks: out}, true, nil
}

func claudeCLIJSONQuote(value string) string {
	raw, errMarshal := json.Marshal(value)
	if errMarshal != nil {
		return `""`
	}
	return string(raw)
}
