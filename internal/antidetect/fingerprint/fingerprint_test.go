package fingerprint

import (
	"strings"
	"testing"
)

const bodyA = `{"system":[{"type":"text","text":"Working directory: /Users/alice/code/billing\nGit remote: git@github.com:acme/billing.git\nCommitter: alice@acme.dev"}],"messages":[]}`

const bodyB = `{"system":[{"type":"text","text":"Working directory: /Users/alice/code/billing\nGit remote: https://github.com/acme/billing\nCommitter: alice@acme.dev"}],"messages":[]}`

const bodyOther = `{"system":[{"type":"text","text":"Working directory: /home/bob/src/ledger\nGit remote: git@github.com:otherco/ledger.git"}],"messages":[]}`

func TestExtractFindsKinds(t *testing.T) {
	got := Extract([]byte(bodyA))
	kinds := map[string]bool{}
	for _, item := range got {
		parts := strings.SplitN(item, ":", 2)
		kinds[parts[0]] = true
	}
	for _, want := range []string{"path", "repo", "email"} {
		if !kinds[want] {
			t.Fatalf("no %q fingerprint in %v", want, got)
		}
	}
}

// The raw path and address must never appear in the output.
func TestExtractNeverReturnsRawValues(t *testing.T) {
	for _, item := range Extract([]byte(bodyA)) {
		for _, banned := range []string{"alice", "acme", "/Users/", "@"} {
			if strings.Contains(item, banned) {
				t.Fatalf("fingerprint %q leaks %q", item, banned)
			}
		}
	}
}

// The same workspace reached over ssh and https must produce the same
// repository fingerprint; otherwise the feature misses the common case.
func TestExtractNormalizesTransportForRepos(t *testing.T) {
	a := Extract([]byte(`git@github.com:acme/billing.git`))
	b := Extract([]byte(`https://github.com/acme/billing`))
	if len(a) == 0 || len(b) == 0 {
		t.Fatalf("no repo fingerprint: %v / %v", a, b)
	}
	if a[0] != b[0] {
		t.Fatalf("ssh and https forms disagree: %q vs %q", a[0], b[0])
	}
}

func TestExtractIsBounded(t *testing.T) {
	many := strings.Repeat("/Users/u/project ", 100)
	if got := Extract([]byte(many)); len(got) > maxFingerprintsPerRequest {
		t.Fatalf("extracted %d fingerprints, want at most %d", len(got), maxFingerprintsPerRequest)
	}
}

func TestExtractIgnoresEmptyPayload(t *testing.T) {
	if got := Extract(nil); len(got) != 0 {
		t.Fatalf("nil payload produced %v", got)
	}
}

func TestObserveReportsCollisionAcrossCredentials(t *testing.T) {
	registry := NewRegistry()

	if collisions := registry.Observe("cred-a", []byte(bodyA)); len(collisions) != 0 {
		t.Fatalf("the first credential cannot collide: %v", collisions)
	}
	collisions := registry.Observe("cred-b", []byte(bodyB))
	if len(collisions) == 0 {
		t.Fatal("two credentials on the same workspace must collide")
	}
	for _, collision := range collisions {
		if len(collision.Others) != 1 || collision.Others[0] != "cred-a" {
			t.Fatalf("collision %v should name the other credential", collision)
		}
	}
}

func TestObserveDoesNotRepeatWarnings(t *testing.T) {
	registry := NewRegistry()
	registry.Observe("cred-a", []byte(bodyA))
	registry.Observe("cred-b", []byte(bodyB))

	if got := registry.Observe("cred-b", []byte(bodyB)); len(got) != 0 {
		t.Fatalf("the same collision must not warn twice: %v", got)
	}
}

func TestObserveIsolatesUnrelatedWorkspaces(t *testing.T) {
	registry := NewRegistry()
	registry.Observe("cred-a", []byte(bodyA))
	if got := registry.Observe("cred-b", []byte(bodyOther)); len(got) != 0 {
		t.Fatalf("unrelated workspaces collided: %v", got)
	}
}

func TestForCredentialReturnsItsSet(t *testing.T) {
	registry := NewRegistry()
	registry.Observe("cred-a", []byte(bodyA))
	got := registry.ForCredential("cred-a")
	if len(got) < 3 {
		t.Fatalf("expected path, repo and email fingerprints, got %v", got)
	}
}

func TestNilRegistryIsSafe(t *testing.T) {
	var registry *Registry
	if got := registry.Observe("cred-a", []byte(bodyA)); got != nil {
		t.Fatalf("nil registry returned %v", got)
	}
	if got := registry.ForCredential("cred-a"); got != nil {
		t.Fatalf("nil registry returned %v", got)
	}
}
