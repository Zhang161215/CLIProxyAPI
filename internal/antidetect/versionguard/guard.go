package versionguard

import (
	"os"
	"strconv"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/antidetect/audit"
	log "github.com/sirupsen/logrus"
)

// AllowDriftEnv opts a deployment out of the startup drift check.
//
// The check is a guard rail, not a policy: an operator who intentionally runs a
// newer User-Agent against an older handshake needs an escape hatch that does
// not require a rebuild.
const AllowDriftEnv = "CPA_ANTIDETECT_ALLOW_VERSION_DRIFT"

// Options carries the operator's drift decision.
type Options struct {
	AllowDrift bool
}

// OptionsFromEnv reads the drift escape hatch from the environment.
func OptionsFromEnv() Options {
	raw := strings.TrimSpace(os.Getenv(AllowDriftEnv))
	if raw == "" {
		return Options{}
	}
	allowed, errParse := strconv.ParseBool(raw)
	if errParse != nil {
		log.Warnf("antidetect: ignoring unparsable %s=%q", AllowDriftEnv, raw)
		return Options{}
	}
	return Options{AllowDrift: allowed}
}

// Check logs the identity digest and returns an error when the identity drifted
// and the operator did not opt out.
//
// It never terminates the process: the caller decides, which keeps this usable
// from tests and from embedded SDK hosts.
func Check(profile Profile, opts Options) error {
	log.Infof("antidetect: %s", profile.Summary())

	driftErr := Assert(profile)
	if driftErr == nil {
		return nil
	}
	if opts.AllowDrift {
		log.Warnf("antidetect: %v (%s is set, continuing)", driftErr, AllowDriftEnv)
		audit.Log(audit.Record{
			Event:    "version_guard",
			Decision: audit.DecisionAccepted,
			Detail:   "drift accepted by operator: " + strings.Join(profile.Drift(), "; "),
		})
		return nil
	}

	// An unverified seed is a separate hazard from cross-layer drift: signing
	// with a guessed constant produces a stable wrong digest on every request.
	if _, verified := SeedFor(profile.Version); !verified {
		log.Warnf("antidetect: no verified CCH seed for %s; signed requests will be degraded "+
			"(known: %s)", profile.Version, strings.Join(KnownVersions(), ", "))
	}

	audit.Log(audit.Record{
		Event:    "version_guard",
		Decision: audit.DecisionBlocked,
		Detail:   strings.Join(profile.Drift(), "; "),
	})
	return driftErr
}
