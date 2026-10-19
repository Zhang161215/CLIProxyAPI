package executor

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/antidetect/audit"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/antidetect/monitor"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// ProfileInterval is how often per-credential profiles are written out.
//
// One line per credential per interval is cheap; the value is that after a
// revocation the profile of the hours before it still exists.
const ProfileInterval = 5 * time.Minute

var (
	monitorOnce     sync.Once
	monitorRegistry *monitor.Registry
)

func claudeProfiles() *monitor.Registry {
	monitorOnce.Do(func() {
		monitorRegistry = monitor.NewRegistry(nil)
		go flushProfilesForever()
	})
	return monitorRegistry
}

// setClaudeMonitorForTest swaps the registry and returns a restore func.
func setClaudeMonitorForTest(registry *monitor.Registry) func() {
	previous := claudeProfiles()
	monitorRegistry = registry
	monitorOnce.Do(func() {})
	return func() { monitorRegistry = previous }
}

// flushProfilesForever writes every profile and resets it, so each interval
// describes a window rather than a lifetime.
func flushProfilesForever() {
	ticker := time.NewTicker(ProfileInterval)
	defer ticker.Stop()
	for range ticker.C {
		flushProfiles()
	}
}

func flushProfiles() {
	registry := claudeProfiles()
	for _, snapshot := range registry.Snapshots() {
		audit.Log(audit.Record{
			Event:        "credential_profile",
			Decision:     audit.DecisionAccepted,
			CredentialID: audit.ID(snapshot.CredentialID),
			Summary:      formatProfile(snapshot),
			Detail:       strings.Join(snapshot.RiskSignals(), "; "),
		})
		registry.Reset(snapshot.CredentialID)
	}
}

// formatProfile renders one profile as key=value pairs for later parsing.
func formatProfile(snapshot monitor.Snapshot) string {
	return fmt.Sprintf("requests=%d allowed=%d blocked=%d paced=%d pace_rejected=%d ratelimited=%d ips=%d uas=%d entry=%d sessions=%d max_util=%.2f",
		snapshot.Requests, snapshot.Allowed, snapshot.Blocked, snapshot.Paced, snapshot.PaceRejected,
		snapshot.RateLimited, snapshot.DistinctIPs, snapshot.DistinctUAs, snapshot.DistinctEntry,
		snapshot.DistinctSess, snapshot.MaxUtilization)
}

// clientIPFromContext returns the downstream address recorded by gin, if any.
func clientIPFromContext(ctx context.Context) string {
	if ginCtx, ok := ctx.Value("gin").(*gin.Context); ok && ginCtx != nil && ginCtx.Request != nil {
		return ginCtx.ClientIP()
	}
	return ""
}

func userAgentFromContext(ctx context.Context) string {
	headers := helps.IncomingHeadersFromContext(ctx)
	if headers == nil {
		return ""
	}
	return strings.TrimSpace(headers.Get("User-Agent"))
}

func entrypointFromContext(ctx context.Context) string {
	userAgent := userAgentFromContext(ctx)
	// The entrypoint is the second field of "claude-cli/<ver> (external, <entry>, ...)".
	const marker = "external,"
	index := strings.Index(userAgent, marker)
	if index < 0 {
		return ""
	}
	rest := strings.TrimSpace(userAgent[index+len(marker):])
	if end := strings.IndexAny(rest, ",)"); end >= 0 {
		rest = rest[:end]
	}
	return strings.TrimSpace(rest)
}

func sessionIDFromContext(ctx context.Context) string {
	headers := helps.IncomingHeadersFromContext(ctx)
	if headers == nil {
		return ""
	}
	return strings.TrimSpace(headers.Get("X-Claude-Code-Session-Id"))
}

// observeClaudeRequest records one admitted request against its credential.
func observeClaudeRequest(ctx context.Context, auth *cliproxyauth.Auth) {
	credentialID := authCredentialID(auth)
	if credentialID == "" {
		return
	}
	claudeProfiles().Observe(credentialID,
		clientIPFromContext(ctx),
		userAgentFromContext(ctx),
		entrypointFromContext(ctx),
		sessionIDFromContext(ctx),
		true,
	)
}

func recordClaudeBlock(auth *cliproxyauth.Auth, rule string) {
	if credentialID := authCredentialID(auth); credentialID != "" {
		claudeProfiles().Block(credentialID, rule)
	}
}

func recordClaudePace(auth *cliproxyauth.Auth) {
	if credentialID := authCredentialID(auth); credentialID != "" {
		claudeProfiles().Pace(credentialID)
	}
}

func recordClaudePaceRejected(auth *cliproxyauth.Auth) {
	if credentialID := authCredentialID(auth); credentialID != "" {
		claudeProfiles().PaceRejected(credentialID)
	}
}

// recordClaudeRateLimit notes that the upstream answered 429 for this
// credential. The call may still succeed after a retry; the count is about the
// credential being watched, not about a failed request.
func recordClaudeRateLimit(auth *cliproxyauth.Auth) {
	if credentialID := authCredentialID(auth); credentialID != "" {
		claudeProfiles().RateLimit(credentialID)
	}
}

func recordClaudeUtilization(auth *cliproxyauth.Auth, utilization float64) {
	if credentialID := authCredentialID(auth); credentialID != "" {
		claudeProfiles().Utilization(credentialID, utilization)
	}
}
