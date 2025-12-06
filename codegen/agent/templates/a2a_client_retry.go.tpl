// RetryConfig configures retry behavior for A2A client operations.
type RetryConfig struct {
	// MaxAttempts is the maximum number of attempts (including the initial attempt).
	MaxAttempts int
	// InitialBackoff is the initial delay before the first retry.
	InitialBackoff time.Duration
	// MaxBackoff is the maximum delay between retries.
	MaxBackoff time.Duration
	// BackoffMultiplier is the factor by which the backoff increases after each retry.
	BackoffMultiplier float64
}

// DefaultRetryConfig returns a sensible default retry configuration.
func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		MaxAttempts:       3,
		InitialBackoff:    100 * time.Millisecond,
		MaxBackoff:        10 * time.Second,
		BackoffMultiplier: 2.0,
	}
}

// WithRetryConfig sets the retry configuration for the client.
func WithRetryConfig(cfg RetryConfig) A2AClientOption {
	return func(c *A2AClient) {
		c.retryConfig = cfg
	}
}
