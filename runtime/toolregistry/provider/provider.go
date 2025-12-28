// Package provider implements the provider-side Pulse subscription loop for
// registry-routed tool execution. Providers receive tool calls from a toolset
// stream and publish results to per-call result streams.
package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	pulseclients "goa.design/goa-ai/features/stream/pulse/clients/pulse"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/toolregistry"
	"goa.design/pulse/streaming"
	streamopts "goa.design/pulse/streaming/options"

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

		// SinkAckGracePeriod configures the Pulse sink acknowledgement grace
		// period. When non-zero, Serve passes it to the sink.
		//
		// This value must be identical across all providers using the same sink
		// name for a given toolset stream.
		//
		// Important: If a tool call can take longer than the sink ack grace
		// period and the provider only Ack's after publishing the tool result,
		// Pulse may reclaim and re-deliver the call while it is still in flight.
		// Deployments should set this high enough to cover worst-case tool
		// execution time.
		SinkAckGracePeriod time.Duration

		// Pong acknowledges health pings emitted by the registry gateway.
		// Providers must supply this to participate in health tracking.
		Pong func(ctx context.Context, pingID string) error

		// MaxConcurrentToolCalls caps the number of tool calls executed
		// concurrently by this provider (worker pool size).
		//
		// Serve drains the toolset stream in a dedicated loop and enqueues tool
		// calls for workers; it does not execute tool calls inline. This option
		// exists to bound provider-side resource usage (CPU, memory, upstream
		// concurrency) and to avoid overload amplification.
		//
		// When 0, Serve defaults to a small, safe value.
		MaxConcurrentToolCalls int

		// MaxQueuedToolCalls bounds how many tool calls may be buffered for worker
		// execution. When 0, defaults to a value derived from MaxConcurrentToolCalls.
		//
		// The provider subscription loop never blocks on tool execution. Instead,
		// it enqueues calls and continues draining the toolset stream so it can
		// respond to health pings.
		MaxQueuedToolCalls int

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

	maxConcurrent := opts.MaxConcurrentToolCalls
	if maxConcurrent <= 0 {
		maxConcurrent = 4
	}
	maxQueued := opts.MaxQueuedToolCalls
	if maxQueued <= 0 {
		maxQueued = maxConcurrent * 64
	}

	streamID := toolregistry.ToolsetStreamID(toolset)
	stream, err := pulse.Stream(streamID)
	if err != nil {
		return fmt.Errorf("open toolset stream %q: %w", streamID, err)
	}
	var sinkOpts []streamopts.Sink
	if opts.SinkAckGracePeriod > 0 {
		sinkOpts = append(sinkOpts, streamopts.WithSinkAckGracePeriod(opts.SinkAckGracePeriod))
	}
	sink, err := stream.NewSink(ctx, sinkName, sinkOpts...)
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
	var (
		cancelCtx, cancel = context.WithCancel(ctx)
		wg                sync.WaitGroup
		errc              = make(chan error, 1)
	)
	defer cancel()

	type workItem struct {
		ev  *streaming.Event
		msg toolregistry.ToolCallMessage
	}

	work := make(chan workItem, maxQueued)
	acks := make(chan *streaming.Event, maxQueued+1024)

	signalErr := func(err error) {
		select {
		case errc <- err:
			cancel()
		default:
		}
	}

	ackWG := sync.WaitGroup{}
	ackWG.Add(1)
	go func() {
		defer ackWG.Done()
		for {
			select {
			case <-cancelCtx.Done():
				return
			case ev := <-acks:
				if ev == nil {
					continue
				}
				if err := sink.Ack(cancelCtx, ev); err != nil {
					signalErr(fmt.Errorf("ack toolset event: %w", err))
					return
				}
			}
		}
	}()

	wg.Add(maxConcurrent)
	for i := 0; i < maxConcurrent; i++ {
		go func() {
			defer wg.Done()
			for {
				select {
				case <-cancelCtx.Done():
					return
				case item := <-work:
					callCtx := toolregistry.ExtractTraceContext(cancelCtx, item.msg.TraceParent, item.msg.TraceState, item.msg.Baggage)
					callCtx, span := tracer.Start(
						callCtx,
						"toolregistry.handle",
						trace.WithSpanKind(trace.SpanKindConsumer),
						trace.WithAttributes(
							attribute.String("messaging.system", "pulse"),
							attribute.String("messaging.destination.name", streamID),
							attribute.String("messaging.operation", "process"),
							attribute.String("messaging.message.id", item.ev.ID),
							attribute.String("toolregistry.toolset", toolset),
							attribute.String("toolregistry.tool_use_id", item.msg.ToolUseID),
							attribute.String("toolregistry.tool", item.msg.Tool.String()),
							attribute.String("toolregistry.stream_id", streamID),
							attribute.String("toolregistry.event_id", item.ev.ID),
						),
					)

					res, err := handler.HandleToolCall(callCtx, item.msg)
					if err != nil {
						span.RecordError(err)
						span.SetStatus(codes.Error, "handle tool call")
						logger.Error(
							callCtx,
							"tool call handler failed",
							"component", "tool-registry-provider",
							"toolset", toolset,
							"tool_use_id", item.msg.ToolUseID,
							"tool", item.msg.Tool,
							"err", err,
						)
						res = toolregistry.NewToolResultErrorMessage(item.msg.ToolUseID, "execution_failed", err.Error())
					}

					resultStreamID := toolregistry.ResultStreamID(item.msg.ToolUseID)
					resultStream, streamErr := pulse.Stream(resultStreamID)
					if streamErr != nil {
						span.RecordError(streamErr)
						span.SetStatus(codes.Error, "open result stream")
						span.End()
						signalErr(fmt.Errorf("open result stream %q: %w", resultStreamID, streamErr))
						return
					}
					payload, marshalErr := json.Marshal(res)
					if marshalErr != nil {
						span.RecordError(marshalErr)
						span.SetStatus(codes.Error, "marshal tool result")
						span.End()
						signalErr(fmt.Errorf("marshal tool result: %w", marshalErr))
						return
					}
					if _, addErr := resultStream.Add(callCtx, resultEventType, payload); addErr != nil {
						span.RecordError(addErr)
						span.SetStatus(codes.Error, "publish tool result")
						logger.Error(
							callCtx,
							"publish tool result failed",
							"component", "tool-registry-provider",
							"toolset", toolset,
							"tool_use_id", item.msg.ToolUseID,
							"tool", item.msg.Tool,
							"result_stream_id", resultStreamID,
							"err", addErr,
						)
						span.End()
						signalErr(fmt.Errorf("publish tool result to %q: %w", resultStreamID, addErr))
						return
					}
					span.AddEvent(
						"toolregistry.tool_result_published",
						"toolregistry.result_stream_id", resultStreamID,
					)
					span.End()

					select {
					case acks <- item.ev:
					case <-cancelCtx.Done():
					default:
						signalErr(fmt.Errorf("ack queue full"))
						return
					}
				}
			}
		}()
	}

	pending := make([]workItem, 0, maxQueued)
	flushPending := func() {
		for len(pending) > 0 {
			select {
			case work <- pending[0]:
				pending = pending[1:]
			default:
				return
			}
		}
	}

	for {
		select {
		case <-cancelCtx.Done():
			wg.Wait()
			ackWG.Wait()
			return cancelCtx.Err()
		case err := <-errc:
			wg.Wait()
			ackWG.Wait()
			return err
		case ev, ok := <-events:
			if !ok {
				return fmt.Errorf("toolset stream subscription closed")
			}
			flushPending()
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
				if err := sink.Ack(cancelCtx, ev); err != nil {
					return fmt.Errorf("ack malformed toolset event: %w", err)
				}
				continue
			}
			switch msg.Type {
			case toolregistry.MessageTypePing:
				if msg.PingID != "" {
					if err := opts.Pong(cancelCtx, msg.PingID); err != nil {
						logger.Error(
							cancelCtx,
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
				if err := sink.Ack(cancelCtx, ev); err != nil {
					return fmt.Errorf("ack ping toolset event: %w", err)
				}
				continue
			case toolregistry.MessageTypeCall:
			default:
				if err := sink.Ack(cancelCtx, ev); err != nil {
					return fmt.Errorf("ack unknown toolset event: %w", err)
				}
				continue
			}
			if msg.ToolUseID == "" {
				if err := sink.Ack(cancelCtx, ev); err != nil {
					return fmt.Errorf("ack tool call missing tool_use_id: %w", err)
				}
				continue
			}

			select {
			case work <- workItem{ev: ev, msg: msg}:
			default:
				if len(pending) < cap(pending) {
					pending = append(pending, workItem{ev: ev, msg: msg})
				} else {
					// Intentionally do not ack. Pulse will reclaim and re-deliver the
					// tool call after the sink ack grace period.
					logger.Error(
						cancelCtx,
						"tool call queue full; leaving message unacked for later delivery",
						"component", "tool-registry-provider",
						"toolset", toolset,
						"tool_use_id", msg.ToolUseID,
						"tool", msg.Tool,
						"stream_id", streamID,
						"event_id", ev.ID,
						"max_concurrent", maxConcurrent,
						"max_queued", maxQueued,
					)
				}
			case <-cancelCtx.Done():
			}
		}
	}
}
