package middleware

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/pulse/rmap"
)

type fakeClusterMap struct {
	mu      sync.Mutex
	values  map[string]string
	ch      chan rmap.EventKind
	seedErr error
}

func newFakeClusterMap() *fakeClusterMap {
	return &fakeClusterMap{
		values: make(map[string]string),
		ch:     make(chan rmap.EventKind, 1),
	}
}

func (m *fakeClusterMap) Get(key string) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.values[key]
	return v, ok
}

func (m *fakeClusterMap) SetIfNotExists(_ context.Context, key, value string) (bool, error) {
	if m.seedErr != nil {
		return false, m.seedErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.values[key]; ok {
		return false, nil
	}
	m.values[key] = value
	select {
	case m.ch <- rmap.EventChange:
	default:
	}
	return true, nil
}

func TestClusterLimiterRejectsUnavailableOrInvalidCapacity(t *testing.T) {
	m := newFakeClusterMap()
	m.seedErr = errors.New("shared map unavailable")
	limiter, err := newClusterAdaptiveRateLimiter(t.Context(), m, "model", 100, 100)
	if limiter != nil || !errors.Is(err, m.seedErr) {
		t.Fatalf("expected shared map error, got limiter=%v error=%v", limiter, err)
	}
	m.seedErr = nil
	m.values["model"] = "invalid"
	limiter, err = newClusterAdaptiveRateLimiter(t.Context(), m, "model", 100, 100)
	if limiter != nil || err == nil {
		t.Fatalf("expected invalid shared capacity error, got limiter=%v error=%v", limiter, err)
	}
}

func (m *fakeClusterMap) TestAndSet(_ context.Context, key, test, value string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.values[key]
	if !ok || cur != test {
		return cur, nil
	}
	m.values[key] = value
	select {
	case m.ch <- rmap.EventChange:
	default:
	}
	return cur, nil
}

func (m *fakeClusterMap) Subscribe() <-chan rmap.EventKind {
	return m.ch
}

func TestClusterLimiter_BackoffUpdatesSharedMap(t *testing.T) {
	t.Helper()

	ctx := context.Background()
	m := newFakeClusterMap()
	const key = "model"

	// Seed map with initial value.
	m.values[key] = strconv.Itoa(80000)

	lim, err := newClusterAdaptiveRateLimiter(ctx, m, key, 80000, 80000)
	if err != nil {
		t.Fatal(err)
	}

	client := &fakeClient{
		completeErr: model.ErrRateLimited,
	}
	wrapped := limitedTestClient(t, lim, client)

	req := model.Request{
		Messages: []*model.Message{
			{
				Role: model.ConversationRoleUser,
				Parts: []model.Part{
					model.TextPart{Text: "hello"},
				},
			},
		},
		MaxTokens: 10,
	}

	_, _ = wrapped.Complete(context.Background(), &req)

	// Allow background callback to run.
	time.Sleep(10 * time.Millisecond)

	v, ok := m.Get(key)
	if !ok {
		t.Fatal("expected key to exist in cluster map")
	}
	cur, err := strconv.Atoi(v)
	if err != nil {
		t.Fatalf("invalid value in cluster map: %v", err)
	}
	if cur >= 80000 {
		t.Fatalf("expected shared TPM to decrease, got %d", cur)
	}
}
