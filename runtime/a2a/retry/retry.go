// Package retry provides retry utilities for A2A client operations.
// It includes exponential backoff, retryable error detection, and
// streaming reconnection support.
package retry

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"net"
	"net/http"
	"time"
)

// Config configures retry behavior for A2A operations.
type Config struct {
	// MaxAttempts is the maximum number of attempts (including the initial attempt).
	// A value of 0 or 1 means no retries.
	MaxAttempts int
	// InitialBackoff is the initial delay before the first retry.
	InitialBackoff time.Duration
	// MaxBackoff is the maximum delay between retries.
	MaxBackoff time.Duration
	// BackoffMultiplier is the factor by which the backoff increases after each retry.
	// A value of 2.0 provides exponential backoff.
	BackoffMultiplier float64
	// Jitter adds randomness to the backoff to prevent thundering herd.
	// A value of 0.1 adds up to 10% jitter.
	Jitter float64
}

// DefaultConfig returns a sensible default retry configuration.
func DefaultConfig() Config {
	return Config{
		MaxAttempts:       3,
		InitialBackoff:    100 * time.Millisecond,
		MaxBackoff:        10 * time.Second,
		BackoffMultiplier: 2.0,
		Jitter:            0.1,
	}
}

// ExhaustedError is returned when all retry attempts have been exhausted.
type ExhaustedError struct {
	// Attempts is the number of attempts made.
	Attempts int
	// TotalDuration is the total time spent retrying.
	TotalDuration time.Duration
	// LastError is the error from the last attempt.
	LastError error
}

// Error implements the error interface.
func (e *ExhaustedError) Error() string {
	return fmt.Sprintf("retry exhausted after %d attempts over %v: %v", e.Attempts, e.TotalDuration, e.LastError)
}

// Unwrap returns the underlying error.
func (e *ExhaustedError) Unwrap() error {
	return e.LastError
}

// IsRetryable determines if an error is retryable.
// Retryable errors include:
// - Network errors (connection refused, timeout, DNS errors)
// - HTTP 503 Service Unavailable
// - HTTP 429 Too Many Requests
// - Context deadline exceeded (but not context canceled)
func IsRetryable(err error) bool {
	if err == nil {
		return false
	}

	// Check for context errors
	if errors.Is(err, context.Canceled) {
		return false // User canceled, don't retry
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true // Timeout, may succeed on retry
	}

	// Check for network errors
	var netErr net.Error
	if errors.As(err, &netErr) {
		return netErr.Timeout()
	}

	// Check for DNS errors
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return dnsErr.Temporary()
	}

	// Check for HTTP status code errors
	var httpErr *HTTPStatusError
	if errors.As(err, &httpErr) {
		switch httpErr.StatusCode {
		case http.StatusServiceUnavailable, // 503
			http.StatusTooManyRequests, // 429
			http.StatusBadGateway,      // 502
			http.StatusGatewayTimeout:  // 504
			return true
		}
	}

	return false
}

// HTTPStatusError represents an HTTP error with a status code.
type HTTPStatusError struct {
	StatusCode int
	Message    string
}

// Error implements the error interface.
func (e *HTTPStatusError) Error() string {
	return fmt.Sprintf("HTTP %d: %s", e.StatusCode, e.Message)
}

// Do executes the given function with retry logic.
// The function is retried if it returns a retryable error.
func Do(ctx context.Context, cfg Config, fn func(ctx context.Context) error) error {
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = 1
	}

	start := time.Now()
	var lastErr error

	for attempt := 1; attempt <= cfg.MaxAttempts; attempt++ {
		err := fn(ctx)
		if err == nil {
			return nil
		}

		lastErr = err

		// Check if we should retry
		if !IsRetryable(err) {
			return err
		}

		// Check if we've exhausted attempts
		if attempt >= cfg.MaxAttempts {
			break
		}

		// Calculate backoff
		backoff := calculateBackoff(cfg, attempt)

		// Wait for backoff or context cancellation
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
	}

	return &ExhaustedError{
		Attempts:      cfg.MaxAttempts,
		TotalDuration: time.Since(start),
		LastError:     lastErr,
	}
}

// calculateBackoff computes the backoff duration for a given attempt.
func calculateBackoff(cfg Config, attempt int) time.Duration {
	// Exponential backoff: initial * multiplier^(attempt-1)
	backoff := float64(cfg.InitialBackoff) * math.Pow(cfg.BackoffMultiplier, float64(attempt-1))

	// Apply max backoff
	if backoff > float64(cfg.MaxBackoff) {
		backoff = float64(cfg.MaxBackoff)
	}

	// Apply jitter (using math/rand is acceptable for jitter as it doesn't need
	// cryptographic security, and the gosec warning is acknowledged)
	if cfg.Jitter > 0 {
		jitter := backoff * cfg.Jitter * (rand.Float64()*2 - 1) //nolint:gosec // jitter doesn't need crypto rand
		backoff += jitter
	}

	return time.Duration(backoff)
}

// StreamReconnectConfig configures reconnection behavior for streaming operations.
type StreamReconnectConfig struct {
	// Config is the base retry configuration.
	Config
	// TrackLastEventID enables tracking of the last event ID for resumption.
	TrackLastEventID bool
}

// DefaultStreamReconnectConfig returns a sensible default for streaming reconnection.
func DefaultStreamReconnectConfig() StreamReconnectConfig {
	return StreamReconnectConfig{
		Config: Config{
			MaxAttempts:       5,
			InitialBackoff:    500 * time.Millisecond,
			MaxBackoff:        30 * time.Second,
			BackoffMultiplier: 2.0,
			Jitter:            0.1,
		},
		TrackLastEventID: true,
	}
}

// StreamState tracks the state of a streaming connection for reconnection.
type StreamState struct {
	// LastEventID is the ID of the last successfully received event.
	LastEventID string
	// ReconnectAttempts is the number of reconnection attempts made.
	ReconnectAttempts int
}

// Reset resets the stream state after a successful connection.
func (s *StreamState) Reset() {
	s.ReconnectAttempts = 0
}

// UpdateLastEventID updates the last event ID.
func (s *StreamState) UpdateLastEventID(id string) {
	if id != "" {
		s.LastEventID = id
	}
}
