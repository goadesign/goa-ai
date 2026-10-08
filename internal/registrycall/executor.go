// Package registrycall owns registry admission and result-stream handling for
// static executors and runtime-discovered tools. It returns completed JSON or
// required input as the existing activity output. Consumers decode completed
// values before returning a model result; workflow scheduling stays separate.
package registrycall

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net"
	"os"
	"strconv"
	"time"

	pulsec "goa.design/goa-ai/features/stream/pulse/clients/pulse"
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	aistream "goa.design/goa-ai/runtime/agent/stream"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/toolregistry"
	"goa.design/goa-ai/runtime/toolserverdata"
	goa "goa.design/goa/v3/pkg"
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
		RetryTool(
			ctx context.Context,
			toolset string,
			tool tools.Ident,
			payload []byte,
			meta toolregistry.ToolCallMeta,
			expectedRegistrationToken string,
		) (toolregistry.ToolCallRef, error)
	}

	// SpecLookup supplies immutable tool contracts for one admitted invocation.
	// The same generated codecs decode its result and server data after delivery.
	SpecLookup interface {
		Spec(name tools.Ident) (*tools.ToolSpec, bool)
	}

	// Executor sends calls for one toolset to the remote registry and decodes
	// the returned result with the generated tool contract.
	Executor struct {
		client  Client
		pulse   pulsec.Client
		toolset string
		specs   SpecLookup

		outputDeltaKey        string
		streamSink            aistream.Sink
		streamToolOutputDelta bool

		logger telemetry.Logger
		tracer telemetry.Tracer
	}

	// Option configures optional executor logging, tracing, and output streaming.
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

// WithStreamSink configures the executor to send tool output fragments to sink
// when profile enables ToolOutputDelta. The final tool result remains
// authoritative regardless of the selected profile. It panics when sink is
// nil or profile enables no events because either value is invalid executor
// configuration.
func WithStreamSink(sink aistream.Sink, profile aistream.StreamProfile) Option {
	if sink == nil {
		panic("tool registry executor: stream sink is required")
	}
	if profile == (aistream.StreamProfile{}) {
		panic("tool registry executor: stream profile must enable at least one event")
	}
	return func(e *Executor) {
		e.streamSink = sink
		e.streamToolOutputDelta = profile.ToolOutputDelta
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

// New builds an executor that sends every call to toolset in the remote
// registry and uses specs to decode returned values.
func New(client Client, pulse pulsec.Client, toolset string, specs SpecLookup, opts ...Option) (*Executor, error) {
	if toolset == "" {
		return nil, errors.New("registry toolset name is required")
	}
	e := &Executor{
		client:         client,
		pulse:          pulse,
		toolset:        toolset,
		specs:          specs,
		outputDeltaKey: toolregistry.OutputDeltaEventKey,
		logger:         telemetry.NewNoopLogger(),
		tracer:         telemetry.NewNoopTracer(),
	}
	for _, o := range opts {
		if o != nil {
			o(e)
		}
	}
	return e, nil
}

// Execute admits one service round and returns its saved activity outcome.
// Host input remains unfinished; completed JSON is decoded only by the consumer.
func (e *Executor) Execute(ctx context.Context, meta *toolregistry.ToolCallMeta, call *api.ToolCall) (*api.ToolOutput, error) {
	if call == nil {
		return internalFailureResult("tool request is nil"), nil
	}
	if meta == nil {
		return internalFailureResult("tool call meta is nil"), nil
	}
	if e.client == nil {
		return internalFailureResult("registry client is nil"), nil
	}
	if e.pulse == nil {
		return internalFailureResult("pulse client is nil"), nil
	}
	if e.specs == nil {
		return internalFailureResult("tool specs lookup is nil"), nil
	}

	_, ok := e.specs.Spec(call.Name)
	if !ok {
		result := internalFailureResult(fmt.Sprintf("unknown tool %q", call.Name))
		result.Failure.Kind = planner.FailureInvalidCall
		result.Failure.Recovery.Action = planner.RecoveryReplan
		return result, nil
	}
	toolsetID := e.toolset
	ctx, span := e.tracer.Start(
		ctx,
		"toolregistry.execute",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("toolregistry.toolset", toolsetID),
			attribute.String("toolregistry.tool", call.Name.String()),
			attribute.String("toolregistry.execution_sequence", strconv.FormatUint(call.ExecutionSequence, 10)),
			attribute.String("toolregistry.run_id", meta.RunID),
			attribute.String("toolregistry.session_id", meta.SessionID),
			attribute.String("toolregistry.turn_id", meta.TurnID),
			attribute.String("toolregistry.tool_call_id", meta.ToolCallID),
			attribute.String("toolregistry.parent_tool_call_id", meta.ParentToolCallID),
			attribute.String("toolregistry.result_event_key", toolregistry.ResultEventKey),
			attribute.String("toolregistry.output_delta_key", e.outputDeltaKey),
		),
	)
	defer span.End()

	tmeta := toolregistry.ToolCallMeta{
		TextOnly:              meta.TextOnly,
		ExecutionSequence:     call.ExecutionSequence,
		ExecutionContinuation: call.ExecutionContinuation,
		RunID:                 meta.RunID,
		SessionID:             meta.SessionID,
		TurnID:                meta.TurnID,
		ToolCallID:            meta.ToolCallID,
		ParentToolCallID:      meta.ParentToolCallID,
		Labels:                maps.Clone(meta.Labels),
	}
	if err := toolregistry.ValidateExecution(tmeta.ExecutionSequence, tmeta.ExecutionContinuation, tmeta.TextOnly); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "invalid workflow execution operation")
		return internalFailureResult(err.Error()), nil
	}
	admissionCtx, cancelAdmission := context.WithTimeout(
		ctx,
		toolregistry.MaxToolCallWait+toolregistry.ResultStreamTransportBudget,
	)
	callRef, err := e.client.CallTool(admissionCtx, toolsetID, call.Name, call.Payload, tmeta)
	cancelAdmission()
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "call tool via registry failed")
		if result, classified := preAdmissionFailureResult(err); classified {
			return result, nil
		}
		return outcomeUnknownResult(err), nil
	}
	if err := toolregistry.ValidateToolCallRef(callRef); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "call tool returned invalid reference")
		return outcomeUnknownResult(
			fmt.Errorf("call tool returned invalid reference: %w", err),
		), nil
	}
	executionCtx, cancelExecution := context.WithDeadline(ctx, callRef.ExecutionDeadline)
	defer cancelExecution()
	toolUseID := callRef.ToolUseID
	resultStreamID := toolregistry.ResultStreamID(toolUseID)
	span.AddEvent(
		"toolregistry.call_tool_ok",
		"toolregistry.tool_use_id", toolUseID,
		"toolregistry.result_stream_id", resultStreamID,
	)

	stream, err := e.pulse.Stream(
		resultStreamID,
		options.WithStreamMaxLen(toolregistry.ResultStreamMaxLen),
		options.WithStreamDeadline(callRef.ResultStreamExpiresAt),
	)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "open tool result stream failed")
		return outcomeUnknownResult(
			fmt.Errorf("open tool result stream %q: %w", resultStreamID, err),
		), nil
	}
	// Result streams are per-tool-call and short-lived. Providers can publish the
	// result very quickly after the registry returns from CallTool, so we must
	// start at the oldest event to avoid missing an already-published result.
	reader, err := stream.NewReader(
		executionCtx,
		options.WithReaderStartAtOldest(),
		options.WithReaderBlockDuration(resultReaderBlockDuration),
	)
	if err != nil {
		diag := buildReaderFailureDiagnostics(executionCtx, err)
		e.logger.Error(
			executionCtx,
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
		return outcomeUnknownResult(err), nil
	}
	defer reader.Close()
	span.AddEvent("toolregistry.result_subscribed", "toolregistry.result_stream_id", resultStreamID)

	events := reader.Subscribe()
	for {
		select {
		case <-executionCtx.Done():
			span.RecordError(executionCtx.Err())
			span.SetStatus(codes.Error, "tool result wait canceled")
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return outcomeUnknownResult(
				fmt.Errorf("tool execution deadline elapsed: %w", executionCtx.Err()),
			), nil
		case ev, ok := <-events:
			if !ok {
				err := fmt.Errorf("tool result stream subscription closed")
				span.RecordError(err)
				span.SetStatus(codes.Error, "tool result stream subscription closed")
				return outcomeUnknownResult(err), nil
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

				if e.streamSink != nil && e.streamToolOutputDelta {
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
					if err := e.streamSink.Send(executionCtx, ev); err != nil {
						span.RecordError(err)
						e.logger.Error(
							executionCtx,
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
			if ev.EventName != toolregistry.ResultEventKey {
				continue
			}

			msg, err := toolregistry.DecodeToolResultMessage(ev.Payload)
			if err != nil {
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
			if err := toolregistry.ValidateToolResultMessage(msg); err != nil {
				span.RecordError(err)
				return nil, fmt.Errorf(
					"toolregistry result for %q is invalid: %w (tool_call_id=%s tool_use_id=%s)",
					call.Name,
					err,
					meta.ToolCallID,
					msg.ToolUseID,
				)
			}
			if msg.Retry != nil {
				retryRef, err := e.client.RetryTool(
					executionCtx,
					toolsetID,
					call.Name,
					call.Payload,
					tmeta,
					callRef.RegistrationToken,
				)
				if err != nil {
					span.RecordError(err)
					return outcomeUnknownResult(err), nil
				}
				if err := toolregistry.ValidateToolCallRef(retryRef); err != nil {
					span.RecordError(err)
					return outcomeUnknownResult(
						fmt.Errorf("retry tool returned invalid reference: %w", err),
					), nil
				}
				if retryRef.ToolUseID != callRef.ToolUseID ||
					retryRef.RegistrationToken != callRef.RegistrationToken ||
					!retryRef.ExecutionDeadline.Equal(callRef.ExecutionDeadline) ||
					!retryRef.ResultStreamExpiresAt.Equal(callRef.ResultStreamExpiresAt) {
					err := fmt.Errorf(
						"toolregistry retry changed admitted call from %+v to %+v",
						callRef,
						retryRef,
					)
					span.RecordError(err)
					return outcomeUnknownResult(err), nil
				}
				continue
			}
			span.AddEvent(
				"toolregistry.result_received",
				"toolregistry.tool_use_id", toolUseID,
				"toolregistry.result_stream_id", resultStreamID,
			)
			if msg.InputRequired != nil {
				if tmeta.TextOnly {
					err := errors.New("text-only registry call returned host input")
					span.RecordError(err)
					span.SetStatus(codes.Error, "provider returned unsupported host input")
					return &api.ToolOutput{Failure: malformedResultFailure(err)}, nil
				}
				span.AddEvent("toolregistry.input_required")
				span.SetStatus(codes.Ok, "waiting for host input")
				return &api.ToolOutput{MCPInput: msg.InputRequired}, nil
			}
			if msg.Error != nil {
				return &api.ToolOutput{Failure: toolFailureFromRegistryError(msg.Error)}, nil
			}
			serverData, err := toolregistry.EncodeServerData(msg.ServerData)
			if err != nil {
				return &api.ToolOutput{Failure: malformedResultFailure(fmt.Errorf("toolregistry server data for %q could not be encoded: %w", call.Name, err))}, nil
			}
			span.SetStatus(codes.Ok, "ok")
			return &api.ToolOutput{Payload: rawjson.Message(msg.Result), Bounds: agent.CloneBounds(msg.Bounds), ServerData: rawjson.Message(serverData)}, nil
		}
	}
}

// preAdmissionFailureResult converts errors that prove provider admission did
// not occur: transport validation rejected the request before service dispatch,
// or the registry replayed a durable rejected decision. Other failures remain
// ambiguous because the registry may have admitted the call before the response
// was lost.
func preAdmissionFailureResult(err error) (*api.ToolOutput, bool) {
	var serviceErr *goa.ServiceError
	if !errors.As(err, &serviceErr) {
		return nil, false
	}
	switch serviceErr.Name {
	case "call_not_admitted", "not_found":
		return &api.ToolOutput{
			Failure: &planner.ToolFailure{
				Kind:  planner.FailureUnavailable,
				Error: planner.ToolErrorFromError(err),
				Recovery: planner.RecoveryDirective{
					Action: planner.RecoveryReplan,
				},
			},
		}, true
	case "validation_error",
		goa.InvalidFieldType,
		goa.MissingField,
		goa.InvalidEnumValue,
		goa.InvalidFormat,
		goa.InvalidPattern,
		goa.InvalidRange,
		goa.InvalidLength,
		goa.UnsupportedMediaType,
		goa.DecodePayload,
		goa.MissingPayload:
		return internalFailureResult(
			fmt.Sprintf("registry rejected a codec-validated tool call: %v", err),
		), true
	default:
		return nil, false
	}
}

// outcomeUnknownResult terminates planning after an invocation may have been
// admitted. A replacement call could repeat an external side effect.
func outcomeUnknownResult(err error) *api.ToolOutput {
	outcomeErr := fmt.Errorf(
		"%s: tool execution outcome is unknown; do not retry or issue a replacement call because the effect may have occurred: %w",
		toolregistry.ToolErrorCodeOutcomeUnknown,
		err,
	)
	return &api.ToolOutput{
		Failure: &planner.ToolFailure{
			Kind:  planner.FailureInternal,
			Error: planner.ToolErrorFromError(outcomeErr),
			Recovery: planner.RecoveryDirective{
				Action: planner.RecoveryFinish,
			},
		},
	}
}

// DecodeCompletedResult checks a completed activity outcome against the tool's
// generated codec and server-data contract. Callers route required input to the
// durable runtime before calling this method; no model result exists for it.
func (e *Executor) DecodeCompletedResult(call *api.ToolCall, meta *toolregistry.ToolCallMeta, output *api.ToolOutput) *planner.ToolResult {
	out := &planner.ToolResult{Failure: output.Failure}
	if call != nil {
		out.Name = call.Name
	}
	if meta != nil {
		out.ToolCallID = meta.ToolCallID
	}
	if output.Failure != nil {
		return out
	}
	spec, ok := e.specs.Spec(call.Name)
	if !ok {
		panic(fmt.Sprintf("registry result contract for %q disappeared during one invocation", call.Name))
	}
	out.Bounds = agent.CloneBounds(output.Bounds)
	if spec.Result.Codec.FromJSON != nil {
		res, err := spec.Result.Codec.FromJSON(output.Payload)
		if err != nil {
			out.Bounds = nil
			out.Failure = malformedResultFailure(fmt.Errorf("toolregistry result for %q did not match registered schema: %w", call.Name, err))
			return out
		}
		out.Result = res
	}
	serverData, err := toolserverdata.Apply(spec.CanonicalizeServerData, output.ServerData)
	if err != nil {
		out.Bounds = nil
		out.Result = nil
		out.Failure = malformedResultFailure(fmt.Errorf("toolregistry server data for %q did not match registered schema: %w", call.Name, err))
		return out
	}
	out.ServerData = serverData
	return out
}

// malformedResultFailure terminates a tool call whose provider output violated
// its registered generated contract.
func malformedResultFailure(err error) *planner.ToolFailure {
	return &planner.ToolFailure{
		Kind:  planner.FailureMalformedResult,
		Error: planner.ToolErrorFromError(err),
		Recovery: planner.RecoveryDirective{
			Action: planner.RecoveryFinish,
		},
	}
}

// toolFailureFromRegistryError restores the provider's classification and
// field issues. The workflow owns model-facing correction evidence.
func toolFailureFromRegistryError(msg *toolregistry.ToolError) *planner.ToolFailure {
	return planner.CloneToolFailure(msg.Failure)
}

// internalFailureResult constructs the terminal result for executor invariant
// failures that a planner cannot correct.
func internalFailureResult(message string) *api.ToolOutput {
	return &api.ToolOutput{
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
