package executor

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

// A client configured against a relay writes its gateway admission into the
// billing line inside system text. It must not survive to the upstream.
func TestStripClaudeGatewayMarkerFromStringSystem(t *testing.T) {
	body := []byte(`{"system":"x-anthropic-billing-header: cc_version=2.1.303.c24; cc_entrypoint=sdk-cli; cch=ed74f; cc_gateway=env; cc_prompt_id=abc;"}`)
	out := string(stripClaudeGatewayMarker(body))
	if strings.Contains(out, "cc_gateway") {
		t.Fatalf("marker survived: %s", out)
	}
	for _, want := range []string{"cc_version=2.1.303.c24", "cc_entrypoint=sdk-cli", "cch=ed74f", "cc_prompt_id=abc"} {
		if !strings.Contains(out, want) {
			t.Fatalf("removed too much, missing %q: %s", want, out)
		}
	}
	if strings.Contains(out, "  ") {
		t.Fatalf("left a double space: %s", out)
	}
}

func TestStripClaudeGatewayMarkerFromBlockSystem(t *testing.T) {
	body := []byte(`{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.303.c24; cc_entrypoint=sdk-cli; cc_gateway=1;"}]}`)
	out := stripClaudeGatewayMarker(body)
	text := gjson.GetBytes(out, "system.0.text").String()
	if strings.Contains(text, "cc_gateway") {
		t.Fatalf("marker survived in block: %s", text)
	}
	if !strings.Contains(text, "cc_version=2.1.303.c24") {
		t.Fatalf("removed the wrong field: %s", text)
	}
}

func TestStripClaudeGatewayMarkerNoopWhenAbsent(t *testing.T) {
	body := []byte(`{"system":"x-anthropic-billing-header: cc_version=2.1.280.3a6; cc_entrypoint=sdk-cli; cch=b8d98; cc_prompt_id=abc;"}`)
	if out := stripClaudeGatewayMarker(body); string(out) != string(body) {
		t.Fatalf("body changed although there was no marker: %s", out)
	}
}

// The scrubber must not touch ordinary text that merely mentions the word.
func TestStripClaudeGatewayMarkerIgnoresUnrelatedText(t *testing.T) {
	body := []byte(`{"system":"You are Claude Code. Mention cc_gateway when explaining proxies."}`)
	out := string(stripClaudeGatewayMarker(body))
	if !strings.Contains(out, "Mention cc_gateway when explaining") {
		t.Fatalf("ordinary prose was modified: %s", out)
	}
}

func TestScrubGatewayMarkerTextHandlesMissingTerminator(t *testing.T) {
	got, changed := scrubGatewayMarkerText("cc_version=2.1.303.c24; cc_gateway=env")
	if !changed {
		t.Fatal("unterminated marker not removed")
	}
	if strings.Contains(got, "cc_gateway") {
		t.Fatalf("marker survived: %s", got)
	}
}
