## Agents codegen golden tests

This directory hosts the golden tests for the agents Goa code generator. The goals are:

- Verify the generator emits the right Go code for small, focused DSL designs
- Keep scenarios minimal and deterministic; assert only stable, meaningful output
- Provide a clear path to add scenarios and refresh goldens when intent changes

### Layout

- `tests/` (this package): test helpers and golden tests
  - `golden_helpers_test.go`: shared helpers to execute DSLs, run generation, render sections to final code, and compare to goldens
  - `golden_*.go`: scenario-specific tests
- `tests/testscenarios/`: small, single‑purpose DSL design functions
  - One exported function per scenario returning `func()` DSL
- `tests/testdata/golden/<scenario>/`: expected generated sources
  - File names mirror generator outputs (e.g. `types.go.golden`, `specs.go.golden`, `codecs.go.golden`)

### How the helpers work

- DSL execution: `eval.Reset()` → register Goa and agents roots → `eval.Execute(design)` → `eval.RunDSL()`
- Code generation: call `codegen/agent.Generate` to obtain `[]*codegen.File`
- Rendering: every `SectionTemplate` is executed with `text/template` using its `FuncMap` and `Data`
  - Default helpers for shared headers are injected for determinism:
    - `comment`: Goa codegen comment helper
    - `commandLine`: returns an empty string to avoid embedding environment‐dependent commands
- Comparison: use `testutil.AssertGo` (Go) or `testutil.AssertString` (text/markdown) to compare to goldens
  - Go code is formatted; generated headers are version‐normalized

### Running and updating

- Run all:

```bash
go test ./codegen/agent/tests
```

- Update a specific scenario (example: quickstart header):

```bash
go test ./codegen/agent/tests -run Quickstart_Renders_Minimal -update
```

### Test matrix (initial)

- Tool specs – minimal
  - DSL: `testscenarios.ToolSpecsMinimal()`
  - Verifies: `tool_specs/types.go`, `tool_specs/codecs.go`, `tool_specs/specs.go`
  - Focus: basic payload/result structs, codecs, schemas, and spec registry

- Transforms – method-backed tools
  - DSL: `testscenarios.MethodSimpleCompatible()`
  - Verifies: `gen/<service>/agents/<agent>/<toolset>/transforms.go` contains transform helpers
  - Focus: presence of transform init helpers and header markers

Planned additions (recommended next):

- Tool args/return variants (primitive, inline object, user type w/ customization)
- Tags surfaced in specs
- BindTo cross‑service (imports/aliases, type refs)
- Deterministic user type imports (custom packages, alias stability)
- RunPolicy (caps, time budget, interrupts) emitted into agent config/registry
- Toolset reuse (top‑level `Toolset(...)` referenced in `Uses`) – no duplicate specs
- MCP MCPToolset/Use (external toolset registration) – minimal compile‑time scaffolding

### Authoring conventions

- Keep each scenario laser‑focused; one concept per test file
- Goldens should cover only the files directly affected by the concept
- Prefer small DSLs over elaborate examples; readability and stability first
- Name tests `TestGolden_<Concept>` and scenarios `<Concept>()`
- Goldens live under a folder named after the concept, mirroring generator output paths

### Hygiene and determinism

- Never assert on inherently dynamic values (timestamps, absolute paths, commands)
- Keep helper‑injected header functions deterministic (empty command line by default)
- When templates or intents change, update goldens and include a rationale in the PR

### Troubleshooting

- Golden mismatches
  - Use `-update` to refresh; review diffs carefully to confirm intent
  - If a delta looks wrong, prefer fixing the generator/template rather than the test

- Rendering errors
  - Ensure scenario DSL compiles (required fields, minimal API/service scaffolding)
  - If a header helper is missing (e.g., `commandLine`), add it to the helper FuncMap

### CI notes

- `./codegen/agent/tests` can be run independently from unit tests elsewhere
- Golden failures should be actionable by reviewing the generated vs. expected code
