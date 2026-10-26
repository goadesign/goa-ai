# Goa-AI Design: Agentic Runtime, Toolsets, and MCP Extensions for Goa

**Goa-AI** (`goa.design/goa-ai`) extends the [Goa](https://goa.design) framework with a declarative DSL, code generators, and a durable execution runtime for building AI agents, tool registries, typed evaluations, and Model Context Protocol (MCP) servers and clients in Go.

---

## Table of Contents

1. [What you get](#what-you-get)
2. [How it works](#how-it-works)
3. [Layout](#layout)
4. [Unified Toolset Model](#unified-toolset-model)
   - [Toolset Ownership and Registration](#toolset-ownership-and-registration)
   - [Tool Identification](#tool-identification)
   - [Tool Input Schema](#tool-input-schema)
   - [Consumer Input Binding](#consumer-input-binding)
   - [Presentation, Server Data, and Text-Only Contracts](#presentation-server-data-and-text-only-contracts)
   - [Confirmation, Hints, and Prompt Caching](#confirmation-hints-and-prompt-caching)
   - [Pagination Ownership](#pagination-ownership)
   - [Standalone Value Codecs](#standalone-value-codecs)
5. [Agent Runtime, Operations, and Evaluations](#agent-runtime-operations-and-evaluations)
   - [Direct Agent Operations](#direct-agent-operations)
   - [Service-Owned Typed Completions](#service-owned-typed-completions)
   - [Generated Evaluations](#generated-evaluations)
   - [Planner Step Contract](#planner-step-contract)
   - [Run Timing and Workflow Continuations](#run-timing-and-workflow-continuations)
6. [Streaming, Model Gateways, Observability, and Engine Activation](#streaming-model-gateways-observability-and-engine-activation)
   - [Agent run lifecycle streaming contract](#agent-run-lifecycle-streaming-contract)
   - [Provider Stream Integrity Contract](#provider-stream-integrity-contract)
   - [Model Clients, Adaptive Rate Limiting, and Capability Gates](#model-clients-adaptive-rate-limiting-and-capability-gates)
   - [GenAI Observability Contract](#genai-observability-contract)
   - [Runtime Tracing Error Contract](#runtime-tracing-error-contract)
   - [Temporal Worker Activation Contract](#temporal-worker-activation-contract)
7. [Registry Integration](#registry-integration)
   - [Deployment Ownership](#deployment-ownership)
   - [Registry Vocabulary](#registry-vocabulary)
   - [Transcript Boundary](#transcript-boundary)
8. [MCP Server Definition](#mcp-server-definition)
   - [Protocol and Ownership](#protocol-and-ownership)
   - [Catalog ownership and generated definitions](#catalog-ownership-and-generated-definitions)
   - [Silent MCP HTTP responses](#silent-mcp-http-responses)
   - [MCP DSL Quick Reference](#mcp-dsl-quick-reference)
   - [Generated Server, Adapter, Client, and Transport](#generated-server-adapter-client-and-transport)
   - [Tasks, Subscriptions, Completions, and Content Blocks](#tasks-subscriptions-completions-and-content-blocks)
9. [Agents Quickstart & Example Scaffold](#agents-quickstart--example-scaffold)
10. [Security considerations](#security-considerations)
    - [Private OAuth credential storage](#private-oauth-credential-storage)
11. [Error code mapping](#error-code-mapping)
12. [Contributing](#contributing)
13. [Summary](#summary)

---

## What you get

- **Declarative Agent, Toolset, Evaluation, and MCP DSLs**: Extend Goa service definitions with agents (`Agent`, `Use`, `Export`), toolsets (`Toolset`, `Tool`), typed operations (`Operation`), evaluation suites (`EvalSuite`, `Scenario`), and MCP capabilities (`MCP`, `Resource`, `Prompt`, `TaskExchange`, `SubscriptionSource`).
- **Compile-Time Specialization and Code Generation**: Emit canonical JSON Schemas, deterministic payload/result/server-data codecs, type transforms, tool specifications, Temporal workflow registrations, evaluation harnesses, and MCP JSON-RPC servers/clients with zero runtime reflection over Goa design expressions.
- **Durable Agent Execution Runtime**: Execute multi-turn agent loops, parallel tool batches, nested agent-as-tool child workflows, and stateless human-in-the-loop suspensions (`await_clarification`, `await_confirmation`, `await_external_tools`) backed by Temporal or an in-memory development engine.
- **Cluster-Wide Tool Registry**: Route tool invocations from agent workers to remote service providers over Redis-backed Pulse streams (`ConnectProvider`) with strict wire-protocol admission (`WireProtocolVersion = 15`) and lease-based health monitoring.
- **Provider-Agnostic Model Gateways**: Connect planners to AWS Bedrock, Anthropic, OpenAI, and Google Gemini through unified streaming adapters with strict stream-integrity verification, provider capability gates, and adaptive token-bucket rate limiting.
- **OpenTelemetry GenAI Observability**: Emit structured spans, metrics, and session-scoped Pulse stream events (`session/<session_id>`) with explicit audience filtering (`RuntimeHostProfile`, `AgentDebugProfile`, `MetricsProfile`).

---

## How it works

1. **Design**: Authors declare services, agents, toolsets, operations, evaluation suites, and MCP surfaces in Goa design packages using `goa.design/goa/v3/dsl` and `goa.design/goa-ai/dsl`.
2. **Evaluate DSL**: Running `goa gen <module>/design` executes the Goa-AI generator plugins (`codegen/agent`, `codegen/mcp`, `codegen/eval`), which evaluate the DSL into roots under `expr/agent/` and `expr/mcp/` and validate cross-expression invariants during the `Prepare` and `Validate` phases.
3. **Generate Code**:
   - The **Agent plugin** (`codegen/agent`) emits per-agent packages (`gen/<service>/agents/<agent>/`), service-shared toolset specifications (`gen/<service>/toolsets/<toolset>/`), operation runners (`gen/<service>/operations/<op>/`), standalone value codecs (`gen/<service>/codecs/<codec>/`), and typed completions (`gen/<service>/completions/`).
   - The **Eval plugin** (`codegen/eval`) emits deterministic evaluation suites (`gen/evals/<suite>/`) and scenario harnesses.
   - The **MCP plugin** (`codegen/mcp`) emits JSON-RPC 2.0 MCP handlers, service-method adapters, typed clients, and SSE/HTTP streamable transports (`gen/mcp_<service>/` and `gen/jsonrpc/mcp_<service>/`).
4. **Execute**: Application binaries construct `runtime.Runtime` with a workflow engine (`features/engine/temporal` or `features/engine/inmem`), a session store (`features/session/mongo` or in-memory), a Pulse stream sink (`features/stream/pulse`), and model clients, then register generated agents and toolsets before activating workers.

```
┌────────────────────────────────────────────────────────────────────────────┐
│                              Goa + Goa-AI DSL                              │
│   Service / Agent / Toolset / Operation /Completion / EvalSuite / MCP     │
└─────────────────────────────────────┬──────────────────────────────────────┘
                                      │ `goa gen`
                  ┌───────────────────┼───────────────────┐
                  ▼                   ▼                   ▼
        ┌───────────────────┐ ┌───────────────┐ ┌───────────────────┐
        │   codegen/agent   │ │ codegen/eval  │ │    codegen/mcp    │
        │  gen/<svc>/...    │ │ gen/evals/... │ │  gen/mcp_<svc>/   │
        └─────────┬─────────┘ └───────┬───────┘ └─────────┬─────────┘
                  │                   │                   │
                  ▼                   ▼                   ▼
        ┌────────────────────────────────────────────────────────────────────┐
        │                        Goa-AI Runtime Layer                        │
        │  • runtime/agent/runtime (Workflows, Planner Loop, Hook Bus)       │
        │  • runtime/toolregistry  (Pulse Stream Router, Protocol v15)       │
        │  • runtime/mcp           (JSON-RPC Caller, SSE/HTTP Transport)     │
        │  • features/*            (Temporal, Mongo, Pulse, Bedrock/OpenAI)  │
        └────────────────────────────────────────────────────────────────────┘
```

---

## Layout

```
goa-ai/
├── dsl/                          # Public DSL functions (Agent, Toolset, Tool, Operation, EvalSuite, MCP, ...)
├── expr/
│   ├── agent/                    # Evaluated AST expressions for agents, toolsets, operations, codecs, evals
│   └── mcp/                      # Evaluated AST expressions for MCP servers, tools, resources, prompts, tasks
├── codegen/
│   ├── agent/                    # Code generator for agents, toolsets, specs, operations, codecs, completions
│   ├── eval/                     # Code generator for evaluation suites and scenario runners
│   ├── mcp/                      # Code generator for MCP servers, adapters, clients, and JSON-RPC transports
│   ├── ir/                       # Intermediate representation shared across generators
│   ├── naming/                   # Canonical identifier and package naming helpers
│   └── shared/                   # Shared schema, transform, and template helpers
├── runtime/
│   ├── agent/                    # Core agent runtime packages
│   │   ├── api/                  # Workflow engine contracts, storage activity commands, suspension checkpoints
│   │   ├── engine/               # Abstract workflow engine, WorkflowContext, Future, Receiver interfaces
│   │   ├── hooks/                # Strongly typed hook event bus and subscriber interfaces
│   │   ├── interrupt/            # Run cancellation and human-in-the-loop pause/resume controllers
│   │   ├── model/                # Provider-agnostic LLM request, response, chunk, and citation contracts
│   │   ├── memory/               # Transcript persistence store interfaces
│   │   ├── planner/              # Planner interfaces, PlanResult, ToolFailure, RetryHint, FinalTool
│   │   ├── policy/               # Run capability and budget policies
│   │   ├── prompt/               # Prompt registry, template rendering, and prompt specialization
│   │   ├── rawjson/              # Immutable raw JSON message wrappers
│   │   ├── reminder/             # System reminder injection for planner turns
│   │   ├── runtime/              # Runtime orchestrator, workflow loop, tool execution, hints/
│   │   ├── session/              # Session, run metadata, seed, preparation, and checkpoint store contracts
│   │   ├── stream/               # Subscriber stream profiles (RuntimeHost, AgentDebug, Metrics) and bridge
│   │   ├── telemetry/            # OpenTelemetry GenAI spans, metrics, and logger interfaces
│   │   ├── tools/                # ToolSpec, TypeSpec, JSONCodec, Ident, and schema validation
│   │   └── transcript/           # Canonical transcript ledger, message conversion, and thought signatures
│   ├── content/                  # Rich MCP content block constructors and validators (Text, Image, Audio, ...)
│   ├── mcp/                      # MCP JSON-RPC runtime, Caller, transport clients, OAuth, and error types
│   ├── toolregistry/             # Registry wire protocol (v15), Pulse stream routing, and admission control
│   └── toolserverdata/           # Typed server-data envelopes and audience enforcement (model/timeline/internal)
├── eval/
│   ├── evidence/                 # Deterministic trajectory and tool-call expectation assertions
│   ├── judge/                    # LLM-as-a-judge rubric evaluators
│   └── runner/                   # Evaluation scenario execution harness
├── features/
│   ├── engine/                   # Workflow engines: temporal/ and inmem/
│   ├── memory/mongo/             # MongoDB transcript backing store
│   ├── model/                    # Model adapters: anthropic/, bedrock/, gemini/, openai/, middleware/, ollama/
│   ├── policy/basic/             # Basic turn/time budget policy implementation
│   ├── prompt/mongo/             # MongoDB prompt override store
│   ├── session/mongo/            # MongoDB session, run, event, seed, and checkpoint store
│   └── stream/pulse/             # Redis Streams (Pulse) sink and subscriber implementation
└── gen/ (emitted in user repos)
    ├── <svc>/
    │   ├── agents/<agent>/       # Agent registration, workflow definitions, specs/, exports/, agenttools/
    │   ├── toolsets/<toolset>/   # Shared toolset specs.go, codecs.go, types.go, transforms.go
    │   ├── operations/<op>/      # Direct operation types, codecs, and runners
    │   ├── completions/          # Service-owned typed completion specs and helpers
    │   └── codecs/<codec>/       # Standalone value codecs
    ├── evals/<suite>/            # Generated evaluation suite runners
    ├── mcp_<svc>/                # Generated MCP server, adapter, client, and notification encoders
    └── jsonrpc/mcp_<svc>/        # Generated JSON-RPC HTTP and SSE transport handlers/clients
```

---

## Unified Toolset Model

Goa-AI treats every toolset uniformly through the `Toolset` DSL, regardless of how its tools are executed at runtime.

### Toolset Ownership and Registration

A `Toolset` is declared once—either at the top level or inside its owning `Service`—and consumed by one or more agents via `Use` or exported by an agent via `Export`:

| Toolset Kind | DSL Declaration | Runtime Execution Path | Generated Artifacts |
| :--- | :--- | :--- | :--- |
| **Service-Backed (Method-Bound)** | `Toolset("name", func() { Tool("t", func() { BindTo("Method") }) })` | Dispatched as a Temporal tool activity (`ExecuteToolActivity`), either locally via generated executors or remotely through the Tool Registry (`FromRegistry`) | `gen/<svc>/toolsets/<toolset>/` (`specs.go`, `codecs.go`, `types.go`, `transforms.go`) |
| **Agent-as-Tool (Exported)** | Inside `Agent`: `Export("suite", func() { Tool(...) })`; consumer calls `Use(AgentToolset("svc", "agent", "suite"))` | Executed inline inside the parent workflow as a deterministic child workflow via `ExecuteAgentChild` (never scheduled as an activity) | `gen/<svc>/agents/<agent>/exports/<export>/` and `agenttools/<export>/` |
| **MCP-Backed** | `Toolset(FromMCP("service", "suite"))` or external `FromExternalMCP("server", "suite")` | Routed through the runtime's `mcpruntime.Caller` (`ExecuteToolActivity` -> MCP JSON-RPC `tools/call`) | `gen/<svc>/agents/<agent>/specs/<mcp_suite>/` |
| **Client-Provided / External** | Tools declared without backend service bindings and registered as external | Pauses the workflow with `await_external_tools`, records a suspension checkpoint, and resumes when the caller supplies `ProvideToolResults` | `gen/<svc>/toolsets/<toolset>/` |

#### Service-Shared vs. Agent-Scoped Specs

When a service declares a `Toolset`, `codegen/agent` generates the canonical types, JSON Schemas, and codecs once under `gen/<svc>/toolsets/<toolset>/`. Every agent in that service (or across services) that references the toolset imports those shared definitions, eliminating type duplication. Agent-specific wrappers under `gen/<svc>/agents/<agent>/specs/<toolset>/` re-export or specialize only what differs per consumer (such as consumer input bindings or `UIOnly` projections).

### Tool Identification

Every tool has a canonical, globally unambiguous identifier (`tools.Ident`) formatted as:

```
<toolset_name>.<tool_name>
```

For example, tool `get_forecast` in toolset `weather.read` has canonical ID `weather.read.get_forecast`.
- Generated constants (`const GetForecast tools.Ident = "weather.read.get_forecast"`) are emitted in the toolset's package so callers never construct tool names with string concatenation.
- Because LLM provider APIs (such as AWS Bedrock Converse and OpenAI Function Calling) restrict tool names to `[a-zA-Z0-9_-]{1,64}`, model adapters automatically sanitize `tools.Ident` values at the provider wire boundary (replacing `.` with `_` or hashing overlong names) and reverse-map incoming `ToolUse` blocks back to the canonical `tools.Ident` before returning chunks to the runtime.

### Tool Input Schema

Generated `tools.ToolSpec` factories (`Specs()` and `Spec<Name>()`) produce self-contained tool specifications on demand without reading Goa's `docs.json` at runtime:

- **Canonical Raw JSON Schemas**: Payload, result, and server-data schemas are emitted at codegen time as immutable `rawjson.Message` (`tools.RawJSON`) byte slices. They are never re-parsed into `map[string]any` in the runtime except inside provider SDK adapters that require untyped map inputs.
- **Structured Field Metadata (`TypeSpec.Fields`)**: Alongside the raw JSON Schema, codegen precomputes `[]tools.FieldSpec` on every `TypeSpec`, capturing each field's dot-path, description, JSON type, enum values, and union (`OneOf`) branch membership. Clarification prompts, retry hints, and UI form builders read `TypeSpec.Fields` directly instead of walking JSON Schema trees at runtime.
- **Two-Stage Boundary Validation**: When a model emits a tool call, the runtime first validates the raw JSON arguments against the exact JSON Schema advertised to the model, and then invokes the generated `JSONCodec.FromJSON` decoder to run Goa's typed validations (patterns, min/max bounds, required non-empty arrays, and `OneOf` discriminated unions). If either check fails, the runtime synthesizes a structured `planner.RetryHint` containing the field-level errors and the generated `InputExample` JSON.

### Consumer Input Binding

When an agent consumes a provider toolset via `Use`, some provider payload fields may represent server-verified context or lookup values that the model should not supply directly or should specify via a simpler key. The **Consumer Input Binding DSL** (`dsl/input_binding.go`) customizes how an agent consumes a tool inside `Use`:

```go
Use(ProviderToolset, func() {
    BindInput("update_order_shipping",
        // Hide account_id from the model schema; resolve it at decode time from execution context.
        BindField("account_id"),
        // Replace the provider's complex AddressTarget struct with a model-facing AddressKey string.
        BindField("destination", func() {
            Lookup(AddressKey, "address_label", "postal_code")
        }),
    )
})
```

- **Context-Bound Fields (`BindField("field")`)**: Omitted from the model-facing JSON Schema and consumer payload struct. Before the provider tool executes, the generated `ConsumerInputResolver` populates the field from the verified execution context.
- **Lookup-Bound Fields (`BindField("field", func() { Lookup(keyType, ...) })`)**: Replaced in the model-facing schema with `keyType` (or inlined key attributes). At decode time, the runtime decodes the model's payload into the consumer-specific struct, invokes the registered typed lookup resolver to translate the model's key into the provider's domain type, and constructs the canonical provider payload via generated `InputBindingTransform` helpers (`codegen/agent/consumer_input_*.go`).
- **Context-Injected Strings (`Inject`)**: For tool fields that are simple strings fixed at run start (such as `session_id` or `tenant_id`), `Inject("field_name")` hides the field from the model schema and populates it from `run.Context` via `runtime.ToolCallExecutor` interceptors.

### Presentation, Server Data, and Text-Only Contracts

Goa-AI separates data meant for the model's reasoning transcript from rich visual artifacts meant for the user interface or observer timelines.

#### Server-Data Envelopes and Audiences (`runtime/toolserverdata`)

Tools can declare sidecar `ServerData(kind, type, ...)` payloads in the DSL that travel alongside `ToolResult` without inflating the model's context window. Every server-data entry carries an explicit `Audience` (`runtime/toolserverdata`):
- **`AudienceTimeline` (`"timeline"`)**: Visual artifacts (charts, tables, citations, summary cards) persisted to the session stream and rendered in the user interface.
- **`AudienceModel` (`"model"`)**: Compact structured metadata visible to the planner/model boundary when explicitly projected.
- **`AudienceInternal` (`"internal"`)**: Execution-only state (such as pagination continuation tokens or telemetry metrics) consumed by parent finalizers or hooks and stripped before reaching the UI or model.

#### Presentation DSL (`dsl/presentation.go`) and Text-Only Enforcement

For tools or fields that exist solely to drive visual UI components (such as chart layout options or interactive canvas cards), the DSL provides four presentation primitives:
- **`RequiresUI()`**: Marks an entire tool as requiring a graphical user interface.
- **`UIOnly(fields...)`**: Marks specific payload fields on a tool as UI-only presentation controls.
- **`UIInstructions(text)`**: Attaches model-facing system instructions that are included in the prompt only when a UI is present.
- **`UIResultReminder(text)`**: Emits a turn-scoped system reminder after the tool succeeds in a UI-enabled run.

When an agent definition or an individual run sets `TextOnly: true` (for example, headless background jobs, Slack/SMS integrations, or API-only invocations):
1. Tools marked `RequiresUI()` are excluded from the advertised tool catalog and rejected if called.
2. Payload fields marked `UIOnly(...)` are stripped from the model-visible JSON Schema (`codegen/agent/specs_builder_uionly.go`), and `UIInstructions` / `UIResultReminder` strings are suppressed.
3. At the tool-result boundary, `toolserverdata.ValidateTextOnly` inspects all emitted server-data items and fails fast if any item carries `AudienceTimeline`.

### Confirmation, Hints, and Prompt Caching

#### Operator Confirmation (`dsl/confirmation.go`)

Tools that perform state-mutating or destructive actions (such as canceling an order or deleting records) can require explicit user approval in the DSL:

```go
Tool("cancel_order", "Cancel an existing customer order", func() {
    Args(CancelOrderPayload)
    Return(CancelOrderResult)
    Confirmation(func() {
        Title("Confirm Order Cancellation")
        PromptTemplate("Cancel order {{ .OrderID }} for {{ .CustomerName }}?")
        DeniedResultTemplate("User declined canceling order {{ .OrderID }}.")
    })
})
```

When the model invokes a tool with a `Confirmation` policy (or a runtime-registered confirmation rule), the workflow does not execute the tool activity. Instead, it renders `PromptTemplate` against the validated payload, publishes an `await_confirmation` event, persists a `goa-ai.run-suspension.v14` checkpoint, and exits the workflow cleanly. When the user approves or denies via `ProvideConfirmation`, a new workflow continuation either executes the tool with the exact checkpointed payload or injects the rendered `DeniedResultTemplate` into the transcript.

#### Tool Call and Result Hints (`runtime/agent/runtime/hints`)

To provide human-readable progress labels in streaming UIs (for example, *"Checking forecast for San Francisco..."*), generated specs register deterministic Go `text/template` templates compiled with `missingkey=error` via `hints.RegisterCallHint` / `hints.RegisterResultHint` (and batch helpers `RegisterCallHints` / `RegisterResultHints`). When streaming hook events, the runtime calls `hints.RenderCallHint` and `hints.RenderResultHint` against the typed payload or result.

#### Prompt Caching (`dsl/history.go`)

Agents can configure provider-side prompt cache checkpoints in their DSL Policy block using `Cache`:

```go
Agent("chat", "Conversational assistant", func() {
    RunPolicy(func() {
        Cache(func() {
            AfterSystem()
            AfterTools()
        })
    })
})
```

- `AfterSystem()` places a cache breakpoint immediately after the system prompt preamble.
- `AfterTools()` places a cache breakpoint after the advertised tool catalog definitions (supported on Anthropic and AWS Bedrock Converse for Claude models).

### Pagination Ownership

Tools that query bounded slices of large datasets declare cursor-based pagination in the DSL using `Paginated(cursorField, nextCursorField)` or `BoundedResult`:

- **Model-Facing vs. Runtime-Driven Continuation**: When a tool returns a non-empty `NextCursor` in its `BoundedResult`, the owning toolset or parent agent decides whether pagination is exposed to the model (by advertising `cursor` in the payload schema) or driven automatically by the runtime/finalizer.
- **Guaranteed Termination**: Every paginated tool result must report truthful `Bounds` (`Returned`, `Total`, `Truncated`, `NextCursor`, `RefinementHint`). When a result is truncated and no further cursor is available (or the continuation budget is exhausted), `RefinementHint` tells the model how to narrow its query (for example, by shortening the date range or adding a category filter).

### Standalone Value Codecs

When a service needs deterministic JSON serialization and Goa schema validation for domain types that are not directly bound to a tool payload or result (such as durable workflow state, object-store envelopes, or Redis cache records), authors declare `ValueCodec(name, Type)` in the DSL (`dsl/codec.go`). Codegen emits `gen/<svc>/codecs/<codec>/` containing a standalone `tools.JSONCodec[T]` with `ToJSON` and `FromJSON` functions that enforce all Goa design validations without requiring a tool wrapper.

---

## Agent Runtime, Operations, and Evaluations

### Direct Agent Operations

Not every AI interaction requires an open-ended multi-turn planner loop. For deterministic, single-purpose AI tasks with typed inputs and typed outputs (such as extracting structured metadata from a document, classifying a support ticket, or drafting a summary), Goa-AI provides the `Operation` DSL (`dsl/operation.go`):

```go
Service("documents", func() {
    Operation("extract_metadata", "Extract structured metadata from a document", func() {
        Args(ExtractMetadataPayload)
        Return(ExtractMetadataResult)
        Use(DocumentTools)
    })
})
```

`codegen/agent` generates `gen/<svc>/operations/<op>/` with typed request/response wrappers and registers the operation on the runtime. Operations can be invoked via:
- **`StartOperation` / `EnsureRunCompletion`**: Starts or awaits a durable, session-affiliated operation workflow.
- **`StartOneShotOperation`**: Starts a stateless, sessionless operation workflow.
- **`StartToolOperation` / `StartOneShotToolOperation` / `EnsureToolOperationCompletion` / `EnsureOneShotToolOperationCompletion`**: Executes a single registered tool directly through the runtime's durable workflow and activity machinery without invoking a planner model.

### Service-Owned Typed Completions

When a service needs a single, schema-constrained structured output from a model without any tool calls or workflow persistence (for example, generating a concise session title or parsing a natural-language filter into a struct), it declares a `Completion(name, description, func() { Return(ResultType) })` in the DSL (`dsl/completion.go`):

- `codegen/agent` emits `gen/<svc>/completions/` with a canonical JSON Schema and typed `JSONCodec` for `ResultType`.
- At runtime, `CompleteStructured` / `StreamCompleteStructured` invokes the model with constrained decoding (`StructuredOutput` schema enforcement) and validates the response through the generated codec before returning the Go struct to the caller.
- Completion names must be 1–64 ASCII characters matching `^[A-Za-z0-9_-]+$` so they satisfy every model provider's structured-output schema name rules.

### Generated Evaluations

Goa-AI includes a first-class evaluation framework (`dsl/eval.go`, `codegen/eval`, `eval/`) so agent behavior and tool trajectories can be tested deterministically in CI alongside unit tests:

```go
EvalSuite("assistant_evals", func() {
    TargetAgent("assistant", "helper")
    Scenario("weather_forecast_lookup", func() {
        Prompt("What is the current forecast for San Francisco?")
        // Deterministic evidence assertions or LLM-as-a-judge rubrics
    })
})
```

- **Generated Runners (`gen/evals/<suite>/`)**: `codegen/eval` emits typed scenario harnesses that wire the target agent, mock or live tool executors, and transcript collectors (`eval/runner`).
- **Deterministic Evidence Assertions (`eval/evidence`)**: Tests assert exact trajectory invariants over the recorded run using `evidence.Expect`:
  - `ExpectCall(toolIdent)`: Verifies that a specific tool was called and optionally inspects its decoded payload and result.
  - `ExpectFailure(toolIdent, kind)`: Verifies that a tool call failed with the expected `planner.ToolFailure` classification.
  - `ExpectConfirmation(toolIdent)`: Verifies that the run paused to request operator confirmation for the specified tool.
  - `ForbidTools(toolIdents...)`: Verifies that none of the forbidden tools were invoked during the run.
- **Rubric Judges (`eval/judge`)**: Combines deterministic trajectory checks with model-graded rubric evaluations for semantic quality, tone, and completeness.

### Planner Step Contract

Each turn of an agent workflow invokes the agent's `planner.Planner` implementation (`PlanStart` on the initial turn; `PlanResume` after tool execution or human input). The planner returns a `planner.PlanResult` subject to strict mutual-exclusion invariants enforced by the runtime:

1. **Mutually Exclusive Actions**: A `PlanResult` must contain either:
   - Non-empty `ToolCalls` (requesting parallel or sequential execution of domain tools or nested agents), **or**
   - An `Await` specification (`AwaitClarification`, `AwaitConfirmation`, or `AwaitExternalTools`), **or**
   - A `FinalResponse` (or `FinalTool` result for structured agent outputs) that terminates the turn.
   Returning both `ToolCalls` and `FinalResponse` (or `Await`) in the same step is a contract violation and fails the workflow immediately.
2. **Structured Tool Failures and Recovery (`planner.ToolFailure`)**: When a tool call fails, the runtime classifies the error into `invalid_call`, `domain_rejection`, `unavailable`, `rate_limited`, `timeout`, or `malformed_result` and attaches a `Recovery` directive:
   - `correct_call`: Keeps the failed tool in the advertised catalog and provides a `RetryHint` with field-level schema issues and `InputExample` so the model can fix its arguments on the next turn.
   - `replan`: Removes the failed tool from the next turn's catalog so the model chooses an alternative tool.
   - `finish`: Strips domain tools (preserving only active pagination continuations) and forces the planner to synthesize a final response from the evidence gathered so far.

### Run Timing and Workflow Continuations

#### Bounded Turn and Workflow Budgets

Agent runs separate **Workflow Engine timeouts** from **RunPolicy budgets** (`runtime/agent/policy`):
- `MaxToolCalls`: Maximum total tool invocations permitted across the turn.
- `MaxConsecutiveFailedToolCalls`: Maximum consecutive failed tool steps before the runtime halts tool execution and transitions to finalization.
- `TimeBudget`: Wall-clock budget for active planner and tool execution within a single run segment.

#### Zero-Wait Workflow Suspensions (`goa-ai.run-suspension.v14`)

Goa-AI workflows **never** block inside a Temporal workflow awaiting human input (`await_clarification`, `await_confirmation`, or `await_external_tools`). Holding a workflow open across human think-time ties up worker deployment versions and complicates rollouts. Instead:

1. **Suspend & Checkpoint**: When the planner (or confirmation gate) requests external input, the workflow invokes the `runtime.store` activity with the `Suspension` command, which writes an immutable **`goa-ai.run-suspension.v14`** checkpoint document to the session store (`session.Store`), emits the `await_*` stream event followed by `run_stream_end`, and returns from the Temporal workflow function (`COMPLETED` at the Temporal layer, `suspended` in `session.RunMeta`).
2. **Prepare Continuation**: When the user or external caller supplies input via `ProvideClarification`, `ProvideConfirmation`, or `ProvideToolResults`, the service calls `PrepareContinuation` (`SeedBegin` -> `SeedAppend` -> `SeedPublish` / `PreparationBegin`), validating the supplied response ID and payload against the saved `goa-ai.run-suspension.v14` checkpoint.
3. **Resume on Current Workers**: The runtime launches a fresh Temporal workflow execution for the continuation turn on the currently active `WorkerDeploymentVersion`. The new workflow loads the prior transcript and validated continuation seed from the session store and resumes at `PlanResume`.

#### Cancellation, Relinquishment, and Task Consumer Pending Execution

- **`CancelRun`**: Cancels an active or suspended run. If the run is currently executing a Temporal workflow, the runtime signals cancellation and records `Cancellation` in the session store. If the run is suspended at a checkpoint, `CancelRun` transitions `RunMeta` to `canceled` and sets `RunMeta.SuspensionResolution` so late resume attempts are rejected deterministically.
- **`RelinquishRunSuspension`**: Allows an owning orchestrator (such as a parent workflow coordinator) to release a suspended run without executing a resumption turn, recording the resolution reason in `RunMeta.SuspensionResolution`.
- **Single-Flight Storage Activity (`runtime.store`)**: All session and transcript mutations inside a workflow pass through a single local/regular activity (`runtime.store`) multiplexed by `api.StorageActivityCommand`, which enforces exactly one of **14** mutually exclusive command variants per invocation:
  - Run lifecycle starts: `RootStart`, `ChildStart`, `OneShotStart`, `OneShotChildStart`, `SynchronousStart`
  - Continuation preparation & seeding: `PreparationBegin`, `SeedBegin`, `SeedAppend`, `SeedPublish`
  - In-turn and terminal transitions: `Append`, `Cancellation`, `Suspension`, `Terminal`, `UnstartedTerminal`

---

## Streaming, Model Gateways, Observability, and Engine Activation

### Agent run lifecycle streaming contract

Every event emitted during an agent turn flows through the in-process `hooks.Bus` and is projected by `stream.Subscriber` onto a session-owned Pulse stream (`session/<session_id>`):

- **Session-Scoped Stream Ownership**: Both root agent runs and nested agent-as-tool child runs publish to the same `session/<session_id>` stream. Events from nested child runs carry their own `RunID` and are linked to the parent tool call via a preceding `child_run_linked` event and `ToolResult.RunLink`.
- **Explicit Per-Run Termination (`run_stream_end`)**: Every run execution segment (whether it ends in `completed`, `failed`, `canceled`, or pauses in `suspended`) emits a terminal `workflow` phase event followed by a `run_stream_end` marker for its `RunID`. SSE gateways and test runners watch for `run_stream_end` matching the root `RunID` to close the HTTP response stream deterministically without polling.
- **Stream Visibility Profiles (`runtime/agent/stream`)**: Consumers select one of three preconfigured `StreamProfile` projections:
  1. **`stream.RuntimeHostProfile()` (`"runtime-host"`)**: The default profile for product gateways and UI SSE bridges. Emits assistant deltas, thinking blocks, tool call start/update/result notifications, await prompts, workflow status transitions, `child_run_linked` links, and `run_stream_end` markers.
  2. **`stream.AgentDebugProfile()` (`"agent-debug"`)**: Adds internal planner prompts, raw retry hints, and detailed child-run diagnostics for developer debugging tools.
  3. **`stream.MetricsProfile()` (`"metrics"`)**: Emits only lightweight usage, token count, and timing telemetry events.

### Provider Stream Integrity Contract

Streaming model adapters (`features/model/bedrock`, `anthropic`, `openai`, `gemini`, `ollama`) enforce a strict state-machine contract over provider token streams before emitting `model.Chunk` values to the planner (`planner.ConsumeStream` / `PlannerModelClient`):

1. **Complete Tool Call Assembly**: Provider adapters accumulate streamed JSON fragments for `ToolUse` blocks internally and emit a `ChunkTypeToolCall` only when the provider signals block completion and the JSON payload is syntactically complete. Truncated or malformed JSON tool deltas fail the stream immediately rather than reaching the tool decoder.
2. **Cryptographic Thought Signatures**: When reasoning models (such as Claude 3.7+/4 with extended thinking or Gemini 2.5+ thinking) emit signed thinking blocks, the adapter preserves the exact `ThinkingPart.Signature` and `RedactedThinkingPart` byte payloads. `runtime/agent/transcript` replays these blocks unmodified in subsequent assistant turns so multi-turn tool loops never trigger provider signature-verification errors.
3. **Explicit Stop Reason and Usage Accounting**: Every valid model stream must close with an explicit `ChunkTypeStop` carrying the canonical stop reason (`end_turn`, `tool_use`, `max_tokens`, `content_filtered`) and reconciled token `Usage` (input, output, cache read, and cache write tokens). Premature EOF without a stop chunk is classified as a transport error.

### Model Clients, Adaptive Rate Limiting, and Capability Gates

- **Adaptive Token-Bucket Rate Limiter (`features/model/middleware`)**: `AdaptiveRateLimiter` wraps any `model.Client` with cluster-coordinated (Pulse `rmap`) or process-local AIMD (Additive Increase / Multiplicative Decrease) token budgeting:
  - Before invoking the model, `Reserve` estimates input token cost and acquires capacity from the shared token bucket.
  - When the stream finishes, `Settle` reconciles actual provider-reported token usage against the reservation and adjusts the target TPM (tokens per minute) rate—increasing linearly on sustained throughput and backing off multiplicatively when a provider returns `429 TooManyRequests` / throttling exceptions.
- **Claude Capability Gates (`features/model/internal/claudecaps`)**: Centralizes model-family capability detection across Anthropic and AWS Bedrock Converse (such as whether the target Claude model version supports interleaved thinking, adaptive thinking budgets, tool-result image blocks, or `AfterTools` prompt cache breakpoints) so adapters fail fast or specialize requests deterministically at construction time.

### GenAI Observability Contract

`runtime/agent/telemetry` instruments every run, planner turn, model call, and tool execution following the OpenTelemetry Semantic Conventions for Generative AI (`gen_ai.*`):
- **Span Hierarchy**: `gen_ai.agent.run` (root/child workflow) -> `gen_ai.planner.turn` -> `gen_ai.chat` (model invocation) and `gen_ai.execute_tool` (tool activity or child agent).
- **Recorded Attributes**: `gen_ai.system`, `gen_ai.request.model`, `gen_ai.usage.input_tokens`, `gen_ai.usage.output_tokens`, `gen_ai.response.finish_reasons`, `gen_ai.tool.name`, `gen_ai.tool.call.id`, and `goa_ai.run_id` / `goa_ai.session_id`.

### Runtime Tracing Error Contract

Span status and error recording distinguish **recoverable domain/model outcomes** from **infrastructure or invariant failures**:
- When a model emits invalid tool arguments (`invalid_call`) or a tool returns a business-level `domain_rejection` that the planner handles via `RetryHint` or `Recovery`, the `gen_ai.execute_tool` span records the structured failure attributes (`goa_ai.tool.failure.kind`, `goa_ai.tool.recovery.action`) while keeping the parent workflow span healthy (`Unset` / `Ok`).
- Span status is marked `codes.Error` only when an unrecovered infrastructure failure, unhandled activity error, timeout, or runtime invariant violation aborts the operation.

### Temporal Worker Activation Contract

`features/engine/temporal` separates **agent/workflow registration** from **worker polling activation**:
1. During startup, services construct `temporal.Engine` in registration mode (`RegisterWorkflow`, `RegisterActivity`) and register all agents, toolsets, and operations on `runtime.Runtime`.
2. Calling `Engine.Start(ctx)` (or activating worker polling) seals the registration table and starts polling the configured Temporal task queues (`WorkerDeploymentVersion` / build ID routing).
3. Attempting to register an agent, toolset, or workflow after `Start` has been called returns an immediate error, preventing race conditions where a Temporal worker picks up a workflow task before all local activity handlers and codecs are registered.

---

## Registry Integration

The **Tool Registry** (`registry/` and `runtime/toolregistry`) decouples agent workers from domain tool providers across service and process boundaries.

### Deployment Ownership

```
┌─────────────────────────┐         gRPC CallTool         ┌─────────────────────────┐
│      Agent Worker       │ ────────────────────────────► │      Tool Registry      │
│  (Consumes FromRegistry │                               │  (Catalog & Multiplexer │
│       Toolsets)         │ ◄──────────────────────────── │   WireProtocol v15)     │
└─────────────────────────┘          ToolResult           └────────────┬────────────┘
                                                                       │
                                              Pulse Stream             │ ConnectProvider
                                       `toolset:<id>:requests`         │ (Inbound Stream)
                                                                       ▼
                                                          ┌─────────────────────────┐
                                                          │  Domain Tool Provider   │
                                                          │  (Executes Service Method│
                                                          │   & CompleteToolCall)   │
                                                          └─────────────────────────┘
```

- **Provider-Initiated Registration**: Domain services that own tools run a lightweight provider loop (`ConnectProvider`) that registers the toolset's canonical schemas and wire protocol version with the Tool Registry and subscribes to the toolset's request stream (`toolset:<toolset_id>:requests`). The registry never needs outbound network routes or firewall rules to reach domain providers.
- **Strict Wire Protocol Admission (`WireProtocolVersion = 15`)**: Every provider registration, tool dispatch envelope, and result completion message carries `WireProtocolVersion = 15` (`runtime/toolregistry/admission.go`). The registry rejects registrations or calls with mismatched protocol versions at the boundary.
- **Lease and Health Tracking**: Providers send periodic heartbeats over their active stream (`registry:health:<toolset_id>`). If a provider's lease expires without renewal, the registry marks the toolset unhealthy and fails fast with `unavailable` rather than queuing calls into a dead stream.

### Registry Vocabulary

| Term | Meaning |
| :--- | :--- |
| **`ToolsetRegistration`** | The runtime descriptor binding a `Toolset` name, its `[]tools.ToolSpec`, its execution handler (`Execute`), and its origin (`Local`, `Registry`, `Agent`, `MCP`). |
| **`ToolCallMessage`** | The wire envelope published by the registry to `toolset:<toolset_id>:requests`, carrying `ToolUseID`, `Tool` (`tools.Ident`), raw validated `Payload` JSON, `ExecutionContext` headers, and trace context. |
| **`ToolResultMessage`** | The completion envelope returned by the provider via `CompleteToolCall` (or published to `result:<tool_use_id>`), carrying the canonical `Result` JSON, typed `ServerData` items, or structured `ToolFailure`. |

### Transcript Boundary

The Tool Registry is purely a **stateless dispatch and multiplexing bus**—it never reads, writes, or retains conversation transcripts or session history:
- All payload validation against the consumer's schema happens before dispatch, and all result/server-data persistence into the session transcript (`session.Store`) happens inside the calling agent workflow (`runtime.store` activity) after `CallTool` returns.
- Provider processes receive only the single tool call's validated payload and verified `ExecutionContext`, keeping domain services stateless with respect to agent conversations.

---

## MCP Server Definition

Goa-AI provides a complete Model Context Protocol (MCP) implementation (`dsl/mcp.go`, `expr/mcp/`, `codegen/mcp`, `runtime/mcp`, `runtime/content`) that turns any Goa service into a standards-compliant MCP server and generates typed Go MCP clients.

### Protocol and Ownership

- **JSON-RPC 2.0 Transport**: Generated MCP servers expose the standard MCP methods (`initialize`, `ping`, `tools/list`, `tools/call`, `resources/list`, `resources/templates/list`, `resources/read`, `resources/subscribe`, `resources/unsubscribe`, `prompts/list`, `prompts/get`, `completion/complete`, `tasks/get`, `tasks/result`, `tasks/list`, `tasks/cancel`) over Streamable HTTP and SSE (`gen/jsonrpc/mcp_<service>/`).
- **Service-Method Delegation**: Every MCP tool, resource, prompt, completion, or task operation binds to an underlying Goa service `Method`. The generated `MCPAdapter` translates between MCP wire envelopes and the service's native Go payload/result structs so business logic stays transport-agnostic.

### Catalog ownership and generated definitions

The MCP server's advertised catalog (`tools/list`, `resources/list`, `resources/templates/list`, `prompts/list`) is precomputed at codegen time into static definitions in `gen/mcp_<service>/`:
- **Static Service Binding (`BindStaticService` / `CompileStaticService`)**: For services whose MCP catalog is fixed at compile time, `codegen/mcp` emits the exact JSON-RPC catalog descriptors and schema payloads directly into the generated package.
- **Dynamic Filtering**: When tool or resource availability depends on caller principal scopes or runtime feature flags, the generated server applies an optional visibility filter over the static catalog without rebuilding schemas at runtime.

### Silent MCP HTTP responses

Per the MCP JSON-RPC 2.0 specification, inbound JSON-RPC **notifications** (messages without an `id`, such as `notifications/initialized` or `notifications/cancelled`) must not produce a JSON-RPC response body. The generated HTTP transport (`gen/jsonrpc/mcp_<service>/`) returns `202 Accepted` with an empty body for notifications and reserves `200 OK` (`application/json` or `text/event-stream`) for request-response exchanges.

### MCP DSL Quick Reference

| DSL Function | Scope | Purpose |
| :--- | :--- | :--- |
| `MCP(name, version, opts...)` | `Service` | Enables MCP for the service with server name, semver version, and protocol version (`ProtocolVersion`). |
| `MCPTool(name, description, fn)` / `Tool(...)` | `Method` / `Toolset` | Exposes a service method as an MCP tool with generated input/output JSON Schemas. |
| `ToolTitle(title)`, `ReadOnlyHint(b)`, `DestructiveHint(b)`, `IdempotentHint(b)`, `OpenWorldHint(b)` | `Tool` (`dsl/mcp_annotations.go`) | Attaches MCP behavioral annotations (`annotations`) to the advertised tool descriptor. |
| `ToolContent(fn)` | `Method` / `Tool` (`dsl/tool_content.go`) | Maps fields of the method's result type to rich MCP content blocks (`Text`, `Image`, `Audio`, `ResourceLink`, `EmbeddedResource`). |
| `Resource(name, uri, mimeType)` / `WatchableResource(...)` | `Method` | Exposes a method as a static MCP resource (`resources/read`), optionally supporting `resources/subscribe`. |
| `ResourceTemplate(name, uriTemplate, mimeType)` | `Method` | Exposes a parameterized RFC 6570 URI template (`resources/templates/list`). |
| `Prompt(name, description)` / `DynamicPrompt(...)` | `Method` | Exposes a static or method-backed prompt template (`prompts/get`). |
| `PromptCompletion(name)` / `ResourceCompletion(name)` | `Method` (`dsl/prompt_completion.go`, `dsl/resource_completion.go`) | Binds a service method to handle `completion/complete` requests for prompt arguments or resource template parameters. |
| `TaskExchange()` | `Method` | Marks a long-running method as supporting asynchronous MCP task polling and cancellation (`tasks/get`, `tasks/result`, `tasks/cancel`). |
| `SubscriptionSource(name)` | `Method` | Binds a streaming or polling source that emits `notifications/resources/updated` events to subscribed clients. |

### Generated Server, Adapter, Client, and Transport

Running `goa gen` on an MCP-enabled service produces two coordinated packages:
1. **`gen/mcp_<service>/`**:
   - `server.go` & `adapter.go`: The `MCPAdapter` implementing the MCP protocol methods by calling your Goa `Service` interface, validating URI templates, and converting results into MCP content blocks (`content_types.go`).
   - `client.go`: A strongly typed Go `Client` that implements `mcpruntime.Caller` for invoking remote MCP servers from Goa-AI agents.
   - `notification_encode.go`: Encoders for server-initiated notifications (`notifications/resources/updated`, `notifications/tools/list_changed`, progress updates).
2. **`gen/jsonrpc/mcp_<service>/`**:
   - HTTP and SSE JSON-RPC 2.0 server mounts (`NewServer`, `Mount`) and client transports with session header management (`Mcp-Session-Id`) and protocol version negotiation (`MCP-Protocol-Version`).

### Tasks, Subscriptions, Completions, and Content Blocks

- **Rich Content Blocks (`runtime/content` and `dsl/tool_content.go`)**: Both handwritten and DSL-generated tool handlers construct validated MCP content blocks via `runtime/content` (`NewText`, `NewImage`, `NewAudio`, `NewResourceLink`, `NewEmbeddedTextResource`, `NewEmbeddedBlobResource`). Generated server conversions (`codegen/mcp/content_types.go`) validate MIME types, base64 encodings, and non-empty URIs before serializing the response.
- **Asynchronous Tasks (`TaskExchange`)**: Methods annotated with `TaskExchange()` can return an initial task handle (`TaskId`, `Status: "working"`, `PollInterval`) and complete asynchronously while clients poll `tasks/get` / `tasks/result` or cancel via `tasks/cancel`.
- **Resource Subscriptions (`WatchableResource` & `SubscriptionSource`)**: Clients subscribe to watchable resource URIs via `resources/subscribe`. When the underlying `SubscriptionSource` emits a change event, the generated notifier pushes `notifications/resources/updated` over the client's SSE stream.
- **Argument Completions (`PromptCompletion` & `ResourceCompletion`)**: Routes `completion/complete` requests for `ref/prompt` and `ref/resource` references to the bound service method and returns bounded completion candidates (`Values`, `Total`, `HasMore`).

---

## Agents Quickstart & Example Scaffold

### 1. Define a Service, Toolset, and Agent (`design/design.go`)

```go
package design

import (
    . "goa.design/goa/v3/dsl"
    . "goa.design/goa-ai/dsl"
)

var _ = API("assistant", func() {})

var WeatherToolset = Toolset("weather", func() {
    Tool("get_forecast", "Get the current weather forecast for a city", func() {
        Args(func() {
            Attribute("city", String, "City name, e.g. San Francisco")
            Required("city")
        })
        Return(func() {
            Attribute("summary", String, "Weather summary")
            Attribute("temp_c", Float64, "Temperature in Celsius")
            Required("summary", "temp_c")
        })
        BindTo("get_forecast")
    })
})

var _ = Service("assistant", func() {
    Method("get_forecast", func() {
        Payload( func() {
            Attribute("city", String, "City name")
            Required("city")
        })
        Result(func() {
            Attribute("summary", String, "Weather summary")
            Attribute("temp_c", Float64, "Temperature in Celsius")
            Required("summary", "temp_c")
        })
    })

    Agent("helper", "Weather assistant agent", func() {
        Use(WeatherToolset)
        RunPolicy(func() {
            DefaultCaps(MaxToolCalls(5))
            TimeBudget("2m")
        })
    })
})
```

### 2. Generate Code and Example Scaffold

- Generate the canonical contracts under `gen/`:
  ```bash
  goa gen example.com/assistant/design
  ```
- Optionally generate starter implementation files (`goa example example.com/assistant/design`), which emits:
  - `cmd/<service>/main.go`: Runnable binary entrypoint.
  - `internal/agents/<service>/bootstrap/bootstrap.go`: Runtime construction and agent/toolset registration wiring.
  - `internal/agents/<agent>/planner/planner.go`: Starter `planner.Planner` implementation (`PlanStart` / `PlanResume`).
  - `internal/agents/<agent>/toolsets/<toolset>/execute.go`: Starter local toolset executor bound to the service's methods.

---

## Security considerations

- **Single-Edge Authorization and Context Propagation**: Validate OAuth bearer tokens, session ownership, and principal privileges at the HTTP/gRPC ingress boundary before calling `StartRun` or `PrepareContinuation`. Pass verified identity and scope identifiers via `run.Context` (`Inject`) or `ConsumerInputResolver` (`BindInput`) so the model can never forge tenant or account identifiers in tool arguments.
- **Strict Schema and Boundary Rejection**: Never sanitize, coerce, or heuristically rewrite malformed model tool calls. Generated JSON Schemas and Goa codecs reject unknown fields, invalid enum variants, and out-of-range numbers at the boundary before any domain service method is invoked.
- **Confirmation Gates for Mutating Tools**: Any tool that modifies external state or dispatches irreversible commands should declare a `Confirmation` block so the workflow suspends (`goa-ai.run-suspension.v14`) and requires an explicit, authenticated user `ProvideConfirmation` call bound to the exact checkpointed payload.

### Private OAuth credential storage

When an MCP client or remote tool provider authenticates via OAuth 2.0 (`runtime/mcp/oauth`), access tokens, refresh tokens, and PKCE verifiers must be stored in private, encrypted persistence keyed by the authenticated principal and connection ID—never placed in `run.Context`, model-visible tool arguments, `ToolResultPart.Content`, or session stream events (`session/<session_id>`).

---

## Error code mapping

### MCP JSON-RPC 2.0 Server & Protocol Error Codes (`runtime/mcp`)

| JSON-RPC Code | Goa / MCP Condition | Description |
| :--- | :--- | :--- |
| `-32700` | Parse error | Invalid JSON received by the JSON-RPC transport. |
| `-32600` | Invalid Request | The JSON payload is not a valid JSON-RPC 2.0 request object. |
| `-32601` | Method not found | Unknown MCP method or unregistered tool/prompt name. |
| `-32602` | Invalid params | Payload failed Goa schema validation, missing required fields, or invalid URI template arguments (`bad_request`, `invalid_argument`). |
| `-32603` | Internal error | Unhandled server error or downstream dependency failure (`internal`). |
| `-32020` | `HeaderMismatch` | Request session or protocol header conflicts with the established MCP session state. |
| `-32021` | `MissingRequiredClientCapability` | Client omitted a capability required by the server or tool during `initialize`. |
| `-32022` | `UnsupportedProtocolVersion` | Client requested an `MCP-Protocol-Version` not supported by the server. |

### Typed MCP Client Error Hierarchy (`runtime/mcp/errors.go`)

When a Goa-AI agent invokes an MCP server via `mcpruntime.Caller`, errors are classified into typed Go errors so the runtime's retry and recovery engine can distinguish deterministic rejections from ambiguous network failures:
- **`*mcp.ToolExecutionError`**: The MCP server returned a valid result with `isError: true` (domain-level tool rejection).
- **`*mcp.HTTPResponseError`**: The HTTP transport returned a non-2xx status code before a JSON-RPC envelope was produced.
- **`*mcp.MalformedResponseError`**: The server returned invalid JSON or a response violating the MCP schema (`malformed_result`).
- **`*mcp.InternalError`**: The server returned a JSON-RPC error object (such as `-32602` or `-32603`).
- **`*mcp.OutcomeUnknownError`**: The connection dropped or timed out after the request was sent, so the caller cannot know whether a non-idempotent tool side effect occurred.

---

## Contributing

- **Requirements**: Go 1.27+ (`go 1.27.3` in `go.mod`) and `golangci-lint`.
- **Standard Workflow**:
  1. Edit DSL (`dsl/`, `expr/`) or generator templates (`codegen/`), keeping codegen specialized at generation time rather than adding runtime reflection.
  2. Build and lint:
     ```bash
     make build
     make lint
     ```
  3. Run unit and end-to-end integration suites:
     ```bash
     make test
     make itest
     ```
- **Documentation Sync**: Any change to user-facing DSL functions, runtime contracts, or generated code layouts must update both this `DESIGN.md` and the documentation pages under `content/en/docs/2-goa-ai/`.

---

## Summary

Goa-AI unifies **Goa's design-first code generation** with a **durable, production-grade agentic runtime**:
- **Single Source of Truth**: Services, toolsets, agents, direct operations, typed completions, evaluation suites, and MCP servers are all declared in Goa DSL and compiled into zero-reflection Go packages.
- **Strong Boundary Contracts**: Model inputs are validated against exact JSON Schemas and generated Goa codecs; human waits use zero-wait `goa-ai.run-suspension.v14` checkpoints; remote tool providers connect over Wire Protocol v15 streams; and real-time session streams (`session/<session_id>`) deliver audience-filtered updates with deterministic `run_stream_end` completion markers.
