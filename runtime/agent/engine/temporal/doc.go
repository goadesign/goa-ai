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
// Worker processes use NewWorker to create an engine with Temporal client and
// worker options:
//
//	eng, err := temporal.NewWorker(temporal.Options{
//	    ClientOptions: &client.Options{
//	        HostPort:  "temporal:7233",
//	        Namespace: "default",
//	        // Required: enforce goa-ai's workflow boundary contract.
//	        // Tool results/artifacts cross boundaries as canonical JSON bytes (api.ToolEvent/api.ToolArtifact),
//	        // and planner.ToolResult is rejected if it ever tries to cross a Temporal boundary.
//	        // Pass the generated tool specs aggregate for the agent(s) hosted by this runtime.
//	        // Example: specs "<module>/gen/<service>/agents/<agent>/specs"
//	        // DataConverter: temporal.NewAgentDataConverter(specs.Spec),
//	    },
//	    WorkerOptions: temporal.WorkerOptions{
//	        TaskQueue:              "orchestrator.chat",
//	        MaxConcurrentActivities: 10,
//	    },
//	})
//	if err != nil {
//	    log.Fatal(err)
//	}
//	rt := runtime.New(runtime.WithEngine(eng))
//	// Register toolsets first, then agents.
//	if err := rt.Seal(context.Background()); err != nil {
//	    log.Fatal(err)
//	}
//	defer eng.Close()
//
// Client-only processes use NewClient and do not register local workflows or
// activities:
//
//	eng, err := temporal.NewClient(temporal.Options{
//	    ClientOptions: &client.Options{
//	        HostPort:  "temporal:7233",
//	        Namespace: "default",
//	    },
//	})
//	if err != nil {
//	    log.Fatal(err)
//	}
//
// # Worker vs Client Mode
//
// Worker mode polls task queues and executes workflows locally. Client mode
// submits workflows without local execution.
//
// Registration sealing is part of the worker-mode contract: the runtime must seal
// registration only after all toolsets and agents have been registered so polling
// begins from a coherent local registry.
//
// # Workflow Determinism
//
// Temporal workflows must be deterministic: given the same inputs and event
// history, they must produce the same outputs. This package provides a
// WorkflowContext that exposes only deterministic operations:
//
//   - Now() returns workflow time (not wall clock)
//   - PublishRecord schedules record persistence outside the workflow thread
//   - ExecutePlannerActivity runs planner activities
//   - ExecuteToolActivity/ExecuteToolActivityAsync run tool activities
//   - PauseRequests/ResumeRequests/... return typed signal receivers
//   - StartChildWorkflow starts nested workflows
//
// Planners and tool executors run inside activities, which are not constrained
// by determinism. The workflow handler (generated code) coordinates activities
// and processes their results deterministically.
//
// # OpenTelemetry Integration
//
// The engine emits traces using a "trace domains" contract:
//
//   - Synchronous request handling (HTTP/gRPC) stays within a single trace tree.
//   - Durable scheduling (Temporal) creates a new trace tree per activity
//     execution and links it back to the initiating request trace via OTel links.
//
// This avoids long-lived traces that fragment in collectors/sampling pipelines
// while preserving navigability across domains.
//
// # Query Handlers
//
// Workflows can expose query handlers for external introspection. The runtime
// uses queries to retrieve run status and transcript state without blocking
// workflow execution.
package temporal
