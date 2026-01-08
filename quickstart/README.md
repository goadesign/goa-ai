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
        Use("helpers", func() {
            Tool("answer", "Answer a simple question", func() {
                Args(AskPayload)
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

## 3) Generate code and example

```bash
goa gen example.com/quickstart/design
goa example example.com/quickstart/design
```

This creates:
- **`gen/`** - Generated code (never edit by hand)
- **`cmd/orchestrator/main.go`** - Runnable example using the bootstrap
- **`internal/agents/bootstrap/bootstrap.go`** - Wires runtime and registers agents
- **`internal/agents/chat/planner/planner.go`** - Stub planner (edit to connect your LLM)

## 4) Run the generated example

```bash
go run ./cmd/orchestrator
```

Expected output:

```
RunID: orchestrator-chat-...
Assistant: Hello from example planner.
```

The generated example uses the in-memory engine, so no Temporal is needed for development.

## 5) (Optional) Connect to Temporal for production

For production, start Temporal and configure the runtime:

```bash
# Start Temporal dev server
docker run --rm -d --name temporal-dev -p 7233:7233 temporalio/auto-setup:latest
```

Then modify the bootstrap to use the Temporal engine:

```go
import (
    "goa.design/goa-ai/runtime/agent/engine/temporal"
    "go.temporal.io/sdk/client"

    // Your generated tool specs aggregate.
    // The generated package exposes: func Spec(tools.Ident) (*tools.ToolSpec, bool)
    specs "<module>/gen/<service>/agents/<agent>/specs"
)

eng, _ := temporal.New(temporal.Options{
    ClientOptions: &client.Options{
        HostPort:      "127.0.0.1:7233",
        Namespace:     "default",
        DataConverter: temporal.NewAgentDataConverter(specs.Spec),
    },
    WorkerOptions: temporal.WorkerOptions{
        TaskQueue: "<service>_<agent>_workflow",
    },
})
rt := agentsruntime.New(agentsruntime.WithEngine(eng))
```

## 6) Customize the planner

Edit `internal/agents/chat/planner/planner.go` to connect your LLM:

```go
func (p *examplePlanner) PlanStart(ctx context.Context, in *planner.PlanInput) (*planner.PlanResult, error) {
    // 1. Get LLM client from runtime
    // mc, _ := in.Agent.ModelClient("openai")
    
    // 2. Build prompt from in.Messages
    
    // 3. Decide: call tools or give final response
    return &planner.PlanResult{
        FinalResponse: &planner.FinalResponse{
            Message: &model.Message{
                Role:  model.ConversationRoleAssistant,
                Parts: []model.Part{model.TextPart{Text: "Your response here"}},
            },
        },
    }, nil
}
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
