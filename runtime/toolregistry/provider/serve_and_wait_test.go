// These tests keep real provider work pending beyond settlement. The two public
// entry points share the lifecycle result but have different return guarantees.
// Controlled dependencies are released and joined even when an assertion fails.

package provider

import (
	"context"
	"errors"
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
	goa "goa.design/goa/v3/pkg"
	"goa.design/pulse/streaming"
	streamopts "goa.design/pulse/streaming/options"
)

type (
	joinServeFunc func(context.Context, pulse.Client, string, Handler, Registration, Options) error

	// joinGate holds one dependency invocation until the test releases it.
	joinGate struct {
		entered chan context.Context
		release chan struct{}
		exited  chan struct{}
		holdOne sync.Once
		openOne sync.Once
	}

	joinHandlerFunc func(context.Context, toolregistry.ToolCallMessage) (toolregistry.ToolResultMessage, error)

	// joinTracer blocks after the worker's registry callback has returned.
	// A callback-only counter would miss this remaining worker work.
	joinTracer struct {
		telemetry.Tracer
		gate *joinGate
	}

	joinSpan struct {
		telemetry.Span
		ctx  context.Context
		gate *joinGate
	}
)

const (
	testPhaseClaim          = "claim"
	testPhaseComplete       = "complete"
	testPhaseAck            = "ack"
	testPhaseClose          = "close"
	testPhaseStartupRelease = "startup_release"
)

func TestServeAndWaitDelayedPhases(t *testing.T) {
	t.Parallel()
	for _, phase := range []string{
		testPhaseClaim, "handler", testPhaseComplete, testPhaseAck, "post_callback", "ensure", "renew", testPhaseClose,
	} {
		t.Run(phase, func(t *testing.T) {
			t.Parallel()
			for _, entry := range []struct {
				name  string
				joins bool
			}{
				{name: "bounded lifecycle"},
				{name: "ServeAndWait", joins: true},
			} {
				t.Run(entry.name, func(t *testing.T) {
					t.Parallel()
					ctx, cancel := context.WithCancel(t.Context())
					gate := newJoinGate()
					events := make(chan *streaming.Event, 1)
					subscribed := make(chan struct{})
					settling := make(chan context.Context, 1)
					var releases, closes atomic.Int64
					earlyFailure := errors.New("drain failed before joining")
					lateFailure := errors.New("close finished after settlement")
					registration := successfulRegistration()
					registration.Drain = func(ctx context.Context, _, _, _, _ string, _ time.Duration) error {
						if phase == testPhaseClose {
							<-ctx.Done()
							return earlyFailure
						}
						return nil
					}
					registration.Release = func(context.Context, string, string, string, string) error {
						releases.Add(1)
						return nil
					}
					var handler Handler = &recordingHandler{}
					opts := Options{
						ProviderID:             testProviderID,
						Pong:                   func(context.Context, string, string, string) error { return nil },
						ShutdownTimeout:        20 * time.Millisecond,
						EnsureInterval:         time.Hour,
						MaxConcurrentToolCalls: 2,
					}
					sink := mockpulse.NewSink(t)
					sink.SetSubscribe(func() <-chan *streaming.Event {
						close(subscribed)
						return events
					})
					sink.SetClose(func(ctx context.Context) error {
						// Close receives the shared settlement context. Drain's
						// attempt context is canceled when its callback returns.
						settling <- ctx
						closes.Add(1)
						if phase == testPhaseClose {
							gate.hold(ctx)
							return lateFailure
						}
						return nil
					})
					sink.SetAck(func(ctx context.Context, _ *streaming.Event) error {
						if phase == testPhaseAck {
							gate.hold(ctx)
							return ctx.Err()
						}
						return nil
					})
					stream := mockpulse.NewStream(t)
					stream.SetNewSink(func(context.Context, string, ...streamopts.Sink) (pulse.Sink, error) {
						return sink, nil
					})
					stream.SetEnsureGroup(func(ctx context.Context, _ string) error {
						if phase == "ensure" {
							gate.hold(ctx)
							return ctx.Err()
						}
						return nil
					})
					client := mockpulse.NewClient(t)
					client.SetStream(func(string, ...streamopts.Stream) (pulse.Stream, error) {
						return stream, nil
					})
					switch phase {
					case testPhaseClaim:
						registration.Claim = func(ctx context.Context, _ ClaimRequest) (ClaimDisposition, error) {
							gate.hold(ctx)
							return "", ctx.Err()
						}
					case "handler":
						handler = joinHandlerFunc(func(ctx context.Context, _ toolregistry.ToolCallMessage) (toolregistry.ToolResultMessage, error) {
							gate.hold(ctx)
							return toolregistry.ToolResultMessage{}, ErrResultUnavailable
						})
					case testPhaseComplete:
						registration.Complete = func(ctx context.Context, _, _, _, _, _ string, _ toolregistry.ToolResultMessage) (bool, error) {
							gate.hold(ctx)
							return false, ctx.Err()
						}
					case "post_callback":
						opts.Tracer = &joinTracer{Tracer: telemetry.NewNoopTracer(), gate: gate}
					case "ensure":
						opts.EnsureInterval = time.Millisecond
					case "renew":
						// Exercise the public real-timer entry point with a
						// valid short fixture lease, not a production default.
						registration.AttemptTimeout = 10 * time.Millisecond
						registration.RetryInitialInterval = time.Millisecond
						registration.RetryMaxInterval = time.Millisecond
						registration.ShutdownMargin = 5 * time.Millisecond
						registration.Register = func(context.Context, string, string, string) (RegistrationLease, error) {
							return RegistrationLease{RegistrationToken: testRegistrationTokenA, Duration: 200 * time.Millisecond}, nil
						}
						registration.Renew = func(ctx context.Context, _, _, _, _ string) (time.Duration, error) {
							gate.hold(ctx)
							return 0, ctx.Err()
						}
					}
					switch phase {
					case testPhaseClaim, "handler", testPhaseComplete, testPhaseAck, "post_callback":
						events <- testToolCallEvent(t, "join-"+phase)
					}
					var serve joinServeFunc = ServeAndWait
					var boundedRun providerRun
					if !entry.joins {
						// This is Serve's exact shared call. Retain its private
						// owner only so test cleanup also joins the late work.
						// shutdown_test.go separately exercises public Serve.
						serve = func(ctx context.Context, client pulse.Client, toolset string, handler Handler, registration Registration, opts Options) error {
							return boundedRun.serve(ctx, client, toolset, handler, registration, opts, waitRegistrationDelay)
						}
					}
					// Register this first: startJoinServe cleanup releases the
					// held dependency before this final private-owner join.
					t.Cleanup(boundedRun.goroutines.Wait)
					result := startJoinServe(t, cancel, func() error {
						return serve(ctx, client, "test.toolset", handler, registration, opts)
					}, gate)
					awaitJoinValue(t, subscribed)
					if phase == testPhaseClose {
						cancel()
						awaitJoinValue(t, gate.entered)
					} else {
						awaitJoinValue(t, gate.entered)
						cancel()
					}
					settlementCtx := awaitJoinValue(t, settling)
					awaitJoinValue(t, settlementCtx.Done())
					require.ErrorIs(t, settlementCtx.Err(), context.DeadlineExceeded)
					var err error
					if entry.joins {
						assertJoinHeld(t, result)
						gate.open()
						err = awaitJoinValue(t, result)
					} else {
						err = awaitJoinValue(t, result)
						select {
						case <-gate.exited:
							t.Fatal("Serve unexpectedly waited for the held dependency")
						default:
						}
						gate.open()
					}
					awaitJoinValue(t, gate.exited)
					boundedRun.goroutines.Wait()
					require.ErrorIs(t, err, context.Canceled)
					require.ErrorIs(t, err, context.DeadlineExceeded)
					if phase == testPhaseClose {
						require.ErrorIs(t, err, earlyFailure)
						require.NotErrorIs(t, err, lateFailure)
					}
					assert.Zero(t, releases.Load(), "joining cannot authorize late release")
					assert.Equal(t, int64(1), closes.Load(), "join must observe the original Close")
				})
			}
		})
	}
}

func TestServeAndWaitJoinsEveryWorker(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	first, second := newJoinGate(), newJoinGate()
	events := make(chan *streaming.Event, 2)
	events <- testToolCallEvent(t, "first")
	events <- testToolCallEvent(t, "second")
	settling := make(chan context.Context, 1)
	var releases atomic.Int64
	registration := successfulRegistration()
	registration.Release = func(context.Context, string, string, string, string) error {
		releases.Add(1)
		return nil
	}
	handler := joinHandlerFunc(func(ctx context.Context, msg toolregistry.ToolCallMessage) (toolregistry.ToolResultMessage, error) {
		switch msg.ToolUseID {
		case "first":
			first.hold(ctx)
		case "second":
			second.hold(ctx)
		}
		return toolregistry.ToolResultMessage{}, ErrResultUnavailable
	})
	sink := mockpulse.NewSink(t)
	sink.SetSubscribe(func() <-chan *streaming.Event { return events })
	sink.SetClose(func(ctx context.Context) error {
		settling <- ctx
		return nil
	})
	sink.SetAck(func(context.Context, *streaming.Event) error { return nil })
	stream := mockpulse.NewStream(t)
	stream.SetNewSink(func(context.Context, string, ...streamopts.Sink) (pulse.Sink, error) {
		return sink, nil
	})
	client := mockpulse.NewClient(t)
	client.SetStream(func(string, ...streamopts.Stream) (pulse.Stream, error) { return stream, nil })
	result := startJoinServe(t, cancel, func() error {
		return ServeAndWait(ctx, client, "test.toolset", handler, registration, Options{
			ProviderID:             testProviderID,
			Pong:                   func(context.Context, string, string, string) error { return nil },
			ShutdownTimeout:        20 * time.Millisecond,
			MaxConcurrentToolCalls: 2,
			EnsureInterval:         time.Hour,
		})
	}, first, second)
	awaitJoinValue(t, first.entered)
	awaitJoinValue(t, second.entered)
	cancel()
	settlementCtx := awaitJoinValue(t, settling)
	awaitJoinValue(t, settlementCtx.Done())
	require.ErrorIs(t, settlementCtx.Err(), context.DeadlineExceeded)
	first.open()
	awaitJoinValue(t, first.exited)
	assertJoinHeld(t, result)
	second.open()
	err := awaitJoinValue(t, result)
	awaitJoinValue(t, second.exited)
	require.ErrorIs(t, err, context.Canceled)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Zero(t, releases.Load())
}

func TestServeAndWaitCleanSettlement(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	events := make(chan *streaming.Event, 1)
	events <- testToolCallEvent(t, "clean-join")
	acked := make(chan struct{})
	var complete, closeCalls, releases atomic.Int64
	registration := successfulRegistration(func(result toolregistry.ToolResultMessage) error {
		assert.Equal(t, "clean-join", result.ToolUseID)
		complete.Add(1)
		return nil
	})
	registration.Release = func(context.Context, string, string, string, string) error {
		assert.Equal(t, int64(1), complete.Load())
		assert.Equal(t, int64(1), closeCalls.Load())
		select {
		case <-acked:
		default:
			t.Error("release preceded acknowledgement")
		}
		releases.Add(1)
		return nil
	}
	sink := mockpulse.NewSink(t)
	sink.SetSubscribe(func() <-chan *streaming.Event { return events })
	sink.SetClose(func(context.Context) error {
		closeCalls.Add(1)
		return nil
	})
	sink.SetAck(func(context.Context, *streaming.Event) error {
		close(acked)
		return nil
	})
	stream := mockpulse.NewStream(t)
	stream.SetNewSink(func(context.Context, string, ...streamopts.Sink) (pulse.Sink, error) { return sink, nil })
	client := mockpulse.NewClient(t)
	client.SetStream(func(string, ...streamopts.Stream) (pulse.Stream, error) { return stream, nil })
	result := startJoinServe(t, cancel, func() error {
		return ServeAndWait(ctx, client, "test.toolset", &recordingHandler{}, registration, Options{
			ProviderID:      testProviderID,
			Pong:            func(context.Context, string, string, string) error { return nil },
			ShutdownTimeout: time.Second,
			EnsureInterval:  time.Hour,
		})
	})
	awaitJoinValue(t, acked)
	cancel()
	err := awaitJoinValue(t, result)
	require.ErrorIs(t, err, context.Canceled)
	assert.True(t, containsOnlyCancellation(err), "unexpected settlement error: %v", err)
	assert.Equal(t, int64(1), releases.Load())
}

func TestServeAndWaitStartupFailures(t *testing.T) {
	t.Parallel()
	for _, phase := range []string{"validation", "stream", "registration", "sink", testPhaseStartupRelease} {
		t.Run(phase, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(t.Context())
			failure := errors.New("startup failed")
			releaseFailure := errors.New("startup release failed")
			var sinks, releases atomic.Int64
			registration := successfulRegistration()
			registration.Register = func(context.Context, string, string, string) (RegistrationLease, error) {
				if phase == "registration" {
					return RegistrationLease{}, goa.NewServiceError(failure, "admission_retired", false, false, false)
				}
				return RegistrationLease{RegistrationToken: testRegistrationTokenA, Duration: time.Hour}, nil
			}
			registration.ReleaseTimeout = 10 * time.Millisecond
			registration.Release = func(ctx context.Context, _, _, _, _ string) error {
				releases.Add(1)
				if phase == testPhaseStartupRelease {
					<-ctx.Done()
					return releaseFailure
				}
				return nil
			}
			stream := mockpulse.NewStream(t)
			stream.SetNewSink(func(context.Context, string, ...streamopts.Sink) (pulse.Sink, error) {
				sinks.Add(1)
				return nil, failure
			})
			client := mockpulse.NewClient(t)
			client.SetStream(func(string, ...streamopts.Stream) (pulse.Stream, error) {
				if phase == "stream" {
					return nil, failure
				}
				return stream, nil
			})
			opts := Options{
				ProviderID: testProviderID,
				Pong:       func(context.Context, string, string, string) error { return nil },
			}
			if phase == "validation" {
				opts.ProviderID = ""
			}
			result := startJoinServe(t, cancel, func() error {
				return ServeAndWait(ctx, client, "test.toolset", &recordingHandler{}, registration, opts)
			})
			err := awaitJoinValue(t, result)
			if phase == "validation" {
				require.ErrorContains(t, err, "provider id is required")
			} else {
				require.ErrorIs(t, err, failure)
			}
			switch phase {
			case "sink", testPhaseStartupRelease:
				assert.Equal(t, int64(1), sinks.Load())
				assert.Equal(t, int64(1), releases.Load())
			default:
				assert.Zero(t, sinks.Load())
				assert.Zero(t, releases.Load())
			}
			if phase == testPhaseStartupRelease {
				require.ErrorIs(t, err, releaseFailure)
				require.ErrorIs(t, err, context.DeadlineExceeded)
			}
		})
	}
}

func (f joinHandlerFunc) HandleToolCall(ctx context.Context, msg toolregistry.ToolCallMessage) (toolregistry.ToolResultMessage, error) {
	return f(ctx, msg)
}

func (tracer *joinTracer) Start(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, telemetry.Span) {
	ctx, span := tracer.Tracer.Start(ctx, name, opts...)
	return ctx, &joinSpan{Span: span, ctx: ctx, gate: tracer.gate}
}

func (span *joinSpan) End(opts ...trace.SpanEndOption) {
	span.gate.hold(span.ctx)
	span.Span.End(opts...)
}

func newJoinGate() *joinGate {
	return &joinGate{
		entered: make(chan context.Context, 1),
		release: make(chan struct{}),
		exited:  make(chan struct{}),
	}
}

func (gate *joinGate) hold(ctx context.Context) {
	gate.holdOne.Do(func() {
		gate.entered <- ctx
		<-gate.release
		close(gate.exited)
	})
}

func (gate *joinGate) open() {
	gate.openOne.Do(func() { close(gate.release) })
}

// startJoinServe owns the test call and guarantees that failed assertions still
// release held dependencies and observe the public invocation finishing.
func startJoinServe(t *testing.T, cancel context.CancelFunc, call func() error, gates ...*joinGate) <-chan error {
	t.Helper()
	result := make(chan error, 1)
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		result <- call()
	}()
	t.Cleanup(func() {
		cancel()
		for _, gate := range gates {
			gate.open()
		}
		select {
		case <-exited:
		case <-time.After(time.Second):
			t.Error("provider test invocation did not exit")
		}
	})
	return result
}

func awaitJoinValue[T any](t *testing.T, values <-chan T) T {
	t.Helper()
	select {
	case value := <-values:
		return value
	case <-time.After(time.Second):
		t.Fatal("provider lifecycle barrier did not complete")
		var zero T
		return zero
	}
}

// assertJoinHeld runs only after the settlement context has ended. This bounded
// harness observation is not a production shutdown-duration assertion.
func assertJoinHeld(t *testing.T, result <-chan error) {
	t.Helper()
	select {
	case err := <-result:
		t.Fatalf("joined provider returned with work still held: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
}
