package session

// This file checks the immutable run-start values accepted by every durable
// Store implementation.

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestValidateRunStartRequiresMillisecondPrecision(t *testing.T) {
	t.Parallel()

	start := RunStart{
		AgentID:   "service.agent",
		RunID:     "run-1",
		SessionID: "session-1",
		StartedAt: time.Date(2026, time.August, 29, 12, 0, 0, 1, time.UTC),
	}

	require.EqualError(t, ValidateRunStart(start, false), "started_at must use millisecond precision")
	start.StartedAt = start.StartedAt.Truncate(time.Millisecond)
	require.NoError(t, ValidateRunStart(start, false))

	start.PredecessorRunID = start.RunID
	require.EqualError(t, ValidateRunStart(start, false), "predecessor run id must differ from run id")
}
