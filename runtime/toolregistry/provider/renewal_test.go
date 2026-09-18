package provider

// These tests exercise duration-only renewal through callback and Serve paths.
// One startup token must survive retries and shutdown; renewal cannot recreate
// lost permission or extend the provider's old local safety cutoff.

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	pulse "goa.design/goa-ai/features/stream/pulse/clients/pulse"
	mockpulse "goa.design/goa-ai/features/stream/pulse/clients/pulse/mocks"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/toolregistry"
	goa "goa.design/goa/v3/pkg"
	"goa.design/pulse/streaming"
	streamopts "goa.design/pulse/streaming/options"
)

func TestServeRequiresRenewalBeforeOpeningStream(t *testing.T) {
	t.Parallel()

	registration := successfulRegistration()
	registration.Renew = nil
	err := Serve(context.Background(), mockpulse.NewClient(t), "test.toolset", &recordingHandler{}, registration, Options{
		ProviderID: testProviderID,
		Pong:       func(context.Context, string, string, string) error { return nil },
	})
	require.ErrorContains(t, err, "renewal callback is required")
}

func TestRegistrationCallbacksValidateLeaseDuration(t *testing.T) {
	t.Parallel()

	for _, operation := range []string{"register", "renew"} {
		t.Run(operation, func(t *testing.T) {
			t.Parallel()
			for _, tc := range []struct {
				name     string
				duration time.Duration
				wantErr  string
			}{
				{"negative", -time.Nanosecond, "non-positive lease duration"},
				{"zero", 0, "non-positive lease duration"},
				{"below safety budget", 3*time.Second - time.Nanosecond, "must exceed shutdown and retry budget"},
				{"at safety budget", 3 * time.Second, "must exceed shutdown and retry budget"},
				{"above safety budget", 3*time.Second + time.Nanosecond, ""},
				{"at maximum", toolregistry.MaxProviderLeaseDuration, ""},
				{"above maximum", toolregistry.MaxProviderLeaseDuration + time.Nanosecond, "lease duration exceeds"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					now := time.Unix(1_700_000_000, 0)
					registration := registrationConfig{
						admissionRevision: testAdmissionRevision,
						register: func(context.Context, string, string, string, string) (RegistrationLease, error) {
							return RegistrationLease{RegistrationToken: testRegistrationTokenA, Duration: tc.duration}, nil
						},
						renew: func(context.Context, string, string, string, string) (time.Duration, error) {
							return tc.duration, nil
						},
						attemptTimeout:   time.Second,
						retryMaxInterval: time.Second,
						shutdownMargin:   time.Second,
						now:              func() time.Time { return now },
					}
					var state registrationState
					var err error
					if operation == "register" {
						state, err = registerProvider(context.Background(), "test.toolset", testProviderID, testProviderIncarnationID, registration)
					} else {
						state, err = renewProvider(context.Background(), "test.toolset", testProviderID, testProviderIncarnationID, testRegistrationTokenA, registration, now.Add(time.Minute))
					}
					if tc.wantErr != "" {
						require.ErrorContains(t, err, tc.wantErr)
						return
					}
					require.NoError(t, err)
					assert.Equal(t, testRegistrationTokenA, state.lease.RegistrationToken)
					assert.Equal(t, now.Add(tc.duration), state.deadline)
				})
			}
		})
	}
}

func TestRenewProviderDerivesDeadlineFromAttemptStart(t *testing.T) {
	t.Parallel()

	started := time.Unix(1_700_000_000, 0)
	now := started
	registration := registrationConfig{
		renew: func(_ context.Context, toolset, providerID, incarnationID, token string) (time.Duration, error) {
			assert.Equal(t, "test.toolset", toolset)
			assert.Equal(t, testProviderID, providerID)
			assert.Equal(t, testProviderIncarnationID, incarnationID)
			assert.Equal(t, testRegistrationTokenA, token)
			now = now.Add(4 * time.Second)
			return 10 * time.Second, nil
		},
		attemptTimeout: 5 * time.Second,
		now:            func() time.Time { return now },
	}
	state, err := renewProvider(context.Background(), "test.toolset", testProviderID, testProviderIncarnationID, testRegistrationTokenA, registration, started.Add(time.Minute))
	require.NoError(t, err)
	assert.Equal(t, started.Add(10*time.Second), state.deadline)
	assert.Equal(t, 6*time.Second, state.deadline.Sub(now))
}

func TestRenewProviderBoundsAttemptByTimeoutAndOldCutoff(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name      string
		timeout   time.Duration
		remaining time.Duration
	}{
		{"attempt timeout first", 10 * time.Millisecond, time.Minute},
		{"old cutoff first", time.Minute, 10 * time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			started := time.Now()
			registration := registrationConfig{
				renew: func(ctx context.Context, _, _, _, _ string) (time.Duration, error) {
					deadline, ok := ctx.Deadline()
					assert.True(t, ok)
					assert.WithinDuration(t, started.Add(min(tc.timeout, tc.remaining)), deadline, 5*time.Millisecond)
					<-ctx.Done()
					return 0, ctx.Err()
				},
				attemptTimeout: tc.timeout,
				now:            time.Now,
			}
			_, err := renewProvider(context.Background(), "test.toolset", testProviderID, testProviderIncarnationID, testRegistrationTokenA, registration, started.Add(tc.remaining))
			require.ErrorIs(t, err, context.DeadlineExceeded)
		})
	}
}

func TestRegistrationSupervisorDoesNotRenewAtOldCutoff(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0)
	cutoff := now.Add(9 * time.Second)
	registration := registrationConfig{
		renew: func(context.Context, string, string, string, string) (time.Duration, error) {
			t.Fatal("renewal must not start at the old cutoff")
			return 0, nil
		},
		attemptTimeout: time.Second,
		shutdownMargin: time.Second,
		now:            func() time.Time { return now },
		jitter:         identityRegistrationJitter,
	}
	err := superviseRegistration(context.Background(), "test.toolset", testProviderID, testProviderIncarnationID,
		registrationState{
			lease:    RegistrationLease{RegistrationToken: testRegistrationTokenA, Duration: 10 * time.Second},
			deadline: cutoff.Add(time.Second),
		}, registration, telemetry.NewNoopLogger(), func(context.Context, time.Duration) error {
			now = cutoff
			return nil
		})
	require.ErrorIs(t, err, ErrRegistrationLeaseExpired)
}

func TestRegistrationSupervisorCancellationPrecedesSuccessfulLateRenewal(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	started := time.Unix(1_700_000_000, 0)
	now := started
	cutoff := started.Add(9 * time.Second)
	var renewals, waits int
	registration := registrationConfig{
		renew: func(renewCtx context.Context, _, _, _, token string) (time.Duration, error) {
			renewals++
			require.Equal(t, 1, renewals)
			assert.Equal(t, testRegistrationTokenA, token)
			require.True(t, now.Before(cutoff))
			now = cutoff.Add(-time.Nanosecond)
			cancel()
			require.ErrorIs(t, renewCtx.Err(), context.Canceled)
			// A validated successful reply can arrive after the old cutoff
			// while the already-canceled lifecycle is settling accepted work.
			now = cutoff.Add(time.Nanosecond)
			return 2 * time.Minute, nil
		},
		retryInitialInterval: time.Second,
		retryMaxInterval:     time.Second,
		attemptTimeout:       time.Minute,
		shutdownMargin:       time.Second,
		now:                  func() time.Time { return now },
		jitter:               identityRegistrationJitter,
	}
	err := superviseRegistration(ctx, "test.toolset", testProviderID, testProviderIncarnationID,
		registrationState{
			lease:    RegistrationLease{RegistrationToken: testRegistrationTokenA, Duration: 10 * time.Second},
			deadline: started.Add(10 * time.Second),
		}, registration, telemetry.NewNoopLogger(), func(_ context.Context, delay time.Duration) error {
			waits++
			require.Equal(t, 1, waits, "canceled success must not schedule another renewal")
			now = now.Add(delay)
			return nil
		})
	require.Equal(t, context.Canceled, err, "successful shutdown must not manufacture lease expiry")
	assert.Equal(t, 1, renewals)
	assert.Equal(t, 1, waits)
}

func TestRegistrationSupervisorRetriesSameTokenAndResetsBackoff(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0)
	leaseLost := goa.NewServiceError(errors.New("lease missing"), "provider_lease_lost", false, false, false)
	var attempts int
	registration := registrationConfig{
		renew: func(_ context.Context, toolset, providerID, incarnationID, token string) (time.Duration, error) {
			assert.Equal(t, "test.toolset", toolset)
			assert.Equal(t, testProviderID, providerID)
			assert.Equal(t, testProviderIncarnationID, incarnationID)
			assert.Equal(t, testRegistrationTokenA, token)
			attempts++
			switch attempts {
			case 1, 2, 4:
				return 0, errors.New("registry unavailable")
			case 3:
				return 90 * time.Second, nil
			default:
				return 0, leaseLost
			}
		},
		attemptTimeout:       time.Second,
		retryInitialInterval: time.Second,
		retryMaxInterval:     4 * time.Second,
		shutdownMargin:       time.Second,
		now:                  func() time.Time { return now },
		jitter:               identityRegistrationJitter,
	}
	var waits []time.Duration
	err := superviseRegistration(context.Background(), "test.toolset", testProviderID, testProviderIncarnationID,
		registrationState{
			lease:    RegistrationLease{RegistrationToken: testRegistrationTokenA, Duration: time.Minute},
			deadline: now.Add(time.Minute),
		}, registration, telemetry.NewNoopLogger(), func(_ context.Context, delay time.Duration) error {
			waits = append(waits, delay)
			require.LessOrEqual(t, len(waits), 5, "lease loss must end renewal")
			now = now.Add(delay)
			return nil
		})
	require.ErrorIs(t, err, leaseLost)
	assert.Equal(t, []time.Duration{20 * time.Second, time.Second, 2 * time.Second, 30 * time.Second, time.Second}, waits)
	assert.Equal(t, 5, attempts)
}

func TestRegistrationSupervisorRejectsDurationConsumedByAttempt(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0)
	registration := registrationConfig{
		renew: func(context.Context, string, string, string, string) (time.Duration, error) {
			now = now.Add(9 * time.Second)
			return 10 * time.Second, nil
		},
		attemptTimeout:   time.Second,
		retryMaxInterval: time.Second,
		shutdownMargin:   time.Second,
		now:              func() time.Time { return now },
		jitter:           identityRegistrationJitter,
	}
	err := superviseRegistration(context.Background(), "test.toolset", testProviderID, testProviderIncarnationID,
		registrationState{
			lease:    RegistrationLease{RegistrationToken: testRegistrationTokenA, Duration: time.Minute},
			deadline: now.Add(time.Minute),
		}, registration, telemetry.NewNoopLogger(), func(_ context.Context, delay time.Duration) error {
			now = now.Add(delay)
			return nil
		})
	require.ErrorIs(t, err, ErrRegistrationLeaseExpired)
	require.ErrorContains(t, err, "renewal duration 10s was consumed by the attempt")
}

func TestServeRenewalDuringDrainKeepsOneLease(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name       string
		renewalErr error
	}{
		{"successful renewal", nil},
		{"lease loss", goa.NewServiceError(errors.New("lease missing"), "provider_lease_lost", false, false, false)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			handler := &blockingHandler{started: make(chan struct{}), unblock: make(chan struct{})}
			events := make(chan *streaming.Event, 1)
			events <- testToolCallEvent(t, "renewal-shutdown")
			renewalStarted := make(chan struct{})
			drained := make(chan struct{})
			var renewals, registrations, releases atomic.Int64
			var closed, completed, acknowledged atomic.Bool
			var incarnation string
			registration := successfulRegistration()
			registration.Register = func(_ context.Context, _, _, gotIncarnation, _ string) (RegistrationLease, error) {
				registrations.Add(1)
				incarnation = gotIncarnation
				return RegistrationLease{RegistrationToken: testRegistrationTokenA, Duration: time.Hour}, nil
			}
			registration.Renew = func(renewCtx context.Context, toolset, providerID, gotIncarnation, token string) (time.Duration, error) {
				renewals.Add(1)
				assert.Equal(t, "test.toolset", toolset)
				assert.Equal(t, testProviderID, providerID)
				assert.Equal(t, incarnation, gotIncarnation)
				assert.Equal(t, testRegistrationTokenA, token)
				close(renewalStarted)
				<-drained
				assert.ErrorIs(t, renewCtx.Err(), context.Canceled)
				return time.Hour, tc.renewalErr
			}
			registration.Drain = func(drainCtx context.Context, _, _, gotIncarnation, token string, duration time.Duration) error {
				assert.NoError(t, drainCtx.Err())
				assert.Equal(t, incarnation, gotIncarnation)
				assert.Equal(t, testRegistrationTokenA, token)
				assert.Equal(t, time.Second+SettlementAuthorityMargin, duration)
				close(drained)
				return nil
			}
			registration.Complete = func(context.Context, string, string, string, string, string, toolregistry.ToolResultMessage) error {
				assert.True(t, closed.Load())
				completed.Store(true)
				return nil
			}
			registration.Release = func(releaseCtx context.Context, _, _, gotIncarnation, token string) error {
				assert.NoError(t, releaseCtx.Err())
				assert.Equal(t, incarnation, gotIncarnation)
				assert.Equal(t, testRegistrationTokenA, token)
				assert.True(t, acknowledged.Load())
				releases.Add(1)
				return nil
			}
			sink := mockpulse.NewSink(t)
			sink.SetSubscribe(func() <-chan *streaming.Event { return events })
			sink.SetClose(func(context.Context) error {
				<-drained
				closed.Store(true)
				close(handler.unblock)
				return nil
			})
			sink.SetAck(func(context.Context, *streaming.Event) error {
				assert.True(t, completed.Load())
				acknowledged.Store(true)
				return nil
			})
			stream := mockpulse.NewStream(t)
			stream.SetNewSink(func(context.Context, string, ...streamopts.Sink) (pulse.Sink, error) { return sink, nil })
			client := mockpulse.NewClient(t)
			client.SetStream(func(string, ...streamopts.Stream) (pulse.Stream, error) { return stream, nil })
			done := make(chan error, 1)
			go func() {
				done <- serve(ctx, client, "test.toolset", handler, registration, Options{
					ProviderID:      testProviderID,
					Pong:            func(context.Context, string, string, string) error { return nil },
					ShutdownTimeout: time.Second,
				}, func(waitCtx context.Context, _ time.Duration) error {
					select {
					case <-handler.started:
						return nil
					case <-waitCtx.Done():
						return waitCtx.Err()
					}
				})
			}()
			select {
			case <-renewalStarted:
			case <-time.After(time.Second):
				t.Fatal("renewal did not start after accepting work")
			}
			cancel()
			select {
			case err := <-done:
				require.ErrorIs(t, err, context.Canceled)
				if tc.renewalErr == nil {
					assert.True(t, containsOnlyCancellation(err), "unexpected shutdown failure: %v", err)
				} else {
					require.ErrorIs(t, err, tc.renewalErr)
					assert.False(t, containsOnlyCancellation(err))
				}
			case <-time.After(2 * time.Second):
				t.Fatal("provider did not settle accepted work and release")
			}
			assert.Equal(t, int64(1), registrations.Load())
			assert.Equal(t, int64(1), renewals.Load())
			assert.Equal(t, int64(1), releases.Load())
		})
	}
}

func TestReleaseProviderReturnsAtDeadline(t *testing.T) {
	t.Parallel()

	releaseErr := errors.New("registry unavailable")
	var attempts int
	registration := registrationConfig{
		release: func(_ context.Context, _, _, _, token string) error {
			assert.Equal(t, testRegistrationTokenA, token)
			attempts++
			return releaseErr
		},
		retryInitialInterval: time.Minute,
		retryMaxInterval:     time.Minute,
		attemptTimeout:       time.Second,
		releaseTimeout:       10 * time.Millisecond,
		jitter:               identityRegistrationJitter,
	}
	err := releaseProvider(context.Background(), "test.toolset", testProviderID, testProviderIncarnationID, testRegistrationTokenA,
		registration, telemetry.NewNoopLogger(), func(ctx context.Context, _ time.Duration) error {
			<-ctx.Done()
			return ctx.Err()
		})
	require.ErrorIs(t, err, releaseErr)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Equal(t, 1, attempts)
}

func TestServePreservesRenewalFailureWhenDrainUsesSettlementDeadline(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	renewalStarted := make(chan struct{})
	drainStarted := make(chan struct{})
	leaseLost := goa.NewServiceError(errors.New("lease missing"), "provider_lease_lost", false, false, false)
	var releases atomic.Int64
	registration := successfulRegistration()
	registration.Renew = func(context.Context, string, string, string, string) (time.Duration, error) {
		close(renewalStarted)
		<-drainStarted
		return 0, leaseLost
	}
	registration.Drain = func(ctx context.Context, _, _, _, _ string, _ time.Duration) error {
		close(drainStarted)
		<-ctx.Done()
		return ctx.Err()
	}
	registration.Release = func(context.Context, string, string, string, string) error {
		releases.Add(1)
		return nil
	}
	events := make(chan *streaming.Event)
	sink := mockpulse.NewSink(t)
	sink.SetSubscribe(func() <-chan *streaming.Event { return events })
	sink.SetClose(func(context.Context) error { return nil })
	stream := mockpulse.NewStream(t)
	stream.SetNewSink(func(context.Context, string, ...streamopts.Sink) (pulse.Sink, error) { return sink, nil })
	client := mockpulse.NewClient(t)
	client.SetStream(func(string, ...streamopts.Stream) (pulse.Stream, error) { return stream, nil })
	done := make(chan error, 1)
	go func() {
		done <- serve(ctx, client, "test.toolset", &recordingHandler{}, registration, Options{
			ProviderID:      testProviderID,
			Pong:            func(context.Context, string, string, string) error { return nil },
			ShutdownTimeout: 50 * time.Millisecond,
		}, func(context.Context, time.Duration) error { return nil })
	}()
	select {
	case <-renewalStarted:
	case <-time.After(time.Second):
		t.Fatal("renewal did not start")
	}
	cancel()
	select {
	case err := <-done:
		require.ErrorIs(t, err, leaseLost)
		require.ErrorIs(t, err, context.Canceled)
		require.ErrorIs(t, err, context.DeadlineExceeded)
	case <-time.After(time.Second):
		t.Fatal("provider exceeded its settlement deadline")
	}
	assert.Zero(t, releases.Load())
}
