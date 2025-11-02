# Goa‑AI Quickstart

Minimal, copy‑paste runnable example to go from zero → talking agent. Keep your design in `design/`, never edit `gen/`.

## Prerequisites

- Go 1.24+
- Goa v3 CLI (`go install goa.design/goa/v3/cmd/goa@latest`)
- Temporal dev server (for workflow execution)
  - Easiest: Docker one‑liner below, or use Temporalite

## 1) Scaffold a fresh project

```
mkdir -p $GOPATH/src/example.com/quickstart && cd $_
go mod init example.com/quickstart
go get goa.design/goa/v3@latest
go get goa.design/goa-ai@latest
```

## 2) Add a tiny design (design/design.go)

This declares one service (`orchestrator`) with a single agent (`chat`) and a tiny helper toolset.

```go
package design

import (
    . "goa.design/goa/v3/dsl"
    . "goa.design/goa-ai/dsl"
)

var _ = API("orchestrator", func() {})

// Input and output types with inline descriptions (required by this repo style)
var AskPayload = Type("AskPayload", func() {
    Attribute("question", String, "User question to answer")
    Example(map[string]any{"question": "What is the capital of Japan?"})
    Required("question")
})

var Answer = Type("Answer", func() {
    Attribute("text", String, "Answer text")
    Required("text")
})

var _ = Service("orchestrator", func() {
    Agent("chat", "Friendly Q&A assistant", func() {
        Uses(func() {
            Toolset("helpers", func() {
                Tool("answer", "Answer a simple question", func() {
                    Args(AskPayload)
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

## 3) Generate code

```
goa gen example.com/quickstart/design
goa example example.com/quickstart/design
```

This creates the generated tree under `gen/` and runnable examples under `cmd/orchestrator`.

## 4) Add the tiniest planner + runner (cmd/demo/main.go)

This wires the Temporal engine, registers the generated agent, and runs a single turn.

```go
package main

import (
    "context"
    "fmt"

    "go.temporal.io/sdk/client"

    chat "example.com/quickstart/gen/orchestrator/agents/chat"
    "goa.design/goa-ai/runtime/agent/engine/temporal"
    "goa.design/goa-ai/runtime/agent/planner"
    "goa.design/goa-ai/runtime/agent/runtime"
)

// A tiny planner: always replies, no tools (great for first run)
type StubPlanner struct{}

func (p *StubPlanner) PlanStart(ctx context.Context, in planner.PlanInput) (planner.PlanResult, error) {
    return planner.PlanResult{FinalResponse: &planner.FinalResponse{
        Message: planner.AgentMessage{Role: "assistant", Content: "Hello from Goa‑AI!"},
    }}, nil
}
func (p *StubPlanner) PlanResume(ctx context.Context, in planner.PlanResumeInput) (planner.PlanResult, error) {
    return planner.PlanResult{FinalResponse: &planner.FinalResponse{
        Message: planner.AgentMessage{Role: "assistant", Content: "Done."},
    }}, nil
}

func main() {
    // 1) Engine (Temporal dev server)
    eng, err := temporal.New(temporal.Options{
        ClientOptions: &client.Options{HostPort: "127.0.0.1:7233", Namespace: "default"},
        WorkerOptions: temporal.WorkerOptions{TaskQueue: "orchestrator.chat"},
    })
    if err != nil { panic(err) }
    defer eng.Close()

    // 2) Runtime
    rt := runtime.New(runtime.Options{Engine: eng})
    runtime.SetDefault(rt)

    // 3) Register generated agent with our planner
    if err := chat.RegisterChatAgent(context.Background(), rt, chat.ChatAgentConfig{Planner: &StubPlanner{}}); err != nil {
        panic(err)
    }

    // 4) Run it
    out, err := chat.Run(context.Background(), rt, "session-1",
        []planner.AgentMessage{{Role: "user", Content: "Say hi"}},
    )
    if err != nil { panic(err) }
    fmt.Println("RunID:", out.RunID)
    fmt.Println("Assistant:", out.Content)
}
```

## 5) Start Temporal dev (one‑liner)

```
docker run --rm -d --name temporal-dev -p 7233:7233 temporalio/auto-setup:latest
```

Alternatively, install Temporalite or point the client to an existing cluster.

## 6) Run the demo

```
go run ./cmd/demo
```

Expected output (similar):

```
RunID: orchestrator.chat-...
Assistant: Hello from Goa‑AI!
```

## (Optional) HTTP / JSON‑RPC server

`goa example` also generated an HTTP JSON‑RPC server under `cmd/orchestrator`.

- Start it: `go run ./cmd/orchestrator -debug`
- It mounts the MCP‑compatible JSON‑RPC API on POST `/rpc`.
- Try a simple RPC (replace the tool name with one from your design):

```bash
curl -s http://localhost:8080/rpc \
  -H 'Content-Type: application/json' \
  -d '{
        "jsonrpc":"2.0",
        "id":1,
        "method":"tools/call",
        "params":{
          "name":"orchestrator.helpers.answer",
          "arguments": {"question": "What is the capital of Japan?"}
        }
      }' | jq .
```

Note: tool execution requires wiring executors. For a first run, the in‑process demo above is the simplest path. When you bind tools to service methods (`BindTo` in the design), `goa example` will scaffold executors you can fill in.

## Notes

- Always change design in `design/*.go` then run `goa gen` (and `goa example` as needed). Never edit `gen/` by hand.
- Generated tool specs live under `gen/<svc>/agents/<agent>/specs/…` with typed codecs.
- Policies and caps are enforced by the runtime during execution; keep planners small and declarative.
