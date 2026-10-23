// Package fingerprint detects when two credentials look like the same machine.
//
// A proxy can hide its address, its TLS handshake and its header set. It cannot
// hide what the client writes into the prompt: the working directory, the git
// remote, and the committer address all travel inside the request body, and the
// upstream reads them.
//
// This package hashes those fragments and looks for the same fragment under more
// than one credential. A collision is not misconduct — two accounts can share a
// laptop — but a fleet where every credential serves the same repository is a
// fleet that looks like one person running many accounts.
//
// Only hashes leave this package. Raw paths and addresses never leave it at all.
package fingerprint

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// maxFingerprintsPerRequest bounds extraction so a large body cannot produce an
// unbounded set of hashes.
const maxFingerprintsPerRequest = 16

var (
	// POSIX home paths: /Users/<name>/..., /home/<name>/...
	posixPathPattern = regexp.MustCompile(`/(?:Users|home|root)/[A-Za-z0-9._-]+(?:/[A-Za-z0-9._-]+){0,2}`)
	// Windows home paths, as they appear inside JSON strings.
	windowsPathPattern = regexp.MustCompile(`[A-Za-z]:\\\\Users\\\\[A-Za-z0-9._ -]+(?:\\\\[A-Za-z0-9._ -]+){0,2}`)
	// Repository references: an https or ssh remote, or the shorthand form.
	repoPattern = regexp.MustCompile(`(?:github\.com|gitlab\.com|bitbucket\.org)[:/][A-Za-z0-9._-]+/[A-Za-z0-9._-]+`)
	// Committer addresses.
	emailPattern = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`)
)

// normalizeRepo collapses the two ways a repository is written — ssh shorthand
// and an https remote — into one string, so the same repository produces one
// fingerprint whichever transport the client used.
func normalizeRepo(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	normalized = strings.TrimSuffix(normalized, ".git")
	normalized = strings.TrimSuffix(normalized, "/")
	// ssh shorthand uses a colon between host and owner; https uses a slash.
	if hostEnd := strings.Index(normalized, ".com"); hostEnd > 0 {
		head := normalized[:hostEnd+len(".com")]
		tail := strings.TrimPrefix(normalized[hostEnd+len(".com"):], ":")
		tail = strings.TrimPrefix(tail, "/")
		normalized = head + "/" + tail
	}
	return normalized
}

// isCommitterAddress rejects the ssh "git@host" prefix, which matches the email
// shape but names a transport, not a person.
func isCommitterAddress(value string) bool {
	local := strings.ToLower(value)
	if at := strings.Index(local, "@"); at > 0 {
		return local[:at] != "git"
	}
	return false
}

// hash normalizes a fragment and returns a stable, opaque identifier.
//
// Normalization is lowercase-only: two paths that differ in case are treated as
// the same machine, which is the direction that produces a warning rather than
// silently missing one.
// markers are the cheap prefilter: a body without any of these cannot produce a
// filesystem, repository or committer fingerprint, so the structured scan is
// skipped entirely. Most chat traffic hits this path.
var markers = [][]byte{
	[]byte("/Users/"), []byte("/home/"), []byte("/root/"), []byte("Users\\"),
	[]byte("@"), []byte("github.com"), []byte("gitlab.com"), []byte("bitbucket.org"),
}

func mayContainFingerprint(payload []byte) bool {
	for _, marker := range markers {
		if bytes.Contains(payload, marker) {
			return true
		}
	}
	return false
}

// scanRegion returns the part of the payload worth searching.
func scanRegion(payload []byte) string {
	var doc struct {
		System   json.RawMessage `json:"system"`
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if errUnmarshal := json.Unmarshal(payload, &doc); errUnmarshal == nil {
		var builder strings.Builder
		builder.Write(doc.System)
		for index := len(doc.Messages) - 1; index >= 0; index-- {
			if doc.Messages[index].Role != "user" {
				continue
			}
			builder.Write(doc.Messages[index].Content)
			break
		}
		if builder.Len() > 0 {
			return truncate(builder.String(), maxFieldBytes)
		}
	}
	return truncate(string(payload), maxScannedBytes)
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}

func hash(kind, value string) string {
	sum := sha256.Sum256([]byte("cpa-fingerprint|" + kind + "|" + strings.ToLower(strings.TrimSpace(value))))
	return kind + ":" + hex.EncodeToString(sum[:6])
}

// maxScannedBytes bounds how much of a payload is searched when the structure
// cannot be located. A Claude body can exceed a megabyte, and running four
// regular expressions over all of it measured at ~53 ms per request — enough to
// show up in latency.
const maxScannedBytes = 32 * 1024

// maxFieldBytes bounds one extracted text field.
const maxFieldBytes = 16 * 1024

// Extract returns hashed fingerprints found in a request payload.
//
// It searches the fields that actually carry environment text (the system block
// and the newest user turn) rather than the whole body, then falls back to a
// bounded prefix. Same findings, without paying for a megabyte of scanning on
// every request.
func Extract(payload []byte) []string {
	if !mayContainFingerprint(payload) {
		return nil
	}
	body := scanRegion(payload)
	if body == "" {
		return nil
	}
	seen := make(map[string]struct{})
	out := make([]string, 0, 8)

	add := func(kind string, match string) {
		if len(out) >= maxFingerprintsPerRequest {
			return
		}
		hashed := hash(kind, match)
		if _, exists := seen[hashed]; exists {
			return
		}
		seen[hashed] = struct{}{}
		out = append(out, hashed)
	}

	for _, match := range posixPathPattern.FindAllString(body, -1) {
		add("path", match)
	}
	for _, match := range windowsPathPattern.FindAllString(body, -1) {
		add("path", match)
	}
	for _, match := range repoPattern.FindAllString(body, -1) {
		add("repo", normalizeRepo(match))
	}
	for _, match := range emailPattern.FindAllString(body, -1) {
		if !isCommitterAddress(match) {
			continue
		}
		add("email", match)
	}
	return out
}

// Collision describes a fingerprint that appears under another credential too.
type Collision struct {
	Fingerprint string
	// Others lists the other credentials that produced the same fingerprint.
	Others []string
}

// Registry tracks which credential produced which fingerprint.
type Registry struct {
	mu     sync.Mutex
	byCred map[string]map[string]struct{}
	byFP   map[string]map[string]struct{}
	// reported remembers credential+fingerprint pairs already warned about, so
	// one shared repository does not warn on every request.
	reported map[string]struct{}
}

// NewRegistry builds an empty registry.
func NewRegistry() *Registry {
	return &Registry{
		byCred:   make(map[string]map[string]struct{}),
		byFP:     make(map[string]map[string]struct{}),
		reported: make(map[string]struct{}),
	}
}

// Observe records a credential's fingerprints and returns the collisions that
// have not been reported yet.
func (r *Registry) Observe(credentialID string, payload []byte) []Collision {
	if r == nil || strings.TrimSpace(credentialID) == "" {
		return nil
	}
	fingerprints := Extract(payload)
	if len(fingerprints) == 0 {
		return nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.byCred[credentialID] == nil {
		r.byCred[credentialID] = make(map[string]struct{})
	}

	var collisions []Collision
	for _, item := range fingerprints {
		if _, known := r.byCred[credentialID][item]; known {
			continue
		}
		r.byCred[credentialID][item] = struct{}{}
		if r.byFP[item] == nil {
			r.byFP[item] = make(map[string]struct{})
		}
		r.byFP[item][credentialID] = struct{}{}

		owners := r.byFP[item]
		if len(owners) < 2 {
			continue
		}
		key := credentialID + "|" + item
		if _, warned := r.reported[key]; warned {
			continue
		}
		r.reported[key] = struct{}{}

		others := make([]string, 0, len(owners)-1)
		for owner := range owners {
			if owner != credentialID {
				others = append(others, owner)
			}
		}
		sort.Strings(others)
		collisions = append(collisions, Collision{Fingerprint: item, Others: others})
	}
	return collisions
}

// ForCredential returns the fingerprints recorded for one credential.
func (r *Registry) ForCredential(credentialID string) []string {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	set := r.byCred[credentialID]
	out := make([]string, 0, len(set))
	for item := range set {
		out = append(out, item)
	}
	sort.Strings(out)
	return out
}
