---

## 9. Ready for Prime Time: Advanced Features 🔭

* **Sessions & Runs:** Sessions are explicit and owned by the host service. Create and end them through that service. Runs (`client.Run`/`client.Start`) require an active session.
* **Session-Owned Streaming:** In production, trusted runtime hosts should attach to the **session-owned stream** (`session/<session_id>`) and filter by `run_id`. Close the connection when you observe a `run_stream_end` event for the attached run ID. Nested agent runs emit `child_run_linked` links and their own `run_stream_end`; parent runs only emit `run_stream_end` after all child runs have ended. Create a separate public event contract before forwarding data to a browser.
* **Asynchronous Runs:** Use `client.Start()` to get a workflow handle. This is great for long-running tasks, cancellation, and non-interactive integrations.
* **Human Input:** Clarifications, confirmations, and external results end the current workflow with a typed suspension. Call `PrepareContinuation` with the new workflow ID, call `MarshalBinary`, then store that ID from `RunID()` alongside the prepared bytes and accepted answer in one application database transaction. A later process can load those bytes, call `ParsePreparedRun`, and submit the result with `StartPrepared`.
* **Policies & Caps:** The `RunPolicy` in your design (max tool calls, time budgets) is automatically enforced by the runtime.
* **Persistence & Observability:** The `runtime.New` function requires one `storage.Store` that owns run metadata, continuation checkpoints, and ordered run records. Options configure the engine, memory, streaming, and telemetry.
* **Temporal DataConverter:** The Temporal engine always installs its strict, bounded data converter. Applications provide connection and namespace settings through `ClientOptions`; they cannot replace the workflow data contract.
* **Dynamic registries:** Consume one named `Toolset(FromRegistry(...))` or a whole `Registry` with `Use`. Connect the clustered registry's generated service client and Pulse client using `rt.RegisterRegistry(name, client, pulseClient)` before starting a run. Generated definitions read the catalog once per planning activity; `Definition()` and `NewClient(rt)` perform no network reads and require no catalog arguments. The runtime executes selected service tools using their saved contract and exact registration token. Publish provider `ToolSchemas()` records to carry confirmation, pagination, field metadata, and server-only data. Agent and control tools remain compiled dependencies.
* **Deferred tool search:** Add `Deferred()` inside the consuming `Use` for all static or registry tools, or `Deferred("search", "analyze")` for exact authored local names in a compiled toolset; other tools remain immediately available. Named selections work with local and MCP tools, but not `FromRegistry` or whole registries. Empty, duplicate, unknown, and mixed all/named selections are rejected; repeated `Deferred()` remains valid. Regenerate the consumer after changing its choice. Pass `input.Agent.AdvertisedToolDefinitions()` to model requests; do not implement a planner search tool. OpenAI's adapter searches generated metadata with BM25 and returns native definition results; Claude performs hosted search, including through Bedrock's Messages adapter. Preserve message metadata through history storage and compaction. Unsupported adapters fail explicitly. Regenerate old startup `Discover`/`RegistryToolsets` integrations and replace their dynamic executor wiring with `RegisterRegistry`.

```go
// Example of production-ready runtime options
rt := runtime.New(runtimeStore,
    // runtime.WithEngine(myTemporalEngine),
    // runtime.WithMemoryStore(myMongoMemoryStore),
    // runtime.WithStream(myEventStreamSink, stream.RuntimeHostProfile()),
)
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

