# Codegen Partial Evaluation Design

## Goal

Make the generated agent and MCP adapter code reflect the Goa design directly,
instead of rediscovering design-known structure at runtime.

## Problem

The current codegen is mostly template-specialized, but a few generated paths
still violate the repository's partial-evaluation rule:

- agent config validation builds a runtime `[]string` of required MCP caller IDs
  and loops over it, even though the set of required IDs is static.
- the generated MCP resource adapter round-trips typed payloads through JSON,
  unmarshals to `map[string]any`, sorts keys, and iterates them to rebuild query
  parameters, even though the payload shape is known when the template runs.
- the generated agent registry lazily builds hint-template maps at runtime even
  though the hinted tool set is fixed by the DSL.

These paths still work, but they generate generic runtime code where the
generator should emit specialized code.

## Decision

### 1. Specialize MCP caller validation in generated agent config

`codegen/agent/templates/config.go.tpl` should emit one direct nil-check per
known MCP toolset binding instead of constructing a runtime slice and iterating
it.

The generated code should look like design-owned validation:

- if `MCPCallers` is nil, fail immediately when any MCP toolset exists
- for each known toolset constant, emit a direct lookup and error path

No runtime collection-building for design-known IDs should remain.

### 2. Precompute resource query structure in adapter generator data

`codegen/mcp/adapter_generator.go` should compute resource-query metadata ahead
of template execution:

- deterministic field order
- field name / query key
- scalar vs repeated encoding shape
- direct access path from the typed payload

`codegen/mcp/templates/mcp_client_wrapper.go.tpl` should then emit field-aware
query construction directly from that metadata.

The generated resource adapter must no longer:

- encode payloads to JSON only to inspect them again
- unmarshal into `map[string]any`
- sort runtime keys
- run a generic switch over map values to determine repeated parameters

### 3. Specialize registry hint template maps

`codegen/agent/templates/registry.go.tpl` should emit either:

- no hint-map code when there are no hint templates, or
- a direct composite literal when there are known hint templates

The generated code should not lazily initialize maps with `if callRaw == nil`
or `if resultRaw == nil` for template-known tool hints.

## Resulting Contract

### Generator contract

- the generator owns structural decisions derived from the DSL
- generated runtime code only handles truly runtime values: payload contents,
  HTTP responses, stream results, and execution errors

### MCP adapter contract

- resource query construction is statically shaped by the bound payload type
- runtime code reads values and encodes them, but does not infer schema

### Agent config / registry contract

- generated validation and hint wiring are direct consequences of the bound
  toolsets and tools
- no generic loops or lazy maps are emitted for design-known lists

## Consequences

### Benefits

- generated code becomes easier to read and reason about
- fewer generic runtime branches remain in agent/MCP output
- template output more honestly reflects the DSL
- tests can lock in the absence of runtime rediscovery algorithms

### Trade-off

The generator data and templates become slightly richer because more work moves
into codegen. This is intentional: smart codegen is preferable to dumping
design structure into runtime helper logic.

## Implementation Notes

- add targeted tests that assert generated code no longer contains the runtime
  loop in config validation
- add targeted tests that assert generated MCP resource wrappers no longer
  contain `map[string]any`, `json.Unmarshal`, or runtime key sorting
- refresh only the goldens affected by the specialized output
- keep behavior the same; this is a specialization refactor, not a protocol
  change
