## Goa‑AI Overview

Goa‑AI is a design‑first framework for building agentic, tool‑driven systems in Go. You declare agents, toolsets, and run policies in Goa’s DSL; Goa‑AI then generates typed code, codecs, workflows, and registry helpers that plug into a production‑grade runtime (in‑memory for dev, Temporal for durability). Planners focus on strategy; the runtime handles orchestration, policies, memory, streaming, telemetry, and MCP integration.

### When to use Goa‑AI
- **LLM workflows with tools**: Build agents that call typed tools with validations and examples, not ad‑hoc JSON.
- **Durable orchestration**: Need long‑running, resumable runs with retries, time budgets, and deterministic replays.
- **Agent composition**: Treat one agent as a tool of another, even across processes (inline execution, single history).
- **Typed schemas everywhere**: Generated payload/result types and codecs keep schema drift and hand‑rolled encoding out.
- **Operational visibility**: Stream planner/tool/assistant events; persist transcripts; instrument with logs/metrics/traces.
- **MCP integration**: Consume tool suites from MCP servers through generated wrappers and callers.

## Core mental model
```
DSL → Codegen → Runtime → Engine + Features
```
- **DSL (`goa-ai/dsl`)**: Declare agents inside a Goa `Service`. Specify toolsets (native or MCP) and a `RunPolicy`.
- **Codegen (`codegen/agent`, `codegen/mcp`)**: Emits agent packages under `gen/`, tool specs/codecs, Temporal activities, and registry helpers.
- **Runtime (`runtime/agent`, `runtime/mcp`)**: Durable plan/execute loop with policy enforcement, memory/session stores, hook bus, telemetry, MCP callers.
- **Engine (`runtime/agent/engine`)**: Abstracts the workflow backend (in‑memory for dev; Temporal adapter for production).
- **Features (`features/*`)**: Optional modules (Mongo memory/session, Pulse stream sink, Bedrock/OpenAI model clients, policy engine).

Never edit `gen/` by hand — always regenerate after DSL changes.

## Usage patterns
- **Single‑process dev (fast path)**  
  - Use the default in‑memory engine; register your agent with a stub planner; run and iterate quickly.
- **Worker / client split (durable)**  
  - Workers construct a runtime with a worker‑capable engine, register agents/planners, and poll.  
  - Clients use generated typed clients to `Run` or `Start` (and `Wait`) runs against workers.
- **Agent composition (agent‑as‑tool)**  
  - One agent `Exports` a toolset; another `Uses` it. Generated code executes nested agents inline in the same workflow.
- **MCP toolsets**  
  - Reference MCP suites in the DSL; supply callers; generated registries wire schemas/codecs/transport with retries and tracing.
- **Streaming, memory, telemetry**  
  - Configure a memory store and a stream sink; the runtime publishes events and persists transcripts automatically via hooks.

## Toolsets: service‑owned, agent‑ or method‑backed

Toolsets are owned by Goa services; agents, MCP, and custom executors are
consumers or implementations. The DSL (`Toolset`, `Tool`, `BindTo`,
`Uses`, `Exports`) remains symmetric.

- **Service‑owned toolsets (method‑backed or custom)**  
  - Declared via `Toolset("name", func() { ... })`; tools may `BindTo`
    Goa service methods or be implemented by custom executors.
  - Codegen emits per‑toolset specs/types/codecs under
    `gen/<service>/tools/<toolset>/`.
  - Agents that `Use` these toolsets import the provider specs and get
    typed call builders and executor factories for wiring service
    clients or other executors.

- **Agent‑implemented toolsets (agent‑as‑tool)**  
  - Defined in an agent `Exports` block, and optionally `Uses`d by other
    agents.
  - Ownership still lives with the service; the agent is the
    implementation.
  - Codegen emits provider‑side `agenttools/<toolset>` helpers with
    `NewRegistration` and typed call builders, plus consumer‑side
    helpers in agents that `Use` the exported toolset.

In all cases, `Uses` merges tool specs into the consuming agent’s tool
universe so planners see a single, coherent tool catalog regardless of
how tools are wired.

### Tool schemas JSON (`tool_schemas.json`)

For each agent, codegen also emits a backend‑agnostic JSON catalogue of tools
under:

```text
gen/<service>/agents/<agent>/specs/tool_schemas.json
```

The file contains one entry per tool with its canonical ID and JSON Schemas:

```json
{
  "tools": [
    {
      "id": "toolset.tool",
      "service": "orchestrator",
      "toolset": "helpers",
      "title": "Answer a simple question",
      "description": "Answer a simple question",
      "tags": ["chat"],
      "payload": { "name": "Ask", "schema": { /* JSON Schema */ } },
      "result": { "name": "Answer", "schema": { /* JSON Schema */ } }
    }
  ]
}
```

Schemas are derived from the same DSL and builders as the generated specs and
codecs. If schema generation fails for an agent, `goa gen` fails fast so
callers never observe a drift between runtime contracts and the JSON catalogue.

## Minimal example
### 1) DSL (design/design.go)
```go
package design

import (
	. "goa.design/goa/v3/dsl"
	. "goa.design/goa-ai/dsl"
)

var _ = API("orchestrator", func() {})

var Ask = Type("Ask", func() {
	Attribute("question", String, "User question")
	Example(map[string]any{"question": "What is the capital of Japan?"})
	Required("question")
})

var Answer = Type("Answer", func() {
	Attribute("text", String, "Answer text")
	Required("text")
})

var _ = Service("orchestrator", func() {
	Agent("chat", "Friendly Q&A agent", func() {
		Uses(func() {
			Toolset("helpers", func() {
				Tool("answer", "Answer a simple question", func() {
					Args(Ask)
					Return(Answer)
				})
			})
		})
		RunPolicy(func() {
			DefaultCaps(MaxToolCalls(2), MaxConsecutiveFailedToolCalls(1))
			TimeBudget("15s")
		})
	})
})
```

Generate code:
```bash
goa gen example.com/quickstart/design
```

### 2) Wire runtime and run (cmd/demo/main.go)
```go
package main

import (
	"context"
	"fmt"

	chat "example.com/quickstart/gen/orchestrator/agents/chat"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/runtime"
)

// A tiny planner: always replies, no tools (great for first run)
type StubPlanner struct{}

func (p *StubPlanner) PlanStart(ctx context.Context, in planner.PlanInput) (planner.PlanResult, error) {
	return planner.PlanResult{
		FinalResponse: &planner.FinalResponse{
			Message: model.Message{Role: "assistant", Content: "Hello from Goa‑AI!"},
		},
	}, nil
}
func (p *StubPlanner) PlanResume(ctx context.Context, in planner.PlanResumeInput) (planner.PlanResult, error) {
	return planner.PlanResult{
		FinalResponse: &planner.FinalResponse{
			Message: model.Message{Role: "assistant", Content: "Done."},
		},
	}, nil
}

func main() {
	rt := runtime.New() // in‑memory engine by default

	if err := chat.RegisterChatAgent(context.Background(), rt, chat.ChatAgentConfig{
		Planner: &StubPlanner{},
	}); err != nil {
		panic(err)
	}

	client := chat.NewClient(rt) // generated, typed
	out, err := client.Run(context.Background(),
		[]model.Message{{Role: "user", Content: "Say hi"}},
		runtime.WithSessionID("session-1"),
	)
	if err != nil {
		panic(err)
	}
	fmt.Println("RunID:", out.RunID)
	fmt.Println("Assistant:", out.Content)
}
```

For durability, construct a Temporal engine and pass it to `runtime.New(runtime.Options{Engine: eng})`, then use `Start/Wait` for asynchronous runs and to set task queues, memos, and search attributes.

## How it works (high level)
### Plan → Execute Tools → Resume (loop)
1. The runtime starts a workflow for the agent (in‑memory or Temporal).  
2. Calls your planner’s `PlanStart` with the current messages.  
3. Schedules tool calls returned by the planner (tool payloads/results are encoded/decoded via generated codecs).  
4. Calls `PlanResume` with tool results; repeat until the planner returns a final response or caps/time budgets are hit.  
5. Streams events (planner/tool/assistant) and persists transcript entries if configured.

### Policies and caps
- Enforced per planner turn: max tool calls, consecutive failures, and time budgets.  
- Tools can be allowlisted/filtered by policy engines.

### Tool execution
- **Native toolsets**: You write implementations; generated codecs and specs ensure schemas are typed and validated.
- **Agent‑as‑tool**: Calls `ExecuteAgentInline` to run a nested agent deterministically within the same workflow history.
- **MCP toolsets**: Generated wrappers and callers handle JSON schemas/encoders and transports (HTTP/SSE/stdio) with retries and tracing.

### Memory, streaming, telemetry
- Hook bus publishes `tool_start`, `tool_result`, `assistant_message`, etc.  
- Memory/session stores (e.g., Mongo) subscribe and persist transcripts and run metadata.  
- Stream sinks (e.g., Pulse) carry real‑time events back to callers or UIs.  
- OTEL‑aware logging/metrics/tracing instrument workflows and activities.

### Engine abstraction
- **In‑memory**: Fast dev loop, no external deps.  
- **Temporal**: Durable execution, replay, retries, signals, workers; adapters wire activities and context propagation.

## Effective usage tips
- **Design first**: Put all agent and tool schemas in the DSL; add examples and validations. Let codegen own schemas/codecs.
- **Never hand‑encode**: Use generated codecs and clients (including for JSON‑RPC/MCP); avoid ad‑hoc `json.Marshal`/`Unmarshal`.
- **Keep planners focused**: Planners decide “what” (final answer vs tools to call); tool implementations do the “how”.
- **Split client vs worker**: Register agents only on workers; use generated typed clients from other processes for submissions.
- **Compose with exports/uses**: Prefer agent‑as‑tool to avoid brittle cross‑service contracts and keep a single deterministic history.
- **Regenerate often**: Change DSL → `goa gen` → lint/test → run. Never edit `gen/` manually.

### Advertising tools in planners
- Use the generated `specs.AdvertisedSpecs()` from `gen/<svc>/agents/<agent>/specs` to return tool specs to the model. This keeps IDs and schemas aligned with the design (provider IDs for Used toolsets; chat‑owned for Exports) and avoids manual lists.

## Temporal runtime flow (end‑to‑end)
This section reflects the exported APIs and structs in the runtime and generated packages.

1) Client invocation (caller process)
- Use the generated `NewClient(rt) runtime.AgentClient` and call:
  - `Run(ctx, []model.Message, ...runtime.RunOption)` to start and wait
  - or `Start(ctx, ...opts)` to get an `engine.WorkflowHandle` and `Wait/Signal/Cancel` out‑of‑process
- Common `RunOption`s:
  - `runtime.WithSessionID(string)` (required)
  - `runtime.WithTaskQueue(string)`, `WithMemo(map[string]any)`, `WithSearchAttributes(map[string]any)`
  - Policy overrides per run: `WithPerTurnMaxToolCalls(int)`, `WithRunMaxToolCalls(int)`, `WithRunMaxConsecutiveFailedToolCalls(int)`, `WithRunTimeBudget(time.Duration)`, `WithRunInterruptsAllowed(bool)`, `WithRestrictToTool(tools.Ident)`, `WithAllowedTags([]string)`, `WithDeniedTags([]string)`
  - These override the agent's DSL RunPolicy defaults for the run; zero values mean "no override" (use the design defaults).

2) Engine start (inside `runtime`)
- `AgentClient.Start` calls `runtime.startRun` which resolves the agent and delegates to `startRunOn`.
- `startRunOn` constructs `engine.WorkflowStartRequest` containing:
  - `ID` (generated if absent), `Workflow` (from registration), `TaskQueue`, `Input` (a `*runtime.RunInput`), and optional `Memo`/`SearchAttributes`/`RetryPolicy`.
- `Engine.StartWorkflow` returns an `engine.WorkflowHandle`. The runtime stores it for later signaling (`PauseRun`, `ResumeRun`, `Provide*`).

3) Worker executes workflow
- During agent registration, the generated code calls `rt.RegisterAgent(ctx, runtime.AgentRegistration{ ... })` which:
  - Registers the workflow via `engine.WorkflowDefinition{Name, TaskQueue, Handler: runtime.WorkflowHandler(rt)}`
  - Registers activities: `PlanStartActivityHandler(rt)`, `PlanResumeActivityHandler(rt)`, and `ExecuteToolActivityHandler(rt)` with their `engine.ActivityDefinition`s and options.
- The engine invokes the workflow handler, which coerces input to `*runtime.RunInput` and calls `rt.ExecuteWorkflow`.

4) Plan/execute/resume loop (inside workflow)
- `ExecuteWorkflow(wfCtx, *RunInput)`:
  - Publishes `run_started`, records status, builds a `planner.PlanInput` and calls `runPlanActivity` to run the first planner turn via activity.
  - Initializes caps/time budget from `runtime.RunPolicy` (from registration) and enters `runLoop(...)`.
- `runLoop` repeats:
  - Enforce time budget and interrupts.
  - If `PlanResult.ToolCalls` present → `executeToolCalls(...)`
  - Else if `PlanResult.Await` present → publish await and pause; resume via signals.
  - Else if `PlanResult.FinalResponse` present → complete.
- `runPlanActivity` schedules the plan/resume activity: `wfCtx.ExecuteActivity(ActivityRequest{Name, Input, Queue/Retry/Timeout})` and validates the returned `*planner.PlanResult`.

5) Tool execution (activity vs inline)
- `executeToolCalls` chooses path per toolset:
  - Activity path (default): JSON‑encode payload using generated codec, schedule `ExecuteToolActivity` via `wfCtx.ExecuteActivityAsync(...)`, collect `ToolOutput` futures in order.
  - Inline path (agent‑as‑tool): execute synchronously inside the workflow by calling the registered toolset `Execute` with the workflow context injected into `ctx` (see agent‑as‑tool below). Results are published and aggregated as `planner.ToolResult` directly.
- `ExecuteToolActivity` decodes payload via generated codec, calls the toolset registration’s `Execute(ctx, planner.ToolRequest)`, then re‑encodes the result with the generated result codec. Validation errors are converted into structured `planner.RetryHint` for planners/policies.

6) Completion
- `runLoop` returns a `*runtime.RunOutput` containing:
  - `Final` (`model.Message`), last `ToolEvents`, `Notes` and aggregated `Usage`.
- Runtime sets final status and returns to the client (`Run` path) or leaves result on the handle (`Start` path).

## Agent‑as‑tool (inline composition)
Generated packages for exported toolsets provide first‑class helpers to register an agent as a toolset. Internally this drives inline execution in the same workflow history.

- Generated constants and helpers in the exporter package:
  - Tool IDs (fully qualified) and type aliases for `Payload`/`Result` codecs.
  - `New<Agent>ToolsetRegistration(rt *runtime.Runtime) runtime.ToolsetRegistration`:
    - Uses `runtime.NewAgentToolsetRegistration(rt, runtime.AgentToolConfig{ ... })`
    - Sets `Inline: true` and supplies a strong‑contract `Route` (`runtime.AgentRoute`) plus the exact activity names for plan/resume/execute_tool.
  - `NewRegistration(rt, systemPrompt, ...runtime.AgentToolOption)` to configure per‑tool text/templates and an optional system prompt.
  - Typed tool call builders like `New<TheTool>Call(args *<TheTool>Payload, ...CallOption) planner.ToolRequest`

- What happens at runtime:
  - The consumer process registers the exporter’s toolset with `rt.RegisterToolset(reg)`; the registration’s `Execute` function is the default agent‑tool executor.
  - When the runtime encounters a tool call whose toolset is `Inline`, it:
    - Publishes a scheduled event.
    - Injects the `engine.WorkflowContext` into `ctx` and calls the toolset’s `Execute(ctx, call)`.
    - The default executor builds messages from the tool payload (optionally using provided templates or text), constructs a nested `run.Context` with `runtime.NestedRunID`, and calls:
      - `rt.ExecuteAgentInline(wfCtx, <local AgentID>, messages, nestedRunCtx)` when the agent is registered locally, or
      - `rt.ExecuteAgentInlineWithRoute(wfCtx, route, planActivity, resumeActivity, executeToolActivity, messages, nestedRunCtx)` for cross‑process inline execution using the provider’s queue and activity names.
    - The nested agent runs a full plan/execute/resume loop inline as part of the parent workflow history, then the result is adapted back to a `planner.ToolResult` for the parent planner.

Key runtime types involved:
- `runtime.ToolsetRegistration` (Name, Specs, Execute, TaskQueue, Inline)
- `runtime.AgentRoute` (ID, WorkflowName, DefaultTaskQueue)
- `runtime.AgentToolConfig` (AgentID or Route, activity names, optional system prompt, per‑tool templates/texts)
- `runtime.NewAgentToolsetRegistration(rt, cfg)` to produce registrations for inline execution
- `runtime.ExecuteAgentInline(...)` and `runtime.ExecuteAgentInlineWithRoute(...)` for nested runs

## Integration points: user code vs generated code vs runtime
- User code
  - Implement `planner.Planner` (`PlanStart`, `PlanResume`).
  - Optionally implement service‑backed tools by providing a `runtime.ToolCallExecutor` (or `ToolCallExecutorFunc`) and registering via the generated `New<Agent><Toolset>ToolsetRegistration(exec)` + `rt.RegisterToolset`.
  - Configure runtime: `runtime.New(runtime.WithEngine(...), WithMemoryStore(...), WithRunStore(...), WithHooks(...), WithStream(...), WithLogger/WithMetrics/WithTracer(...), runtime.WithWorker(agentID, runtime.NewWorker(...)))`.
  - Provide model clients: `rt.RegisterModel("model-id", client)`.
  - Drive execution via generated `NewClient(rt).Run/Start` and `runtime.With*` options; handle `engine.WorkflowHandle` if using `Start`.
  - For exported agent‑as‑tool, optionally provide per‑tool text/templates via `runtime.WithText`, `runtime.WithTemplate`, or use `runtime.CompileAgentToolTemplates`.

- Generated code
  - Per agent: constants `AgentID`, `WorkflowName`, `DefaultTaskQueue`, `PlanActivity`, `ResumeActivity`, `ExecuteToolActivity`.
  - `Register<Agent>(ctx, rt, <Agent>Config) error` registers workflow + activities + toolsets + policy + specs.
  - `NewWorker(...runtime.WorkerOption) runtime.WorkerConfig` to build queue overrides for worker bindings.
  - `Route() runtime.AgentRoute` and `NewClient(rt) runtime.AgentClient` for remote callers.
  - For toolsets:
    - Service toolsets: `New<Agent><Toolset>ToolsetRegistration(exec runtime.ToolCallExecutor)`; you supply the executor.
    - Exported agent toolsets: `New<Agent>ToolsetRegistration(rt)` and `NewRegistration(rt, systemPrompt, ...runtime.AgentToolOption)`.

- Runtime/library code
  - `runtime.RegisterAgent`, `runtime.RegisterToolset`, `runtime.Client/ClientFor/MustClient/MustClientFor`.
  - `runtime.AgentClient` with `Run/Start`.
  - `runtime.RunOption`s (`WithSessionID`, `WithTaskQueue`, `WithMemo`, `WithSearchAttributes`, policy overrides).
  - `engine.Engine`, `engine.WorkflowDefinition`, `engine.ActivityDefinition`, `engine.WorkflowHandle`.
  - Activities: `PlanStartActivity`, `PlanResumeActivity`, `ExecuteToolActivity`.
  - Inline composition: `runtime.NewAgentToolsetRegistration`, `runtime.ExecuteAgentInline`, `runtime.ExecuteAgentInlineWithRoute`.
  - Tool codecs/specs: `tools.ToolSpec`, `tools.JSONCodec` used by the runtime to marshal/unmarshal payloads/results.

## Streaming for UIs
Stream client‑facing events (assistant chunks, planner thoughts, tool progress/results, human‑in‑the‑loop awaits, usage) to WebSocket/SSE or a bus (e.g., Pulse).

- What to implement
  - Implement `stream.Sink` with:
    - `Send(ctx context.Context, event stream.Event) error`
    - `Close(ctx context.Context) error`
  - Event types you’ll receive: `assistant_reply`, `planner_thought`, `tool_start`, `tool_update`, `tool_end`, `await_clarification`, `await_external_tools`, `usage`.

- Global broadcast (all runs)
  - Initialize the runtime with a sink. The runtime auto‑registers a stream subscriber that bridges hooks → stream:
```go
sink := myStreamSink // implements stream.Sink
rt := runtime.New(runtime.WithStream(sink))
```

- Per‑run streaming (UI per tab/connection)
  - Attach a filtered subscriber for a specific run and receive only its events:
```go
closeFn, err := rt.SubscribeRun(ctx, runID, sink)
if err != nil { /* handle */ }
defer closeFn() // unsubscribes and closes the sink
```

- Per‑request manual bridge (using the bus directly)
  - Create and register a temporary subscriber for a given connection:
```go
sub, _ := streambridge.Register(rt.Bus, sink) // returns hooks.Subscription
defer sub.Close()
```

Notes
- Stream events are derived from the internal hook bus, filtered to client‑facing payloads.
- Tool payloads/results in stream events are structured (not pre‑encoded); your sink should JSON‑encode them for transport.
- For cross‑process UIs, prefer a message bus sink (e.g., Pulse) wired via `WithStream`.

## Pointers
- DSL reference: `docs/dsl.md`
- Runtime guide: `docs/runtime.md`
- Quickstart example: `quickstart/README.md`
- MCP integration: see `codegen/mcp` and `runtime/mcp`
- Features (memory, session, stream, model clients): `features/*`

