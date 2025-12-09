package types

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestTaskRoundTrip verifies that Task marshals and unmarshals without loss.
func TestTaskRoundTrip(t *testing.T) {
	orig := &Task{
		ID: "task-1",
		Status: &TaskStatus{
			State:     "completed",
			Timestamp: "2025-01-01T00:00:00Z",
		},
		Metadata: map[string]any{"k": "v"},
	}

	b, err := json.Marshal(orig)
	require.NoError(t, err)

	var decoded Task
	require.NoError(t, json.Unmarshal(b, &decoded))
	require.Equal(t, orig.ID, decoded.ID)
	require.NotNil(t, decoded.Status)
	require.Equal(t, orig.Status.State, decoded.Status.State)
}
