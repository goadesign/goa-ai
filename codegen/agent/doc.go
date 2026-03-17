// Package codegen implements the Goa plugin that turns the agents DSL into
// runtime-ready Go packages. The generator mirrors Goa's own pipeline:
// evaluated roots are converted into template data, toolset ownership is
// resolved directly from those roots inside this package, and the final files
// are rendered through goa.design/goa/v3/codegen.Files.
//
// Toolsets fall into two main categories and drive different generated helpers:
//   - Service-backed toolsets (method-backed tools declared in Uses blocks) emit
//     per-toolset executor factories, RegisterUsedToolsets, and With...Executor
//     helpers so applications can bind service clients and register toolsets.
//   - Agent-exported toolsets (declared in Exports blocks) emit agenttools
//     helpers in the provider agent package plus thin consumer helpers in
//     agents that Use the exported toolset; applications wire these via
//     runtime.NewAgentToolsetRegistration and runtime.AgentToolOption.
//
// MCP-backed toolsets are owned by the provider service for specs/codecs but
// still emit consumer-local executors and registration helpers. That ownership
// contract is resolved here from evaluated agent/Goa roots rather than via a
// separate intermediate IR package.
//
// Generator contracts (keep these consistent)
//
//   - Naming and scoping:
//
//   - Use a single goa `codegen.NameScope` per generated package to ensure
//     deterministic naming.
//
//   - Prefer `scope.HashedUnique(ut, baseName)` for stable disambiguation.
//     It guarantees the same type hash always maps to the same emitted name.
//
//   - Type references and locators:
//
//   - Never build Go type references via string concatenation. Always use
//     the Goa NameScope helpers:
//
//   - `GoTypeName/GoTypeRef` for same-package references.
//
//   - `GoFullTypeName/GoFullTypeRef` when the defining package may differ.
//
//   - Respect type locators (`Meta("struct:pkg:path", "...")`): when present,
//     `codegen.UserTypeLocation` and the scope helpers will qualify types
//     correctly and ensure imports can be derived.
//
//   - Imports:
//
//   - Prefer `codegen.Header` + import specs and let Goa prune unused imports.
//     Generator code should not attempt ad-hoc import cleanup.
//
//   - Defaults (critical for correctness):
//
//   - Goa’s defaulting is coupled to pointer semantics:
//
//   - JSON decode-body helper types use pointer fields so we can distinguish
//     “missing” from “zero”.
//
//   - Final *payload* types use `useDefault=true` so defaulted optional
//     primitives are values (non-pointers) and defaults can be injected by
//     `codegen.GoTransform`.
//
//   - Any transform that reads tool payload fields must use a matching
//     AttributeContext (`UseDefault=true`) or GoTransform will emit invalid
//     nil checks/dereferences against value fields.
package codegen
