package runtime

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/engine"
)

type heartbeatRecorder struct {
	mu    sync.Mutex
	count int
}

func (r *heartbeatRecorder) RecordHeartbeat(details ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.count++
}

func (r *heartbeatRecorder) Count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.count
}

func TestStartActivityHeartbeatRecordsHeartbeatsWhenHeartbeatTimeoutConfigured(t *testing.T) {
	t.Parallel()

	recorder := &heartbeatRecorder{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ctx = engine.WithActivityHeartbeatRecorder(ctx, recorder)
	ctx = engine.WithActivityHeartbeatTimeout(ctx, 3*time.Millisecond)
	stop := startActivityHeartbeat(ctx)
	defer stop()

	require.Eventually(t, func() bool {
		return recorder.Count() >= 1
	}, 100*time.Millisecond, 5*time.Millisecond)
}

func TestStartActivityHeartbeatRecordsHeartbeatsWithoutHeartbeatTimeout(t *testing.T) {
	t.Parallel()

	recorder := &heartbeatRecorder{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ctx = engine.WithActivityHeartbeatRecorder(ctx, recorder)
	stop := startActivityHeartbeatWithInterval(ctx, time.Millisecond)
	defer stop()

	require.Eventually(t, func() bool {
		return recorder.Count() >= 2
	}, time.Second, time.Millisecond)
}

func TestStartActivityHeartbeatWithoutRecorder(t *testing.T) {
	t.Parallel()
	stop := startActivityHeartbeat(t.Context())
	stop()
	stop()
}

func TestHeartbeatIntervalPreservesOptionalFailureDeadline(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		timeout time.Duration
		want    time.Duration
	}{
		{name: "no failure deadline", want: 5 * time.Second},
		{name: "short deadline", timeout: 3 * time.Second, want: time.Second},
		{name: "at cadence ceiling", timeout: 15 * time.Second, want: 5 * time.Second},
		{name: "long deadline", timeout: time.Minute, want: 5 * time.Second},
	} {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.want, heartbeatInterval(test.timeout))
		})
	}
}
