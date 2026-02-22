// Package executor provides registry-backed tool execution. It routes tool
// invocations through the registry gateway and awaits results on Pulse streams.
package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"sort"
	"strings"
	"time"

	pulsec "goa.design/goa-ai/features/stream/pulse/clients/pulse"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/runtime"
	aistream "goa.design/goa-ai/runtime/agent/stream"
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

	// SpecLookup resolves tool specifications for decoding results and server data.
	SpecLookup interface {
		Spec(name tools.Ident) (*tools.ToolSpec, bool)
	}

	Executor struct {
		client Client
		pulse  pulsec.Client
		specs  SpecLookup

		sinkName       string
		resultEventKey string
		outputDeltaKey string
		streamSink     aistream.Sink

		logger telemetry.Logger
		tracer telemetry.Tracer
	}

	Option func(*Executor)

	// sinkFailureDiagnostics captures stable, high-signal context for sink join
	// failures so production incidents can be correlated across run/pod/node and
	// quickly classified as DNS or generic network failures.
	sinkFailureDiagnostics struct {
		hostName               string
		podName                string
		nodeName               string
		ctxHasDeadline         bool
		ctxDeadlineRemainingMs int64
		netTimeout             bool
		dnsError               bool
		dnsName                string
		dnsServer              string
		dnsIsTimeout           bool
		dnsIsTemporary         bool
	}
)

// WithSinkName sets the Pulse sink/consumer-group name used when subscribing to
// per-call result streams. Callers should use a stable name across restarts so
// pending entries are not orphaned in Redis.
func WithSinkName(name string) Option {
	return func(e *Executor) {
		e.sinkName = name
	}
}

// WithResultEventKey sets the Pulse event name used for canonical ToolResultMessage
// payloads on per-call result streams.
func WithResultEventKey(key string) Option {
	return func(e *Executor) {
		e.resultEventKey = key
	}
}

// WithStreamSink configures the executor to forward best-effort tool output delta
// frames into the provided stream sink while it waits for the canonical tool
// result message. This does not affect tool execution semantics: the final tool
// result remains authoritative.
func WithStreamSink(sink aistream.Sink) Option {
	return func(e *Executor) {
		e.streamSink = sink
	}
}

// WithLogger configures the executor logger. When nil, the executor uses a noop
// logger.
func WithLogger(logger telemetry.Logger) Option {
	return func(e *Executor) {
		e.logger = logger
	}
}

// WithTracer configures the executor tracer. When nil, the executor uses a noop
// tracer.
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
		outputDeltaKey: toolregistry.OutputDeltaEventKey,
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
			attribute.String("toolregistry.output_delta_key", e.outputDeltaKey),
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
		diag := buildSinkFailureDiagnostics(ctx, err)
		e.logger.Error(
			ctx,
			"toolregistry result stream sink create failed",
			"component", "tool-registry-executor",
			"toolset", toolsetID,
			"tool", call.Name,
			"tool_use_id", toolUseID,
			"run_id", meta.RunID,
			"session_id", meta.SessionID,
			"turn_id", meta.TurnID,
			"tool_call_id", meta.ToolCallID,
			"result_stream_id", resultStreamID,
			"sink", e.sinkName,
			"host", diag.hostName,
			"pod", diag.podName,
			"node", diag.nodeName,
			"ctx_has_deadline", diag.ctxHasDeadline,
			"ctx_deadline_remaining_ms", diag.ctxDeadlineRemainingMs,
			"net_timeout", diag.netTimeout,
			"dns_error", diag.dnsError,
			"dns_name", diag.dnsName,
			"dns_server", diag.dnsServer,
			"dns_timeout", diag.dnsIsTimeout,
			"dns_temporary", diag.dnsIsTemporary,
			"err", err,
		)
		span.AddEvent(
			"toolregistry.result_sink_create_failed",
			"toolregistry.result_stream_id", resultStreamID,
			"toolregistry.sink", e.sinkName,
			"toolregistry.error", err.Error(),
			"toolregistry.host", diag.hostName,
			"toolregistry.pod", diag.podName,
			"toolregistry.node", diag.nodeName,
			"toolregistry.ctx_has_deadline", diag.ctxHasDeadline,
			"toolregistry.ctx_deadline_remaining_ms", diag.ctxDeadlineRemainingMs,
			"toolregistry.net_timeout", diag.netTimeout,
			"toolregistry.dns_error", diag.dnsError,
			"toolregistry.dns_name", diag.dnsName,
			"toolregistry.dns_server", diag.dnsServer,
			"toolregistry.dns_timeout", diag.dnsIsTimeout,
			"toolregistry.dns_temporary", diag.dnsIsTemporary,
		)
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
			if ev.EventName == e.outputDeltaKey {
				var msg toolregistry.ToolOutputDeltaMessage
				if err := json.Unmarshal(ev.Payload, &msg); err != nil {
					span.RecordError(err)
					if ackErr := sink.Ack(ctx, ev); ackErr != nil {
						return nil, fmt.Errorf("ack malformed tool output delta message: %w", ackErr)
					}
					continue
				}
				if msg.ToolUseID != toolUseID {
					if err := sink.Ack(ctx, ev); err != nil {
						return nil, fmt.Errorf("ack unrelated tool output delta message: %w", err)
					}
					continue
				}
				if err := sink.Ack(ctx, ev); err != nil {
					span.RecordError(err)
					span.SetStatus(codes.Error, "ack tool output delta message failed")
					return nil, fmt.Errorf("ack tool output delta message: %w", err)
				}

				if e.streamSink != nil {
					p := aistream.ToolOutputDeltaPayload{
						ToolCallID:       meta.ToolCallID,
						ParentToolCallID: meta.ParentToolCallID,
						ToolName:         call.Name.String(),
						Stream:           msg.Stream,
						Delta:            msg.Delta,
					}
					ev := aistream.ToolOutputDelta{
						Base: aistream.NewBase(aistream.EventToolOutputDelta, meta.RunID, meta.SessionID, p),
						Data: p,
					}
					if err := e.streamSink.Send(ctx, ev); err != nil {
						span.RecordError(err)
						e.logger.Error(
							ctx,
							"publish tool output delta failed",
							"component", "tool-registry-executor",
							"tool_use_id", toolUseID,
							"tool", call.Name,
							"err", err,
						)
					}
				}
				continue
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
			return e.decodeToolResult(spec, call, meta.ToolCallID, msg), nil
		}
	}
}

func (e *Executor) decodeToolResult(spec *tools.ToolSpec, call *planner.ToolRequest, toolCallID string, msg toolregistry.ToolResultMessage) *planner.ToolResult {
	tool := tools.Ident("")
	if call != nil {
		tool = call.Name
	}
	out := &planner.ToolResult{
		Name:       tool,
		ToolCallID: toolCallID,
	}
	out.ServerData = marshalServerDataItems(cloneServerDataItems(msg.ServerData))
	if msg.Error != nil {
		out.Error = planner.NewToolError(msg.Error.Message)
		if hint := buildRetryHintFromIssues(tool, spec, msg.Error.Issues); hint != nil {
			out.RetryHint = hint
		} else if hint := retryHintFromToolErrorCode(tool, msg.Error.Code); hint != nil {
			out.RetryHint = hint
		}
		if out.RetryHint != nil && out.RetryHint.ExampleInput == nil {
			out.RetryHint.ExampleInput = cloneExampleInput(spec)
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
	return out
}

func cloneServerDataItems(items []*toolregistry.ServerDataItem) []*toolregistry.ServerDataItem {
	if len(items) == 0 {
		return nil
	}
	out := make([]*toolregistry.ServerDataItem, 0, len(items))
	for _, item := range items {
		if item == nil {
			continue
		}
		out = append(out, &toolregistry.ServerDataItem{
			Kind:     item.Kind,
			Audience: item.Audience,
			Data:     append(json.RawMessage(nil), item.Data...),
		})
	}
	return out
}

func marshalServerDataItems(items []*toolregistry.ServerDataItem) json.RawMessage {
	if len(items) == 0 {
		return nil
	}
	b, err := json.Marshal(items)
	if err != nil {
		panic(fmt.Sprintf("toolregistry executor: marshal server-data items failed: %v", err))
	}
	return json.RawMessage(b)
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
	case "invalid_arguments":
		// Tool-codec validation errors are surfaced by providers as invalid_arguments.
		// These are always user-actionable: they indicate the payload did not satisfy
		// the tool schema (missing fields, enum violations, range constraints, etc.).
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

func cloneExampleInput(spec *tools.ToolSpec) map[string]any {
	if spec == nil || len(spec.Payload.ExampleInput) == 0 {
		return nil
	}
	return cloneAnyMap(spec.Payload.ExampleInput)
}

func cloneAnyMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = cloneAny(v)
	}
	return out
}

func cloneAny(in any) any {
	switch v := in.(type) {
	case map[string]any:
		return cloneAnyMap(v)
	case []any:
		out := make([]any, len(v))
		for i := range v {
			out[i] = cloneAny(v[i])
		}
		return out
	default:
		return in
	}
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

// buildSinkFailureDiagnostics extracts deterministic runtime context for sink
// creation failures (deadline state, host identity, and net/DNS classification)
// without mutating control flow.
func buildSinkFailureDiagnostics(ctx context.Context, err error) sinkFailureDiagnostics {
	diag := sinkFailureDiagnostics{
		hostName: firstNonEmpty(os.Getenv("HOSTNAME"), "unknown"),
		podName:  firstNonEmpty(os.Getenv("POD_NAME"), os.Getenv("HOSTNAME"), "unknown"),
		nodeName: firstNonEmpty(os.Getenv("K8S_NODE_NAME"), os.Getenv("NODE_NAME"), "unknown"),
	}
	if host, hostErr := os.Hostname(); hostErr == nil && host != "" {
		diag.hostName = host
		if diag.podName == "unknown" {
			diag.podName = host
		}
	}
	if deadline, ok := ctx.Deadline(); ok {
		diag.ctxHasDeadline = true
		diag.ctxDeadlineRemainingMs = time.Until(deadline).Milliseconds()
	}
	var networkError net.Error
	if errors.As(err, &networkError) {
		diag.netTimeout = networkError.Timeout()
	}
	var dnsError *net.DNSError
	if errors.As(err, &dnsError) {
		diag.dnsError = true
		diag.dnsName = dnsError.Name
		diag.dnsServer = dnsError.Server
		diag.dnsIsTimeout = dnsError.IsTimeout
		diag.dnsIsTemporary = dnsError.IsTemporary
	}
	return diag
}

// firstNonEmpty returns the first non-empty string from values, or an empty
// string if none are set.
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
