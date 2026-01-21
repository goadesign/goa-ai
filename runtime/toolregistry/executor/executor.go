// Package executor provides registry-backed tool execution. It routes tool
// invocations through the registry gateway and awaits results on Pulse streams.
package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	pulsec "goa.design/goa-ai/features/stream/pulse/clients/pulse"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/runtime"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/toolregistry"
	"goa.design/pulse/streaming/options"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

type (
	// Client initiates tool calls through a registry gateway.
	Client interface {
		CallTool(ctx context.Context, toolset string, tool tools.Ident, payload []byte, meta toolregistry.ToolCallMeta) (toolUseID string, resultStreamID string, err error)
	}

	// SpecLookup resolves tool specifications for decoding results and artifacts.
	SpecLookup interface {
		Spec(name tools.Ident) (*tools.ToolSpec, bool)
	}

	Executor struct {
		client Client
		pulse  pulsec.Client
		specs  SpecLookup

		sinkName       string
		resultEventKey string

		logger telemetry.Logger
		tracer telemetry.Tracer
	}

	Option func(*Executor)
)

func WithSinkName(name string) Option {
	return func(e *Executor) {
		e.sinkName = name
	}
}

func WithResultEventKey(key string) Option {
	return func(e *Executor) {
		e.resultEventKey = key
	}
}

func WithLogger(logger telemetry.Logger) Option {
	return func(e *Executor) {
		e.logger = logger
	}
}

func WithTracer(tracer telemetry.Tracer) Option {
	return func(e *Executor) {
		e.tracer = tracer
	}
}

func New(client Client, pulse pulsec.Client, specs SpecLookup, opts ...Option) *Executor {
	e := &Executor{
		client:         client,
		pulse:          pulse,
		specs:          specs,
		sinkName:       "agent",
		resultEventKey: "result",
		logger:         telemetry.NewNoopLogger(),
		tracer:         telemetry.NewNoopTracer(),
	}
	for _, o := range opts {
		if o != nil {
			o(e)
		}
	}
	return e
}

func (e *Executor) Execute(ctx context.Context, meta *runtime.ToolCallMeta, call *planner.ToolRequest) (*planner.ToolResult, error) {
	if call == nil {
		return &planner.ToolResult{Error: planner.NewToolError("tool request is nil")}, nil
	}
	if meta == nil {
		return &planner.ToolResult{Name: call.Name, Error: planner.NewToolError("tool call meta is nil")}, nil
	}
	if e.client == nil {
		return &planner.ToolResult{Name: call.Name, Error: planner.NewToolError("registry client is nil")}, nil
	}
	if e.pulse == nil {
		return &planner.ToolResult{Name: call.Name, Error: planner.NewToolError("pulse client is nil")}, nil
	}
	if e.specs == nil {
		return &planner.ToolResult{Name: call.Name, Error: planner.NewToolError("tool specs lookup is nil")}, nil
	}

	spec, ok := e.specs.Spec(call.Name)
	if !ok {
		return &planner.ToolResult{Name: call.Name, Error: planner.NewToolError(fmt.Sprintf("unknown tool %q", call.Name))}, nil
	}
	toolsetID := spec.Toolset
	if toolsetID == "" {
		return &planner.ToolResult{Name: call.Name, Error: planner.NewToolError(fmt.Sprintf("tool %q missing toolset routing id", call.Name))}, nil
	}

	tracer := e.tracer
	if tracer == nil {
		tracer = telemetry.NewNoopTracer()
	}
	ctx, span := tracer.Start(
		ctx,
		"toolregistry.execute",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("toolregistry.toolset", toolsetID),
			attribute.String("toolregistry.tool", call.Name.String()),
			attribute.String("toolregistry.run_id", meta.RunID),
			attribute.String("toolregistry.session_id", meta.SessionID),
			attribute.String("toolregistry.turn_id", meta.TurnID),
			attribute.String("toolregistry.tool_call_id", meta.ToolCallID),
			attribute.String("toolregistry.parent_tool_call_id", meta.ParentToolCallID),
			attribute.String("toolregistry.sink", e.sinkName),
			attribute.String("toolregistry.result_event_key", e.resultEventKey),
		),
	)
	defer span.End()

	tmeta := toolregistry.ToolCallMeta{
		RunID:            meta.RunID,
		SessionID:        meta.SessionID,
		TurnID:           meta.TurnID,
		ToolCallID:       meta.ToolCallID,
		ParentToolCallID: meta.ParentToolCallID,
	}
	toolUseID, resultStreamID, err := e.client.CallTool(ctx, toolsetID, call.Name, call.Payload, tmeta)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "call tool via registry failed")
		return &planner.ToolResult{Name: call.Name, Error: planner.ToolErrorFromError(err), ToolCallID: meta.ToolCallID}, nil
	}
	span.AddEvent(
		"toolregistry.call_tool_ok",
		"toolregistry.tool_use_id", toolUseID,
		"toolregistry.result_stream_id", resultStreamID,
	)

	stream, err := e.pulse.Stream(resultStreamID)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "open tool result stream failed")
		return nil, fmt.Errorf("open tool result stream %q: %w", resultStreamID, err)
	}
	// Result streams are per-tool-call and short-lived. Providers can publish the
	// result very quickly after the registry returns from CallTool, so we must
	// start at the oldest event to avoid missing an already-published result.
	sink, err := stream.NewSink(ctx, e.sinkName, options.WithSinkStartAtOldest())
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "create sink for tool result stream failed")
		return nil, fmt.Errorf("create sink %q for tool result stream %q: %w", e.sinkName, resultStreamID, err)
	}
	defer sink.Close(ctx)
	span.AddEvent("toolregistry.result_subscribed", "toolregistry.result_stream_id", resultStreamID)

	events := sink.Subscribe()
	for {
		select {
		case <-ctx.Done():
			span.RecordError(ctx.Err())
			span.SetStatus(codes.Error, "tool result wait canceled")
			return nil, ctx.Err()
		case ev, ok := <-events:
			if !ok {
				span.RecordError(fmt.Errorf("tool result stream subscription closed"))
				span.SetStatus(codes.Error, "tool result stream subscription closed")
				return nil, fmt.Errorf("tool result stream subscription closed")
			}
			if ev.EventName != e.resultEventKey {
				if err := sink.Ack(ctx, ev); err != nil {
					span.RecordError(err)
					span.SetStatus(codes.Error, "ack non-result event failed")
					return nil, fmt.Errorf("ack tool result stream event: %w", err)
				}
				continue
			}

			var msg toolregistry.ToolResultMessage
			if err := json.Unmarshal(ev.Payload, &msg); err != nil {
				span.RecordError(err)
				if ackErr := sink.Ack(ctx, ev); ackErr != nil {
					return nil, fmt.Errorf("ack malformed tool result message: %w", ackErr)
				}
				continue
			}
			if msg.ToolUseID != toolUseID {
				if err := sink.Ack(ctx, ev); err != nil {
					return nil, fmt.Errorf("ack unrelated tool result message: %w", err)
				}
				continue
			}
			if err := sink.Ack(ctx, ev); err != nil {
				span.RecordError(err)
				span.SetStatus(codes.Error, "ack tool result message failed")
				return nil, fmt.Errorf("ack tool result message: %w", err)
			}
			if destroyErr := stream.Destroy(ctx); destroyErr != nil {
				span.RecordError(destroyErr)
				span.SetStatus(codes.Error, "destroy tool result stream failed")
				return nil, fmt.Errorf("destroy tool result stream %q: %w", resultStreamID, destroyErr)
			}
			span.AddEvent(
				"toolregistry.result_received",
				"toolregistry.tool_use_id", toolUseID,
				"toolregistry.result_stream_id", resultStreamID,
			)
			span.SetStatus(codes.Ok, "ok")
			return e.decodeToolResult(spec, call.Name, meta.ToolCallID, msg), nil
		}
	}
}

func (e *Executor) decodeToolResult(spec *tools.ToolSpec, tool tools.Ident, toolCallID string, msg toolregistry.ToolResultMessage) *planner.ToolResult {
	out := &planner.ToolResult{
		Name:       tool,
		ToolCallID: toolCallID,
	}
	if msg.Error != nil {
		out.Error = planner.NewToolError(msg.Error.Message)
		if hint := buildRetryHintFromIssues(tool, spec, msg.Error.Issues); hint != nil {
			out.RetryHint = hint
		} else if hint := retryHintFromToolErrorCode(tool, msg.Error.Code); hint != nil {
			out.RetryHint = hint
		}
		return out
	}
	if spec.Result.Codec.FromJSON != nil {
		res, err := spec.Result.Codec.FromJSON(msg.Result)
		if err != nil {
			out.Error = planner.ToolErrorFromError(err)
			return out
		}
		out.Result = res
	}
	if spec.Sidecar != nil && spec.Sidecar.Codec.FromJSON != nil && len(msg.Artifacts) > 0 {
		arts := make([]*planner.Artifact, 0, len(msg.Artifacts))
		for _, a := range msg.Artifacts {
			data, err := spec.Sidecar.Codec.FromJSON(a.Data)
			if err != nil {
				out.Error = planner.ToolErrorFromError(err)
				return out
			}
			arts = append(arts, &planner.Artifact{
				Kind:       a.Kind,
				Data:       data,
				SourceTool: tool,
			})
		}
		out.Artifacts = arts
	}
	return out
}

func retryHintFromToolErrorCode(tool tools.Ident, code string) *planner.RetryHint {
	switch code {
	case "invalid_input":
		// Service-level invalid_input errors should surface as invalid input to callers.
		// We reuse the invalid_arguments retry reason so downstream UIs classify the
		// failure correctly (invalid_input vs internal) without adding new wire fields.
		return &planner.RetryHint{
			Reason: planner.RetryReasonInvalidArguments,
			Tool:   tool,
		}
	case "timeout":
		return &planner.RetryHint{
			Reason: planner.RetryReasonTimeout,
			Tool:   tool,
		}
	}
	return nil
}

func buildRetryHintFromIssues(toolName tools.Ident, spec *tools.ToolSpec, issues []*tools.FieldIssue) *planner.RetryHint {
	if len(issues) == 0 {
		return nil
	}
	fields := make([]string, 0, len(issues))
	missing := make([]string, 0, len(issues))
	for _, is := range issues {
		if is == nil || is.Field == "" {
			continue
		}
		fields = append(fields, is.Field)
		if is.Constraint == "missing_field" {
			missing = append(missing, is.Field)
		}
	}
	if len(fields) == 0 {
		return nil
	}
	fields = uniqueStrings(fields)
	missing = uniqueStrings(missing)
	sort.Strings(fields)
	sort.Strings(missing)

	question := buildClarifyingQuestion(toolName, missing, fields)
	var example map[string]any
	if spec != nil && len(spec.Payload.ExampleInput) > 0 {
		example = spec.Payload.ExampleInput
	}
	reason := planner.RetryReasonInvalidArguments
	if len(missing) > 0 {
		reason = planner.RetryReasonMissingFields
	}
	return &planner.RetryHint{
		Reason:             reason,
		Tool:               toolName,
		MissingFields:      missing,
		ExampleInput:       example,
		ClarifyingQuestion: question,
	}
}

func buildClarifyingQuestion(toolName tools.Ident, missing, fields []string) string {
	if len(missing) == 2 && missing[0] == "query" && missing[1] == "requested_signals" {
		return "I need additional information to run " + toolName.String() + ". Please provide either `query` (a short description) or `requested_signals` (a non-empty list of signal names) and resend the tool call."
	}
	if len(missing) > 0 {
		return "I need additional information to run " + toolName.String() + ". Please provide: " + strings.Join(missing, ", ") + "."
	}
	return "I could not run " + toolName.String() + " due to invalid arguments. Please correct: " + strings.Join(fields, ", ") + " and resend the tool call."
}

func uniqueStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
