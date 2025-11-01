# AU-3 Status Assessment: Build Provider Config from Specs

## Current Status: **Partially Complete** (~60%)

### What's Working ✅

1. **Using `tool_specs.Specs` as source**
   - `services/chat-agent/prompts/builder.go`: `toolList()` reads from `adaspec.Specs` and `chatspec.Specs`
   - `services/atlas-data-agent/service.go`: `buildADAToolSet()` reads from `adaspec.Specs`
   - Both use `s.Payload.Schema` directly (no manual JSON schema construction)

2. **Schema extraction**
   - Reading `s.Name`, `s.Description`, `s.Payload.Schema` from generated specs
   - No `docs.json` parsing or manual schema definitions

### What's Missing ❌

1. **Wrong target type**
   - Currently converting to `gentypes.BRTool` (AURA's legacy type)
   - Should convert to `model.ToolDefinition` (goa-ai runtime type)
   - `BRTool` is a wrapper around Bedrock-specific types, not the provider-agnostic abstraction

2. **No `config_from_specs.go` helpers**
   - Task calls for `per-agent tools/config_from_specs.go` files
   - Current code has manual conversion logic scattered in prompts/service files
   - Should have centralized helpers that convert `tool_specs.Specs` → `model.ToolDefinition[]`

3. **Not using goa-ai model abstraction**
   - Still using `services/inference-engine` (AURA's own abstraction)
   - Not using `goa.design/goa-ai/runtime/agent/model` Client interface
   - This is a blocker for full migration but may be acceptable for incremental progress

### Current Code Locations

**Services using tool_specs to build configs:**
- `services/chat-agent/prompts/builder.go`: `toolList()` → `[]*gentypes.BRTool`
- `services/atlas-data-agent/service.go`: `buildADAToolSet()` → `ToolSet` (wrapper around `[]*gentypes.BRTool`)

**Pattern used:**
```go
for _, s := range tool_specs.Specs {
    tool := &gentypes.BRTool{
        Name:        s.Name,
        Description: s.Description,
        InputSchema: gentypes.JSON(string(s.Payload.Schema)),
    }
    // ... add to list
}
```

### Target Pattern

Should be:
```go
// services/chat-agent/tools/config_from_specs.go
func ToolDefinitionsFromSpecs(specs []tools.ToolSpec) []model.ToolDefinition {
    defs := make([]model.ToolDefinition, 0, len(specs))
    for _, s := range specs {
        var schema any
        if len(s.Payload.Schema) > 0 {
            if err := json.Unmarshal(s.Payload.Schema, &schema); err != nil {
                // fallback to empty object
                schema = map[string]any{"type": "object"}
            }
        } else {
            schema = map[string]any{"type": "object"}
        }
        defs = append(defs, model.ToolDefinition{
            Name:        s.Name,
            Description: s.Description,
            InputSchema: schema,
        })
    }
    return defs
}
```

### Remaining Work

1. **Create helper functions** (per agent)
   - `services/chat-agent/tools/config_from_specs.go`
   - `services/atlas-data-agent/tools/config_from_specs.go`
   - `services/knowledge-agent/tools/config_from_specs.go` (if needed)
   - `services/diagnostics-agent/tools/config_from_specs.go` (if needed)

2. **Replace manual BRTool construction**
   - Update `services/chat-agent/prompts/builder.go` to use helper
   - Update `services/atlas-data-agent/service.go` to use helper
   - Convert `BRTool` → `model.ToolDefinition` where planners call models

3. **Wire into planners** (when planners migrate to goa-ai model.Client)
   - Planners should use `model.ToolDefinition[]` in `model.Request.Tools`
   - Remove `BRTool` and `ToolSet` wrappers

4. **Add unit tests**
   - Test `ToolDefinitionsFromSpecs` with various tool specs
   - Verify schema unmarshaling handles edge cases
   - Verify empty schemas default to `{"type": "object"}`

### Blockers/Considerations

- **Inference engine abstraction**: AURA still uses `services/inference-engine` instead of goa-ai `model.Client`. This may be intentional for incremental migration, but planners should eventually use goa-ai's model abstraction.
- **BRTool usage**: 84 references to `BRTool`/`ToolSet` remain. These need to be migrated to `model.ToolDefinition` or at least converted at the boundary.

### Verification Checklist

- [ ] Helper functions exist in `per-agent tools/config_from_specs.go`
- [ ] Helpers convert `tool_specs.Specs` → `model.ToolDefinition[]`
- [ ] No manual JSON schema construction (all from `s.Payload.Schema`)
- [ ] Unit tests cover schema unmarshaling edge cases
- [ ] `toolList()` and `buildADAToolSet()` use helpers instead of manual construction
- [ ] (Optional) Planners use `model.ToolDefinition` when calling models

### Recommendation

**Status**: **Partially Complete** - Good foundation but needs:
1. Centralized helper functions
2. Switch from `BRTool` to `model.ToolDefinition`
3. Unit tests

Priority: High (blocks full migration to goa-ai model abstraction)

