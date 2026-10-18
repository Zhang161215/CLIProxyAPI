package main

import (
	"regexp"
	"strings"
	"testing"
)

const sampleBody = `{"model":"claude-opus-5","max_tokens":64000,"stream":true,` +
	`"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.177.dd9; cc_entrypoint=sdk-cli; cch=a82da;"}],` +
	`"messages":[{"role":"user","content":"hi"}],` +
	`"fallbacks":[{"model":"claude-haiku-4-5","credit":true}],"fallback_credit_token":"abc.def","temperature":1}`

func TestDigestProjectionIsFiveHexAndStable(t *testing.T) {
	first := digestProjection(sampleBody, 0x4d659218e32a3268)
	second := digestProjection(sampleBody, 0x4d659218e32a3268)
	if first != second {
		t.Fatalf("projection is not deterministic: %s vs %s", first, second)
	}
	if !regexp.MustCompile(`^[0-9a-f]{5}$`).MatchString(first) {
		t.Fatalf("digest %q is not five lowercase hex characters", first)
	}
}

func TestDigestProjectionResetsCCH(t *testing.T) {
	withOne := digestProjection(strings.Replace(sampleBody, "cch=a82da", "cch=11111", 1), 0x4d659218e32a3268)
	withAnother := digestProjection(strings.Replace(sampleBody, "cch=a82da", "cch=22222", 1), 0x4d659218e32a3268)
	if withOne != withAnother {
		t.Fatal("the cch placeholder must be reset before hashing, so its prior value cannot matter")
	}
}

func TestDigestProjectionBlanksModelValues(t *testing.T) {
	withOne := digestProjection(strings.Replace(sampleBody, "claude-opus-5", "claude-sonnet-5", 1), 0x4d659218e32a3268)
	baseline := digestProjection(sampleBody, 0x4d659218e32a3268)
	if withOne != baseline {
		t.Fatal("model values must be blanked before hashing: a router rewrites them after signing")
	}
}

func TestStripMemberRemovesValueAndComma(t *testing.T) {
	stripped := stripMember(sampleBody, "max_tokens")
	if strings.Contains(stripped, "max_tokens") {
		t.Fatalf("max_tokens survived: %s", stripped)
	}
	if strings.Contains(stripped, ",,") || strings.Contains(stripped, `,}`) {
		t.Fatalf("strip left a dangling comma: %s", stripped)
	}
}

func TestStripMemberHandlesNestedArray(t *testing.T) {
	stripped := stripMember(sampleBody, "fallbacks")
	if strings.Contains(stripped, "fallbacks") {
		t.Fatalf("fallbacks survived: %s", stripped)
	}
	if strings.Contains(stripped, "claude-haiku-4-5") {
		t.Fatalf("nested array contents survived: %s", stripped)
	}
}

func TestStripMemberHandlesStringValue(t *testing.T) {
	stripped := stripMember(sampleBody, "fallback_credit_token")
	if strings.Contains(stripped, "fallback_credit_token") {
		t.Fatalf("fallback_credit_token survived: %s", stripped)
	}
}

func TestStripMemberIgnoresAbsentKey(t *testing.T) {
	if got := stripMember(sampleBody, "not_a_key"); got != sampleBody {
		t.Fatal("an absent key must leave the body untouched")
	}
}

func TestAnalyzeExtractsIdentity(t *testing.T) {
	rep := analyze("fixture.json", capture{
		HeadersOrdered: [][]string{
			{"User-Agent", "claude-cli/2.1.177 (external, sdk-cli)"},
			{"X-Stainless-Package-Version", "0.75.0"},
			{"X-Stainless-Runtime-Version", "v22.11.0"},
			{"X-Stainless-Os", "MacOS"},
			{"X-Stainless-Arch", "arm64"},
			{"X-Claude-Code-Session-Id", "11111111-2222-4333-8444-555555555555"},
		},
		Body: sampleBody,
	})
	if rep.Identity.UserAgentVersion != "2.1.177" {
		t.Fatalf("user agent version = %q", rep.Identity.UserAgentVersion)
	}
	if rep.Identity.CCVersion != "2.1.177" {
		t.Fatalf("cc_version = %q", rep.Identity.CCVersion)
	}
	if rep.Identity.Entrypoint != "sdk-cli" {
		t.Fatalf("entrypoint = %q", rep.Identity.Entrypoint)
	}
	if rep.Identity.CCH != "a82da" {
		t.Fatalf("cch = %q", rep.Identity.CCH)
	}
	if rep.Identity.StainlessHeaders["x-stainless-package-version"] != "0.75.0" {
		t.Fatalf("stainless package version = %q", rep.Identity.StainlessHeaders["x-stainless-package-version"])
	}
	if rep.AdoptVersion != "2.1.177" {
		t.Fatalf("adopt version = %q", rep.AdoptVersion)
	}
}

func TestAnalyzeReportsMismatchForWrongSeed(t *testing.T) {
	rep := analyze("fixture.json", capture{Body: sampleBody})
	if rep.SeedTried == 0 {
		t.Fatal("a seed must be tried when one is recorded")
	}
	// The captured digest belongs to a real client build; a synthetic body will
	// not reproduce it, and the tool must say so instead of defaulting to ok.
	if rep.Matches {
		t.Fatal("a synthetic body must not verify against a captured digest")
	}
	if rep.NextStep == "" {
		t.Fatal("a mismatch must produce a next step")
	}
}
