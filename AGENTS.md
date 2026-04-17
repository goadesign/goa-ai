You are an agentic systems engineer. Optimize for elegance, strong contracts, conceptual correctness, and less code. Prefer deleting bad abstractions to preserving them. Do not add fallbacks, coercions, or defensive code that hides bugs.

# Repository Guidelines

## Core Operating Rules

- Plan before acting: for <=2 files, state a brief plan then implement; for >=3 files, write a step-by-step plan first.
- Read before editing. Search instead of guessing.
- Fix root causes, not local workarounds.
- Prefer the simplest design that satisfies the contract. Reduce surface area, delete dead code, and avoid new concepts unless they clearly pay for themselves.
- Be concise in progress updates and summaries.
- Keep one canonical implementation and one source of truth per concept. Delete unused code and commented-out code.
- Validate only at boundaries: HTTP/gRPC handlers, event consumers, DB results, third-party APIs, `ctx.Value()`, type assertions, and required map lookups. Inside the codebase, trust Goa and construction-time invariants.
- Fail fast on invariant violations. Do not add nil/empty guards, fallback behavior, back-compat fishing logic, or "should not happen" branches for values guaranteed by contracts.
- Do not perform best-effort coercions in runtime/codegen. If a payload, result, or type assertion does not match the contract, return a precise error instead of silently remapping it.
- Configuration belongs in constructors, not environment-variable reads in core logic.
- Keep docs in sync with behavior:
  - User-facing `goa-ai` DSL, runtime, or codegen changes must update `content/en/docs/2-goa-ai/` and translated pages when applicable.
  - Update `README.md` and `DESIGN.md` when behavior changes.

## Language And Code Rules

### Go style

- Go 1.24+ with `go fmt ./...`.
- Group imports with stdlib separate from external.
- Use `lower_snake_case.go`; split large files proactively and prefer <=1000 lines.
- Use short lowercase package names. Exported identifiers and exported struct fields need GoDoc.
- Prefer `any` over `interface{}`.
- Always check errors. Wrap with `%w`, use `errors.Is/As`, and never ignore errors or write `_ = call()`.
- Keep signatures on one line when they fit within about 100 columns.
- Use `len(x) == 0` for slices/maps; do not check nil before `len`.
- Use multi-line blocks. Short literals may stay inline; long literals should use one field per line with trailing commas.
- Prefer exact string comparison; use `strings.EqualFold` only when the external contract is case-insensitive.
- Use modern Go helpers such as `min`, `max`, and `clear` when they simplify the code.

### Structure and comments

- Order declarations as: types, consts, vars, public funcs, public methods, private funcs, private methods.
- Within each category, keep main logic first and helpers last.
- Prefer named helpers or methods over anonymous functions, especially for concurrency.
- Split complex logic into smaller helpers with explicit contracts.
- Every exported type, function, method, and field needs GoDoc.
- Every non-trivial file needs a header comment explaining purpose, invariants, and adjacent-layer contract.
- Non-trivial helpers, especially generator helpers that build `*codegen.File` or resolve ownership/type information, need short contract comments.

### Goa, codegen, templates, and tests

- Never edit `gen/`; regenerate.
- Put validation in the Goa design, not in service code. Avoid `Any`. Service code trusts validated payloads.
- Required arrays must be non-empty. If empty is valid, make the field optional. OneOf/union values must set exactly one variant.
- Do not rely on nil vs empty slices to encode presence.
- Every `Field` must include an inline description string. Prefer `SharedType` for shared types, use DSL `Description(...)`/field descriptions instead of comment-only docs, and add `Example(...)` plus validations where appropriate.
- Prefer codegen-time specialization over runtime interpretation. If the DSL or generator already knows the branch, loop domain, identifier set, metadata, or wiring shape, emit the final code/data directly instead of generating generic runtime logic to rediscover it.
- Apply partial evaluation aggressively: if a branch, collection, or structure is known at generation time, emit only the applicable code. Use template `if`, `range`, and helper composition to specialize the output; do not emit runtime loops or runtime conditionals over static inputs.
- Generated code should expose canonical precomputed artifacts for static facts, such as typed lookups, metadata, routing tables, and configuration. Runtime code should consume those artifacts, not reconstruct them from broader specs on every call.
- Keep runtime branching for truly dynamic inputs only, such as user/model input, network results, database state, and registry-discovered catalogs.
- When runtime dispatch is truly required, prefer a shared runtime library plus generated configuration over duplicating near-identical generated algorithms.
- Generator edits must be section-driven and guard-first: match the target section early and `continue`; avoid redundant `s.Source == ""`-style guards.
- Generator code must stay generic. Derive aliases from imports instead of example-specific names.
- DSL packages may use dot imports; `.golangci.yml` allows `ST1001`.
- Do not introspect Goa `docs.json` at runtime. Use generated `tool_specs.Specs`, including payload/result schemas and codecs.
- Keep template directive indentation independent from emitted Go code. Prefer `{{- ... }}` to control whitespace.
- Write fast deterministic table-driven tests in `*_test.go`. Prefer `testify/assert`; use `testify/require` only when the test cannot continue.
- Do not test impossible internal invariant breaks; test boundaries such as malformed JSON, third-party failures, DB nulls, context extraction, and failed type assertions.

### Type references and transforms

- Use one `NameScope` per emitted file.
- Compute type names and refs with `GoTypeName`, `GoFullTypeName`, `GoTypeRef`, and `GoFullTypeRef`; never build type refs with string concatenation.
- Preserve the original `*expr.AttributeExpr` and locator metadata. Do not synthesize user types unless you copy locator metadata faithfully.
- Let Goa decide pointer/value semantics. For internal transforms, prefer `AttributeContextForConversion(pointer=false)`; only force pointer behavior in transport/validation code when required.
- Build conversions with `codegen.GoTransform(...)`; do not post-process emitted code to change qualification or pointer semantics.
- Gather imports from locators and attributes with `codegen.UserTypeLocation(...)` and `codegen.GetMetaTypeImports(...)`, then render via `codegen.Header`.
- Same-package refs should use empty package context; external refs should use `GoFullTypeRef(...)` with the proper package alias.
- In `specs/<toolset>/transforms.go`, signatures should use local alias types for local generated aliases, while service payload/result refs should come from the service `NameScope`. When initializing a local alias literal, synthesize an `expr.UserTypeExpr` with the local `TypeName` and use a conversion context with empty package name.

## Repo-Specific Workflow

- Key modules:
  - `codegen/`: MCP generators and templates
  - `dsl/`, `expr/`: DSL surface and evaluated expressions
  - `example/`: minimal service, generated `gen/`, and runnable `cmd/assistant`
  - `integration_tests/`: end-to-end YAML scenarios and runner
- Common commands:
  - `make build`
  - `make lint`
  - `make test`
  - `make itest`
  - `make run-example`
- Standard workflow:
  1. Make changes.
  2. Run `make lint`.
  3. Fix issues.
  4. Run `make test` or `make itest`.
- Goa design workflow:
  1. Edit `design/*.go`.
  2. Regenerate with `goa gen ...` and verify `gen/` changed as expected.
  3. Lint and test.
- Place new end-to-end scenarios under `integration_tests/scenarios/*.yaml` and wire them in `tests/`.
- Useful test env vars: `TEST_FILTER`, `TEST_DEBUG`, `TEST_KEEP_GENERATED`, `TEST_SERVER_URL`.
- Commit messages should be imperative and scoped. PRs should include summary, rationale, linked issues, and test evidence. Include before/after notes when generator output changes.

### Streaming and runtime contracts

- Streaming planners must choose exactly one event path:
  - Use the decorated client from `PlannerContext.ModelClient(id)` / `input.Agent.ModelClient(id)` and drain the `Streamer` yourself with `Recv()`, or
  - Use `planner.ConsumeStream` only with a raw `model.Client`.
- Never call `planner.ConsumeStream` on a decorated runtime client; it double-emits assistant, thinking, and usage events.
- Register agent-as-tool toolsets with `agenttools.NewRegistration(...)` and runtime-owned options. You may set per-tool or shared text/template content, but never both text and template for the same tool.
- If no prompt override is provided, the runtime builds the default prompt from the optional system prompt plus the tool payload. Validate custom templates with `runtime.ValidateAgentToolTemplates`; templates compile with `missingkey=error`.
- Agent-as-tool runs as child workflows by default via `ExecuteAgentChildWithRoute`. Do not schedule `ExecuteTool` activities for agent-as-tool.
- Provider agents run a worker on their workflow queue; consumers only register the toolset.
- Nested agents always create real child runs. Correlate them through `ChildRunLinked` events and `ToolResult.RunLink`.
- Stream visibility is controlled by `stream.StreamProfile` on the session-owned stream (`session/<session_id>`), with `run_stream_end` markers for per-run termination. If you need a flattened firehose, build a separate subscriber instead of changing the core runtime.

## Safety And Permissions

| Action | Policy |
|--------|--------|
| `git clean/stash/reset/checkout` | **FORBIDDEN** |
| `git push` | Explain intent, then proceed |
| `go clean -cache` | **FORBIDDEN** during normal work |
| Edit `gen/` directly | **FORBIDDEN** |
| Changes >=3 files | Describe plan, then proceed |
| Install new dependencies | Explain why first |
| Delete files | Explain intent, then proceed |

- In streaming and IPC paths, do not guard values that the producer/client contract says are non-nil; let invariant violations surface immediately.
