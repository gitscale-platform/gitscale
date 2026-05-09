package mcp

import (
	"github.com/gitscale-platform/gitscale/plane/application/restapi"
)

// DeferredDefaultProtocolVersion is the MCP protocol-version string
// used when the operator has not set MCP_PROTOCOL_VERSION explicitly.
//
// TODO(adr,jul-2026): Pin a chosen MCP protocol version via ADR. Until
// then this value is the latest published draft string at implement
// time and a WARN log fires at boot when it is used unmodified. See
// CLAUDE.md §"Open architecture questions" and
// docs/superpowers/specs/2026-05-09-issue-112-mcp-server-design.md.
const DeferredDefaultProtocolVersion = "2025-06-18"

// Config bundles the configuration knobs for an MCP server.
//
// ProtocolVersion is returned verbatim from `initialize`; clients that
// pin a different draft are NOT rejected (lenient negotiation, see
// spec). Operators flip this without a code change once the July 2026
// ADR resolves.
//
// RateConfig parameters the per-principal token-bucket on SurfaceMCP.
// Distinct from the REST surface so MCP traffic does not starve the
// REST API and vice-versa (ADR-012).
//
// SessionHMACSecret is the HMAC key used to sign session tokens
// returned by `initialize` and verified on every subsequent
// `tools/list` / `tools/call` request. Must be at least 32 bytes; a
// shorter value causes NewServer to return an error rather than
// silently degrading.
type Config struct {
	ProtocolVersion   string
	RateConfig        restapi.RateConfig
	ServerName        string
	ServerVersion     string
	SessionHMACSecret []byte
}
