// Package middleware provides model.Client middleware such as adaptive rate
// limiting. Middleware runs around the raw provider beneath the opaque client,
// then model.NewClient applies final output validation once.
package middleware

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"sync"
	"time"

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

		currentTPM float64
		minTPM     float64
		maxTPM     float64

		recoveryRate   float64
		reserveOutput  bool
		reconcileUsage bool
		balance        float64
		balanceAt      time.Time
		balanceChanged chan struct{}

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
		next      model.Streamer
		limiter   *AdaptiveRateLimiter
		reserved  int
		usage     model.TokenUsage
		settleErr error
		once      sync.Once
	}

	// clusterMap is the subset of rmap.Map used by the cluster-aware limiter.
	clusterMap interface {
		Get(key string) (string, bool)
		SetIfNotExists(ctx context.Context, key, value string) (bool, error)
		TestAndSet(ctx context.Context, key, test, value string) (string, error)
		Subscribe() <-chan rmap.EventKind
		Unsubscribe(<-chan rmap.EventKind)
	}
)

const outputReservationClusterKeySuffix = ".input-plus-max-output.v1"
const reconciledUsageClusterKeySuffix = ".provider-usage.v1"

// NewAdaptiveRateLimiter constructs an AdaptiveRateLimiter with a token-capacity
// budget per minute. When m and key are set, it coordinates capacity across
// processes using a Pulse replicated map; otherwise it operates as a
// process-local limiter. The context owns shared capacity updates for the
// limiter lifetime; cancellation releases its map subscription.
func NewAdaptiveRateLimiter(ctx context.Context, m *rmap.Map, key string, initialTPM, maxTPM float64) (*AdaptiveRateLimiter, error) {
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
) (*AdaptiveRateLimiter, error) {
	key = outputReservationClusterKey(key)
	return newPublicAdaptiveRateLimiter(ctx, m, key, initialTPM, maxTPM, true)
}

// NewUsageReconciledAdaptiveRateLimiter reserves estimated input plus maximum
// output before a model call. Once the provider reports usage, the local token
// balance replaces that reservation with the reported total. The capacity is
// coordinated across processes; token balances remain local to each process.
func NewUsageReconciledAdaptiveRateLimiter(
	ctx context.Context,
	m *rmap.Map,
	key string,
	initialTPM, maxTPM float64,
) (*AdaptiveRateLimiter, error) {
	if key != "" {
		key += reconciledUsageClusterKeySuffix
	}
	l, err := newPublicAdaptiveRateLimiter(ctx, m, key, initialTPM, maxTPM, true)
	if err != nil {
		return nil, err
	}
	l.reconcileUsage = true
	return l, nil
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
) (*AdaptiveRateLimiter, error) {
	if initialTPM < 1 || maxTPM < initialTPM || math.IsNaN(initialTPM) || math.IsNaN(maxTPM) || math.IsInf(initialTPM, 0) || math.IsInf(maxTPM, 0) {
		return nil, errors.New("adaptive rate limiting requires finite token capacities of at least one, with maximum at least initial")
	}
	if (m == nil) != (key == "") {
		return nil, errors.New("adaptive rate limiting requires a map and key together")
	}
	var cm clusterMap
	if m != nil {
		cm = m
	}
	limiter, err := newClusterAdaptiveRateLimiter(ctx, cm, key, initialTPM, maxTPM)
	if err != nil {
		return nil, err
	}
	limiter.reserveOutput = reserveOutput
	return limiter, nil
}

// newAdaptiveRateLimiter constructs an AdaptiveRateLimiter configured with an
// initial token-capacity-per-minute budget and an upper bound. The limiter uses
// a simple AIMD strategy and is used internally by the cluster-aware
// constructor.
//
// initialTPM and maxTPM use the token-cost units selected by the middleware.
func newAdaptiveRateLimiter(initialTPM, maxTPM float64) *AdaptiveRateLimiter {
	minTPM := initialTPM * 0.1
	if minTPM < 1 {
		minTPM = 1
	}
	recoveryRate := initialTPM * 0.05
	if recoveryRate < 1 {
		recoveryRate = 1
	}
	return &AdaptiveRateLimiter{
		currentTPM:     initialTPM,
		minTPM:         minTPM,
		maxTPM:         maxTPM,
		recoveryRate:   recoveryRate,
		balance:        initialTPM,
		balanceAt:      time.Now(),
		balanceChanged: make(chan struct{}),
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
	reserved, err := c.limiter.waitWithReservation(ctx, c.counter, req)
	if err != nil {
		return nil, err
	}
	resp, err := c.next.Complete(ctx, req)
	if c.limiter.reconcileUsage {
		if err != nil {
			if settleErr := c.limiter.settle(reserved, model.UsageFromError(err)); settleErr != nil {
				err = errors.Join(err, settleErr)
			}
		} else {
			err = c.limiter.settle(reserved, &resp.Usage)
		}
	}
	c.limiter.observe(err)
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// Stream enforces the limiter before delegating to the underlying client.
func (c *limitedProvider) Stream(ctx context.Context, req *model.Request) (model.Streamer, error) {
	reserved, err := c.limiter.waitWithReservation(ctx, c.counter, req)
	if err != nil {
		return nil, err
	}
	stream, err := c.next.Stream(ctx, req)
	if err != nil {
		if c.limiter.reconcileUsage {
			if settleErr := c.limiter.settle(reserved, model.UsageFromError(err)); settleErr != nil {
				err = errors.Join(err, settleErr)
			}
		}
		c.limiter.observe(err)
		if stream != nil {
			err = errors.Join(err, stream.Close())
		}
		return nil, err
	}
	return &limitedStreamer{next: stream, limiter: c.limiter, reserved: reserved}, nil
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
	if s.limiter.reconcileUsage {
		if delta, ok := chunk.(model.UsageChunk); ok {
			usage, usageErr := model.AddTokenUsage(s.usage, delta.Usage)
			if usageErr != nil {
				return nil, usageErr
			}
			s.usage = usage
		}
	}
	if err != nil {
		s.once.Do(func() {
			if s.limiter.reconcileUsage {
				s.settleErr = s.settle(err)
			}
			// Only literal EOF proves successful model capacity. A wrapped EOF
			// reports the provider failure that added the wrapper.
			//nolint:errorlint // Exact equality is required by the model stream contract.
			if err == io.EOF {
				s.limiter.observe(nil)
				return
			}
			s.limiter.observe(err)
		})
		if s.settleErr != nil {
			return nil, s.settleErr
		}
	}
	return chunk, err
}

// Close releases the provider stream without guessing whether an unread
// stream would have succeeded or failed.
func (s *limitedStreamer) Close() error {
	err := s.next.Close()
	if s.limiter.reconcileUsage {
		s.once.Do(func() { s.settleErr = s.settle(err) })
	}
	if s.settleErr != nil {
		return errors.Join(err, s.settleErr)
	}
	return err
}

// settle uses the complete provider response when available. A failed or
// unfinished stream uses retained usage or the usage chunks already received.
func (s *limitedStreamer) settle(err error) error {
	if usage := model.UsageFromError(err); usage != nil {
		return s.limiter.settle(s.reserved, usage)
	}
	//nolint:errorlint // Only literal EOF proves a complete provider response.
	if err == io.EOF {
		if response := s.next.Response(); response != nil {
			return s.limiter.settle(s.reserved, &response.Usage)
		}
	}
	return s.limiter.settle(s.reserved, &s.usage)
}

// Response returns the provider's response after clean stream completion.
func (s *limitedStreamer) Response() *model.Response {
	return s.next.Response()
}

// waitWithReservation charges a request before it reaches the provider and
// returns that charge so reported usage can replace it at completion.
func (l *AdaptiveRateLimiter) waitWithReservation(
	ctx context.Context,
	counter model.TokenCounter,
	req *model.Request,
) (int, error) {
	if l.reserveOutput && req.MaxTokens <= 0 {
		return 0, errors.New("adaptive rate limiting with output reservation requires positive max tokens")
	}
	count, err := counter.CountTokens(ctx, req)
	if err != nil {
		return 0, err
	}
	if count.InputTokens < 0 {
		return 0, errors.New("adaptive rate limiting requires a nonnegative provider token count")
	}
	if !count.Exact && !l.reconcileUsage {
		return 0, errors.New("adaptive rate limiting requires an exact provider token count")
	}
	cost := count.InputTokens
	if l.reserveOutput {
		if req.MaxTokens > math.MaxInt-cost {
			return 0, errors.New("adaptive rate limiting token cost exceeds integer range")
		}
		cost += req.MaxTokens
	}
	return cost, l.waitForBalance(ctx, cost)
}

// waitForBalance spends a provisional request charge from the process-local
// token balance. Completed provider usage corrects this balance later.
func (l *AdaptiveRateLimiter) waitForBalance(ctx context.Context, cost int) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		l.mu.Lock()
		l.advanceBalance(time.Now())
		if float64(cost) > l.currentTPM {
			l.mu.Unlock()
			return errors.New("adaptive rate limiting token cost exceeds configured capacity")
		}
		if l.balance >= float64(cost) {
			l.balance -= float64(cost)
			l.mu.Unlock()
			return nil
		}
		wait := time.Duration(min((float64(cost)-l.balance)/l.currentTPM*float64(time.Minute), float64(time.Minute)))
		changed := l.balanceChanged
		l.mu.Unlock()
		timer := time.NewTimer(max(wait, time.Millisecond))
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-changed:
			timer.Stop()
		case <-timer.C:
		}
	}
}

// settle replaces a request's estimated charge with the provider's reported
// token total. Unknown usage retains the provisional charge in the limiter.
func (l *AdaptiveRateLimiter) settle(reserved int, usage *model.TokenUsage) error {
	if usage == nil || usage.InputTokens == 0 && usage.OutputTokens == 0 && usage.TotalTokens == 0 {
		return nil
	}
	if _, err := model.AddTokenUsage(model.TokenUsage{}, *usage); err != nil {
		return fmt.Errorf("adaptive rate limiting received invalid provider usage: %w", err)
	}
	actual := float64(usage.TotalTokens)
	if actual == 0 {
		actual = float64(usage.InputTokens) + float64(usage.OutputTokens)
	}
	l.mu.Lock()
	l.advanceBalance(time.Now())
	l.balance += float64(reserved) - actual
	if l.balance > l.currentTPM {
		l.balance = l.currentTPM
	}
	l.signalBalanceChange()
	l.mu.Unlock()
	return nil
}

// signalBalanceChange wakes requests that were waiting for a prior model call
// to finish. The caller holds l.mu while replacing the notification channel.
func (l *AdaptiveRateLimiter) signalBalanceChange() {
	close(l.balanceChanged)
	l.balanceChanged = make(chan struct{})
}

// advanceBalance adds tokens earned since the last request or provider report.
// The caller holds l.mu so concurrent requests share one local balance.
func (l *AdaptiveRateLimiter) advanceBalance(now time.Time) {
	l.balance += now.Sub(l.balanceAt).Seconds() * l.currentTPM / 60
	if l.balance > l.currentTPM {
		l.balance = l.currentTPM
	}
	l.balanceAt = now
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
	l.advanceBalance(time.Now())

	newTPM := l.currentTPM * 0.5
	if newTPM < l.minTPM {
		newTPM = l.minTPM
	}
	if newTPM == l.currentTPM {
		l.mu.Unlock()
		return
	}
	l.currentTPM = newTPM
	if l.balance > newTPM {
		l.balance = newTPM
	}
	l.signalBalanceChange()

	cb := l.onBackoff

	l.mu.Unlock()

	if cb != nil {
		cb(newTPM)
	}
}

func (l *AdaptiveRateLimiter) probe() {
	l.mu.Lock()
	l.advanceBalance(time.Now())

	newTPM := l.currentTPM + l.recoveryRate
	if newTPM > l.maxTPM {
		newTPM = l.maxTPM
	}
	if newTPM == l.currentTPM {
		l.mu.Unlock()
		return
	}
	l.currentTPM = newTPM
	l.signalBalanceChange()

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
	l.advanceBalance(time.Now())
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
	if l.balance > tpm {
		l.balance = tpm
	}
	l.signalBalanceChange()
	l.mu.Unlock()
}

func (l *AdaptiveRateLimiter) setClusterCallbacks(onBackoff, onProbe func(newTPM float64)) {
	l.mu.Lock()
	l.onBackoff = onBackoff
	l.onProbe = onProbe
	l.mu.Unlock()
}

func newClusterAdaptiveRateLimiter(ctx context.Context, m clusterMap, key string, initialTPM, maxTPM float64) (l *AdaptiveRateLimiter, err error) {
	if key == "" || m == nil {
		return newAdaptiveRateLimiter(initialTPM, maxTPM), nil
	}

	// Subscribe before seeding so a Redis write that reaches the local map
	// during initialization cannot leave startup waiting for a missed update.
	ch := m.Subscribe()
	if ch == nil {
		return nil, errors.New("shared token capacity map is stopped")
	}
	defer func() {
		if err != nil {
			m.Unsubscribe(ch)
		}
	}()
	if _, ok := m.Get(key); !ok {
		if _, err := m.SetIfNotExists(ctx, key, strconv.FormatFloat(initialTPM, 'f', -1, 64)); err != nil {
			return nil, fmt.Errorf("initialize shared token capacity: %w", err)
		}
	}

	cur, ok := m.Get(key)
	for !ok {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("wait for shared token capacity: %w", ctx.Err())
		case _, open := <-ch:
			if !open {
				return nil, errors.New("shared token capacity map stopped during initialization")
			}
			cur, ok = m.Get(key)
		}
	}
	sharedTPM, err := strconv.ParseFloat(cur, 64)
	if err != nil || sharedTPM <= 0 || sharedTPM > maxTPM || math.IsNaN(sharedTPM) || math.IsInf(sharedTPM, 0) {
		return nil, fmt.Errorf("invalid shared token capacity %q", cur)
	}

	l = newAdaptiveRateLimiter(sharedTPM, maxTPM)

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

	go l.watchSharedCapacity(ctx, m, key, ch)

	return l, nil
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
		nextStr := strconv.FormatFloat(next, 'f', -1, 64)
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
		nextStr := strconv.FormatFloat(next, 'f', -1, 64)
		prev, err := m.TestAndSet(ctx, key, curStr, nextStr)
		if err != nil {
			return
		}
		if prev == curStr {
			return
		}
	}
}

// watchSharedCapacity applies replicated capacity changes until the caller or
// map stops, then releases the subscription acquired during construction.
func (l *AdaptiveRateLimiter) watchSharedCapacity(ctx context.Context, m clusterMap, key string, ch <-chan rmap.EventKind) {
	defer m.Unsubscribe(ch)
	for {
		select {
		case <-ctx.Done():
			return
		case _, open := <-ch:
			if !open {
				return
			}
		}
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
}
