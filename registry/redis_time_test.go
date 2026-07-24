package registry

// redis_time_test.go provides a deterministic Redis-time substitute for unit
// tests. Integration tests use the real Redis TIME implementation.

import (
	"context"
	"sync"
	"time"
)

type testTimeSource struct {
	mu  sync.RWMutex
	now time.Time
	err error
}

func newTestTimeSource(now time.Time) *testTimeSource {
	return &testTimeSource{now: now}
}

func (s *testTimeSource) Now(context.Context) (time.Time, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.now, s.err
}

func (s *testTimeSource) Set(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.now = now
}
