# Goa‑AI: Design‑First Agentic Systems in Go

Build intelligent, tool‑wielding agents with the confidence of strong types and the power of
durable execution. Goa‑AI brings the design‑first philosophy you love from Goa to the world of AI
agents—declare your agents, toolsets, and policies in a clean DSL, and let code generation handle
the rest.

No more hand‑rolled JSON schemas. No more brittle tool wiring. No more wondering if your agent will
survive a restart. Just elegant designs that compile into production‑grade systems.

## Why Goa‑AI?

| Challenge                            | How Goa‑AI Helps                                                    |
|--------------------------------------|---------------------------------------------------------------------|
| **LLM workflows feel fragile**       | Type‑safe tool payloads with validations and examples—no ad‑hoc JSON guessing games |
| **Long‑running agents crash**        | Durable orchestration with automatic retries, time budgets, and deterministic replay |
| **Composing agents is messy**        | First‑class agent‑as‑tool composition, even across processes, with unified history |
| **Schema drift haunts you**          | Generated codecs and registries keep everything in sync—change the DSL, regenerate, done |
| **Observability is an afterthought** | Built‑in streaming, transcripts, logs, metrics, and traces from day one |
| **MCP integration is manual**        | Generated wrappers turn MCP servers into typed toolsets automatically |

## The Mental Model

```
DSL → Codegen → Runtime → Engine + Features
```

Think of it as a pipeline from intention to execution:

1. **DSL** (`goa-ai/dsl`) — Express what you want: agents, tools, policies. Clean, declarative,
   version‑controlled.

2. **Codegen** (`codegen/agent`, `codegen/mcp`) — Transform your design into typed Go packages:
   tool specs, codecs, workflow definitions, registry helpers. Lives under `gen/`—never edit by
   hand.

3. **Runtime** (`runtime/agent`, `runtime/mcp`) — The workhorse that executes your agents:
   plan/execute loops, policy enforcement, memory, sessions, streaming, telemetry, and MCP
   integration.

4. **Engine** (`runtime/agent/engine`) — Swap backends without changing code. In‑memory for fast
   iteration; Temporal for production durability.

5. **Features** (`features/*`) — Plug in what you need: Mongo for memory/sessions/runs, Pulse for
   real‑time streams, Bedrock/OpenAI/Gateway model clients, policy engines.

## Ways to Work

### Fast Iteration (Single Process)

Spin up the in‑memory engine, wire a stub planner, and iterate at the speed of thought. No external
dependencies, no deployment ceremony—just ideas becoming reality.

### Production Ready (Worker/Client Split)

Workers poll for tasks with a durable Temporal engine. Clients submit runs through generated typed
APIs. Runs survive restarts, scale horizontally, and replay deterministically.

### Powerful Composition (Agent‑as‑Tool)

One agent exports a toolset; another consumes it. The nested agent executes inline within the same
workflow history—single transaction, unified debugging, elegant composition.

### External Tools (MCP Toolsets)

Reference MCP servers in your DSL and get generated registries with typed schemas, codecs, transport
handling, retries, and tracing baked in.

### Full Observability (Streaming & Telemetry)

Configure a memory store and stream sink once. The runtime automatically persists transcripts,
publishes real‑time events, and instruments everything with OTEL‑aware logging, metrics, and traces.

## Toolsets: Where the Magic Happens

Toolsets are owned by Goa services—agents, MCP, and custom executors are consumers or
implementations. The DSL keeps everything symmetric with `Toolset`, `Tool`, `BindTo`, `Use`, and
`Export`.

### Service‑Owned Toolsets

Declare tools with `Toolset("name", func() { ... })`. Bind them to Goa service methods or provide
custom executors. Codegen produces per‑toolset specs, types, and codecs under
`gen/<service>/tools/<toolset>/`.

Agents that `Use` these toolsets get typed call builders and executor factories—just wire up your
service client and go.

### Agent‑Implemented Toolsets (Agent‑as‑Tool)

Define tools in an `Export` block, and other agents can `Use` them seamlessly. Ownership stays with
the service; the agent provides the implementation.

Codegen emits provider‑side helpers with `NewRegistration` and typed builders, plus consumer‑side
helpers for agents using the exported toolset.

### One Unified Tool Catalog

No matter how tools are wired—service methods, custom executors, or nested agents—`Use` merges
everything into a single, coherent catalog. Your planner sees one clean universe of tools.

### Tool Schemas JSON

Every agent gets a backend‑agnostic JSON catalogue at:

```text
gen/<service>/agents/<agent>/specs/tool_schemas.json
```

Each entry contains the canonical tool ID with full JSON Schemas:

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

Schemas derive from the same DSL as your generated specs and codecs. If schema generation fails,
`goa gen` fails fast—no silent drift between runtime contracts and the JSON catalogue.

## Your First Agent in Five Minutes

### 1. Design (design/design.go)

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
        Use("helpers", func() {
            Tool("answer", "Answer a simple question", func() {
                Args(Ask)
                Return(Answer)
            })
        })
		RunPolicy(func() {
			DefaultCaps(MaxToolCalls(2), MaxConsecutiveFailedToolCalls(1))
			TimeBudget("15s")
		})
	})
})
```

### 2. Generate

```bash
goa gen example.com/quickstart/design
```

### 3. Run (cmd/demo/main.go)

```go
package main

import (
	"context"
	"fmt"

	chat "example.com/quickstart/gen/orchestrator/agents/chat"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/runtime"
)

// A tiny planner: always replies, no tools (perfect for first run)
type StubPlanner struct{}

func (p *StubPlanner) PlanStart(
	ctx context.Context,
	in *planner.PlanInput,
) (*planner.PlanResult, error) {
	return &planner.PlanResult{
		FinalResponse: &planner.FinalResponse{
			Message: &model.Message{
				Role:  model.ConversationRoleAssistant,
				Parts: []model.Part{model.TextPart{Text: "Hello from Goa‑AI!"}},
			},
		},
	}, nil
}

func (p *StubPlanner) PlanResume(
	ctx context.Context,
	in *planner.PlanResumeInput,
) (*planner.PlanResult, error) {
	return &planner.PlanResult{
		FinalResponse: &planner.FinalResponse{
			Message: &model.Message{
				Role:  model.ConversationRoleAssistant,
				Parts: []model.Part{model.TextPart{Text: "Done."}},
			},
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
		[]*model.Message{{
			Role:  model.ConversationRoleUser,
			Parts: []model.Part{model.TextPart{Text: "Say hi"}},
		}},
		runtime.WithSessionID("session-1"),
	)
	if err != nil {
		panic(err)
	}
	fmt.Println("RunID:", out.RunID)
	// Extract text from the final message parts
	if out.Final != nil {
		for _, p := range out.Final.Parts {
			if tp, ok := p.(model.TextPart); ok {
				fmt.Println("Assistant:", tp.Text)
			}
		}
	}
}
```

**Want durability?** Just swap in a Temporal engine:

```go
rt := runtime.New(runtime.WithEngine(temporalEngine))
```

Then use `Start/Wait` for asynchronous runs with task queues, memos, and search attributes.

## Under the Hood

### The Plan → Execute → Resume Loop

1. **Start** — The runtime spins up a workflow for your agent (in‑memory or Temporal)
2. **Plan** — Your planner's `PlanStart` receives the conversation and decides: final answer or
   tool calls?
3. **Execute** — Tool calls run through generated codecs, validated and type‑safe
4. **Resume** — `PlanResume` gets tool results; the loop continues until a final response or policy
   limits hit
5. **Stream** — Events flow to UIs; transcripts persist if configured

### Policies Keep Things Sane

Per‑turn enforcement of:

- Maximum tool calls
- Consecutive failure limits
- Time budgets
- Tool allowlists via policy engines

### Three Flavors of Tool Execution

| Type                | How It Works                                                                         |
|---------------------|--------------------------------------------------------------------------------------|
| **Native toolsets** | Your implementations + generated codecs = typed, validated tools                     |
| **Agent‑as‑tool**   | `ExecuteAgentInline` runs a nested agent deterministically within the same workflow  |
| **MCP toolsets**    | Generated wrappers handle JSON schemas, transport (HTTP/SSE/stdio), retries, tracing |

MCP callers in `runtime/mcp` support multiple transports:

- **`StdioCaller`** — Spawns MCP server as subprocess, communicates via stdin/stdout
- **`HTTPCaller`** — HTTP POST to MCP endpoints
- **`SSECaller`** — Server‑Sent Events for streaming MCP responses

All callers implement the `Caller` interface and include automatic retry (`runtime/mcp/retry`) and
distributed tracing.

### Memory, Streaming & Telemetry

The hook bus publishes events (`tool_start`, `tool_result`, `assistant_message`, ...) that:

- **Memory/session stores** (e.g., Mongo) subscribe to for transcript persistence
- **Stream sinks** (e.g., Pulse) carry to real‑time UIs
- **OTEL instrumentation** captures for logs, metrics, and traces

### Engine Abstraction

| Engine        | Best For                                                       |
|---------------|----------------------------------------------------------------|
| **In‑memory** | Fast dev loops, no external dependencies                       |
| **Temporal**  | Durable execution, replay, retries, signals, horizontal scaling |

### Human‑in‑the‑Loop (Pause & Resume)

Agents can pause mid‑run to request human input or external tool results:

- **Await Clarification** — Planner returns `Await.Clarification` when it needs user input
  (missing fields, ambiguous request). The runtime publishes an event and pauses.
- **Await External Tools** — Planner requests out‑of‑band tool execution; the runtime pauses
  until results arrive via signal.
- **Pause/Resume Signals** — Workflows accept `SignalPause` and `SignalResume` for manual
  intervention. Use `SignalProvideClarification` or `SignalProvideToolResults` to deliver answers.

The `interrupt` package (`runtime/agent/interrupt`) provides the `Controller` that drains signals
and exposes helpers for the workflow loop.

### Hook Bus (Internal Event Backbone)

The hook bus (`runtime/agent/hooks`) is the internal pub/sub backbone for runtime observability:

- **Publishers**: Workflows, planners, tool executors emit events (`run_started`, `tool_call_scheduled`,
  `assistant_message`, `thinking_block`, etc.)
- **Subscribers**: Memory stores, stream sinks, telemetry adapters receive events and react
- **Decoupling**: Producers don't know about consumers; add observability without touching core logic

Stream sinks bridge hook events to client‑facing formats via `stream.Subscriber`.

### Transcript Ledger

The transcript ledger (`runtime/agent/transcript`) maintains a provider‑precise record of the
conversation needed to rebuild model payloads exactly:

- **Provider Fidelity** — Preserves ordering and shape required by providers (thinking → tool_use →
  tool_result)
- **Stateless API** — Pure methods safe for workflow replay
- **Provider‑Agnostic Storage** — Converts to/from provider formats at edges

Use the ledger when you need deterministic conversation replay or provider‑specific payload
reconstruction.

### Run Store

The run store (`runtime/agent/run`) persists run metadata (status, timestamps, agent ID, session)
separate from memory transcripts:

- **Interface**: `run.Store` with `Upsert` and `Load`
- **In‑memory**: `run/inmem` for development
- **Mongo**: `features/run/mongo` for production persistence

Configure via `runtime.WithRunStore(store)`.

## Best Practices

**Design first** — Put all agent and tool schemas in the DSL. Add examples and validations. Let
codegen own schemas and codecs.

**Never hand‑encode** — Use generated codecs and clients everywhere. Avoid `json.Marshal`/
`Unmarshal` for tool payloads.

**Keep planners focused** — Planners decide *what* (final answer vs. which tools). Tool
implementations handle *how*.

**Split client from worker** — Register agents on workers; use generated typed clients from other
processes to submit runs.

**Compose with export/use** — Prefer agent‑as‑tool over brittle cross‑service contracts. Single
history, unified debugging.

**Regenerate often** — DSL change → `goa gen` → lint/test → run. Never edit `gen/` manually.

### Advertising Tools to Planners

Use `specs.AdvertisedSpecs()` from `gen/<svc>/agents/<agent>/specs` to pass tool specs to the model.
This keeps IDs and schemas aligned with your design and eliminates manual lists.

## Temporal Runtime Flow (Deep Dive)

For those who want the full picture of how execution flows through the system.

### 1. Client Invocation

Use the generated `NewClient(rt)` to get a `runtime.AgentClient`, then:

- **Synchronous**: `Run(ctx, []*model.Message, ...runtime.RunOption)` — start and wait
- **Asynchronous**: `Start(ctx, ...opts)` → `engine.WorkflowHandle` → `Wait/Signal/Cancel`

**RunOptions** let you configure per‑run behavior:

| Option                              | Purpose                      |
|-------------------------------------|------------------------------|
| `WithSessionID(string)`             | Required session identifier  |
| `WithTaskQueue(string)`             | Route to specific workers    |
| `WithMemo(map[string]any)`          | Attach metadata              |
| `WithSearchAttributes(map[string]any)` | Enable queries            |
| `WithPerTurnMaxToolCalls(int)`      | Override DSL defaults        |
| `WithRunMaxToolCalls(int)`          | Cap total tool calls         |
| `WithRunTimeBudget(duration)`       | Set time limits              |
| `WithRestrictToTool(tools.Ident)`   | Limit available tools        |
| `WithAllowedTags([]string)`         | Filter by tags               |

### 2. Engine Start

`AgentClient.Start` calls `runtime.startRun`, which resolves the agent and delegates to
`startRunOn`. This constructs an `engine.WorkflowStartRequest` with:

- `ID` (generated if absent)
- `Workflow` (from registration)
- `TaskQueue`, `Input` (`*runtime.RunInput`)
- Optional `Memo`, `SearchAttributes`, `RetryPolicy`

`Engine.StartWorkflow` returns an `engine.WorkflowHandle` for later signaling.

### 3. Worker Execution

During registration, generated code calls `rt.RegisterAgent(ctx, runtime.AgentRegistration{...})`,
which:

- Registers the workflow via `engine.WorkflowDefinition`
- Registers activities: `PlanStartActivityHandler`, `PlanResumeActivityHandler`,
  `ExecuteToolActivityHandler`

The engine invokes the workflow handler, which calls `rt.ExecuteWorkflow`.

### 4. The Plan/Execute/Resume Loop

`ExecuteWorkflow(wfCtx, *RunInput)`:

1. Publishes `run_started`, initializes caps/time budget
2. Calls `runPlanActivity` for the first planner turn
3. Enters `runLoop`:
   - Enforce time budget and interrupts
   - If `ToolCalls` present → `executeToolCalls`
   - If `Await` present → publish and pause for signals
   - If `FinalResponse` present → complete

### 5. Tool Execution

`executeToolCalls` routes each call:

| Path         | When           | How                                                               |
|--------------|----------------|-------------------------------------------------------------------|
| **Activity** | Default        | JSON‑encode via codec, schedule `ExecuteToolActivity`, collect futures |
| **Inline**   | Agent‑as‑tool  | Execute synchronously in workflow context, publish results directly |

`ExecuteToolActivity` decodes payloads, calls the toolset's `Execute`, re‑encodes results.
Validation errors become structured `planner.RetryHint` for planners.

### 6. Completion

`runLoop` returns `*runtime.RunOutput` containing:

- `Final` (the assistant's `*model.Message`)
- `ToolEvents` (all tool results in execution order)
- `Notes` and aggregated `Usage`

The runtime sets final status and returns to the client.

## Agent‑as‑Tool: Inline Composition

Exported toolsets get first‑class helpers for registering agents as tools. Nested agents execute
inline within the parent workflow history.

### Generated Provider Helpers

- **Tool IDs** (fully qualified) and type aliases for codecs
- **`New<Agent>ToolsetRegistration(rt *runtime.Runtime)`** — creates registration with `Inline: true`
  and strong‑contract routing
- **`NewRegistration(rt, systemPrompt, ...runtime.AgentToolOption)`** — configure per‑tool
  text/templates
- **Typed call builders** like `New<Tool>Call(args, ...CallOption)`

### Runtime Behavior

1. Consumer registers with `rt.RegisterToolset(reg)`
2. When the runtime sees an `Inline` toolset call:
   - Publishes a scheduled event
   - Injects `engine.WorkflowContext` into `ctx`
   - Calls the toolset's `Execute`
3. The executor builds messages from the payload (with optional templates) and calls:
   - `rt.ExecuteAgentInline` (local agent), or
   - `rt.ExecuteAgentInlineWithRoute` (cross‑process)
4. Nested agent runs full plan/execute/resume inline
5. Results adapt back to `planner.ToolResult` for the parent

### Key Types

| Type                                   | Purpose                                          |
|----------------------------------------|--------------------------------------------------|
| `runtime.ToolsetRegistration`          | Name, Specs, Execute, TaskQueue, Inline flag     |
| `runtime.AgentRoute`                   | ID, WorkflowName, DefaultTaskQueue               |
| `runtime.AgentToolConfig`              | AgentID or Route, activity names, prompts, templates |
| `runtime.NewAgentToolsetRegistration`  | Build inline registrations                       |
| `runtime.ExecuteAgentInline[WithRoute]`| Execute nested runs                              |

## Integration Points

### Your Code

- Implement `planner.Planner` (`PlanStart`, `PlanResume`)
- Provide tool executors via `runtime.ToolCallExecutor`
- Configure runtime: `runtime.New(WithEngine, WithMemoryStore, WithRunStore, WithHooks, WithStream,
  WithLogger, WithMetrics, WithTracer, WithWorker)`
- Register models: `rt.RegisterModel("model-id", client)`
- Submit runs via generated clients
- For agent‑as‑tool: configure text/templates with `runtime.WithText`, `runtime.WithTemplate`

### Generated Code

- Per agent: `AgentID`, `WorkflowName`, `DefaultTaskQueue`, activity names
- `Register<Agent>(ctx, rt, Config)` — full registration
- `NewWorker(...runtime.WorkerOption)` — worker configuration
- `Route()` and `NewClient(rt)` — remote access
- Per toolset: `New<Agent><Toolset>ToolsetRegistration`

### Runtime/Library

- `runtime.RegisterAgent`, `runtime.RegisterToolset`
- `runtime.Client`, `runtime.ClientFor`, `runtime.MustClient`, `runtime.MustClientFor`
- `runtime.AgentClient` with `Run/Start`
- `engine.Engine`, `engine.WorkflowDefinition`, `engine.ActivityDefinition`, `engine.WorkflowHandle`
- Activities: `PlanStartActivity`, `PlanResumeActivity`, `ExecuteToolActivity`
- Inline composition: `runtime.ExecuteAgentInline`, `runtime.ExecuteAgentInlineWithRoute`
- Tool infrastructure: `tools.ToolSpec`, `tools.JSONCodec`
- Tool errors: `toolerrors.ToolError` for structured error reporting
- Hooks: `hooks.Bus`, `hooks.Subscriber`, `hooks.Event` for runtime observability
- Interrupts: `interrupt.Controller` for pause/resume signal handling

## Streaming for UIs

Push real‑time events to WebSocket/SSE or a message bus for live agent experiences.

### Implement a Stream Sink

```go
type MySink struct{}

func (s *MySink) Send(ctx context.Context, event stream.Event) error {
    // Handle: assistant_reply, planner_thought, tool_start, 
    //         tool_update, tool_end, await_clarification, 
    //         await_external_tools, usage
    return nil
}

func (s *MySink) Close(ctx context.Context) error {
    return nil
}
```

### Global Broadcast (All Runs)

```go
sink := &MySink{}
rt := runtime.New(runtime.WithStream(sink))
```

### Per‑Run Streaming (Per UI Tab)

```go
closeFn, err := rt.SubscribeRun(ctx, runID, sink)
if err != nil { /* handle */ }
defer closeFn() // unsubscribes and closes
```

### Manual Bridge (Direct Bus Access)

```go
import "goa.design/goa-ai/runtime/agent/stream/bridge"

sub, _ := bridge.Register(rt.Bus, sink)
defer sub.Close()
```

**Tips**:

- Stream events are structured, not pre‑encoded—JSON‑encode for transport
- For cross‑process UIs, wire a message bus sink (e.g., Pulse) via `WithStream`

## Learn More

| Topic           | Resource                                           |
|-----------------|----------------------------------------------------|
| DSL reference   | `docs/dsl.md`                                      |
| Runtime guide   | `docs/runtime.md`                                  |
| Quickstart      | `quickstart/README.md`                             |
| MCP integration | `codegen/mcp` and `runtime/mcp`                    |
| Features        | `features/*` (memory, session, run, stream, model clients) |

### Feature Packages

| Package                  | Purpose                                                |
|--------------------------|--------------------------------------------------------|
| `features/memory/mongo`  | Mongo‑backed memory store for transcripts              |
| `features/session/mongo` | Mongo‑backed session store for multi‑turn state        |
| `features/run/mongo`     | Mongo‑backed run store for run metadata                |
| `features/stream/pulse`  | Pulse message bus sink for real‑time streaming         |
| `features/model/bedrock` | AWS Bedrock model client (Claude, etc.)                |
| `features/model/openai`  | OpenAI‑compatible model client                         |
| `features/model/gateway` | Remote model gateway for centralized model serving     |
| `features/policy/basic`  | Basic policy engine for tool filtering and caps        |

---

*Build agents that are a joy to develop and a breeze to operate. Welcome to Goa‑AI.*
