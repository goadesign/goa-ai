# Welcome to Your Goa-AI Agents! 👋

This guide is your personal co-pilot, generated specifically to help you bring your new AI agents to life. We'll go from the code Goa just created to a running agent in a few simple steps.

> **A Quick Note on This File:**
>
> - **Want to hide me?** No problem! Add `DisableAgentDocs()` to your `API` design and I won't be generated next time.
> - **Safety First:** It's safe to delete this file. It will reappear, updated, after the next `goa gen`.
> - **Golden Rule:** Never edit the `gen/` directory directly. Your design files are the source of truth!

---

## 1. Your Design, At a Glance ✨

Here’s a map of what Goa-AI just built for you based on your `design/*.go` files.
* **Service `orchestrator`:**
    * **Agent `chat`** (ID: `orchestrator.chat`):
        * **Mission:** *Friendly Q&A assistant*
        * **Uses Toolsets:**
            * `orchestrator.helpers`
        * **Exports Toolsets:***none*
        * **Run Policy:**
            * Max Tool Calls: `2`
            * Max Consecutive Failures: `1`
            * Time Budget: `15s`
    * **Direct Completions:**
        * `draft_task`

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

---

## 3. Meet Your Agents 🤖

Here are the detailed cheat sheets for each agent you designed.
<details>
<summary><strong>Agent: <code>chat</code></strong> (ID: <code>orchestrator.chat</code>)</summary>

* **Package:** `example.com/quickstart/gen/orchestrator/agents/chat`
* **Directory:** `gen/orchestrator/agents/chat`
* **Config Struct:** `ChatAgentConfig`
* **Register Function:** `RegisterChatAgent(ctx, rt, cfg)`
* **How to Run:**
    * **Sessions are first-class:** call `rt.CreateSession(ctx, sessionID)` once before you start any runs under that session ID.
    * **Synchronous (wait for result):**
        ```go
        client := chat.NewClient(rt)
        out, err := client.Run(ctx, sessionID, messages)
        ```
    * **Asynchronous (get a handle):**
        ```go
        client := chat.NewClient(rt)
        handle, err := client.Start(ctx, sessionID, messages)
        ```
* **Workflow Name:** `orchestrator.chat.workflow` (Queue: `orchestrator_chat_workflow`)

#### Minimal Configuration```go
cfg := chat.ChatAgentConfig{
    Planner: myPlanner,
}
```
</details>

---

## 4. 🧠 The Planner: Giving Your Agent a Brain

The `Planner` is where your agent's intelligence lives. It connects to an LLM to decide what to do next. The `StubPlanner` above is great for testing, but here's the correct interface for a real implementation.

```go
type MySmartPlanner struct{}

// PlanStart is called at the beginning of a run.
func (p *MySmartPlanner) PlanStart(ctx context.Context, in *planner.PlanInput) (*planner.PlanResult, error) {
    // 1. Get an LLM client from the runtime.
    // mc, _ := in.Agent.PlannerModelClient("bedrock")
    
    // 2. Build a prompt from in.Messages.
    
    // 3. Call the LLM and decide whether to call tools or give a final answer.
    return &planner.PlanResult{
        FinalResponse: &planner.FinalResponse{
            Message: &model.Message{
				Role:  model.ConversationRoleAssistant,
				Parts: []model.Part{model.TextPart{Text: "I'm ready to help!"}},
			},
        },
    }, nil
}

// PlanResume is called after tools have run, giving the agent new information.
func (p *MySmartPlanner) PlanResume(ctx context.Context, in *planner.PlanResumeInput) (*planner.PlanResult, error) {
    // 1. Inspect the tool results from in.ToolOutputs.
    // 2. Build a new prompt including the tool results.
    // 3. Call the LLM to decide what to do next.
    return &planner.PlanResult{
        FinalResponse: &planner.FinalResponse{
            Message: &model.Message{
				Role:  model.ConversationRoleAssistant,
				Parts: []model.Part{model.TextPart{Text: "The tools have run. Here's what I found..."}},
			},
        },
    }, nil
}
```

---

## 5. 🛠️ Giving Your Agents Tools

Your agents can do useful work by calling other parts of your system. Here's how to wire them up.

#### Local Service-Backed Tools (`BindTo`) — Executor-First

When your tool maps to a service method (via `BindTo`), Goa-AI generates:
- Service-owned tool specs/codecs under `gen/<service>/toolsets/<toolset>/`
- Agent-exported tool specs/codecs under `gen/<service>/agents/<agent>/exports/<toolset>/`
- One typed descriptor factory per tool (for example `SummarizeDocTool()`) pairing the tool identifier with its payload and result codecs, so planners, executors, and eval hooks decode tool JSON without restating the name-to-codec pairing fixed by the design
- Transform helpers (when shapes are compatible): `transforms.go`
- An application-owned executor stub under `internal/agents/<agent>/toolsets/<toolset>/execute.go`

Wire executors using the generated `RegisterUsedToolsets` helper:

```go
// After registering the agent, wire the toolset executors
err := <agentpkg>.RegisterUsedToolsets(ctx, rt,
    <agentpkg>.With<ToolsetName>Executor(myExecutor),
)
if err != nil { panic(err) }
```

Implement the executor's `Execute` function to:
- Switch on `call.Name` for each tool
- Decode `call.Payload` to typed args with the generated typed descriptor (`specs.<Tool>Tool().Payload.FromJSON`); when the tool declares `Inject(...)`, call `specs.Decode<Tool>(call.Payload, *meta, meta.Labels)` so generated code also supplies those server-owned fields
- Optionally use `Init<Tool>MethodPayload`/`Init<Tool>ToolResult` transforms
- Call your service client and return a `planner.ToolResult`

Minimal executor scaffold:

```go
// internal/agents/<agent>/toolsets/<toolset>/execute.go
package <toolset>

import (
    "context"
    "errors"
    "goa.design/goa-ai/runtime/agent/planner"
    "goa.design/goa-ai/runtime/agent/runtime"
    "goa.design/goa-ai/runtime/agent/tools"
    specs "<module>/gen/<service>/toolsets/<toolset>"
)

func Execute(ctx context.Context, meta *runtime.ToolCallMeta, call *runtime.ToolCall) (*runtime.ToolExecutionResult, error) {
    switch call.Name {
    case specs.<Tool>:
        // Decode with the generated typed descriptor: args is the typed
        // payload (e.g. *specs.<ToolPayload>), no type assertion needed.
        args, err := specs.<Tool>Tool().Payload.FromJSON(call.Payload)
        if err != nil {
            var issuer interface {
                Issues() []*tools.FieldIssue
            }
            var issues []*tools.FieldIssue
            if errors.As(err, &issuer) {
                issues = issuer.Issues()
            }
            return runtime.Executed(&planner.ToolResult{
                Name: call.Name,
                Failure: &planner.ToolFailure{
                    Kind: planner.FailureInvalidCall,
                    Error: planner.ToolErrorFromError(err),
                    Recovery: planner.RecoveryDirective{
                        Action: planner.RecoveryCorrectCall,
                        Issues: issues,
                    },
                },
            }), nil
        }
        // Call your service client with args and return the generated result
        // type. Method-backed providers generated in the owning service already
        // contain the required payload and result transforms.
        return runtime.Executed(&planner.ToolResult{
			Name:   call.Name,
			Result: &specs.<ToolReturn>{
				Status: "ok",
			},
		}), nil
    }
    return runtime.Executed(&planner.ToolResult{
        Name: call.Name,
        Failure: &planner.ToolFailure{
            Kind: planner.FailureInvalidCall,
            Error: planner.NewToolError("unknown tool"),
            Recovery: planner.RecoveryDirective{Action: planner.RecoveryReplan},
        },
	}), nil
}
```

#### Connecting to Remote Services (MCP)

If your agent uses tools from another service via MCP (`Use(MCPToolset(...))`):

1.  Get the generated Goa client for the remote service.
2.  Wrap it in an `mcpruntime.Caller`.
3.  Pass it to your agent's config, using the generated constant for the key.

```go
// 1. Get the generated Goa client for the remote service.
remoteClient := <jsonrpc_client_pkg>.NewClient(/* your endpoints */)

// 2. Wrap it in an MCP Caller.
caller := mcpruntime.NewCaller(remoteClient)

// 3. Supply it in the agent config.
cfg := <agentpkg>.<AgentConfig>{
    Planner: myPlanner,
    MCPCallers: map[string]mcpruntime.Caller{
        <agentpkg>.<ToolsetIDConst>: caller, // e.g., "assistant.assistant-mcp"
    },
}
```

---
<details>
<summary><strong>Click to see a detailed reference of your agent's toolboxes...</strong></summary>

## 6. Your Agent's Toolbox: A Reference

### Agent `chat` Toolsets

* **Tools this agent can USE:**
* **`orchestrator.helpers`** 
* **Tool: `helpers.answer`**
* *Answer a simple question*
* **Tools this agent EXPORTS for others to use:**
* *This agent does not export any toolsets.*
</details>

---

## 7. Agents Calling Agents (The `Exports` Keyword)

When an agent `Exports` a toolset, other agents can call it. Goa-AI generates a special `agenttools` package to make this easy.

```go
// In your main.go, register the exported toolset so others can find it.
reg, err := <agenttools>.NewRegistration(
    rt,
    "You are a helpful specialist assistant.",  // A system prompt for the nested agent (optional)
    // Configure per-tool content (optional). If omitted, the runtime builds a default
    // user message from the payload; override the builder with WithPromptBuilder.
    runtime.WithText(<agenttools>.ToolXYZ, "Please perform the following task: {{ . }}"),
)
if err != nil { panic(err) }

// Now this toolset is available in the runtime for other agents to use!
if err := rt.RegisterToolset(reg); err != nil { panic(err) }
```

---

## 8. 🧪 Evaluating Your Agents

An evaluation suite is a set of stable scenarios that exercise your agent and grade the outcome, so you can catch regressions before your users do. Your design declares the following suite:

### Suite `chat_quality` (agent `orchestrator.chat`)

Evaluates the chat agent end to end against the in-memory runtime.

* **`greeting_reply`** (tags: `smoke`): The agent produces a final assistant reply to a user question. Supply its typed input when constructing the suite in `cmd/chat_quality-evals`.

* **`helpers_contract`** (tags: `contract`): The helpers.answer tool contract is reachable from the agent.

Everything typed lives in `gen/evals/chat_quality/`: a `Hooks` interface with one method per scenario, an `Inputs` struct for typed inputs, and (for agent-attached suites) `MustToolContract` to assert against the agent's reachable tool contracts. `goa example` scaffolds an application-owned command at `cmd/chat_quality-evals/main.go` **once**; it is yours to edit and is never overwritten.

Implement each hook to run the real agent, then return the evidence to grade:

* **Checks** are deterministic facts: which tools were called, with which arguments, whether the run completed.
* **Claims** are plain-language statements about the reply ("the answer names the capital of Japan") graded by a model judge.

Don't recompute trajectory facts by hand. Bridge the runtime's event bus into
an `evidence.Collector` (package `goa.design/goa-ai/eval/evidence`) while the
agent runs, then declare expectations with the generated typed tool
descriptors — the predicates are compile-checked against the tool's actual
payload and result types:

```go
collector := evidence.NewCollector()
sub, err := streambridge.Register(rt.Bus, sink) // sink filters the scenario's session and calls collector.Consume
// ... run the agent ...
ev, err := collector.Finish()
expect := evidence.Expect{
    Tools: []evidence.Tool{
        evidence.ExpectCall(specs.<Tool>Tool(),
            func(p *specs.<ToolPayload>) error { /* assert arguments */ return nil },
            func(r *specs.<ToolReturn>) error { /* assert result */ return nil },
        ),
    },
}
return eval.Result{Checks: expect.Checks(ev), Claims: claims, Output: ev.Answer}, nil
```

`Expect` grades the causal trajectory (in-order subsequence by default,
`Exact: true` for call-for-call equality), forbidden tools, failure
classifications (`evidence.ExpectFailure`), pending operator confirmations
(`evidence.ExpectConfirmation`), and the terminal workflow phase.

```bash
go run ./cmd/chat_quality-evals                    # run every scenario, JSON report on stdout
go run ./cmd/chat_quality-evals -scenario <id>     # run selected scenarios (repeatable)
go run ./cmd/chat_quality-evals -tag <tag>         # run by tag (repeatable)
```

The command exits non-zero when any scenario fails, so it drops straight into CI. Deterministic-only suites can pass a `nil` judge; as soon as a hook returns claims, wire a real judge (see `goa.design/goa-ai/eval/judge`) backed by your model client.

---

## 9. Ready for Prime Time: Advanced Features 🔭

* **Sessions & Runs:** Sessions are explicit. Create them with `rt.CreateSession(ctx, sessionID)` and end them with `rt.DeleteSession(ctx, sessionID)`. Runs (`client.Run`/`client.Start`) require an active session.
* **Session-Owned Streaming (for UIs):** In production, stream consumers should attach to the **session-owned stream** (`session/<session_id>`) and filter by `run_id`. Close SSE when you observe a `run_stream_end` event for the attached run ID. Nested agent runs emit `child_run_linked` links and their own `run_stream_end`; parent runs only emit `run_stream_end` after all child runs have ended.
* **Asynchronous Runs:** Use `client.Start()` to get a workflow handle. This is great for long-running tasks, cancellation, and non-interactive integrations.
* **Human Input:** Clarifications, confirmations, and external results end the current workflow with a typed suspension. Keep the complete suspension in trusted server-side storage, atomically claim it once, and submit one answer with `client.StartContinuation()` to start the next workflow.
* **Policies & Caps:** The `RunPolicy` in your design (max tool calls, time budgets) is automatically enforced by the runtime.
* **Persistence & Observability:** The `runtime.New` function accepts `runtime.Options` to configure production-grade components like a Temporal engine, MongoDB for memory, and telemetry hooks.
* **Temporal DataConverter:** The Temporal engine always installs its strict, bounded data converter. Applications provide connection and namespace settings through `ClientOptions`; they cannot replace the workflow data contract.
* **Registries & Discovery:** When you declare registries and `FromRegistry(...)` toolsets in your DSL, Goa-AI generates typed registry HTTP clients under `gen/<svc>/registry/<name>/` plus per-toolset specs helpers (with `DiscoverAndPopulate`, `Specs`, and `RegistryToolsetID`) so you can discover tools at runtime and register executors using `runtime.ToolsetRegistration`.

```go
// Example of production-ready runtime options
rt := runtime.New(runtime.Options{
    // Engine: myTemporalEngine,
    // MemoryStore: myMongoMemoryStore,
    // Stream: myEventStreamSink,
})
```

Example: constructing a Temporal worker engine:

```go
import (
    "goa.design/goa-ai/runtime/agent/engine/temporal"
    "go.temporal.io/sdk/client"
)

eng, err := temporal.NewWorker(temporal.Options{
    ClientOptions: &client.Options{
        HostPort:      "127.0.0.1:7233",
        Namespace:     "default",
    },
    WorkerOptions: temporal.WorkerOptions{
        TaskQueue: "<service>_<agent>_workflow",
    },
})
if err != nil {
    panic(err)
}
defer eng.Close()
```

---

## 10. 📜 The Golden Rules: Working with Codegen

* ✍️ **Design First:** Always make changes in your `design/*.go` files.
* 🔄 **Regenerate:** Run `goa gen <module>/design` to apply your changes.
* 🚫 **Hands Off `gen/`:** Never edit the `gen/` directory by hand. Your changes will be overwritten!

---

## 11. 🤔 Stuck? Common Questions & Fixes

* **Error: "runtime not initialized"**
* **Fix:** Ensure you register agents with the same runtime instance you use to start runs.
* **Error: "agent not registered"**
    * **Fix:** Check that `Register<AgentName>(...)` was called successfully for that agent before you tried to run it.
* **Error: "session id is required"**
    * **Fix:** Always provide a unique, non-empty string for the `sessionID` when calling `agent.Run(...)`.
* **Error: "session not found"**
    * **Fix:** Sessions are explicit—call `rt.CreateSession(ctx, sessionID)` once before starting runs under that session ID.
* **Error: "mcp caller is required for <suite>"**
    * **Fix:** Your agent's config is missing an entry in the `MCPCallers` map for the specified toolset ID. See section 5.
* **Agent-as-Tool isn't working?**
    * **Fix:** Ensure you've provided `WithText` or `WithTemplate` for **every single tool** in the exported toolset when calling `NewRegistration`.
