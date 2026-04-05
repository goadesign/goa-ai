// Package toolregistry defines the canonical wire protocol shared by registry
// providers, executors, and the clustered registry service.
//
// This package is intentionally narrower than the other "registry" concepts in
// goa-ai:
//   - DSL `Registry(...)` / `FromRegistry(...)` describe remote tool catalogs and
//     dynamic toolset references.
//   - Generated `gen/<svc>/registry/<name>/` packages are agent-side clients and
//     helpers for one declared DSL registry source.
//   - The `registry/` package implements the standalone clustered registry
//     service that stores toolsets, tracks provider health, and routes calls.
//   - `runtime/toolregistry` defines only the Pulse stream IDs, message
//     envelopes, trace propagation, and output-delta plumbing those components
//     share.
package toolregistry
