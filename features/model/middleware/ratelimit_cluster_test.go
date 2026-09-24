// These tests control replicated-map delivery separately from Redis writes.
// Limiter startup must wait for the stored capacity, including another writer's
// value, and release its subscription on failure or shutdown.
package middleware

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"testing/synctest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/pulse/rmap"
)

type fakeClusterMap struct {
	mu         sync.Mutex
	values     map[string]string
	ch         chan rmap.EventKind
	seed       func(string, string) (bool, error)
	subscribed bool
}

func TestClusterLimiterWaitsForReplicatedCapacity(t *testing.T) {
	for _, inserted := range []bool{true, false} {
		t.Run(strconv.FormatBool(inserted), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				m := newFakeClusterMap()
				m.seed = func(key, value string) (bool, error) {
					assert.True(t, m.subscribed, "subscribe before writing")
					assert.Equal(t, "model", key)
					assert.Equal(t, "100", value)
					return inserted, nil
				}
				done := make(chan *AdaptiveRateLimiter, 1)
				go func() {
					limiter, err := newClusterAdaptiveRateLimiter(ctx, m, "model", 100, 100)
					assert.NoError(t, err)
					done <- limiter
				}()
				synctest.Wait()
				require.Empty(t, done, "Redis acceptance is not local replication")
				m.publish("unrelated", "1")
				synctest.Wait()
				require.Empty(t, done, "unrelated updates must not complete startup")
				m.publish("model", "75")
				limiter := <-done
				require.NotNil(t, limiter)
				assert.InDelta(t, 75, limiter.currentTPM, 0)
				m.publish("model", "60")
				synctest.Wait()
				assert.InDelta(t, 60, limiter.currentTPM, 0)
				cancel()
				synctest.Wait()
				assert.False(t, m.subscribed)
			})
		})
	}
}

func TestClusterLimiterRejectsUnavailableOrInvalidCapacity(t *testing.T) {
	for _, test := range []struct {
		name  string
		seed  func(string, string) (bool, error)
		value string
	}{
		{name: "write failure", seed: func(string, string) (bool, error) { return false, errors.New("shared map unavailable") }},
		{name: "invalid value", value: "invalid"},
	} {
		t.Run(test.name, func(t *testing.T) {
			m := newFakeClusterMap()
			m.seed = test.seed
			if test.value != "" {
				m.values["model"] = test.value
			}
			limiter, err := newClusterAdaptiveRateLimiter(t.Context(), m, "model", 100, 100)
			assert.Nil(t, limiter)
			require.Error(t, err)
			assert.False(t, m.subscribed)
		})
	}
}

func TestClusterLimiterStopsWaitingForReplication(t *testing.T) {
	for _, stopMap := range []bool{false, true} {
		t.Run(strconv.FormatBool(stopMap), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				m := newFakeClusterMap()
				m.seed = func(string, string) (bool, error) { return true, nil }
				done := make(chan error, 1)
				go func() {
					limiter, err := newClusterAdaptiveRateLimiter(ctx, m, "model", 100, 100)
					assert.Nil(t, limiter)
					done <- err
				}()
				synctest.Wait()
				if stopMap {
					m.Unsubscribe(m.ch)
				} else {
					cancel()
				}
				err := <-done
				if stopMap {
					require.ErrorContains(t, err, "map stopped during initialization")
				} else {
					require.ErrorIs(t, err, context.Canceled)
				}
				assert.False(t, m.subscribed)
			})
		})
	}
}

func TestClusterLimiterRejectsStoppedMap(t *testing.T) {
	m := newFakeClusterMap()
	m.ch = nil
	limiter, err := newClusterAdaptiveRateLimiter(t.Context(), m, "model", 100, 100)
	assert.Nil(t, limiter)
	assert.ErrorContains(t, err, "map is stopped")
}

func TestClusterLimiterBackoffUpdatesSharedMap(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := newFakeClusterMap()
		m.values["model"] = "80000"
		lim, err := newClusterAdaptiveRateLimiter(t.Context(), m, "model", 80000, 80000)
		require.NoError(t, err)
		client := &fakeClient{completeErr: model.ErrRateLimited}
		wrapped := limitedTestClient(t, lim, client)
		req := model.Request{
			Messages: []*model.Message{{
				Role:  model.ConversationRoleUser,
				Parts: []model.Part{model.TextPart{Text: "hello"}},
			}},
			MaxTokens: 10,
		}
		_, err = wrapped.Complete(t.Context(), &req)
		require.ErrorIs(t, err, model.ErrRateLimited)
		synctest.Wait()
		v, ok := m.Get("model")
		require.True(t, ok)
		assert.Equal(t, "40000", v)
	})
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
	if m.seed != nil {
		return m.seed(key, value)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.values[key]; ok {
		return false, nil
	}
	m.publishLocked(key, value)
	return true, nil
}

func (m *fakeClusterMap) TestAndSet(_ context.Context, key, test, value string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.values[key]
	if !ok || cur != test {
		return cur, nil
	}
	m.publishLocked(key, value)
	return cur, nil
}

func (m *fakeClusterMap) Subscribe() <-chan rmap.EventKind {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.subscribed = m.ch != nil
	return m.ch
}

func (m *fakeClusterMap) Unsubscribe(ch <-chan rmap.EventKind) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.subscribed && m.ch == ch {
		m.subscribed = false
		close(m.ch)
	}
}

func (m *fakeClusterMap) publish(key, value string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.publishLocked(key, value)
}

func (m *fakeClusterMap) publishLocked(key, value string) {
	m.values[key] = value
	if m.subscribed {
		select {
		case m.ch <- rmap.EventChange:
		default:
		}
	}
}
