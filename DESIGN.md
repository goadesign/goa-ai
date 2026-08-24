# Goa-AI: Design-First Agentic Systems

Build intelligent agents, MCP servers, and registry-integrated toolsets from your Goa designs. This plugin extends Goa with agent orchestration, MCP protocol support and centralized registries.

## What you get

- **Agents**: Durable plan/execute loops with policy enforcement, memory, and streaming
- **Typed Completions**: Service-owned structured assistant-output contracts with generated codecs and helpers
- **Generated Evaluations**: Design-owned scenarios with typed application hooks, bounded execution, and trustworthy semantic judging
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

- private completion specs containing the result schema and generated codec
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
Adapters with provider-native structured-output examples receive the generated
root example separately from the schema. Unary helpers ask the model once for a
structured value. If the generated codec rejects the response, the helper
returns a non-retryable `planner.OutputContractError` and does not ask the model
again. `completion.Response.ModelResponse` contains that exact model response
and its token usage. Streaming helpers follow the same one-request rule. Provider
adapters may suppress previews when their wire representation contains private
framing that is absent from the completion contract.

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

When a completed model reply or planner result breaks its required shape, the
planner returns `OutputContractError`. The runtime validates the full result
before publishing any selected model text or tool call, and Temporal records
the failure as non-retryable even when it crosses an activity or child-workflow
boundary. The parent does not ask the model to repair that reply.

The runtime keeps execution policy and planner intent separate:

| Completed step | Next state |
| --- | --- |
| The configured `CompletionTool` executed successfully | End the run without another planner turn |
| `CompletionTool` is configured and another terminal condition occurs | Fail because the required tool did not succeed |
| A cap or deadline requires finalization and the run supplied `LimitTerminalPlans` | Execute the matching terminal call without loading saved messages |
| A cap or deadline requires finalization without either completion policy | `PlanResumeInput.Finalize` |
| A successful `TerminalRun` tool completed without `CompletionTool` | End the run without another planner turn |
| Any failed tool requires `finish` recovery without `CompletionTool` | `PlanResumeInput.Finalize` with reason `tool_failure` |
| Any failed tool has a `ToolFailure` whose recovery action permits tools | Runtime-enforced correction or replan turn |
| A successful batch has `SynthesizeAfterTools` set without `CompletionTool` | `PlanResumeInput.SynthesisOnly` |
| Otherwise | Normal continuation turn |

`LimitTerminalPlans` is one optional run-policy value containing exactly three
payload-only calls: time budget, tool-call cap, and consecutive failed-call cap.
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

`runtime.LimitReasonLabel` and `goa-ai.limit_reason` were removed. Consumers of
fixed-limit and planner-authored finalization calls, including `tool_failure`,
must use `runtime.FinalizationReasonLabel` and
`goa-ai.finalization_reason`.
The additional workflow-input field requires a pinned Temporal Worker
Deployment cutover: old and new strict decoders cannot share one unversioned
task queue during rollout.

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
rather than a raw model request. `finish` forbids more domain
work and enters finalization. The finalizer may return a final response or
registered terminal bookkeeping calls, such as committing a Task report. When
the same tool has both correction and replan failures in one batch, the
correctable failure keeps that tool available. A recovery turn may end with an
input suspension; its evidence remains available when a new workflow continues
after the answer.
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
an opaque private checkpoint. Before returning that result, the runtime stores
the complete suspension beside its run metadata in the configured session
store. Exact activity retries are idempotent; a different suspension for the
same run is rejected as corruption. The application exposes only the public
request to clients and must atomically accept one response before it starts the
next workflow, so concurrent submissions cannot continue the same state twice
under different run IDs. A continuation supplies the completed run ID and
exactly one typed response for the first request; the runtime loads its own
checkpoint. The application may also supply engine options for the new
workflow, such as memo and search attributes used for ownership and
observability. These options do not alter the saved execution contract. If
requests remain, that workflow stores and returns a new suspension. The
checkpoint restores the transcript, planner state, labels, policy,
nested-agent identity, and exact tool-call/result provenance. Required tool
names are recorded, and
`Runtime.ValidateContinuation` rejects a checkpoint when the new worker does
not register one of them. Restoration passes every concrete saved payload and
result through the current generated codec. A value the current contract cannot
decode fails at that typed boundary; the runtime does not preserve continuation
compatibility across releases. A tool call created in an earlier workflow
retains that workflow's run ID while its result records the new workflow's run
ID. The tool-result hook and `tool_end` stream payload carry the original call
run ID explicitly; the result event's own run ID identifies the workflow that
received the external answer. When a nested agent suspends, the parent ends
with the same request; continuing the parent starts a new child workflow from
the child's saved checkpoint. Sessionless one-shot runs reject external-input
requests because they have no continuation API.

#### Deployment ownership

Goa-AI owns workflow replay, suspension persistence, continuation validation,
and exact call/result provenance. The consuming application owns release
routing for the runtime workers, generated packages, and callers that use those
contracts. They form one release unit: consumers regenerate every package from
the same Goa-AI revision and deploy the complete generated system together.
Goa-AI does not provide backward compatibility, mixed-version operation, or a
persisted-suspension migration mode. Workflows and suspensions created by the
previous release may fail after deployment. Historical completed-session
records remain owned by the session store and are not rewritten for a runtime
contract release.

`ValidateContinuation` checks the checkpoint and registered tool contracts; it
does not make an incompatible persisted value compatible. Suspension schema
`goa-ai.run-suspension.v4` is the only supported shape. Every model-authored
await item preserves the runtime `ToolCallID` separately from the provider
`ModelToolCallID`, so execution records and provider transcript reconstruction
never substitute one identity for the other. Suspensions written by older
runtimes, including v3, cannot be resumed after this coordinated release. The
runtime has no dual read, fallback, or migration mode; incompatible checkpoints
fail validation.

Coordinated generated-code deployment does not own ordinary service
availability. Services called by activities must keep a ready endpoint
throughout their own rollout. These responsibilities stay outside Goa-AI because the
consumer owns its process layout, traffic router, persistence, and downstream
services. The operational checklist is in
[docs/runtime.md](docs/runtime.md#coordinated-generated-system-releases).

`DeleteSession` ends execution but intentionally retains run metadata.
Applications that permanently delete customer data call `PurgeSession` after
workflow and stream settlement; the session store then removes the session,
all of its run records, and every private checkpoint in one owned operation.

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
`Serve` stops renewal, atomically marks each exact token/incarnation lease
draining, then closes the canonical shared sink before processing the remaining
local queue. Draining leases are excluded from new publication but retain
authority to claim and settle calls already delivered before intake closed. The
drain transition carries the configured shutdown duration so Redis keeps that
authority for the full settlement lifecycle. `Serve` waits workers and
registry-owned terminal publication, drains every queued acknowledgement, and
releases each exact lease. A sink-setup
failure has no consumption to settle and proceeds directly to bounded release.
Close, worker, result, or acknowledgement failure is explicit and suppresses
release; lease expiry is the durable fallback. Before a worker dispatches
locally queued work, `ClaimToolCall` authenticates its exact lease and request
event at the global call record. The atomic result is `execute`, `terminal`,
`claimed`, or `expired`; only `execute` invokes the handler. Dispatch ownership
never transfers, so acknowledgement redelivery cannot repeat side effects.
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

Registry construction enumerates every authoritative catalog key and applies
the same strict current-format parser used by registration and routing before
health tracking starts. Unknown fields, legacy lease values, or any other
incompatible record keep the registry unready and report every affected key;
startup never rewrites or decodes an older format.

The wire-visible `RegistrationToken` is not a secret. It is the lowercase
SHA-256 digest of the domain `goa-ai/tool-registry-admission/v2\0`, the uint32
big-endian wire protocol version, the raw 32-byte canonical schema fingerprint,
a uint32 big-endian admission-revision byte length, and the revision bytes. Tool
order and toolset/tool tag order are
normalized before schema fingerprinting; payload, result, and sidecar raw schema
bytes are exact identity inputs, so generated schema JSON must remain canonical.
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
handoff until lease expiry. A wire protocol change remains a coordinated hard
cutover because registry servers and consumers do not negotiate envelope
versions.
`Unregister` is not rollout orchestration; it intentionally changes active to
retired while preserving leases. Same-token retirement retry succeeds, a stale
expected token returns `admission_conflict`, retired toolsets are unavailable to
discovery and calls, and the retired exact token cannot register again.

Wire protocol changes use a hard cutover. There is no legacy envelope, optional
fallback, capability negotiation, or dual decoder. Quiesce consumers, drain
admitted calls and provider leases, then stop every old registry replica. Back
up the catalog and atomically remove entries owned by the old wire version while
preserving retained call records. Start the new registry against that cleaned
catalog, then start matching providers and finally consumers. Wire protocol
version 8 uses this order; version 7 and version 8 registries never serve
concurrently.

The new registry rejects an old consumer's missing version before service
invocation, catalog lookup, health checks, result-stream creation, call
admission, or Pulse publication. It rejects an old provider on Register or
renewal before provider admission. The one runtime-owned version therefore
fences both independent rollout populations, while the version-bound
registration token preserves the existing provider-generation fence.

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
The explicit decision record is wire protocol version 8. Version 7 and version
8 registry replicas cannot serve concurrently; deployment replaces the complete
registry replica set before providers and consumers roll forward.
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

The one-time cleanup of pre-contract Redis records and queued unfenced messages
is an operational hard-cutover concern, not a compatibility mechanism. See
[`docs/POST_ROLLOUT_CLEANUP.md`](docs/POST_ROLLOUT_CLEANUP.md). Deterministic
admission identity, catalog-owned leases, token fencing, flat shared streams,
server-owned handoff, and incompatible-admission non-overlap remain permanent.

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
  event also fingerprints the local validation error instead of retaining
  provider-controlled text. Model content remains observability data rather
  than workflow state, so diagnostic storage cannot retry inference and
  Temporal and hook payloads remain bounded. A planner result rejected after
  model output was accepted emits `PlannerOutputRejected` instead, with only
  the bounded local reason identity.
- **Generated tool validation**: A model definition created from a generated
  tool specification retains its generated payload decoder inside the process.
  Unary tool calls and final streamed tool-call chunks must match a definition
  in the exact model request. Generated payloads are decoded before planner code
  can observe them, so an invalid first response cannot lead to another model
  call. The activity validates the selected planner request again before
  scheduling effects.
- **Planner-transparent provenance**: Each model call produces an isolated
  canonical response and ordered presentation before planner code observes
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
  canonical provider message returned by its response helpers. It publishes
  only the selected response's text, thinking, and tool-argument deltas, while
  usage accounts for every invocation, including valid numeric counts from a
  rejected usage chunk. It commits the complete selected response once after
  atomic admission and before effects. Planners never manage transcript handles
  or provider replay metadata, and uncertain ownership fails instead of
  selecting by call order or visible text.
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
  Bedrock returns that error for Runtime models such as Claude Opus 4.7,
  Sonnet 5, and Mythos 5 that require AWS's separate Mantle count endpoint.
  When encoded tools carry authored `input_examples`, completion, streaming,
  and counting all attach the same Anthropic tool-examples beta header.
  Exact retention always keeps whole recent turns; it never truncates
  tool_use/tool_result pairs to satisfy a token budget.
- **Bookkeeping control plane**: `Bookkeeping()` calls and results remain in the
  provider transcript so signed model-authored parts replay without modification.
  They consume neither retrieval budget nor consecutive-failure allowance.
  Successful bookkeeping results are omitted only from compact `ToolOutputs`
  and do not force another planner turn. Every failed bookkeeping result enters
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
  succeeds. Recovery call IDs extend the planner activity payload and select the
  canonical failed outputs that shape both reminders and the advertised catalog.
  Empty recovery IDs are omitted from ordinary activity payloads. Runtime
  workers, generated packages, and callers deploy as one coordinated hard
  cutover. Mixed versions are unsupported; ongoing workflows and saved
  suspensions may fail after the cutover.
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
  Completion-aware suspensions use checkpoint version 2 so older runtimes
  reject, rather than ignore, the saved policy. Current runtimes reject
  version-1 checkpoints; deployments must complete or discard older
  suspensions before upgrading.
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
- Temporal engines always construct their client from `ClientOptions` and
  install the Goa-AI data converter. No supported constructor can bypass exact
  number decoding, unknown-field rejection, unsafe-value checks, or the total
  1 MiB limit for one workflow or activity call. Tool executors persist larger
  domain results first and return their durable reference.
- Post-start fatal worker failures surface through the configured
  `worker.Options.OnFatalError` callback instead of being silently ignored.
  Integrating services should treat that callback as process-fatal and exit.

## Tool Input Schema

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
source call identities. The configured run-log store must implement
`runlog.SessionReader` for a session-backed transcript containing dedicated
continuation calls. Tool names matching `continue_` followed by exactly 24
lowercase hexadecimal characters are reserved for runtime-generated
continuation tools. Agent and toolset registration reject that exact format;
similar names such as `continue_search` and qualified canonical tools such as
`tools.continue_search` remain valid.

For each tool with a non-empty payload, the plugin derives JSON Schema from the
Goa attribute using Goa's `openapi.Schema` type for complete JSON Schema draft
2020-12 support. The generated tool spec is the canonical model-facing contract:
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
