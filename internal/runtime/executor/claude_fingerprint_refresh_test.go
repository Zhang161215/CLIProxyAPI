package executor

import (
	"strings"
	"testing"
)

func billingBody(t *testing.T, fingerprint, firstUserText string) []byte {
	t.Helper()
	header := "x-anthropic-billing-header: cc_version=2.1.303." + fingerprint + "; cc_entrypoint=sdk-cli; cch=00000;"
	body := "{\"system\":[{\"type\":\"text\",\"text\":" + jsonQuote(header) + "}]," +
		"\"messages\":[{\"role\":\"user\",\"content\":[{\"type\":\"text\",\"text\":" + jsonQuote(firstUserText) + "}]}]}"
	return []byte(body)
}

func jsonQuote(value string) string {
	replacer := strings.NewReplacer("\\", "\\\\", "\"", "\\\"")
	return "\"" + replacer.Replace(value) + "\""
}

// The suffix must describe the text the upstream receives, not the text that
// happened to be present when the header was first written.
func TestFingerprintRefreshFollowsTheFinalText(t *testing.T) {
	// The fingerprint samples indices 4, 7 and 20, so the two texts are built to
	// differ exactly there and nowhere the algorithm looks twice.
	original := "012345678901234567890123"
	edited := "0123X5678901234567890123"

	if computeFingerprint(original, "2.1.303") == computeFingerprint(edited, "2.1.303") {
		t.Fatal("test inputs produce the same fingerprint")
	}
	want := computeFingerprint(edited, "2.1.303")

	refreshed := refreshClaudeBillingFingerprint(billingBody(t, "000", edited))
	if !strings.Contains(string(refreshed), "cc_version=2.1.303."+want) {
		t.Fatalf("fingerprint not refreshed: %s", refreshed)
	}
}

func TestFingerprintRefreshLeavesBodiesWithoutBilling(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"hello"}]}`)
	if got := refreshClaudeBillingFingerprint(body); string(got) != string(body) {
		t.Fatalf("body changed although it has no billing line: %s", got)
	}
}

func TestFingerprintRefreshIsIdempotent(t *testing.T) {
	body := billingBody(t, "000", "please fix the failing tests")
	once := refreshClaudeBillingFingerprint(body)
	twice := refreshClaudeBillingFingerprint(once)
	if string(once) != string(twice) {
		t.Fatalf("second pass changed the body:\n%s\n%s", once, twice)
	}
}
