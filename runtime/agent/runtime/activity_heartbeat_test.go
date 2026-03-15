package runtime

import (
	"context"
	"sync"
	"testing"
	"time"

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

func TestStartActivityHeartbeatSkipsContextsWithoutHeartbeatTimeout(t *testing.T) {
	t.Parallel()

	recorder := &heartbeatRecorder{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ctx = engine.WithActivityHeartbeatRecorder(ctx, recorder)
	stop := startActivityHeartbeatWithInterval(ctx, time.Millisecond)
	defer stop()

	time.Sleep(10 * time.Millisecond)
	require.Zero(t, recorder.Count())
}
