# goa-ai Codegen Refactoring Proposal

## Executive Summary

This document proposes a comprehensive refactoring of goa-ai's agent code generation to:
1. Split large files into smaller, focused modules (<400 lines each)
2. Eliminate string-based type manipulation in favor of Goa's `NameScope` helpers
3. Use `Meta` DSL to reference runtime types instead of generating duplicates
4. Add DSL helpers (e.g., `BoundedResult()`) that tag attributes with canonical Meta keys
5. Remove idiosyncrasies (inline generated structs, inconsistent pointer handling)
6. Follow Goa's established codegen patterns consistently

## Current State Analysis

### File Size Comparison

**Goa's `codegen/service/` (5879 lines total):**
| File | Lines | Concern |
|------|-------|---------|
| `service_data.go` | 2282 | Data structures (largest, but single concern) |
| `convert.go` | 1051 | Type conversion logic |
| `service.go` | 399 | File generation orchestration |
| `interceptors.go` | 264 | Interceptor generation |
| `endpoint.go` | 183 | Endpoint file generation |
| Others | <150 each | Focused concerns |

**goa-ai's `codegen/agent/` (5986 lines total):**
| File | Lines | Concerns Mixed |
|------|-------|----------------|
| `generate.go` | 1571 | Entry point + ALL file generators mixed |
| `specs_builder.go` | 1508 | Types + schemas + validation + JSON + imports + transforms |
| `data.go` | 1387 | Data structures + constructors (acceptable) |
| Others | <220 each | Focused |

**Key insight:** Goa's largest file (`service_data.go`, 2282 lines) is acceptable because it has a **single concern** (data structures). The problem isn't file size per se - it's mixing concerns.

### Key Problems

#### 1. Direct String Manipulation for Types

Current code uses string surgery to handle pointers and package qualification:

```go
// BAD: String-based pointer detection
aliasIsPointer := strings.Contains(defLine, "= *")
ptr := aliasIsPointer || strings.HasPrefix(fullRef, "*")

// BAD: Manual type reference construction
tool.MethodPayloadTypeRef = "*" + sd.PkgName + "." + md.Payload

// BAD: String replacement for alias collision
t.MethodPayloadTypeRef = strings.ReplaceAll(
    t.MethodPayloadTypeRef,
    oldPrefix,
    newPrefix,
)
```

#### 2. Inconsistent NameScope Usage

The codebase inconsistently uses Goa's type resolution helpers:

```go
// Sometimes uses proper scope methods
fullRef = scope.GoFullTypeRef(att, pkg)

// Other times builds strings manually
fullRef = typeName  // Missing pointer semantics
```

#### 3. Generated Inline Struct Fields

Instead of referencing runtime types via Meta, the generator emits inline struct definitions:

```go
// Current: Generates inline struct in specs package
bounds := &struct {
    Returned       int     `json:"returned"`
    Total          *int    `json:"total"`
    Truncated      bool    `json:"truncated"`
    RefinementHint *string `json:"refinement_hint"`
}{}
```

This should reference `agent.Bounds` via Meta (the runtime type already exists in `runtime/agent/bounds.go`).

#### 4. Monolithic File Structure

`specs_builder.go` handles too many concerns:
- `toolSpecsData` aggregation
- `typeData` construction
- Schema generation (`schemaForAttribute`)
- Validation code generation
- JSON decode-body type materialization
- Import collection (`gatherAttributeImports`)
- Transform code generation
- Field description collection

## Goa Codegen Patterns to Adopt

### 1. NameScope for All Type References

Goa uses `NameScope` consistently for deterministic, pointer-correct type references:

```go
// Goa pattern: Always use scope methods
scope.GoTypeName(att)           // Unqualified type name
scope.GoFullTypeName(att, pkg)  // Package-qualified name
scope.GoTypeRef(att)            // Reference with pointer if needed
scope.GoFullTypeRef(att, pkg)   // Full reference with pointer

// Goa pattern: Use GoTypeDef for inline definitions
scope.GoTypeDef(att, ptr, useDefault)
scope.GoTypeDefWithTargetPkg(att, ptr, useDefault, targetPkg)
```

### 2. AttributeContext for Pointer/Package Semantics

Goa encapsulates context in `AttributeContext`:

```go
// Create context with pointer semantics, package, and defaults
ctx := codegen.NewAttributeContext(pointer, reqIgnore, useDefault, pkg, scope)

// Context determines pointer/value semantics automatically
ctx.IsPrimitivePointer(name, att)
ctx.Pkg(att)  // Returns correct package via UserTypeLocation
```

### 3. Meta DSL for External Type References

Goa uses `Meta` to reference types from other packages:

```go
// DSL: Reference external type
var MyType = Type("MyType", func() {
    Meta("struct:pkg:path", "goa.design/goa-ai/runtime/agent")
    Meta("struct:type:name", "ResultBounds")
    // Fields inherit from external type
})

// Codegen: UserTypeLocation extracts package info
loc := codegen.UserTypeLocation(ut)
pkg := loc.PackageName()      // "agent"
path := loc.RelImportPath     // "goa.design/goa-ai/runtime/agent"
```

### 4. GoTransform for Type Conversions

Goa generates conversion code via `GoTransform`:

```go
// Clean transform generation
code, helpers, err := codegen.GoTransform(
    srcAttr, tgtAttr,      // Source and target attributes
    "in", "out",           // Variable names
    srcCtx, tgtCtx,        // Attribute contexts
    "transform",           // Helper prefix
    true,                  // New variable (:= vs =)
)
```

### 5. File Organization Pattern

Goa's `codegen/service/` demonstrates clean separation:

```
service/
├── service_data.go    # Data structures only
├── service.go         # Service file generation
├── endpoint.go        # Endpoint file generation
├── client.go          # Client file generation
├── views.go           # View file generation
├── convert.go         # Conversion file generation
├── interceptors.go    # Interceptor file generation
└── templates/         # All templates
```

## Proposed Architecture

### Guiding Principle: Split by Concern, Not by Size

Goa's approach: `service_data.go` is 2282 lines but acceptable because it has **one concern** (data structures). The file generators (`service.go`, `endpoint.go`, `client.go`) are smaller because each handles one output file type.

Apply this to goa-ai:
- `data.go` (1387 lines) - **Keep as-is**, it's data structures
- `generate.go` (1571 lines) - **Split by output file type**
- `specs_builder.go` (1508 lines) - **Split by concern** (types vs schema vs codecs)

### Recommended Structure (Flat Files, Clear Domains)

```
codegen/agent/
├── data.go                    # Data structures (keep ~1400 lines, single concern)
├── prepare.go                 # DSL preparation (keep as-is, 168 lines)
├── alias.go                   # Tool aliasing helpers (keep as-is, 65 lines)
├── funcmap.go                 # Template helpers (keep as-is, 144 lines)
├── templates.go               # Template loader (keep as-is, 39 lines)
├── json_tags.go               # JSON tag helpers (keep as-is, 66 lines)
│
├── generate.go                # Entry point ONLY (~100 lines)
│                              # Just orchestrates: calls other generators
│
├── gen_agent.go               # agent.go, config.go generation
├── gen_registry.go            # registry.go generation
├── gen_specs.go               # specs/*/*.go generation (types, codecs, specs)
├── gen_transforms.go          # transforms.go generation
├── gen_executor.go            # service_executor.go, mcp_executor.go
├── gen_bootstrap.go           # internal/agents/* generation
│
├── specs_types.go             # Type materialization via NameScope
├── specs_schema.go            # JSON schema generation
├── specs_codecs.go            # Marshal/unmarshal/validate code
│
└── templates/                 # Templates (keep structure)
```

**Why flat instead of subpackage:**
- No clear domain boundary (specs types are used by generators)
- Avoids import cycles between data.go and specs/
- Easier navigation in IDE
- Goa uses flat files too (no subpackages in `codegen/service/`)

### Core Type: Materializer

Replace ad-hoc type resolution with a structured materializer (aligns with `agent_tool_specs_refactor.md`):

```go
package specs

// Materializer encapsulates Goa NameScope usage, Meta inspection, and 
// import gathering. It provides clean type reference resolution without
// string manipulation.
type Materializer struct {
    scope    *codegen.NameScope
    genpkg   string
    service  *service.Data
    imports  map[string]*codegen.ImportSpec
}

// NewMaterializer creates a materializer for the given service context.
func NewMaterializer(genpkg string, svc *service.Data) *Materializer {
    scope := svc.Scope
    if scope == nil {
        scope = codegen.NewNameScope()
    }
    return &Materializer{
        scope:   scope,
        genpkg:  genpkg,
        service: svc,
        imports: make(map[string]*codegen.ImportSpec),
    }
}

// MaterializeAlias inspects the attribute and returns (def, ref, imports)
// entirely via NameScope - no string surgery. This is the key helper that
// replaces all manual pointer/package string manipulation.
func (m *Materializer) MaterializeAlias(
    typeName string,
    att *expr.AttributeExpr,
    targetPkg string,
) (def string, ref string, imp *codegen.ImportSpec) {
    // Handle external types via Meta
    if loc := codegen.UserTypeLocation(att.Type); loc != nil && loc.RelImportPath != "" {
        pkg := loc.PackageName()
        def = typeName + " = " + m.scope.GoTypeDefWithTargetPkg(att, false, true, pkg)
        ref = typeName
        imp = &codegen.ImportSpec{Name: pkg, Path: joinImportPath(m.genpkg, loc.RelImportPath)}
        return
    }
    
    // Local types: alias to underlying shape
    def = typeName + " = " + m.scope.GoTypeDef(att, false, true)
    ref = typeName
    return
}

// IsPrimitivePointer determines if a primitive field should be a pointer
// based on Goa's semantic rules: required status and default value handling.
// This wraps Goa's AttributeContext.IsPrimitivePointer which encapsulates the logic.
//
// IMPORTANT: Pointer semantics are always about a field WITHIN a parent object.
// You cannot ask "is this type a pointer?" in isolation - you must provide
// the field name and parent attribute.
func (m *Materializer) IsPrimitivePointer(
    fieldName string,
    parentAttr *expr.AttributeExpr,
    useDefault bool,
) bool {
    // Goa's canonical check - handles required, default, and type checks
    return parentAttr.IsPrimitivePointer(fieldName, useDefault)
}

// IsUserType returns true if the attribute is a user-defined type.
// User types are always rendered as pointers by GoTypeRef/GoTypeDef.
func (m *Materializer) IsUserType(att *expr.AttributeExpr) bool {
    _, ok := att.Type.(expr.UserType)
    return ok
}

// IsRawStruct returns true if the attribute is an inline object (not a user type).
// Inline structs are rendered as value types, not pointers.
func (m *Materializer) IsRawStruct(att *expr.AttributeExpr) bool {
    _, isObj := att.Type.(*expr.Object)
    _, isUser := att.Type.(expr.UserType)
    return isObj && !isUser
}

// CollectImports gathers all imports via UserTypeLocation and GetMetaTypeImports.
func (m *Materializer) CollectImports(att *expr.AttributeExpr) []*codegen.ImportSpec {
    uniq := make(map[string]*codegen.ImportSpec)
    _ = codegen.Walk(att, func(a *expr.AttributeExpr) error {
        for _, im := range codegen.GetMetaTypeImports(a) {
            if im.Path != "" {
                uniq[im.Path] = im
            }
        }
        if ut, ok := a.Type.(expr.UserType); ok {
            if loc := codegen.UserTypeLocation(ut); loc != nil && loc.RelImportPath != "" {
                uniq[loc.RelImportPath] = &codegen.ImportSpec{
                    Name: loc.PackageName(),
                    Path: joinImportPath(m.genpkg, loc.RelImportPath),
                }
            }
        }
        return nil
    })
    return sortedImports(uniq)
}
```

`joinImportPath` above simply concatenates the generator package path and the relative import path (for example, `goa.design/goa-ai` + `runtime/agent` → `goa.design/goa-ai/runtime/agent`).

#### Helper Contracts and Internal API

The refactor introduces a small internal “API surface” inside `codegen/agent` to centralize type materialization and transforms:

- **Materializer**
  - Input: `genpkg string`, `svc *service.Data`.
  - Responsibilities:
    - Own the **single** `NameScope` for the service (`svc.Scope`, or a new one if nil) and expose it to callers.
    - Provide `MaterializeAlias(typeName, att, targetPkg)` that returns `(def, ref, imp)` and is the **only** place that decides pointer vs value and package qualification for specs aliases.
    - Provide `CollectImports(att)` used by specs types/schema builders to gather imports via `UserTypeLocation` and `GetMetaTypeImports`.
  - Invariants:
    - All type references for a given service (specs, transforms, executors, bootstrap) use this scope; no other `NameScope` instances are created for **type refs**.

- **BuildToolSpecs**
  - Signature (conceptual):
    - `func BuildToolSpecs(mat *Materializer, tools []*ToolData) (*ToolSpecsData, error)`
  - Responsibilities:
    - Build and return `ToolSpecsData` (existing `toolSpecsData` + `typeData` structures) for a toolset, including:
      - Per-tool alias information and type references.
      - JSON schema and validation metadata.
      - JSON decode-body materialization where needed.
      - Aggregated imports for types, schema, and codecs.
  - Invariants:
    - All callers that previously built specs data inline now go through this single function.

- **TransformBuilder**
  - Input: a `*Materializer` (to reuse its `NameScope` for type refs).
  - Responsibilities:
    - Generate `GoTransform` code between specs types and service method types (payloads and results).
    - Manage a dedicated helper `NameScope` for transform helper function names only (not for types).
    - Collect helper functions at file scope and expose them via `Helpers()`.
  - Invariants:
    - All tool↔service transforms are routed through `TransformBuilder`; there are no ad-hoc `GoTransform` calls elsewhere in `codegen/agent`.

#### NameScope Ownership and Reuse

To keep type references consistent and collision-free:

- There is **exactly one** `NameScope` per service in `codegen/agent` (stored on `service.Data` and surfaced via `Materializer`).
- All type references for that service must use this scope (including specs, transforms, executors, and bootstrap code).
- `TransformBuilder` is allowed its own `helperScope`, but that scope is used **only** for naming helper functions, never for computing type references.

### Runtime Type Reuse Analysis

#### Existing Runtime Types (in `runtime/agent/`)

| Type | Location | Currently Generated? | Reuse via Meta? |
|------|----------|---------------------|-----------------|
| `agent.Bounds` | `bounds.go` | **Yes** - duplicated in specs | **Yes** - use `struct:field:type` |
| `agent.BoundedResult` | `bounds.go` | Interface, generated impl | Keep generating impl |
| `planner.ToolResult` | `planner/planner.go` | No | N/A |
| `runtime.ToolCallMeta` | `runtime/types.go` | No | N/A |
| `runtime.Timing` | `runtime/types.go` | No | N/A |

**Finding:** Only `agent.Bounds` is a candidate for Meta-based reuse. The bounded result pattern already works correctly - codegen generates a result type that implements `agent.BoundedResult` interface. The only duplication is the `Bounds` struct shape itself.

#### Key Meta Tags

Goa supports several Meta keys for type relocation:

| Meta Key | Purpose | Example |
|----------|---------|---------|
| `struct:pkg:path` | Relocate entire type to external package | `"goa.design/goa-ai/runtime/agent"` |
| `struct:type:name` | Use alternate type name | `"Bounds"` |
| `struct:field:type` | Override individual field type | `"agent.Bounds", "goa.design/goa-ai/runtime/agent"` |

#### Simplified Approach: Trust Existing DSL

The existing `BoundedResult()` DSL helper already marks tools as bounded. Rather than a new Meta-based approach, simplify codegen to:

1. **Stop synthesizing bounds struct inline** - just reference `agent.Bounds`
2. **Keep generating result types** that implement `BoundedResult` interface
3. **Use `struct:field:type` Meta** on the bounds field to point at `agent.Bounds`

```go
// In codegen: when building bounded result types, set Meta on bounds field
boundsField.Meta = expr.MetaExpr{
    "struct:field:type": []string{"agent.Bounds", "goa.design/goa-ai/runtime/agent"},
}
```

This eliminates the inline struct duplication while preserving the interface implementation pattern.

### Clean Transform Generation

Centralize transform generation in `specs/transforms.go`:

```go
package specs

// TransformBuilder generates GoTransform code between tool and service types.
// It wraps Goa's GoTransform and manages helper function collection.
type TransformBuilder struct {
    mat         *Materializer
    helperScope *codegen.NameScope
    helpers     map[string]*codegen.TransformFunctionData
}

func NewTransformBuilder(mat *Materializer) *TransformBuilder {
    return &TransformBuilder{
        mat:         mat,
        helperScope: codegen.NewNameScope(),
        helpers:     make(map[string]*codegen.TransformFunctionData),
    }
}

// PayloadTransform generates code to convert tool payload to method payload.
func (t *TransformBuilder) PayloadTransform(
    tool *ToolData,
    specsAlias, svcAlias string,
) (*TransformResult, error) {
    if tool.Args == nil || tool.MethodPayloadAttr == nil {
        return nil, nil
    }
    
    // Verify compatibility using Goa helper
    if err := codegen.IsCompatible(
        tool.Args.Type,
        tool.MethodPayloadAttr.Type,
        "in", "out",
    ); err != nil {
        return nil, nil // Incompatible types, skip transform generation
    }
    
    // Build attribute contexts - let Goa determine pointer semantics
    // CRITICAL: Do NOT manually set pointer=true; Goa's defaults handle it
    srcCtx := codegen.NewAttributeContextForConversion(
        false, false, false, specsAlias, t.mat.scope)
    tgtCtx := codegen.NewAttributeContextForConversion(
        false, false, false, svcAlias, t.mat.scope)
    
    // Generate transform via Goa's canonical helper
    body, helpers, err := codegen.GoTransform(
        tool.Args, tool.MethodPayloadAttr,
        "in", "out", srcCtx, tgtCtx, "", false)
    if err != nil {
        return nil, err
    }
    
    // Collect helpers at file scope (deduped by name)
    for _, h := range helpers {
        t.helpers[h.Name] = h
    }
    
    return &TransformResult{
        Name:   "Init" + codegen.Goify(tool.Name, true) + "MethodPayload",
        Param:  t.mat.scope.GoFullTypeRef(tool.Args, specsAlias),
        Result: t.mat.scope.GoFullTypeRef(tool.MethodPayloadAttr, svcAlias),
        Body:   body,
    }, nil
}

// Helpers returns all collected transform helper functions for file emission.
func (t *TransformBuilder) Helpers() []*codegen.TransformFunctionData {
    result := make([]*codegen.TransformFunctionData, 0, len(t.helpers))
    for _, h := range t.helpers {
        result = append(result, h)
    }
    sort.Slice(result, func(i, j int) bool {
        return result[i].Name < result[j].Name
    })
    return result
}
```

If `codegen.IsCompatible` returns an error, the tool and method types are considered incompatible for automatic transforms and `TransformBuilder` simply skips generating a transform (callers must either ignore transforms for that tool or handle mapping manually). All type references used in the generated transform code must come from the service-level `NameScope` owned by `Materializer`.

### Simplified File Generation

Keep file generation in `generate.go` but have it delegate to `specs.Builder`:

```go
// In generate.go
func agentPerToolsetSpecsFiles(agent *AgentData) ([]*codegen.File, error) {
    var files []*codegen.File
    
    for _, ts := range agent.AllToolsets {
        if len(ts.Tools) == 0 || ts.SpecsDir == "" {
            continue
        }
        
        // Delegate to specs package for data building
        mat := specs.NewMaterializer(agent.GenPkg, agent.Service)
        data, err := specs.BuildToolSpecs(mat, ts.Tools)
        if err != nil {
            return nil, err
        }
        
        // Generate files using data (templates stay in generate.go)
        if typesFile := toolSpecTypesFile(ts, data); typesFile != nil {
            files = append(files, typesFile)
        }
        if codecsFile := toolSpecCodecsFile(ts, data); codecsFile != nil {
            files = append(files, codecsFile)
        }
        if specsFile := toolSpecsFile(ts, data); specsFile != nil {
            files = append(files, specsFile)
        }
    }
    
    return files, nil
}
```

This keeps templates and file orchestration in one place while delegating type/schema/codec logic to the `specs` package.

## Refactoring Tasks

### Track 1: Split `generate.go` by Output File Type

Extract each file generator into its own file (mirrors Goa's `service.go`, `endpoint.go`, `client.go` pattern):

| Current Function | New File | ~Lines |
|-----------------|----------|--------|
| `agentImplFile`, `agentConfigFile` | `gen_agent.go` | ~200 |
| `agentRegistryFile` | `gen_registry.go` | ~150 |
| `agentPerToolsetSpecsFiles`, `agentSpecsAggregatorFile`, `agentSpecsJSONFile` | `gen_specs.go` | ~300 |
| `agentToolsetsTransforms` | `gen_transforms.go` | ~200 |
| `agentServiceToolsetsExecutor`, `mcpServerExecutor` | `gen_executor.go` | ~200 |
| `agentBootstrap`, `agentToolsInternal` | `gen_bootstrap.go` | ~200 |
| Entry point only | `generate.go` | ~100 |

**Approach:** Mechanical extraction, no logic changes. Run `make test` after each extraction.

### Track 2: Split `specs_builder.go` by Concern

| Current Logic | New File | ~Lines |
|--------------|----------|--------|
| `typeData`, `toolEntry`, `toolSpecsData` structs | Keep in `specs_builder.go` or new `specs_data.go` | ~150 |
| `materialize`, type ref logic | `specs_types.go` | ~300 |
| `schemaForAttribute`, JSON schema | `specs_schema.go` | ~200 |
| Validation, `collectUserTypeValidators` | `specs_codecs.go` | ~300 |
| `materializeJSONUserTypes`, JSON decode bodies | Keep in `specs_types.go` | (merged) |
| `gatherAttributeImports` | `specs_types.go` | (merged) |

**Approach:** Extract functions, ensure no string manipulation for pointers/packages.

### Track 3: Eliminate String Manipulation

**Audit targets** (grep for these patterns):
```bash
grep -n 'strings.Contains.*"= \*"' codegen/agent/*.go
grep -n 'strings.HasPrefix.*"\*"' codegen/agent/*.go  
grep -n '"\*" +' codegen/agent/*.go
grep -n 'pkg + "\." +' codegen/agent/*.go
```

**Replace with:**
- `scope.GoTypeRef(att)` / `scope.GoFullTypeRef(att, pkg)` for references
- `scope.GoTypeDef(att, ptr, useDefault)` for definitions
- `parentAttr.IsPrimitivePointer(fieldName, useDefault)` for pointer checks
- `expr.IsObject(att.Type)` / `_, ok := att.Type.(expr.UserType)` for type checks

**Goal:** after Track 3, there should be **no string-based pointer or package manipulation anywhere under `codegen/agent/`**. All type references must go through the service-level `NameScope` (via `Materializer`) and associated helpers (`GoTypeRef`, `GoFullTypeRef`, `GoTypeDef`, attribute contexts), rather than manually constructing or inspecting type strings.

### Track 4: Bounds Type Reuse

1. **Modify codegen** to set Meta on bounds field:
   ```go
   boundsAttr.Meta["struct:field:type"] = []string{"agent.Bounds", "goa.design/goa-ai/runtime/agent"}
   ```

2. **Remove inline bounds struct synthesis** - let Goa's type system handle it via Meta

3. **Update existing bounded result construction** in `specs_builder.go` (the code that currently synthesizes the anonymous bounds struct) to:
   - Drop the inline `struct { Returned int ... }` definition entirely.
   - Rely on the `struct:field:type` Meta to reference `agent.Bounds` instead.

4. **Verify** generated result types still implement `agent.BoundedResult` for at least one bounded tool via golden tests and by compiling example consumers (for example, AURA).

### Execution Order

```
Week 1: Track 1 (split generate.go) - low risk, mechanical
Week 2: Track 2 (split specs_builder.go) - medium risk
Week 3: Track 3 (eliminate strings) - interwoven with Track 2
Week 4: Track 4 (bounds reuse) + polish
```

Run AURA integration after each track to catch semantic regressions.

Track-to-file mapping:

- **Track 1:** Touches `generate.go` and introduces the `gen_*.go` files (agent, registry, specs, transforms, executor, bootstrap); behavior should be identical and goldens should not change.
- **Track 2 & 3:** Primarily affect `specs_builder.go` and the new `specs_*.go` files (types, schema, codecs), plus any call sites that used to build specs data inline.
- **Track 4:** Modifies the bounded result builder in `specs_builder.go` / `specs_types.go` to reuse `agent.Bounds` via Meta.

## Out of Scope

This refactor intentionally **does not**:

- Change the public DSL surface (for example, `BoundedResult()` semantics remain identical).
- Introduce new runtime behaviors beyond reusing `agent.Bounds` via Meta for bounded result fields.
- Restructure or rename runtime packages (`runtime/agent`, `runtime/types`, etc.).
- Change the logical contents of generated `tool_specs.json` beyond type-reference cleanups (pointer/value semantics and JSON shapes should remain equivalent).
- Eliminate string-based type manipulation outside of `codegen/agent` (other Goa codegen packages are out of scope for this document).

## Benefits

### Maintainability
- Smaller, focused files (<400 lines each)
- Clear separation of concerns
- Easier to understand and modify

### Correctness
- Goa's `NameScope` handles all edge cases
- Pointer semantics derived from **field-in-parent context**:
  - Use `parentAttr.IsPrimitivePointer(fieldName, useDefault)` for primitives
  - User types are always pointers (via `GoTypeRef`)
  - Inline objects are value types
  - Never inspect generated strings for `*` prefix
- Package qualification via `UserTypeLocation`

### Consistency
- All type refs use same code path
- Matches Goa's own codegen patterns
- Easier to reason about generated code

### Extensibility
- Adding new file types is straightforward
- Type building is reusable
- Transform generation is centralized

## Testing Strategy

**Golden tests will change** - that's expected. The testing protocol per track is:

1. Run `make test` to regenerate goldens and inspect diffs under `codegen/agent/testdata` (and any other affected golden directories).
2. Manually review golden diffs to confirm:
   - No string-constructed pointer or package prefixes remain under `codegen/agent`.
   - Type references have moved to NameScope-based helpers as intended.
   - Bounded result types reuse `agent.Bounds` via Meta where applicable.
3. Once satisfied, **update golden files in the same commit** as the generator changes.
4. Run `make test` again to ensure all tests pass with updated goldens.
5. For **Track 1 (split generate.go)** specifically, generated output should be identical; any golden diff indicates a bug in the mechanical extraction.
6. For **Tracks 2–4 (specs refactor)**, generated code is expected to change (cleaner types, Meta-based bounds) but must remain semantically equivalent for consumers.

Integration validation:

- After each track, re-generate AURA agents with the development version of goa-ai (for example, via `./scripts/gen goa` in AURA) and run `make test` in AURA.
- If AURA’s agent services compile and their tests pass, the refactor is considered semantically correct from a consumer’s perspective.

Unit tests:

- Add focused tests for new helpers (`specs_types.go`, `specs_schema.go`, `specs_codecs.go`, `TransformBuilder`, etc.) that verify NameScope usage and Meta-based imports without requiring a full Goa eval.

## Open Questions and Notes

1. **Golden tests vs integration tests?**
   - Golden tests are the primary check for generator output shape; they are intentionally updated **incrementally per track** (see Testing Strategy).
   - Integration tests in downstream consumers (such as AURA) remain the semantic validation layer.

2. **Bounds type reuse scope?**
   - Only `agent.Bounds` identified as reusable
   - Should we look harder for other duplications, or is this the only one?

3. **Order of attack?**
   - Option A: `generate.go` first (split by file type, easier)
   - Option B: `specs_builder.go` first (higher impact, harder)
   - **Recommendation:** Do both in parallel as planned

## Conclusion

This refactoring will improve the goa-ai codegen codebase by:
- **Splitting by concern**, not arbitrary size limits (following Goa's pattern)
- **Eliminating string-based type manipulation** in favor of NameScope helpers
- **Reusing `agent.Bounds`** via Meta instead of generating inline structs
- **Flat file structure** (no subpackages) - simpler, matches Goa's approach

**Key principles:**
1. Pointer semantics are about **field-in-parent** context, never isolated types
2. Use `parentAttr.IsPrimitivePointer(fieldName, useDefault)` for primitives
3. User types are always pointers; inline objects are always values
4. Never inspect generated strings for `*` or package prefixes
5. Let `NameScope.GoTypeRef/GoTypeDef` handle all rendering
6. One file = one concern (data structures OK to be large if single-purpose)

**Expected outcome:**
- `generate.go`: ~100 lines (entry point only)
- `gen_*.go`: 6 files, ~150-300 lines each
- `specs_*.go`: 3 files, ~200-300 lines each
- `data.go`: ~1400 lines (unchanged, single concern)


