// Package codegen implements the Goa plugin responsible for turning the agents
// DSL expressions into runtime-ready Go packages. The plugin mirrors Goa's own
// codegen pipeline: expressions are collected during evaluation, converted into
// intermediary data structures during the prepare phase, then rendered through
// templates via goa.design/goa/v3/codegen.Files.
//
// Toolsets fall into two main categories and drive different generated helpers:
//   - Service-backed toolsets (method-backed tools declared in Uses blocks) emit
//     per-toolset executor factories, RegisterUsedToolsets, and With...Executor
//     helpers so applications can bind service clients and register toolsets.
//   - Agent-exported toolsets (declared in Exports blocks) emit agenttools
//     helpers in the provider agent package plus thin consumer helpers in
//     agents that Use the exported toolset; applications wire these via
//     runtime.NewAgentToolsetRegistration and runtime.AgentToolOption.
package codegen
