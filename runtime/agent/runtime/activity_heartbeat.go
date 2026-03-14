// Package runtime emits cooperative activity heartbeats for long-running planner
// and tool activities so engine-driven cancellation reaches streaming provider
// calls promptly instead of after the activity returns on its own.
package runtime

import (
	"context"
	"sync"
	"time"

	"goa.design/goa-ai/runtime/agent/engine"
)

const defaultActivityHeartbeatInterval = 5 * time.Second

// startActivityHeartbeat begins a lightweight heartbeat loop for long-running
// activities. The loop only runs when the engine configured a heartbeat
// timeout for the current attempt; otherwise it returns a no-op stop function.
func startActivityHeartbeat(ctx context.Context) func() {
	return startActivityHeartbeatWithInterval(ctx, 0)
}

// startActivityHeartbeatWithInterval exists so tests can exercise the heartbeat
// loop without waiting for the production interval. When interval is zero, the
// runtime derives a cadence from the configured heartbeat timeout.
func startActivityHeartbeatWithInterval(ctx context.Context, interval time.Duration) func() {
	timeout := engine.ActivityHeartbeatTimeout(ctx)
	if timeout <= 0 {
		return func() {}
	}
	if interval <= 0 {
		interval = heartbeatInterval(timeout)
	}
	if !engine.RecordActivityHeartbeat(ctx) {
		return func() {}
	}

	stop := make(chan struct{})
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				engine.RecordActivityHeartbeat(ctx)
			}
		}
	}()

	var once sync.Once
	return func() {
		once.Do(func() {
			close(stop)
		})
	}
}

// heartbeatInterval derives a safe heartbeat cadence for a configured timeout.
// It keeps the existing 5s production ceiling while ensuring the loop runs
// comfortably before the deadline expires.
func heartbeatInterval(timeout time.Duration) time.Duration {
	interval := timeout / 3
	if interval <= 0 {
		return time.Millisecond
	}
	if interval > defaultActivityHeartbeatInterval {
		return defaultActivityHeartbeatInterval
	}
	return interval
}
