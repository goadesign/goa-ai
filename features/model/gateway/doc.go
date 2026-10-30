// Package gateway provides transport-agnostic server and client wrappers and a
// Goa-generated gRPC transport for model completion. Unary and streaming
// requests support middleware. Exact token counting is a separate optional
// operation: transports that implement it use NewCountingRemoteClient or
// NewGRPCClient, while NewRemoteClient reports that counting is unsupported.
package gateway
