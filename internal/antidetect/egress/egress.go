// Package egress enforces one outbound path per credential.
//
// Sharing an exit between credentials is the cheapest way to link them: the
// upstream sees the same address serving unrelated conversations, which is the
// same shape as one account used by several people. Binding is enforced before
// a request is scheduled, and it fails closed: an unbound credential under
// enforcement refuses to run rather than quietly going direct.
package egress

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
)

// Pool describes one configured exit.
type Pool struct {
	// ProxyURL is the address the transport uses for this exit.
	ProxyURL string `json:"proxy_url"`
	// Country and Timezone let a mismatch against the credential's account
	// region be reported instead of silently tolerated.
	Country  string `json:"country,omitempty"`
	Timezone string `json:"timezone,omitempty"`
}

// Config is the operator's egress policy.
type Config struct {
	// Enforce refuses to run a credential that has no binding.
	Enforce bool            `json:"enforce"`
	Pools   map[string]Pool `json:"pools"`
}

// ConfigEnv carries the policy as JSON, e.g.
//
//	{"enforce":true,"pools":{"hk-1":{"proxy_url":"socks5://h:p","country":"HK"}}}
const ConfigEnv = "CPA_ANTIDETECT_EGRESS"

// BindingEnv is the credential attribute naming its exit. It lives on the auth
// record so a credential travels with its identity across restarts.
const BindingEnv = "egress_id"

// ConfigFromEnv parses the policy, treating malformed JSON as "disabled" rather
// than as an outage: a typo must not stop every request.
func ConfigFromEnv() (Config, error) {
	raw := strings.TrimSpace(os.Getenv(ConfigEnv))
	if raw == "" {
		return Config{}, nil
	}
	var cfg Config
	if errUnmarshal := json.Unmarshal([]byte(raw), &cfg); errUnmarshal != nil {
		return Config{}, fmt.Errorf("parse %s: %w", ConfigEnv, errUnmarshal)
	}
	return cfg, nil
}

// Binder records which credential owns which exit.
//
// Ownership is exclusive: two credentials on one exit is the condition this
// package exists to prevent, so the second bind wins nothing and reports the
// conflict instead of silently sharing.
type Binder struct {
	mu     sync.Mutex
	owners map[string]string // pool ID -> credential ID
	byCred map[string]string // credential ID -> pool ID
}

// NewBinder builds an empty binder.
func NewBinder() *Binder {
	return &Binder{
		owners: make(map[string]string),
		byCred: make(map[string]string),
	}
}

// Bind assigns an exit to a credential and reports a conflict if another
// credential already holds it. Rebinding the same pair is a no-op.
func (b *Binder) Bind(poolID, credentialID string) error {
	poolID = strings.TrimSpace(poolID)
	credentialID = strings.TrimSpace(credentialID)
	if poolID == "" {
		return fmt.Errorf("egress: pool id is empty")
	}
	if credentialID == "" {
		return fmt.Errorf("egress: credential id is empty")
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if owner, taken := b.owners[poolID]; taken && owner != credentialID {
		return fmt.Errorf("egress: pool %q is already bound to another credential", poolID)
	}
	// A credential that moves must release its previous exit, or the old exit
	// stays reserved for a credential that no longer uses it.
	if previous, existed := b.byCred[credentialID]; existed && previous != poolID {
		delete(b.owners, previous)
	}
	b.owners[poolID] = credentialID
	b.byCred[credentialID] = poolID
	return nil
}

// Release frees the exit held by a credential.
func (b *Binder) Release(credentialID string) {
	credentialID = strings.TrimSpace(credentialID)
	if credentialID == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if poolID, existed := b.byCred[credentialID]; existed {
		delete(b.owners, poolID)
		delete(b.byCred, credentialID)
	}
}

// PoolFor reports the exit a credential is bound to.
func (b *Binder) PoolFor(credentialID string) (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	poolID, exists := b.byCred[strings.TrimSpace(credentialID)]
	return poolID, exists
}

// Check validates a credential against the policy and the binder.
//
// It returns the pool the caller should use. Under enforcement an unbound or
// unknown pool is an error: falling back to a direct connection would defeat
// the point of binding and hide the misconfiguration.
func Check(cfg Config, binder *Binder, credentialID, declaredPool string) (Pool, error) {
	credentialID = strings.TrimSpace(credentialID)
	declaredPool = strings.TrimSpace(declaredPool)

	if !cfg.Enforce {
		return cfg.Pools[declaredPool], nil
	}
	if credentialID == "" {
		return Pool{}, fmt.Errorf("egress: credential has no id; cannot enforce binding")
	}
	if declaredPool == "" {
		return Pool{}, fmt.Errorf("egress: credential %q has no %s; binding is enforced", credentialID, BindingEnv)
	}
	pool, known := cfg.Pools[declaredPool]
	if !known {
		return Pool{}, fmt.Errorf("egress: credential %q declares unknown pool %q", credentialID, declaredPool)
	}
	if binder != nil {
		if errBind := binder.Bind(declaredPool, credentialID); errBind != nil {
			return Pool{}, errBind
		}
	}
	return pool, nil
}
