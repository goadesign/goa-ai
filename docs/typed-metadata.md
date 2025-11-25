# Typed tool sidecars and artifacts

This document captures how goa-ai exposes **typed tool sidecars** end-to-end
and how Aura uses them for sidecar artifacts (for example, full-fidelity time
series for chart rendering while the LLM sees a bounded view).

## ToolSpec.Sidecar and sidecar schemas

The agent plugin now emits, per tool:

- `Payload` `TypeSpec` (name, schema, codec)
- `Result` `TypeSpec` (name, schema, codec)
- Optional `Sidecar *TypeSpec`

The runtime struct is:

```go
type ToolSpec struct {
	Name        tools.Ident
	Service     string
	Toolset     string
	Description string
	Tags        []string
	IsAgentTool bool
	AgentID     string
	Payload     tools.TypeSpec
	Result      tools.TypeSpec
	Sidecar     *tools.TypeSpec
}
```

`tool_schemas.json` adds a `sidecar` section when present:

```json
{
  "tools": [
    {
      "id": "toolset.tool",
      "service": "svc",
      "toolset": "toolset",
      "title": "Title",
      "description": "Description",
      "tags": ["tag"],
      "payload":  { "name": "PayloadType",  "schema": { /* ... */ } },
      "result":   { "name": "ResultType",   "schema": { /* ... */ } },
      "sidecar":  { "name": "SidecarType",  "schema": { /* ... */ } }
    }
  ]
}
```

Sidecar schemas are **never** sent to model providers. They describe the
shape of `planner.ToolResult.Sidecar` for tools that declare a sidecar type.

## Declaring sidecars in the DSL

Tools may declare an optional typed sidecar schema:

```go
Tool("get_time_series", "Get Time Series", func() {
    Args(AtlasGetTimeSeriesToolArgs)
    Return(AtlasGetTimeSeriesToolReturn)
    Sidecar(AtlasGetTimeSeriesSidecar)
})
```

Sidecar follows the same patterns as `Args`/`Return`:

- Inline object definitions
- Reuse of user types (`Type`, `ResultType`, shared types)
- Primitive types when appropriate

A common pattern is a wrapper that carries a full artifact:

```go
var AtlasGetTimeSeriesSidecar = Type("AtlasGetTimeSeriesSidecar", func() {
    Description("Sidecar for GetTimeSeries tool (UI artifact).")
    Field(1, "artifact", AtlasGetTimeSeriesToolReturn,
        "Full tool result used as a sidecar artifact for UI rendering; not sent to the model.")
    Required("artifact")
})
```

## Generated helpers

Per-toolset specs packages (`gen/<svc>/tools/<toolset>`) now include:

- A `<GoName>Sidecar` alias type when `Sidecar(...)` is declared
- JSON schema and codec for the sidecar type
- Convenience helpers:

```go
// Generic codec lookup
func SidecarCodec(name string) (*tools.JSONCodec[any], bool)

// Per-tool helpers (example: tool "get_time_series")
func GetGetTimeSeriesSidecar(res *planner.ToolResult) (*GetTimeSeriesSidecar, error)
func SetGetTimeSeriesSidecar(res *planner.ToolResult, sc *GetTimeSeriesSidecar) error
```

`Set*Sidecar` encodes the typed struct to JSON, decodes into a `map[string]any`,
and merges it into `planner.ToolResult.Sidecar`. Existing keys are preserved;
fields from the typed sidecar overwrite same-named keys.

These helpers are optional: runtimes remain backward compatible with existing
`map[string]any` metadata so long as the JSON produced matches the declared
schema.

## Aura sidecar artifacts

Aura uses typed sidecars primarily for **sidecar artifacts**:

- Model-facing payload/result stay bounded and minimal.
- UI-facing artifacts (charts, topology, timelines) ride in sidecars as
  typed objects whose schemas are exported via `tool_schemas.json`.
- Frontend code (`generate-tool-types.ts`) reads `sidecar.schema` alongside
  payload/result and generates TypeScript interfaces like:

  - `AtlasReadAtlasGetTimeSeriesSidecar`
  - `AdaGetTimeSeriesSidecar`

Chat UI renderers then narrow `ToolCall.metadata` to these interfaces based on
tool ID, and read fields such as `artifact` from the sidecar to render charts
and cards.

Over time, additional artifact-producing tools (topology, events, todos, …)
can migrate to this pattern by:

1. Defining a sidecar type in the Goa design.
2. Calling `Sidecar(...)` in the tool DSL.
3. Using `Set*Sidecar` in adapters (or preserving existing JSON shape).
4. Regenerating code and updating the UI to consume the new sidecar type.


