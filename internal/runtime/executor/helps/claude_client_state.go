package helps

import (
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

// ClaudeClientState is a three-valued view of an inbound request's client
// identity.
//
// The detector used to expose a single bool, and every consumer treated
// "not confirmed" as "not Claude Code". That collapses two very different
// situations into one: a caller that is plainly some other client, and a
// caller that carries several Claude Code signals but not all of them — a new
// entrypoint, a beta string this build has not measured, a header the caller
// legitimately dropped.
//
// The difference matters because "not confirmed" is what enables CLI cloaking,
// and cloaking rewrites the request. Rewriting a genuine client is strictly
// worse than leaving it alone: the rewrite replaces the true fingerprint with
// a synthetic one that no real client emits. Unknown therefore means
// "passthrough", not "rewrite".
type ClaudeClientState uint8

const (
	// ClaudeClientRejected means the request shows no Claude Code signals at
	// all. Cloaking may apply.
	ClaudeClientRejected ClaudeClientState = iota
	// ClaudeClientUnknown means the request carries some Claude Code signals
	// but not enough to confirm it. Callers must not rewrite it.
	ClaudeClientUnknown
	// ClaudeClientConfirmed means the request is a native Claude Code client.
	ClaudeClientConfirmed
)

// String renders the state for logs and metrics.
func (s ClaudeClientState) String() string {
	switch s {
	case ClaudeClientConfirmed:
		return "confirmed"
	case ClaudeClientUnknown:
		return "unknown"
	default:
		return "rejected"
	}
}

// MayCloak reports whether the state permits rewriting the request.
func (s ClaudeClientState) MayCloak() bool {
	return s == ClaudeClientRejected
}

// ClientState classifies a detection result.
//
// The three cases exist because "not confirmed" currently means three different
// things, and only one of them is safe to rewrite:
//
//   - A known non-native entrypoint (sdk-ts, sdk-py, ...) is an Agent SDK
//     caller. Cloaking it to CLI shape is the documented product behaviour, so
//     it stays rejected.
//   - A native entrypoint that fails a gate (missing metadata.user_id, an
//     implausible version) is what a header copier looks like: it reproduced
//     the cheap signals but not the ones a real client emits for free. It stays
//     rejected too.
//   - An entrypoint this build does not recognize, carrying Claude Code
//     signals, is the one case that must not be rewritten. Upstream ships new
//     entrypoints regularly; an unrecognized one is far more likely a genuine
//     newer client than a copier, and rewriting a genuine client replaces a
//     real fingerprint with a synthetic one no client emits.
func (d ClaudeCodeRequestDetection) ClientState() ClaudeClientState {
	if d.Confirmed {
		return ClaudeClientConfirmed
	}
	if d.NativeClient {
		return ClaudeClientRejected
	}
	if d.Entrypoint != "" && claudeCodeSubclientByEntrypoint[d.Entrypoint] != "" {
		return ClaudeClientRejected
	}
	if d.UserAgent || d.BetasPresent || d.XAppCLI || d.HelperProfile {
		return ClaudeClientUnknown
	}
	return ClaudeClientRejected
}

// DetectClientState reports the client state for a request that has already
// been decoded.
//
// confirmed is the caller's existing verdict; it is trusted as-is so this never
// contradicts the request path that already ran. Only the negative case is
// refined, which keeps the change additive.
func DetectClientState(headers http.Header, payload []byte, countTokens bool, confirmed bool, cfg *config.Config) ClaudeClientState {
	if confirmed {
		return ClaudeClientConfirmed
	}
	if len(headers) == 0 {
		// Nothing to classify. Preserve the previous behaviour rather than
		// silently disabling cloaking for callers that never forwarded headers.
		return ClaudeClientRejected
	}
	detection := DetectClaudeCodeRequest(headers, payload, countTokens, cfgNonNil(cfg))
	return detection.ClientState()
}

// cfgNonNil keeps the variadic detector call from receiving an untyped nil,
// which would otherwise reach config lookups on the detection path.
func cfgNonNil(cfg *config.Config) *config.Config {
	if cfg == nil {
		return &config.Config{}
	}
	return cfg
}

// ApplyClaudeCallerDeviceHeaders keeps the caller's own software profile and
// fills only the values it did not send.
//
// The default path replaces the profile outright, which is right for a caller
// whose shape is unknown but wrong for one that is probably a genuine Claude
// Code client this build does not recognize: replacing its User-Agent and
// stainless family fabricates a fingerprint, and the caller's real one was
// already consistent with the rest of its request.
func ApplyClaudeCallerDeviceHeaders(r *http.Request, incoming http.Header, cfg *config.Config) {
	if r == nil {
		return
	}
	profile := defaultClaudeDeviceProfile(cfg)
	ensure := func(name, fallback string) {
		if current := strings.TrimSpace(r.Header.Get(name)); current != "" {
			return
		}
		if sent := strings.TrimSpace(incoming.Get(name)); sent != "" {
			r.Header.Set(name, sent)
			return
		}
		r.Header.Set(name, fallback)
	}
	ensure("X-Stainless-Runtime-Version", profile.RuntimeVersion)
	ensure("X-Stainless-Package-Version", profile.PackageVersion)
	ensure("X-Stainless-Os", mapStainlessOS())
	ensure("X-Stainless-Arch", mapStainlessArch())

	// The User-Agent is the one header a caller can easily fabricate, so it is
	// the one that must be checked before it is trusted. Preserving a plausible
	// claude-cli string keeps an unrecognized genuine client intact; anything
	// else falls back to the canonical value instead of forwarding garbage
	// upstream under this credential's name.
	current := strings.TrimSpace(r.Header.Get("User-Agent"))
	if current == "" {
		current = strings.TrimSpace(incoming.Get("User-Agent"))
	}
	if current != "" && plausibleClaudeCodeUserAgent(current, cfg) {
		r.Header.Set("User-Agent", current)
		return
	}
	r.Header.Set("User-Agent", profile.UserAgent)
}
