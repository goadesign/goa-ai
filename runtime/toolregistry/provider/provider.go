// Package provider implements the provider-side Pulse subscription loop for
// registry-routed tool execution. Providers receive tool calls from a toolset
// stream and publish results to per-call result streams.
package provider

import (
	"context"
	"encoding/json"
	"fmt"

	pulseclients "goa.design/goa-ai/features/stream/pulse/clients/pulse"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/toolregistry"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

type (
	// Handler executes tool calls received from a toolset stream.
	// Implementations are responsible for decoding/encoding tool payload/result
	// using the compiled tool codecs for their toolset.
	Handler interface {
		HandleToolCall(ctx context.Context, msg toolregistry.ToolCallMessage) (toolregistry.ToolResultMessage, error)
	}

	// Options configure the provider loop.
	Options struct {
		// SinkName identifies the Pulse sink used for subscribing.
		// When empty, defaults to "provider".
		SinkName string
		// ResultEventType is the Pulse entry type used for publishing results.
		// When empty, defaults to "result".
		ResultEventType string

		// Pong acknowledges health pings emitted by the registry gateway.
		// Providers must supply this to participate in health tracking.
		Pong func(ctx context.Context, pingID string) error

		// Logger is used for provider internal logging. When nil, defaults to a noop logger.
		Logger telemetry.Logger
		// Tracer is used for provider spans. When nil, defaults to a noop tracer.
		Tracer telemetry.Tracer
	}
)

// Serve subscribes to the toolset request stream and dispatches tool call
// messages to handler. It publishes tool results to per-call result streams.
func Serve(ctx context.Context, pulse pulseclients.Client, toolset string, handler Handler, opts Options) error {
	if pulse == nil {
		return fmt.Errorf("pulse client is required")
	}
	if toolset == "" {
		return fmt.Errorf("toolset is required")
	}
	if handler == nil {
		return fmt.Errorf("handler is required")
	}
	sinkName := opts.SinkName
	if sinkName == "" {
		sinkName = "provider"
	}
	resultEventType := opts.ResultEventType
	if resultEventType == "" {
		resultEventType = "result"
	}
	if opts.Pong == nil {
		return fmt.Errorf("pong handler is required")
	}
	logger := opts.Logger
	if logger == nil {
		logger = telemetry.NewNoopLogger()
	}
	tracer := opts.Tracer
	if tracer == nil {
		tracer = telemetry.NewNoopTracer()
	}

	streamID := toolregistry.ToolsetStreamID(toolset)
	stream, err := pulse.Stream(streamID)
	if err != nil {
		return fmt.Errorf("open toolset stream %q: %w", streamID, err)
	}
	sink, err := stream.NewSink(ctx, sinkName)
	if err != nil {
		return fmt.Errorf("create sink %q for toolset stream %q: %w", sinkName, streamID, err)
	}
	defer sink.Close(ctx)

	logger.Debug(
		ctx,
		"tool-registry provider subscribed",
		"component", "tool-registry-provider",
		"toolset", toolset,
		"stream_id", streamID,
		"sink", sinkName,
	)

	events := sink.Subscribe()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev, ok := <-events:
			if !ok {
				return fmt.Errorf("toolset stream subscription closed")
			}
			var msg toolregistry.ToolCallMessage
			if err := json.Unmarshal(ev.Payload, &msg); err != nil {
				logger.Error(
					ctx,
					"unmarshal toolset message failed",
					"component", "tool-registry-provider",
					"toolset", toolset,
					"stream_id", streamID,
					"event_id", ev.ID,
					"event_name", ev.EventName,
					"err", err,
				)
				if ackErr := sink.Ack(ctx, ev); ackErr != nil {
					return fmt.Errorf("ack malformed toolset event: %w", ackErr)
				}
				continue
			}
			switch msg.Type {
			case toolregistry.MessageTypePing:
				if msg.PingID != "" {
					if err := opts.Pong(ctx, msg.PingID); err != nil {
						logger.Error(
							ctx,
							"pong failed",
							"component", "tool-registry-provider",
							"toolset", toolset,
							"stream_id", streamID,
							"event_id", ev.ID,
							"ping_id", msg.PingID,
							"err", err,
						)
						return fmt.Errorf("pong: %w", err)
					}
				}
				if ackErr := sink.Ack(ctx, ev); ackErr != nil {
					return fmt.Errorf("ack ping toolset event: %w", ackErr)
				}
				continue
			case toolregistry.MessageTypeCall:
			default:
				if ackErr := sink.Ack(ctx, ev); ackErr != nil {
					return fmt.Errorf("ack unknown toolset event: %w", ackErr)
				}
				continue
			}
			if msg.ToolUseID == "" {
				if ackErr := sink.Ack(ctx, ev); ackErr != nil {
					return fmt.Errorf("ack tool call missing tool_use_id: %w", ackErr)
				}
				continue
			}

			callCtx := toolregistry.ExtractTraceContext(ctx, msg.TraceParent, msg.TraceState, msg.Baggage)
			callCtx, span := tracer.Start(
				callCtx,
				"toolregistry.handle",
				trace.WithSpanKind(trace.SpanKindConsumer),
				trace.WithAttributes(
					attribute.String("messaging.system", "pulse"),
					attribute.String("messaging.destination.name", streamID),
					attribute.String("messaging.operation", "process"),
					attribute.String("messaging.message.id", ev.ID),
					attribute.String("toolregistry.toolset", toolset),
					attribute.String("toolregistry.tool_use_id", msg.ToolUseID),
					attribute.String("toolregistry.tool", msg.Tool.String()),
					attribute.String("toolregistry.stream_id", streamID),
					attribute.String("toolregistry.event_id", ev.ID),
				),
			)

			res, err := handler.HandleToolCall(callCtx, msg)
			if err != nil {
				span.RecordError(err)
				span.SetStatus(codes.Error, "handle tool call")
				logger.Error(
					callCtx,
					"tool call handler failed",
					"component", "tool-registry-provider",
					"toolset", toolset,
					"tool_use_id", msg.ToolUseID,
					"tool", msg.Tool,
					"err", err,
				)
				res = toolregistry.NewToolResultErrorMessage(msg.ToolUseID, "execution_failed", err.Error())
			}

			resultStreamID := toolregistry.ResultStreamID(msg.ToolUseID)
			resultStream, streamErr := pulse.Stream(resultStreamID)
			if streamErr != nil {
				span.RecordError(streamErr)
				span.SetStatus(codes.Error, "open result stream")
				span.End()
				return fmt.Errorf("open result stream %q: %w", resultStreamID, streamErr)
			}
			payload, marshalErr := json.Marshal(res)
			if marshalErr != nil {
				span.RecordError(marshalErr)
				span.SetStatus(codes.Error, "marshal tool result")
				span.End()
				return fmt.Errorf("marshal tool result: %w", marshalErr)
			}
			if _, addErr := resultStream.Add(callCtx, resultEventType, payload); addErr != nil {
				span.RecordError(addErr)
				span.SetStatus(codes.Error, "publish tool result")
				logger.Error(
					callCtx,
					"publish tool result failed",
					"component", "tool-registry-provider",
					"toolset", toolset,
					"tool_use_id", msg.ToolUseID,
					"tool", msg.Tool,
					"result_stream_id", resultStreamID,
					"err", addErr,
				)
				span.End()
				return fmt.Errorf("publish tool result to %q: %w", resultStreamID, addErr)
			}
			span.AddEvent(
				"toolregistry.tool_result_published",
				"toolregistry.result_stream_id", resultStreamID,
			)
			span.End()
			if ackErr := sink.Ack(ctx, ev); ackErr != nil {
				return fmt.Errorf("ack tool call event: %w", ackErr)
			}
		}
	}
}
