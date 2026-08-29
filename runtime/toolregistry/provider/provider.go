// Package provider implements the provider-side Pulse subscription loop for
// registry-routed tool execution. Providers receive tool calls from a toolset
// stream and submit claimed-call results through the registry boundary.
package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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
	// ClaimRequest identifies one registry ownership operation. The operation
	// ID remains fixed across transport retries but changes when Pulse later
	// redelivers the same request event.
	ClaimRequest struct {
		// Toolset is the qualified toolset receiving the call.
		Toolset string
		// ProviderID is the stable identity of the provider process.
		ProviderID string
		// ProviderIncarnationID identifies this Serve lifecycle.
		ProviderIncarnationID string
		// ProviderRegistrationToken is the exact token held by this provider.
		ProviderRegistrationToken string
		// CallRegistrationToken is the token stamped on the queued call.
		CallRegistrationToken string
		// ToolUseID is the global identity of the tool call.
		ToolUseID string
		// RequestEventID is the Pulse event being claimed.
		RequestEventID string
		// OperationID identifies this claim and all of its transport retries.
		OperationID string
	}

	// Handler executes tool calls received from a toolset stream.
	// Implementations are responsible for decoding/encoding tool payload/result
	// using the compiled tool codecs for their toolset. Serve obtains one durable
	// registry dispatch claim before invoking a handler, so Pulse redelivery
	// cannot repeat execution. Handlers must honor ctx cancellation and return
	// promptly so Serve can join every worker before releasing its exact lease.
	Handler interface {
		HandleToolCall(ctx context.Context, msg toolregistry.ToolCallMessage) (toolregistry.ToolResultMessage, error)
	}

	// Options configure the provider loop.
	Options struct {
		// ProviderID is the stable identity of this provider process for this
		// toolset. Deployments should derive it from pod identity plus toolset.
		ProviderID string

		// SinkAckGracePeriod configures the Pulse sink acknowledgement grace
		// period. When non-zero, Serve passes it to the sink.
		//
		// This value must be identical across all providers using the same sink
		// name for a given toolset stream.
		//
		// A grace period shorter than execution may cause harmless redelivery;
		// the registry dispatch claim prevents a second handler invocation.
		SinkAckGracePeriod time.Duration

		// Pong acknowledges health pings emitted by the registry gateway.
		// Providers must supply this to participate in health tracking and the
		// callback must return promptly after ctx cancellation.
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

		// EnsureInterval is how often Serve recreates the toolset stream
		// consumer group if Redis lost it. Pulse sinks silently retry on
		// missing groups, so without this repair a provider whose group was
		// lost would never receive pings or tool calls again. Registration
		// recovery needs no equivalent: the required Registration supervision
		// re-registers on every lease renewal.
		//
		// When 0, defaults to 30 seconds.
		EnsureInterval time.Duration

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
		// acknowledgement drain under one shared deadline after Serve stops
		// claiming new calls. Zero uses DefaultShutdownTimeout.
		ShutdownTimeout time.Duration

		// Logger is used for provider internal logging. When nil, defaults to a noop logger.
		Logger telemetry.Logger

		// Tracer is used for provider spans. When nil, defaults to a noop tracer.
		Tracer telemetry.Tracer
	}

	// registryOutputDeltaPublisher submits best-effort output fragments through
	// the registry's authoritative call-state boundary.
	//
	// Contract:
	//   - This is a UX-only signal: consumers may drop deltas without affecting
	//     correctness.
	//   - The canonical tool output remains the final ToolResultMessage published
	//     under the result event key.
	registryOutputDeltaPublisher struct {
		publish func(context.Context, string, string) error
	}
)

const (
	// DefaultShutdownTimeout bounds the provider's graceful consumption stop
	// and settlement phase before lease expiry becomes the durable fallback.
	DefaultShutdownTimeout = 30 * time.Second
	// SettlementAuthorityMargin keeps the draining provider lease valid beyond
	// the local shutdown deadline while the final registry response crosses the
	// transport boundary.
	SettlementAuthorityMargin = 5 * time.Second
	// MaxShutdownTimeout leaves room for SettlementAuthorityMargin inside the
	// registry's maximum provider lease duration.
	MaxShutdownTimeout = toolregistry.MaxProviderLeaseDuration - SettlementAuthorityMargin
	// MaxConcurrentToolCalls bounds worker allocation from provider config.
	MaxConcurrentToolCalls = 1024
	// MaxQueuedToolCalls bounds waiting work and acknowledgement allocation.
	MaxQueuedToolCalls = 65536
	// MaxOverloadPublishDuration bounds queue-saturation publication so the
	// intake loop resumes health pings promptly.
	MaxOverloadPublishDuration = 2 * time.Second
)

func (p *registryOutputDeltaPublisher) PublishToolOutputDelta(ctx context.Context, stream string, delta string) error {
	return p.publish(ctx, stream, delta)
}

// Serve prepares the toolset request stream, establishes the required registry
// registration, and then dispatches tool calls to handler. It publishes tool
// results through registry-owned call state and renews registration until shutdown.
func Serve(
	ctx context.Context,
	pulse pulseclients.Client,
	toolset string,
	handler Handler,
	registration Registration,
	opts Options,
) error {
	return serve(ctx, pulse, toolset, handler, registration, opts)
}

// serve runs one provider lifecycle.
func serve(
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
	if strings.ContainsRune(opts.ProviderID, '\x00') {
		return fmt.Errorf("provider id must not contain NUL")
	}
	registrationConfig, err := registration.normalized()
	if err != nil {
		return err
	}
	incarnationID := uuid.NewString()
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
	ensureInterval := opts.EnsureInterval
	if ensureInterval <= 0 {
		ensureInterval = 30 * time.Second
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
	if shutdownTimeout > MaxShutdownTimeout {
		return fmt.Errorf(
			"shutdown timeout must not exceed %s",
			MaxShutdownTimeout,
		)
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
	sink, err := stream.NewSink(ctx, toolregistry.ProviderConsumerGroup, sinkOpts...)
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
			fmt.Errorf(
				"create sink %q for toolset stream %q: %w",
				toolregistry.ProviderConsumerGroup,
				streamID,
				err,
			),
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
		"sink", toolregistry.ProviderConsumerGroup,
	)

	events := sink.Subscribe()
	var (
		errc          = make(chan error, 1)
		settleErrMu   sync.Mutex
		settlementErr error
	)

	ensureDone := make(chan struct{})
	go func() {
		defer close(ensureDone)
		runEnsureGroupLoop(
			cancelCtx,
			stream,
			toolregistry.ProviderConsumerGroup,
			toolset,
			opts.ProviderID,
			ensureInterval,
			logger,
		)
	}()

	type workItem struct {
		ev  *streaming.Event
		msg toolregistry.ToolCallMessage
	}

	work := make(chan workItem, maxQueued)
	acks := make(chan *streaming.Event, maxConcurrent+maxQueued)
	handlerCtx, cancelHandlers := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelHandlers()
	ackLifecycleCtx, cancelAcks := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelAcks()
	var workers sync.WaitGroup
	workers.Add(maxConcurrent)
	workerDone := make(chan struct{})
	ackDone := make(chan struct{})
	go func() {
		workers.Wait()
		close(workerDone)
	}()

	signalSettleFailure := func(err error) bool {
		if cancelCtx.Err() != nil && containsOnlyCancellation(err) {
			return false
		}
		settleErrMu.Lock()
		settlementErr = errors.Join(settlementErr, err)
		settleErrMu.Unlock()
		select {
		case errc <- err:
		default:
		}
		cancel()
		return true
	}

	registrationResult := make(chan error, 1)
	go func() {
		registrationErr := superviseRegistration(
			cancelCtx,
			toolset,
			opts.ProviderID,
			incarnationID,
			registrationState,
			registrationConfig,
			logger,
			waitRegistrationDelay,
		)
		registrationResult <- registrationErr
		cancel()
	}()

	go func() {
		defer close(ackDone)
		for {
			if ackLifecycleCtx.Err() != nil {
				return
			}
			var ev *streaming.Event
			select {
			case <-ackLifecycleCtx.Done():
				return
			case next, ok := <-acks:
				if !ok {
					return
				}
				ev = next
			}
			ackCtx, ackCancel := context.WithTimeout(ackLifecycleCtx, shutdownTimeout)
			err := sink.Ack(ackCtx, ev)
			ackCancel()
			if err != nil {
				if ackLifecycleCtx.Err() != nil && containsOnlyCancellation(err) {
					return
				}
				signalSettleFailure(fmt.Errorf("ack toolset event %s: %w", ev.ID, err))
			}
		}
	}()

	for i := 0; i < maxConcurrent; i++ {
		go func() {
			defer workers.Done()
			for {
				if cancelCtx.Err() != nil || handlerCtx.Err() != nil {
					return
				}
				var item workItem
				select {
				case <-cancelCtx.Done():
					return
				case <-handlerCtx.Done():
					return
				case next, ok := <-work:
					if !ok {
						return
					}
					item = next
				}
				if cancelCtx.Err() != nil || handlerCtx.Err() != nil {
					return
				}
				callCtx := toolregistry.ExtractTraceContext(
					handlerCtx,
					item.msg.TraceParent,
					item.msg.TraceState,
					item.msg.Baggage,
				)
				callCtx, cancelCall := context.WithDeadline(
					callCtx,
					time.UnixMilli(item.msg.ExecutionDeadlineUnixMilli),
				)
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
				disposition, claimErr := claimToolCall(
					callCtx,
					registrationConfig,
					shutdownTimeout,
					toolset,
					opts.ProviderID,
					incarnationID,
					admittedToken,
					item.msg,
					item.ev.ID,
				)
				if claimErr != nil {
					span.RecordError(claimErr)
					span.SetStatus(codes.Error, "claim tool call")
					span.End()
					signalSettleFailure(fmt.Errorf(
						"claim tool call %q: %w",
						item.msg.ToolUseID,
						claimErr,
					))
					cancelCall()
					continue
				}
				if disposition != ClaimExecute {
					span.AddEvent(
						"toolregistry.tool_call_not_dispatched",
						trace.WithAttributes(attribute.String("toolregistry.claim_disposition", string(disposition))),
					)
					span.End()
					select {
					case acks <- item.ev:
					case <-handlerCtx.Done():
					}
					cancelCall()
					continue
				}
				resultStreamID := toolregistry.ResultStreamID(item.msg.ToolUseID)
				callCtx = toolregistry.WithToolUseID(callCtx, item.msg.ToolUseID)
				callCtx = toolregistry.WithOutputDeltaPublisher(callCtx, &registryOutputDeltaPublisher{
					publish: func(ctx context.Context, stream, delta string) error {
						return registrationConfig.publishOutputDelta(
							ctx,
							toolset,
							opts.ProviderID,
							incarnationID,
							admittedToken,
							item.msg.RegistrationToken,
							item.msg.ToolUseID,
							item.ev.ID,
							stream,
							delta,
						)
					},
				})

				res, handlerErr := handler.HandleToolCall(callCtx, item.msg)
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
				res.RegistrationToken = item.msg.RegistrationToken
				res.ToolUseID = item.msg.ToolUseID
				if resultErr := toolregistry.ValidateToolResultMessage(res); resultErr != nil {
					span.RecordError(resultErr)
					span.SetStatus(codes.Error, "validate tool result")
					span.End()
					signalSettleFailure(fmt.Errorf(
						"tool call handler returned invalid result for %q: %w",
						item.msg.Tool,
						resultErr,
					))
					cancelCall()
					continue
				}

				publishCtx, publishCancel := context.WithTimeout(context.WithoutCancel(callCtx), shutdownTimeout)
				stopPublishCancel := context.AfterFunc(handlerCtx, publishCancel)
				addErr := registrationConfig.complete(
					publishCtx,
					toolset,
					opts.ProviderID,
					incarnationID,
					admittedToken,
					item.ev.ID,
					res,
				)
				stopPublishCancel()
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
					cancelCall()
					continue
				}
				span.AddEvent(
					"toolregistry.tool_result_published",
					"toolregistry.result_stream_id", resultStreamID,
				)
				span.End()

				select {
				case acks <- item.ev:
				case <-handlerCtx.Done():
				}
				cancelCall()
			}
		}()
	}

	ackImmediate := func(ev *streaming.Event, description string) error {
		ackCtx, ackCancel := context.WithTimeout(cancelCtx, shutdownTimeout)
		err := sink.Ack(ackCtx, ev)
		ackCancel()
		if err != nil {
			return fmt.Errorf("ack %s event %s: %w", description, ev.ID, err)
		}
		return nil
	}
	acceptEvent := func(ev *streaming.Event) error {
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
			return ackImmediate(ev, "malformed toolset")
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
			return ackImmediate(ev, "invalid toolset")
		}
		switch msg.Type {
		case toolregistry.MessageTypePing:
			if msg.RegistrationToken == admittedToken && msg.PingID != "" {
				pongCtx, pongCancel := context.WithTimeout(cancelCtx, pongTimeout)
				err := opts.Pong(pongCtx, opts.ProviderID, incarnationID, msg.PingID)
				pongCancel()
				if err != nil {
					logger.Error(
						ctx,
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
			return ackImmediate(ev, "ping toolset")
		case toolregistry.MessageTypeCall:
		default:
			return ackImmediate(ev, "unknown toolset")
		}
		if msg.ToolUseID == "" {
			return ackImmediate(ev, "tool call missing tool_use_id")
		}
		select {
		case work <- workItem{ev: ev, msg: msg}:
			return nil
		default:
			logger.Error(
				ctx,
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
			if err := reportOverload(
				cancelCtx,
				registrationConfig,
				shutdownTimeout,
				toolset,
				opts.ProviderID,
				incarnationID,
				admittedToken,
				msg,
				ev.ID,
			); err != nil {
				return err
			}
			return ackImmediate(ev, "overloaded tool call")
		}
	}

	finish := func(runErr, renewalErr error) error {
		cancel()
		settlementCtx, settlementCancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
		defer settlementCancel()

		type registrationStopResult struct {
			err               error
			changedToken      string
			changedTokenDrain <-chan error
		}
		registrationStop := make(chan registrationStopResult, 1)
		go func(stoppedErr error) {
			if stoppedErr == nil {
				stoppedErr = <-registrationResult
			}
			result := registrationStopResult{err: stoppedErr}
			var changedTokenErr *registrationTokenChangedError
			if errors.As(stoppedErr, &changedTokenErr) {
				result.changedToken = changedTokenErr.receivedToken
				changedTokenDrain := make(chan error, 1)
				result.changedTokenDrain = changedTokenDrain
				// Publish the known renewal error and token before draining it.
				// The caller can then preserve both even if drain uses the rest
				// of the shared settlement deadline.
				registrationStop <- result
				changedTokenDrain <- drainProvider(
					settlementCtx,
					toolset,
					opts.ProviderID,
					incarnationID,
					result.changedToken,
					shutdownTimeout+SettlementAuthorityMargin,
					registrationConfig,
					logger,
					waitRegistrationDelay,
				)
				return
			}
			registrationStop <- result
		}(renewalErr)

		leaseTokens := []string{admittedToken}
		drainErr := drainProvider(
			settlementCtx,
			toolset,
			opts.ProviderID,
			incarnationID,
			admittedToken,
			shutdownTimeout+SettlementAuthorityMargin,
			registrationConfig,
			logger,
			waitRegistrationDelay,
		)

		// The draining lease is now non-routable. Close intake before workers
		// start claims for remaining queue entries; claims that committed before
		// the drain may still finish under this exact lease.
		closeErr := closeSinkBounded(settlementCtx, sink)
		close(work)

		ensureErr := waitForDone(settlementCtx, ensureDone, "consumer-group ensure")
		workerErr := waitForDone(settlementCtx, workerDone, "tool workers")
		cancelHandlers()

		var ackDrainErr error
		if workerErr == nil {
			close(acks)
			ackDrainErr = waitForDone(settlementCtx, ackDone, "acknowledgements")
		}
		cancelAcks()

		var registrationWaitErr error
		var registrationStopped registrationStopResult
		registrationStoppedOK := false
		select {
		case registrationStopped = <-registrationStop:
			registrationStoppedOK = true
		default:
		}
		if !registrationStoppedOK {
			select {
			case registrationStopped = <-registrationStop:
				registrationStoppedOK = true
			case <-settlementCtx.Done():
				registrationWaitErr = fmt.Errorf("wait for registration renewal: %w", settlementCtx.Err())
			}
		}
		if registrationStoppedOK {
			renewalErr = registrationStopped.err
			if registrationStopped.changedToken != "" {
				leaseTokens = append(leaseTokens, registrationStopped.changedToken)
			}
			if registrationStopped.changedTokenDrain != nil {
				select {
				case changedTokenDrainErr := <-registrationStopped.changedTokenDrain:
					drainErr = errors.Join(drainErr, changedTokenDrainErr)
				case <-settlementCtx.Done():
					drainErr = errors.Join(
						drainErr,
						fmt.Errorf("drain changed registration token: %w", settlementCtx.Err()),
					)
				}
			}
		}
		runErr = joinProviderStopErrors(runErr, renewalErr)

		settleErrMu.Lock()
		recordedSettlementErr := settlementErr
		settleErrMu.Unlock()

		stopErr := errors.Join(
			drainErr,
			closeErr,
			registrationWaitErr,
			ensureErr,
			workerErr,
			ackDrainErr,
			recordedSettlementErr,
		)
		if stopErr != nil {
			return errors.Join(runErr, stopErr)
		}
		releaseErr := releaseProviderTokens(
			ctx,
			toolset,
			opts.ProviderID,
			incarnationID,
			leaseTokens,
			registrationConfig,
			logger,
			waitRegistrationDelay,
		)
		return errors.Join(runErr, releaseErr)
	}

	for {
		if runErr, renewalErr, stopped := pollProviderStop(cancelCtx, registrationResult, errc); stopped {
			return finish(runErr, renewalErr)
		}
		select {
		case <-cancelCtx.Done():
			runErr, renewalErr, _ := pollProviderStop(cancelCtx, registrationResult, errc)
			return finish(runErr, renewalErr)
		case renewalErr := <-registrationResult:
			return finish(nil, renewalErr)
		case err := <-errc:
			return finish(err, nil)
		case ev, ok := <-events:
			if runErr, renewalErr, stopped := pollProviderStop(cancelCtx, registrationResult, errc); stopped {
				return finish(runErr, renewalErr)
			}
			if !ok {
				return finish(errors.New("toolset stream subscription closed"), nil)
			}
			if err := acceptEvent(ev); err != nil {
				if signalSettleFailure(err) {
					return finish(err, nil)
				}
				runErr, renewalErr, _ := pollProviderStop(cancelCtx, registrationResult, errc)
				return finish(runErr, renewalErr)
			}
		}
	}
}

// pollProviderStop checks lifecycle signals without competing with intake.
// Callers run it before and after receiving an event so ready events cannot
// starve cancellation, registration failure, or worker failure.
func pollProviderStop(
	ctx context.Context,
	registrationResult, runResult <-chan error,
) (runErr, renewalErr error, stopped bool) {
	var renewalReady, runReady bool
	select {
	case renewalErr, renewalReady = <-registrationResult:
	default:
	}
	select {
	case runErr, runReady = <-runResult:
	default:
	}
	if renewalReady || runReady {
		return runErr, renewalErr, true
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr, nil, true
	}
	return nil, nil, false
}

// joinProviderStopErrors keeps a real renewal failure while preventing the
// registration goroutine's internal cancellation from changing the
// classification of an unrelated provider failure.
func joinProviderStopErrors(runErr, renewalErr error) error {
	if runErr != nil && containsOnlyCancellation(renewalErr) {
		return runErr
	}
	return errors.Join(runErr, renewalErr)
}

// containsOnlyCancellation reports whether every leaf in an error chain is a
// context cancellation or deadline. Unknown leaf errors are real failures and
// must remain visible.
func containsOnlyCancellation(err error) bool {
	if err == nil {
		return false
	}
	var joined interface{ Unwrap() []error }
	if errors.As(err, &joined) {
		children := joined.Unwrap()
		if len(children) == 0 {
			return false
		}
		for _, child := range children {
			if !containsOnlyCancellation(child) {
				return false
			}
		}
		return true
	}
	if cause := errors.Unwrap(err); cause != nil {
		return containsOnlyCancellation(cause)
	}
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// claimToolCall asks the registry for the one pre-dispatch transition. The
// handler may run only when the returned disposition is ClaimExecute.
func claimToolCall(
	ctx context.Context,
	registration registrationConfig,
	timeout time.Duration,
	toolset, providerID, incarnationID, providerRegistrationToken string,
	message toolregistry.ToolCallMessage,
	requestEventID string,
) (ClaimDisposition, error) {
	claimCtx, claimCancel := context.WithTimeout(ctx, timeout)
	defer claimCancel()
	disposition, err := registration.claim(claimCtx, ClaimRequest{
		Toolset:                   toolset,
		ProviderID:                providerID,
		ProviderIncarnationID:     incarnationID,
		ProviderRegistrationToken: providerRegistrationToken,
		CallRegistrationToken:     message.RegistrationToken,
		ToolUseID:                 message.ToolUseID,
		RequestEventID:            requestEventID,
		OperationID:               uuid.NewString(),
	})
	if err != nil {
		return "", err
	}
	switch disposition {
	case ClaimExecute, ClaimTerminal, ClaimOwned, ClaimExpired:
		return disposition, nil
	default:
		return "", fmt.Errorf("registry returned unsupported claim disposition %q", disposition)
	}
}

// reportOverload submits transient retry intent before the request is
// acknowledged; registry-owned call state suppresses it after terminal state.
func reportOverload(
	ctx context.Context,
	registration registrationConfig,
	timeout time.Duration,
	toolset, providerID, incarnationID, providerRegistrationToken string,
	message toolregistry.ToolCallMessage,
	requestEventID string,
) error {
	publishCtx, publishCancel := context.WithTimeout(ctx, min(timeout, MaxOverloadPublishDuration))
	defer publishCancel()
	if err := registration.reportOverload(
		publishCtx,
		toolset,
		providerID,
		incarnationID,
		providerRegistrationToken,
		message.RegistrationToken,
		message.ToolUseID,
		requestEventID,
	); err != nil {
		return fmt.Errorf("report overloaded tool call %q: %w", message.ToolUseID, err)
	}
	return nil
}

// closeSinkBounded stops consumer-group claiming and proves Close returned
// before the lifecycle shutdown deadline.
func closeSinkBounded(ctx context.Context, sink pulseclients.Sink) error {
	if err := sink.Close(ctx); err != nil {
		return fmt.Errorf("close toolset sink: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("close toolset sink: %w", err)
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

// runEnsureGroupLoop periodically recreates the toolset stream consumer group
// when Redis lost it, so the subscription created by Serve resumes receiving
// pings and tool calls after Redis state loss. Registration re-assertion is
// owned by the registration supervision loop; group repair is the only
// concern left here. Failures are logged and retried on the next interval and
// never terminate the provider.
func runEnsureGroupLoop(
	ctx context.Context,
	stream pulseclients.Stream,
	sinkName, toolset, providerID string,
	interval time.Duration,
	logger telemetry.Logger,
) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := stream.EnsureGroup(ctx, sinkName); err != nil {
				logger.Error(
					ctx,
					"ensure consumer group failed",
					"component", "tool-registry-provider",
					"toolset", toolset,
					"provider_id", providerID,
					"sink", sinkName,
					"err", err,
				)
			}
		}
	}
}
