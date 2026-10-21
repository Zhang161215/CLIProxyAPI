package executor

import (
	"strings"
	"testing"
)

// cc_workload carries an opaque two-letter code. Emitting a descriptive string
// publishes a value no released client sends.
func TestBillingHeaderAcceptsKnownWorkloadCodes(t *testing.T) {
	for _, code := range []string{"ki", "tn", "ny", "ng"} {
		header := generateBillingHeader(true, "2.1.280", "hello world test", "sdk-cli", code, false, "", "pid")
		if !strings.Contains(header, "cc_workload="+code+";") {
			t.Fatalf("code %q missing from %q", code, header)
		}
	}
}

func TestBillingHeaderDropsUnknownWorkloadValue(t *testing.T) {
	header := generateBillingHeader(true, "2.1.280", "hello world test", "sdk-cli", "main", false, "", "pid")
	if strings.Contains(header, "cc_workload") {
		t.Fatalf("unknown workload leaked into the header: %q", header)
	}
}

func TestBillingHeaderOmitsWorkloadWhenUnset(t *testing.T) {
	header := generateBillingHeader(true, "2.1.280", "hello world test", "sdk-cli", "", false, "", "pid")
	if strings.Contains(header, "cc_workload") {
		t.Fatalf("unset workload must stay absent: %q", header)
	}
}

func TestNormalizeClaudeWorkloadIsCaseAndSpaceTolerant(t *testing.T) {
	if got := normalizeClaudeWorkload("  KI "); got != "ki" {
		t.Fatalf("normalize = %q, want ki", got)
	}
	if got := normalizeClaudeWorkload("nope"); got != "" {
		t.Fatalf("normalize = %q, want empty", got)
	}
}
