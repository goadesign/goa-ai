package shared

// ProtocolConfig supplies the JSON-RPC route path needed to build transport
// expressions. Shared transport helpers deliberately depend on this minimal
// contract so protocol-specific metadata stays in the owning generator package.
type ProtocolConfig interface {
	// JSONRPCPath returns the path for JSON-RPC endpoints.
	JSONRPCPath() string
}
