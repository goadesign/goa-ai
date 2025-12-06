# Repository Guidelines

## Project Structure & Modules
- `codegen/`: MCP code generators and templates.
- `dsl/` and `expr/`: DSL surfaces and expressions used during Goa eval.
- `example/`: Minimal service, generated `gen/` tree, and runnable `cmd/assistant`.
- `integration_tests/`: MCP end‑to‑end tests (YAML scenarios + runner).

## Streaming Planners (goa-ai runtime)

- Streaming planners MUST choose exactly one path for emitting
  `planner.PlannerEvents`:
  - Use the **runtime-decorated client** obtained from
    `PlannerContext.ModelClient(id)` and drain the `Streamer` yourself, or
  - Use `planner.ConsumeStream` with a **raw** `model.Client` that is not
    wrapped by the runtime.
- When you call `input.Agent.ModelClient(id)`, the runtime returns a client
  wrapped with an event decorator. That decorator emits `AssistantChunk`,
  `PlannerThinkingBlock`, and `UsageDelta` each time you call `Recv()` on the
  `Streamer`. In this mode:
  - Do **not** call `planner.ConsumeStream` on the resulting `Streamer`.
  - Drain the stream in the planner using a `for` / `Recv()` loop and build the
    final text and tool-call list locally.
- If you need `planner.ConsumeStream`, obtain a raw `model.Client` (without the
  event wrapper) and pass its `Streamer` to `ConsumeStream`. Mixing the
  decorated client with `ConsumeStream` is a bug and will double-emit all
  thinking/text events.


## Build, Test, and Development
- Build library: `make build`
- Lint: `make lint` (config: `.golangci.yml`)
- Unit tests: `make test`
- Integration tests: `make itest`
- Run example server: `make run-example`

## Change Workflow

- Standard:
  1. Make changes
  2. Run lint: `make lint`
  3. Fix errors
  4. Run tests: `make test` (or `make itest`)
- Goa design changes:
  1. Edit `design/*.go`
  2. Regenerate code (`goa gen ...`) — verify `gen/` updated
  3. Lint and test
- Never manually edit `gen/` — always regenerate.

## Testing Guidelines
- Frameworks: standard `testing`, `testify/require` in integration tests.
- Place new e2e scenarios under `integration_tests/scenarios/*.yaml` and wire in `tests/`.
- Aim for coverage on: tools, resources, prompts, initialization, and errors.
- Useful env vars: `TEST_FILTER`, `TEST_DEBUG`, `TEST_KEEP_GENERATED`, `TEST_SERVER_URL` (see `integration_tests/README.md`).

### Unit Test Assertions
- Prefer `t.Errorf` over `t.Fatalf` so the test reports multiple failures where
  safe. Reserve `t.Fatalf` for cases where continuing would panic or
  meaningfully corrupt subsequent assertions (e.g., required setup failed and
  state is unusable).

## Commit & Pull Requests
- Commits: imperative and scoped (e.g., “Refactor adapter stream bridge”, “Fix tools/list schema”).
- PRs must include: summary, rationale, linked issues, and test evidence (unit or integration). Add before/after notes for generator output when relevant.
- Keep diffs minimal; update docs (`README.md`, `DESIGN.md`) when behavior changes.

## Coding Guidelines
- Style & naming: Go 1.24+, format with `go fmt ./...`; keep imports grouped
  (stdlib separate). Files use `lower_snake_case.go`; packages are short,
  lowercase. Exported identifiers need GoDoc; avoid stutter. Wrap errors with `%w`
  and use `errors.Is/As`.
- File organization: Order declarations as Types → Consts → Vars → Public funcs
  → Public methods → Private funcs → Private methods. No commented‑out code;
  delete dead code.
- Additional style: Prefer `any` over `interface{}` in new code. Use multi‑line
`if` blocks. For long struct/composite literals, one field per line with
trailing commas; closing brace on its own line. In general content between curly
braces must be on multiple lines.  Curly braces style: always place a newline
immediately after `{` and before `}` for blocks and composite literals (no
single‑line blocks or literals), including empty blocks. Example:
  
      // Good
      type T struct {
          A int
      }
      v := T{
          A: 1,
          B: 2,
          C: "long value that is a long text",
      }
      w := U{ A: 1 } // short literal
      if cond {
          do()
      }
      
      // Avoid
      type T struct { A int }
      v := T{ A: 1, B: 2, C: "long value that is a long text"}
      w := U{ 
        A: 1
      }
      if cond { do() }
- Documentation: Every exported type, function, and struct field must have a Go
  stdlib GoDoc-quality comment that explains its contract to someone with no prior
  context. Treat this like stdlib documentation—clarify when/how callers should
  use the API and what each field/config represents.
- Generator edits MUST be section‑driven and guard‑first: check section name
  early and `continue` (`if s.Name != "target" { continue }`), then mutate. Avoid
  redundant `s.Source == ""` checks.
- Generator code MUST NOT rely on example‑specific aliases or names. Derive
  aliases (e.g., original client alias) from header imports and operate
  generically.
- DSL note: Dot imports are encouraged in DSL packages; see `.golangci.yml`
  which disables `ST1001`.
- Schema access: do not introspect Goa `docs.json` at runtime. Always use the
  generated `tool_specs.Specs` (and their `Payload/Result.Schema` and codecs)
  for schema and encoding needs. Avoid maintaining parallel schema helpers.
- Testing: Write table‑driven tests in `*_test.go` using `testing` (optional
  `testify`). Name tests `TestXxx`; keep unit tests fast/deterministic. Run `go
  test -race -vet=off ./...` (or `make test`) locally and avoid coverage
  regressions.

## Codegen: Partial Evaluation Principle

Code generation should apply **partial evaluation**: if something is known
statically at generation time, evaluate it then rather than generating code
that evaluates it at runtime.

### What to avoid in generated code

- **Runtime loops over static collections**: If you know the items at generation
  time, unroll the loop in the template. Don't generate `for _, item := range items`.
- **Runtime conditionals on static conditions**: If you know the branch at
  generation time, only emit the relevant code. Don't generate `if hasFeature { ... }`.
- **Generic algorithms that inspect structure at runtime**: If the algorithm's
  behavior depends on expression structure, generate the specialized algorithm
  directly.

### Examples

**Bad** — generating a runtime loop when iterations are known:
```go
// Template loops at runtime
for _, skill := range a.skills {
    process(skill)
}
```

**Good** — unroll at generation time:
```go
// Template: {{- range .Skills }}
// Generated: specific code per skill, no loop
process(Skill{ID: "skill1", Name: "Query"})
process(Skill{ID: "skill2", Name: "Analyze"})
```

**Bad** — generating a runtime conditional when branch is known:
```go
// Generated
if a.hasSecuritySchemes {
    buildSecuritySchemes()
}
```

**Good** — only emit code if condition is true:
```go
// Template: {{- if .HasSecuritySchemes }}
// Generated: security code only appears if agent has security schemes
```

### When dynamic code is necessary

For truly dynamic behavior (e.g., request handlers that must dispatch based on
runtime input), prefer:
1. Create a **runtime library** with configurable constructs
2. Generate code that **configures** those constructs with agent-specific data
3. The library handles common logic; generated code provides configuration

This avoids generating N copies of similar code with slight variations.

## Codegen: Handling Types Elegantly (Goa Scopes, Refs, and Locators)

This repository generates a significant amount of Go based on Goa designs. Type
references must be produced by Goa’s codegen APIs — never by manual string
concatenation — to stay correct across composites, aliases, and external package
locators.

Key principles

- Always compute type references using NameScope helpers:
  - `Scope.GoTypeName(att)`, `Scope.GoFullTypeName(att, pkgAlias)`
  - `Scope.GoTypeRef(att)`, `Scope.GoFullTypeRef(att, pkgAlias)`
  - Prefer the “Full” variants when qualifying with a specific package alias.
- Use a single `NameScope` per emitted file to ensure consistent, conflict‑free
  naming across that file.
- Preserve the original `*expr.AttributeExpr` when computing refs so Goa can
  honor `Meta("struct:pkg:path")` locators. Do not fabricate user types unless
  you faithfully copy locator metadata.
- Let Goa decide pointer/value semantics. Do not globally force pointers
  (e.g., `pointer=true`) in `AttributeContextForConversion` except in transports
  where it’s required for validation. For internal transforms, set `pointer=false`
  and rely on default semantics (required/default → pointer or value as needed).
- Build conversion bodies via `codegen.GoTransform(src, dst, "in", "out", srcCtx, dstCtx, …)`;
  don’t post‑process emitted code to change pointer/value or qualification.
- Gather imports from attributes and locators:
  - `codegen.UserTypeLocation(ut)`, `codegen.GetMetaTypeImports(att)`
  - plus explicit service/specs imports where needed.
  - Render with `codegen.Header` so unused imports are pruned automatically.

Internal adapter transforms

- When generating adapter helpers that map tool payloads/results to service
  method payloads/results:
  - Signatures use `Scope.GoFullTypeRef` on the original tool/service attributes
    with the correct package alias (specs vs service); this handles composites
    (arrays/maps) and external locators.
  - Bodies use `GoTransform` with `AttributeContextForConversion(pointer=false)`
    for both source and target contexts to preserve Goa’s default pointer rules.
  - Example (conceptual):
    - Payload helper: `Init<GoName>MethodPayload(in <specsRef>) <serviceRef>`
    - Result helper: `Init<GoName>ToolResult(in <serviceRef>) <specsRef>`

Why this matters

- Meta locators and composite types break ad‑hoc string construction. Using
  Goa’s Scope + locators ensures refs are qualified to the correct package and
  that pointer/value forms match the design’s required/default contracts.
- A single file‑level `NameScope` guarantees deterministic naming and reduces
  collisions.

Common pitfalls to avoid

- Do not concatenate `"*" + pkg + "." + Type` — this fails for arrays/maps and
  nested composites.
- Do not set `pointer=true` in transforms unless you’re in transport/validation
  code; it forces pointers even where defaults/requireds intend value types.
- Do not drop locator metadata when synthesizing user types; refs will be wrong.


### Boundaries & Validation

- Validate only at system boundaries:
  - gRPC/HTTP handlers (Goa performs validation)
  - Event consumers (validate deserialized events)
  - Database query results (check errors and unexpected nulls)
  - Third‑party API responses
  - Context extraction (`ctx.Value()`)
  - Type assertions (always check `ok`)
  - Map lookups when required
- Inside service code, do not re‑validate values already guaranteed by contracts:
  - Function arguments between your own functions
  - Goa‑generated type fields (already validated)
  - Non‑nil pointers guaranteed by constructors
- Style within functions:
  - Prefer early returns over deep nesting; avoid useless locals
  - Use modern Go helpers (e.g., `max`, `min`, `clear`) when appropriate
  - Avoid defensive programming: do not add nil checks, fallback paths, or silent defaults for values whose contracts guarantee validity. Let violations panic or error loudly so bugs surface early.
- DSL and codegen packages MUST rely on Goa’s evaluation guarantees (e.g., `ToolExpr.Toolset` is never nil). Never guard these invariants—if they are violated, fail fast to expose the design issue.

### Repo‑Wide Contracts (No Defensive Guards)

- Validate only at boundaries (above). Inside the codebase, assume invariants hold and avoid speculative guards or fallbacks.
- Do not add nil/empty checks for values guaranteed by construction or prior validation. Examples that are guaranteed in this repo:
  - Generated maps/registries (e.g., `tool_specs.Specs`) have non‑nil entries; `Name`, `Payload.Schema`, and `Result.Schema` exist.
  - Event payloads are non‑nil when their `Type` indicates they are present.
  - Service method type references are valid (resolved via Goa `NameScope`), no string surgery needed.
- Prefer fail‑fast: if a contract is violated, return a precise, structured error (or panic for true invariants) rather than continuing with best‑effort behavior.
- Never synthesize type references by string surgery. Always use Goa `NameScope` helpers (`GoTypeRef/GoFullTypeRef`) to derive pointer/value semantics. If you need a reference to a local generated name, construct a synthetic `expr.UserTypeExpr` with `TypeName` set to the local name and pass it to `GoTypeRef` to compute the correct pointer prefix.

PR review checklist (reject when present in core logic):
- “Should not happen” branches or generic fallbacks; comments like “just in case”, “fallback”.
- Defensive guards on invariant holders (e.g., `if s == nil || s.Name == ""` for spec entries; `len(schema) == 0` after generation).
- String manipulation to build type/package refs; use Goa `NameScope` helpers instead.

Tests policy (complements Testing Guidelines):
- Do not test impossible states from internal invariants (e.g., nil spec entries, empty schemas, missing payloads for a set event type). If such a test seems needed, you are testing the wrong layer—add a boundary test or fix the upstream contract.
- Do test boundaries: malformed JSON at the transport, third‑party failures, DB nulls, context extraction issues, type assertion misses across package boundaries.

Bad → Good examples:
- Bad: `if s == nil || s.Name == "" || len(s.Payload.Schema) == 0 { continue }`
- Good: `for _, s := range tool_specs.Specs { /* use s.Name/Schema directly */ }`

### Template Formatting (Goa codegen templates)

- Keep Go code indentation independent from template directives. Do not shift Go code to align with `{{ ... }}` blocks.
- Indent template directives relative to each other to reflect structure (`if`, `range`, `else`, `end`). Prefer `{{- ... }}` to trim surrounding whitespace when appropriate.
- Example pattern:

  ```
  {{- if condition }}
      {{- if nested }}
  // Go code here (indented for Go readability only)
      {{- end }}
  {{- end }}
  ```

- Apply the same rule in loops and multi-branch templates. Keep closing `{{- end }}` aligned with its opening directive.
- Favor readable, minimal whitespace while preserving valid Go formatting in the emitted code.

### Slice and map length checks

- Do not check for nil before calling `len` on slices or maps. In Go, `len(nilSlice)` and
  `len(nilMap)` both return 0. Prefer `len(x) == 0` over `x == nil || len(x) == 0`.
  This keeps code concise and idiomatic.

### Go Type References in Codegen

When emitting code that references types, rely on Goa’s codegen helpers:

- Use the owning service/specs `NameScope` to compute references.
- Same‑package references: set the attribute context package to `""` and use
  `codegen.NewAttributeContextForConversion(...)` so generated code does not
  qualify with the current package alias.
- External references: use `Scope.GoFullTypeRef(attr, pkg)` to qualify user types
  with the proper package alias derived from `UserTypeLocation` or service scope.
- Prefer `GoTypeRef/GoFullTypeRef` (attribute‑aware) over string concatenation.
  Only use a local type name directly when referring to a generated alias within
  the same package (e.g., `*ByIDPayload`).

For transforms helpers (specs/<toolset>/transforms.go):

- Param/result types on function signatures must reference the local alias types
  (e.g., `*ByIDPayload`, `*ByIDResult`).
- When converting to/from service method types, compute `*svc.Payload` and
  `*svc.Result` via `GoFullTypeRef` and the service NameScope.
- To initialize the local alias in the body, synthesize a temporary
  `expr.UserTypeExpr` with `TypeName` set to the local alias and the underlying
  attribute set to the tool’s attribute. Pass that as the transform target along
  with a conversion context whose package is `""` so struct literals render as
  `&ByIDResult{...}` instead of a qualified service type.

### Goa Design Authoring

- Every `Field` MUST include an inline description string (4th argument). Example: `Field(2, "name", String, "Name of tool")`.
- Provide examples and validations inside the field DSL func when applicable:
  - Use `Example(...)` for representative values.
  - Use `Minimum/Maximum`, `MinLength/MaxLength`, `Enum`, and `Format` as appropriate.
- Prefer `SharedType` for cross-service types; keep descriptions self-contained.
- Avoid documentation in `//` comments for fields or types; use DSL `Description("...")` and field descriptions instead.
- Ensure requiredness via `Required(...)` and avoid redundant runtime nil/empty guards in code — rely on Goa validations.

### Goa Critical Rules

- Required arrays must contain at least one element; empty slices serialize to `null` and fail Goa validation. If empty is valid, make the field optional.
- OneOf/union types must have exactly one variant set — never send `nil` for unions.
- Define all validation in the Goa design. Service code trusts validation.
- Return typed or structured errors at boundaries; wrap with `%w`.

### Error Handling (Always check for errors)

- Always check and handle errors returned by functions. Do not ignore `err` variables.
- NEVER ignore errors or discard them with the blank identifier. Do not write patterns like `_ = call()`. Either handle (log and continue with intent) or return the error explicitly.
- When closing or cleaning up resources (streams, connections), check returned errors and log them.

### Contract validation and redundant checks

- Goa design validations enforce required fields and non-nil payloads at the edges. Do not add redundant nil/empty guards inside service methods for values that are guaranteed by Goa (e.g., required payload pointers, required fields). Rely on the contract and let violations surface loudly to fix producers.
- Examples:
  - Do not guard required payloads with `if p == nil { return }` in hot paths; construction and Goa validations guarantee non-nil by contract.
  - Do not sprinkle optional fallbacks for required IDs (session_id, alarm_id); construction must supply them and services should fail fast otherwise.

### Contract-Driven Code (No "loose" defensive paths)

- Favor strong, explicit contracts over permissive fallbacks.
- Do not add speculative "fishing" logic or broad back-compat scans when a clear type or field is available.
- Prefer fail-fast validation and precise, structured errors over silent recovery.
- Avoid optional/nullable fields unless they are genuinely optional

#### No Fallback Coercion in Runtime/Codegen

- Do not perform best-effort coercions when types do not match expected contracts.
- Generated code must prefer strong typing and fail fast rather than silently
  remapping payloads or results (e.g., avoid JSON round-trips to "fix" types
  unless the contract explicitly defines that mapping).
- If a tool payload/result type assertion fails, return a clear, structured error
  instead of attempting fallback conversions.

#### No "Should-Not-Happen" Fallbacks

- Do not add catch‑all or “should not happen” fallbacks (e.g., silent defaults
  when a method or type lookup fails). We rely on Goa’s guarantees; violations
  are bugs and must fail fast.
- Prefer explicit checks that panic or return an error with a precise message
  rather than continuing with best‑effort behavior.
- Never synthesize type references by string surgery. Use Goa’s `NameScope`
  helpers (`GoFullTypeRef/Name`, `GoTypeRef/Name`) and `UserTypeLocation` to
  produce deterministic, pointer‑correct references.

#### Avoid Defensive Programming
- Configuration should be passed via constructors; do not read environment variables in core logic.
 - Do not guard against states that Goa guarantees cannot occur. For example,
   `expr.Root.Services` and `Service.Methods` do not contain nil entries;
   avoid `nil` checks when iterating them. Prefer fail-fast code that relies on
   Goa invariants—unexpected states are bugs and must surface loudly.

### Function Signature Formatting

- Keep function signatures on a single line when they fit within ~80 columns.
- Do NOT split parameters across multiple lines with trailing commas like:
  ```go
  // Bad
  func (b *Builder) GetOrCreateType(
      name string,
      builder func() *expr.AttributeExpr,
  ) *expr.UserTypeExpr {
  ```
- Instead, keep parameters on one line:
  ```go
  // Good
  func (b *Builder) GetOrCreateType(name string, builder func() *expr.AttributeExpr) *expr.UserTypeExpr {
  ```
- Only split to multiple lines when the signature genuinely exceeds ~100 columns and cannot be reasonably shortened.

### Files and Style Clarifications

- Target ~80 columns where practical.
- Files should be ≤ 2000 lines; split proactively when adding code would exceed this.
- Strings: use exact comparison; only use `strings.EqualFold` when the external contract is case‑insensitive.

## Safety & Permissions

| Action              | Policy                         |
|---------------------|--------------------------------|
| `git clean/stash`   | FORBIDDEN (risk of data loss)  |
| `git checkout`      | FORBIDDEN                      |
| `git push`          | Explain intent, then proceed   |
| Changes (≥3 files)  | Describe plan, then proceed    |
| Install dependencies| Explain, then proceed          | 
| Delete files        | Explain, then proceed          |
| Everything else     | Allowed                        |

- Never run `go clean -cache` during normal development (expensive rebuilds).

- Do not add nil/empty guards for values guaranteed by Goa or by construction. If a required field is missing, let it fail loudly at the boundary.
- Prefer immediate, unmistakable failures over subtle behavior later. If a contract is violated, blow up early so the producer can be fixed.
- Do not include conditional caps or config handling like `if in.Caps != nil { ... }` when the design requires them; set them directly and rely on validations.
- Do not add defensive guards that “paper over” invariant violations (e.g., treating nil events as normal or silently skipping unexpected states).
- In streaming/IPC paths, if the producer/client contract says a value is non-nil, do not guard it with nil checks; allow panics to surface violations so the producer/client can be fixed.

### Agent-as-Tool registration (options API)

Register exported agent-tools using the generated helper and runtime-owned options:

```go
reg, err := agenttools.NewRegistration(
    rt,
    "You are a data expert.",
    agenttools.WithText(agenttools.ToolQueryData, "Query: {{ . }}"),
    agenttools.WithTemplate(agenttools.ToolAnalyzeData, compiledAnalyzeTmpl),
)
if err != nil { log.Fatal(err) }
if err := rt.RegisterToolset(reg); err != nil { log.Fatal(err) }
```

Apply the same content across tools:

```go
reg, _ := agenttools.NewRegistration(rt, "",
    agenttools.WithTextAll(agenttools.ToolIDs, "Handle: {{ . }}"),
)
```

Content rules:
- You may configure per-tool text or templates. If neither is provided, the runtime builds a sensible default: optional SystemPrompt as a system message, followed by a user message constructed from the tool payload (string payloads are used as-is; structured payloads are JSON-encoded). You can override the default builder by supplying `WithPromptBuilder`.
- Do not set both text and template for the same tool.
- Templates are compiled with missingkey=error; use `runtime.ValidateAgentToolTemplates` for early checks (validation applies only to the templates you provide).

### Agent-as-Tool

- Use child workflows by default. Codegen emits Route and Inline=true; runtime
  executes via `ExecuteAgentChildWithRoute` inside the workflow loop.
- Do not schedule ExecuteTool activities for agent-as-tool. Service toolsets
  continue to run as activities with ActivityOptions.
- Provider agents must run a worker on their workflow queue. Consumers only need
  to `RegisterToolset(reg)`. See architecture docs for rationale and flows.

#### Child runs, run links, and stream profiles

- Agent-as-tool always executes in a real child run. When a nested agent is
  invoked, the runtime:
  - creates a new workflow run with its own `RunID` and `run.Handle`,
  - emits an `AgentRunStarted` hook/stream event in the parent run that carries
    a `run.Handle` link to the child (run ID, agent ID, parent run/tool IDs),
  - sets `ToolResult.RunLink` on the parent tool result so finalizers and
    planners can correlate structured results with the child run.
- The runtime no longer flattens or suppresses child tool events globally.
  Visibility is controlled by `stream.StreamProfile`, which selects event
  kinds per audience (chat vs debug vs metrics). All profiles operate on
  per‑run streams linked via `AgentRunStarted` events; if you need a flattened,
  firehose‑style projection for debugging, build it as a separate subscriber
  on top of the per‑run model rather than changing the core runtime behavior.
