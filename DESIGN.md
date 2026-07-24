# Goa-AI: Design-First Agentic Systems

Build intelligent agents, MCP servers, and registry-integrated toolsets from your Goa designs. This plugin extends Goa with agent orchestration, MCP protocol support and centralized registries.

## What you get

- **Agents**: Durable plan/execute loops with policy enforcement, memory, and streaming
- **Typed Completions**: Service-owned structured assistant-output contracts with generated codecs and helpers
- **MCP**: Endpoints mapped from your Goa service (tools, resources, prompts) with JSON-RPC/SSE transport
- **Registries**: Centralized tool catalogs with federation, caching, and semantic search
- **Unified Toolsets**: Single `Toolset` construct with providers (local, MCP, registry)

## How it works

For each service annotated with agents or MCP, the plugin:

1. Derives service expressions from your DSL (see `expr/agent/` and `expr/mcp.go`).
2. Runs standard Goa generators:
   - Service layer via `codegen/service` (service, endpoints, client)
   - JSON-RPC transport via `jsonrpc/codegen` (server, client, types; SSE when streaming)
   - Agent workflows, activities, tool specs, and completion specs via `codegen/agent`
3. Applies small, deterministic transformations so files land under appropriate paths.

We compose on top of Goa—no forks, minimal templates, and predictable output.

## Layout

- Agent packages: `gen/<svc>/agents/<agent>/`
- Tool specs: `gen/<svc>/agents/<agent>/specs/`
- Service completions: `gen/<svc>/completions/`
- MCP service: `gen/mcp_<service>/`
- Registry clients: `gen/<svc>/registry/<name>/`

## Unified Toolset Model

Goa-AI provides a unified `Toolset` construct with configurable providers:

```go
// Local toolset (inline schemas)
var LocalTools = Toolset("utils", func() {
    Tool("summarize", "Summarize text", func() {
        Args(func() { Attribute("text", String) })
        Return(func() { Attribute("summary", String) })
    })
})

// Goa-backed MCP toolset
var MCPTools = Toolset("assistant", FromMCP("assistant-service", "assistant-mcp"))

// External MCP toolset with inline schemas
var RemoteMCPTools = Toolset("remote-search", FromExternalMCP("remote", "search"), func() {
    Tool("web_search", "Search the web", func() {
        Args(func() { Attribute("query", String) })
        Return(func() { Attribute("results", ArrayOf(String)) })
    })
})

// Registry-backed toolset (discovered at runtime)
var RegistryTools = Toolset("enterprise", FromRegistry(CorpRegistry, "data-tools"))
```

All toolsets are first-class citizens—agents use `Use(toolset)` uniformly regardless of provider.

## Service-Owned Typed Completions

Direct assistant output is a different contract than a tool call, so Goa-AI models
it explicitly with `Completion(...)` on a service:

```go
var Draft = Type("Draft", func() {
    Attribute("name", String, "Task name")
    Attribute("goal", String, "Outcome-style goal")
    Required("name", "goal")
})

var _ = Service("tasks", func() {
    Completion("draft_from_transcript", "Produce a task draft directly", func() {
        Return(Draft)
    })
})
```

Completion names are part of the structured-output contract. They must be
1-64 ASCII characters, may contain letters, digits, `_`, and `-`, and must
start with a letter or digit.

This generates a service-owned completions package with:

- the completion result schema
- generated result codecs and validation helpers
- typed `completion.Spec` values
- unary helpers that request provider-enforced structured output and decode the
  assistant response through the generated codec
- streaming helpers that surface preview `completion_delta` fragments plus one
  canonical final `completion` payload

Streaming completions stay on the raw `model.Streamer` surface, and generated
`Decode<Name>Chunk(...)` helpers decode only the final canonical payload.
Providers that do not implement structured output fail explicitly with
`model.ErrStructuredOutputUnsupported`.
Generated schemas stay provider-neutral. Provider adapters may normalize that
canonical schema to a provider-specific subset for constrained decoding, but
they must fail explicitly instead of redefining the service contract.

The design intentionally keeps completions separate from toolsets: toolsets model
callable capabilities, while completions model final assistant answers. Both reuse
the same Goa types, validations, and codegen pipeline so there is one contract
surface for structured model I/O.

## Planner Step Contract

Each `PlanResult` is one admitted workflow step. Planners may return tool calls,
await work, or a terminal result according to the runtime's mutually exclusive
step shapes.

`PlanResult.SynthesizeAfterTools` selects the success transition for a tool-only
step: after the selected batch succeeds, the next planner activity may only
synthesize a final response. The workflow serializes that decision in its
activity input and exposes it as `PlanResumeInput.SynthesisOnly`, so the contract
survives activity retries and execution on another worker. Planners remain
responsible for enforcing synthesis-only output because provider APIs may need
the tool catalog to interpret the preceding tool results.

The runtime keeps execution policy and planner intent separate:

| Completed step | Next state |
| --- | --- |
| A cap or deadline requires finalization | `PlanResumeInput.Finalize` |
| A successful `TerminalRun` tool completed | End the run without another planner turn |
| Any failed tool has a `RetryHint` whose `AllowsRetry()` is true | Normal repair turn |
| `SynthesizeAfterTools` is true and no failure allows retry | `PlanResumeInput.SynthesisOnly` |
| Otherwise | Normal continuation turn |

This order makes recovery explicit rather than presence-based. `RetryHint` may
also carry terminal classification such as `timeout`; callers use
`AllowsRetry()` instead of treating every non-nil hint as permission to retry.
Synthesis-after-tools batches must contain at least one budgeted tool and cannot
contain a `TerminalRun` tool, ensuring the existing step classification always
reaches the appropriate planner resume. The resume activity validates the
returned planner result, so ignoring `SynthesisOnly` fails at the activity
boundary rather than reopening execution.

### Run Timing and Indefinite Awaits

The workflow loop is the sole owner of run-duration enforcement. `TimeBudget`
and `FinalizerGrace` (`RunPolicy`) become a deterministic "Hard" deadline
tracked in workflow code; the run finalizes gracefully through the normal
terminal-hook path once that deadline elapses during active planner/tool
work. Time spent blocked on an external-input await (`await_clarification`,
`await_confirmation`, provided tool results) is explicitly excluded from that
deadline, so an operator can take arbitrarily long to respond without
burning the run's active-time budget.

Engines must never impose a second, competing wall-clock ceiling (for
example Temporal's `WorkflowRunTimeout`) on top of this. Unlike the
workflow's own deadline check, an engine-level timeout force-closes the run
from outside application code, so it can fire mid-await and permanently
strand the run without ever emitting a `RunCompleted` event — exactly the
failure this design avoids. `resolveRunTiming` therefore never derives an
engine run timeout from policy; engine start requests leave that field
unset.

## Registry Integration

Declare centralized registry sources for dynamic tool discovery and agent publication:

```go
var CorpRegistry = Registry("corp-registry", func() {
    Description("Corporate tool registry")
    URL("https://registry.corp.internal")
    APIVersion("v1")
    Security(CorpAPIKey)
    SyncInterval("5m")
    CacheTTL("1h")
})

// Federated external registry
var AnthropicRegistry = Registry("anthropic", func() {
    URL("https://registry.anthropic.com/v1")
    Security(AnthropicOAuth)
    Federation(func() {
        Include("web-search", "code-execution")
        Exclude("experimental/*")
    })
})
```

### Registry Vocabulary

- **DSL registry source**: `Registry(...)` declares a remote catalog and `FromRegistry(...)` binds a toolset to it.
- **Generated registry client**: `gen/<svc>/registry/<name>/` contains the agent-side client/helpers for one declared DSL registry source.
- **Registry wire protocol**: `runtime/toolregistry/` defines the Pulse stream names, message envelopes, and output-delta context used by providers, executors, and the clustered gateway.
- **Clustered registry service**: `registry/` implements the standalone multi-node service that admits toolsets, tracks provider health, and routes tool calls over the wire protocol.

Generated `registry.go` files in agent packages are local runtime registration helpers; they do not implement the clustered registry service.

Provider health is owned by the clustered registry and is scoped to provider
instances, not just toolset names. Provider processes supply one stable
`ProviderID` per process/toolset pair on registration, `toolprovider.Serve`, and
`Pong`. The registry stores health per provider id and schema registration token:
identical schema re-registration preserves the token for rollout overlap, while
schema changes rotate it and require a fresh pong from a provider serving the new
schema.

Health tracking and provider registration self-heal after Redis state loss.
Ping scheduling uses expiring per-toolset Redis leases that the next scheduler
tick re-acquires, the registry repairs its replicated-map revision counters so
post-loss writes propagate to surviving nodes, and `toolprovider.Serve`
periodically recreates its consumer group and re-asserts registration via the
`EnsureRegistration` option, restoring the catalog entry without redeploys.

### Transcript Boundary

- **Stateless model adapters**: Provider clients accept the full provider-ready
  transcript in `model.Request.Messages`; they never reload history from a
  runtime-owned `RunID`.
- **Durable replay**: The runtime persists canonical transcript deltas as
  runlog records so providers, replay tooling, and future backends can
  reconstruct the exact message order generically.
- **Planner-transparent provenance**: Each model call produces an isolated
  canonical response before planner code observes completion. Streams expose
  only closed typed presentation events and carry the canonical response
  separately through gateways. The runtime identifies tool turns from unchanged
  model-facing calls and terminal turns from the canonical provider message
  returned by its response helpers. It commits the complete selected response
  once after atomic admission and before effects. Planners never manage
  transcript handles or provider replay metadata, and uncertain ownership fails
  instead of selecting by call order or visible text.
- **History compression**: Agent designs may declare compression defaults with
  `CompressAtTurns`, `CompressAtMaxInputTokens`, `KeepMaxTurns`, and
  `KeepMaxInputTokens`. The runtime evaluates token budgets with the configured
  model client's exact `model.TokenCounter`, so tokenization stays
  deployment/model-specific while the design records the agent's default policy.
  The Anthropic adapter implements this capability through the Messages token
  count API using the same message, tool, and cache encoding as completion, so
  direct Anthropic and compatible gateways such as Bedrock Mantle can provide
  exact counts without a second transcript conversion. Counting consumes the
  canonical encoding only — completion policy such as the max_tokens
  requirement never applies, because the count API carries no max_tokens.
  When encoded tools carry authored `input_examples`, completion, streaming,
  and counting all attach the same Anthropic tool-examples beta header.
  Exact retention always keeps whole recent turns; it never truncates
  tool_use/tool_result pairs to satisfy a token budget.
- **Bookkeeping control plane**: `Bookkeeping()` calls and results remain in the
  provider transcript so signed model-authored parts replay without modification.
  They consume neither retrieval budget nor consecutive-failure allowance.
  Successful bookkeeping results are omitted only from compact `ToolOutputs`
  and do not force another planner turn. A failed bookkeeping result enters a
  repair turn only when its `RetryHint.AllowsRetry()` is true. A bookkeeping-only
  turn must otherwise resolve in the same turn via a terminal outcome or an
  await/pause handshake.
- **Forced finalization control plane**: when runtime caps or deadlines force
  finalization, planners may return terminal bookkeeping tools instead of a
  prose final answer. The runtime executes only `TerminalRun()` tools in that
  path (`TerminalRun()` implies bookkeeping), keeps them inside the remaining
  hard-deadline window, and closes the run only if every terminal side effect
  succeeds. Retry-owned
  restrict-to-tool state filters budgeted work tools only, so bookkeeping tools
  remain available in correction and finalization turns. Caller
  `WithRestrictToTool` policy remains run-scoped and still applies to every tool.
- **Visible reasoning contract**: Bedrock adaptive-thinking requests ask for
  summarized reasoning display explicitly so streamed `thinking` events remain
  visible across Claude adaptive model revisions whose provider defaults may
  otherwise omit the reasoning text payload.

## MCP Server Definition

Enable MCP protocol for a service with `MCP`:

```go
Service("calculator", func() {
    MCP("calc", "1.0.0", ProtocolVersion("2025-06-18"))
    Method("add", func() {
        Payload(func() { Attribute("a", Int); Attribute("b", Int) })
        Result(func() { Attribute("sum", Int) })
        Tool("add", "Add two numbers") // Context-aware in Method
    })
})
```

### Protocol version

Set the MCP protocol version in your design using the DSL option on `MCP`:

```go
MCP("assistant-mcp", "1.0.0", ProtocolVersion("2025-06-18"))
```

The generator emits a constant `DefaultProtocolVersion` in `gen/mcp_<service>/protocol_version.go`.

### Adapter options

The generated `MCPAdapterOptions` provides configuration hooks:

- Logger: `func(ctx context.Context, event string, details any)` to observe adapter lifecycle.
- ErrorMapper: `func(error) error` to normalize errors to JSON-RPC codes.
- AllowedResourceURIs, DeniedResourceURIs: simple allow/deny lists for resource URIs.
- StructuredStreamJSON: when true, stream events are emitted as `resource` items with `application/json`.
- ProtocolVersionOverride: override `DefaultProtocolVersion` at construction time.

## Streaming

No custom streaming templates. When your methods stream, Goa's JSON-RPC generator emits the SSE stack. We simply adjust paths/imports so it lives under the MCP tree.

## Agent run lifecycle streaming contract

The runtime emits a single terminal lifecycle event per run via `hooks.RunCompletedEvent`.
The stream subscriber translates it into a `workflow` stream event (`stream.WorkflowPayload`)
that UIs and stream bridges can consume without heuristics.

- **Terminal status**
  - `status="success"` → `phase="completed"`
  - `status="failed"` → `phase="failed"`
  - `status="canceled"` → `phase="canceled"`

- **Cancellation is not an error**
  - For `status="canceled"`, the stream payload **must not** include a user-facing `error`.
  - Consumers should treat cancellation as a terminal, non-error end state.

- **Failures are structured**
  - For `status="failed"`, the stream payload includes:
    - `error_kind`: stable classifier for UX/decisioning (provider kinds like `rate_limited`, `unavailable`, or runtime kinds like `timeout`/`internal`)
    - `retryable`: whether retrying may succeed without changing input
    - `error`: **user-safe** message suitable for direct display
    - `debug_error`: raw error string for logs/diagnostics (not for UI)
  - Invalid-argument tool failures may carry a planner retry hint. Timeout,
    cancellation, and exhausted run budget are terminal and carry no retry
    hint.

- **Terminal identity**
  - `RunCompletedEvent.Labels` carries the run-scoped labels provided at run
    start (`RunInput.Labels`, nil when the run had none) so completion
    subscribers can attribute the outcome without tracking run identity out of
    band. `run.Snapshot.Labels` exposes the same identity to polling readers,
    replayed from the durable `RunStarted` record.

This keeps consumers simple: render `error`, gate “Retry” on `retryable`, and treat `canceled` as non-error.

## Provider Stream Integrity Contract

Provider adapters (Bedrock, Anthropic) validate the streaming event protocol
with a strict state machine: a message must start before content blocks flow
and must stop exactly once before metadata. Violations never produce a
fabricated response; they fail the stream with a precise error. Two terminal
shapes are classified instead of surfaced as opaque protocol errors:

- **Empty stream** — the stream terminates before any message starts (a
  `messageStop` with no prior `messageStart`, or a stream that closes with no
  events at all). Providers intermittently do this when a model emits an
  empty completion. Adapters build the error with `model.NewEmptyStreamError`,
  which carries the `model.ErrEmptyStream` sentinel plus a retryable
  `unavailable` ProviderError (code `empty_stream`). Callers detect it with
  `errors.Is(err, model.ErrEmptyStream)` and may retry the request a bounded
  number of times before surfacing the failure.
- **Truncated stream** — the stream closes cleanly after a message started but
  before `messageStop`. The classification is a retryable `unavailable`
  ProviderError (code `truncated_stream`) without the empty-stream sentinel:
  output was partially produced, so blind pre-output retry policies must not
  match it.

`model.NewStreamEndedEarlyError(provider, operation, started)` is the single
classifier for streams that close before message stop; adapters pass whether a
message had started and the model package owns which of the two shapes
applies.

Adapters never retry internally; retry policy belongs to the integrating
application (for example, a guard that retries only before the first visible
output chunk).

## Runtime Tracing Error Contract

The runtime uses one generic rule for span failures across model clients and
Temporal activities:

- Non-nil errors mark spans failed by default.
- They do not mark spans failed when the active context is already done and the
  returned error is a structured context-termination shape.
- Supported termination shapes are `context.Canceled`,
  `context.DeadlineExceeded`, and gRPC `Canceled` / `DeadlineExceeded`
  statuses.

This tracing rule is intentionally generic. Application-specific error
taxonomies, dashboard semantics, and product observability attributes belong in
the integrating application rather than in the runtime.

## GenAI Observability Contract

The runtime emits OpenTelemetry GenAI semantic-convention spans for agent
operations:

- Planner-scoped model calls use `gen_ai.operation.name="chat"` and span names
  of the form `chat {model}`. Model requests must carry a model name or model
  class, and the runtime attaches conversation, agent, request model, max token,
  response model, finish reason, token usage, and streaming
  time-to-first-chunk attributes.
- Tool executions use `gen_ai.operation.name="execute_tool"` and span names of
  the form `execute_tool {tool_name}`. The runlog hook subscriber owns these
  spans so inline, activity, and registry-backed tools produce exactly one
  GenAI tool operation. Tool arguments and results are not recorded as span
  attributes because they may contain user data.
- Agent-as-tool links emit caller-side `invoke_agent {agent_name}` spans. The
  child agent emits its own model and tool spans under its own agent identity.

Prompt content, chat history, tool arguments, and tool results remain opt-in
application policy. The runtime records identifiers, names, counts, timings,
errors, and token usage by default.

## Temporal Worker Activation Contract

Temporal worker startup is a real runtime contract, not a background best-effort
side effect:

- Worker-capable engines stage workflow and activity registrations until
  `runtime.Seal(ctx)` closes registration.
- In the Temporal engine, sealing is the activation boundary. It starts every
  registered worker with `worker.Start()`, retries startup failures until `ctx`
  ends, and returns an error if activation never succeeds before the caller's
  deadline.
- Once sealing returns `nil`, the runtime may safely start serving traffic
  because its workers are actively polling.
- Post-start fatal worker failures surface through the configured
  `worker.Options.OnFatalError` callback instead of being silently ignored.
  Integrating services should treat that callback as process-fatal and exit.

## Tool Input Schema

For each tool with a non-empty payload, the plugin derives JSON Schema from the
Goa attribute using Goa's `openapi.Schema` type for complete JSON Schema draft
2020-12 support. The generated tool spec is the canonical model-facing contract:
it contains the annotated schema, a schema with only the root `example` removed,
the raw authored example JSON, and a parsed `ExampleInput` object when the
payload has an authored top-level Goa `Example(...)`.

An authored top-level Goa `Example(...)` is the only source for provider-facing
top-level tool examples. Synthesized Goa examples may remain nested JSON Schema
annotations for fields and definitions, but they are not promoted to
provider-native examples. This keeps provider examples intentional rather than
letting generated placeholder data become model guidance.

Provider adapters choose between the precomputed projections. Providers that
consume JSON Schema annotations use the annotated schema. Direct Anthropic and
Bedrock Claude use top-level `input_examples` with the schema that omits the root
example; Bedrock carries those examples through Anthropic's provider-native
request fields in `additionalModelRequestFields` when the required beta contract
applies, while the direct Anthropic adapter attaches the corresponding beta
header (additively, preserving caller-configured betas) to completion,
streaming, and token-count requests. The header is attached only when a tool
carries authored examples because header-compatible gateways such as Bedrock
Mantle reject beta identifiers they do not recognize. Claude-on-Vertex serves
the same native contract: it delivers `input_examples` with no beta
activation and ignores the header (live-verified via rawPredict
`usage.input_tokens`). Runtime and product code do not inspect or rewrite
schemas to infer provider-specific shapes.

Any proxy or product-owned model boundary that reconstructs goa-ai model tools
must carry these projections as one provider-neutral `model.ToolInputContract`.
The boundary should not import generator-only `tools.TypeSpec`, re-marshal
decoded schemas back into generated bytes, or know which provider consumes which
projection. Dropping the example fields before a provider adapter runs prevents
Anthropic/Bedrock from sending `input_examples`, even though the generated tool
spec still contained the authored examples.

## Tool Identification

Tools are identified by canonical IDs in the format `<toolset>.<tool>` (dot-separated). The generated code produces typed constants (e.g., `MyTool tools.Ident`) matching this format.

## Agents Quickstart & Example Scaffold

A contextual quickstart file `AGENTS_QUICKSTART.md` is emitted at the module root on `goa gen`, summarizing what was generated and how to wire it. To opt out, invoke `DisableAgentDocs()` inside your API DSL.

The `goa example` phase generates application-owned scaffold under `internal/agents/`:

- `internal/agents/bootstrap/bootstrap.go`: constructs a minimal runtime and registers generated agents
- `internal/agents/<agent>/planner/planner.go`: planner stub implementing `PlanStart`/`PlanResume`
- `internal/agents/<agent>/toolsets/<toolset>/adapter.go`: stubs for mapping method-backed tools

## Security considerations

- Resource policy: use deny/allow lists to constrain which URIs can be read
- Registry authentication: use Goa security schemes (`APIKeySecurity`, `OAuth2Security`, etc.)
- Logging: avoid logging sensitive payloads and results in production

## Error code mapping

The adapter maps Goa `ServiceError` with name `invalid_params` to JSON-RPC `-32602`, `method_not_found` to `-32601`, and otherwise defaults to `-32603` (internal).

## Contributing

- Add agent concepts in `expr/agent/` and update the expression builders
- Add MCP concepts in `expr/mcp.go` and update the MCP expression builder
- Add registry concepts in `expr/agent/registry.go`
- Keep new templates small and transport-agnostic; compose on Goa JSON-RPC outputs

## Summary

This plugin gives you agents, MCP, and registries with familiar Goa patterns, minimal surface area, and a directory layout that feels natural. It's accurate, easy to maintain, and designed to evolve alongside Goa.
