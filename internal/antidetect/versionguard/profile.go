// Package versionguard keeps the advertised Claude Code identity and the
// actual wire fingerprint from drifting apart.
//
// Every upstream anti-detection signal Claude Code emits is version-coupled:
// the User-Agent, the cc_version in the billing header, the build fingerprint
// salt, the CCH projection, and the TLS ClientHello captured from a specific
// binary. When one of them is bumped without the others, the resulting request
// advertises a client version that never existed on the wire. That mismatch is
// observable per request and stable across a session, so it is the highest
// value fix in this fork.
package versionguard

import (
	"fmt"
	"sort"
	"strings"
)

// Profile is the single source of truth for the advertised client identity.
type Profile struct {
	// Version is the Claude Code version advertised in the User-Agent and in
	// the cc_version field of the x-anthropic-billing-header system block.
	Version string
	// PackageVersion is the x-stainless-package-version the CLI ships.
	PackageVersion string
	// RuntimeVersion is the x-stainless-runtime-version the CLI ships.
	RuntimeVersion string
	// OS and Arch are the x-stainless-os / x-stainless-arch values.
	OS   string
	Arch string
	// TLSHelloFrom records the Claude Code version whose ClientHello the uTLS
	// spec was captured from. It must equal Version: a 2.1.280 User-Agent over
	// a 2.1.220 handshake is a combination that does not exist upstream.
	TLSHelloFrom string
	// CCHFrom records the Claude Code version the CCH projection and its
	// xxh64 seed were verified against.
	CCHFrom string
}

// CCHSeeds maps a Claude Code version to the xxh64 seed used by its
// x-anthropic-billing-header CCH projection.
//
// Seeds are versioned rather than global: the constant is read out of the
// shipped binary, and a rebuild can change it. Only versions verified against
// a real capture belong here.
var CCHSeeds = map[string]uint64{
	// Verified against a third-party capture of 2.1.177.
	"2.1.177": 0x4d659218e32a3268,
	// Verified 2026-10-18 against a live 2.1.280 request captured from this
	// host: the tool recomputed the captured digest exactly (MATCH).
	"2.1.280": 0x4d659218e32a3268,
}

// SeedFor returns the CCH seed for a Claude Code version and whether the
// version has been verified.
//
// An unverified version must not be signed with a guessed seed. A confident but
// wrong digest is a stable, clusterable signal, while a random value is not:
// the trade favors degrading, never guessing.
func SeedFor(version string) (uint64, bool) {
	seed, ok := CCHSeeds[strings.TrimSpace(version)]
	return seed, ok
}

// KnownVersions returns the verified seed versions, newest-looking last.
func KnownVersions() []string {
	versions := make([]string, 0, len(CCHSeeds))
	for version := range CCHSeeds {
		versions = append(versions, version)
	}
	sort.Strings(versions)
	return versions
}

// Canonical returns the identity this build intends to present.
//
// Update this together with the constants in
// internal/runtime/executor/helps/claude_device_profile.go and the uTLS spec
// comment in internal/runtime/executor/helps/utls_client.go. The ledger exists
// so that "forgot to re-capture the handshake" becomes a test failure instead
// of a silent fingerprint mismatch in production.
func Canonical() Profile {
	return Profile{
		Version:        "2.1.280",
		PackageVersion: "0.112.1",
		RuntimeVersion: "v26.3.0",
		OS:             "MacOS",
		Arch:           "arm64",
		TLSHelloFrom:   "2.1.280",
		CCHFrom:        "2.1.280",
	}
}

// Drift lists every field whose provenance version differs from the advertised
// version. An empty result means the identity is internally consistent.
func (p Profile) Drift() []string {
	var drift []string
	check := func(field, provenance string) {
		if provenance != p.Version {
			drift = append(drift, fmt.Sprintf(
				"%s: advertised %s, provenance %s", field, p.Version, provenance))
		}
	}
	check("tls_client_hello", p.TLSHelloFrom)
	check("cch_projection", p.CCHFrom)
	return drift
}

// Summary renders a one-line identity digest for startup logs. It must never
// include credentials, device IDs or account identifiers.
func (p Profile) Summary() string {
	return fmt.Sprintf("claude_identity version=%s pkg=%s runtime=%s os=%s arch=%s tls_hello_from=%s cch_from=%s",
		p.Version, p.PackageVersion, p.RuntimeVersion, p.OS, p.Arch, p.TLSHelloFrom, p.CCHFrom)
}

// Assert returns an error when the identity drifted. Callers decide whether
// that is fatal: cmd/server must refuse to start unless the operator opted in
// to drift with antidetect.versionguard.allow_drift.
func Assert(p Profile) error {
	drift := p.Drift()
	if len(drift) == 0 {
		return nil
	}
	return fmt.Errorf("claude identity drift: %s", strings.Join(drift, "; "))
}
