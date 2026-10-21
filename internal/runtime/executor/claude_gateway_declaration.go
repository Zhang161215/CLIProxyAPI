package executor

import (
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/antidetect/audit"
)

// gatewayDeclarationLog remembers which declarations were already reported.
//
// The point of the audit line is to tell the operator that their client is
// configured to announce itself as a gateway, so it must fire on the first
// occurrence of each channel and then stay quiet: a busy relay would otherwise
// produce one line per request.
var (
	gatewayDeclarationMu      sync.Mutex
	gatewayDeclarationSeen    = make(map[string]struct{}, 8)
	maxGatewayDeclarationSeen = 16
)

// recordGatewayDeclaration reports a client self-declaration once per channel.
func recordGatewayDeclaration(channel string) {
	if channel == "" {
		return
	}
	gatewayDeclarationMu.Lock()
	if _, seen := gatewayDeclarationSeen[channel]; seen {
		gatewayDeclarationMu.Unlock()
		return
	}
	if len(gatewayDeclarationSeen) >= maxGatewayDeclarationSeen {
		// The set is bounded; anything new after that is still stripped, only not
		// reported, which is the right trade for a diagnostic line.
		gatewayDeclarationMu.Unlock()
		return
	}
	gatewayDeclarationSeen[channel] = struct{}{}
	gatewayDeclarationMu.Unlock()

	audit.Log(audit.Record{
		Event:    "gateway_declaration",
		Decision: audit.DecisionMismatch,
		Detail:   "stripped " + channel + ": the client is announcing the relay it runs behind",
	})
}

// resetGatewayDeclarationLog clears the dedupe set. Tests use it; production
// keeps the record for the lifetime of the process.
func resetGatewayDeclarationLog() {
	gatewayDeclarationMu.Lock()
	gatewayDeclarationSeen = make(map[string]struct{}, 8)
	gatewayDeclarationMu.Unlock()
}
