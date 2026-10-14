// Package runtime sends heartbeats while planner, tool, and preparation activities
// run so an engine can deliver cancellation to their contexts. A heartbeat failure
// deadline is optional; cancellation delivery does not depend on that deadline.
package runtime

import (
	"context"
	"sync"
	"time"

	"goa.design/goa-ai/runtime/agent/engine"
)

const defaultActivityHeartbeatInterval = 5 * time.Second

// startActivityHeartbeat sends heartbeats when the activity context has an
// engine recorder, then returns a function that stops further scheduling.
// Contexts without a recorder need no heartbeat loop.
func startActivityHeartbeat(ctx context.Context) func() {
	return startActivityHeartbeatWithInterval(ctx, 0)
}

// startActivityHeartbeatWithInterval exists so tests can exercise the heartbeat
// loop without waiting for the production interval. When interval is zero, the
// runtime derives a cadence from the configured heartbeat timeout.
func startActivityHeartbeatWithInterval(ctx context.Context, interval time.Duration) func() {
	timeout := engine.ActivityHeartbeatTimeout(ctx)
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

// heartbeatInterval schedules cancellation checks every five seconds when no
// heartbeat failure deadline is set. A positive deadline shortens that interval
// so the running activity can report before its attempt is considered unhealthy.
func heartbeatInterval(timeout time.Duration) time.Duration {
	if timeout <= 0 {
		return defaultActivityHeartbeatInterval
	}
	interval := timeout / 3
	if interval <= 0 {
		return time.Millisecond
	}
	if interval > defaultActivityHeartbeatInterval {
		return defaultActivityHeartbeatInterval
	}
	return interval
}
