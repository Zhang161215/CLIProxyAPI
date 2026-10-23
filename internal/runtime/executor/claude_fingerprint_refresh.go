package executor

import (
	"regexp"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// claudeBillingVersionPattern captures the version and the three hex digits of
// the attribution fingerprint inside the billing line.
var claudeBillingVersionPattern = regexp.MustCompile(`cc_version=([0-9]+\.[0-9]+\.[0-9]+)(?:\.[0-9a-f]{3})?`)

// refreshClaudeBillingFingerprint recomputes the cc_version suffix from the body
// as it will actually be sent.
//
// The suffix is SHA256(salt + firstUserText[4,7,20] + version)[:3], and the
// upstream verifies it. It is written early, when the request enters the
// pipeline, but payload rules and sensitive-word replacement can still rewrite
// the first user message afterwards; the suffix would then describe text the
// upstream never receives. That is a failed attribution check, which is a
// stronger signal than no fingerprint at all.
func refreshClaudeBillingFingerprint(body []byte) []byte {
	if len(body) == 0 {
		return body
	}
	billing := gjson.GetBytes(body, "system.0.text")
	if billing.Type != gjson.String || !strings.HasPrefix(billing.String(), "x-anthropic-billing-header:") {
		return body
	}
	text := billing.String()
	match := claudeBillingVersionPattern.FindStringSubmatch(text)
	if match == nil {
		return body
	}
	version := match[1]
	fresh := computeFingerprint(claudeBillingFingerprintMessageText(body), version)

	replaced := claudeBillingVersionPattern.ReplaceAllStringFunc(text, func(found string) string {
		return "cc_version=" + version + "." + fresh
	})
	if replaced == text {
		return body
	}
	updated, errSet := sjson.SetBytes(body, "system.0.text", replaced)
	if errSet != nil {
		return body
	}
	return updated
}
