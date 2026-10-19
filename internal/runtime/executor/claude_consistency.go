package executor

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/antidetect/audit"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

// foreignIdentitySentences are opening lines that other coding agents put at the
// top of their system prompt. A request that claims to be Claude Code but opens
// with one of these is contradicting itself, and the upstream reads the system
// prompt, not just the headers.
var foreignIdentitySentences = []string{
	"you are opencode",
	"you are cursor",
	"you are windsurf",
	"you are cline",
	"you are roo",
	"you are continue",
	"you are aider",
	"you are copilot",
}

// Finding is one internal contradiction inside a single request.
type Finding struct {
	Kind   string
	Detail string
}

// FindInconsistencies reports places where one field of a request disagrees
// with another.
//
// Nothing here rewrites the request: a mismatch is evidence for the operator,
// and rewriting a field we cannot verify would trade one inconsistency for a
// worse one. credentialTimezone may be empty, in which case the timezone check
// is skipped.
func FindInconsistencies(payload []byte, credentialTimezone string) []Finding {
	if len(payload) == 0 {
		return nil
	}
	var findings []Finding

	// web_search carries a location the client believes it is in. When that
	// differs from the timezone this credential presents, the same request
	// describes two different places.
	if wanted := strings.TrimSpace(credentialTimezone); wanted != "" {
		tools := gjson.GetBytes(payload, "tools")
		if tools.IsArray() {
			for _, tool := range tools.Array() {
				kind := tool.Get("type").String()
				if kind != "web_search" && !strings.HasPrefix(kind, "web_search_") {
					continue
				}
				declared := strings.TrimSpace(tool.Get("user_location.timezone").String())
				if declared == "" || strings.EqualFold(declared, wanted) {
					continue
				}
				findings = append(findings, Finding{
					Kind:   "timezone_conflict",
					Detail: "web_search user_location declares " + declared + " but the credential presents " + wanted,
				})
			}
		}
	}

	// A system prompt that opens as another agent contradicts the identity the
	// headers claim.
	system := strings.ToLower(systemText(payload))
	for _, sentence := range foreignIdentitySentences {
		if strings.Contains(system, sentence) {
			findings = append(findings, Finding{
				Kind:   "foreign_identity",
				Detail: "system prompt contains " + strconvQuote(sentence),
			})
			break
		}
	}

	return findings
}

// systemText flattens the system field, which may be a string or text blocks.
func systemText(payload []byte) string {
	system := gjson.GetBytes(payload, "system")
	if !system.Exists() {
		return ""
	}
	if system.Type == gjson.String {
		return system.String()
	}
	var builder strings.Builder
	system.ForEach(func(_, block gjson.Result) bool {
		if block.Get("type").String() != "text" {
			return true
		}
		builder.WriteString(block.Get("text").String())
		builder.WriteByte('\n')
		return true
	})
	return builder.String()
}

// recordClaudeConsistency writes one audit line per contradiction found.
func recordClaudeConsistency(auth *cliproxyauth.Auth, cfg *config.Config, payload []byte) {
	credentialID := authCredentialID(auth)
	if credentialID == "" {
		return
	}
	timezone := ""
	if location := claudeCodeTimezone(cfg, auth); location != nil {
		timezone = location.String()
	}
	for _, finding := range FindInconsistencies(payload, timezone) {
		audit.Log(audit.Record{
			Event:        "request_consistency",
			Decision:     audit.DecisionMismatch,
			CredentialID: audit.ID(credentialID),
			Rule:         finding.Kind,
			Detail:       finding.Detail,
		})
	}
}

// strconvQuote keeps the log line readable without pulling in strconv's escape
// rules for a constant list of plain sentences.
func strconvQuote(value string) string {
	return "\"" + value + "\""
}
