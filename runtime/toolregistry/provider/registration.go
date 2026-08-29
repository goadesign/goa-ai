// Package provider owns registry membership for each active tool provider.
//
// Registration is reconciled by Serve before and during stream consumption so
// registry or Redis state loss cannot strand a healthy provider process outside
// the catalog. The caller supplies the typed registry operation; this package
// owns when it runs and never reconstructs or weakens the registered schema.
package provider

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"sync"
	"time"

	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/toolregistry"
	goa "goa.design/goa/v3/pkg"
)

type (
	// RegistrationLease is the admission generation and lease duration returned
	// by one typed registry Register call.
	RegistrationLease struct {
		// RegistrationToken is the deterministic composite admission fence.
		RegistrationToken string
		// Duration is the provider lease lifetime granted by the registry.
		Duration time.Duration
	}

	// ClaimDisposition is the registry-owned pre-dispatch settlement outcome.
	ClaimDisposition string

	// Registration configures the registry membership that Serve owns for its
	// complete lifecycle.
	Registration struct {
		// AdmissionRevision is the deployment-issued revision shared by every
		// provider replica in one fenced admission. Same-contract scaling and
		// RollingUpdate reuse it; change it only to create a new fence.
		AdmissionRevision string

		// Register performs one idempotent provider admission or lease renewal
		// using the toolset, stable provider ID, runtime-generated incarnation,
		// and immutable admission revision supplied by Serve. Implementations
		// must call the typed registry client
		// with one immutable schema payload and return its admitted generation
		// token and lease duration.
		//
		// Register must honor ctx and return promptly after its cancellation.
		// Serve reports callbacks that outlive the shared settlement deadline
		// and continues process shutdown without waiting beyond that deadline.
		Register func(
			ctx context.Context,
			toolset, providerID, incarnationID, admissionRevision string,
		) (RegistrationLease, error)

		// Drain atomically marks the exact lease non-routable while preserving
		// its authority to complete already-claimed calls.
		// Serve may call Drain concurrently for distinct tokens after a renewal
		// returns a different token. Implementations must support concurrent
		// calls and return promptly after ctx cancellation.
		Drain func(
			ctx context.Context,
			toolset, providerID, incarnationID, expectedRegistrationToken string,
			settlementDuration time.Duration,
		) error

		// Release idempotently removes the exact provider-incarnation lease from
		// the admitted token.
		// Serve calls it after consumption and renewal have stopped and all
		// workers/acks have settled. If renewal reports an unexpected token,
		// Serve may call Release concurrently for distinct tokens so every lease
		// remains inside one shutdown deadline. Implementations must support
		// concurrent calls and honor ctx.
		Release func(
			ctx context.Context,
			toolset, providerID, incarnationID, expectedRegistrationToken string,
		) error

		// Complete atomically publishes one canonical terminal result and commits
		// terminal state in the registry-owned call record.
		Complete func(
			ctx context.Context,
			toolset, providerID, incarnationID, providerRegistrationToken,
			requestEventID string,
			result toolregistry.ToolResultMessage,
		) error

		// PublishOutputDelta asks the registry to append one fragment only while
		// the exact claimed call remains nonterminal.
		PublishOutputDelta func(
			ctx context.Context,
			toolset, providerID, incarnationID, providerRegistrationToken,
			callRegistrationToken, toolUseID, requestEventID, stream, delta string,
		) error

		// ReportOverload asks the registry to append retry control only while the
		// exact claimed call remains nonterminal.
		ReportOverload func(
			ctx context.Context,
			toolset, providerID, incarnationID, providerRegistrationToken,
			callRegistrationToken, toolUseID, requestEventID string,
		) error

		// Claim asks the registry for the one authoritative pre-dispatch
		// transition. The same exact claim may be retried after an uncertain
		// transport result and must return ClaimExecute again. Only ClaimExecute
		// permits handler invocation.
		Claim func(ctx context.Context, request ClaimRequest) (ClaimDisposition, error)

		// RetryInitialInterval is the initial delay before retrying a failed
		// registration. Consecutive failures back off exponentially. Zero uses
		// DefaultRegistrationRetryInitialInterval.
		RetryInitialInterval time.Duration

		// RetryMaxInterval bounds registration retry backoff. Zero uses
		// DefaultRegistrationRetryMaxInterval.
		RetryMaxInterval time.Duration

		// AttemptTimeout bounds each registration attempt. Zero uses
		// DefaultRegistrationTimeout.
		AttemptTimeout time.Duration

		// ShutdownMargin is how long before the authoritative lease expiration
		// Serve closes its sink. Zero uses DefaultRegistrationShutdownMargin.
		ShutdownMargin time.Duration

		// ReleaseTimeout bounds all release retries after Serve has stopped.
		// Zero uses DefaultRegistrationReleaseTimeout.
		ReleaseTimeout time.Duration
	}

	registrationConfig struct {
		admissionRevision  string
		register           func(ctx context.Context, toolset, providerID, incarnationID, admissionRevision string) (RegistrationLease, error)
		drain              func(ctx context.Context, toolset, providerID, incarnationID, expectedRegistrationToken string, settlementDuration time.Duration) error
		release            func(ctx context.Context, toolset, providerID, incarnationID, expectedRegistrationToken string) error
		complete           func(ctx context.Context, toolset, providerID, incarnationID, providerRegistrationToken, requestEventID string, result toolregistry.ToolResultMessage) error
		publishOutputDelta func(
			ctx context.Context,
			toolset, providerID, incarnationID, providerRegistrationToken,
			callRegistrationToken, toolUseID, requestEventID, stream, delta string,
		) error
		reportOverload func(
			ctx context.Context,
			toolset, providerID, incarnationID, providerRegistrationToken,
			callRegistrationToken, toolUseID, requestEventID string,
		) error
		claim                func(ctx context.Context, request ClaimRequest) (ClaimDisposition, error)
		retryInitialInterval time.Duration
		retryMaxInterval     time.Duration
		attemptTimeout       time.Duration
		shutdownMargin       time.Duration
		releaseTimeout       time.Duration
		now                  func() time.Time
		jitter               registrationJitter
	}

	registrationState struct {
		lease    RegistrationLease
		deadline time.Time
	}

	registrationWait func(ctx context.Context, delay time.Duration) error

	registrationJitter func(base, maximum time.Duration) time.Duration

	// registrationTokenChangedError preserves both exact leases so Serve can
	// release them only after consumption and acknowledgements settle.
	registrationTokenChangedError struct {
		expectedToken string
		receivedToken string
	}
)

const (
	// ClaimExecute grants this exact Serve lifecycle immutable execution
	// ownership for the call.
	ClaimExecute ClaimDisposition = "execute"
	// ClaimTerminal reports that retained terminal history already exists.
	ClaimTerminal ClaimDisposition = "terminal"
	// ClaimOwned reports that another delivery already owns execution.
	ClaimOwned ClaimDisposition = "claimed"
	// ClaimExpired reports that Redis time settled the expired call.
	ClaimExpired ClaimDisposition = "expired"

	// DefaultRegistrationRetryInitialInterval is the default delay after the
	// first failed provider registration attempt.
	DefaultRegistrationRetryInitialInterval = 500 * time.Millisecond

	// DefaultRegistrationRetryMaxInterval bounds the default exponential retry
	// schedule for provider registration.
	DefaultRegistrationRetryMaxInterval = 30 * time.Second

	// DefaultRegistrationTimeout is the default deadline for one provider
	// registration attempt.
	DefaultRegistrationTimeout = 5 * time.Second

	// DefaultRegistrationShutdownMargin closes provider consumption before the
	// authoritative registry lease can expire.
	DefaultRegistrationShutdownMargin = time.Second

	// DefaultRegistrationReleaseTimeout bounds exact release reconciliation
	// during provider shutdown.
	DefaultRegistrationReleaseTimeout = 10 * time.Second

	registrationJitterDivisor  = 5
	registrationRenewalDivisor = 3
)

var (
	// ErrRegistrationLeaseExpired means Serve stopped consumption because it
	// could not renew safely before the last authoritative provider lease
	// expired.
	ErrRegistrationLeaseExpired = errors.New("provider registration lease expired")

	// ErrRegistrationTokenChanged means a successful renewal returned another
	// admission generation for an immutable Serve lifecycle.
	ErrRegistrationTokenChanged = errors.New("provider registration token changed")

	// ErrRegistrationSuperseded means another admission is waiting and this
	// provider must stop claiming work immediately.
	ErrRegistrationSuperseded = errors.New("provider registration superseded")
)

// Error describes the immutable-lifecycle token violation.
func (e *registrationTokenChangedError) Error() string {
	return fmt.Sprintf(
		"%s: admission %q renewed as %q",
		ErrRegistrationTokenChanged,
		e.expectedToken,
		e.receivedToken,
	)
}

// Unwrap classifies the invariant failure for errors.Is.
func (e *registrationTokenChangedError) Unwrap() error {
	return ErrRegistrationTokenChanged
}

// normalized validates registration and applies the documented zero-value
// durations before Serve performs any external side effects.
func (r Registration) normalized() (registrationConfig, error) {
	return r.normalizedWithJitter(jitterRegistrationDelay)
}

// normalizedWithJitter installs the supplied jitter source so deterministic
// tests can exercise retry and renewal scheduling without sleeping.
func (r Registration) normalizedWithJitter(jitter registrationJitter) (registrationConfig, error) {
	if r.Register == nil {
		return registrationConfig{}, fmt.Errorf("registration callback is required")
	}
	if r.Release == nil {
		return registrationConfig{}, fmt.Errorf("release callback is required")
	}
	if r.Drain == nil {
		return registrationConfig{}, fmt.Errorf("drain callback is required")
	}
	if r.Complete == nil {
		return registrationConfig{}, fmt.Errorf("complete callback is required")
	}
	if r.PublishOutputDelta == nil {
		return registrationConfig{}, fmt.Errorf("publish output delta callback is required")
	}
	if r.ReportOverload == nil {
		return registrationConfig{}, fmt.Errorf("report overload callback is required")
	}
	if r.Claim == nil {
		return registrationConfig{}, fmt.Errorf("claim callback is required")
	}
	if err := toolregistry.ValidateAdmissionRevision(r.AdmissionRevision); err != nil {
		return registrationConfig{}, err
	}
	if r.RetryInitialInterval < 0 {
		return registrationConfig{}, fmt.Errorf("registration retry initial interval must not be negative")
	}
	if r.RetryMaxInterval < 0 {
		return registrationConfig{}, fmt.Errorf("registration retry max interval must not be negative")
	}
	if r.AttemptTimeout < 0 {
		return registrationConfig{}, fmt.Errorf("registration attempt timeout must not be negative")
	}
	if r.ShutdownMargin < 0 {
		return registrationConfig{}, fmt.Errorf("registration shutdown margin must not be negative")
	}
	if r.ReleaseTimeout < 0 {
		return registrationConfig{}, fmt.Errorf("registration release timeout must not be negative")
	}

	retryInitialInterval := r.RetryInitialInterval
	if retryInitialInterval == 0 {
		retryInitialInterval = DefaultRegistrationRetryInitialInterval
	}
	retryMaxInterval := r.RetryMaxInterval
	if retryMaxInterval == 0 {
		retryMaxInterval = DefaultRegistrationRetryMaxInterval
	}
	if retryInitialInterval > retryMaxInterval {
		return registrationConfig{}, fmt.Errorf("registration retry initial interval must not exceed max interval")
	}
	attemptTimeout := r.AttemptTimeout
	if attemptTimeout == 0 {
		attemptTimeout = DefaultRegistrationTimeout
	}
	shutdownMargin := r.ShutdownMargin
	if shutdownMargin == 0 {
		shutdownMargin = DefaultRegistrationShutdownMargin
	}
	releaseTimeout := r.ReleaseTimeout
	if releaseTimeout == 0 {
		releaseTimeout = DefaultRegistrationReleaseTimeout
	}
	for name, duration := range map[string]time.Duration{
		"registration retry max interval": retryMaxInterval,
		"registration attempt timeout":    attemptTimeout,
		"registration shutdown margin":    shutdownMargin,
		"registration release timeout":    releaseTimeout,
	} {
		if duration > toolregistry.MaxProviderLeaseDuration {
			return registrationConfig{}, fmt.Errorf("%s must not exceed %s", name, toolregistry.MaxProviderLeaseDuration)
		}
	}
	return registrationConfig{
		admissionRevision:    r.AdmissionRevision,
		register:             r.Register,
		drain:                r.Drain,
		release:              r.Release,
		complete:             r.Complete,
		publishOutputDelta:   r.PublishOutputDelta,
		reportOverload:       r.ReportOverload,
		claim:                r.Claim,
		retryInitialInterval: retryInitialInterval,
		retryMaxInterval:     retryMaxInterval,
		attemptTimeout:       attemptTimeout,
		shutdownMargin:       shutdownMargin,
		releaseTimeout:       releaseTimeout,
		now:                  time.Now,
		jitter:               jitter,
	}, nil
}

// registerUntilSuccess establishes registry membership after Serve opens the
// stream and before it creates the consumer-group sink. Transient failures
// remain owned by Serve and are retried until the lifecycle context is canceled.
func registerUntilSuccess(
	ctx context.Context,
	toolset, providerID, incarnationID string,
	registration registrationConfig,
	logger telemetry.Logger,
	wait registrationWait,
) (registrationState, error) {
	failures := 0
	for {
		state, err := registerProvider(ctx, toolset, providerID, incarnationID, registration, time.Time{})
		if err == nil {
			if !state.deadline.After(registration.now().Add(registration.shutdownMargin)) {
				err = fmt.Errorf(
					"register provider %q for toolset %q: lease duration %s was consumed by the attempt",
					providerID,
					toolset,
					state.lease.Duration,
				)
			} else {
				return state, nil
			}
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return registrationState{}, errors.Join(err, ctxErr)
		}
		if isPermanentRegistrationError(err) {
			return registrationState{}, err
		}
		failures++
		retryDelay := registration.retryDelay(failures)
		logger.Warn(
			ctx,
			"provider registration failed; retrying",
			"component", "tool-registry-provider",
			"toolset", toolset,
			"provider_id", providerID,
			"retry_interval", retryDelay,
			"err", err,
		)
		if err := wait(ctx, retryDelay); err != nil {
			return registrationState{}, err
		}
	}
}

// superviseRegistration renews membership only while the last authoritative
// lease remains valid. It returns before the safety cutoff so Serve can close
// the consumer-group sink before registry admission expires.
func superviseRegistration(
	ctx context.Context,
	toolset, providerID, incarnationID string,
	state registrationState,
	registration registrationConfig,
	logger telemetry.Logger,
	wait registrationWait,
) error {
	failures := 0
	for {
		cutoff := state.deadline.Add(-registration.shutdownMargin)
		now := registration.now()
		if !now.Before(cutoff) {
			return fmt.Errorf("%w after %s lease", ErrRegistrationLeaseExpired, state.lease.Duration)
		}
		delay := registration.renewalDelay(state.lease.Duration)
		if failures > 0 {
			delay = registration.retryDelay(failures)
		}
		remaining := cutoff.Sub(now)
		delay = min(delay, remaining/2)
		if delay <= 0 {
			return fmt.Errorf("%w after %s lease", ErrRegistrationLeaseExpired, state.lease.Duration)
		}
		if err := wait(ctx, delay); err != nil {
			return err
		}
		renewed, err := registerProvider(ctx, toolset, providerID, incarnationID, registration, cutoff)
		if err == nil {
			if !registration.now().Before(cutoff) {
				return fmt.Errorf("%w while renewal was in flight", ErrRegistrationLeaseExpired)
			}
			if renewed.lease.RegistrationToken != state.lease.RegistrationToken {
				return &registrationTokenChangedError{
					expectedToken: state.lease.RegistrationToken,
					receivedToken: renewed.lease.RegistrationToken,
				}
			}
			if !renewed.deadline.After(registration.now().Add(registration.shutdownMargin)) {
				return fmt.Errorf(
					"%w: renewal duration %s was consumed by the attempt",
					ErrRegistrationLeaseExpired,
					renewed.lease.Duration,
				)
			}
			state = renewed
			failures = 0
			logger.Debug(
				ctx,
				"provider registration renewed",
				"component", "tool-registry-provider",
				"toolset", toolset,
				"provider_id", providerID,
			)
			continue
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return errors.Join(err, ctxErr)
		}
		if isAdmissionBlocked(err) {
			return fmt.Errorf("%w: renewal blocked: %w", ErrRegistrationSuperseded, err)
		}
		if isPermanentRegistrationError(err) {
			return err
		}
		failures++
		logger.Warn(
			ctx,
			"provider registration renewal failed; retrying",
			"component", "tool-registry-provider",
			"toolset", toolset,
			"provider_id", providerID,
			"lease_duration", state.lease.Duration,
			"err", err,
		)
	}
}

// drainProvider boundedly retries the exact non-routable transition before
// Serve closes its request sink.
func drainProvider(
	parent context.Context,
	toolset, providerID, incarnationID, token string,
	settlementDuration time.Duration,
	registration registrationConfig,
	logger telemetry.Logger,
	wait registrationWait,
) error {
	return reconcileProviderState(
		parent,
		"drain",
		toolset,
		providerID,
		incarnationID,
		token,
		registration,
		func(ctx context.Context, toolset, providerID, incarnationID, token string) error {
			return registration.drain(
				ctx,
				toolset,
				providerID,
				incarnationID,
				token,
				settlementDuration,
			)
		},
		logger,
		wait,
	)
}

// releaseProvider boundedly retries exact provider release after all claiming
// and renewal activity has stopped.
func releaseProvider(
	parent context.Context,
	toolset, providerID, incarnationID, token string,
	registration registrationConfig,
	logger telemetry.Logger,
	wait registrationWait,
) error {
	return reconcileProviderState(
		parent,
		"release",
		toolset,
		providerID,
		incarnationID,
		token,
		registration,
		registration.release,
		logger,
		wait,
	)
}

// releaseProviderTokens removes every exact lease that this provider
// incarnation may own. All releases run at the same time under one shared
// deadline, so an unexpected token change cannot multiply process shutdown
// time.
func releaseProviderTokens(
	parent context.Context,
	toolset, providerID, incarnationID string,
	tokens []string,
	registration registrationConfig,
	logger telemetry.Logger,
	wait registrationWait,
) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), registration.releaseTimeout)
	defer cancel()

	errs := make([]error, len(tokens))
	var releases sync.WaitGroup
	for index, token := range tokens {
		releases.Go(func() {
			errs[index] = releaseProvider(
				ctx,
				toolset,
				providerID,
				incarnationID,
				token,
				registration,
				logger,
				wait,
			)
		})
	}
	releases.Wait()
	return errors.Join(errs...)
}

// reconcileProviderState retries one exact lease transition within the
// registry-owned shutdown budget.
func reconcileProviderState(
	parent context.Context,
	operation, toolset, providerID, incarnationID, token string,
	registration registrationConfig,
	transition func(context.Context, string, string, string, string) error,
	logger telemetry.Logger,
	wait registrationWait,
) error {
	ctx, cancel := context.WithTimeout(parent, registration.releaseTimeout)
	defer cancel()
	failures := 0
	for {
		attemptCtx, attemptCancel := context.WithTimeout(ctx, registration.attemptTimeout)
		err := transition(attemptCtx, toolset, providerID, incarnationID, token)
		attemptCancel()
		if err == nil {
			return nil
		}
		failures++
		delay := registration.retryDelay(failures)
		logger.Warn(
			ctx,
			"provider "+operation+" failed; retrying",
			"component", "tool-registry-provider",
			"toolset", toolset,
			"provider_id", providerID,
			"err", err,
		)
		if waitErr := wait(ctx, delay); waitErr != nil {
			return fmt.Errorf("%s provider %q for toolset %q: %w", operation, providerID, toolset, errors.Join(err, waitErr))
		}
	}
}

// registerProvider runs one caller-supplied typed registration operation under
// the configured attempt timeout.
func registerProvider(
	ctx context.Context,
	toolset, providerID, incarnationID string,
	registration registrationConfig,
	cutoff time.Time,
) (registrationState, error) {
	attemptStarted := registration.now()
	timeout := registration.attemptTimeout
	if !cutoff.IsZero() {
		timeout = min(timeout, cutoff.Sub(attemptStarted))
		if timeout <= 0 {
			return registrationState{}, ErrRegistrationLeaseExpired
		}
	}
	attemptCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	lease, err := registration.register(
		attemptCtx,
		toolset,
		providerID,
		incarnationID,
		registration.admissionRevision,
	)
	if err != nil {
		return registrationState{}, fmt.Errorf("register provider %q for toolset %q: %w", providerID, toolset, err)
	}
	if err := toolregistry.ValidateRegistrationToken(lease.RegistrationToken); err != nil {
		return registrationState{}, fmt.Errorf(
			"register provider %q for toolset %q: invalid registration token: %w",
			providerID,
			toolset,
			err,
		)
	}
	if lease.Duration <= 0 {
		return registrationState{}, fmt.Errorf("register provider %q for toolset %q: non-positive lease duration", providerID, toolset)
	}
	if lease.Duration > toolregistry.MaxProviderLeaseDuration {
		return registrationState{}, fmt.Errorf(
			"register provider %q for toolset %q: lease duration exceeds %s",
			providerID,
			toolset,
			toolregistry.MaxProviderLeaseDuration,
		)
	}
	safetyBudget, err := registration.leaseSafetyBudget()
	if err != nil {
		return registrationState{}, fmt.Errorf(
			"register provider %q for toolset %q: %w",
			providerID,
			toolset,
			err,
		)
	}
	if lease.Duration <= safetyBudget {
		return registrationState{}, fmt.Errorf(
			"register provider %q for toolset %q: lease duration %s must exceed shutdown and retry budget %s",
			providerID,
			toolset,
			lease.Duration,
			safetyBudget,
		)
	}
	return registrationState{
		lease:    lease,
		deadline: attemptStarted.Add(lease.Duration),
	}, nil
}

// leaseSafetyBudget is the time required to stop safely after one bounded
// renewal attempt and one maximum retry delay.
func (r registrationConfig) leaseSafetyBudget() (time.Duration, error) {
	budget := r.shutdownMargin
	for _, duration := range []time.Duration{r.attemptTimeout, r.retryMaxInterval} {
		if duration > time.Duration(math.MaxInt64)-budget {
			return 0, fmt.Errorf("registration lease safety budget overflows time.Duration")
		}
		budget += duration
	}
	return budget, nil
}

// waitRegistrationDelay waits for the next reconciliation attempt or returns
// immediately when the provider lifecycle ends.
func waitRegistrationDelay(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// retryDelay returns the bounded exponential delay for one consecutive failure.
func (r registrationConfig) retryDelay(failures int) time.Duration {
	base := r.retryInitialInterval
	for range failures - 1 {
		if base >= r.retryMaxInterval/2 {
			base = r.retryMaxInterval
			break
		}
		base *= 2
	}
	return r.jitter(base, r.retryMaxInterval)
}

// renewalDelay schedules the first reconciliation around one third of the
// granted lease so callback latency, clock skew, and retries retain ample room
// before the conservative local cutoff.
func (r registrationConfig) renewalDelay(leaseDuration time.Duration) time.Duration {
	return r.jitter(leaseDuration/registrationRenewalDivisor, 0)
}

// jitterRegistrationDelay applies ±20 percent jitter and honors maximum when
// one is supplied.
func jitterRegistrationDelay(base, maximum time.Duration) time.Duration {
	spread := base / registrationJitterDivisor
	// #nosec G404 -- jitter prevents scheduling synchronization; it does not
	// protect a security-sensitive random value.
	delay := base - spread + time.Duration(rand.Int64N(int64(2*spread)+1))
	if maximum > 0 {
		delay = min(delay, maximum)
	}
	return delay
}

// isPermanentRegistrationError identifies retirement and validation failures
// that require an operator-controlled rollout rather than automatic retry.
func isPermanentRegistrationError(err error) bool {
	var serviceErr *goa.ServiceError
	if !errors.As(err, &serviceErr) {
		return false
	}
	switch serviceErr.Name {
	case "admission_retired", "validation_error":
		return true
	default:
		return false
	}
}

// isAdmissionBlocked identifies the retryable replacement fence.
func isAdmissionBlocked(err error) bool {
	var serviceErr *goa.ServiceError
	return errors.As(err, &serviceErr) && serviceErr.Name == "admission_blocked"
}
