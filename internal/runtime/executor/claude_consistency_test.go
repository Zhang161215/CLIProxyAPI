package executor

import "testing"

func TestFindInconsistenciesCleanRequest(t *testing.T) {
	payload := []byte(`{"system":"You are Claude Code, Anthropic's official CLI for Claude.","tools":[{"type":"web_search_20250305","name":"web_search","user_location":{"type":"approximate","timezone":"Asia/Singapore"}}]}`)
	if findings := FindInconsistencies(payload, "Asia/Singapore"); len(findings) != 0 {
		t.Fatalf("clean request produced findings: %+v", findings)
	}
}

// A client in one place while the credential claims another is describing two
// different machines in the same request.
func TestFindInconsistenciesTimezoneConflict(t *testing.T) {
	payload := []byte(`{"system":"You are Claude Code.","tools":[{"type":"web_search_20250305","user_location":{"timezone":"America/New_York"}}]}`)
	findings := FindInconsistencies(payload, "Asia/Singapore")
	if len(findings) != 1 || findings[0].Kind != "timezone_conflict" {
		t.Fatalf("findings = %+v", findings)
	}
}

func TestFindInconsistenciesSkipsTimezoneWhenCredentialHasNone(t *testing.T) {
	payload := []byte(`{"tools":[{"type":"web_search_20250305","user_location":{"timezone":"America/New_York"}}]}`)
	if findings := FindInconsistencies(payload, ""); len(findings) != 0 {
		t.Fatalf("no credential timezone means nothing to compare: %+v", findings)
	}
}

func TestFindInconsistenciesForeignIdentity(t *testing.T) {
	payload := []byte(`{"system":[{"type":"text","text":"You are OpenCode, the best coding agent on the planet."}]}`)
	findings := FindInconsistencies(payload, "")
	if len(findings) != 1 || findings[0].Kind != "foreign_identity" {
		t.Fatalf("findings = %+v", findings)
	}
}

func TestFindInconsistenciesIgnoresForeignWordsInUserText(t *testing.T) {
	payload := []byte(`{"system":"You are Claude Code.","messages":[{"role":"user","content":"the docs say You are OpenCode somewhere"}]}`)
	if findings := FindInconsistencies(payload, ""); len(findings) != 0 {
		t.Fatalf("user text must not be treated as identity: %+v", findings)
	}
}

func TestFindInconsistenciesReportsBoth(t *testing.T) {
	payload := []byte(`{"system":"You are Cursor.","tools":[{"type":"web_search_20250305","user_location":{"timezone":"Europe/Paris"}}]}`)
	findings := FindInconsistencies(payload, "Asia/Singapore")
	if len(findings) != 2 {
		t.Fatalf("findings = %+v, want two", findings)
	}
}

func TestFindInconsistenciesEmptyPayload(t *testing.T) {
	if findings := FindInconsistencies(nil, "Asia/Singapore"); len(findings) != 0 {
		t.Fatalf("findings = %+v", findings)
	}
}
