// Package middleware provides reusable model.Client middlewares such as
// adaptive rate limiting.
package middleware

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/pulse/rmap"
)

type (
	// AdaptiveRateLimiter applies an AIMD-style adaptive token bucket on top of a
	// model.Client. It estimates the token cost of each request, blocks callers
	// until capacity is available, and adjusts its effective tokens-per-minute
	// budget in response to rate limiting signals from the provider.
	//
	// The limiter is process-local and designed to sit at the provider client
	// boundary. Callers construct a single instance per process and wrap the
	// underlying model.Client with Middleware before passing it to planners or
	// runtimes.
	AdaptiveRateLimiter struct {
		mu sync.Mutex

		limiter *rate.Limiter

		currentTPM float64
		minTPM     float64
		maxTPM     float64

		recoveryRate float64

		onBackoff func(newTPM float64)
		onProbe   func(newTPM float64)
	}

	limitedClient struct {
		next    model.Client
		limiter *AdaptiveRateLimiter
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

// NewAdaptiveRateLimiter constructs an AdaptiveRateLimiter with a
// tokens-per-minute budget. When m and key are set, it coordinates capacity
// across processes using a Pulse replicated map; otherwise it operates as a
// process-local limiter.
func NewAdaptiveRateLimiter(ctx context.Context, m *rmap.Map, key string, initialTPM, maxTPM float64) *AdaptiveRateLimiter {
	var cm clusterMap
	if m != nil {
		cm = &rmapClusterMap{m: m}
	}
	return newClusterAdaptiveRateLimiter(ctx, cm, key, initialTPM, maxTPM)
}

// newAdaptiveRateLimiter constructs an AdaptiveRateLimiter configured with an
// initial tokens-per-minute budget and an upper bound. The limiter uses a
// simple AIMD strategy and is used internally by the cluster-aware
// constructor.
//
// initialTPM and maxTPM are expressed in tokens per minute. When maxTPM is
// zero or less than initialTPM, it is clamped to initialTPM.
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
// tokens-per-minute limit for both Complete and Stream calls.
func (l *AdaptiveRateLimiter) Middleware() func(model.Client) model.Client {
	return func(next model.Client) model.Client {
		if next == nil {
			return nil
		}
		return &limitedClient{
			next:    next,
			limiter: l,
		}
	}
}

// Complete enforces the limiter before delegating to the underlying client.
func (c *limitedClient) Complete(ctx context.Context, req *model.Request) (*model.Response, error) {
	if err := c.limiter.wait(ctx, req); err != nil {
		return nil, err
	}
	resp, err := c.next.Complete(ctx, req)
	c.limiter.observe(err)
	return resp, err
}

// Stream enforces the limiter before delegating to the underlying client.
func (c *limitedClient) Stream(ctx context.Context, req *model.Request) (model.Streamer, error) {
	if err := c.limiter.wait(ctx, req); err != nil {
		return nil, err
	}
	stream, err := c.next.Stream(ctx, req)
	c.limiter.observe(err)
	return stream, err
}

func (l *AdaptiveRateLimiter) wait(ctx context.Context, req *model.Request) error {
	tokens := estimateTokens(req)
	return l.limiter.WaitN(ctx, tokens)
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

// estimateTokens computes a cheap heuristic for the number of tokens in the
// request transcript. It counts characters in text and string tool results,
// converts them to tokens using a fixed ratio, and adds a small buffer for
// system prompts and provider overhead.
func estimateTokens(req *model.Request) int {
	charCount := 0
	for _, m := range req.Messages {
		for _, p := range m.Parts {
			switch v := p.(type) {
			case model.TextPart:
				if v.Text != "" {
					charCount += len(v.Text)
				}
			case model.ToolResultPart:
				if s, ok := v.Content.(string); ok && s != "" {
					charCount += len(s)
				}
			}
		}
	}
	if charCount <= 0 {
		// Minimal non-zero estimate so callers still incur limiter costs even
		// when messages are extremely small.
		return 500
	}
	// Approximate 1 token per ~3 characters, then add a fixed buffer for
	// system prompts and provider framing.
	tokens := charCount / 3
	if tokens < 1 {
		tokens = 1
	}
	return tokens + 500
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
