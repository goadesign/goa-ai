// Package middleware provides model.Client middleware such as adaptive rate
// limiting. Middleware runs around the raw provider beneath the opaque client,
// then model.NewClient applies final output validation once.
package middleware

import (
	"context"
	"errors"
	"io"
	"math"
	"strconv"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/pulse/rmap"
)

type (
	// AdaptiveRateLimiter applies an AIMD-style adaptive token-capacity bucket
	// around a raw model.Provider or beneath a validated model.Client.
	// NewAdaptiveRateLimiter charges exact input tokens.
	// NewOutputReservationAdaptiveRateLimiter charges input tokens plus the
	// provider's configured output reservation. Both adjust effective capacity
	// from provider rate limits.
	//
	// The limiter is process-local and designed to sit at the provider client
	// boundary. Callers construct a single instance per process, then use
	// WrapProvider for a raw gateway or Middleware before passing a validated
	// client to planners and runtimes.
	AdaptiveRateLimiter struct {
		mu sync.Mutex

		limiter *rate.Limiter

		currentTPM float64
		minTPM     float64
		maxTPM     float64

		recoveryRate  float64
		reserveOutput bool

		onBackoff func(newTPM float64)
		onProbe   func(newTPM float64)
	}

	limitedProvider struct {
		next    model.Provider
		counter model.TokenCounter
		limiter *AdaptiveRateLimiter
	}

	// limitedStreamer reports the exact terminal stream outcome to the limiter.
	// Opening a stream is not success because the provider can return a rate
	// limit after earlier chunks have already arrived.
	limitedStreamer struct {
		next    model.Streamer
		limiter *AdaptiveRateLimiter
		once    sync.Once
	}

	// clusterMap is the subset of rmap.Map used by the cluster-aware limiter.
	clusterMap interface {
		Get(key string) (string, bool)
		SetIfNotExists(ctx context.Context, key, value string) (bool, error)
		TestAndSet(ctx context.Context, key, test, value string) (string, error)
		Subscribe() <-chan rmap.EventKind
	}

	rmapClusterMap struct {
		m *rmap.Map
	}
)

const outputReservationClusterKeySuffix = ".input-plus-max-output.v1"

// NewAdaptiveRateLimiter constructs an AdaptiveRateLimiter with a token-capacity
// budget per minute. When m and key are set, it coordinates capacity across
// processes using a Pulse replicated map; otherwise it operates as a
// process-local limiter.
func NewAdaptiveRateLimiter(ctx context.Context, m *rmap.Map, key string, initialTPM, maxTPM float64) *AdaptiveRateLimiter {
	return newPublicAdaptiveRateLimiter(ctx, m, key, initialTPM, maxTPM, false)
}

// NewOutputReservationAdaptiveRateLimiter constructs an AdaptiveRateLimiter
// that charges exact input tokens plus each request's positive MaxTokens value.
// Its versioned cluster key keeps this combined cost separate from input-only
// limiters during rolling upgrades.
func NewOutputReservationAdaptiveRateLimiter(
	ctx context.Context,
	m *rmap.Map,
	key string,
	initialTPM, maxTPM float64,
) *AdaptiveRateLimiter {
	key = outputReservationClusterKey(key)
	return newPublicAdaptiveRateLimiter(ctx, m, key, initialTPM, maxTPM, true)
}

// outputReservationClusterKey isolates combined input-and-output accounting
// from input-only capacity stored under the caller's base key.
func outputReservationClusterKey(key string) string {
	if key == "" {
		return ""
	}
	return key + outputReservationClusterKeySuffix
}

// newPublicAdaptiveRateLimiter adapts the public Pulse map and fixes the
// request-cost contract for the lifetime of the returned limiter.
func newPublicAdaptiveRateLimiter(
	ctx context.Context,
	m *rmap.Map,
	key string,
	initialTPM, maxTPM float64,
	reserveOutput bool,
) *AdaptiveRateLimiter {
	var cm clusterMap
	if m != nil {
		cm = &rmapClusterMap{m: m}
	}
	limiter := newClusterAdaptiveRateLimiter(ctx, cm, key, initialTPM, maxTPM)
	limiter.reserveOutput = reserveOutput
	return limiter
}

// newAdaptiveRateLimiter constructs an AdaptiveRateLimiter configured with an
// initial token-capacity-per-minute budget and an upper bound. The limiter uses
// a simple AIMD strategy and is used internally by the cluster-aware
// constructor.
//
// initialTPM and maxTPM use the token-cost units selected by the middleware.
// When maxTPM is zero or less than initialTPM, it is clamped to initialTPM.
func newAdaptiveRateLimiter(initialTPM, maxTPM float64) *AdaptiveRateLimiter {
	if initialTPM <= 0 {
		// Default to a conservative budget when callers do not provide one.
		initialTPM = 60000
	}
	if maxTPM <= 0 || maxTPM < initialTPM {
		maxTPM = initialTPM
	}
	minTPM := initialTPM * 0.1
	if minTPM < 1 {
		minTPM = 1
	}
	recoveryRate := initialTPM * 0.05
	if recoveryRate < 1 {
		recoveryRate = 1
	}
	lim := rate.NewLimiter(rate.Limit(initialTPM/60.0), int(initialTPM))

	return &AdaptiveRateLimiter{
		limiter:      lim,
		currentTPM:   initialTPM,
		minTPM:       minTPM,
		maxTPM:       maxTPM,
		recoveryRate: recoveryRate,
	}
}

// Middleware returns a model.Client middleware that enforces the adaptive
// token-capacity limit selected when the limiter was constructed for both
// Complete and Stream calls. The returned client retains the input client's
// optional token-counting capability.
func (l *AdaptiveRateLimiter) Middleware() func(model.Client) (model.Client, error) {
	return func(next model.Client) (model.Client, error) {
		return model.WrapClient(next, func(raw model.Provider) model.Provider {
			return &limitedProvider{
				next:    raw,
				counter: next,
				limiter: l,
			}
		})
	}
}

// WrapProvider returns a raw provider that enforces the adaptive token-capacity
// limit before calling next. The provider must also implement model.TokenCounter
// so each request is charged from its exact provider-visible input.
func (l *AdaptiveRateLimiter) WrapProvider(next model.Provider) (model.Provider, error) {
	if err := model.ValidateProvider(next); err != nil {
		return nil, err
	}
	counter, ok := next.(model.TokenCounter)
	if !ok {
		return nil, errors.New("adaptive rate limiting requires provider token counting")
	}
	return &limitedProvider{
		next:    next,
		counter: counter,
		limiter: l,
	}, nil
}

// Complete enforces the limiter before delegating to the underlying client.
func (c *limitedProvider) Complete(ctx context.Context, req *model.Request) (*model.Response, error) {
	if err := c.limiter.wait(ctx, c.counter, req); err != nil {
		return nil, err
	}
	resp, err := c.next.Complete(ctx, req)
	c.limiter.observe(err)
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// Stream enforces the limiter before delegating to the underlying client.
func (c *limitedProvider) Stream(ctx context.Context, req *model.Request) (model.Streamer, error) {
	if err := c.limiter.wait(ctx, c.counter, req); err != nil {
		return nil, err
	}
	stream, err := c.next.Stream(ctx, req)
	if err != nil {
		c.limiter.observe(err)
		if stream != nil {
			err = errors.Join(err, stream.Close())
		}
		return nil, err
	}
	return &limitedStreamer{next: stream, limiter: c.limiter}, nil
}

// CountTokens preserves the optional token-counting capability through the
// middleware chain. Native counters are delegated so policy code sees the same
// contract as the wrapped provider client.
func (c *limitedProvider) CountTokens(ctx context.Context, req *model.Request) (model.TokenCount, error) {
	return c.counter.CountTokens(ctx, req)
}

// Recv forwards one chunk and teaches the limiter from the first terminal
// result. Clean EOF increases capacity; a streamed rate limit reduces it.
func (s *limitedStreamer) Recv() (model.Chunk, error) {
	chunk, err := s.next.Recv()
	if err != nil {
		s.once.Do(func() {
			// Only literal EOF proves successful model capacity. A wrapped EOF
			// reports the provider failure that added the wrapper.
			//nolint:errorlint // Exact equality is required by the model stream contract.
			if err == io.EOF {
				s.limiter.observe(nil)
				return
			}
			s.limiter.observe(err)
		})
	}
	return chunk, err
}

// Close releases the provider stream without guessing whether an unread
// stream would have succeeded or failed.
func (s *limitedStreamer) Close() error {
	return s.next.Close()
}

// Response returns the provider's response after clean stream completion.
func (s *limitedStreamer) Response() *model.Response {
	return s.next.Response()
}

func (l *AdaptiveRateLimiter) wait(
	ctx context.Context,
	counter model.TokenCounter,
	req *model.Request,
) error {
	if l.reserveOutput && req.MaxTokens <= 0 {
		return errors.New("adaptive rate limiting with output reservation requires positive max tokens")
	}
	count, err := counter.CountTokens(ctx, req)
	if err != nil {
		return err
	}
	if !count.Exact {
		return errors.New("adaptive rate limiting requires an exact provider token count")
	}
	if !l.reserveOutput {
		return l.limiter.WaitN(ctx, count.InputTokens)
	}
	if req.MaxTokens > math.MaxInt-count.InputTokens {
		return errors.New("adaptive rate limiting token cost exceeds integer range")
	}
	return l.limiter.WaitN(ctx, count.InputTokens+req.MaxTokens)
}

func (l *AdaptiveRateLimiter) observe(err error) {
	if err == nil {
		l.probe()
		return
	}
	if errors.Is(err, model.ErrRateLimited) {
		l.backoff()
	}
}

func (l *AdaptiveRateLimiter) backoff() {
	l.mu.Lock()

	newTPM := l.currentTPM * 0.5
	if newTPM < l.minTPM {
		newTPM = l.minTPM
	}
	if newTPM == l.currentTPM {
		l.mu.Unlock()
		return
	}
	l.currentTPM = newTPM
	l.limiter.SetLimit(rate.Limit(newTPM / 60.0))
	l.limiter.SetBurst(int(newTPM))

	cb := l.onBackoff

	l.mu.Unlock()

	if cb != nil {
		cb(newTPM)
	}
}

func (l *AdaptiveRateLimiter) probe() {
	l.mu.Lock()

	newTPM := l.currentTPM + l.recoveryRate
	if newTPM > l.maxTPM {
		newTPM = l.maxTPM
	}
	if newTPM == l.currentTPM {
		l.mu.Unlock()
		return
	}
	l.currentTPM = newTPM
	l.limiter.SetLimit(rate.Limit(newTPM / 60.0))
	l.limiter.SetBurst(int(newTPM))

	cb := l.onProbe

	l.mu.Unlock()

	if cb != nil {
		cb(newTPM)
	}
}

// replaceTPM updates the limiter effective budget to the given value,
// clamped to the configured [minTPM, maxTPM] range.
func (l *AdaptiveRateLimiter) replaceTPM(tpm float64) {
	l.mu.Lock()
	if tpm < l.minTPM {
		tpm = l.minTPM
	}
	if tpm > l.maxTPM {
		tpm = l.maxTPM
	}
	if tpm == l.currentTPM {
		l.mu.Unlock()
		return
	}
	l.currentTPM = tpm
	l.limiter.SetLimit(rate.Limit(tpm / 60.0))
	l.limiter.SetBurst(int(tpm))
	l.mu.Unlock()
}

func (l *AdaptiveRateLimiter) setClusterCallbacks(onBackoff, onProbe func(newTPM float64)) {
	l.mu.Lock()
	l.onBackoff = onBackoff
	l.onProbe = onProbe
	l.mu.Unlock()
}

func (m *rmapClusterMap) Get(key string) (string, bool) {
	return m.m.Get(key)
}

func (m *rmapClusterMap) SetIfNotExists(ctx context.Context, key, value string) (bool, error) {
	return m.m.SetIfNotExists(ctx, key, value)
}

func (m *rmapClusterMap) TestAndSet(ctx context.Context, key, test, value string) (string, error) {
	return m.m.TestAndSet(ctx, key, test, value)
}

func (m *rmapClusterMap) Subscribe() <-chan rmap.EventKind {
	return m.m.Subscribe()
}

func newClusterAdaptiveRateLimiter(ctx context.Context, m clusterMap, key string, initialTPM, maxTPM float64) *AdaptiveRateLimiter {
	if key == "" || m == nil {
		return newAdaptiveRateLimiter(initialTPM, maxTPM)
	}

	// Best-effort initialization: if the key does not exist yet, seed it with
	// the initial value. A concurrent writer may still win; we refresh below.
	if _, ok := m.Get(key); !ok {
		if _, err := m.SetIfNotExists(ctx, key, strconv.Itoa(int(initialTPM))); err != nil {
			// When seeding the shared budget fails, fall back to a process-local
			// limiter so callers still make progress instead of treating the
			// cluster map as partially initialized.
			return newAdaptiveRateLimiter(initialTPM, maxTPM)
		}
	}

	sharedTPM := initialTPM
	if cur, ok := m.Get(key); ok {
		if v, err := strconv.ParseFloat(cur, 64); err == nil && v > 0 {
			sharedTPM = v
		}
	}

	l := newAdaptiveRateLimiter(sharedTPM, maxTPM)

	min := l.minTPM
	max := l.maxTPM
	step := l.recoveryRate

	l.setClusterCallbacks(
		func(_ float64) {
			go globalBackoff(context.Background(), m, key, min)
		},
		func(_ float64) {
			go globalProbe(context.Background(), m, key, step, max)
		},
	)

	// Watch for external changes to the shared budget and reconcile the local
	// limiter when they occur.
	ch := m.Subscribe()
	go func() {
		for range ch {
			cur, ok := m.Get(key)
			if !ok {
				continue
			}
			v, err := strconv.ParseFloat(cur, 64)
			if err != nil || v <= 0 {
				continue
			}
			l.replaceTPM(v)
		}
	}()

	return l
}

func globalBackoff(ctx context.Context, m clusterMap, key string, floor float64) {
	const maxAttempts = 3

	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	for i := 0; i < maxAttempts; i++ {
		curStr, ok := m.Get(key)
		if !ok {
			return
		}
		cur, err := strconv.ParseFloat(curStr, 64)
		if err != nil || cur <= 0 {
			return
		}
		next := cur * 0.5
		if next < floor {
			next = floor
		}
		nextStr := strconv.Itoa(int(next))
		prev, err := m.TestAndSet(ctx, key, curStr, nextStr)
		if err != nil {
			return
		}
		if prev == curStr {
			return
		}
	}
}

func globalProbe(ctx context.Context, m clusterMap, key string, step, ceiling float64) {
	const maxAttempts = 3

	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	for i := 0; i < maxAttempts; i++ {
		curStr, ok := m.Get(key)
		if !ok {
			return
		}
		cur, err := strconv.ParseFloat(curStr, 64)
		if err != nil || cur <= 0 {
			return
		}
		if cur >= ceiling {
			return
		}
		next := cur + step
		if next > ceiling {
			next = ceiling
		}
		nextStr := strconv.Itoa(int(next))
		prev, err := m.TestAndSet(ctx, key, curStr, nextStr)
		if err != nil {
			return
		}
		if prev == curStr {
			return
		}
	}
}
