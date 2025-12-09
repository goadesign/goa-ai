// Package temporal implements the goa-ai workflow engine adapter backed by
// Temporal (https://temporal.io). It satisfies the generic engine.Engine
// interface, allowing generated code and the runtime to orchestrate durable
// workflows without importing the Temporal SDK directly.
//
// # Why Temporal?
//
// Temporal provides durable execution for long-running agent workflows. When an
// agent makes multiple tool calls, awaits human input, or runs for extended
// periods, Temporal ensures the workflow state survives process restarts, network
// failures, and crashes. The runtime replays the workflow from event history,
// producing deterministic execution.
//
// # Constructing an Engine
//
// Use New to create an engine with Temporal client and worker options:
//
//	eng, err := temporal.New(temporal.Options{
//	    ClientOptions: &client.Options{
//	        HostPort:  "temporal:7233",
//	        Namespace: "default",
//	    },
//	    WorkerOptions: temporal.WorkerOptions{
//	        TaskQueue:              "orchestrator.chat",
//	        MaxConcurrentActivities: 10,
//	    },
//	})
//	if err != nil {
//	    log.Fatal(err)
//	}
//	defer eng.Close()
//
// # Worker vs Client Mode
//
// The same engine can operate in two modes:
//
//   - Worker mode: Polls task queues and executes workflows locally. Use this in
//     processes that register agents and run planners/tools.
//
//   - Client mode: Submits workflows without local execution. Use this in API
//     gateways or CLI tools that start runs but don't process them.
//
// Both modes use the same Options; the difference is whether agents are registered
// on the runtime. Client-only processes skip agent registration.
//
// # Workflow Determinism
//
// Temporal workflows must be deterministic: given the same inputs and event
// history, they must produce the same outputs. This package provides a
// WorkflowContext that exposes only deterministic operations:
//
//   - Now() returns workflow time (not wall clock)
//   - ExecuteActivity and ExecuteActivityAsync schedule activities
//   - SignalChannel returns deterministic signal receivers
//   - StartChildWorkflow starts nested workflows
//
// Planners and tool executors run inside activities, which are not constrained
// by determinism. The workflow handler (generated code) coordinates activities
// and processes their results deterministically.
//
// # OpenTelemetry Integration
//
// The engine automatically installs OTEL interceptors on the Temporal client and
// worker, propagating trace context through workflow and activity boundaries. No
// additional configuration is needed when the runtime is configured with a Tracer.
//
// # Query Handlers
//
// Workflows can expose query handlers for external introspection. The runtime
// uses queries to retrieve run status and transcript state without blocking
// workflow execution.
package temporal
