// Command verify-cch turns a captured Claude Code request into the two facts
// this fork is missing: the identity values to adopt, and whether the CCH seed
// still produces the digest the client sent.
//
// It reads one file written by tools/capture/dump_claude_request.py, so the
// capture work ends here rather than in a follow-up conversation.
//
// Usage:
//
//	go run ./tools/capture/verify-cch -file capture-out/<name>.json
//	go run ./tools/capture/verify-cch -file capture-out/<name>.json -json
//
// Exit code 1 means the digest could not be reproduced with any known seed,
// which means the CCH projection changed and signing must be treated as
// unverified for this version.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/pierrec/xxHash/xxHash64"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/antidetect/versionguard"
)

const cchMask = 0xFFFFF

var (
	uaVersionPattern    = regexp.MustCompile(`claude-cli/([0-9]+\.[0-9]+\.[0-9]+)`)
	ccEntrypointPattern = regexp.MustCompile(`cc_entrypoint=([^;"]+)`)
	cchValuePattern     = regexp.MustCompile(`cch=([0-9a-fA-F]{5})`)
	ccVersionPattern    = regexp.MustCompile(`cc_version=([0-9]+\.[0-9]+\.[0-9]+)(?:\.[0-9a-zA-Z]+)?`)
	modelValuePattern   = regexp.MustCompile(`"model"\s*:\s*"[^"]*"`)
	memberKeysToStrip   = []string{"fallbacks", "fallback_credit_token", "max_tokens"}
	ccStainlessHeaders  = []string{"x-stainless-package-version", "x-stainless-runtime-version", "x-stainless-os", "x-stainless-arch"}
)

type capture struct {
	Host           string     `json:"host"`
	Path           string     `json:"path"`
	HeadersOrdered [][]string `json:"headers_ordered"`
	Body           string     `json:"body"`
}

type identity struct {
	UserAgentVersion  string            `json:"user_agent_version"`
	CCVersion         string            `json:"cc_version"`
	Entrypoint        string            `json:"cc_entrypoint"`
	CCH               string            `json:"cch"`
	StainlessHeaders  map[string]string `json:"stainless_headers"`
	SessionID         string            `json:"session_id_present"`
	MetadataUserIDSet bool              `json:"metadata_user_id_present"`
}

type result struct {
	Capture      string   `json:"capture"`
	Identity     identity `json:"identity"`
	SeedTried    uint64   `json:"seed_tried"`
	ComputedCCH  string   `json:"computed_cch"`
	CapturedCCH  string   `json:"captured_cch"`
	Matches      bool     `json:"matches"`
	KnownSeedFor string   `json:"known_seed_for_version"`
	AdoptVersion string   `json:"adopt_version"`
	NextStep     string   `json:"next_step"`
	Warnings     []string `json:"warnings,omitempty"`
}

func main() {
	path := flag.String("file", "", "capture JSON written by dump_claude_request.py")
	asJSON := flag.Bool("json", false, "emit the result as JSON")
	flag.Parse()

	if strings.TrimSpace(*path) == "" {
		fmt.Fprintln(os.Stderr, "verify-cch: -file is required")
		os.Exit(2)
	}

	raw, errRead := os.ReadFile(*path)
	if errRead != nil {
		fmt.Fprintf(os.Stderr, "verify-cch: read: %v\n", errRead)
		os.Exit(2)
	}

	var doc capture
	if errUnmarshal := json.Unmarshal(raw, &doc); errUnmarshal != nil {
		fmt.Fprintf(os.Stderr, "verify-cch: parse: %v\n", errUnmarshal)
		os.Exit(2)
	}

	rep := analyze(*path, doc)
	if *asJSON {
		encoded, _ := json.MarshalIndent(rep, "", "  ")
		fmt.Println(string(encoded))
	} else {
		printResult(rep)
	}

	if !rep.Matches && rep.CapturedCCH != "" {
		os.Exit(1)
	}
}

func analyze(path string, doc capture) result {
	rep := result{Capture: path, Identity: identity{StainlessHeaders: map[string]string{}}}

	ua := headerValue(doc.HeadersOrdered, "user-agent")
	if match := uaVersionPattern.FindStringSubmatch(ua); len(match) > 1 {
		rep.Identity.UserAgentVersion = match[1]
	}
	for _, name := range ccStainlessHeaders {
		if value := headerValue(doc.HeadersOrdered, name); value != "" {
			rep.Identity.StainlessHeaders[name] = value
		}
	}
	rep.Identity.SessionID = boolWord(headerValue(doc.HeadersOrdered, "x-claude-code-session-id") != "")

	if match := ccVersionPattern.FindStringSubmatch(doc.Body); len(match) > 1 {
		rep.Identity.CCVersion = match[1]
	}
	if match := ccEntrypointPattern.FindStringSubmatch(doc.Body); len(match) > 1 {
		rep.Identity.Entrypoint = strings.TrimSpace(match[1])
	}
	if match := cchValuePattern.FindStringSubmatch(doc.Body); len(match) > 1 {
		rep.Identity.CCH = strings.ToLower(match[1])
		rep.CapturedCCH = rep.Identity.CCH
	}
	rep.Identity.MetadataUserIDSet = strings.Contains(doc.Body, `"metadata"`) && strings.Contains(doc.Body, "user_id")

	// Prefer the version the wire itself claims: the billing header is what the
	// server reads, so it is the version any seed lookup must match.
	adopt := rep.Identity.CCVersion
	if adopt == "" {
		adopt = rep.Identity.UserAgentVersion
	}
	rep.AdoptVersion = adopt

	seed, known := versionguard.SeedFor(adopt)
	if known {
		rep.KnownSeedFor = adopt
	} else if adopt != "" {
		// Fall back to the newest verified seed so a version bump that did not
		// change the algorithm still verifies.
		versions := versionguard.KnownVersions()
		if len(versions) > 0 {
			newest := versions[len(versions)-1]
			seed, _ = versionguard.SeedFor(newest)
			rep.KnownSeedFor = newest
			rep.Warnings = append(rep.Warnings, fmt.Sprintf(
				"no seed recorded for %s; tried the newest verified seed (%s)", adopt, newest))
		}
	}

	if rep.CapturedCCH != "" && seed != 0 {
		rep.SeedTried = seed
		computed := digestProjection(doc.Body, seed)
		rep.ComputedCCH = computed
		rep.Matches = computed == rep.CapturedCCH
	}

	switch {
	case rep.CapturedCCH == "":
		rep.NextStep = "the capture has no cch= in its body; re-capture an interactive (not helper) request"
	case rep.Matches:
		rep.NextStep = "seed verified: record it for " + adopt + " in CCHSeeds, then align TLSHelloFrom/CCHFrom to " + adopt
	default:
		rep.NextStep = "digest mismatch: treat signing for " + adopt + " as unverified and re-derive the projection before shipping"
	}
	return rep
}

// digestProjection reproduces the client's CCH digest.
//
// The projection is surgical: the client hashes the body it is about to send,
// with the digest field reset, model values blanked (keys kept), and the fields
// a router may rewrite removed. Hashing a re-serialized body would change key
// order and never match.
func digestProjection(body string, seed uint64) string {
	projected := cchValuePattern.ReplaceAllString(body, "cch=00000")
	projected = modelValuePattern.ReplaceAllString(projected, `"model":""`)
	for _, key := range memberKeysToStrip {
		projected = stripMember(projected, key)
	}
	sum := xxHash64.Checksum([]byte(projected), seed)
	return fmt.Sprintf("%05x", sum&cchMask)
}

// stripMember removes `"key":value` and the comma that joined it to its
// neighbour, using a JSON decoder so nested values are bounded correctly.
func stripMember(body, key string) string {
	needle := []byte(`"` + key + `"`)
	index := bytes.Index([]byte(body), needle)
	if index < 0 {
		return body
	}
	colonOffset := bytes.IndexByte([]byte(body[index:]), ':')
	if colonOffset < 0 {
		return body
	}
	valueStart := index + colonOffset + 1
	decoder := json.NewDecoder(strings.NewReader(body[valueStart:]))
	var raw json.RawMessage
	if errDecode := decoder.Decode(&raw); errDecode != nil {
		return body
	}
	valueEnd := valueStart + int(decoder.InputOffset())

	start, end := index, valueEnd
	if end < len(body) && body[end] == ',' {
		end++
	} else {
		for start > 0 && (body[start-1] == ' ' || body[start-1] == '\n' || body[start-1] == '\t') {
			start--
		}
		if start > 0 && body[start-1] == ',' {
			start--
		}
	}
	return body[:start] + body[end:]
}

func headerValue(headers [][]string, name string) string {
	lower := strings.ToLower(name)
	for _, pair := range headers {
		if len(pair) != 2 {
			continue
		}
		if strings.ToLower(pair[0]) == lower {
			return pair[1]
		}
	}
	return ""
}

func boolWord(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}

func printResult(rep result) {
	fmt.Printf("capture: %s\n", rep.Capture)
	fmt.Println()
	fmt.Println("identity on the wire")
	fmt.Printf("  user-agent version : %s\n", orUnknown(rep.Identity.UserAgentVersion))
	fmt.Printf("  cc_version         : %s\n", orUnknown(rep.Identity.CCVersion))
	fmt.Printf("  cc_entrypoint      : %s\n", orUnknown(rep.Identity.Entrypoint))
	fmt.Printf("  cch                : %s\n", orUnknown(rep.Identity.CCH))
	fmt.Printf("  metadata.user_id   : %t\n", rep.Identity.MetadataUserIDSet)
	for _, name := range ccStainlessHeaders {
		fmt.Printf("  %-19s: %s\n", name, orUnknown(rep.Identity.StainlessHeaders[name]))
	}
	fmt.Println()
	fmt.Println("cch verification")
	fmt.Printf("  seed tried         : 0x%016X (recorded for %s)\n", rep.SeedTried, orUnknown(rep.KnownSeedFor))
	fmt.Printf("  computed           : %s\n", orUnknown(rep.ComputedCCH))
	fmt.Printf("  captured           : %s\n", orUnknown(rep.CapturedCCH))
	if rep.Matches {
		fmt.Println("  verdict            : MATCH")
	} else {
		fmt.Println("  verdict            : MISMATCH")
	}
	for _, warning := range rep.Warnings {
		fmt.Printf("  warning            : %s\n", warning)
	}
	fmt.Println()
	fmt.Printf("adopt version: %s\n", orUnknown(rep.AdoptVersion))
	fmt.Printf("next step    : %s\n", rep.NextStep)
}

func orUnknown(value string) string {
	if strings.TrimSpace(value) == "" {
		return "(absent)"
	}
	return value
}
