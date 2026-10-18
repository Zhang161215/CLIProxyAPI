// Package audit writes one structured line per anti-detection decision.
//
// When an account is revoked, the only question that matters is which signal
// the decision rested on. Without a record, the answer is a guess, and the next
// account repeats the mistake. The record is intentionally narrow: it names the
// decision and the evidence for it, and never copies the request.
package audit

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

// Decision is the outcome of a gate.
type Decision string

const (
	DecisionAllowed  Decision = "allowed"
	DecisionBlocked  Decision = "blocked"
	DecisionPaced    Decision = "paced"
	DecisionUnknown  Decision = "unknown_client"
	DecisionAccepted Decision = "accepted"
	// DecisionMissing means an expected signal never arrived.
	DecisionMissing Decision = "missing"
	// DecisionCollision means two credentials look like the same machine.
	DecisionCollision Decision = "collision"
	// DecisionDrift means an exit address changed after it was recorded.
	DecisionDrift Decision = "drift"
)

// Record is one auditable decision.
//
// CredentialID must already be hashed by the caller via ID(). Raw identifiers,
// emails and tokens never reach this struct.
type Record struct {
	Time         time.Time
	Event        string
	Decision     Decision
	CredentialID string
	Model        string
	Entrypoint   string
	ClientState  string
	Rule         string
	Category     string
	Excerpt      string
	Detail       string
	// Summary carries a compact, parseable profile line for post-mortems.
	Summary string
}

// ID hashes a credential identifier for correlation without disclosure.
//
// A stable hash is enough to correlate every request from one credential; the
// raw value adds nothing and turns a log file into a target.
func ID(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	sum := sha256.Sum256([]byte("cpa-audit|" + trimmed))
	return hex.EncodeToString(sum[:6])
}

// Log writes one record.
//
// Fields are emitted individually rather than as a marshalled blob so the
// existing log pipeline can index them, and so an empty field is simply absent.
func Log(record Record) {
	if record.Time.IsZero() {
		record.Time = time.Now().UTC()
	}
	fields := log.Fields{
		"audit":    "antidetect",
		"event":    record.Event,
		"decision": string(record.Decision),
	}
	optional := []struct {
		key   string
		value string
	}{
		{"credential", record.CredentialID},
		{"model", record.Model},
		{"entrypoint", record.Entrypoint},
		{"client_state", record.ClientState},
		{"rule", record.Rule},
		{"category", record.Category},
		{"excerpt", record.Excerpt},
		{"detail", record.Detail},
		{"summary", record.Summary},
	}
	for _, item := range optional {
		if item.value != "" {
			fields[item.key] = item.value
		}
	}

	if record.Decision == DecisionBlocked {
		log.WithFields(fields).Warn("antidetect decision")
		return
	}
	log.WithFields(fields).Info("antidetect decision")
}
