package runtime

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGenerateDeterministicToolCallID_UniqueAcrossAttempts(t *testing.T) {
	id1 := generateDeterministicToolCallID("run-1", "turn-1", 1, "atlas.read.get_time_series", 0)
	id2 := generateDeterministicToolCallID("run-1", "turn-1", 2, "atlas.read.get_time_series", 0)
	require.NotEqual(t, id1, id2)
}

func TestGenerateDeterministicToolCallID_DeterministicForSameInputs(t *testing.T) {
	id1 := generateDeterministicToolCallID("run-1", "turn-1", 3, "atlas.read.get_time_series", 7)
	id2 := generateDeterministicToolCallID("run-1", "turn-1", 3, "atlas.read.get_time_series", 7)
	require.Equal(t, id1, id2)
}
