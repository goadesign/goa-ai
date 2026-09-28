package provider

// These tests run the provider worker and acknowledgement loops. An explicit
// missing result ends one handler without inventing a terminal or stopping its
// peers; ordinary results, errors, and cleanup failures keep their contracts.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"
	pulse "goa.design/goa-ai/features/stream/pulse/clients/pulse"
	mockpulse "goa.design/goa-ai/features/stream/pulse/clients/pulse/mocks"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/toolregistry"
	"goa.design/pulse/streaming"
	streamopts "goa.design/pulse/streaming/options"
)

type (
	unavailableTestHandler func(context.Context, toolregistry.ToolCallMessage) (toolregistry.ToolResultMessage, error)

	// unavailableServeRun owns a test lifecycle and its bounded observation
	// channels. err is read only after done closes.
	unavailableServeRun struct {
		events chan *streaming.Event
		acks   chan string
		done   chan struct{}
		cancel context.CancelFunc
		tracer *unavailableTracer
		err    error
	}

	unavailableTracer struct {
		telemetry.Tracer
		ended    atomic.Int64
		recorded chan error
	}

	unavailableSpan struct {
		telemetry.Span
		tracer *unavailableTracer
	}
)

func TestServeResultUnavailablePreservesParallelCall(t *testing.T) {
	t.Parallel()

	active := make(chan context.Context, 1)
	unavailable := make(chan context.Context, 1)
	finishActive := make(chan struct{})
	var finishOnce sync.Once
	results := make(chan toolregistry.ToolResultMessage, 2)
	output := make(chan string, 1)
	drained := make(chan struct{})
	var releases atomic.Int64
	registration := successfulRegistration(func(result toolregistry.ToolResultMessage) error {
		results <- result
		return nil
	})
	registration.Drain = func(context.Context, string, string, string, string, time.Duration) error {
		close(drained)
		return nil
	}
	registration.Release = func(context.Context, string, string, string, string) error {
		releases.Add(1)
		return nil
	}
	registration.PublishOutputDelta = func(
		_ context.Context,
		_, _, _, _, _, callID, _, _, _ string,
	) error {
		output <- callID
		return nil
	}
	handler := unavailableTestHandler(func(ctx context.Context, msg toolregistry.ToolCallMessage) (toolregistry.ToolResultMessage, error) {
		switch msg.ToolUseID {
		case "active":
			active <- ctx
			select {
			case <-finishActive:
			case <-ctx.Done():
				return toolregistry.ToolResultMessage{}, ctx.Err()
			}
			publisher, ok := toolregistry.OutputDeltaPublisherFromContext(ctx)
			if !ok {
				return toolregistry.ToolResultMessage{}, errors.New("output publisher missing")
			}
			if err := publisher.PublishToolOutputDelta(ctx, "stdout", "finished"); err != nil {
				return toolregistry.ToolResultMessage{}, err
			}
		case "unavailable":
			unavailable <- ctx
			return toolregistry.ToolResultMessage{}, errors.Join(ErrResultUnavailable, context.Canceled)
		}
		return toolregistry.NewToolResultMessage(msg.RegistrationToken, msg.ToolUseID, json.RawMessage(`{"ok":true}`)), nil
	})
	run := startUnavailableServe(t, handler, registration, nil)
	t.Cleanup(func() { finishOnce.Do(func() { close(finishActive) }) })

	run.events <- testToolCallEvent(t, "active")
	activeCtx := awaitUnavailable(t, active)
	run.events <- testToolCallEvent(t, "unavailable")
	unavailableCtx := awaitUnavailable(t, unavailable)
	assert.Equal(t, "unavailable", awaitUnavailable(t, run.acks))
	require.ErrorIs(t, unavailableCtx.Err(), context.Canceled)
	require.NoError(t, activeCtx.Err())
	assert.Empty(t, results)
	require.ErrorIs(t, awaitUnavailable(t, run.tracer.recorded), ErrResultUnavailable)
	assert.Equal(t, int64(1), run.tracer.ended.Load())
	select {
	case <-drained:
		t.Fatal("unavailable result started provider drain")
	default:
	}
	assert.Zero(t, releases.Load())

	// The second worker is free although the first handler is still active.
	run.events <- testToolCallEvent(t, "next")
	assert.Equal(t, "next", awaitUnavailable(t, run.acks))
	assert.Equal(t, "next", awaitUnavailable(t, results).ToolUseID)
	require.NoError(t, activeCtx.Err())

	run.cancel()
	awaitUnavailable(t, drained)
	require.NoError(t, activeCtx.Err(), "drain preserves accepted handler context")
	assert.Zero(t, releases.Load(), "release must wait for the other handler")
	finishOnce.Do(func() { close(finishActive) })
	assert.Equal(t, "active", awaitUnavailable(t, output))
	assert.Equal(t, "active", awaitUnavailable(t, results).ToolUseID)
	assert.Equal(t, "active", awaitUnavailable(t, run.acks))
	awaitUnavailable(t, run.done)
	require.ErrorIs(t, run.err, context.Canceled)
	assert.Equal(t, int64(1), releases.Load())
	assert.Equal(t, int64(3), run.tracer.ended.Load())
	assert.Empty(t, results, "unavailable handler must never submit a terminal")
}

func TestServeResultUnavailableReturnContract(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name         string
		result       toolregistry.ToolResultMessage
		err          error
		complete     bool
		failure      bool
		pastDeadline bool
	}{
		{name: "direct marker", err: ErrResultUnavailable},
		{name: "wrapped marker", err: fmt.Errorf("remote result: %w", ErrResultUnavailable)},
		{name: "joined cause", err: errors.Join(ErrResultUnavailable, context.DeadlineExceeded)},
		{
			name: "marker ignores result before identity and validation",
			result: toolregistry.ToolResultMessage{
				RegistrationToken: "foreign", ToolUseID: "foreign", Error: &toolregistry.ToolError{},
			},
			err: ErrResultUnavailable,
		},
		{name: "same text is ordinary error", err: errors.New(ErrResultUnavailable.Error()), complete: true, failure: true},
		{name: "local error", err: errors.New("local operation failed"), complete: true, failure: true},
		{name: "local canceled error", err: context.Canceled, complete: true, failure: true},
		{name: "local deadline error", err: context.DeadlineExceeded, complete: true, failure: true},
		{name: "nil semantic result succeeds", complete: true},
		{
			name:     "terminal survives ended call context",
			result:   toolregistry.ToolResultMessage{Result: json.RawMessage(`{"ok":true}`)},
			complete: true, pastDeadline: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			results := make(chan toolregistry.ToolResultMessage, 1)
			contexts := make(chan context.Context, 1)
			registration := successfulRegistration()
			registration.Complete = func(
				ctx context.Context,
				_, _, _, _, _ string,
				result toolregistry.ToolResultMessage,
			) (bool, error) {
				if err := ctx.Err(); err != nil {
					return false, err
				}
				results <- result
				return true, nil
			}
			handler := unavailableTestHandler(func(ctx context.Context, _ toolregistry.ToolCallMessage) (toolregistry.ToolResultMessage, error) {
				contexts <- ctx
				return test.result, test.err
			})
			run := startUnavailableServe(t, handler, registration, nil)
			event := testToolCallEvent(t, "return-contract")
			if test.pastDeadline {
				event = testToolCallEventAt(t, "return-contract",
					time.Now().Add(toolregistry.ResultStreamTransportBudget-time.Second))
			}
			run.events <- event
			callCtx := awaitUnavailable(t, contexts)
			if test.pastDeadline {
				require.ErrorIs(t, callCtx.Err(), context.DeadlineExceeded)
			}
			assert.Equal(t, event.ID, awaitUnavailable(t, run.acks))
			if test.complete {
				result := awaitUnavailable(t, results)
				assert.Equal(t, "return-contract", result.ToolUseID)
				assert.Equal(t, testRegistrationTokenA, result.RegistrationToken)
				if test.failure {
					require.NotNil(t, result.Error)
					assert.Equal(t, "execution_failed", result.Error.Code)
				} else {
					assert.Nil(t, result.Error)
					assert.Equal(t, test.result.Result, result.Result)
				}
			} else {
				assert.Empty(t, results)
				require.ErrorIs(t, callCtx.Err(), context.Canceled)
			}
			assert.Equal(t, int64(1), run.tracer.ended.Load())
			run.cancel()
			awaitUnavailable(t, run.done)
			require.ErrorIs(t, run.err, context.Canceled)
		})
	}
}

func TestServeResultUnavailableDoesNotMaskCallbackFailure(t *testing.T) {
	t.Parallel()

	for _, operation := range []string{testPhaseClaim, testPhaseComplete, testPhaseAck} {
		t.Run(operation, func(t *testing.T) {
			var releases, handled, completed atomic.Int64
			registration := successfulRegistration()
			registration.Release = func(context.Context, string, string, string, string) error {
				releases.Add(1)
				return nil
			}
			registration.Complete = func(context.Context, string, string, string, string, string, toolregistry.ToolResultMessage) (bool, error) {
				completed.Add(1)
				if operation == testPhaseComplete {
					return false, ErrResultUnavailable
				}
				return true, nil
			}
			var ackErr error
			if operation == testPhaseClaim {
				registration.Claim = func(context.Context, ClaimRequest) (ClaimDisposition, error) {
					return "", ErrResultUnavailable
				}
			}
			if operation == testPhaseAck {
				ackErr = ErrResultUnavailable
			}
			handler := unavailableTestHandler(func(_ context.Context, msg toolregistry.ToolCallMessage) (toolregistry.ToolResultMessage, error) {
				handled.Add(1)
				if operation == testPhaseAck {
					return toolregistry.ToolResultMessage{}, ErrResultUnavailable
				}
				return toolregistry.NewToolResultMessage(msg.RegistrationToken, msg.ToolUseID, nil), nil
			})
			run := startUnavailableServe(t, handler, registration, ackErr)
			run.events <- testToolCallEvent(t, "callback-failure")
			awaitUnavailable(t, run.done)
			require.ErrorIs(t, run.err, ErrResultUnavailable)
			assert.Zero(t, releases.Load())
			assert.Empty(t, run.acks)
			if operation == testPhaseClaim {
				assert.Zero(t, handled.Load())
			} else {
				assert.Equal(t, int64(1), handled.Load())
			}
			if operation == testPhaseComplete {
				assert.Equal(t, int64(1), completed.Load())
			} else {
				assert.Zero(t, completed.Load())
			}
		})
	}
}

func (h unavailableTestHandler) HandleToolCall(ctx context.Context, msg toolregistry.ToolCallMessage) (toolregistry.ToolResultMessage, error) {
	return h(ctx, msg)
}

func (t *unavailableTracer) Start(ctx context.Context, _ string, _ ...trace.SpanStartOption) (context.Context, telemetry.Span) {
	return ctx, &unavailableSpan{Span: t.Span(ctx), tracer: t}
}

func (s *unavailableSpan) End(...trace.SpanEndOption) {
	s.tracer.ended.Add(1)
}

func (s *unavailableSpan) RecordError(err error, _ ...trace.EventOption) {
	s.tracer.recorded <- err
}

// startUnavailableServe uses the existing lifecycle with two workers. Cleanup
// cancels and joins it before mock expectations are checked.
func startUnavailableServe(t *testing.T, handler Handler, registration Registration, ackErr error) *unavailableServeRun {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	run := &unavailableServeRun{
		events: make(chan *streaming.Event, 2),
		acks:   make(chan string, 4),
		done:   make(chan struct{}),
		cancel: cancel,
		tracer: &unavailableTracer{Tracer: telemetry.NewNoopTracer(), recorded: make(chan error, 4)},
	}
	sink := mockpulse.NewSink(t)
	sink.SetSubscribe(func() <-chan *streaming.Event { return run.events })
	sink.SetClose(func(context.Context) error { return nil })
	sink.SetAck(func(_ context.Context, event *streaming.Event) error {
		if ackErr != nil {
			return ackErr
		}
		run.acks <- event.ID
		return nil
	})
	stream := mockpulse.NewStream(t)
	stream.SetNewSink(func(context.Context, string, ...streamopts.Sink) (pulse.Sink, error) {
		return sink, nil
	})
	client := mockpulse.NewClient(t)
	client.SetStream(func(string, ...streamopts.Stream) (pulse.Stream, error) { return stream, nil })
	t.Cleanup(func() {
		cancel()
		select {
		case <-run.done:
		case <-time.After(3 * time.Second):
			t.Error("provider did not finish during test cleanup")
		}
	})
	go func() {
		run.err = Serve(ctx, client, "test.toolset", handler, registration, Options{
			ProviderID:             testProviderID,
			Pong:                   func(context.Context, string, string, string) error { return nil },
			MaxConcurrentToolCalls: 2,
			MaxQueuedToolCalls:     2,
			ShutdownTimeout:        time.Second,
			Tracer:                 run.tracer,
		})
		close(run.done)
	}()
	return run
}

// awaitUnavailable bounds test observations so a failed lifecycle assertion
// cannot leave the test waiting indefinitely.
func awaitUnavailable[T any](t *testing.T, values <-chan T) T {
	t.Helper()
	select {
	case value := <-values:
		return value
	case <-time.After(3 * time.Second):
		t.Fatal("provider observation timed out")
		var zero T
		return zero
	}
}
