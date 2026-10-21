package executor

import (
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// claudeGatewayMarkerPrefix is the field a client adds to its billing line when
// ANTHROPIC_BASE_URL points somewhere other than an official host.
const claudeGatewayMarkerPrefix = "cc_gateway="

// stripClaudeGatewayMarker removes the client's gateway self-declaration from
// the billing line in the request body.
//
// The billing line lives in system text, not in an HTTP header, so a caller that
// was configured against a relay writes "cc_gateway=env;" into the body and the
// proxy forwards it verbatim. The header allowlist already drops the x-gateway
// family; this closes the same declaration on the body path. Only that one field
// is removed: version, entrypoint, cch and the attribution fields are left
// untouched so the line stays exactly as the client would have written it minus
// the admission.
func stripClaudeGatewayMarker(body []byte) []byte {
	if len(body) == 0 || !strings.Contains(string(body), claudeGatewayMarkerPrefix) {
		return body
	}
	system := gjson.GetBytes(body, "system")
	if !system.Exists() {
		return body
	}

	out := body
	if system.Type == gjson.String {
		if next, changed := scrubGatewayMarkerText(system.String()); changed {
			if updated, errSet := sjson.SetBytes(out, "system", next); errSet == nil {
				out = updated
			}
		}
		return out
	}

	index := 0
	system.ForEach(func(_, block gjson.Result) bool {
		if block.Get("type").String() == "text" {
			text := block.Get("text").String()
			if next, changed := scrubGatewayMarkerText(text); changed {
				if updated, errSet := sjson.SetBytes(out, "system."+strconv.Itoa(index)+".text", next); errSet == nil {
					out = updated
				}
			}
		}
		index++
		return true
	})
	return out
}

// scrubGatewayMarkerText removes every cc_gateway=<value>; occurrence, including
// the space that separated it from the previous field.
func scrubGatewayMarkerText(text string) (string, bool) {
	if !strings.Contains(text, claudeGatewayMarkerPrefix) {
		return text, false
	}
	sb := strings.Builder{}
	sb.Grow(len(text))
	rest := text
	changed := false
	for {
		index := strings.Index(rest, claudeGatewayMarkerPrefix)
		if index < 0 {
			sb.WriteString(rest)
			break
		}
		start := index
		// Drop the separator that preceded the field so no double space remains.
		if start > 0 && rest[start-1] == ' ' {
			start--
		}
		sb.WriteString(rest[:start])
		end := strings.IndexByte(rest[index:], ';')
		if end < 0 {
			// No terminator: the field runs to the end of the line.
			rest = ""
			changed = true
			break
		}
		rest = rest[index+end+1:]
		changed = true
	}
	if !changed {
		return text, false
	}
	return sb.String(), true
}
