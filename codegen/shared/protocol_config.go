package shared

// ProtocolConfig defines protocol-specific settings for code generation.
// Both MCP implement this interface to provide their specific
// configuration values.
type ProtocolConfig interface {
	// JSONRPCPath returns the path for JSON-RPC endpoints.
	JSONRPCPath() string
	// ProtocolVersion returns the protocol version string.
	ProtocolVersion() string
	// Capabilities returns protocol-specific capabilities for initialization.
	Capabilities() map[string]any
	// Name returns the protocol name (e.g., "MCP").
	Name() string
}
