// These tests send completed tool events through the runtime's telemetry
// subscriber and a real OpenTelemetry exporter. Private failure details must
// remain available to the model without appearing anywhere in the tool span.
package runtime

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/telemetry"
)

func TestGenAIToolSpanPreservesPrivateFailureEvidence(t *testing.T) {
	// Clue uses the global tracer provider. Keep this test sequential and
	// restore that provider before any parallel tests resume.
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previous)
		assert.NoError(t, provider.Shutdown(context.Background()))
	})
	rt := &Runtime{tracer: telemetry.NewClueTracer()}
	parent := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: trace.TraceID{1}, SpanID: trace.SpanID{2}, TraceFlags: trace.FlagsSampled,
	})
	ctx := trace.ContextWithSpanContext(context.Background(), parent)

	for _, tc := range []struct {
		name   string
		kind   planner.FailureKind
		action planner.RecoveryAction
	}{
		{name: "success"},
		{name: "invalid call", kind: planner.FailureInvalidCall, action: planner.RecoveryCorrectCall},
		{name: "domain rejection", kind: planner.FailureDomainRejection, action: planner.RecoveryFinish},
		{name: "unavailable", kind: planner.FailureUnavailable, action: planner.RecoveryReplan},
		{name: "rate limited", kind: planner.FailureRateLimited, action: planner.RecoveryReplan},
		{name: "timeout", kind: planner.FailureTimeout, action: planner.RecoveryFinish},
		{name: "malformed result", kind: planner.FailureMalformedResult, action: planner.RecoveryFinish},
		{name: "internal", kind: planner.FailureInternal, action: planner.RecoveryFinish},
	} {
		for _, duration := range []time.Duration{0, 125 * time.Millisecond} {
			t.Run(fmt.Sprintf("%s/%s", tc.name, duration), func(t *testing.T) {
				exporter.Reset()
				var failure *planner.ToolFailure
				result := rawjson.Message(`{"value":"private-result-marker"}`)
				if tc.kind != "" {
					failure = &planner.ToolFailure{
						Kind: tc.kind,
						Error: planner.NewToolErrorWithCause("private-message-marker",
							planner.NewToolErrorWithCause("private-cause-marker", planner.NewToolError("private-deep-cause-marker"))),
						Recovery: planner.RecoveryDirective{Action: tc.action},
					}
					require.NoError(t, planner.ValidateToolFailure(failure))
					result = nil
				}
				event := hooks.NewToolResultReceivedEvent(
					"run", "service.agent", "session", "run", "service.lookup", "call", "",
					result, nil, "private-preview-marker", nil, duration, nil, failure,
				)
				event.SetTimestampMS(1_000_000)
				before := *event
				before.Failure = planner.CloneToolFailure(event.Failure)

				require.NoError(t, rt.recordGenAITelemetryEvent(ctx, event))
				assert.Equal(t, before, *event, "tracing must not rewrite conversation evidence")
				spans := exporter.GetSpans()
				require.Len(t, spans, 1)
				span := spans[0]
				assert.Equal(t, "execute_tool service.lookup", span.Name)
				assert.Equal(t, trace.SpanKindInternal, span.SpanKind)
				assert.Equal(t, parent, span.Parent)
				assert.Equal(t, parent.TraceID(), span.SpanContext.TraceID())
				endedAt := time.UnixMilli(event.Timestamp()).UTC()
				assert.Equal(t, endedAt, span.EndTime)
				assert.Equal(t, endedAt.Add(-duration), span.StartTime)
				assert.ElementsMatch(t, []attribute.KeyValue{
					telemetry.AttrGenAIConversationID.String("session"),
					telemetry.AttrGenAIAgentID.String("service.agent"),
					telemetry.AttrGenAIAgentName.String("service.agent"),
					telemetry.AttrGenAIOperationName.String(telemetry.GenAIOperationExecuteTool),
					telemetry.AttrGenAIToolName.String("service.lookup"),
					telemetry.AttrGenAIToolCallID.String("call"),
				}, span.Attributes)
				for _, marker := range []string{
					"private-message-marker", "private-cause-marker", "private-deep-cause-marker",
					"private-result-marker", "private-preview-marker",
				} {
					assert.NotContains(t, fmt.Sprint(span.Name, span.Status, span.Attributes, span.Events, span.Links), marker)
				}
				if failure == nil {
					assert.Equal(t, codes.Ok, span.Status.Code)
					assert.Empty(t, span.Events)
					return
				}
				assert.Equal(t, codes.Error, span.Status.Code)
				assert.Equal(t, string(tc.kind), span.Status.Description)
				require.Len(t, span.Events, 1)
				assert.Equal(t, "exception", span.Events[0].Name)
				assert.Contains(t, span.Events[0].Attributes, attribute.String("exception.message", string(tc.kind)))

				// The same completed failure still supplies its original message
				// to the model, while the full cause chain and recovery survive.
				content, err := rt.toolResultContent(&ToolCall{Name: event.ToolName}, &planner.ToolResult{
					Name: event.ToolName, Failure: event.Failure,
				})
				require.NoError(t, err)
				assert.Equal(t, "private-message-marker", content)
			})
		}
	}
}
