package executor

import (
	"context"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/antidetect/monitor"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestObserveClaudeRequestRecordsProfile(t *testing.T) {
	registry := monitor.NewRegistry(nil)
	restore := setClaudeMonitorForTest(registry)
	defer restore()

	headers := http.Header{}
	headers.Set("User-Agent", "claude-cli/2.1.280 (external, sdk-cli)")
	headers.Set("X-Claude-Code-Session-Id", "11111111-2222-4333-8444-555555555555")
	ctx := helps.WithIncomingHeaders(context.Background(), headers)

	auth := &cliproxyauth.Auth{ID: "cred-monitor"}
	observeClaudeRequest(ctx, auth, "test-key")
	observeClaudeRequest(ctx, auth, "test-key")

	snapshots := registry.Snapshots()
	if len(snapshots) != 1 {
		t.Fatalf("snapshots = %d, want 1", len(snapshots))
	}
	snapshot := snapshots[0]
	if snapshot.Requests != 2 || snapshot.Allowed != 2 {
		t.Fatalf("requests=%d allowed=%d", snapshot.Requests, snapshot.Allowed)
	}
	if snapshot.DistinctUAs != 1 {
		t.Fatalf("distinct UAs = %d", snapshot.DistinctUAs)
	}
	if snapshot.DistinctSess != 1 {
		t.Fatalf("distinct sessions = %d", snapshot.DistinctSess)
	}
}

func TestEntrypointParsing(t *testing.T) {
	cases := map[string]string{
		"claude-cli/2.1.280 (external, cli)":             "cli",
		"claude-cli/2.1.280 (external, sdk-cli)":         "sdk-cli",
		"claude-cli/2.1.280 (external, future-x, sdk/1)": "future-x",
		"not-a-claude-ua":                                "",
	}
	for userAgent, want := range cases {
		headers := http.Header{}
		headers.Set("User-Agent", userAgent)
		ctx := helps.WithIncomingHeaders(context.Background(), headers)
		if got := entrypointFromContext(ctx); got != want {
			t.Fatalf("entrypointFromContext(%q) = %q, want %q", userAgent, got, want)
		}
	}
}

func TestFormatProfileIsParseable(t *testing.T) {
	got := formatProfile(monitor.Snapshot{Requests: 3, Allowed: 2, DistinctIPs: 2, MaxUtilization: 0.95})
	for _, want := range []string{"requests=3", "allowed=2", "ips=2", "max_util=0.95"} {
		if !containsField(got, want) {
			t.Fatalf("summary %q missing %q", got, want)
		}
	}
}

func containsField(haystack, needle string) bool {
	for _, part := range splitFields(haystack) {
		if part == needle {
			return true
		}
	}
	return false
}

func splitFields(value string) []string {
	var out []string
	current := ""
	for _, r := range value {
		if r == ' ' {
			if current != "" {
				out = append(out, current)
				current = ""
			}
			continue
		}
		current += string(r)
	}
	if current != "" {
		out = append(out, current)
	}
	return out
}
