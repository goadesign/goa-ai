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
- Style & naming: Go 1.24+, format with `go fmt ./...`; keep imports grouped (stdlib separate). Files use `lower_snake_case.go`; packages are short, lowercase. Exported identifiers need GoDoc; avoid stutter. Wrap errors with `%w` and use `errors.Is/As`.
- File organization: Order declarations as Types → Consts → Vars → Public funcs → Public methods → Private funcs → Private methods. No commented‑out code; delete dead code.
- Additional style: Prefer `any` over `interface{}` in new code. Use multi‑line `if` blocks; target ~80 columns where practical. For long struct/composite literals, one field per line with trailing commas; closing brace on its own line.
- DSL note: Dot imports are allowed in DSL packages; see `.golangci.yml` which disables `ST1001`.
- Testing: Write table‑driven tests in `*_test.go` using `testing` (optional `testify`). Name tests `TestXxx`; keep unit tests fast/deterministic. Run `go test -race -vet=off ./...` (or `make test`) locally and avoid coverage regressions.

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

#### Avoid Defensive Programming

- Do not add nil/empty guards for values guaranteed by Goa or by construction. If a required field is missing, let it fail loudly at the boundary.
- Prefer immediate, unmistakable failures over subtle behavior later. If a contract is violated, blow up early so the producer can be fixed.
- Do not include conditional caps or config handling like `if in.Caps != nil { ... }` when the design requires them; set them directly and rely on validations.
- Do not add defensive guards that “paper over” invariant violations (e.g., treating nil events as normal or silently skipping unexpected states).
- In streaming/IPC paths, if the producer/client contract says a value is non-nil, do not guard it with nil checks; allow panics to surface violations so the producer/client can be fixed.