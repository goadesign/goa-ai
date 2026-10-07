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
//	    },
//	    WorkerOptions: temporal.WorkerOptions{
//	        TaskQueue: "assistant",
//	    },
//	})
//	if err != nil {
//	    log.Fatal(err)
//	}
//	rt := runtime.New(runtimeStore, runtime.WithEngine(eng))
//	// Register toolsets first, then agents.
//	sealCtx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
//	defer cancel()
//	if err := rt.Seal(sealCtx); err != nil {
//	    log.Fatal(err)
//	}
//	defer eng.Close()
//
// NewWorker and NewClient always install NewAgentDataConverter. This keeps
// exact-number decoding, unknown-field rejection, unsafe-value rejection, and
// payload limits identical in every worker and client process.
//
// NewWorker also installs a required workflow control interceptor, including when
// tracing is disabled. Native workflows on those workers call
// NewWorkflowContext(eng, ctx) and return its error before scheduling work. The
// constructor returns (engine.WorkflowContext, error); a context from a separate
// worker or another engine is rejected. There is no caller-owned drain step:
// the worker waits for accepted control deliveries before ordinary completion.
//
// Existing callers of NewWorkflowContext must handle its new error result.
// Workers replaying histories with provider control must install this
// interceptor and run the matching adapter version. Child starts now record
// private identifiers and headers. Histories that scheduled children before this
// change must finish on their previous worker code, or remain routed to that
// worker version during deployment and rollback. This adapter has no bypass for
// replaying those earlier child-start commands with the new implementation.
//
// Each child binding follows the first accepted native run through Temporal
// retries and Continue-As-New. The producer reads the current run and original
// run IDs from Temporal; every request and ordered delivery retains that origin.
// An earlier run's delayed report cannot move to a later run's request. Workers
// in the Temporal namespace are trusted: the private binding is not protection
// against administrators who can inspect and alter namespace history.
//
// Acceptance is retained for exact repeated deliveries. Acknowledgments always
// target the original sender run. If that run is already gone, the receiver
// retains its acceptance without failing healthy work or redirecting the reply.
// Application decisions about measured intervals and allowances remain in the
// runtime. Native wrappers relay requests and retain descendant pause reports;
// they never claim a descendant's pause as their own.
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
// registration only after all toolsets and agents have been registered. Seal is
// the worker activation boundary: it returns only after every local worker has
// started successfully or the caller's context deadline ends.
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
//   - StartChildWorkflow starts nested workflows that Temporal terminates when
//     their parent closes
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
