# Example: Inline Composition Across Processes

This example shows how a consumer agent composes an exporter agent inline while
workers run in separate processes.

```go
package main

import (
    "context"

    exporter "example.com/gen/exporter/agents/exporter"
    consumer "example.com/gen/consumer/agents/consumer"
    "goa.design/goa-ai/runtime/agent/runtime"
)

func main() {
    rt := runtime.New(/* engine with worker/client configured */)

    // In the consumer worker process:
    // 1) Register exporter route-only metadata (no planner locally)
    _ = exporter.RegisterExporterRoute(context.Background(), rt)

    // 2) Register the exported toolset as inline agent-tool
    reg := exporter.NewExporterToolsetRegistration(rt)
    reg.Inline = true
    _ = rt.RegisterToolset(reg)

    // 3) Register consumer agent (with its own planner)
    _ = consumer.RegisterConsumer(context.Background(), rt, consumer.ConsumerConfig{Planner: myPlanner})

    // Start workers per engine integration...
}
```

At runtime the consumer’s `ExecuteAgentInline` schedules the exporter’s `Plan` and
`Resume` activities on the exporter queue. Temporal routes those to the exporter
workers; the parent workflow maintains a single history and a single stream of
`tool_start` / `tool_result` events.

