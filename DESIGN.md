# Goa-AI: Design-First Agentic Systems

Build intelligent agents, MCP servers, and registry-integrated toolsets from your Goa designs. This plugin extends Goa with agent orchestration, MCP protocol support and centralized registries.

## What you get

- **Agents**: Durable plan/execute loops with policy enforcement, memory, and streaming
- **Typed Completions**: Service-owned structured assistant-output contracts with generated codecs and helpers
- **Generated Evaluations**: Design-owned scenarios with typed application hooks, bounded execution, and trustworthy semantic judging
- **MCP**: Unary tools, resources, and static prompts mapped from your Goa service over JSON-RPC HTTP
- **Registries**: Centralized tool catalogs with federation, caching, and semantic search
- **Unified Toolsets**: Single `Toolset` construct with providers (local, MCP, registry)

Agent streams assistant text and thinking from the designated planner model call
directly to clients. Emitted assistant text is append-only: later tool or output
validation cannot retract it. One execution-scoped response ID groups fragments
from the same planner activity. Plan and Resume activities are single-attempt,
so one response ID can never name output from two model executions. A later
explicit planner turn receives a new response ID. The workflow's committed
assistant-turn event uses that same ID. An accepted response stores its complete
provider transcript and sends those exact ordered messages to stream consumers.
A rejected response or ordinary failure
reported before activity cancellation stores the exact text already delivered
as a plain assistant message before recovery or failure continues. Live
fragments themselves do not enter the run log, hook
bus, or memory. Tool
argument fragments and completed calls stay inside model validation until the provider's terminal
response reconciles with the stream and the originating advertised schema plus
attached decoder accept every payload. A raw remote gateway transports provider
output without replacing those request-owned checks. After planner selection,
the canonical response is persisted once, and its ordered runtime records are
published through one idempotent activity batch.

## How it works

For each service annotated with agents or MCP, the plugin:

1. Derives service expressions from your DSL (see `expr/agent/` and `expr/mcp.go`).
2. Runs standard Goa generators:
   - Service layer via `codegen/service` (service, endpoints, client)
   - JSON-RPC transport via `jsonrpc/codegen` (server, client, and types)
   - Agent workflows, activities, tool specs, and completion specs via `codegen/agent`
   - Evaluation suites and typed scenario hooks via `eval/codegen`
3. Applies small, deterministic transformations so files land under appropriate paths.

We compose on top of Goa—no forks, minimal templates, and predictable output.

## Layout

- Agent packages: `gen/<svc>/agents/<agent>/`
- Tool specs: `gen/<svc>/agents/<agent>/specs/`
- Service completions: `gen/<svc>/completions/`
- Evaluation suites: `gen/evals/<suite>/`
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
    Example(map[string]any{
        "name": "Investigate startup alarms",
        "goal": "Explain every alarm observed during startup.",
    })
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

- public `Spec<Name>()` factories that return fresh typed contracts containing
  the result schema and generated codec
- narrow `<Name>Example()` accessors that return an immutable copy of the
  canonical authored root example, when the return type declares one
- public `Complete<Name>` wrappers that own provider-enforced structured output
  and decode the assistant response through the private generated codec
- public `StreamComplete<Name>` wrappers that may surface preview
  `completion_delta` fragments and
  expose the final `completion` value only after the provider ends a valid
  stream and its complete response contains the same value

Streaming completions use a typed `completion.Streamer[T]`. It may expose
preview chunks, but its typed `Value` stays unavailable until the complete
provider stream and final response pass validation.
Providers that do not implement structured output fail explicitly with
`model.ErrStructuredOutputUnsupported`.
Generated schemas stay provider-neutral. Provider adapters may normalize that
canonical schema to a provider-specific subset for constrained decoding, but
they must fail explicitly instead of redefining the service contract.
Gemini tool declarations retain the supported structure, required fields,
types, and enums, translate `oneOf` choices to `anyOf`, and omit validation
limits and annotations that Vertex rejects. The request-owned validator applies
the complete original schema to the completed tool call before it becomes
visible. An unknown structural keyword fails translation instead of being
dropped.
Before provider work, the validated client compiles the canonical schema into a
request-owned validator. Raw schema bytes pass directly to the JSON Schema
compiler's in-memory loader; the runtime does not publish or pass a generic map
as the schema contract. It applies that validator to unary output. For
streaming output, it retains the final completion until the provider ends,
validates both final representations, and requires their JSON bytes to match
before returning the completion. Generated validation adds checks without
replacing these framework checks. Provider enforcement and local validation
are both required: local validation remains the final acceptance check, but it
never substitutes for a provider that cannot enforce the requested schema.
The Bedrock Converse adapter uses a private strict tool for Claude 4.6 so
Runtime `CountTokens` and Converse receive the same schema. Claude 4.5 retains
native `OutputConfig` because its manual thinking mode cannot use forced tools.
The Claude-on-Bedrock Messages adapter uses native output formatting only when
Bedrock can enforce it. Sonnet 5 and Opus 5 reject both
`output_config.format` and the `strict` tool property, so the adapter returns
`model.ErrStructuredOutputUnsupported` before inference. It never weakens the
request to an ordinary tool backed only by local validation. The same
Anthropic adapter encodes user-message `ImagePart` bytes as base64 image blocks
for PNG, JPEG, GIF, and WebP, so direct Anthropic, Claude-on-Vertex, and
Claude-on-Bedrock clients share one multimodal message contract.
Adapters with provider-native structured-output examples receive the generated
root example separately from the schema. Unary helpers ask the model once for a
structured value. If the generated codec rejects the response, the helper
returns a non-retryable `planner.OutputContractError` and does not ask the model
again. `completion.Response.ModelResponse` contains that exact model response
and its token usage. Streaming helpers follow the same one-request rule. Provider
adapters may suppress previews when their wire representation contains private
framing that is absent from the completion contract.

The separate `runtime/agent/tooloutput` package owns the case where an
application deliberately permits the model to replace invalid ordinary-tool
arguments. `Run[T]` accepts one typed `completion.Spec[T]`, normally returned
by the generated `Spec<Name>()` factory. The spec exposes only the output name,
description, schema, example, and codec. The helper privately
uses those fields as both the argument and result contract of one ordinary
tool, so callers cannot add tool policy or execution behavior. A private
in-memory agent advertises and forces only that tool, allows one successful
tool execution, and requires it to complete the run. The typed schema and codec
remain the model boundary; malformed JSON and typed validation failures use the
runtime's bounded correction flow. Provider failures and every non-argument
failure remain terminal. The returned `T` is exactly the value decoded from
the accepted arguments, so callers cannot insert domain execution or rewriting
between accepted model output and the returned value.

The design intentionally keeps completions separate from toolsets: toolsets model
callable capabilities, while completions model final assistant answers. Both reuse
the same Goa types, validations, and codegen pipeline so there is one contract
surface for structured model I/O.

Every model request crosses one allocation preflight before the client copies
messages, media, tool contracts, or structured-output schemas or calls an
observer or provider. The complete request shares one 16 MiB byte budget and
one 100,000-value visit budget.

All provider output crosses one allocation preflight before copying,
fingerprinting, or decoding. A unary response has one 16 MiB byte budget and
one 100,000-value visit budget; nested dynamic metadata and tool-result values
have a maximum depth of 64. A stream shares those bounds across all chunks and
its terminal response, which is also bounded independently. Before copying the
terminal response, the boundary exempts only fields that exactly repeat
accepted chunks; response wrappers, metadata, and new or mismatched data
consume the remaining shared budget. Every collection length is checked before
allocating its copy or its sorted map-key list.

Canonical dynamic values are JSON-shaped: nil, booleans, finite numbers, valid
UTF-8 strings, byte slices, arrays, slices, and valid UTF-8 string-keyed maps.
Structs are not canonical; bounded structs may survive only in rejected-output
evidence. Pointers are unsupported in canonical data and rejected evidence.
The boundary rejects invalid UTF-8, cycles, unknown typed parts, typed-nil
streamers, and over-budget output without fallback, truncation, coercion,
repair, or omission.

## Generated Evaluations

Applications define stable evaluation scenarios with `eval/dsl` beside their
normal Goa v3 service design. A scenario `Input` is a Goa schema; concrete
users, facilities, prompts, and other fixture values remain application code.
`goa gen` emits local input types, Goa validation, one typed hook method per
scenario, and a constructor that validates all supplied values before any live
call. A scenario without `Input` receives only `context.Context`.

Suites may be generic top-level declarations or nested inside an agent.
Agent-attached suites receive a generated lookup over the static tool specs
reachable through that agent and its nested generated agents. The lookup
delegates to canonical generated `ToolSpec` values and codecs; it does not
reconstruct schemas or include registry-discovered contracts.

`goa example` emits `cmd/<suite>-evals/main.go` once. This application-owned
scaffold exposes every input and hook TODO but is not overwritten after product
logic is added. The application implements those hooks to call the real product
and return:

- deterministic checks for exact facts such as IDs, counts, and tool calls;
- semantic claims for meaning that requires reading a model answer; and
- artifacts that help diagnose a failure.

The runner owns execution mechanics. Callers choose all scenarios, exact
scenario IDs, or tags through separate methods. Selection is validated before
any product or model call. An explicit positive concurrency limit bounds work;
failures do not stop unrelated scenarios; reports remain in declaration order.
Progressive reporter callbacks expose starts and finishes without moving
scheduling into the application. Caller cancellation stops new hooks, cancels
in-flight contexts, and returns a partial report plus the context error.
Application hooks, reporters, and the judge support concurrent calls up to the
configured limit.

When semantic claims are present, a provider-neutral judge classifies each one
as `entailed`, `contradicted`, `not_addressed`, or `indeterminate`. Before any
scenario runs, the runner verifies all four meanings with framework-owned
examples. Applications cannot weaken that check by supplying easier examples.
Only `entailed` passes. Deterministic-only suites may omit the judge entirely.

See [docs/evals.md](docs/evals.md) for the DSL, generated API, runner methods,
and report behavior.

## Planner Step Contract

Each `PlanResult` is one admitted workflow step. Planners may return tool calls,
await work, or a terminal result according to the runtime's mutually exclusive
step shapes.

External input does not weaken provider transcript identity. A model-authored
free-text interaction uses the tool-clarification await branch, which retains
the exact tool name, call ID, and payload. The successor run supplies the human
answer as that tool's generated result. Runtime-owned clarification prompts
remain a separate branch whose successor supplies a user message. This
distinction prevents
prompt reminders or presence-dependent fields from standing in for a real
`tool_use` / `tool_result` exchange.

`PlanResult.SynthesizeAfterTools` selects the success transition for a tool-only
step: after the selected batch succeeds, the next planner activity may only
synthesize a final response. The workflow serializes that decision in its
activity input and exposes it as `PlanResumeInput.SynthesisOnly`, so the contract
survives activity retries and execution on another worker. Planners remain
responsible for enforcing synthesis-only output because provider APIs may need
the tool catalog to interpret the preceding tool results.

Planner requests contain domain intent: a tool name and typed payload. A request
forwarded from a model response also carries that provider's correlation ID;
planner-authored requests do not invent one. The runtime assigns every accepted
request a deterministic execution ID from the run, turn, attempt, tool, and
batch position. It rejects a provider ID that does not belong to the selected
model response. When planner code compiles model output into different
executable intent, the runtime retains the original model name and payload
separately for the transcript.

Model-derived calls have a nonempty `ModelToolCallID` and must name a tool in the
exact catalog shown to the model. Planner-authored calls have an empty ID and
must name a tool in the agent definition's executable catalog. During exact
`correct_call` recovery, they must instead name a tool from the saved recovery
catalog. Dedicated continuation tools stay hidden from models, but
planner code may call one when it reconstructs the typed cursor payload itself.
Such a direct call is standalone and does not create a model-facing continuation
action. Generated codecs and run policy still validate every request.

Generated agent definitions populate the executable catalog. Code that builds
an `AgentDefinition` directly must list every tool the agent may execute; an
empty list means the agent has no executable tools.

The validated model client applies each tool's advertised schema before its
attached input decoder and before a response becomes canonical. A schema
rejection or non-nil `*tools.ValidationError` can produce limited-size
replacement guidance without copying rejected argument values. Ordinary
decoder and internal errors remain terminal. The workflow records usage, keeps
the rejected call out of transcript history, and spends one recovery turn on a
normal planner resume with executable tools available. It selects the rejected
invocation by invocation start order, never by a model-supplied value or
completion order.
When a provider cannot represent a completed tool call because its arguments
are not valid JSON, the adapter reads only the stream's terminal usage and
completion evidence. The same recovery path supplies fixed JSON replacement
guidance without retaining the malformed bytes, provider diagnostics, or tool
identity.
When a supported provider instead returns a valid tool name absent from the
request's advertised catalog, the adapter rejects the complete response. The
same invocation-recovery value carries only that untouched name, mutually
exclusive with tool-input correction guidance. The replacement starts from the
last accepted conversation with its current executable catalog. Unstructured
validation failures remain terminal.

When a completed model reply or planner result breaks its required shape, the
planner returns `OutputContractError`. The runtime validates the full result
before accepting its selected tool calls or storing its selected response. The
provider's output-limit status is part of the response given to the planner.
For a final response without tool calls, the runtime does not override a planner
that accepts that exact response. An output-limited response with tool calls is
rejected before any call can execute because the provider may not have finished
the complete call batch.
Ordinary output contract errors are terminal and Temporal does not retry them.
When the planner can give exact replacement guidance for completed model
output, it returns one of two explicit errors. `NewRecoverableModelAnswerError`
spends one recovery turn on a replacement answer without ordinary tools.
`NewRecoverableModelPlanningError` spends one recovery turn on replacement
output with the current executable catalog. The rejected output may contain
invalid tool calls or may have omitted a required call. Both paths are bound to
the exact rejected response fingerprint, record usage, and keep the rejected
body private. Infrastructure failures remain activity errors and follow the
activity retry policy. Plan and Resume activities are the exception: their
generated and registered policy permits exactly one attempt because they may
have already emitted append-only text. Tool activities retain retries because
their stable call identities let tool owners return stored results without
repeating side effects.

The runtime keeps execution policy and planner intent separate:

| Completed step | Next state |
| --- | --- |
| The configured `CompletionTool` executed successfully | End the run without another planner turn |
| `CompletionTool` is configured and another terminal condition occurs | Fail because the required tool did not succeed |
| A cap or deadline requires finalization and the run supplied `LimitTerminalPlans` | Execute the matching terminal call without loading saved messages |
| A cap or deadline requires finalization without either completion policy | `PlanResumeInput.Finalize` |
| A successful `TerminalRun` tool completed without `CompletionTool` | End the run without another planner turn |
| A failed tool requires `finish` recovery and no successful query has another page | `PlanResumeInput.Finalize` with reason `tool_failure` |
| A failed tool requires `finish` recovery while a successful query has another page | Recovery turn containing the failure evidence and only the generated continuation actions |
| Any failed tool has a `ToolFailure` whose recovery action permits tools | Runtime-enforced correction or replan turn |
| A finalization response is correctable, or its terminal tool returns `correct_call` | Spend one recovery turn while retaining finalization and, for a tool failure, advertise only that exact terminal tool |
| A successful batch has `SynthesizeAfterTools` set without `CompletionTool` | `PlanResumeInput.SynthesisOnly` |
| Otherwise | Normal continuation turn |

`LimitTerminalPlans` is one optional run-policy value containing exactly three
payload-only calls: time budget, tool-call cap, and recovery-turn cap.
Before the first planner call, the runtime requires every payload to pass the
registered generated codec and every target to be a `TerminalRun` bookkeeping
tool owned by the agent that does not require confirmation. At a matching limit,
the workflow adds its current identifiers and labels and executes the call
through the normal terminal-tool path. The individual `tool_failure`
termination case never selects these fixed calls because its final result may
depend on saved tool evidence. Instead, the finalization planner may use that
evidence to author a registered terminal bookkeeping call.

Immediately before either a fixed limit call or a planner-authored finalization
call executes, the runtime writes the exact `planner.TerminationReason` to
`runtime.FinalizationReasonLabel` (`goa-ai.finalization_reason`). This happens
after run and policy labels are applied, so callers, policies, planners, and
models cannot choose or replace the reason. Ordinary tool calls do not receive
this label: the runtime removes the reserved key if any of those sources
supplies it.

Consumers of fixed-limit and planner-authored finalization calls, including
`tool_failure`, read the termination reason from
`runtime.FinalizationReasonLabel` (`goa-ai.finalization_reason`).

Temporal workers strictly decode planner activity input. Every worker and
caller on one task queue must use the same generated input contract.

This order makes recovery explicit rather than presence-based. `ToolFailure`
classifies why execution failed independently from its `RecoveryDirective`:
`correct_call` keeps the failed tool available and attaches its generated
validation issues, rejected model-authored input, and example to the next
planner activity. Executors and tool activities report only the classification,
error, recovery action, and generated field issues. Immediately before the
failure becomes durable or model-visible, the workflow requires a provider
tool-call ID and replaces any executor-supplied correction input and example
with clones from the retained model call and registered tool specification.
Runtime-authored calls, including automatic pagination continuations, have no
provider tool-call ID and therefore cannot request `correct_call`; the workflow
returns a contract error without rendering their execution payload. The planner
may retry fewer, equal, or more calls, combine work, use another advertised
capability, await input, or answer. Provider adapters continue to project
historical canonical tool names independently of the current catalog.
`replan` removes the failed tool from the recovery turn while permitting another
advertised action, input request, or answer. A direct model call to an excluded
tool is rejected as invalid planner output before any sibling call executes.
Planner-owned tool-backed awaits remain strict because they encode suspension
rather than a raw model request. `finish` forbids new domain work. It enters
finalization unless a successful sibling query already has another page. In
that case the recovery turn excludes every authored domain tool and retains only
the generated continuation actions for those unfinished queries; the planner
may continue them or answer from the evidence already collected. The finalizer
may return a final response or registered terminal bookkeeping calls, such as
committing a Task report. When the same tool has both correction and replan
failures in one batch, the correctable failure keeps that tool available. A
recovery turn may end with an input suspension; its evidence remains available
when a new workflow continues after the answer.
A failed batch never enters `SynthesisOnly` and does not preserve its earlier
`SynthesizeAfterTools` intent; a planner that retries work selects synthesis
again on that new batch.
An agent-as-tool result follows the same typed success or failure transition as
every other tool. Its observed child-tool count is run-link telemetry, not an
outcome signal: validation may reject the request before a child tool runs, and
a child agent may return a valid result without invoking another tool.
Synthesis-after-tools batches must contain at least one budgeted tool and cannot
contain a `TerminalRun` tool, ensuring the existing step classification always
reaches the appropriate planner resume. The resume activity validates the
returned planner result, so ignoring `SynthesisOnly` fails at the activity
boundary rather than reopening execution.

### Run Timing and Workflow Continuations

Each accepted user input starts one top-level workflow for that turn. The
workflow ends with either that turn's final result or an external-input
suspension. Nested agents retain their linked child workflows.

The workflow loop is the sole owner of run-duration enforcement. `TimeBudget`
becomes the deterministic Budget deadline for active planner and tool work.
The Hard deadline is Budget plus `FinalizerGrace`; it bounds the final planner
activity after budget exhaustion. Terminal hook persistence uses its own
completion context after planner execution. Time spent blocked on an
external-input request (`await_clarification`, `await_confirmation`, or
provided tool results) consumes neither deadline. The workflow ends with the
remaining Budget and Hard durations in a trusted checkpoint. A continuation
starts a new workflow and rebuilds both deadlines from those durations, so a
person can take arbitrarily long to respond without burning active work time or
keeping a workflow assigned to an old deployment.

The terminal `RunOutput.Suspension` contains an ordered public request list and
an opaque private checkpoint. Before returning that result, the runtime calls
`storage.Store.RecordRunSuspension`, which stores the checkpoint, suspended run
status, and matching `RunSuspended` record in one operation. Exact activity
retries are idempotent; a different suspension for the same run is rejected as
corruption. The application exposes only the public request to clients and must
atomically accept one response before it starts the next workflow, so concurrent
submissions cannot continue the same state twice under different run IDs. A
continuation supplies the completed run ID and exactly one typed response for
the first request; the runtime loads its own checkpoint. Running runs remain
temporarily not ready. Completed, failed, and canceled runs permanently have no
suspension. A missing checkpoint, malformed stored value, mismatched stored and
payload IDs, invalid checkpoint state, or checkpoint for another predecessor
returns `runtime.ErrRunSuspensionCorrupt`. Store failures retain their original
errors because they do not prove that the checkpoint is permanently unusable.
The application may also supply engine
options for the new workflow, such as memo and search attributes used for
ownership and observability. These options do not alter the saved execution
contract. If requests remain, that workflow stores and returns a new
suspension. The checkpoint restores the transcript, planner state, labels,
policy, nested-agent identity, and exact tool-call/result provenance. The
generated caller and worker share one immutable `AgentDefinition` containing
the route, tool specifications, codecs, required labels, completion policy, and
transitive child definitions. `AgentClient.PrepareContinuation` loads the saved
checkpoint and validates it and the supplied response against that complete
definition before the workflow engine receives the successor ID. It returns an
opaque `PreparedRun`. `StartPrepared` submits that exact stored request, so a
retry cannot silently load or construct a different request. The
workflow validates the same stored input again before restoring it. A saved
payload, result, child suspension, or policy value that the current generated
contract does not accept rejects the continuation. A tool call created in an earlier workflow
retains that workflow's run ID while its result records the new workflow's run
ID. The tool-result hook and `tool_end` stream payload carry the original call
run ID explicitly; the result event's own run ID identifies the workflow that
received the external answer. When a nested agent suspends, the parent ends
with the same request; continuing the parent starts a new child workflow from
the child's saved checkpoint. Sessionless one-shot runs reject external-input
requests because they have no continuation API.

Initial sessionful runs use the same command. `Prepare` validates the generated
agent contract and produces the final engine request without starting it.
The returned `PreparedRun.RunID` is the workflow ID the application associates
with its durable command. Applications can store the versioned bytes and parse
them after a process restart. `Start` and `Continue` are convenience methods
that prepare and start through this same path.

Initial and one-shot calls express each launch setting through `WithTaskQueue`,
`WithMemo`, or `WithSearchAttributes`. Continuations take one
`runtime.WorkflowOptions` value because their other arguments identify the
saved run and its typed answer. The value configures only the new engine start;
it never becomes workflow input or changes the saved continuation state.

Initial preparation is client-only and performs no storage write, worker
registration change, or engine call. One-shot preparation uses the same path.
Continuation preparation first reads the saved suspension, then performs the
same pure request construction. It does not create the optional stored form.
`MarshalBinary` creates and validates that form; `StartPrepared` independently
submits the prepared engine request. The convenience methods never serialize
the stored form.

Prepared bytes remain private application data because they can contain the
complete transcript and continuation checkpoint. The application atomically
stores those bytes with initial-run admission or continuation-answer acceptance.
Launch settings such as memo, search attributes, and task queue exist only on
the engine request; `api.RunInput` contains only workflow input. The inclusive
`engine.MaxPayloadBytes` limit counts request IDs and names together with the
encoded workflow input, memo, search-value, and reserved digest memo data and
metadata. The prepared JSON record has a separate eight-times-larger limit for
its complete representation, including the agent ID, explicit queue override,
JSON escaping, base64 expansion, and field syntax. `MarshalBinary` enforces it
when creating a record and parsing enforces it before decoding. The storage
limit protects the record format and never increases the engine request limit.

Runtime callers provide ordinary memo values. Goa-AI encodes each value once
and gives engines an `engine.EncodedValue` containing its encoding metadata and
data bytes. Engines store those bytes directly. This keeps a local start and a
start parsed after a process restart identical without requiring engines to
know the caller's private Go types.

Every engine adapter applies the public `engine/contract` ownership rules. It
normalizes and privately retains each accepted request, then gives the workflow
handler a fresh `RunInput` for every initial or retry attempt. After success it
retains one private `RunOutput` and makes another copy for every wait, query, or
other caller-facing read. Shared normalization fixes portable search values and
the root request digest. Translating and submitting those values remains the
backend adapter's responsibility.

A storage-encoding failure returns `ErrPreparedRunRejected` without changing
the in-memory prepared request; it remains startable, although a caller that
requires durable admission must store it before starting. Malformed parsed
bytes or a request that no longer satisfies the current generated definition
also return `ErrPreparedRunRejected` and cannot start with that generated
release. Passing valid bytes to the wrong generated agent client returns the
same error, but does not invalidate the bytes; the application must submit them
through the matching client. The detailed caller contract is in
[External Input and Workflow Continuations](docs/runtime.md#external-input-and-workflow-continuations).
`ErrWorkflowStartFailed` preserves the exact accepted request. Goa-AI does not
retry it implicitly; the application chooses each attempt and submits the same
prepared value after an uncertain response. A workflow-start conflict remains
permanent because another request already owns the ID.

#### Deployment ownership

Goa-AI owns workflow replay, suspension persistence, continuation validation,
and exact call/result provenance. The consuming application owns release
routing for the runtime workers, generated packages, and callers that use those
contracts. They form one release unit: consumers regenerate every package from
the same Goa-AI revision and deploy the complete generated system together.
Before that deployment, they finish or cancel every workflow created by the old
runtime and resolve or abandon every start whose engine result was uncertain.
The new workers do not replay old active workflow histories or retry old
uncertain start requests.
Completed run history keeps the same meaning across this release. A host may
still need to convert the physical records or collections so its new
`storage.Store` can read them. That conversion must preserve each recorded
outcome and event.

The runtime requires one `storage.Store`. It exposes commands that name one
complete workflow state change instead of separate metadata and record writes.
`StartRootRun`, `StartChildRun`, `StartOneShotRun`, and
`StartOneShotChildRun` accept a concrete
`RunStart` after the engine accepts the workflow. Exact retries return the
stored outcome and record identifiers; changed identity returns
`session.ErrRunConflict`. `RecordRunCancellation`, `RecordRunSuspension`, and
`RecordRunTerminal` store the state change and matching ordered record together.
This is a source-breaking replacement for the former `session.Store` and
`runlog.Store` interfaces.

The consuming application owns session administration and the durable
repository. If that repository belongs to a separate Session service, agent
workers satisfy `storage.Store` through an adapter built on the service's
generated client. They do not open or share the Session service's database.
The complete worker-facing store contract is documented in
[Runtime Store](docs/runtime.md#runtime-store-storagestore).

Workflow start and session ending share one serialization point in the host's
store. Root and child starts create running metadata when the session is active.
If the host already ended the session, they instead create terminal canceled
metadata with reason `session_ended`; an exact retry never changes an existing
running run. The host atomically changes an active session to ended and cannot
restore a purged session ID. All processes that write runtime storage must use
the same contract; mixed old and new writers are unsupported.

The engine binds a workflow ID to the complete immutable start request while
the backend can still query that execution. Repeating that ID and request
returns a handle to the original open or closed execution. A changed request
returns `WorkflowStartConflictError`. Temporal forces duplicate starts to
return an error, then compares an engine-owned versioned digest stored in
workflow memo. The shared digest frames the caller-submitted workflow name, task
queue, input boundary payload, run timeout, retry policy, and every sorted memo
and search-attribute entry with its payload metadata and bytes. The in-memory
engine stores only that fixed-size digest and applies the same rule.
Every root and child request requires the engine workflow ID to equal
`RunInput.RunID`. A zero retry policy supplies no override. A non-zero policy
counts the first execution in `MaxAttempts` and may set retry timing only when
it also selects a positive attempt count or unlimited attempts.
Continue-as-new retains one workflow chain identity. Once backend history
expires, durable product command identity prevents reopening a settled
obligation; the engine does not add a second durable registry.

The accepted workflow owns the run history. It sends every durable change
through the single `runtime.store` activity. Each command sets exactly one of
`Append`, `RootStart`, `ChildStart`, `OneShotStart`, `OneShotChildStart`,
`Cancellation`, `Suspension`, or `Terminal`; the result sets the same field and
no other field.
The activity therefore cannot infer an operation from record contents or return
an unrelated result shape. Hosts return `storage.ContractError` for stored-state
conflicts and other permanent contract failures, so engines do not retry them.
Temporary database and network failures remain ordinary errors and can retry.

A root start always stores `RunStarted`. If the session has ended, it then
stores `RunCompleted` with canceled status and the workflow does no planner or
tool work. A child start always stores `ChildRunLinked` followed by
`RunStarted`; an ended session adds the canceled `RunCompleted` record. A
Temporal child is terminated if its parent workflow closes first. A
continued run puts the exact run ID whose
checkpoint it restored in `RunStarted.PredecessorRunID`; initial runs leave it
empty. The same value is part of `session.RunStart`. Before writing any part of
the successor start, the store requires the predecessor to exist, be suspended,
and have the same session, agent, and parent. `RunMeta` does not duplicate the
predecessor because the immutable start record owns that relationship. The
returned immutable `StartOutcome` tells the workflow whether it may begin
planner work. Prompt references are derived from `PromptRendered`, the
continuation predecessor in `RunStarted`, and `ChildRunLinked` rather than
duplicated in `RunMeta`. Cancellation, suspension, and terminal commands update
run metadata and store their matching record in one transaction. The hook bus
receives successfully stored records afterward and does not write lifecycle
state.

Prompt rendering has no storage side effect. When its context contains a
`prompt.RenderRecorder`, every successful render records the same resolved
prompt ID, version, and scope value. A caller that renders text before starting
a run puts those events in the immutable `RunInput` with
`WithRenderedPrompts`; the accepted workflow stores them after `RunStarted` and
before planner work. A planner activity returns its recorded events with the
accepted result. Consumer-side agent-tool rendering carries them in the child
input, and one-shot callbacks attach them to the one-shot run already created.
`RenderRecorder.Events` returns completed renders in stable prompt identity
order, so concurrent render completion cannot change workflow start identity.
Child rendering runs in an activity. Workflow history therefore contains the
exact rendered text and events, and replay never reads prompt storage again.
The activity returns exactly one of `Success` or `Failure`. Success contains
only messages and rendered prompt facts. Workflow code derives the child run,
session, parent, tool, and label identity from the original recorded tool call;
the activity cannot replace that identity.
These are transport differences only. Every event accepted into a proceeding
run produces the same `PromptRendered` record, and a failed render produces no
event.

The first root-start or child-link activity retries transient persistence
failures without an attempt ceiling. It classifies malformed typed input and
immutable event-key conflicts as non-retryable, so accepted work cannot reach
planning before the first record is durable and deterministic defects do not
loop. Child workflow IDs are single-use commands: Temporal uses
`REJECT_DUPLICATE`, the in-memory engine rejects every second explicit issue,
and Temporal replay remains execution of the original command. Temporal does
not return a child handle until it has accepted or rejected that child start;
the handle then represents completion only. The in-memory engine encodes and
decodes every successful root and child output through the same strict, bounded
converter used at the Temporal workflow boundary. The returned result is a
separate copy and an oversized or unserializable result fails in both engines.
A child link
stores the tool and call that created the child plus the child run and agent;
tool arguments, budgets, attempts, and repeated identity remain in workflow
input or dedicated event fields. A new child start is accepted only while its
parent is running. An exact retry of an already stored child start remains valid
after the parent stops.

Cancellation stores its first reason and matching record before engine
cancellation, retains it through every engine outcome, and never rolls it back.
Root, child, and one-shot runs share this contract. Active metadata paired with
`ErrWorkflowNotFound` is an invariant error. No metadata paired with the same
engine result is idempotent absence. A run that completed between a host's
active-run listing and its cancellation request also returns idempotent success.

Suspension and terminal writes retry until the store either accepts the exact
state change or returns a contract error that another retry cannot fix. Runtime
reads never repair lifecycle state. If engine history is already closed while
the stored run remains active, `Runtime.EnsureRunCompletion` is the explicit
command that retrieves the final engine result and writes the missing
suspension or terminal record through a repair-only store operation. If the run
is already closed, the command validates and redelivers the exact stored result.
The store atomically writes a missing result while the run is active or reports
the terminal state that a workflow stored first. The runtime publishes a
reconstructed event only when that event owns the stored record; otherwise it
reloads and redelivers the stored winner. Before delivering a child terminal
event, it validates the child's stored start and redelivers the exact parent
link. `Runtime.EnsureChildRunLink` exposes that exact link delivery separately
when a host must restore nested parent relationships before it replays final
results in their stored order. Active Sessions require `Runtime.WithStream` for
either ensure command. When the store response reports that this process
inserted the completion, the runtime sends it to the local hook bus once before
stream delivery. Retries redeliver only the exact stored stream events. Ended
Sessions retain their results and suppress stream delivery. The Session status
returned with the stored record decides the obligation, so an event accepted
while active remains due if the Session ends during a delivery retry. A
failure to retrieve the engine result is distinct from the workflow's
own final error and cannot be saved as the workflow outcome. The engine also
returns the stable time at which it closed the workflow. A recovered record
uses that time, so an exact retry cannot create a different timestamp. Every
accepted lifecycle timestamp uses millisecond precision because runtime records
carry time as integer milliseconds.

`RunOneShot` stores its start before invoking application code. It records
prompt events and the terminal result after the callback returns, even when the
callback canceled its context. Store retries reuse the prepared records and do
not run the callback again.

The recipe digest is also an all-writer cutover. Before deployment, stop new
admissions and prove that no unresolved pre-upgrade start obligation or active
workflow still requires attachment by exact ID. Deploy all workflow starters
together, then resume admission. A queryable Temporal execution without the
reserved recipe memo conflicts; the adapter never reconstructs or guesses its
start request.

Goa-AI does not provide a production database implementation or prescribe a
database migration procedure. The host owns the durable schema and any one-time
conversion needed to satisfy `storage.Store`. Before the new runtime writes,
the resulting data must support one transaction boundary for lifecycle state
and its matching run records, reject unresolved legacy states, and omit
metadata derived from records. Old split-store writers and new integrated-store
writers must not overlap. How the host reaches that state depends on its
database and deployment environment.

Continuation preparation accepts only the current suspension schema,
`goa-ai.run-suspension.v7`. Earlier versions are rejected because they cannot
preserve the exact identities and complete successful tool results required by
the current continuation contract. Every model-authored
await item preserves the runtime `ToolCallID` separately from the provider
`ModelToolCallID`, so execution records and provider transcript reconstruction
never substitute one identity for the other. Any other checkpoint shape fails
validation.

Coordinated generated-code deployment does not own ordinary service
availability. Services called by activities must keep a ready endpoint
throughout their own rollout. These responsibilities stay outside Goa-AI because the
consumer owns its process layout, traffic router, persistence, and downstream
services. The operational checklist is in
[docs/runtime.md](docs/runtime.md#coordinated-generated-system-releases).

The host application ends and purges sessions through its own administrative
API, not through `runtime.Runtime`. Ending a session prevents new root and child
work from proceeding but retains its runtime history. Before purging, the host
ends the session and settles every active run. Purge permanently reserves the
session ID before deleting its runs, checkpoints, and ordered records. Any later
write for that session must fail as purged, including an exact retry whose
original record was deleted. The integrated in-memory store provides these host
operations for local development and tests under the same mutex as workflow
writes.

Planner activities project the active deadline onto
`ScheduleToCloseTimeout`, which limits the complete queue/retry/backoff
lifetime. `ScheduleToStartTimeout` and `StartToCloseTimeout` retain their
separate queue-wait and attempt-failure semantics. Initial and resumed planning
receive the `TimeBudget` deadline; planner finalization alone receives the Hard
deadline. When initial or resumed planning exhausts the budget deadline, the
runtime performs one explicit finalization turn inside the reserved
`FinalizerGrace`. Engine adapters identify schedule-to-close expiration
explicitly, so the runtime never infers timeout ownership from workflow time.
Planner results that already completed are consumed at the Budget boundary:
terminal and await results remain valid, budgeted tool plans are rejected
before transcript commit, and runtime bookkeeping completes inside Hard.

Engines must never impose a second, competing wall-clock ceiling (for
example Temporal's `WorkflowRunTimeout`) on top of this. Unlike the
workflow's own deadline check, an engine-level timeout force-closes the run
from outside application code, so it can fire during active work without the
runtime emitting its canonical terminal event. `resolveRunTiming` therefore
never derives an engine run timeout from policy; engine start requests leave
that field unset.

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

Provider admission is owned by the clustered registry. `Serve` generates one
UUID incarnation per lifecycle; leases are keyed by stable provider ID plus
incarnation, so delayed old-process release cannot remove a replacement.
Membership epoch and pong freshness live beside those leases in the same CAS
record. The active `toolprovider.Serve` lifecycle owns an
immutable, required `AdmissionRevision`, opens the Pulse stream, invokes a typed
context-compliant registration callback that sends the runtime-owned
`toolregistry.WireProtocolVersion` with that revision, and creates the shared
sink only after admission. It renews with jitter while the last
duration-derived monotonic deadline remains valid and closes consumption before
expiration. The first renewal is derived from one third of the granted lease
duration; bounded retries preserve cutoff slack. After every admitted exit,
`Serve` stops renewal, atomically marks the original admitted
token/incarnation lease draining, then closes the canonical shared sink and
leaves unclaimed local work for redelivery. A changed token returned by renewal
is drained concurrently under the same shutdown deadline. Draining leases are
excluded from new publication and new claims, but retain authority to settle
claims that committed before draining.
An exact retry of one of those claim operations returns its original `execute`
decision. The drain transition carries the configured shutdown duration so
Redis keeps that authority for the full settlement lifecycle. `Serve` waits
workers and registry-owned terminal publication, drains every queued
acknowledgement, and releases each successfully settled exact lease. Failed
settlement suppresses release and returns the cleanup error. A sink-setup
failure has no consumption to settle and proceeds directly to bounded release.
Close, worker, result, or acknowledgement failure is explicit and suppresses
release; lease expiry is the durable fallback. Before a worker dispatches
locally queued work, `ClaimToolCall` authenticates its exact lease and request
event at the global call record. The atomic result is `execute`, `terminal`,
`claimed`, or `expired`; only `execute` invokes the handler. Dispatch ownership
never transfers. One claim-operation ID is reused by transport retries, while a
later event redelivery creates a new ID and receives `claimed`, so redelivery
cannot repeat side effects.
Claims enter global and exact-lease settlement indexes. At the call's absolute
execution deadline, or earlier if the lease is released, the registry
atomically commits `internal` / `outcome_unknown`, states that the effect may
have occurred, and forbids another execution. This happens before the later
call-record retention deadline. The same transition publishes stale-generation
terminal history before acknowledgement.

Provider processes supply one stable `ProviderID` per process/toolset pair and
one deployment-issued `AdmissionRevision` per fenced admission to `Serve`.
Provider registration wiring must also send the single
`runtime/toolregistry.WireProtocolVersion`; it is not deployment configuration
or capability negotiation. Consumers never register, but every `CallTool` and
`RetryTool` request must send the same version before the registry can publish
protocol bytes.
Every replica with the same contract shares the revision across scaling and
RollingUpdate; a revision changes only when a new execution fence is intended.
The registry rejects a missing or mismatched wire version with
`validation_error` before creating streams, mutating catalog state, admitting a
lease, or scheduling health checks. It stores active/retired state, toolset,
wire protocol version, canonical schema fingerprint, admission revision, token,
Redis `RegisteredAt`, every provider-incarnation
lease, health epoch, last pong, and the exact set of all retired registration
tokens in one exact-CAS catalog record. A nonzero-to-zero lease transition
advances the epoch and resets pong freshness. Ping IDs carry token plus epoch;
pongs authenticate that pair and the responding incarnation atomically.
Aggregate health and new-call routing require one unexpired, non-draining lease
plus a fresh current-epoch pong.

`CheckAdmission` applies that same routing test to one deployment-supplied
registration token. Generated toolset specs expose
`RegistrationToken(admissionRevision)`, which combines their precomputed schema
fingerprint with the runtime wire version and deployment revision through the
same pure implementation the registry uses. Providers include that generated
fingerprint in `Register`; the registry derives it again from the submitted
toolset and rejects any mismatch before creating routing state. The check
returns `ready=false`
when the toolset is absent, a different token is active, no routable lease
remains, or the current epoch lacks a fresh pong. Infrastructure failures remain
errors, while caller cancellation remains cancellation. This read contract lets
a deployment verify the exact routable tool contract after Kubernetes confirms
that the intended workload rollout completed. Workload readiness remains
independent of admission, so a changed-token rolling update cannot deadlock.

Registry construction enumerates every authoritative catalog key and applies
the same strict current-format parser used by registration and routing before
health tracking starts. Unknown fields or any other non-current record keep the
registry unready and report every affected key; startup never rewrites a stored
record.

The wire-visible `RegistrationToken` is not a secret. It is the lowercase
SHA-256 digest of the domain `goa-ai/tool-registry-admission/v2\0`, the uint32
big-endian wire protocol version, the raw 32-byte canonical schema fingerprint,
a uint32 big-endian admission-revision byte length, and the revision bytes. Tool
order and toolset/tool tag order are
normalized before schema fingerprinting. The model payload, registry execution
payload, result, and sidecar schema bytes are exact identity inputs, so
generated schema JSON must remain canonical.
The same wire version, schema, and revision derive the same admission token
across replicas. Changing any of those inputs derives a different token. Every
Goa and Pulse boundary requires the canonical lowercase 64-hex spelling
`^[0-9a-f]{64}$`.

Registration is one exact-CAS state machine. An absent record becomes an active
candidate plus its first lease. The exact token adds or renews one provider.
A different token first prunes expired leases using Redis TIME: while any old
lease remains it returns retryable `admission_blocked`; once none remain it
replaces the record with the candidate and first lease while atomically
tombstoning the prior token. Concurrent renewal, replacement, and competing
candidates serialize on that CAS. Retirement also atomically tombstones the
current token. Any candidate in the tombstone set returns permanent
`admission_retired`; therefore A→B→A cannot resurrect A. Another fresh token may
activate after retired leases are released or expire. Tombstones are permanent,
unbounded correctness history. They grow with distinct admissions; no lossy
compaction is safe without another immutable authority that can prove
non-resurrection.

Same-token scaling and RollingUpdate require no deployment token persistence.
A different schema or admission revision may use the same RollingUpdate: the
new pod stays blocked at registration while the old admission drains, so
incompatible providers never execute concurrently. A new call that observes no
healthy provider waits until the replacement becomes healthy or the existing
execution deadline expires. Request publication atomically checks that the
selected provider is still routable. If draining won that race, the
unpublished call selects the replacement and tries publication again; only a
published call has an immutable provider assignment.
Graceful releases permit immediate server-owned handoff, while crashes delay
handoff until lease expiry. Registry servers and consumers use one exact wire
protocol; they do not negotiate envelope versions.
`Unregister` is not rollout orchestration; it intentionally changes active to
retired while preserving leases. Same-token retirement retry succeeds, a stale
expected token returns `admission_conflict`, retired toolsets are unavailable to
discovery and calls, and the retired exact token cannot register again.

The wire protocol has no optional fallback, capability negotiation, or dual
decoder. The registry rejects a missing or mismatched version before service
invocation, catalog lookup, health checks, result-stream creation, call
admission, or Pulse publication. Register and renewal apply the same check
before provider admission. The runtime-owned version therefore fences both
producers of protocol bytes, while the version-bound registration token
preserves the provider-generation fence.

Supported rmap `Destroy`, stream destruction, and ticker loss recover live under
the same registry name without a process restart: renewal reconstructs the
catalog lease, the stream recreates, and ticker reconciliation restores pings.
A raw total Redis reset is different because it erases rmap revision history and
the permanent retirement tombstones. Deterministic identity still derives the
same token, but live recovery and anti-resurrection cannot be claimed without a
restored catalog backup. Operators must stop incompatible admissions and restore
or deliberately rebootstrap state before resuming providers.
The gateway stamps each routed call with the exact active token used for
validation. It derives the global transport `ToolUseID` as a domain-separated,
uint64-length-delimited SHA-256 hash of required `run_id` plus model/provider
`tool_call_id`; retries in one run therefore reuse the result stream while
concurrent runs cannot collide. Both IDs are required; direct callers generate
and retain stable call identity. The original model/provider ID remains separate
metadata.

Providers execute only matching calls. For a stale queued call, the current
provider asks the registry to reject the exact call; the registry authenticates
that provider's distinct current token/incarnation lease and publishes the only
valid terminal `stale_registration` result under the call's older token. The
provider then acknowledges the request, so generation changes cannot execute old
work or strand a waiting caller. Every
success and error result echoes the call token; independent executor readers
accept only the exact tool-use ID and token pair and ignore mismatched late
results from reused IDs. Ordinary completion verifies the completing provider's
exact token/incarnation lease, including a preserved lease after retirement, and
atomically stores the full canonical result in terminal call state while
appending its delivery event. Result streams retain at most
`ResultStreamMaxLen` events through the absolute retention deadline; a terminal
trimmed from Pulse is restored from the call record on replay. Output deltas and
completion require the exact immutable dispatch owner and stop at the earlier
execution deadline. Delta bytes and count are bounded. Overload notices are
idempotent per request event and accepted only before dispatch; stale overload
observations become the canonical stale terminal atomically. Preserved retired
or draining exact leases may
settle calls they already claimed. The waiting queue is exact and bounded. A provider that claims
a call after that queue fills reports transient retry intent with reason
`provider_overloaded` before acknowledging it. Retry control has
no planner failure; terminal `ToolError` is the single authority for planner
classification and recovery. The executor submits `RetryTool` with the original
admission token. The registry requires that exact admission to remain active,
attaches to its existing immutable call admission, and atomically republishes at
most once per retained overload event across replicas. It never creates retry
state or republishes through a replacement provider. Provider ping intake remains
available while the retry waits.

The registry atomically records each global `ToolUseID` once. The authoritative
record stores a token-independent digest of toolset, tool, payload, and call
metadata. Before request publication, its provider token may move to the
current healthy registration because no provider can have started the call.
Publication atomically verifies that token and makes the positive assignment
immutable. `CallTool` attaches to this record before current catalog or health
lookup; an exact retry of a published call therefore returns its original token
and deadlines after retirement or replacement. A valid unpublished call that
finds no healthy provider waits and subtracts the wait from its original
execution budget. Provider recovery publishes the call normally; deadline
expiry atomically replaces the unpublished state with the rejected decision.
Other catalog and health failures commit the rejected state or observe an
admission that won the race. The registry does not infer whether a no-provider
interval came from a deployment or an outage. A rejected state returns typed
`call_not_admitted` and prevents every exact retry from executing while that
run-scoped decision is retained, so the executor may safely replan. Generated
`not_found` and `validation_error` preserve their actionable types in the same
decision record. Errors after request publication remain ambiguous and produce
`outcome_unknown`, which forbids replacement execution because an effect may
have occurred.
The explicit decision record is wire protocol version 10. Registry replicas,
providers, and consumers use that exact protocol. Records with another shape
are rejected and never rewritten.
Protocol 8 or 9 catalog entries must be removed before protocol 10 can start.
Only the per-toolset fields in the
registry's catalog hash are removed; drained call records and Pulse streams
remain until their normal expiry so an old call cannot execute twice. The
executable procedure is documented in the
[preview upgrade guide](docs/runtime.md#preview-upgrade-guide).
`CallTool` owns only initial admission and publication. `RetryTool` owns overload
republication and requires both the
existing admission record and its still-active token. The request-stream append
and admission marker commit atomically, so ownership cannot expire between
publication and commit and exact retries return the original request event. The
call record owns a Redis-selected absolute execution deadline no longer than
`MaxToolCallWait` and a later bounded retention expiration shared with result
history. Provider handler context and executor waiting use the execution
deadline. Structural reference validation checks only timestamp shape and
ordering; Redis owns liveness. The record stores the complete canonical
terminal payload. Atomic terminal publication preserves the later retention
expiration; replay restores a missing terminal delivery event without
re-executing the call. Claimed calls that reach execution deadline without a
provider terminal atomically become `outcome_unknown` while the authoritative
record is still retained. Redis expiry deletes only after that settlement
window.
The request stream is trimmed by the minimum safe consumer-group watermark:
the earliest pending ID for groups with pending work, otherwise each group's
last-delivered ID. Length-based trimming is forbidden because a single old PEL
entry can outlive arbitrarily many acknowledged later entries.
Retry decisions use the overload event retained in the call record and never
snapshot Pulse history. Executors use independent oldest-first Pulse Readers,
so each waiter replays retained events
without consumer groups, acknowledgements, or keepalive metadata. No consumer
destroys the stream; abandoned, completed, and recreated state expires at the
record's absolute deadline.

Deterministic admission identity, catalog-owned leases, token fencing, flat
shared streams, server-owned handoff, and incompatible-admission non-overlap
are permanent. Catalog and queued records must match the current wire protocol;
the runtime rejects unknown records instead of guessing how to translate them.

Health tracking and provider registration self-heal after Redis state loss.
Ping scheduling uses expiring per-toolset Redis leases that the next scheduler
tick re-acquires, the registry repairs the catalog map's replicated revision
counter so post-loss writes propagate to surviving nodes, and
`toolprovider.Serve` periodically recreates its consumer group (see
`Options.EnsureInterval`) while the required `Registration` supervision loop
re-registers on every lease renewal, restoring the catalog entry without
redeploys.

### Transcript Boundary

- **Stateless model adapters**: Provider clients accept the full provider-ready
  transcript in `model.Request.Messages`; they never reload history from a
  runtime-owned `RunID`.
- **Durable replay**: The runtime persists canonical transcript deltas as
  runlog records so providers, replay tooling, and future backends can
  reconstruct the exact message order generically. Canonical tool-call IDs
  remain opaque and unchanged; adapters whose wire protocol restricts ID syntax
  assign request-local aliases and use the same alias for each matching tool
  result. Provider calls must contain an ID. Planner requests preserve that ID
  when they forward a provider call and leave it empty when the planner authored
  the request. The runtime assigns every accepted request its deterministic
  execution ID from the run, turn, attempt, tool, and batch index.
- **Rejected model evidence**: The model boundary hashes a versioned,
  deterministic encoding of complete responses before copying or validating
  them. The encoding includes malformed raw tool bytes without requiring valid
  JSON. The activity result and durable rejection event carry the
  complete-response presence, SHA-256 digest, byte count, and encoding version
  when a digest exists.
  Each version explicitly orders response fields, message-part variants, and
  metadata struct fields rather than depending on Go declaration order. Struct
  field tags and anonymous-field status are part of the encoding.
  Canonical metadata and tool-result values may contain nil, booleans, finite
  numbers, strings, string-keyed maps, slices, and arrays. Fingerprinting and
  rejected-response evidence copying also encode structs with exported fields
  so an invalid provider response still has a stable identity. Canonical copying
  rejects structs, pointers, functions, channels, unsafe pointers, complex
  numbers, uintptr values, reference cycles, more than 64 nesting levels, or
  more than 100,000 visited values in one metadata object or tool-result value.
  Strings, byte slices, and map keys in one such value may total at most 16 MiB.
  Fingerprints are captured before ownership cloning so invalid metadata or
  parts cannot be mistaken for no response. If
  concurrent calls reject output, the envelope uses the earliest-started
  rejected invocation's reason and complete-response evidence. The
  event fingerprints the private validation-cause text instead of retaining
  that text. A mechanical rejection also carries the closed
  `OutputValidationKind` from the first check that rejected the response. The
  kind identifies only the failed contract area; it contains no response text,
  provider text, tool identity, arguments, or schema path and cannot authorize
  recovery. Model content remains observability data rather than workflow
  state, so diagnostic storage cannot retry inference and Temporal and hook
  payloads remain bounded. Planner-authored policy rejection is semantic: the
  same response can be accepted by one planner and rejected by another. It
  therefore carries no mechanical kind and keeps its exact planner correction
  as the only recovery fact. A planner result rejected after model output was
  accepted emits `PlannerOutputRejected` instead, with only the bounded private
  cause identity.
- **Generated tool validation**: A model definition created from a generated
  tool specification retains its advertised schema and generated payload
  decoder inside the process.
  Unary tool calls and streamed tool calls must match a definition in the exact
  model request. A completed streamed call remains withheld until terminal
  usage arrives and the complete response matches every retained chunk. The
  originating schema then validates each call before its decoder runs and
  before planner code can observe it. Only schema rejections and typed
  tool-input validation errors can carry final usage into bounded replacement
  planning without executing the rejected call. Ordinary decoder failures and
  incomplete or inconsistent streams remain terminal. The activity validates
  the selected planner request again before scheduling effects.
- **Planner-transparent provenance**: Each model call produces an isolated
  canonical response and ordered live output before planner code observes
  completion. One opaque validated-stream value owns validated chunks, the
  validated response, and event scoping. It is
  bound to the immutable output-validation contract copied when validation
  begins and cannot be reused under different model identity, structured
  output, tool, or generated-validator checks. Message and sampling changes do
  not alter that contract. One receive operation includes provider access,
  validation, and every observer callback; close waits for that complete
  sequence and returns one stable result to every caller. Observer callbacks
  may inspect the current response but must not call receive or close on the
  same stream. The validated stream never exposes the provider stream. Each framework-owned
  message carries a private
  in-memory origin copied with it, so two messages with identical content remain
  distinct without comparing visible text or metadata. The runtime identifies
  tool turns from unchanged model-facing calls and terminal turns from the
  canonical provider message returned by its response helpers. Only the
  designated planner client publishes live text, thinking, and tool-argument
  deltas, while usage accounts for every invocation, including valid numeric
  counts from a rejected usage chunk. It commits the complete selected response
  once after atomic admission and before effects. Planners never manage
  transcript handles or provider replay metadata, and uncertain ownership fails
  instead of selecting by call order or visible text.
- **History compression**: Agent designs may declare compression defaults with
  `CompressAtTurns`, `CompressAtMaxInputTokens`, `KeepMaxTurns`, and
  `KeepMaxInputTokens`. The runtime evaluates token budgets with the configured
  model client's exact `model.TokenCounter`, so tokenization stays
  deployment/model-specific while the design records the agent's default policy.
  Each history-policy count includes its preserved system messages, candidate
  complete turns, and advertised tools. Thinking and structured output remain
  planner decisions made after the history policy runs, so they are not part of
  this threshold. The selected adapter counts this exact history shape using
  its provider tokenizer. A gateway
  preserves exact counting only when its transport supplies the separate
  count operation through `NewCountingRemoteClient`; otherwise counting
  returns `model.ErrTokenCountingUnsupported`.
  The Bedrock Converse adapter routes models supported by Runtime
  `CountTokens` there. For Claude Opus 4.7, Opus 4.8, Opus 5, Sonnet 5, and
  Mythos 5, it delegates the resolved Bedrock-effective request to an optional
  Mantle counter. Without that configured counter it returns
  `model.ErrTokenCountingUnsupported`. Mantle cannot represent Bedrock
  structured output. Claude 4.6 instead uses the same strict private tool for
  Runtime counting and Converse.
  The Claude-on-Bedrock Messages adapter requires an exact Anthropic counter.
  It passes supported native structured output to the counter after replacing
  a cross-region inference-profile model ID with its foundation model ID.
  Unsupported model and transport combinations fail before inference or
  counting, so the adapter never counts a weaker representation.
  When encoded tools carry authored `input_examples`, completion, streaming,
  and counting all attach the same Anthropic tool-examples beta header.
  Exact retention always keeps whole recent turns; it never truncates
  tool_use/tool_result pairs to satisfy a token budget.
- **Bookkeeping control plane**: `Bookkeeping()` calls and results remain in the
  provider transcript so signed model-authored parts replay without modification.
  They do not consume the tool-call budget. Successful bookkeeping results do
  not reset recovery turns, are omitted only from compact `ToolOutputs`, and
  do not force another planner turn. Every failed bookkeeping result enters
  the recovery transition: `correct_call` and `replan` may repair through tools,
  while `finish` enters finalization so the planner can synthesize the terminal
  outcome or invoke a terminal bookkeeping action. A successful
  bookkeeping-only turn must otherwise resolve in the same turn via a terminal
  outcome or an external-input suspension.
- **Forced finalization control plane**: when runtime caps or deadlines force
  finalization, planners may return terminal bookkeeping tools instead of a
  prose final answer. The runtime executes only `TerminalRun()` tools in that
  path (`TerminalRun()` implies bookkeeping), keeps them inside the remaining
  hard-deadline window, and closes the run only if every terminal side effect
  succeeds. A rejected finalizer output or terminal-tool `correct_call` failure
  consumes the existing recovery-turn budget while retaining the same
  finalization reason and restricted tool catalog. Other terminal failures end
  the run. Recovery call IDs extend the planner activity payload and select the
  canonical failed outputs that shape both reminders and the advertised catalog.
  Empty recovery IDs are omitted from ordinary activity payloads. Runtime
  workers, generated packages, and callers use one generated contract and
  deploy as one release unit. Mixed contracts are unsupported.
  Current run policy still applies while the fixed terminal call is prepared.
  For example, `WithRestrictToTool` can reject that call because the
  restriction applies to every tool in the run.
- **Run-scoped completion operations**: callers use
  `WithRunCompletionTool(tool_name)` when one successful budgeted tool call is
  the operation's complete outcome. The serialized run policy survives
  suspension and continuation. The completion tool must belong to the executing
  agent, be non-bookkeeping and non-terminal, and be allowed by the run's static
  tool policy. A planner response containing that tool cannot contain another
  call or an await request. The run cannot request post-tool synthesis because
  its required next terminal answer cannot satisfy the completion policy. A
  successful result closes the run without another planner turn; correctable
  failures retain ordinary recovery and cap accounting. A planner terminal response,
  forced-finalization request, exhausted cap, or deadline ends the run with an
  error instead of fabricating success after the required side effect did not
  occur. `CompletionTool` and `LimitTerminalPlans` are mutually exclusive
  because they assign different outcomes to the same exhausted limits.
  Completion-aware suspensions use `goa-ai.run-suspension.v7`. The saved policy
  is required, and a checkpoint with another version fails at that typed
  boundary.
- **Visible reasoning contract**: when a caller enables thinking for a Bedrock
  adaptive Claude model, the adapter asks for summarized reasoning display
  explicitly so streamed `thinking` events contain text. This includes Claude
  Sonnet 5, whose always-on thinking otherwise returns only an opaque signature,
  and later adaptive model revisions with the same omitted-display default.

## MCP Server Definition

Enable MCP protocol for a service with `MCP`:

```go
Service("calculator", func() {
    MCP("calc", "1.0.0", ProtocolVersion("2025-06-18"))
    JSONRPC(func() {
        POST("/mcp")
    })
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
- ErrorMapper: `func(error) error` replaces an authored-service error before
  the adapter returns it as tool error content or a resource-read JSON-RPC
  error.

## Transport

Generated MCP tools and resources are unary JSON-RPC methods. The HTTP caller
accepts responses encoded as JSON or as an HTTP event stream. Goa-AI does not
generate MCP subscriptions, notifications, or streaming resources.

The preview removes the former MCP subscription and notification APIs. See
[the preview upgrade guide](docs/runtime.md#preview-upgrade-guide) for the
complete list and the replacement for each supported use case.

## Agent run lifecycle streaming contract

The runtime emits one terminal lifecycle event per workflow: `RunCompleted` for
success, failure, or cancellation, and `RunSuspended` when external input is
required. The stream subscriber translates both into a `workflow` stream event
(`stream.WorkflowPayload`) followed by `run_stream_end`, so UIs and other stream
consumers know exactly when to stop reading.

- **Terminal status**
  - `status="success"` → `phase="completed"`
  - `status="failed"` → `phase="failed"`
  - `status="canceled"` → `phase="canceled"`
  - `status="suspended"` → `phase="suspended"`

- **Cancellation is not an error**
  - For `status="canceled"`, the stream payload **must not** include a user-facing `error`.
  - Consumers should treat cancellation as a terminal, non-error end state.
  - Cancellation from a service activity, inline tool, or agent child cancels
    the owning run and does not synthesize a failed `ToolResult`.
  - Engine adapters normalize backend cancellation to `context.Canceled` while
    runtime hooks are recorded, then restore the backend's cancellation type at
    the workflow boundary. Temporal therefore records the execution as canceled
    rather than failed.

- **Failures are structured**
  - For `status="failed"`, the stream payload includes:
    - `error_kind`: stable classifier for UX/decisioning (provider kinds like `rate_limited`, `unavailable`, or runtime kinds like `timeout`/`internal`)
    - `retryable`: whether retrying may succeed without changing input
    - `error`: **user-safe** message suitable for direct display
    - `debug_error`: raw error string for logs/diagnostics (not for UI)
  - Tool-execution events carry a `ToolFailure` with an independent failure kind
    and recovery action. `correct_call` keeps the failed tool available and
    supplies structured correction evidence without requiring a retry,
    `replan` removes the failed tool from the next turn's caller-allowed
    catalog, and `finish` is terminal for tool execution. A same-tool
    `correct_call` keeps that tool available alongside a parallel `replan`.
  - Planner activities preserve `ProviderError.Retryable()` in Temporal
    application errors. Invalid requests and authentication failures therefore
    stop immediately, while throttling and transient provider failures retain
    activity retries.

- **Terminal identity**
  - `RunCompletedEvent.Labels` carries the run-scoped labels provided at run
    start (`RunInput.Labels`, nil when the run had none) so completion
    subscribers can attribute that outcome without tracking run identity out
    of band. Suspended runs and polling readers obtain the same labels from the
    durable `RunStarted` record through `run.Snapshot.Labels`.

This keeps consumers simple: render `error`, gate “Retry” on `retryable`, and treat `canceled` as non-error.

## Provider Stream Integrity Contract

Provider adapters (Bedrock, Anthropic) validate the streaming event protocol
with a strict state machine: a message must start before content blocks flow
and must stop exactly once before metadata. A provider terminal event that
violates this order is rejected as `OutputValidationStreamProtocol`. Transport
failures and caller cancellation remain outside `OutputValidationError`.
Two event-source termination shapes retain provider-failure classifications:

- **Empty event source** — the event source closes before any message starts.
  Adapters build the error with `model.NewEmptyStreamError`, which carries the
  `model.ErrEmptyStream` sentinel plus a retryable `unavailable` ProviderError
  (code `empty_stream`). Callers detect it with
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

Planner model-call spans also record `goa_ai.request.tool_count` and
`goa_ai.request.tool_names`, which expose the exact names advertised to that
request without recording arguments or labels. Every registry replica emits a
`toolregistry.catalog.entry` span for each active toolset returned by the
shared catalog; retired records are not reported. The replicas then compete to
send the provider ping, and the winner emits one `toolregistry.health` sample
for that toolset and interval. Each replica also emits a
`toolregistry.health.sweep` span for every scheduler attempt. An application
may configure `Registry.Config.ExpectedToolsets`; after a successful catalog
read, every replica emits `toolregistry.catalog.expectation` with an exact
present or absent result for each required name. Failed reads emit no presence
result. Applications that alert on these spans must keep the scheduler root
trace in their sampling policy. The full catalog, readiness, and error contract is
documented in
[Registry and model-request traces](docs/runtime.md#registry-and-model-request-traces).

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
- Temporal engines always construct their client from `ClientOptions` and
  install the Goa-AI data converter. No supported constructor can bypass exact
  number decoding, unknown-field rejection, unsafe-value checks, or the total
  1 MiB limit for one workflow or activity call. Tool executors persist larger
  domain results first and return their durable reference.
- Post-start fatal worker failures surface through the configured
  `worker.Options.OnFatalError` callback instead of being silently ignored.
  Integrating services should treat that callback as process-fatal and exit.

## Tool Input Schema

Every model-visible tool input is an object. Designs may use an inline object or
an object-shaped user type; omitting `Args` on an
unbound tool means `{}`, while omitting it on a tool with `BindTo` uses the
bound method payload, which must follow the same object-shape rule. Primitive,
array, map, and `OneOf` roots are rejected, while `Return` remains
unrestricted. Generated objects and unions reject undeclared properties, maps
accept dynamic keys, and the validated model client applies the advertised
schema before any attached input decoder. Only schema rejections and typed
tool-input validation errors qualify for limited-size correction guidance that
omits rejected arguments. Code generation records field types through nested
objects, collections, and union branches. Callers that build `ToolSpec` values
directly may supply the same field metadata. The model client uses that metadata
to name one field and its required, type, or enum rule when the structured
schema failure has one unique deepest cause. For unions, only the branch named
by a valid string discriminator participates. Array indexes and map keys appear
as `*`. Ambiguous failures and specifications without field metadata keep
generic guidance. The complete contract lives in
[Model-Visible Tool Arguments](docs/runtime.md#model-visible-tool-arguments).

Fields marked with `Inject` are absent from the model-visible input and filled
from call metadata or run labels before the tool executes. They must be Goa
`String` fields or named Goa `String` types; custom Go field replacements are
rejected. The generated injection function applies the field's Goa validation
to either source before assigning the value. The complete contract lives in
[Injected Fields](docs/runtime.md#injected-fields-inject).

### Pagination Ownership

Pagination metadata names both the result cursor and, when appropriate, a
dedicated continuation tool. `ContinueWith` is the canonical contract when an
opaque cursor already contains the resolved query: the originating tool has no
cursor mode, the continuation tool accepts one required cursor, and generated
runtime metadata routes the next page. `Cursor` on the originating tool remains
available only for APIs where callers must intentionally repeat the original
query. This keeps correlation state in the issuing system instead of asking a
model to reproduce it. Parallel source invocations are valid. When a live page
returns zero items and a next cursor, the runtime advances that chain before
planning because no semantic evidence exists for the model to judge. Once a
page returns items with a next cursor, the runtime derives one temporary
no-argument action for that live source invocation. A truncated result without
a cursor exposes its refinement hint instead. Each action has a stable
model-facing name and describes the original model-visible query, while execution retains the
canonical continuation tool name. The bounded-result reminder names that same
temporary action, never the hidden canonical tool, so the prompt and advertised
catalog cannot disagree. Selecting an action binds the exact source
tool-call identity and cursor into the executable request. Successful
continuation results carry that source identity through the durable tool-call
record, so repeated equal queries, equal opaque cursors, sequential pages, and
parallel batches remain independent. A continuation that returns its input
cursor violates the paging contract and fails immediately. The model chooses
which semantic query with returned evidence to continue but never reproduces a
cursor or correlation identifier.

A new run reconstructs still-live continuation actions from structured
transcript tool-call IDs and canonical scheduled/result events in the session
run log. It never trusts a cursor copied through the transcript and never
selects the latest result heuristically. Completed chains remain absent, while
multiple unfinished chains retain distinct action names derived from their
source call identities. The configured runtime store supplies this history
through `ListSessionRunRecords`. Tool names matching `continue_`
followed by exactly 24
lowercase hexadecimal characters are reserved for runtime-generated
continuation tools. Agent and toolset registration reject that exact format;
similar names such as `continue_search` and qualified canonical tools such as
`tools.continue_search` remain valid.

For each tool, the plugin derives JSON Schema from its effective Goa input
using Goa's `openapi.Schema` type for complete JSON Schema draft 2020-12
support. The generated tool spec is the canonical model-facing contract:
it contains the annotated schema, a schema with the root `example` removed,
the raw authored example JSON, and a parsed `ExampleInput` object when the
payload has an authored top-level Goa `Example(...)`.

Authored Goa `Example(...)` values are the only source for provider-facing tool
examples. Codegen removes synthesized Goa examples throughout the JSON Schema
graph, including fields and definitions, so generated placeholder data never
becomes model guidance. Explicitly authored nested examples remain on their
schema nodes. The annotated schema retains the authored root example; the plain
schema omits that root for providers that carry it through a native
tool-examples field.

Provider adapters choose between the precomputed projections. Providers that
consume JSON Schema annotations use the annotated schema. Anthropic Messages
uses top-level `input_examples` with the schema that omits the root example for
the direct Anthropic API. The adapter attaches the corresponding beta header
(additively, preserving caller-configured betas) to completion, streaming, and
token-count requests. The header is attached only when a tool carries authored
examples because gateways may reject beta identifiers they do not recognize.

Amazon Bedrock's native Messages endpoint has a narrower tool object. Live
Sonnet 5 and Opus 5 requests reject both `input_examples` and the `strict`
property, including `strict:false`, but accept the authored root `example`
inside `input_schema` for ordinary tools on fresh and resumed turns.
`bedrock.NewAnthropic` therefore retains the annotated schema and omits
`input_examples` for ordinary tools. This does not make those tools a valid
replacement for `StructuredOutput`; such requests fail before inference.

The Converse adapter can carry the same examples through Anthropic's
provider-native request fields in `additionalModelRequestFields` only on turns
where Bedrock does not require a competing `ToolConfig` declaration. Applications
that require examples and tools to remain identical across Claude turns use the
Messages transport instead of relying on that split representation.
Claude-on-Vertex serves the same native contract: it delivers `input_examples`
with no beta activation and ignores the header (live-verified via rawPredict
`usage.input_tokens`). Runtime and product code do not inspect or rewrite
schemas to infer provider-specific shapes.

Any proxy or product-owned model boundary that reconstructs goa-ai model tools
must carry these projections as one provider-neutral `model.ToolInputContract`.
The boundary should not import generator-only `tools.TypeSpec`, re-marshal
decoded schemas back into generated bytes, or know which provider consumes which
projection. Dropping the example fields before a provider adapter runs prevents
direct Anthropic from sending `input_examples` and Bedrock Messages from
retaining the schema `example`, even though the generated tool spec still
contained the authored example.

## Tool Identification

Tools are identified by canonical IDs in the format `<toolset>.<tool>` (dot-separated). The generated code produces typed constants (e.g., `MyTool tools.Ident`) matching this format.

## Agents Quickstart & Example Scaffold

A contextual quickstart file `AGENTS_QUICKSTART.md` is emitted at the module root on `goa gen`, summarizing what was generated and how to wire it. To opt out, invoke `DisableAgentDocs()` inside your API DSL.

The `goa example` phase generates application-owned scaffold under `internal/agents/`:

- `internal/agents/bootstrap/bootstrap.go`: constructs a minimal runtime and registers generated agents
- `internal/agents/<agent>/planner/planner.go`: planner stub implementing `PlanStart`/`PlanResume`
- `internal/agents/<agent>/toolsets/<toolset>/adapter.go`: stubs for mapping method-backed tools

## Security considerations

- Registry authentication: use Goa security schemes (`APIKeySecurity`, `OAuth2Security`, etc.)
- Logging: avoid logging sensitive payloads and results in production

## Error code mapping

When an authored Goa method exposed as an MCP tool fails, the adapter returns a
successful JSON-RPC `tools/call` response whose MCP result contains text error
content and sets `isError` to `true`. Errors that prevent the adapter from
running a valid MCP operation use JSON-RPC errors instead.

Malformed JSON maps to `-32700`, an invalid JSON-RPC request to `-32600`, and an
unknown JSON-RPC method to `-32601`. Request decoding and adapter errors declared
as `invalid_params` map to `-32602`; adapter errors declared as `internal_error`,
plus every undeclared endpoint error, map to `-32603`. An unknown tool name,
resource URI, or prompt name is an invalid argument to an existing MCP method
and therefore maps to `-32602`, not `-32601`. JSON-RPC notifications do not
receive error responses.

## Contributing

- Add agent concepts in `expr/agent/` and update the expression builders
- Add MCP concepts in `expr/mcp.go` and update the MCP expression builder
- Add registry concepts in `expr/agent/registry.go`
- Keep new templates small and transport-agnostic; compose on Goa JSON-RPC outputs

## Summary

This plugin gives you agents, MCP, and registries with familiar Goa patterns, minimal surface area, and a directory layout that feels natural. It's accurate, easy to maintain, and designed to evolve alongside Goa.
