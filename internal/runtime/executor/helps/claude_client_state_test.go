package helps

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientStateConfirmed(t *testing.T) {
	state := ClaudeCodeRequestDetection{Confirmed: true}.ClientState()
	if state != ClaudeClientConfirmed {
		t.Fatalf("state = %s, want confirmed", state)
	}
	if state.MayCloak() {
		t.Fatal("a confirmed client must never be cloaked")
	}
}

// A native entrypoint that fails a gate is what a header copier looks like: it
// reproduced the cheap signals but not the ones a real client emits for free.
// Upstream deliberately cloaks it and that stays unchanged.
func TestClientStateNativeEntrypointMissingGateIsRejected(t *testing.T) {
	state := ClaudeCodeRequestDetection{
		NativeClient: true,
		UserAgent:    true,
		XAppCLI:      true,
		BetasPresent: true,
		Entrypoint:   "claude-vscode",
	}.ClientState()
	if state != ClaudeClientRejected {
		t.Fatalf("state = %s, want rejected", state)
	}
}

// An Agent SDK entrypoint is cloaked on purpose: making it look like the CLI is
// the documented product behaviour, not a misjudgement.
func TestClientStateKnownNonNativeEntrypointIsRejected(t *testing.T) {
	state := ClaudeCodeRequestDetection{
		UserAgent:    true,
		XAppCLI:      true,
		BetasPresent: true,
		Entrypoint:   "sdk-ts",
	}.ClientState()
	if state != ClaudeClientRejected {
		t.Fatalf("state = %s, want rejected", state)
	}
}

// An entrypoint this build does not recognize, carrying Claude Code signals, is
// the one case that must not be rewritten: upstream ships new entrypoints
// regularly, and rewriting a genuine newer client would replace its real
// fingerprint with a synthetic one.
func TestClientStateUnknownEntrypointWithSignalsIsUnknown(t *testing.T) {
	state := ClaudeCodeRequestDetection{
		UserAgent:    true,
		XAppCLI:      true,
		BetasPresent: true,
		Entrypoint:   "remote-cowork-v2",
	}.ClientState()
	if state != ClaudeClientUnknown {
		t.Fatalf("state = %s, want unknown", state)
	}
	if state.MayCloak() {
		t.Fatal("an unknown-state client must not be cloaked")
	}
}

func TestClientStateNoSignalsIsRejected(t *testing.T) {
	state := ClaudeCodeRequestDetection{}.ClientState()
	if state != ClaudeClientRejected {
		t.Fatalf("state = %s, want rejected", state)
	}
}

// Headers that never reached the executor must not silently disable cloaking:
// the previous behaviour is preserved when there is nothing to classify.
func TestDetectClientStateWithoutHeadersPreservesBehaviour(t *testing.T) {
	state := DetectClientState(nil, []byte(`{}`), false, false, nil)
	if state != ClaudeClientRejected {
		t.Fatalf("state = %s, want rejected", state)
	}
}

func TestDetectClientStateTrustsCallerConfirmation(t *testing.T) {
	headers := http.Header{"User-Agent": {"claude-cli/2.1.280 (external, cli)"}}
	state := DetectClientState(headers, []byte(`{}`), false, true, nil)
	if state != ClaudeClientConfirmed {
		t.Fatalf("state = %s, want confirmed", state)
	}
}

// The caller's User-Agent is the cheapest header to fabricate, so the passthrough
// path must only keep a plausible one.
func TestApplyClaudeCallerDeviceHeadersValidatesUserAgent(t *testing.T) {
	cases := map[string]string{
		"claude-cli/2.1.280 (external, future-surface)": "claude-cli/2.1.280 (external, future-surface)",
		"curl/8.0":                             "claude-cli/2.1.280 (external, cli)",
		"":                                     "claude-cli/2.1.280 (external, cli)",
		"claude-cli/0.0.1 (external, ancient)": "claude-cli/2.1.280 (external, cli)",
	}
	for input, want := range cases {
		request := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		incoming := http.Header{}
		if input != "" {
			request.Header.Set("User-Agent", input)
			incoming.Set("User-Agent", input)
		}
		ApplyClaudeCallerDeviceHeaders(request, incoming, nil)
		if got := request.Header.Get("User-Agent"); got != want {
			t.Fatalf("User-Agent(%q) = %q, want %q", input, got, want)
		}
	}
}
