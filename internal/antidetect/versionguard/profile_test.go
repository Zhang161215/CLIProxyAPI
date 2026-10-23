package versionguard

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

const (
	deviceProfilePath = "../../runtime/executor/helps/claude_device_profile.go"
	utlsClientPath    = "../../runtime/executor/helps/utls_client.go"
)

var (
	claudeUserAgentPattern = regexp.MustCompile(`defaultClaudeFingerprintUserAgent\s*=\s*"claude-cli/([0-9]+\.[0-9]+\.[0-9]+)`)
	packageVersionPattern  = regexp.MustCompile(`defaultClaudeFingerprintPackageVersion\s*=\s*"([0-9]+\.[0-9]+\.[0-9]+)"`)
	runtimeVersionPattern  = regexp.MustCompile(`defaultClaudeFingerprintRuntimeVersion\s*=\s*"([^"]+)"`)
	helloProvenancePattern = regexp.MustCompile(`Last verified against a\s+live\s+([0-9]+\.[0-9]+\.[0-9]+)\s+client`)
)

func readWorkspaceFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func mustMatch(t *testing.T, path string, pattern *regexp.Regexp, source string) string {
	t.Helper()
	match := pattern.FindStringSubmatch(source)
	if len(match) < 2 {
		t.Fatalf("%s: pattern %s did not match; update the ledger patterns before trusting this test", path, pattern)
	}
	return match[1]
}

// TestLedgerMatchesUpstreamSource keeps Canonical() honest: if upstream bumps a
// literal, this fails and forces a deliberate decision instead of letting the
// ledger drift away from the code it describes.
func TestLedgerMatchesUpstreamSource(t *testing.T) {
	profile := Canonical()

	deviceSource := readWorkspaceFile(t, deviceProfilePath)
	if got := mustMatch(t, deviceProfilePath, claudeUserAgentPattern, deviceSource); got != profile.Version {
		t.Errorf("advertised version drift: source User-Agent is %s, ledger says %s", got, profile.Version)
	}
	if got := mustMatch(t, deviceProfilePath, packageVersionPattern, deviceSource); got != profile.PackageVersion {
		t.Errorf("stainless package version drift: source %s, ledger %s", got, profile.PackageVersion)
	}
	if got := mustMatch(t, deviceProfilePath, runtimeVersionPattern, deviceSource); got != profile.RuntimeVersion {
		t.Errorf("stainless runtime version drift: source %s, ledger %s", got, profile.RuntimeVersion)
	}

	helloSource := readWorkspaceFile(t, utlsClientPath)
	if got := mustMatch(t, utlsClientPath, helloProvenancePattern, helloSource); got != profile.TLSHelloFrom {
		t.Errorf("TLS ClientHello provenance drift: spec captured from %s, ledger says %s", got, profile.TLSHelloFrom)
	}
}

// TestAdvertisedIdentityIsInternallyConsistent is the acceptance criterion for
// P0-1. It is expected to FAIL on the upstream baseline: the User-Agent and
// cc_version advertise one version while the handshake and the CCH projection
// come from an older one. A client version that did not ship with that
// handshake cannot exist on the wire, which makes the mismatch both observable
// per request and stable across a session.
func TestAdvertisedIdentityIsInternallyConsistent(t *testing.T) {
	profile := Canonical()
	if err := Assert(profile); err != nil {
		t.Fatalf("%v\n\nFix by aligning all of them: %s", err, strings.Join(profile.Drift(), "\n  - "))
	}
}

// TestSummaryHasNoSecrets guards against leaking credential material into the
// startup log line that operators paste into bug reports.
func TestSummaryHasNoSecrets(t *testing.T) {
	summary := Canonical().Summary()
	for _, banned := range []string{"sk-ant", "token", "device_id", "account_uuid"} {
		if strings.Contains(strings.ToLower(summary), banned) {
			t.Fatalf("summary leaks %q: %s", banned, summary)
		}
	}
}
