# Goa-AI Runtime Usage in Aura

This document captures how Aura integrates with the refactored goa-ai runtime and codegen.

## Client vs Worker

- Client-only processes (submit runs): construct a runtime with a client-capable engine and do not register agents locally. Use the generated per-agent client:

```
rt := runtime.New(runtime.WithEngine(temporalClient))
client := chat.NewClient(rt)
out, err := client.Run(ctx, messages, runtime.WithSessionID("s-123"))
```

- Worker processes (execute runs): construct a runtime with a worker-capable engine, register agents (with real planners), and start engine workers.

```
eng := temporalengine.New(...)
rt := runtime.New(runtime.WithEngine(eng), runtime.WithStream(pulseSink), runtime.WithMemoryStore(memStore), runtime.WithRunStore(runStore))
_ = chat.RegisterChatAgent(ctx, rt, chat.ChatAgentConfig{Planner: myPlanner})
_ = eng.Worker().Start()
```

## Typed IDs

- Use `agent.Ident` for agent IDs and `tools.Ident` for tool IDs throughout.
- Convert to string only at transport or external API boundaries (logging, DTOs, enums). Avoid comparing raw strings for tool IDs inside planners/executors.

Examples:

```
// Build a ToolRequest from a model chunk
calls = append(calls, planner.ToolRequest{Name: tools.Ident(d.Name), Payload: d.Payload})

// When building wire DTOs, cast to string
wire := &gen.Tool{Name: string(spec.Name), Description: spec.Description}
```

## Agent Tools Helpers

When an agent exports a toolset, goa-ai generates an `agenttools` package:

- Typed tool ID constants (tools.Ident)
- Alias types (`<Tool>Payload`, `<Tool>Result`)
- Codecs (`<Tool>PayloadCodec`, `<Tool>ResultCodec`)
- Typed call builders (`New<Tool>Call(*<Tool>Payload, ...)`)

Use these instead of ad-hoc strings:

```
import chattools "example.com/assistant/gen/orchestrator/agents/chat/agenttools/search"

req := chattools.NewSearchCall(&chattools.SearchPayload{Query: "golang"})

Note: Per-toolset specs packages (`gen/<svc>/agents/<agent>/specs/<toolset>`) also
export typed tool ID constants for all generated tools (including non‑exported
toolsets). Prefer those constants when you are working directly with specs.
```

## Pattern Recap

- Prefer typed IDs inside the runtime/planner/executor paths.
- Convert to/from string only at the edges (providers, DTOs, logs).
- Favor generated helpers (`<agent>.NewClient`, agenttools aliases/constants) to minimize boilerplate and keep code safe and idiomatic.
