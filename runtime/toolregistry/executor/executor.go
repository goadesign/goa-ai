// Package executor provides registry-backed tool execution. It routes tool
// invocations through the registry gateway and awaits results on Pulse streams.
package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"time"

	pulsec "goa.design/goa-ai/features/stream/pulse/clients/pulse"
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
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
	// Client initiates tool calls and returns both transport identity and the
	// exact admission-generation token stamped on the routed request.
	Client interface {
		CallTool(
			ctx context.Context,
			toolset string,
			tool tools.Ident,
			payload []byte,
			meta toolregistry.ToolCallMeta,
		) (toolregistry.ToolCallRef, error)
	}

	// SpecLookup resolves tool specifications for decoding results and server data.
	SpecLookup interface {
		Spec(name tools.Ident) (*tools.ToolSpec, bool)
	}

	Executor struct {
		client Client
		pulse  pulsec.Client
		specs  SpecLookup

		resultEventKey string
		outputDeltaKey string
		streamSink     aistream.Sink

		logger telemetry.Logger
		tracer telemetry.Tracer
	}

	Option func(*Executor)

	// readerFailureDiagnostics captures stable, high-signal context for reader
	// failures so production incidents can be correlated across run/pod/node and
	// quickly classified as DNS or generic network failures.
	readerFailureDiagnostics struct {
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

const resultReaderBlockDuration = 100 * time.Millisecond

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
		resultEventKey: toolregistry.ResultEventKey,
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

func (e *Executor) Execute(ctx context.Context, meta *runtime.ToolCallMeta, call *planner.ToolRequest) (*runtime.ToolExecutionResult, error) {
	if call == nil {
		return runtime.Executed(internalFailureResult("", "", "tool request is nil")), nil
	}
	if meta == nil {
		return runtime.Executed(internalFailureResult(call.Name, "", "tool call meta is nil")), nil
	}
	if e.client == nil {
		return runtime.Executed(internalFailureResult(call.Name, meta.ToolCallID, "registry client is nil")), nil
	}
	if e.pulse == nil {
		return runtime.Executed(internalFailureResult(call.Name, meta.ToolCallID, "pulse client is nil")), nil
	}
	if e.specs == nil {
		return runtime.Executed(internalFailureResult(call.Name, meta.ToolCallID, "tool specs lookup is nil")), nil
	}

	spec, ok := e.specs.Spec(call.Name)
	if !ok {
		result := internalFailureResult(call.Name, meta.ToolCallID, fmt.Sprintf("unknown tool %q", call.Name))
		result.Failure.Kind = planner.FailureInvalidCall
		result.Failure.Recovery.Action = planner.RecoveryReplan
		return runtime.Executed(result), nil
	}
	toolsetID := spec.Toolset
	if toolsetID == "" {
		return runtime.Executed(internalFailureResult(
			call.Name,
			meta.ToolCallID,
			fmt.Sprintf("tool %q missing toolset routing id", call.Name),
		)), nil
	}
	ctx, cancel := context.WithTimeout(ctx, toolregistry.MaxToolCallWait)
	defer cancel()

	ctx, span := e.tracer.Start(
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
	callRef, err := e.client.CallTool(ctx, toolsetID, call.Name, call.Payload, tmeta)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "call tool via registry failed")
		// CallTool only initiates the invocation through the registry gateway; a
		// tool's own domain failure arrives later on the result stream as
		// msg.Error. The runtime therefore exposes a replan transition rather
		// than silently repeating a call whose admission state is unknown.
		return runtime.Executed(&planner.ToolResult{
			Name:       call.Name,
			ToolCallID: meta.ToolCallID,
			Failure: &planner.ToolFailure{
				Kind:  planner.FailureUnavailable,
				Error: planner.ToolErrorFromError(err),
				Recovery: planner.RecoveryDirective{
					Action: planner.RecoveryReplan,
				},
			},
		}), nil
	}
	if err := toolregistry.ValidateRegistrationToken(callRef.RegistrationToken); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "call tool returned invalid registration token")
		return nil, fmt.Errorf("call tool returned invalid registration token: %w", err)
	}
	if err := toolregistry.ValidateResultStreamTTLMillis(callRef.ResultStreamTTL.Milliseconds()); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "call tool returned invalid result stream TTL")
		return nil, fmt.Errorf("call tool returned invalid result stream TTL: %w", err)
	}
	toolUseID := callRef.ToolUseID
	resultStreamID := toolregistry.ResultStreamID(toolUseID)
	span.AddEvent(
		"toolregistry.call_tool_ok",
		"toolregistry.tool_use_id", toolUseID,
		"toolregistry.result_stream_id", resultStreamID,
	)

	stream, err := e.pulse.Stream(
		resultStreamID,
		options.WithStreamSlidingTTL(callRef.ResultStreamTTL),
	)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "open tool result stream failed")
		return runtime.Executed(e.unavailableResult(
			call,
			meta,
			fmt.Errorf("open tool result stream %q: %w", resultStreamID, err),
		)), nil
	}
	// Result streams are per-tool-call and short-lived. Providers can publish the
	// result very quickly after the registry returns from CallTool, so we must
	// start at the oldest event to avoid missing an already-published result.
	reader, err := stream.NewReader(
		ctx,
		options.WithReaderStartAtOldest(),
		options.WithReaderBlockDuration(resultReaderBlockDuration),
	)
	if err != nil {
		diag := buildReaderFailureDiagnostics(ctx, err)
		e.logger.Error(
			ctx,
			"toolregistry result stream reader create failed",
			"component", "tool-registry-executor",
			"toolset", toolsetID,
			"tool", call.Name,
			"tool_use_id", toolUseID,
			"run_id", meta.RunID,
			"session_id", meta.SessionID,
			"turn_id", meta.TurnID,
			"tool_call_id", meta.ToolCallID,
			"result_stream_id", resultStreamID,
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
			"toolregistry.result_reader_create_failed",
			"toolregistry.result_stream_id", resultStreamID,
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
		span.SetStatus(codes.Error, "create reader for tool result stream failed")
		return runtime.Executed(e.unavailableResult(call, meta, err)), nil
	}
	defer reader.Close()
	span.AddEvent("toolregistry.result_subscribed", "toolregistry.result_stream_id", resultStreamID)

	events := reader.Subscribe()
	for {
		select {
		case <-ctx.Done():
			span.RecordError(ctx.Err())
			span.SetStatus(codes.Error, "tool result wait canceled")
			return nil, ctx.Err()
		case ev, ok := <-events:
			if !ok {
				err := fmt.Errorf("tool result stream subscription closed")
				span.RecordError(err)
				span.SetStatus(codes.Error, "tool result stream subscription closed")
				return runtime.Executed(e.unavailableResult(call, meta, err)), nil
			}
			if ev.EventName == e.outputDeltaKey {
				var msg toolregistry.ToolOutputDeltaMessage
				if err := json.Unmarshal(ev.Payload, &msg); err != nil {
					span.RecordError(err)
					continue
				}
				if err := toolregistry.ValidateRegistrationToken(msg.RegistrationToken); err != nil {
					span.RecordError(err)
					continue
				}
				if msg.ToolUseID != toolUseID || msg.RegistrationToken != callRef.RegistrationToken {
					continue
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
				continue
			}

			var msg toolregistry.ToolResultMessage
			if err := json.Unmarshal(ev.Payload, &msg); err != nil {
				span.RecordError(err)
				return nil, fmt.Errorf("decode terminal tool result event %s: %w", ev.ID, err)
			}
			if err := toolregistry.ValidateRegistrationToken(msg.RegistrationToken); err != nil {
				span.RecordError(err)
				return nil, fmt.Errorf("validate terminal tool result event %s: %w", ev.ID, err)
			}
			if msg.ToolUseID != toolUseID || msg.RegistrationToken != callRef.RegistrationToken {
				continue
			}
			if msg.Error != nil && msg.Error.Code == toolregistry.ToolErrorCodeProviderOverloaded {
				if _, err := e.client.CallTool(ctx, toolsetID, call.Name, call.Payload, tmeta); err != nil {
					span.RecordError(err)
					return runtime.Executed(e.unavailableResult(call, meta, err)), nil
				}
				continue
			}
			span.AddEvent(
				"toolregistry.result_received",
				"toolregistry.tool_use_id", toolUseID,
				"toolregistry.result_stream_id", resultStreamID,
			)
			result, err := e.decodeToolResult(spec, call, meta.ToolCallID, msg)
			if err != nil {
				span.RecordError(err)
				span.SetStatus(codes.Error, "decode tool result failed")
				return nil, err
			}
			span.SetStatus(codes.Ok, "ok")
			return runtime.Executed(result), nil
		}
	}
}

// unavailableResult maps registry and Pulse infrastructure failures to a
// planner-visible replan transition. The executor never repeats an admitted
// call whose completion state may be uncertain.
func (e *Executor) unavailableResult(
	call *planner.ToolRequest,
	meta *runtime.ToolCallMeta,
	err error,
) *planner.ToolResult {
	return &planner.ToolResult{
		Name:       call.Name,
		ToolCallID: meta.ToolCallID,
		Failure: &planner.ToolFailure{
			Kind:  planner.FailureUnavailable,
			Error: planner.ToolErrorFromError(err),
			Recovery: planner.RecoveryDirective{
				Action: planner.RecoveryReplan,
			},
		},
	}
}

func (e *Executor) decodeToolResult(spec *tools.ToolSpec, call *planner.ToolRequest, toolCallID string, msg toolregistry.ToolResultMessage) (*planner.ToolResult, error) {
	tool := tools.Ident("")
	if call != nil {
		tool = call.Name
	}
	if msg.Error != nil {
		if msg.Bounds != nil {
			return nil, fmt.Errorf("toolregistry result for %q is invalid: error and bounds are both set (tool_call_id=%s tool_use_id=%s)", tool, toolCallID, msg.ToolUseID)
		}
		if rawMessageHasNonNullJSON(msg.Result) {
			return nil, fmt.Errorf("toolregistry result for %q is invalid: error and result are both set (tool_call_id=%s tool_use_id=%s)", tool, toolCallID, msg.ToolUseID)
		}
	}
	out := &planner.ToolResult{
		Name:       tool,
		ToolCallID: toolCallID,
	}
	if msg.Error != nil {
		out.Failure = toolFailureFromRegistryError(msg.Error, spec, call)
		return out, nil
	}
	out.Bounds = agent.CloneBounds(msg.Bounds)
	out.ServerData = marshalServerDataItems(cloneServerDataItems(msg.ServerData))
	if spec.Result.Codec.FromJSON != nil {
		res, err := spec.Result.Codec.FromJSON(msg.Result)
		if err != nil {
			decodeErr := fmt.Errorf("toolregistry result for %q did not match registered schema: %w", tool, err)
			out.Bounds = nil
			out.ServerData = nil
			out.Failure = &planner.ToolFailure{
				Kind:  planner.FailureMalformedResult,
				Error: planner.ToolErrorFromError(decodeErr),
				Recovery: planner.RecoveryDirective{
					Action: planner.RecoveryFinish,
				},
			}
			return out, nil
		}
		out.Result = res
	}
	return out, nil
}

// rawMessageHasNonNullJSON reports whether a wire result carries a real payload.
// The registry protocol treats an omitted result and JSON null as absent.
func rawMessageHasNonNullJSON(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null"))
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

func marshalServerDataItems(items []*toolregistry.ServerDataItem) rawjson.Message {
	if len(items) == 0 {
		return nil
	}
	b, err := json.Marshal(items)
	if err != nil {
		panic(fmt.Sprintf("toolregistry executor: marshal server-data items failed: %v", err))
	}
	return rawjson.Message(b)
}

// toolFailureFromRegistryError translates the provider protocol into the
// runtime's canonical classification and transition. Structured provider
// issues are preserved verbatim and only valid-call corrections receive the
// rejected input and generated payload example.
func toolFailureFromRegistryError(msg *toolregistry.ToolError, spec *tools.ToolSpec, call *planner.ToolRequest) *planner.ToolFailure {
	failure := &planner.ToolFailure{
		Kind:  planner.FailureInternal,
		Error: planner.NewToolError(msg.Message),
		Recovery: planner.RecoveryDirective{
			Action: planner.RecoveryFinish,
		},
	}
	switch msg.Code {
	case toolregistry.ToolErrorCodeStaleRegistration,
		toolregistry.ToolErrorCodeProviderOverloaded:
		failure.Kind = planner.FailureUnavailable
		failure.Recovery.Action = planner.RecoveryReplan
	case "rate_limited":
		failure.Kind = planner.FailureRateLimited
		failure.Recovery.Action = planner.RecoveryReplan
	case "invalid_input":
		failure.Kind = planner.FailureDomainRejection
		failure.Recovery.Action = planner.RecoveryReplan
	case "invalid_arguments":
		if spec == nil || call == nil {
			panic("toolregistry executor: invalid_arguments requires the registered spec and rejected call")
		}
		failure.Kind = planner.FailureInvalidCall
		failure.Recovery = planner.RecoveryDirective{
			Action:      planner.RecoveryCorrectCall,
			Issues:      msg.Issues,
			PriorInput:  append(rawjson.Message(nil), call.Payload...),
			ExampleJSON: append(rawjson.Message(nil), spec.Payload.ExampleJSON...),
		}
	case "timeout":
		failure.Kind = planner.FailureTimeout
	}
	return failure
}

// internalFailureResult constructs the terminal result for executor invariant
// failures that a planner cannot correct.
func internalFailureResult(name tools.Ident, toolCallID, message string) *planner.ToolResult {
	return &planner.ToolResult{
		Name:       name,
		ToolCallID: toolCallID,
		Failure: &planner.ToolFailure{
			Kind:  planner.FailureInternal,
			Error: planner.NewToolError(message),
			Recovery: planner.RecoveryDirective{
				Action: planner.RecoveryFinish,
			},
		},
	}
}

// buildReaderFailureDiagnostics extracts deterministic runtime context for reader
// creation failures (deadline state, host identity, and net/DNS classification)
// without mutating control flow.
func buildReaderFailureDiagnostics(ctx context.Context, err error) readerFailureDiagnostics {
	diag := readerFailureDiagnostics{
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
