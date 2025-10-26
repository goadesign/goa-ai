# Repository Guidelines

## Project Structure & Modules
- `codegen/`: MCP code generators and templates.
- `dsl/` and `expr/`: DSL surfaces and expressions used during Goa eval.
- `example/`: Minimal service, generated `gen/` tree, and runnable `cmd/assistant`.
- `integration_tests/`: MCP end‑to‑end tests (YAML scenarios + runner).

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
  `if` blocks; target ~80 columns where practical. For long struct/composite
  literals, one field per line with trailing commas; closing brace on its own
  line. In general content between curly braces must be on multiple lines.
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
- Testing: Write table‑driven tests in `*_test.go` using `testing` (optional
  `testify`). Name tests `TestXxx`; keep unit tests fast/deterministic. Run `go
  test -race -vet=off ./...` (or `make test`) locally and avoid coverage
  regressions.

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

#### Avoid Defensive Programming
- Configuration should be passed via constructors; do not read environment variables in core logic.

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

Validation rules:
- Every tool must be configured via text or template
- A tool cannot be configured with both text and template
- Templates are compiled with missingkey=error; use `runtime.ValidateAgentToolTemplates` for early checks
