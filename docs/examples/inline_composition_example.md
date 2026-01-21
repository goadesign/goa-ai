# Example: Agent-as-Tool Composition Across Processes (Child Workflows)

This example shows how a consumer agent invokes tools exported by a provider agent
running on a different worker process.

Key semantics:

- The nested agent executes as a **child workflow** with its own `RunID`.
- The parent run emits a `ChildRunLinked` link event and receives a `ToolResult`
  containing a `RunLink` handle to the child run.
- Streams are **per-run**; UIs can subscribe to the child run via the run link to
  render nested execution without flattening run identity.

## Provider Worker Process

Register the provider agent and run a worker on its workflow/task queue:

```go
package main

import (
    "context"

    exporter "example.com/gen/exporter/agents/exporter"
    "goa.design/goa-ai/runtime/agent/runtime"
)

func main() {
    rt := runtime.New(/* Temporal worker engine configured for exporter queues */)

    if err := exporter.RegisterExporter(context.Background(), rt, exporter.ExporterConfig{
        Planner: newExporterPlanner(),
    }); err != nil {
        panic(err)
    }

    // Start worker per engine integration...
}
```

## Consumer Worker Process

Register the provider’s exported toolset with the consumer runtime, then register
the consumer agent:

```go
package main

import (
    "context"

    exporteragenttools "example.com/gen/exporter/agents/exporter/agenttools"
    consumer "example.com/gen/consumer/agents/consumer"
    "goa.design/goa-ai/runtime/agent/runtime"
)

func main() {
    rt := runtime.New(/* Temporal worker engine configured for consumer queues */)

    reg, err := exporteragenttools.NewRegistration(
        rt,
        "You are a data expert.",
        runtime.WithTextAll(exporteragenttools.ToolIDs, "Handle: {{ . }}"),
    )
    if err != nil {
        panic(err)
    }
    if err := rt.RegisterToolset(reg); err != nil {
        panic(err)
    }

    if err := consumer.RegisterConsumer(context.Background(), rt, consumer.ConsumerConfig{
        Planner: newConsumerPlanner(),
    }); err != nil {
        panic(err)
    }

    // Start worker per engine integration...
}
```
