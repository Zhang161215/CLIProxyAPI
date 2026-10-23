package executor

import (
	"strings"
	"testing"
)

// The startup line has to reflect the environment, otherwise it cannot answer
// the question it exists for: did the variables reach this process?
func TestAntidetectOptionsSummaryReflectsEnvironment(t *testing.T) {
	t.Setenv(RateEnv, "2.5")
	t.Setenv(BurstEnv, "4")
	t.Setenv(ContentGateEnv, "off")
	t.Setenv(WindowGateEnv, "")
	summary := AntidetectOptionsSummary()
	for _, want := range []string{"rate=2.5", "burst=4", "content_gate=off", "window_gate=on"} {
		if !strings.Contains(summary, want) {
			t.Fatalf("summary %q missing %q", summary, want)
		}
	}
}

func TestAntidetectOptionsSummaryDefaultsAreConservative(t *testing.T) {
	t.Setenv(RateEnv, "")
	t.Setenv(BurstEnv, "")
	summary := AntidetectOptionsSummary()
	if !strings.Contains(summary, "rate=0") {
		t.Fatalf("rate should default to 0 (no pacing): %q", summary)
	}
	if !strings.Contains(summary, "content_gate=on") {
		t.Fatalf("content gate should default to on: %q", summary)
	}
}

func TestAntidetectOptionsSummaryEgressStates(t *testing.T) {
	t.Setenv("CPA_ANTIDETECT_EGRESS", "")
	if got := AntidetectOptionsSummary(); !strings.Contains(got, "egress=off(default)") {
		t.Fatalf("unset egress: %q", got)
	}
	t.Setenv("CPA_ANTIDETECT_EGRESS", `{"enforce":true}`)
	if got := AntidetectOptionsSummary(); !strings.Contains(got, "egress=enforce") {
		t.Fatalf("enforcing egress: %q", got)
	}
	t.Setenv("CPA_ANTIDETECT_EGRESS", `{"enforce":false}`)
	if got := AntidetectOptionsSummary(); !strings.Contains(got, "egress=observe") {
		t.Fatalf("observing egress: %q", got)
	}
}
