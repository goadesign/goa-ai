package provider

// These tests pin provider lifecycle ownership: no consumer-group sink exists
// before admission, renewals remain bounded by the authoritative lease, and
// cancellation waits for context-compliant callbacks.

import (
	"context"
	"encoding/json"
	"errors"
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
func successfulRegistration() Registration {
	return Registration{
		AdmissionRevision: testAdmissionRevision,
		Register: func(context.Context, string, string, string, string) (RegistrationLease, error) {
			return RegistrationLease{RegistrationToken: testRegistrationTokenA, Duration: time.Hour}, nil
		},
		Release: func(context.Context, string, string, string, string) error {
			return nil
		},
	}
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
			Release: func(_ context.Context, gotToolset, providerID, incarnationID, token string) error {
				assert.True(t, sinkClosed.Load())
				assert.Equal(t, toolset, gotToolset)
				assert.Equal(t, testProviderID, providerID)
				assert.NotEmpty(t, incarnationID)
				assert.Equal(t, testRegistrationTokenA, token)
				close(released)
				return nil
			},
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
		toolregistry.DefaultResultStreamTTL,
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
		Release: func(context.Context, string, string, string, string) error { return nil },
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
		Release:              func(context.Context, string, string, string, string) error { return nil },
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
		releaseMu sync.Mutex
		released  []string
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
		Release: func(_ context.Context, _, _, _ string, token string) error {
			releaseMu.Lock()
			released = append(released, token)
			releaseMu.Unlock()
			return nil
		},
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
	releaseMu.Lock()
	assert.ElementsMatch(t, []string{testRegistrationTokenA, testRegistrationTokenB}, released)
	releaseMu.Unlock()
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
