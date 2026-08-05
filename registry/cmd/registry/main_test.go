// Command registry configuration tests verify that invalid process
// configuration fails before the service connects to Redis.
package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnvDurationOrError(t *testing.T) {
	for _, test := range []struct {
		name      string
		value     string
		want      time.Duration
		wantError string
	}{
		{name: "empty uses default", want: time.Minute},
		{name: "valid duration", value: "45s", want: 45 * time.Second},
		{name: "malformed duration", value: "later", wantError: "parse TOOL_EXECUTION_TIMEOUT"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("TOOL_EXECUTION_TIMEOUT", test.value)
			duration, err := envDurationOrError("TOOL_EXECUTION_TIMEOUT", time.Minute)
			if test.wantError != "" {
				require.ErrorContains(t, err, test.wantError)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, test.want, duration)
		})
	}
}

func TestRunRejectsMalformedExecutionTimeoutBeforeConnecting(t *testing.T) {
	t.Setenv("TOOL_EXECUTION_TIMEOUT", "later")
	t.Setenv("REDIS_URL", "unreachable.invalid:6379")

	require.ErrorContains(t, run(), "parse TOOL_EXECUTION_TIMEOUT")
}
