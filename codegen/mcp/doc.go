// Package codegen contains generators and templates used to produce MCP service
// adapters, transports, and client wrappers on top of Goa-generated code.
//
// # Codegen Philosophy
//
// MCP code generation is intentionally template-driven and fail-fast. The goal
// is to keep generated behavior stable and comprehensible without relying on
// string-based patching of third-party generated output.
//
// Where MCP needs behavior beyond Goa's standard JSON-RPC generator (e.g.,
// tool adapters, registries, retry hints), this package emits dedicated MCP
// packages and helpers rather than mutating Goa-generated files.
package codegen
