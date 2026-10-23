package executor

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/antidetect/breaker"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/antidetect/egress"
)

// AntidetectOptionsSummary renders the effective antidetect switches.
//
// Without this line the only way to tell whether CPA_ANTIDETECT_* reached the
// process is to run traffic through it and read the profile counters. On a
// Windows host the variables are easy to set in a scope the service never sees,
// so the startup log is the cheapest place to make that visible.
func AntidetectOptionsSummary() string {
	rate := envFloat(RateEnv, 0)
	burst := envFloat(BurstEnv, 1)
	return fmt.Sprintf(
		"rate=%g burst=%g max_wait=%s content_gate=%s window_gate=%s egress=%s breaker=%s window=%s",
		rate, burst, maxThrottleWait(),
		gateState(ContentGateEnv), gateState(WindowGateEnv), egressState(), breakerState(), windowState(),
	)
}

// gateState reports a switch that is on unless explicitly disabled.
func gateState(name string) string {
	raw := strings.ToLower(strings.TrimSpace(os.Getenv(name)))
	switch raw {
	case "", "1", "on", "true", "yes":
		return "on"
	case "0", "off", "false", "no":
		return "off"
	default:
		return "on(unparsed:" + raw + ")"
	}
}

// breakerState renders the 429 breaker configuration.
func breakerState() string {
	opts, enabled := breaker.OptionsFromEnv()
	if !enabled {
		return "off"
	}
	return fmt.Sprintf("%d/%s/%s", opts.Threshold, compactDuration(opts.Window), compactDuration(opts.Cooldown))
}

// windowState renders the utilization levels the gate acts on.
func windowState() string {
	if !windowGateEnabled() {
		return "off"
	}
	thresholds := windowThresholds()
	return fmt.Sprintf("degrade%g/block%g", thresholds.Degraded, thresholds.Blocked)
}

// compactDuration keeps whole minutes readable: 15m rather than 15m0s.
func compactDuration(value time.Duration) string {
	if value%time.Minute == 0 {
		return fmt.Sprintf("%dm", int(value/time.Minute))
	}
	return value.String()
}

// egressState reports whether the exit binder enforces its policy.
func egressState() string {
	raw := strings.TrimSpace(os.Getenv(egress.ConfigEnv))
	if raw == "" {
		return "off(default)"
	}
	if strings.Contains(raw, `"enforce":true`) || strings.Contains(raw, `"enforce": true`) {
		return "enforce"
	}
	return "observe"
}
