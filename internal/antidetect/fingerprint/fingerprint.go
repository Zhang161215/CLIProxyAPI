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
	"crypto/sha256"
	"encoding/hex"
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
func hash(kind, value string) string {
	sum := sha256.Sum256([]byte("cpa-fingerprint|" + kind + "|" + strings.ToLower(strings.TrimSpace(value))))
	return kind + ":" + hex.EncodeToString(sum[:6])
}

// Extract returns hashed fingerprints found in a request payload.
//
// It reads the raw body rather than a decoded structure: the fragments of
// interest live inside free-form prompt text, where a JSON walk would not reach
// them.
func Extract(payload []byte) []string {
	body := string(payload)
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
