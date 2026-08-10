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

External input does not weaken provider transcript identity. A model-authored
free-text interaction uses the tool-clarification await branch, which retains
the exact tool name, call ID, and payload and resumes with the human answer as
that tool's generated result. Runtime-owned clarification prompts remain a
separate branch that resumes as a user message. This distinction prevents
prompt reminders or presence-dependent fields from standing in for a real
`tool_use` / `tool_result` exchange.

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
| Any failed tool has a `ToolFailure` whose recovery action permits tools | Runtime-enforced correction or replan turn |
| `SynthesizeAfterTools` is true and no failure permits tools | `PlanResumeInput.SynthesisOnly` |
| Otherwise | Normal continuation turn |

This order makes recovery explicit rather than presence-based. `ToolFailure`
classifies why execution failed independently from its `RecoveryDirective`:
`correct_call` narrows the next planner activity to one failed tool and requires
changed payloads that satisfy every correction obligation for that tool.
Distinct failed tools are queued in canonical failure order, while provider
adapters continue to project historical canonical tool names independently of
the narrowed current catalog. `replan` removes the failed tool from the
recovery turn and permits another advertised capability or final answer, and
`finish` forbids more tools. The runtime validates and enforces those
transitions. When the same tool has both correction and replan failures in one
batch, correction takes precedence and keeps only that tool available until its
correction obligations are satisfied. A correction turn may pause for missing
user input; the same obligations remain active when planning resumes after the
answer. Replan does not permit a clarification pause.
`SynthesizeAfterTools` from the original batch or a later correction turn
survives while a queue contains only `correct_call` obligations; `replan` or
`finish` clears that intent.
Synthesis-after-tools batches must contain at least one budgeted tool and cannot
contain a `TerminalRun` tool, ensuring the existing step classification always
reaches the appropriate planner resume. The resume activity validates the
returned planner result, so ignoring `SynthesisOnly` fails at the activity
boundary rather than reopening execution.

### Run Timing and Indefinite Awaits

The workflow loop is the sole owner of run-duration enforcement. `TimeBudget`
becomes the deterministic Budget deadline for active planner and tool work.
The Hard deadline is Budget plus `FinalizerGrace`; it bounds the final planner
activity after budget exhaustion. Terminal hook persistence uses its own
completion context after planner execution. Time spent blocked on an
external-input await (`await_clarification`, `await_confirmation`, provided
tool results) extends both deadlines, so an operator can take arbitrarily long
to respond without burning the run's active-time budget.

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
A different wire protocol, schema, or admission revision requires Kubernetes
Recreate so incompatible providers never overlap: graceful releases permit
immediate server-owned handoff, while crashes delay handoff until lease expiry.
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

The registry atomically admits each global `ToolUseID` once. The authoritative
record stores a token-independent digest of toolset, tool, payload, and call
metadata plus the admitted registration token. `CallTool` attaches to this
record before current catalog or health lookup; an exact retained retry therefore
returns its original token and deadlines after retirement or replacement.
For a new call, the registry atomically stores one admitted or rejected state in
the tool-use record. Catalog and provider-health failures commit the rejected
state or observe an admission that won the race. A rejected state returns typed
`call_not_admitted` and prevents every exact retry from executing while that
run-scoped decision is retained, so the executor may safely replan. Generated
`not_found` and `validation_error` preserve their actionable types in the same
decision record. Errors after an admitted decision remain ambiguous and produce
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
  and do not force another planner turn. Every failed bookkeeping result enters
  the recovery transition: `correct_call` and `replan` may repair through tools,
  while `finish` resumes without tools so the planner can synthesize the
  terminal outcome. A successful bookkeeping-only turn must otherwise resolve
  in the same turn via a terminal outcome or an await/pause handshake.
- **Forced finalization control plane**: when runtime caps or deadlines force
  finalization, planners may return terminal bookkeeping tools instead of a
  prose final answer. The runtime executes only `TerminalRun()` tools in that
  path (`TerminalRun()` implies bookkeeping), keeps them inside the remaining
  hard-deadline window, and closes the run only if every terminal side effect
  succeeds. Correct-call restrictions use the stable run-policy envelope and
  are scoped to one normal recovery activity; queued tools receive separate
  turns. Recovery call IDs extend the planner activity payload and select the
  canonical failed outputs that shape both reminders and the advertised catalog.
  Empty recovery IDs are omitted for compatibility. Deployments must stop or
  drain every old worker before recovery turns are scheduled by the new code:
  old activities reject the new input and old workflows reject the new output.
  These restrictions never constrain forced finalization. Caller
  `WithRestrictToTool` policy remains run-scoped and still applies to every
  tool.
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
  - Tool-execution events carry a `ToolFailure` with an independent failure kind
    and recovery action. `correct_call` permits a runtime-constrained turn
    containing only one failed tool and queues other failed tools, `replan`
    removes the failed tool from the next turn's caller-allowed catalog, and
    `finish` is terminal for tool execution. A same-tool `correct_call`
    obligation takes precedence over a parallel `replan`.

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
page returns items, the runtime derives one temporary no-argument action for
that live source invocation. Each action has a stable model-facing name and
describes the original model-visible query, while execution retains the
canonical continuation tool name. Selecting an action binds the exact source
tool-call identity and cursor into the executable request. Successful
continuation results carry that source identity through the durable tool-call
record, so repeated equal queries, equal opaque cursors, sequential pages, and
parallel batches remain independent. A continuation that returns its input
cursor violates the paging contract and fails immediately. The model chooses
which semantic query with returned evidence to continue but never reproduces a
cursor or correlation identifier.

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
