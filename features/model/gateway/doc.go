// Package gateway provides a transport-agnostic server and client wrapper
// for model completion. It exposes composable middleware for unary and
// streaming requests and can be paired with any RPC layer by supplying
// provider and caller functions that operate on the runtime model types.
package gateway
