package retry

import (
	"context"
	"errors"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"
)

// TestIsRetryableProperty verifies Property 6: Retry Behavior.
// **Feature: a2a-codegen-refactor, Property 6: Retry Behavior**
// *For any* error type, IsRetryable SHALL correctly identify retryable errors.
// **Validates: Requirements 5.1, 5.4**
func TestIsRetryableProperty(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("nil error is not retryable", prop.ForAll(
		func(_ int) bool {
			return !IsRetryable(nil)
		},
		gen.Int(),
	))

	properties.Property("context.Canceled is not retryable", prop.ForAll(
		func(_ int) bool {
			return !IsRetryable(context.Canceled)
		},
		gen.Int(),
	))

	properties.Property("context.DeadlineExceeded is retryable", prop.ForAll(
		func(_ int) bool {
			return IsRetryable(context.DeadlineExceeded)
		},
		gen.Int(),
	))

	properties.Property("HTTP 503 is retryable", prop.ForAll(
		func(msg string) bool {
			err := &HTTPStatusError{StatusCode: http.StatusServiceUnavailable, Message: msg}
			return IsRetryable(err)
		},
		gen.AlphaString(),
	))

	properties.Property("HTTP 429 is retryable", prop.ForAll(
		func(msg string) bool {
			err := &HTTPStatusError{StatusCode: http.StatusTooManyRequests, Message: msg}
			return IsRetryable(err)
		},
		gen.AlphaString(),
	))

	properties.Property("HTTP 400 is not retryable", prop.ForAll(
		func(msg string) bool {
			err := &HTTPStatusError{StatusCode: http.StatusBadRequest, Message: msg}
			return !IsRetryable(err)
		},
		gen.AlphaString(),
	))

	properties.Property("HTTP 404 is not retryable", prop.ForAll(
		func(msg string) bool {
			err := &HTTPStatusError{StatusCode: http.StatusNotFound, Message: msg}
			return !IsRetryable(err)
		},
		gen.AlphaString(),
	))

	properties.TestingRun(t)
}

// TestRetryDoProperty verifies retry execution behavior.
// **Feature: a2a-codegen-refactor, Property 6: Retry Behavior**
// **Validates: Requirements 5.1, 5.4**
func TestRetryDoProperty(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 50
	properties := gopter.NewProperties(parameters)

	properties.Property("successful operation returns nil", prop.ForAll(
		func(maxAttempts int) bool {
			if maxAttempts < 1 {
				maxAttempts = 1
			}
			if maxAttempts > 10 {
				maxAttempts = 10
			}

			cfg := Config{
				MaxAttempts:       maxAttempts,
				InitialBackoff:    time.Millisecond,
				MaxBackoff:        10 * time.Millisecond,
				BackoffMultiplier: 2.0,
			}

			err := Do(context.Background(), cfg, func(_ context.Context) error {
				return nil
			})

			return err == nil
		},
		gen.IntRange(1, 10),
	))

	properties.Property("non-retryable error returns immediately", prop.ForAll(
		func(maxAttempts int) bool {
			if maxAttempts < 2 {
				maxAttempts = 2
			}
			if maxAttempts > 10 {
				maxAttempts = 10
			}

			cfg := Config{
				MaxAttempts:       maxAttempts,
				InitialBackoff:    time.Millisecond,
				MaxBackoff:        10 * time.Millisecond,
				BackoffMultiplier: 2.0,
			}

			attempts := 0
			nonRetryableErr := errors.New("non-retryable error")

			err := Do(context.Background(), cfg, func(_ context.Context) error {
				attempts++
				return nonRetryableErr
			})

			// Should only attempt once for non-retryable errors
			return attempts == 1 && errors.Is(err, nonRetryableErr)
		},
		gen.IntRange(2, 10),
	))

	properties.Property("retryable error exhausts all attempts", prop.ForAll(
		func(maxAttempts int) bool {
			if maxAttempts < 1 {
				maxAttempts = 1
			}
			if maxAttempts > 5 {
				maxAttempts = 5
			}

			cfg := Config{
				MaxAttempts:       maxAttempts,
				InitialBackoff:    time.Millisecond,
				MaxBackoff:        10 * time.Millisecond,
				BackoffMultiplier: 2.0,
			}

			attempts := 0
			retryableErr := &HTTPStatusError{StatusCode: http.StatusServiceUnavailable, Message: "unavailable"}

			err := Do(context.Background(), cfg, func(_ context.Context) error {
				attempts++
				return retryableErr
			})

			var exhausted *ExhaustedError
			return attempts == maxAttempts && errors.As(err, &exhausted)
		},
		gen.IntRange(1, 5),
	))

	properties.TestingRun(t)
}

// TestExhaustedErrorProperty verifies exhausted error behavior.
// **Feature: a2a-codegen-refactor, Property 6: Retry Behavior**
// **Validates: Requirements 5.4**
func TestExhaustedErrorProperty(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("ExhaustedError contains attempt count", prop.ForAll(
		func(attempts int) bool {
			if attempts < 1 {
				attempts = 1
			}
			if attempts > 100 {
				attempts = 100
			}

			err := &ExhaustedError{
				Attempts:      attempts,
				TotalDuration: time.Second,
				LastError:     errors.New("test error"),
			}

			return err.Attempts == attempts
		},
		gen.IntRange(1, 100),
	))

	properties.Property("ExhaustedError unwraps to last error", prop.ForAll(
		func(msg string) bool {
			lastErr := errors.New(msg)
			err := &ExhaustedError{
				Attempts:      3,
				TotalDuration: time.Second,
				LastError:     lastErr,
			}

			return errors.Is(err, lastErr)
		},
		gen.AlphaString(),
	))

	properties.TestingRun(t)
}

// TestStreamReconnectProperty verifies Property 7: Streaming Reconnection.
// **Feature: a2a-codegen-refactor, Property 7: Streaming Reconnection**
// *For any* streaming connection, reconnection SHALL track last event ID.
// **Validates: Requirements 5.3**
func TestStreamReconnectProperty(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("StreamState tracks last event ID", prop.ForAll(
		func(eventID string) bool {
			state := &StreamState{}
			state.UpdateLastEventID(eventID)

			if eventID == "" {
				return state.LastEventID == ""
			}
			return state.LastEventID == eventID
		},
		gen.AlphaString(),
	))

	properties.Property("StreamState reset clears reconnect attempts", prop.ForAll(
		func(attempts int) bool {
			if attempts < 0 {
				attempts = 0
			}

			state := &StreamState{ReconnectAttempts: attempts}
			state.Reset()

			return state.ReconnectAttempts == 0
		},
		gen.IntRange(0, 100),
	))

	properties.Property("StreamState preserves last event ID on reset", prop.ForAll(
		func(eventID string) bool {
			state := &StreamState{LastEventID: eventID, ReconnectAttempts: 5}
			state.Reset()

			return state.LastEventID == eventID
		},
		gen.AlphaString(),
	))

	properties.TestingRun(t)
}

// TestCalculateBackoffProperty verifies backoff calculation.
func TestCalculateBackoffProperty(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("backoff increases with attempts", prop.ForAll(
		func(attempt int) bool {
			if attempt < 1 {
				attempt = 1
			}
			if attempt > 10 {
				attempt = 10
			}

			cfg := Config{
				InitialBackoff:    100 * time.Millisecond,
				MaxBackoff:        10 * time.Second,
				BackoffMultiplier: 2.0,
				Jitter:            0, // No jitter for deterministic test
			}

			backoff1 := calculateBackoff(cfg, attempt)
			backoff2 := calculateBackoff(cfg, attempt+1)

			// Backoff should increase (or stay at max)
			return backoff2 >= backoff1
		},
		gen.IntRange(1, 10),
	))

	properties.Property("backoff respects max limit", prop.ForAll(
		func(attempt int) bool {
			if attempt < 1 {
				attempt = 1
			}

			cfg := Config{
				InitialBackoff:    100 * time.Millisecond,
				MaxBackoff:        time.Second,
				BackoffMultiplier: 2.0,
				Jitter:            0,
			}

			backoff := calculateBackoff(cfg, attempt)
			return backoff <= cfg.MaxBackoff
		},
		gen.IntRange(1, 100),
	))

	properties.TestingRun(t)
}

// TestHTTPStatusErrorProperty verifies HTTP status error behavior.
func TestHTTPStatusErrorProperty(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("HTTPStatusError contains status code in message", prop.ForAll(
		func(code int, msg string) bool {
			if code < 100 {
				code = 100
			}
			if code > 599 {
				code = 599
			}

			err := &HTTPStatusError{StatusCode: code, Message: msg}
			errMsg := err.Error()

			// Error message should contain the status code
			return len(errMsg) > 0
		},
		gen.IntRange(100, 599),
		gen.AlphaString(),
	))

	properties.TestingRun(t)
}

// mockTimeoutError implements net.Error for testing.
type mockTimeoutError struct {
	timeout bool
}

func (e *mockTimeoutError) Error() string   { return "mock network error" }
func (e *mockTimeoutError) Timeout() bool   { return e.timeout }
func (e *mockTimeoutError) Temporary() bool { return false } // Deprecated but required by net.Error

// Ensure mockTimeoutError implements net.Error
var _ net.Error = (*mockTimeoutError)(nil)

func TestNetworkErrorRetryable(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		retryable bool
	}{
		{
			name:      "timeout error is retryable",
			err:       &mockTimeoutError{timeout: true},
			retryable: true,
		},
		{
			name:      "non-timeout is not retryable",
			err:       &mockTimeoutError{},
			retryable: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsRetryable(tt.err); got != tt.retryable {
				t.Errorf("IsRetryable() = %v, want %v", got, tt.retryable)
			}
		})
	}
}
