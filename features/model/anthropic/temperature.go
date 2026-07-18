// This file: temperature-omission telemetry for the capability rule shared
// across Claude adapters. The rule itself — which model generations reject
// the `temperature` sampling parameter with a 400 invalid_request_error
// ("temperature is deprecated for this model", live-verified against
// claude-sonnet-5 on Vertex) — lives in
// features/model/internal/claudecaps.TemperatureSupported so this adapter
// (which also backs Claude-on-Vertex) and features/model/bedrock apply the
// identical boundary. Adjacent-layer contract: completionParams (client.go)
// consults the shared predicate before setting params.Temperature and calls
// traceTemperatureOmitted when it drops a caller-configured value; the model
// then runs at its own default sampling behavior.

package anthropic

import (
	"context"

	"go.opentelemetry.io/otel/trace"

	"goa.design/goa-ai/runtime/agent/telemetry"
)

// traceTemperatureOmitted marks the ambient span (if any — trace.SpanFromContext
// returns a no-op span when ctx carries none) with the fact that a caller-
// configured, non-default temperature was dropped from the wire request
// because modelID does not support it. This is the only signal a caller gets
// that their Options.Temperature / Request.Temperature had no effect, so it
// must survive independently of whether the call ultimately succeeds or
// fails.
func traceTemperatureOmitted(ctx context.Context, modelID string, requested float64) {
	trace.SpanFromContext(ctx).SetAttributes(
		telemetry.GenAITemperatureOmittedAttrs(modelID, requested)...,
	)
}
