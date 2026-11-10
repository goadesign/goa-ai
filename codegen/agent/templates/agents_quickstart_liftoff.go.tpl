---

## 2. 🚀 The 3-Step Liftoff: Your First Agent Run

The fastest way to run your agent is using the generated example scaffolding.

### Quick Start (Recommended)

```bash
# 1. Generate code and example files
goa gen <module>/design
goa example <module>/design

# 2. Run the generated example
go run ./cmd/<service>/
```

This generates:
- `internal/agents/<service>/bootstrap/bootstrap.go` — Wires runtime and registers that service's agents
- `internal/agents/<agent>/planner/planner.go` — Stub planner (edit to connect your LLM)
- `cmd/<service>/main.go` — Example main that uses the bootstrap
- `gen/<service>/completions/` — Typed completion helpers when your service declares `Completion(...)`

### Understanding the Generated Code

When an unbounded tool declares a payload example and either has no result or
declares a result example, the scaffold demonstrates one complete deterministic
agent turn:

1. `PlanStart` decodes a generated payload example and calls
   `planner.NewToolRequest(gentool.<Tool>Tool(), args)`.
2. `bootstrap.New` registers a deterministic example executor for that toolset.
   The executor validates the payload with the generated codec and returns the
   generated result example, or a successful no-result outcome.
3. The runtime executes the call and invokes `PlanResume` with the correlated
   result.
4. The runtime matches the result to the pending call. `PlanResume` checks the
   tool name, then returns the exact JSON result in the final assistant message.

This path exercises the generated schemas, codecs, executor registration,
runtime tool dispatch, and `PlanStart` → `PlanResume` transition without
requiring model credentials. Replace the planner and example executor together
when connecting real model and service implementations.

If no tool has the examples needed for that demonstration, the generated
planner returns a final greeting without calling a tool. Bounded tools are not
selected because their returned counts and truncation state belong to a real
executor rather than generated sample data.

When a service also declares `Completion(...)` contracts, Goa always generates
`gen/<service>/completions/`. For completions with an authored result example,
the example main also demonstrates both
`Complete<Name>(...)` and `StreamComplete<Name>(...)` using the generated typed
codecs and schema examples.

