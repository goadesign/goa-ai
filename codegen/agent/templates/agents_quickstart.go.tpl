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

{{- range .Services }}
* **Service `{{ .Service.Name }}`:**
    {{- if .Agents }}
    {{- range .Agents }}
    * **Agent `{{ .Name }}`** (ID: `{{ .ID }}`):
        * **Mission:** *{{ .Description }}*
        * **Uses Toolsets:**
            {{- if .UsedToolsets }}
            {{- range .UsedToolsets }}
            * `{{ .QualifiedName }}`{{ if .Expr.External }} (from remote MCP service `{{ .Expr.MCPService }}.{{ .Name }}`){{ end }}
            {{- end }}
            {{- else }}*none*
            {{- end }}
        * **Exports Toolsets:**
            {{- if .ExportedToolsets }}
            {{- range .ExportedToolsets }}
            * `{{ .QualifiedName }}`
            {{- end }}
            {{- else }}*none*
            {{- end }}
        * **Run Policy:**
            * Max Tool Calls: `{{ .RunPolicy.Caps.MaxToolCalls }}`
            * Max Consecutive Failures: `{{ .RunPolicy.Caps.MaxConsecutiveFailedToolCalls }}`
            * Time Budget: `{{ .RunPolicy.TimeBudget }}`
            * Interrupts Allowed: `{{ .RunPolicy.InterruptsAllowed }}`
    {{- end }}
    {{- else }}
    * This service doesn't define any agents itself, but it might provide tools for others to use!
    {{- end }}
{{- end }}

---

## 2. 🚀 The 3-Step Liftoff: Your First Agent Run

Let's get an agent running in just a few lines of code, without worrying about servers or HTTP yet. This is the fastest way to see your agent in action.

```go
package main

import (
    "context"
    "fmt"

    // The core Goa-AI runtime and planner interfaces
    "goa.design/goa-ai/runtime/agent/runtime"
    "goa.design/goa-ai/runtime/agent/planner"

    // === Your Generated Agent Packages ===
    // (Goa generated these based on your design)
    {{- range .Services }}{{- range .Agents }}
    {{ .PackageName }} "{{ .ImportPath }}"
    {{- end }}{{- end }}
)

// A simple "brain" for our agent. It just says hello for now.
// We'll make this smarter in the next section!
type StubPlanner struct{}
func (p *StubPlanner) PlanStart(ctx context.Context, in planner.PlanInput) (*planner.PlanResult, error) {
    return &planner.PlanResult{
		FinalResponse: &planner.FinalResponse{
			Message: planner.AgentMessage{
				Role:    "assistant",
				Content: "Hello!",
			},
		},
	}, nil
}
func (p *StubPlanner) PlanResume(ctx context.Context, in planner.PlanResumeInput) (*planner.PlanResult, error) {
    return &planner.PlanResult{
		FinalResponse: &planner.FinalResponse{
			Message: planner.AgentMessage{
				Role:    "assistant",
				Content: "Done.",
			},
		},
	}, nil
}

func main() {
    // 1. Create the Runtime
    // This is the central engine for all your agents.
    rt := runtime.New()

    // 2. Register Your Agent(s)
    // Let the runtime know about the agents it can manage.
    {{- $first := true -}}
    {{- range .Services }}{{- range .Agents }}
    {
        cfg := {{ .PackageName }}.{{ .StructName }}Config{
            Planner: &StubPlanner{},
            // We'll add tool configurations here later on.
        }
        if err := {{ .PackageName }}.Register{{ .StructName }}(context.Background(), rt, cfg); err != nil {
            panic(err)
        }
    }
    {{- if $first }}{{ $first = false }}{{ end }}
    {{- end }}{{- end }}

    // 3. Run it!
    // Let's invoke our first agent and see what it says using AgentClient.
    fmt.Println("🚀 Invoking agent...")
    client := {{ (index (index .Services 0).Agents 0).PackageName }}.NewClient(rt)
    out, err := client.Run(
        context.Background(),
        []planner.AgentMessage{
			{
				Role:    "user",
				Content: "Hi there!",
			},
		},
        runtime.WithSessionID("my-first-session"), // A session ID is required!
    )
    if err != nil {
		panic(err)
	}

    fmt.Println("✅ Success!")
    fmt.Println("RunID:", out.RunID)
    fmt.Println("Assistant says:", out.Final.Content)
}
```

---

## 3. Meet Your Agents 🤖

Here are the detailed cheat sheets for each agent you designed.

{{- range .Services }}{{- range .Agents }}
<details>
<summary><strong>Agent: <code>{{ .Name }}</code></strong> (ID: <code>{{ .ID }}</code>)</summary>

* **Package:** `{{ .ImportPath }}`
* **Directory:** `{{ .Dir }}`
* **Config Struct:** `{{ .StructName }}Config`
* **Register Function:** `Register{{ .StructName }}(ctx, rt, cfg)`
* **How to Run:**
    * **Synchronous (wait for result):**
        ```go
        client := {{ .PackageName }}.NewClient(rt)
        out, err := client.Run(ctx, messages, runtime.WithSessionID(sessionID))
        ```
    * **Asynchronous (get a handle):**
        ```go
        client := {{ .PackageName }}.NewClient(rt)
        handle, err := client.Start(ctx, messages, runtime.WithSessionID(sessionID))
        ```
* **Workflow Name:** `{{ .Runtime.Workflow.Name }}` (Queue: `{{ .Runtime.Workflow.Queue }}`)

#### Minimal Configuration

{{- $agent := . -}}

```go
cfg := {{ .PackageName }}.{{ .StructName }}Config{
    Planner: myPlanner,
    {{- if .MCPToolsets }}
    MCPCallers: map[string]mcpruntime.Caller{
        {{- range .MCPToolsets }}
        // Expects a caller for the '{{ .SuiteName }}' suite
        {{ $agent.PackageName }}.{{ .ConstName }}: your_mcp_caller_for_{{ .SuiteName | ToLower }},
        {{- end }}
    },
    {{- end }}
}
```
</details>
{{- end }}{{- end }}

---

## 4. 🧠 The Planner: Giving Your Agent a Brain

The `Planner` is where your agent's intelligence lives. It connects to an LLM to decide what to do next. The `StubPlanner` above is great for testing, but here's the correct interface for a real implementation.

```go
type MySmartPlanner struct{}

// PlanStart is called at the beginning of a run.
func (p *MySmartPlanner) PlanStart(ctx context.Context, in planner.PlanInput) (*planner.PlanResult, error) {
    // 1. Get an LLM client from the runtime.
    // model, _ := in.Agent.ModelClient("openai")
    
    // 2. Build a prompt from in.Messages.
    
    // 3. Call the LLM and decide whether to call tools or give a final answer.
    return &planner.PlanResult{
        FinalResponse: &planner.FinalResponse{
            Message: planner.AgentMessage{
				Role:    "assistant",
				Content: "I'm ready to help!",
			},
        },
    }, nil
}

// PlanResume is called after tools have run, giving the agent new information.
func (p *MySmartPlanner) PlanResume(ctx context.Context, in planner.PlanResumeInput) (*planner.PlanResult, error) {
    // 1. Inspect the tool results from in.ToolResults.
    // 2. Build a new prompt including the tool results.
    // 3. Call the LLM to decide what to do next.
    return &planner.PlanResult{
        FinalResponse: &planner.FinalResponse{
            Message: planner.AgentMessage{
				Role:    "assistant",
				Content: "The tools have run. Here's what I found...",
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
- Typed tool specs/codecs under `gen/<svc>/agents/<agent>/specs/<toolset>/`
- Transform helpers (when shapes are compatible): `transforms.go`
- An application-owned executor stub under `internal/agents/<agent>/toolsets/<toolset>/execute.go`

Wire executors explicitly in your bootstrap (already done in `internal/agents/bootstrap/bootstrap.go`). Implement the stub’s `Execute` function to:
- Switch on `call.Name` for each tool
- Decode `call.Payload` to typed args using the generated codec
- Optionally use `ToMethodPayload_<Tool>`/`ToToolReturn_<Tool>` transforms
- Call your service client and return a `planner.ToolResult`

Minimal executor scaffold:

```go
// internal/agents/<agent>/toolsets/<toolset>/execute.go
package <toolset>

import (
    "context"
    "goa.design/goa-ai/runtime/agent/planner"
    "goa.design/goa-ai/runtime/agent/runtime"
    specs "<module>/gen/<svc>/agents/<agent>/specs/<toolset>"
)

func Execute(ctx context.Context, meta *runtime.ToolCallMeta, call *planner.ToolRequest) (*planner.ToolResult, error) {
    if call == nil {
        return &planner.ToolResult{Error: planner.NewToolError("tool request is nil")}, nil
    }
    if meta == nil {
        return &planner.ToolResult{Error: planner.NewToolError("tool call meta is nil")}, nil
    }
    switch call.Name {
    case "<svc>.<toolset>.<tool>":
        var args specs.<ToolPayload>
        if err := specs.Unmarshal<ToolPayload>(call.Payload, &args); err != nil {
            return &planner.ToolResult{
				Error: planner.NewToolError("invalid payload"),
			}, nil
        }
        // Optionally: mp, _ := ToMethodPayload_<Tool>(args)
        // TODO: invoke your service client, map result via ToToolReturn_<Tool>
        return &planner.ToolResult{
			Result: map[string]any{
				"status": "ok",
			},
		}, nil
    }
    return &planner.ToolResult{
		Error: planner.NewToolError("unknown tool"),
	}, nil
}
```

#### Connecting to Remote Services (MCP)

If your agent uses tools from another service via MCP (`UseMCPToolset`):

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

{{- range .Services }}
{{- range .Agents }}

### Agent `{{ .Name }}` Toolsets

* **Tools this agent can USE:**
    {{- if .UsedToolsets }}
    {{- range .UsedToolsets }}
    * **`{{ .QualifiedName }}`** {{ if .Expr.External }}(MCP Suite: `{{ .Expr.MCPService }}.{{ .Name }}`){{ end }}
        {{- if .Tools }}
        {{- range .Tools }}
        * **Tool: `{{ .QualifiedName }}`**
            * *{{ .Description }}*
        {{- end }}
        {{- end }}
    {{- end }}
    {{- else }}
    * *This agent does not use any toolsets.*
    {{- end }}
* **Tools this agent EXPORTS for others to use:**
    {{- if .ExportedToolsets }}
    {{- range .ExportedToolsets }}
    * **`{{ .QualifiedName }}`**
    {{- end }}
    {{- else }}
    * *This agent does not export any toolsets.*
    {{- end }}
{{- end }}
{{- end }}
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
    runtime.WithText(<agenttools>.ToolXYZ, "Please perform the following task: {{"{{"}} . {{"}}"}}"),
)
if err != nil { panic(err) }

// Now this toolset is available in the runtime for other agents to use!
if err := rt.RegisterToolset(reg); err != nil { panic(err) }
```

---

## 8. Ready for Prime Time: Advanced Features 🔭

* **Asynchronous Runs & Streaming:** Use `client.Start()` to get a workflow handle. This is great for long-running tasks or streaming updates back to a UI.
* **Interrupts (Human-in-the-Loop):** If your policy allows it, you can pause and resume agent runs with `rt.PauseRun()` and `rt.ResumeRun()`.
* **Policies & Caps:** The `RunPolicy` in your design (max tool calls, time budgets) is automatically enforced by the runtime.
* **Persistence & Observability:** The `runtime.New` function accepts `runtime.Options` to configure production-grade components like a Temporal engine, MongoDB for memory, and telemetry hooks.

```go
// Example of production-ready runtime options
rt := runtime.New(runtime.Options{
    // Engine: myTemporalEngine,
    // MemoryStore: myMongoMemoryStore,
    // RunStore: myMongoRunStore,
    // Stream: myEventStreamSink,
})
```

---

## 9. 📜 The Golden Rules: Working with Codegen

* ✍️ **Design First:** Always make changes in your `design/*.go` files.
* 🔄 **Regenerate:** Run `goa gen <module>/design` to apply your changes.
* 🚫 **Hands Off `gen/`:** Never edit the `gen/` directory by hand. Your changes will be overwritten!

---

## 10. 🤔 Stuck? Common Questions & Fixes

* **Error: "runtime not initialized"**
* **Fix:** Ensure you register agents with the same runtime instance you use to start runs.
* **Error: "agent not registered"**
    * **Fix:** Check that `Register<AgentName>(...)` was called successfully for that agent before you tried to run it.
* **Error: "session id is required"**
    * **Fix:** Always provide a unique, non-empty string for the `sessionID` when calling `agent.Run(...)`.
* **Error: "mcp caller is required for <suite>"**
    * **Fix:** Your agent's config is missing an entry in the `MCPCallers` map for the specified toolset ID. See section 5.
* **Agent-as-Tool isn't working?**
    * **Fix:** Ensure you've provided `WithText` or `WithTemplate` for **every single tool** in the exported toolset when calling `NewRegistration`.
