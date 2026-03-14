// Package temporal keeps OpenTelemetry wiring separate from the engine's core
// registration/start path so the adapter can own one instrumentation contract
// without burying it in worker and workflow plumbing.
package temporal

import (
	"go.temporal.io/sdk/client"
	temporalotel "go.temporal.io/sdk/contrib/opentelemetry"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"goa.design/goa-ai/runtime/temporaltrace"
)

type instrumentation struct {
	contextPropagators []workflow.ContextPropagator
	workerInterceptors []interceptor.WorkerInterceptor
	metrics            client.MetricsHandler
}

func configureInstrumentation(opts InstrumentationOptions) *instrumentation {
	inst := &instrumentation{}
	if !opts.DisableTracing {
		// Trace domain contract:
		//   - Temporal activities emit new-root spans (new trace IDs).
		//   - Upstream request traces are preserved via OTel links, not parenthood.
		// We intentionally do not use Temporal's OTEL tracing interceptor: it
		// propagates parent trace context through durable scheduling boundaries,
		// which produces long-lived traces that fragment in downstream pipelines.
		inst.contextPropagators = append(inst.contextPropagators, temporaltrace.NewLinkPropagator())
		inst.workerInterceptors = append(inst.workerInterceptors, temporaltrace.NewActivityInterceptor())
	}
	if !opts.DisableMetrics {
		inst.metrics = temporalotel.NewMetricsHandler(opts.MetricsOptions)
	}
	if len(inst.contextPropagators) == 0 && len(inst.workerInterceptors) == 0 && inst.metrics == nil {
		return nil
	}
	return inst
}

func applyClientInstrumentation(opts *client.Options, inst *instrumentation) {
	if inst == nil {
		return
	}
	opts.ContextPropagators = append(opts.ContextPropagators, inst.contextPropagators...)
	if inst.metrics != nil && opts.MetricsHandler == nil {
		opts.MetricsHandler = inst.metrics
	}
}

func applyWorkerInstrumentation(opts *worker.Options, inst *instrumentation) {
	if inst == nil {
		return
	}
	opts.Interceptors = append(opts.Interceptors, inst.workerInterceptors...)
}
