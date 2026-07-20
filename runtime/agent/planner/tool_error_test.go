package planner

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRetryHintAllowsRetry(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		hint   *RetryHint
		allows bool
	}{
		{name: "absent", hint: nil, allows: false},
		{name: "invalid arguments", hint: &RetryHint{Reason: RetryReasonInvalidArguments}, allows: true},
		{name: "missing fields", hint: &RetryHint{Reason: RetryReasonMissingFields}, allows: true},
		{name: "malformed response", hint: &RetryHint{Reason: RetryReasonMalformedResponse}, allows: true},
		{name: "rate limited", hint: &RetryHint{Reason: RetryReasonRateLimited}, allows: true},
		{name: "tool unavailable", hint: &RetryHint{Reason: RetryReasonToolUnavailable}, allows: true},
		{name: "timeout", hint: &RetryHint{Reason: RetryReasonTimeout}, allows: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.allows, tt.hint.AllowsRetry())
		})
	}
}
