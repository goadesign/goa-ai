// Package codegen contains the MCP generator built on top of Goa evaluation and
// Goa's JSON-RPC codegen.
//
// # Codegen Philosophy
//
// MCP code generation is intentionally fail-fast and transport-closed:
// MCP-enabled services either generate a pure MCP surface or generation fails.
// The package derives all per-run state from the evaluated Goa roots, then
// builds a synthetic MCP service expression and lets Goa generate the JSON-RPC
// transport/client code that MCP needs.
//
// Where MCP needs behavior beyond Goa's standard JSON-RPC generator
// (tool/resource/prompt adapters, MCP-specific clients, helper packages), this
// package emits dedicated files around Goa's output. The generator still
// rewrites a narrow set of example/CLI sections because Goa does not yet expose
// smaller hooks for that scaffolding; the docs should stay honest about that
// coupling until the underlying hook surface improves.
package codegen
