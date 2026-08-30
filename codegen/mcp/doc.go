// Package codegen contains the MCP generator built on top of Goa evaluation and
// Goa's JSON-RPC codegen.
//
// # Codegen Philosophy
//
// An MCP-enabled service may also expose ordinary HTTP, file, and gRPC
// endpoints. Its service-level JSON-RPC declaration supplies the path for the
// generated MCP endpoint, while Goa preserves the other transports and rejects
// routes that would register two handlers for the same method and path. The
// package derives all per-run state from the evaluated Goa roots, builds a
// synthetic MCP service expression, and lets Goa generate the JSON-RPC
// transport and client code that MCP needs.
//
// Where MCP needs behavior beyond Goa's standard JSON-RPC generator
// (tool/resource/prompt adapters, MCP-specific clients, helper packages), this
// package emits dedicated files around Goa's output. The generator still
// rewrites a narrow set of example/CLI sections because Goa does not yet expose
// smaller hooks for that scaffolding; the docs should stay honest about that
// coupling until the underlying hook surface improves.
package codegen
