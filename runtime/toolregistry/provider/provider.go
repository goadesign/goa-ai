// Package provider implements the provider-side Pulse subscription loop for
// registry-routed tool execution. Providers receive tool calls from a toolset
// stream and publish results to per-call result streams.
package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
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
	// using the compiled tool codecs for their toolset. Pulse redelivers a call
	// when result publication succeeds but acknowledgement fails, so handlers
	// must make repeated ToolUseID execution idempotent or durably deduplicate it.
	// Handlers must also honor ctx cancellation and return promptly so Serve can
	// join every worker before releasing its exact provider lease.
	Handler interface {
		HandleToolCall(ctx context.Context, msg toolregistry.ToolCallMessage) (toolregistry.ToolResultMessage, error)
	}

	// Options configure the provider loop.
	Options struct {
		// ProviderID is the stable identity of this provider process for this
		// toolset. Deployments should derive it from pod identity plus toolset.
		ProviderID string

		// SinkName identifies the Pulse sink used for subscribing.
		// When empty, defaults to "provider".
		SinkName string

		// ResultEventType is the Pulse entry type used for publishing results.
		// When empty, defaults to toolregistry.ResultEventKey.
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
		Pong func(ctx context.Context, providerID, incarnationID, pingID string) error

		// PongTimeout bounds how long Serve will wait for the Pong callback to
		// return when handling a ping message.
		//
		// Contract:
		//   - Ping messages exist solely for toolset health tracking; they are not part
		//     of tool execution correctness.
		//   - Pong failures must never crash the provider loop. If the registry is
		//     temporarily unreachable, the toolset should be marked unhealthy by the
		//     registry, and the provider should continue draining the stream so it can
		//     recover without a restart loop.
		//
		// When 0, Serve defaults to a short value suitable for transient outages.
		PongTimeout time.Duration

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

		// ShutdownTimeout bounds sink closure, worker/result settlement, and
		// acknowledgement drain after Serve stops claiming new calls. Zero uses
		// DefaultShutdownTimeout.
		ShutdownTimeout time.Duration

		// Logger is used for provider internal logging. When nil, defaults to a noop logger.
		Logger telemetry.Logger

		// Tracer is used for provider spans. When nil, defaults to a noop tracer.
		Tracer telemetry.Tracer
	}

	// pulseOutputDeltaPublisher publishes best-effort tool output fragments to the
	// tool call's per-call result stream (`result:<tool_use_id>`).
	//
	// Contract:
	//   - This is a UX-only signal: consumers may drop deltas without affecting
	//     correctness.
	//   - The canonical tool output remains the final ToolResultMessage published
	//     under the result event key.
	pulseOutputDeltaPublisher struct {
		stream            pulseclients.Stream
		registrationToken string
		toolUseID         string
	}
)

const (
	// DefaultShutdownTimeout bounds the provider's graceful consumption stop
	// and settlement phase before lease expiry becomes the durable fallback.
	DefaultShutdownTimeout = 30 * time.Second
	// MaxConcurrentToolCalls bounds worker allocation from provider config.
	MaxConcurrentToolCalls = 1024
	// MaxQueuedToolCalls bounds waiting work and acknowledgement allocation.
	MaxQueuedToolCalls = 65536
	// MaxOverloadPublishDuration bounds queue-saturation publication so the
	// intake loop resumes health pings promptly.
	MaxOverloadPublishDuration = 2 * time.Second
)

func (p *pulseOutputDeltaPublisher) PublishToolOutputDelta(ctx context.Context, stream string, delta string) error {
	msg := toolregistry.NewToolOutputDeltaMessage(p.registrationToken, p.toolUseID, stream, delta)
	payload, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal tool output delta: %w", err)
	}
	_, err = p.stream.Add(ctx, toolregistry.OutputDeltaEventKey, payload)
	if err != nil {
		return fmt.Errorf("publish tool output delta: %w", err)
	}
	return nil
}

// Serve prepares the toolset request stream, establishes the required registry
// registration, and then dispatches tool calls to handler. It publishes tool
// results to per-call result streams and renews registration until shutdown.
func Serve(
	ctx context.Context,
	pulse pulseclients.Client,
	toolset string,
	handler Handler,
	registration Registration,
	opts Options,
) error {
	if pulse == nil {
		return fmt.Errorf("pulse client is required")
	}
	if toolset == "" {
		return fmt.Errorf("toolset is required")
	}
	if handler == nil {
		return fmt.Errorf("handler is required")
	}
	if opts.ProviderID == "" {
		return fmt.Errorf("provider id is required")
	}
	registrationConfig, err := registration.normalized()
	if err != nil {
		return err
	}
	incarnationID := uuid.NewString()
	sinkName := opts.SinkName
	if sinkName == "" {
		sinkName = "provider"
	}
	resultEventType := opts.ResultEventType
	if resultEventType == "" {
		resultEventType = toolregistry.ResultEventKey
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
	pongTimeout := opts.PongTimeout
	if pongTimeout <= 0 {
		pongTimeout = 2 * time.Second
	}

	maxConcurrent := opts.MaxConcurrentToolCalls
	if maxConcurrent < 0 {
		return fmt.Errorf("max concurrent tool calls must not be negative")
	}
	if maxConcurrent == 0 {
		maxConcurrent = 4
	}
	if maxConcurrent > MaxConcurrentToolCalls {
		return fmt.Errorf("max concurrent tool calls must not exceed %d", MaxConcurrentToolCalls)
	}
	maxQueued := opts.MaxQueuedToolCalls
	if maxQueued < 0 {
		return fmt.Errorf("max queued tool calls must not be negative")
	}
	if maxQueued == 0 {
		maxQueued = maxConcurrent * 64
	}
	if maxQueued > MaxQueuedToolCalls {
		return fmt.Errorf("max queued tool calls must not exceed %d", MaxQueuedToolCalls)
	}
	shutdownTimeout := opts.ShutdownTimeout
	if shutdownTimeout < 0 {
		return fmt.Errorf("shutdown timeout must not be negative")
	}
	if shutdownTimeout == 0 {
		shutdownTimeout = DefaultShutdownTimeout
	}

	streamID := toolregistry.ToolsetStreamID(toolset)
	stream, err := pulse.Stream(streamID)
	if err != nil {
		return fmt.Errorf("open toolset stream %q: %w", streamID, err)
	}
	cancelCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	registrationState, err := registerUntilSuccess(
		cancelCtx,
		toolset,
		opts.ProviderID,
		incarnationID,
		registrationConfig,
		logger,
		waitRegistrationDelay,
	)
	if err != nil {
		return err
	}
	admittedToken := registrationState.lease.RegistrationToken

	var sinkOpts []streamopts.Sink
	if opts.SinkAckGracePeriod > 0 {
		sinkOpts = append(sinkOpts, streamopts.WithSinkAckGracePeriod(opts.SinkAckGracePeriod))
	}
	sink, err := stream.NewSink(ctx, sinkName, sinkOpts...)
	if err != nil {
		releaseErr := releaseProvider(
			context.WithoutCancel(ctx),
			toolset,
			opts.ProviderID,
			incarnationID,
			admittedToken,
			registrationConfig,
			logger,
			waitRegistrationDelay,
		)
		return errors.Join(
			fmt.Errorf("create sink %q for toolset stream %q: %w", sinkName, streamID, err),
			releaseErr,
		)
	}

	logger.Debug(
		ctx,
		"tool-registry provider subscribed",
		"component", "tool-registry-provider",
		"toolset", toolset,
		"provider_id", opts.ProviderID,
		"stream_id", streamID,
		"sink", sinkName,
	)

	events := sink.Subscribe()
	var (
		errc          = make(chan error, 1)
		settleErrMu   sync.Mutex
		settlementErr error
	)

	type workItem struct {
		ev  *streaming.Event
		msg toolregistry.ToolCallMessage
	}

	work := make(chan workItem, maxQueued)
	acks := make(chan *streaming.Event, maxConcurrent+maxQueued)
	workerDone := make(chan struct{}, maxConcurrent)
	ackDone := make(chan struct{})
	abortAcks := make(chan struct{})

	signalStop := func(err error) {
		select {
		case errc <- err:
		default:
		}
	}
	signalSettleFailure := func(err error) {
		settleErrMu.Lock()
		settlementErr = errors.Join(settlementErr, err)
		settleErrMu.Unlock()
		signalStop(err)
	}

	registrationDone := make(chan struct{})
	go func() {
		defer close(registrationDone)
		if err := superviseRegistration(
			cancelCtx,
			toolset,
			opts.ProviderID,
			incarnationID,
			registrationState,
			registrationConfig,
			logger,
			waitRegistrationDelay,
		); err != nil && cancelCtx.Err() == nil {
			signalStop(err)
		}
	}()

	go func() {
		defer close(ackDone)
		for {
			select {
			case ev, ok := <-acks:
				if !ok {
					return
				}
				ackCtx, ackCancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
				err := sink.Ack(ackCtx, ev)
				ackCancel()
				if err != nil {
					signalSettleFailure(fmt.Errorf("ack toolset event %s: %w", ev.ID, err))
				}
			case <-abortAcks:
				return
			}
		}
	}()

	for i := 0; i < maxConcurrent; i++ {
		go func() {
			defer func() {
				workerDone <- struct{}{}
			}()
			for item := range work {
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
						attribute.String("toolregistry.provider_id", opts.ProviderID),
						attribute.String("toolregistry.tool_use_id", item.msg.ToolUseID),
						attribute.String("toolregistry.tool", item.msg.Tool.String()),
						attribute.String("toolregistry.stream_id", streamID),
						attribute.String("toolregistry.event_id", item.ev.ID),
					),
				)

				resultStreamID := toolregistry.ResultStreamID(item.msg.ToolUseID)
				resultStreamTTL := time.Duration(item.msg.ResultStreamTTLMillis) * time.Millisecond
				resultStream, streamErr := pulse.Stream(
					resultStreamID,
					streamopts.WithStreamSlidingTTL(resultStreamTTL),
				)
				if streamErr != nil {
					span.RecordError(streamErr)
					span.SetStatus(codes.Error, "open result stream")
					span.End()
					signalSettleFailure(fmt.Errorf("open result stream %q: %w", resultStreamID, streamErr))
					continue
				}

				var res toolregistry.ToolResultMessage
				if item.msg.RegistrationToken != admittedToken {
					res = toolregistry.NewToolResultErrorMessage(
						item.msg.RegistrationToken,
						item.msg.ToolUseID,
						toolregistry.ToolErrorCodeStaleRegistration,
						fmt.Sprintf(
							"queued tool call belongs to registration %q; provider serves %q",
							item.msg.RegistrationToken,
							admittedToken,
						),
					)
				} else {
					callCtx = toolregistry.WithOutputDeltaPublisher(callCtx, &pulseOutputDeltaPublisher{
						stream:            resultStream,
						registrationToken: item.msg.RegistrationToken,
						toolUseID:         item.msg.ToolUseID,
					})

					var handlerErr error
					res, handlerErr = handler.HandleToolCall(callCtx, item.msg)
					if handlerErr != nil {
						span.RecordError(handlerErr)
						span.SetStatus(codes.Error, "handle tool call")
						logger.Error(
							callCtx,
							"tool call handler failed",
							"component", "tool-registry-provider",
							"toolset", toolset,
							"provider_id", opts.ProviderID,
							"tool_use_id", item.msg.ToolUseID,
							"tool", item.msg.Tool,
							"err", handlerErr,
						)
						res = toolregistry.NewToolResultErrorMessage(
							item.msg.RegistrationToken,
							item.msg.ToolUseID,
							"execution_failed",
							handlerErr.Error(),
						)
					}
				}
				res.RegistrationToken = item.msg.RegistrationToken
				res.ToolUseID = item.msg.ToolUseID

				payload, marshalErr := json.Marshal(res)
				if marshalErr != nil {
					span.RecordError(marshalErr)
					span.SetStatus(codes.Error, "marshal tool result")
					span.End()
					signalSettleFailure(fmt.Errorf("marshal tool result: %w", marshalErr))
					continue
				}
				publishCtx, publishCancel := context.WithTimeout(context.WithoutCancel(callCtx), shutdownTimeout)
				_, addErr := resultStream.Add(publishCtx, resultEventType, payload)
				publishCancel()
				if addErr != nil {
					span.RecordError(addErr)
					span.SetStatus(codes.Error, "publish tool result")
					logger.Error(
						callCtx,
						"publish tool result failed",
						"component", "tool-registry-provider",
						"toolset", toolset,
						"provider_id", opts.ProviderID,
						"tool_use_id", item.msg.ToolUseID,
						"tool", item.msg.Tool,
						"result_stream_id", resultStreamID,
						"err", addErr,
					)
					span.End()
					signalSettleFailure(fmt.Errorf("publish tool result to %q: %w", resultStreamID, addErr))
					continue
				}
				span.AddEvent(
					"toolregistry.tool_result_published",
					"toolregistry.result_stream_id", resultStreamID,
				)
				span.End()

				acks <- item.ev
			}
		}()
	}

	finish := func(runErr error) error {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
		defer shutdownCancel()

		closeErr := closeSinkBounded(shutdownCtx, sink)
		cancel()
		close(work)

		registrationErr := waitForDone(shutdownCtx, registrationDone, "registration renewal")
		workerErr := waitForWorkers(shutdownCtx, workerDone, maxConcurrent)
		if workerErr == nil {
			close(acks)
		} else {
			close(abortAcks)
		}
		ackDrainErr := waitForDone(shutdownCtx, ackDone, "acknowledgements")

		settleErrMu.Lock()
		recordedSettlementErr := settlementErr
		settleErrMu.Unlock()

		stopErr := errors.Join(closeErr, registrationErr, workerErr, ackDrainErr, recordedSettlementErr)
		if stopErr != nil {
			return errors.Join(runErr, stopErr)
		}

		releaseTokens := []string{admittedToken}
		var changedTokenErr *registrationTokenChangedError
		if errors.As(runErr, &changedTokenErr) {
			releaseTokens = append(releaseTokens, changedTokenErr.receivedToken)
		}
		var releaseErr error
		for _, token := range releaseTokens {
			releaseErr = errors.Join(
				releaseErr,
				releaseProvider(
					context.WithoutCancel(ctx),
					toolset,
					opts.ProviderID,
					incarnationID,
					token,
					registrationConfig,
					logger,
					waitRegistrationDelay,
				),
			)
		}
		return errors.Join(runErr, releaseErr)
	}
	ackImmediate := func(ev *streaming.Event, description string) error {
		ackCtx, ackCancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
		err := sink.Ack(ackCtx, ev)
		ackCancel()
		if err != nil {
			return fmt.Errorf("ack %s event %s: %w", description, ev.ID, err)
		}
		return nil
	}

	for {
		select {
		case <-cancelCtx.Done():
			select {
			case err := <-errc:
				return finish(err)
			default:
			}
			return finish(cancelCtx.Err())
		case err := <-errc:
			return finish(err)
		case ev, ok := <-events:
			if !ok {
				return finish(fmt.Errorf("toolset stream subscription closed"))
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
				if err := ackImmediate(ev, "malformed toolset"); err != nil {
					signalSettleFailure(err)
					return finish(err)
				}
				continue
			}
			if err := toolregistry.ValidateToolCallMessage(msg); err != nil {
				logger.Error(
					ctx,
					"validate toolset message failed",
					"component", "tool-registry-provider",
					"toolset", toolset,
					"stream_id", streamID,
					"event_id", ev.ID,
					"event_name", ev.EventName,
					"err", err,
				)
				if err := ackImmediate(ev, "invalid toolset"); err != nil {
					signalSettleFailure(err)
					return finish(err)
				}
				continue
			}
			switch msg.Type {
			case toolregistry.MessageTypePing:
				if msg.RegistrationToken == admittedToken && msg.PingID != "" {
					pongCtx, pongCancel := context.WithTimeout(cancelCtx, pongTimeout)
					err := opts.Pong(pongCtx, opts.ProviderID, incarnationID, msg.PingID)
					pongCancel()
					if err != nil {
						logger.Error(
							cancelCtx,
							"pong failed",
							"component", "tool-registry-provider",
							"toolset", toolset,
							"provider_id", opts.ProviderID,
							"stream_id", streamID,
							"event_id", ev.ID,
							"ping_id", msg.PingID,
							"err", err,
						)
					}
				}
				if err := ackImmediate(ev, "ping toolset"); err != nil {
					signalSettleFailure(err)
					return finish(err)
				}
				continue
			case toolregistry.MessageTypeCall:
			default:
				if err := ackImmediate(ev, "unknown toolset"); err != nil {
					signalSettleFailure(err)
					return finish(err)
				}
				continue
			}
			if msg.ToolUseID == "" {
				if err := ackImmediate(ev, "tool call missing tool_use_id"); err != nil {
					signalSettleFailure(err)
					return finish(err)
				}
				continue
			}

			select {
			case work <- workItem{ev: ev, msg: msg}:
			default:
				logger.Error(
					cancelCtx,
					"tool call queue full; completing as provider overloaded",
					"component", "tool-registry-provider",
					"toolset", toolset,
					"provider_id", opts.ProviderID,
					"tool_use_id", msg.ToolUseID,
					"tool", msg.Tool,
					"stream_id", streamID,
					"event_id", ev.ID,
					"max_concurrent", maxConcurrent,
					"max_queued", maxQueued,
				)
				if err := publishOverloadedResult(
					cancelCtx,
					pulse,
					msg,
					resultEventType,
					shutdownTimeout,
				); err != nil {
					signalSettleFailure(err)
					return finish(err)
				}
				if err := ackImmediate(ev, "overloaded tool call"); err != nil {
					signalSettleFailure(err)
					return finish(err)
				}
			case <-cancelCtx.Done():
			}
		}
	}
}

// publishOverloadedResult records a transient saturation event before the
// request is acknowledged. Registry-owned call admission serializes the
// bounded republish; providers remain free to drain pings meanwhile.
func publishOverloadedResult(
	ctx context.Context,
	pulse pulseclients.Client,
	message toolregistry.ToolCallMessage,
	eventType string,
	timeout time.Duration,
) error {
	resultStreamID := toolregistry.ResultStreamID(message.ToolUseID)
	resultStreamTTL := time.Duration(message.ResultStreamTTLMillis) * time.Millisecond
	resultStream, err := pulse.Stream(
		resultStreamID,
		streamopts.WithStreamSlidingTTL(resultStreamTTL),
	)
	if err != nil {
		return fmt.Errorf("open overloaded result stream %q: %w", resultStreamID, err)
	}
	result := toolregistry.NewToolResultErrorMessage(
		message.RegistrationToken,
		message.ToolUseID,
		toolregistry.ToolErrorCodeProviderOverloaded,
		"provider capacity is full; retry this tool call",
	)
	result.Error.RetryAfterMillis = toolregistry.ProviderOverloadRetryAfter.Milliseconds()
	payload, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("marshal overloaded tool result: %w", err)
	}
	publishCtx, publishCancel := context.WithTimeout(
		context.WithoutCancel(ctx),
		min(timeout, MaxOverloadPublishDuration),
	)
	defer publishCancel()
	if _, err := resultStream.Add(publishCtx, eventType, payload); err != nil {
		return fmt.Errorf("publish overloaded tool result to %q: %w", resultStreamID, err)
	}
	return nil
}

// closeSinkBounded stops consumer-group claiming and proves Close returned
// before the lifecycle shutdown deadline.
func closeSinkBounded(ctx context.Context, sink pulseclients.Sink) error {
	closed := make(chan struct{})
	go func() {
		sink.Close(ctx)
		close(closed)
	}()
	select {
	case <-closed:
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("close toolset sink: %w", err)
		}
		return nil
	case <-ctx.Done():
		return fmt.Errorf("close toolset sink: %w", ctx.Err())
	}
}

// waitForWorkers joins each configured worker without a waiter goroutine that
// could outlive Serve after a handler violates the cancellation contract.
func waitForWorkers(ctx context.Context, done <-chan struct{}, count int) error {
	for i := 0; i < count; i++ {
		if err := waitForDone(ctx, done, fmt.Sprintf("tool worker %d/%d", i+1, count)); err != nil {
			return err
		}
	}
	return nil
}

// waitForDone reports when a shutdown phase cannot prove completion.
func waitForDone(ctx context.Context, done <-chan struct{}, phase string) error {
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("settle %s: %w", phase, ctx.Err())
	}
}
