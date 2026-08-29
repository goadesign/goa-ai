package provider

// These tests pin provider lifecycle ownership: no consumer-group sink exists
// before admission, renewals remain bounded by the authoritative lease, and
// cancellation waits for context-compliant callbacks.

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
	pulse "goa.design/goa-ai/features/stream/pulse/clients/pulse"
	mockpulse "goa.design/goa-ai/features/stream/pulse/clients/pulse/mocks"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/toolregistry"
	goa "goa.design/goa/v3/pkg"
	"goa.design/pulse/streaming"
	streamopts "goa.design/pulse/streaming/options"
)

// successfulRegistration supplies a long-lived lease to provider tests whose
// behavior does not depend on reconciliation.
func successfulRegistration(
	complete ...func(toolregistry.ToolResultMessage) error,
) Registration {
	completeResult := func(toolregistry.ToolResultMessage) error { return nil }
	if len(complete) > 0 {
		completeResult = complete[0]
	}
	return Registration{
		AdmissionRevision: testAdmissionRevision,
		Register: func(context.Context, string, string, string, string) (RegistrationLease, error) {
			return RegistrationLease{RegistrationToken: testRegistrationTokenA, Duration: time.Hour}, nil
		},
		Drain: func(context.Context, string, string, string, string, time.Duration) error {
			return nil
		},
		Release: func(context.Context, string, string, string, string) error {
			return nil
		},
		Complete: func(
			_ context.Context,
			_, _, _, _, _ string,
			result toolregistry.ToolResultMessage,
		) error {
			return completeResult(result)
		},
		PublishOutputDelta: publishOutputDeltaSuccess,
		ReportOverload:     reportOverloadSuccess,
		Claim:              claimExecute,
	}
}

func publishOutputDeltaSuccess(context.Context, string, string, string, string, string, string, string, string, string) error {
	return nil
}

func reportOverloadSuccess(context.Context, string, string, string, string, string, string, string) error {
	return nil
}

// claimExecute grants dispatch to lifecycle tests that do not exercise claim
// settlement outcomes.
func claimExecute(context.Context, ClaimRequest) (ClaimDisposition, error) {
	return ClaimExecute, nil
}

// identityRegistrationJitter keeps scheduling tests on exact intervals.
func identityRegistrationJitter(base, maximum time.Duration) time.Duration {
	if maximum > 0 {
		return min(base, maximum)
	}
	return base
}

func TestServeRequiresRegistration(t *testing.T) {
	t.Parallel()

	err := Serve(context.Background(), mockpulse.NewClient(t), "test.toolset", &blockingHandler{}, Registration{}, Options{
		ProviderID: testProviderID,
		Pong: func(context.Context, string, string, string) error {
			return nil
		},
	})
	require.ErrorContains(t, err, "registration callback is required")
}

func TestServeRequiresValidAdmissionRevision(t *testing.T) {
	t.Parallel()

	for _, revision := range []string{"", "contains whitespace", ".starts-with-punctuation"} {
		registration := successfulRegistration()
		registration.AdmissionRevision = revision
		err := Serve(
			context.Background(),
			mockpulse.NewClient(t),
			"test.toolset",
			&blockingHandler{},
			registration,
			Options{
				ProviderID: testProviderID,
				Pong: func(context.Context, string, string, string) error {
					return nil
				},
			},
		)
		require.ErrorContains(t, err, "admission revision must match")
	}
}

func TestRegisterProviderRejectsNoncanonicalRegistrationToken(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0)
	_, err := registerProvider(
		context.Background(),
		"test.toolset",
		testProviderID,
		testProviderIncarnationID,
		registrationConfig{
			admissionRevision: testAdmissionRevision,
			register: func(context.Context, string, string, string, string) (RegistrationLease, error) {
				return RegistrationLease{RegistrationToken: "ABC123", Duration: time.Minute}, nil
			},
			attemptTimeout: time.Second,
			now:            func() time.Time { return now },
		},
		time.Time{},
	)

	require.Error(t, err)
	assert.ErrorContains(t, err, "invalid registration token")
}

func TestServeOpensStreamRegistersThenCreatesSink(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const toolset = "test.toolset"
	events := make(chan *streaming.Event, 1)
	streamOpened := make(chan struct{})
	registrationStarted := make(chan struct{})
	allowRegistration := make(chan struct{})
	sinkCreated := make(chan struct{})
	released := make(chan struct{})
	var sinkClosed atomic.Bool

	sink := mockpulse.NewSink(t)
	sink.SetSubscribe(func() <-chan *streaming.Event {
		return events
	})
	sink.SetAck(func(context.Context, *streaming.Event) error {
		return nil
	})
	sink.SetClose(func(context.Context) error {
		sinkClosed.Store(true)
		return nil
	})

	stream := mockpulse.NewStream(t)
	stream.SetNewSink(func(context.Context, string, ...streamopts.Sink) (pulse.Sink, error) {
		close(sinkCreated)
		return sink, nil
	})
	resultStream := mockpulse.NewStream(t)
	resultStream.SetAdd(func(context.Context, string, []byte) (string, error) {
		return "2-0", nil
	})
	client := mockpulse.NewClient(t)
	client.SetStream(func(name string, _ ...streamopts.Stream) (pulse.Stream, error) {
		if name == toolregistry.ToolsetStreamID(toolset) {
			close(streamOpened)
			return stream, nil
		}
		assert.Equal(t, toolregistry.ResultStreamID("tooluse_1"), name)
		return resultStream, nil
	})

	handler := &blockingHandler{
		started: make(chan struct{}),
		unblock: make(chan struct{}),
	}
	errc := make(chan error, 1)
	go func() {
		errc <- Serve(ctx, client, toolset, handler, Registration{
			AdmissionRevision: testAdmissionRevision,
			Register: func(
				_ context.Context,
				gotToolset, providerID, incarnationID, admissionRevision string,
			) (RegistrationLease, error) {
				assert.Equal(t, toolset, gotToolset)
				assert.Equal(t, testProviderID, providerID)
				assert.NotEmpty(t, incarnationID)
				assert.Equal(t, testAdmissionRevision, admissionRevision)
				close(registrationStarted)
				<-allowRegistration
				return RegistrationLease{RegistrationToken: testRegistrationTokenA, Duration: time.Hour}, nil
			},
			Drain: func(context.Context, string, string, string, string, time.Duration) error {
				return nil
			},
			Release: func(_ context.Context, gotToolset, providerID, incarnationID, token string) error {
				assert.True(t, sinkClosed.Load())
				assert.Equal(t, toolset, gotToolset)
				assert.Equal(t, testProviderID, providerID)
				assert.NotEmpty(t, incarnationID)
				assert.Equal(t, testRegistrationTokenA, token)
				close(released)
				return nil
			},
			Complete: func(context.Context, string, string, string, string, string, toolregistry.ToolResultMessage) error {
				return nil
			},
			PublishOutputDelta: publishOutputDeltaSuccess,
			ReportOverload:     reportOverloadSuccess,
			Claim:              claimExecute,
		}, Options{
			ProviderID: testProviderID,
			Pong: func(context.Context, string, string, string) error {
				return nil
			},
		})
	}()

	<-streamOpened
	<-registrationStarted
	select {
	case <-sinkCreated:
		t.Fatal("provider created an unadmitted consumer-group sink")
	default:
	}

	close(allowRegistration)
	<-sinkCreated

	call := toolregistry.NewToolCallMessage(
		testRegistrationTokenA,
		"tooluse_1",
		time.Now().Add(toolregistry.MaxToolCallWait),
		time.Now().Add(toolregistry.DefaultResultStreamTTL),
		tools.Ident("test.toolset.tool"),
		json.RawMessage(`{"x":1}`),
		&toolregistry.ToolCallMeta{RunID: "run-1", SessionID: "session-1", ToolCallID: "call-1"},
	)
	payload, err := json.Marshal(call)
	require.NoError(t, err)
	events <- &streaming.Event{ID: "1-0", EventName: "call", Payload: payload}
	select {
	case <-handler.started:
	case <-time.After(time.Second):
		t.Fatal("provider did not dispatch after admission")
	}

	close(handler.unblock)
	cancel()
	require.ErrorIs(t, <-errc, context.Canceled)
	<-released
}

func TestServeReleasesAfterSinkCreationFailure(t *testing.T) {
	t.Parallel()

	stream := mockpulse.NewStream(t)
	stream.SetNewSink(func(context.Context, string, ...streamopts.Sink) (pulse.Sink, error) {
		return nil, errors.New("sink unavailable")
	})
	client := mockpulse.NewClient(t)
	client.SetStream(func(string, ...streamopts.Stream) (pulse.Stream, error) {
		return stream, nil
	})
	var released atomic.Int64
	registration := successfulRegistration()
	registration.Release = func(_ context.Context, toolset, providerID, _ string, token string) error {
		assert.Equal(t, "test.toolset", toolset)
		assert.Equal(t, testProviderID, providerID)
		assert.Equal(t, testRegistrationTokenA, token)
		released.Add(1)
		return nil
	}

	err := Serve(context.Background(), client, "test.toolset", &blockingHandler{}, registration, Options{
		ProviderID: testProviderID,
		Pong:       func(context.Context, string, string, string) error { return nil },
	})

	require.ErrorContains(t, err, "sink unavailable")
	assert.Equal(t, int64(1), released.Load())
}

func TestReleaseProviderRetriesExactLease(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int64
	registration := registrationConfig{
		release: func(_ context.Context, toolset, providerID, _ string, token string) error {
			assert.Equal(t, "test.toolset", toolset)
			assert.Equal(t, testProviderID, providerID)
			assert.Equal(t, testRegistrationTokenA, token)
			if attempts.Add(1) < 3 {
				return errors.New("registry unavailable")
			}
			return nil
		},
		retryInitialInterval: time.Millisecond,
		retryMaxInterval:     time.Millisecond,
		attemptTimeout:       time.Second,
		releaseTimeout:       time.Second,
		jitter:               identityRegistrationJitter,
	}

	err := releaseProvider(
		context.Background(),
		"test.toolset",
		testProviderID,
		testProviderIncarnationID,
		testRegistrationTokenA,
		registration,
		telemetry.NewNoopLogger(),
		func(context.Context, time.Duration) error { return nil },
	)

	require.NoError(t, err)
	assert.Equal(t, int64(3), attempts.Load())
}

func TestReleaseProviderTokensRunsUnderOneSharedDeadline(t *testing.T) {
	t.Parallel()

	type releaseStart struct {
		token    string
		deadline time.Time
	}
	started := make(chan releaseStart, 2)
	release := make(chan struct{})
	var releaseOnce sync.Once
	allowRelease := func() {
		releaseOnce.Do(func() {
			close(release)
		})
	}
	t.Cleanup(allowRelease)

	registration := registrationConfig{
		release: func(ctx context.Context, _, _, _, token string) error {
			deadline, ok := ctx.Deadline()
			assert.True(t, ok)
			started <- releaseStart{token: token, deadline: deadline}
			<-release
			return nil
		},
		attemptTimeout: time.Minute,
		releaseTimeout: time.Second,
	}
	errc := make(chan error, 1)
	go func() {
		errc <- releaseProviderTokens(
			context.Background(),
			"test.toolset",
			testProviderID,
			testProviderIncarnationID,
			[]string{testRegistrationTokenA, testRegistrationTokenB},
			registration,
			telemetry.NewNoopLogger(),
			func(context.Context, time.Duration) error { return nil },
		)
	}()

	require.Eventually(t, func() bool {
		return len(started) == 2
	}, time.Second, time.Millisecond)
	first := <-started
	second := <-started
	assert.ElementsMatch(t, []string{testRegistrationTokenA, testRegistrationTokenB}, []string{first.token, second.token})
	assert.Equal(t, first.deadline, second.deadline)

	allowRelease()
	require.NoError(t, <-errc)
}

func TestReleaseProviderTokensReturnsAtSharedDeadline(t *testing.T) {
	t.Parallel()

	releaseErr := errors.New("registry unavailable")
	var succeeded, failed atomic.Int64
	registration := registrationConfig{
		release: func(_ context.Context, _, _, _, token string) error {
			if token == testRegistrationTokenA {
				succeeded.Add(1)
				return nil
			}
			failed.Add(1)
			return releaseErr
		},
		retryInitialInterval: time.Minute,
		retryMaxInterval:     time.Minute,
		attemptTimeout:       time.Second,
		releaseTimeout:       50 * time.Millisecond,
		jitter:               identityRegistrationJitter,
	}
	startedAt := time.Now()
	err := releaseProviderTokens(
		context.Background(),
		"test.toolset",
		testProviderID,
		testProviderIncarnationID,
		[]string{testRegistrationTokenA, testRegistrationTokenB},
		registration,
		telemetry.NewNoopLogger(),
		func(ctx context.Context, _ time.Duration) error {
			<-ctx.Done()
			return ctx.Err()
		},
	)

	require.ErrorIs(t, err, releaseErr)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Equal(t, int64(1), succeeded.Load())
	assert.Equal(t, int64(1), failed.Load())
	assert.Less(t, time.Since(startedAt), time.Second)
}

func TestServeSetupFailurePrecedesRegistration(t *testing.T) {
	t.Parallel()

	var registrations atomic.Int64
	client := mockpulse.NewClient(t)
	client.SetStream(func(string, ...streamopts.Stream) (pulse.Stream, error) {
		return nil, errors.New("pulse unavailable")
	})
	err := Serve(context.Background(), client, "test.toolset", &blockingHandler{}, Registration{
		AdmissionRevision: testAdmissionRevision,
		Register: func(context.Context, string, string, string, string) (RegistrationLease, error) {
			registrations.Add(1)
			return RegistrationLease{RegistrationToken: testRegistrationTokenA, Duration: time.Hour}, nil
		},
		Drain:   func(context.Context, string, string, string, string, time.Duration) error { return nil },
		Release: func(context.Context, string, string, string, string) error { return nil },
		Complete: func(context.Context, string, string, string, string, string, toolregistry.ToolResultMessage) error {
			return nil
		},
		PublishOutputDelta: publishOutputDeltaSuccess,
		ReportOverload:     reportOverloadSuccess,
		Claim:              claimExecute,
	}, Options{
		ProviderID: testProviderID,
		Pong: func(context.Context, string, string, string) error {
			return nil
		},
	})
	require.ErrorContains(t, err, "pulse unavailable")
	assert.Zero(t, registrations.Load())
}

func TestServeClosesConsumptionBeforeLeaseExpiry(t *testing.T) {
	t.Parallel()

	events := make(chan *streaming.Event)
	closed := make(chan time.Time, 1)
	sink := mockpulse.NewSink(t)
	sink.SetSubscribe(func() <-chan *streaming.Event {
		return events
	})
	sink.SetClose(func(context.Context) error {
		closed <- time.Now()
		return nil
	})
	stream := mockpulse.NewStream(t)
	stream.SetNewSink(func(context.Context, string, ...streamopts.Sink) (pulse.Sink, error) {
		return sink, nil
	})
	client := mockpulse.NewClient(t)
	client.SetStream(func(string, ...streamopts.Stream) (pulse.Stream, error) {
		return stream, nil
	})
	leaseDuration := 150 * time.Millisecond
	leaseExpiresAt := time.Now().Add(leaseDuration)
	var registrations atomic.Int64

	err := Serve(context.Background(), client, "test.toolset", &blockingHandler{}, Registration{
		AdmissionRevision: testAdmissionRevision,
		Register: func(context.Context, string, string, string, string) (RegistrationLease, error) {
			if registrations.Add(1) > 1 {
				return RegistrationLease{}, errors.New("registry unavailable")
			}
			return RegistrationLease{RegistrationToken: testRegistrationTokenA, Duration: leaseDuration}, nil
		},
		Drain:   func(context.Context, string, string, string, string, time.Duration) error { return nil },
		Release: func(context.Context, string, string, string, string) error { return nil },
		Complete: func(context.Context, string, string, string, string, string, toolregistry.ToolResultMessage) error {
			return nil
		},
		PublishOutputDelta:   publishOutputDeltaSuccess,
		ReportOverload:       reportOverloadSuccess,
		Claim:                claimExecute,
		RetryInitialInterval: 5 * time.Millisecond,
		RetryMaxInterval:     10 * time.Millisecond,
		AttemptTimeout:       10 * time.Millisecond,
		ShutdownMargin:       50 * time.Millisecond,
	}, Options{
		ProviderID: testProviderID,
		Pong: func(context.Context, string, string, string) error {
			return nil
		},
	})

	require.ErrorIs(t, err, ErrRegistrationLeaseExpired)
	closedAt := <-closed
	assert.True(t, closedAt.Before(leaseExpiresAt))
}

func TestRegisterUntilSuccessRetriesTransientFailure(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0)
	var attempts atomic.Int64
	registration := registrationConfig{
		admissionRevision: testAdmissionRevision,
		register: func(
			_ context.Context,
			toolset, providerID, incarnationID, admissionRevision string,
		) (RegistrationLease, error) {
			assert.Equal(t, "test.toolset", toolset)
			assert.Equal(t, testProviderID, providerID)
			assert.Equal(t, testProviderIncarnationID, incarnationID)
			assert.Equal(t, testAdmissionRevision, admissionRevision)
			if attempts.Add(1) == 1 {
				return RegistrationLease{}, errors.New("registry unavailable")
			}
			return RegistrationLease{RegistrationToken: testRegistrationTokenA, Duration: time.Minute}, nil
		},
		retryInitialInterval: time.Second,
		retryMaxInterval:     4 * time.Second,
		attemptTimeout:       time.Second,
		shutdownMargin:       time.Second,
		now:                  func() time.Time { return now },
		jitter:               identityRegistrationJitter,
	}
	var waited time.Duration
	state, err := registerUntilSuccess(
		context.Background(),
		"test.toolset",
		testProviderID,
		testProviderIncarnationID,
		registration,
		telemetry.NewNoopLogger(),
		func(_ context.Context, delay time.Duration) error {
			waited = delay
			return nil
		},
	)
	require.NoError(t, err)
	assert.Equal(t, testRegistrationTokenA, state.lease.RegistrationToken)
	assert.Equal(t, now.Add(time.Minute), state.deadline)
	assert.Equal(t, int64(2), attempts.Load())
	assert.Equal(t, time.Second, waited)
}

func TestRegistrationRetryBackoffAndJitterBounds(t *testing.T) {
	t.Parallel()

	registration := registrationConfig{
		retryInitialInterval: time.Second,
		retryMaxInterval:     4 * time.Second,
		jitter:               identityRegistrationJitter,
	}
	assert.Equal(t, time.Second, registration.retryDelay(1))
	assert.Equal(t, 2*time.Second, registration.retryDelay(2))
	assert.Equal(t, 4*time.Second, registration.retryDelay(3))
	assert.Equal(t, 4*time.Second, registration.retryDelay(8))

	for range 100 {
		delay := jitterRegistrationDelay(10*time.Second, 11*time.Second)
		assert.GreaterOrEqual(t, delay, 8*time.Second)
		assert.LessOrEqual(t, delay, 11*time.Second)
		renewal := jitterRegistrationDelay(10*time.Second, 0)
		assert.GreaterOrEqual(t, renewal, 8*time.Second)
		assert.LessOrEqual(t, renewal, 12*time.Second)
	}
}

func TestRegistrationRenewalDelayDerivesFromThirtyOneSecondLease(t *testing.T) {
	t.Parallel()

	registration := registrationConfig{jitter: identityRegistrationJitter}

	assert.Equal(t, 31*time.Second/3, registration.renewalDelay(31*time.Second))
}

func TestRegisterUntilSuccessReturnsPermanentAdmissionError(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name      string
		errorName string
		message   string
	}{
		{name: "retired", errorName: "admission_retired", message: "retired admission"},
		{name: "validation", errorName: "validation_error", message: "invalid registration"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var attempts atomic.Int64
			registration := registrationConfig{
				admissionRevision: testAdmissionRevision,
				register: func(context.Context, string, string, string, string) (RegistrationLease, error) {
					attempts.Add(1)
					return RegistrationLease{}, goa.NewServiceError(
						errors.New(tc.message),
						tc.errorName,
						false,
						false,
						false,
					)
				},
				retryInitialInterval: time.Second,
				retryMaxInterval:     4 * time.Second,
				attemptTimeout:       time.Second,
				shutdownMargin:       time.Second,
				now:                  time.Now,
				jitter:               identityRegistrationJitter,
			}

			_, err := registerUntilSuccess(
				context.Background(),
				"test.toolset",
				testProviderID,
				testProviderIncarnationID,
				registration,
				telemetry.NewNoopLogger(),
				func(context.Context, time.Duration) error {
					t.Fatal("permanent admission error must not retry")
					return nil
				},
			)
			require.ErrorContains(t, err, tc.message)
			assert.Equal(t, int64(1), attempts.Load())
		})
	}
}

func TestRegisterUntilSuccessPreservesFailureThatRacesCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	registrationFailure := errors.New("registration rejected")
	registration := registrationConfig{
		admissionRevision: testAdmissionRevision,
		register: func(context.Context, string, string, string, string) (RegistrationLease, error) {
			cancel()
			return RegistrationLease{}, registrationFailure
		},
		attemptTimeout: time.Second,
		now:            time.Now,
	}

	_, err := registerUntilSuccess(
		ctx,
		"test.toolset",
		testProviderID,
		testProviderIncarnationID,
		registration,
		telemetry.NewNoopLogger(),
		func(context.Context, time.Duration) error {
			t.Fatal("canceled registration must not retry")
			return nil
		},
	)
	require.ErrorIs(t, err, registrationFailure)
	require.ErrorIs(t, err, context.Canceled)
}

func TestRegistrationCallbackReceivesAttemptDeadline(t *testing.T) {
	t.Parallel()

	registration := registrationConfig{
		admissionRevision: testAdmissionRevision,
		register: func(ctx context.Context, _, _, _, _ string) (RegistrationLease, error) {
			_, hasDeadline := ctx.Deadline()
			assert.True(t, hasDeadline)
			<-ctx.Done()
			return RegistrationLease{}, ctx.Err()
		},
		attemptTimeout: 10 * time.Millisecond,
		now:            time.Now,
	}
	_, err := registerProvider(context.Background(), "test.toolset", testProviderID, testProviderIncarnationID, registration, time.Time{})
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestRegisterProviderDerivesDeadlineFromAttemptStart(t *testing.T) {
	t.Parallel()

	attemptStarted := time.Unix(1_700_000_000, 0)
	now := attemptStarted
	registration := registrationConfig{
		admissionRevision: testAdmissionRevision,
		register: func(context.Context, string, string, string, string) (RegistrationLease, error) {
			now = now.Add(4 * time.Second)
			return RegistrationLease{RegistrationToken: testRegistrationTokenA, Duration: 10 * time.Second}, nil
		},
		attemptTimeout: time.Second,
		now:            func() time.Time { return now },
	}

	state, err := registerProvider(context.Background(), "test.toolset", testProviderID, testProviderIncarnationID, registration, time.Time{})
	require.NoError(t, err)
	assert.Equal(t, attemptStarted.Add(10*time.Second), state.deadline)
	assert.Equal(t, 6*time.Second, state.deadline.Sub(now))
}

func TestRegisterProviderDeadlineIgnoresWallClockEpochSkew(t *testing.T) {
	t.Parallel()

	for _, attemptStarted := range []time.Time{
		time.Unix(0, 0),
		time.Date(2200, time.January, 1, 0, 0, 0, 0, time.UTC),
	} {
		now := attemptStarted
		registration := registrationConfig{
			admissionRevision: testAdmissionRevision,
			register: func(context.Context, string, string, string, string) (RegistrationLease, error) {
				now = now.Add(250 * time.Millisecond)
				return RegistrationLease{RegistrationToken: testRegistrationTokenA, Duration: 31 * time.Second}, nil
			},
			attemptTimeout: time.Second,
			now:            func() time.Time { return now },
		}

		state, err := registerProvider(
			context.Background(),
			"test.toolset",
			testProviderID,
			testProviderIncarnationID,
			registration,
			time.Time{},
		)
		require.NoError(t, err)
		assert.Equal(t, 31*time.Second, state.deadline.Sub(attemptStarted))
		assert.Equal(t, 30*time.Second+750*time.Millisecond, state.deadline.Sub(now))
	}
}

func TestRegistrationSupervisorRejectsChangedRenewalToken(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0)
	registration := registrationConfig{
		admissionRevision: testAdmissionRevision,
		register: func(context.Context, string, string, string, string) (RegistrationLease, error) {
			return RegistrationLease{RegistrationToken: testRegistrationTokenB, Duration: time.Minute}, nil
		},
		retryInitialInterval: time.Second,
		retryMaxInterval:     time.Second,
		attemptTimeout:       time.Second,
		shutdownMargin:       time.Second,
		now:                  func() time.Time { return now },
		jitter:               identityRegistrationJitter,
	}

	err := superviseRegistration(
		context.Background(),
		"test.toolset",
		testProviderID,
		testProviderIncarnationID,
		registrationState{
			lease:    RegistrationLease{RegistrationToken: testRegistrationTokenA, Duration: time.Minute},
			deadline: now.Add(time.Minute),
		},
		registration,
		telemetry.NewNoopLogger(),
		func(_ context.Context, delay time.Duration) error {
			now = now.Add(delay)
			return nil
		},
	)

	require.ErrorIs(t, err, ErrRegistrationTokenChanged)
	assert.True(t, now.Before(time.Unix(1_700_000_000, 0).Add(59*time.Second)))
}

func TestRegisterProviderRejectsExcessiveLeaseDuration(t *testing.T) {
	t.Parallel()

	registration := registrationConfig{
		admissionRevision: testAdmissionRevision,
		register: func(context.Context, string, string, string, string) (RegistrationLease, error) {
			return RegistrationLease{
				RegistrationToken: testRegistrationTokenA,
				Duration:          toolregistry.MaxProviderLeaseDuration + time.Nanosecond,
			}, nil
		},
		attemptTimeout: time.Second,
		now:            time.Now,
	}

	_, err := registerProvider(
		context.Background(),
		"test.toolset",
		testProviderID,
		testProviderIncarnationID,
		registration,
		time.Time{},
	)
	require.ErrorContains(t, err, "lease duration exceeds")
}

func TestRegisterProviderRejectsLeaseInsideShutdownRetryBudget(t *testing.T) {
	t.Parallel()

	registration := registrationConfig{
		admissionRevision: testAdmissionRevision,
		register: func(context.Context, string, string, string, string) (RegistrationLease, error) {
			return RegistrationLease{
				RegistrationToken: testRegistrationTokenA,
				Duration:          31 * time.Second,
			}, nil
		},
		retryMaxInterval: 30 * time.Second,
		attemptTimeout:   time.Second,
		shutdownMargin:   time.Second,
		now:              time.Now,
	}

	_, err := registerProvider(
		context.Background(),
		"test.toolset",
		testProviderID,
		testProviderIncarnationID,
		registration,
		time.Time{},
	)
	require.ErrorContains(t, err, "must exceed shutdown and retry budget")
}

func TestServeChangedRenewalTokenReleasesBothExactLeases(t *testing.T) {
	t.Parallel()

	events := make(chan *streaming.Event)
	sink := mockpulse.NewSink(t)
	sink.SetSubscribe(func() <-chan *streaming.Event { return events })
	sink.SetAck(func(context.Context, *streaming.Event) error { return nil })
	sink.SetClose(func(context.Context) error { return nil })
	stream := mockpulse.NewStream(t)
	stream.SetNewSink(func(context.Context, string, ...streamopts.Sink) (pulse.Sink, error) {
		return sink, nil
	})
	client := mockpulse.NewClient(t)
	client.SetStream(func(string, ...streamopts.Stream) (pulse.Stream, error) {
		return stream, nil
	})

	var registrations atomic.Int64
	var (
		leaseMu  sync.Mutex
		drained  []string
		released []string
	)
	registration := Registration{
		AdmissionRevision: testAdmissionRevision,
		Register: func(context.Context, string, string, string, string) (RegistrationLease, error) {
			token := testRegistrationTokenA
			if registrations.Add(1) > 1 {
				token = testRegistrationTokenB
			}
			return RegistrationLease{RegistrationToken: token, Duration: 60 * time.Millisecond}, nil
		},
		Drain: func(_ context.Context, _, _, _ string, token string, _ time.Duration) error {
			leaseMu.Lock()
			drained = append(drained, token)
			leaseMu.Unlock()
			return nil
		},
		Release: func(_ context.Context, _, _, _ string, token string) error {
			leaseMu.Lock()
			released = append(released, token)
			leaseMu.Unlock()
			return nil
		},
		Complete: func(context.Context, string, string, string, string, string, toolregistry.ToolResultMessage) error {
			return nil
		},
		PublishOutputDelta:   publishOutputDeltaSuccess,
		ReportOverload:       reportOverloadSuccess,
		Claim:                claimExecute,
		RetryInitialInterval: time.Millisecond,
		RetryMaxInterval:     time.Millisecond,
		AttemptTimeout:       20 * time.Millisecond,
		ShutdownMargin:       time.Millisecond,
		ReleaseTimeout:       time.Second,
	}

	err := Serve(
		context.Background(),
		client,
		"test.toolset",
		&recordingHandler{},
		registration,
		Options{
			ProviderID:      testProviderID,
			Pong:            func(context.Context, string, string, string) error { return nil },
			ShutdownTimeout: time.Second,
		},
	)
	require.ErrorIs(t, err, ErrRegistrationTokenChanged)
	leaseMu.Lock()
	assert.ElementsMatch(t, []string{testRegistrationTokenA, testRegistrationTokenB}, drained)
	assert.ElementsMatch(t, []string{testRegistrationTokenA, testRegistrationTokenB}, released)
	leaseMu.Unlock()
}

func TestServePreservesChangedTokenWhenItsDrainReachesDeadline(t *testing.T) {
	t.Parallel()

	events := make(chan *streaming.Event)
	sink := mockpulse.NewSink(t)
	sink.SetSubscribe(func() <-chan *streaming.Event { return events })
	sink.SetAck(func(context.Context, *streaming.Event) error { return nil })
	sink.SetClose(func(context.Context) error { return nil })
	stream := mockpulse.NewStream(t)
	stream.SetNewSink(func(context.Context, string, ...streamopts.Sink) (pulse.Sink, error) {
		return sink, nil
	})
	client := mockpulse.NewClient(t)
	client.SetStream(func(string, ...streamopts.Stream) (pulse.Stream, error) {
		return stream, nil
	})

	var registrations atomic.Int64
	var released atomic.Int64
	registration := Registration{
		AdmissionRevision: testAdmissionRevision,
		Register: func(context.Context, string, string, string, string) (RegistrationLease, error) {
			token := testRegistrationTokenA
			if registrations.Add(1) > 1 {
				token = testRegistrationTokenB
			}
			return RegistrationLease{RegistrationToken: token, Duration: 60 * time.Millisecond}, nil
		},
		Drain: func(ctx context.Context, _, _, _ string, token string, _ time.Duration) error {
			if token == testRegistrationTokenB {
				<-ctx.Done()
				return ctx.Err()
			}
			return nil
		},
		Release: func(context.Context, string, string, string, string) error {
			released.Add(1)
			return nil
		},
		Complete: func(context.Context, string, string, string, string, string, toolregistry.ToolResultMessage) error {
			return nil
		},
		PublishOutputDelta:   publishOutputDeltaSuccess,
		ReportOverload:       reportOverloadSuccess,
		Claim:                claimExecute,
		RetryInitialInterval: time.Millisecond,
		RetryMaxInterval:     time.Millisecond,
		AttemptTimeout:       20 * time.Millisecond,
		ShutdownMargin:       time.Millisecond,
		ReleaseTimeout:       time.Second,
	}

	err := Serve(
		context.Background(),
		client,
		"test.toolset",
		&recordingHandler{},
		registration,
		Options{
			ProviderID:      testProviderID,
			Pong:            func(context.Context, string, string, string) error { return nil },
			ShutdownTimeout: 20 * time.Millisecond,
		},
	)
	require.ErrorIs(t, err, ErrRegistrationTokenChanged)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Zero(t, released.Load())
}

func TestServeRetainsChangedRenewalTokenDuringCancellation(t *testing.T) {
	t.Parallel()

	events := make(chan *streaming.Event)
	sink := mockpulse.NewSink(t)
	sink.SetSubscribe(func() <-chan *streaming.Event { return events })
	sink.SetAck(func(context.Context, *streaming.Event) error { return nil })
	sink.SetClose(func(context.Context) error { return nil })
	stream := mockpulse.NewStream(t)
	stream.SetNewSink(func(context.Context, string, ...streamopts.Sink) (pulse.Sink, error) {
		return sink, nil
	})
	client := mockpulse.NewClient(t)
	client.SetStream(func(string, ...streamopts.Stream) (pulse.Stream, error) {
		return stream, nil
	})

	renewalStarted := make(chan struct{})
	releaseRenewal := make(chan struct{})
	var releaseRenewalOnce sync.Once
	allowRenewal := func() {
		releaseRenewalOnce.Do(func() {
			close(releaseRenewal)
		})
	}
	t.Cleanup(allowRenewal)
	var registrations atomic.Int64
	var (
		leaseMu  sync.Mutex
		drained  []string
		released []string
	)
	registration := Registration{
		AdmissionRevision: testAdmissionRevision,
		Register: func(context.Context, string, string, string, string) (RegistrationLease, error) {
			if registrations.Add(1) == 1 {
				return RegistrationLease{RegistrationToken: testRegistrationTokenA, Duration: 60 * time.Millisecond}, nil
			}
			close(renewalStarted)
			<-releaseRenewal
			return RegistrationLease{RegistrationToken: testRegistrationTokenB, Duration: 60 * time.Millisecond}, nil
		},
		Drain: func(_ context.Context, _, _, _ string, token string, _ time.Duration) error {
			leaseMu.Lock()
			drained = append(drained, token)
			leaseMu.Unlock()
			return nil
		},
		Release: func(_ context.Context, _, _, _ string, token string) error {
			leaseMu.Lock()
			released = append(released, token)
			leaseMu.Unlock()
			return nil
		},
		Complete: func(context.Context, string, string, string, string, string, toolregistry.ToolResultMessage) error {
			return nil
		},
		PublishOutputDelta:   publishOutputDeltaSuccess,
		ReportOverload:       reportOverloadSuccess,
		Claim:                claimExecute,
		RetryInitialInterval: time.Millisecond,
		RetryMaxInterval:     time.Millisecond,
		AttemptTimeout:       20 * time.Millisecond,
		ShutdownMargin:       time.Millisecond,
		ReleaseTimeout:       time.Second,
	}

	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		errc <- Serve(
			ctx,
			client,
			"test.toolset",
			&recordingHandler{},
			registration,
			Options{
				ProviderID:      testProviderID,
				Pong:            func(context.Context, string, string, string) error { return nil },
				ShutdownTimeout: time.Second,
			},
		)
	}()

	select {
	case <-renewalStarted:
	case <-time.After(time.Second):
		t.Fatal("registration renewal did not start")
	}
	cancel()
	allowRenewal()

	err := <-errc
	require.ErrorIs(t, err, ErrRegistrationTokenChanged)
	leaseMu.Lock()
	assert.ElementsMatch(t, []string{testRegistrationTokenA, testRegistrationTokenB}, drained)
	assert.ElementsMatch(t, []string{testRegistrationTokenA, testRegistrationTokenB}, released)
	leaseMu.Unlock()
}

func TestServePreservesRenewalFailureDuringCancellation(t *testing.T) {
	t.Parallel()

	events := make(chan *streaming.Event)
	sink := mockpulse.NewSink(t)
	sink.SetSubscribe(func() <-chan *streaming.Event { return events })
	sink.SetAck(func(context.Context, *streaming.Event) error { return nil })
	sink.SetClose(func(context.Context) error { return nil })
	stream := mockpulse.NewStream(t)
	stream.SetNewSink(func(context.Context, string, ...streamopts.Sink) (pulse.Sink, error) {
		return sink, nil
	})
	client := mockpulse.NewClient(t)
	client.SetStream(func(string, ...streamopts.Stream) (pulse.Stream, error) {
		return stream, nil
	})

	renewalStarted := make(chan struct{})
	releaseRenewal := make(chan struct{})
	var registrations atomic.Int64
	renewalErr := errors.New("renewal response failed")
	registration := Registration{
		AdmissionRevision: testAdmissionRevision,
		Register: func(context.Context, string, string, string, string) (RegistrationLease, error) {
			if registrations.Add(1) == 1 {
				return RegistrationLease{RegistrationToken: testRegistrationTokenA, Duration: 60 * time.Millisecond}, nil
			}
			close(renewalStarted)
			<-releaseRenewal
			return RegistrationLease{}, renewalErr
		},
		Drain:   func(context.Context, string, string, string, string, time.Duration) error { return nil },
		Release: func(context.Context, string, string, string, string) error { return nil },
		Complete: func(context.Context, string, string, string, string, string, toolregistry.ToolResultMessage) error {
			return nil
		},
		PublishOutputDelta:   publishOutputDeltaSuccess,
		ReportOverload:       reportOverloadSuccess,
		Claim:                claimExecute,
		AttemptTimeout:       20 * time.Millisecond,
		ShutdownMargin:       time.Millisecond,
		ReleaseTimeout:       time.Second,
		RetryInitialInterval: time.Millisecond,
		RetryMaxInterval:     time.Millisecond,
	}

	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		errc <- Serve(
			ctx,
			client,
			"test.toolset",
			&recordingHandler{},
			registration,
			Options{
				ProviderID:      testProviderID,
				Pong:            func(context.Context, string, string, string) error { return nil },
				ShutdownTimeout: time.Second,
			},
		)
	}()

	select {
	case <-renewalStarted:
	case <-time.After(time.Second):
		t.Fatal("registration renewal did not start")
	}
	cancel()
	close(releaseRenewal)

	err := <-errc
	require.ErrorIs(t, err, renewalErr)
	require.ErrorIs(t, err, context.Canceled)
}

func TestJoinProviderStopErrorsClassifiesGeneratedClientCancellation(t *testing.T) {
	t.Parallel()

	renewalFailure := errors.New("renewal failed")
	t.Run("generated transport cancellation is clean", func(t *testing.T) {
		renewalErr := fmt.Errorf("rpc error: code = Canceled: %w", context.Canceled)
		err := joinProviderStopErrors(context.Canceled, renewalErr)
		require.Equal(t, context.Canceled, err)
	})
	t.Run("concurrent renewal failure remains terminal", func(t *testing.T) {
		err := joinProviderStopErrors(
			context.Canceled,
			errors.Join(context.Canceled, renewalFailure),
		)
		require.ErrorIs(t, err, context.Canceled)
		require.ErrorIs(t, err, renewalFailure)
	})
}

func TestServeReturnsAtSettlementDeadlineWhenRenewalIgnoresCancellation(t *testing.T) {
	t.Parallel()

	events := make(chan *streaming.Event)
	sink := mockpulse.NewSink(t)
	sink.SetSubscribe(func() <-chan *streaming.Event { return events })
	sink.SetAck(func(context.Context, *streaming.Event) error { return nil })
	sink.SetClose(func(context.Context) error { return nil })
	stream := mockpulse.NewStream(t)
	stream.SetNewSink(func(context.Context, string, ...streamopts.Sink) (pulse.Sink, error) {
		return sink, nil
	})
	client := mockpulse.NewClient(t)
	client.SetStream(func(string, ...streamopts.Stream) (pulse.Stream, error) {
		return stream, nil
	})

	renewalStarted := make(chan struct{})
	releaseRenewal := make(chan struct{})
	var releaseRenewalOnce sync.Once
	allowRenewal := func() {
		releaseRenewalOnce.Do(func() {
			close(releaseRenewal)
		})
	}
	t.Cleanup(allowRenewal)
	var registrations atomic.Int64
	var releases atomic.Int64
	registration := Registration{
		AdmissionRevision: testAdmissionRevision,
		Register: func(context.Context, string, string, string, string) (RegistrationLease, error) {
			if registrations.Add(1) == 1 {
				return RegistrationLease{RegistrationToken: testRegistrationTokenA, Duration: 60 * time.Millisecond}, nil
			}
			close(renewalStarted)
			<-releaseRenewal
			return RegistrationLease{}, context.Canceled
		},
		Drain: func(context.Context, string, string, string, string, time.Duration) error { return nil },
		Release: func(context.Context, string, string, string, string) error {
			releases.Add(1)
			return nil
		},
		Complete: func(context.Context, string, string, string, string, string, toolregistry.ToolResultMessage) error {
			return nil
		},
		PublishOutputDelta:   publishOutputDeltaSuccess,
		ReportOverload:       reportOverloadSuccess,
		Claim:                claimExecute,
		AttemptTimeout:       20 * time.Millisecond,
		ShutdownMargin:       time.Millisecond,
		ReleaseTimeout:       20 * time.Millisecond,
		RetryInitialInterval: time.Millisecond,
		RetryMaxInterval:     time.Millisecond,
	}

	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		errc <- Serve(
			ctx,
			client,
			"test.toolset",
			&recordingHandler{},
			registration,
			Options{
				ProviderID:      testProviderID,
				Pong:            func(context.Context, string, string, string) error { return nil },
				ShutdownTimeout: 20 * time.Millisecond,
			},
		)
	}()

	select {
	case <-renewalStarted:
	case <-time.After(time.Second):
		t.Fatal("registration renewal did not start")
	}
	startedAt := time.Now()
	cancel()

	select {
	case err := <-errc:
		require.ErrorIs(t, err, context.DeadlineExceeded)
	case <-time.After(time.Second):
		t.Fatal("Serve exceeded its settlement and release deadlines")
	}
	assert.Equal(t, int64(0), releases.Load())
	assert.Less(t, time.Since(startedAt), 500*time.Millisecond)
}

func TestRegistrationSupervisorRejectsRenewalCompletingAfterOldCutoff(t *testing.T) {
	t.Parallel()

	started := time.Unix(1_700_000_000, 0)
	now := started
	registration := registrationConfig{
		admissionRevision: testAdmissionRevision,
		register: func(context.Context, string, string, string, string) (RegistrationLease, error) {
			now = started.Add(9*time.Second + time.Millisecond)
			return RegistrationLease{RegistrationToken: testRegistrationTokenB, Duration: time.Minute}, nil
		},
		retryInitialInterval: time.Second,
		retryMaxInterval:     time.Second,
		attemptTimeout:       time.Minute,
		shutdownMargin:       time.Second,
		now:                  func() time.Time { return now },
		jitter:               identityRegistrationJitter,
	}
	err := superviseRegistration(
		context.Background(),
		"test.toolset",
		testProviderID,
		testProviderIncarnationID,
		registrationState{
			lease:    RegistrationLease{RegistrationToken: testRegistrationTokenA, Duration: 10 * time.Second},
			deadline: started.Add(10 * time.Second),
		},
		registration,
		telemetry.NewNoopLogger(),
		func(_ context.Context, delay time.Duration) error {
			now = now.Add(delay)
			return nil
		},
	)

	require.ErrorIs(t, err, ErrRegistrationLeaseExpired)
}

func TestRegistrationSupervisorRetriesOnlyInsideLease(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0)
	leaseExpiresAt := now.Add(10 * time.Second)
	var attempts atomic.Int64
	registration := registrationConfig{
		admissionRevision: testAdmissionRevision,
		register: func(context.Context, string, string, string, string) (RegistrationLease, error) {
			attempts.Add(1)
			return RegistrationLease{}, errors.New("registry unavailable")
		},
		retryInitialInterval: 3 * time.Second,
		retryMaxInterval:     3 * time.Second,
		attemptTimeout:       time.Second,
		shutdownMargin:       time.Second,
		now:                  func() time.Time { return now },
		jitter:               identityRegistrationJitter,
	}
	var waits []time.Duration
	err := superviseRegistration(
		context.Background(),
		"test.toolset",
		testProviderID,
		testProviderIncarnationID,
		registrationState{
			lease:    RegistrationLease{RegistrationToken: testRegistrationTokenA, Duration: 10 * time.Second},
			deadline: leaseExpiresAt,
		},
		registration,
		telemetry.NewNoopLogger(),
		func(_ context.Context, delay time.Duration) error {
			waits = append(waits, delay)
			now = now.Add(delay)
			if len(waits) == 3 {
				return context.Canceled
			}
			return nil
		},
	)
	require.ErrorIs(t, err, context.Canceled)
	require.Len(t, waits, 3)
	assert.Equal(t, 10*time.Second/3, waits[0])
	assert.Equal(t, (9*time.Second-10*time.Second/3)/2, waits[1])
	assert.Equal(t, (9*time.Second-waits[0]-waits[1])/2, waits[2])
	assert.Equal(t, int64(2), attempts.Load())
	assert.True(t, now.Before(leaseExpiresAt.Add(-time.Second)))
}

func TestRegistrationSupervisorPreservesRenewalFailureDuringCancellation(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0)
	ctx, cancel := context.WithCancel(context.Background())
	renewalErr := errors.New("renewal response failed")
	registration := registrationConfig{
		admissionRevision: testAdmissionRevision,
		register: func(context.Context, string, string, string, string) (RegistrationLease, error) {
			cancel()
			return RegistrationLease{}, renewalErr
		},
		retryInitialInterval: time.Second,
		retryMaxInterval:     4 * time.Second,
		attemptTimeout:       time.Second,
		shutdownMargin:       time.Second,
		now:                  func() time.Time { return now },
		jitter:               identityRegistrationJitter,
	}

	err := superviseRegistration(
		ctx,
		"test.toolset",
		testProviderID,
		testProviderIncarnationID,
		registrationState{
			lease:    RegistrationLease{RegistrationToken: testRegistrationTokenA, Duration: time.Minute},
			deadline: now.Add(time.Minute),
		},
		registration,
		telemetry.NewNoopLogger(),
		func(context.Context, time.Duration) error {
			return nil
		},
	)

	require.ErrorIs(t, err, renewalErr)
	require.ErrorIs(t, err, context.Canceled)
}

func TestRegistrationSupervisorRenewsAuthoritativeLease(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0)
	registration := registrationConfig{
		admissionRevision: testAdmissionRevision,
		register: func(context.Context, string, string, string, string) (RegistrationLease, error) {
			return RegistrationLease{RegistrationToken: testRegistrationTokenA, Duration: time.Minute}, nil
		},
		retryInitialInterval: time.Second,
		retryMaxInterval:     4 * time.Second,
		attemptTimeout:       time.Second,
		shutdownMargin:       time.Second,
		now:                  func() time.Time { return now },
		jitter:               identityRegistrationJitter,
	}
	var waits []time.Duration
	err := superviseRegistration(
		context.Background(),
		"test.toolset",
		testProviderID,
		testProviderIncarnationID,
		registrationState{
			lease:    RegistrationLease{RegistrationToken: testRegistrationTokenA, Duration: 10 * time.Second},
			deadline: now.Add(10 * time.Second),
		},
		registration,
		telemetry.NewNoopLogger(),
		func(_ context.Context, delay time.Duration) error {
			waits = append(waits, delay)
			now = now.Add(delay)
			if len(waits) == 2 {
				return context.Canceled
			}
			return nil
		},
	)

	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, []time.Duration{10 * time.Second / 3, 20 * time.Second}, waits)
}

func TestRegistrationSupervisorStopsImmediatelyWhenSuperseded(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0)
	registration := registrationConfig{
		admissionRevision: testAdmissionRevision,
		register: func(context.Context, string, string, string, string) (RegistrationLease, error) {
			return RegistrationLease{}, goa.NewServiceError(errors.New("replacement waiting"), "admission_blocked", false, false, false)
		},
		retryInitialInterval: time.Second,
		retryMaxInterval:     4 * time.Second,
		attemptTimeout:       time.Second,
		shutdownMargin:       time.Second,
		now:                  func() time.Time { return now },
		jitter:               identityRegistrationJitter,
	}
	err := superviseRegistration(
		context.Background(),
		"test.toolset",
		testProviderID,
		testProviderIncarnationID,
		registrationState{
			lease:    RegistrationLease{RegistrationToken: testRegistrationTokenA, Duration: time.Hour},
			deadline: now.Add(time.Hour),
		},
		registration,
		telemetry.NewNoopLogger(),
		func(context.Context, time.Duration) error {
			return nil
		},
	)
	require.ErrorIs(t, err, ErrRegistrationSuperseded)
	require.ErrorContains(t, err, "replacement waiting")
}

func TestRegisterUntilSuccessWaitsForCanceledAttempt(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	returned := make(chan struct{})
	registration := registrationConfig{
		admissionRevision: testAdmissionRevision,
		register: func(ctx context.Context, _, _, _, _ string) (RegistrationLease, error) {
			close(started)
			<-ctx.Done()
			close(returned)
			return RegistrationLease{}, ctx.Err()
		},
		retryInitialInterval: time.Second,
		retryMaxInterval:     4 * time.Second,
		attemptTimeout:       time.Minute,
		shutdownMargin:       time.Second,
		now:                  time.Now,
		jitter:               identityRegistrationJitter,
	}
	errc := make(chan error, 1)
	go func() {
		_, err := registerUntilSuccess(
			ctx,
			"test.toolset",
			testProviderID,
			testProviderIncarnationID,
			registration,
			telemetry.NewNoopLogger(),
			waitRegistrationDelay,
		)
		errc <- err
	}()

	<-started
	cancel()
	require.ErrorIs(t, <-errc, context.Canceled)
	select {
	case <-returned:
	default:
		t.Fatal("registration returned before the canceled callback exited")
	}
}
