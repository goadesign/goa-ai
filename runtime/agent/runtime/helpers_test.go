package runtime

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"goa.design/goa-ai/runtime/toolregistry"
)

func TestGenerateDeterministicToolCallIDPreservesReadableID(t *testing.T) {
	got := generateDeterministicToolCallID("run-1", "turn-1", 3, "svc.read.get_time_series", 7)

	assert.Equal(t, "run-1/turn-1/attempt-3/svc-read-get_time_series/7", got)
}

func TestGenerateDeterministicToolCallIDUniqueAcrossAttempts(t *testing.T) {
	id1 := generateDeterministicToolCallID("run-1", "turn-1", 1, "svc.read.get_time_series", 0)
	id2 := generateDeterministicToolCallID("run-1", "turn-1", 2, "svc.read.get_time_series", 0)
	assert.NotEqual(t, id1, id2)
}

func TestGenerateDeterministicToolCallIDDeterministicForSameInputs(t *testing.T) {
	id1 := generateDeterministicToolCallID("run-1", "turn-1", 3, "svc.read.get_time_series", 7)
	id2 := generateDeterministicToolCallID("run-1", "turn-1", 3, "svc.read.get_time_series", 7)
	assert.Equal(t, id1, id2)
}

func TestGenerateDeterministicToolCallIDBoundsAtRegistryLimit(t *testing.T) {
	const suffix = "/turn/attempt-1/tool/0"
	runID := strings.Repeat("r", toolregistry.MaxToolCallMetaIDLength-len(suffix))

	atLimit := generateDeterministicToolCallID(runID, "turn", 1, "tool", 0)
	overLimit := generateDeterministicToolCallID(runID+"r", "turn", 1, "tool", 0)

	assert.Len(t, atLimit, toolregistry.MaxToolCallMetaIDLength)
	assert.Equal(t, runID+suffix, atLimit)
	assert.Regexp(t, `^call-[0-9a-f]{64}$`, overLimit)
}

func TestGenerateDeterministicToolCallIDBoundsNestedIDs(t *testing.T) {
	runID := strings.Repeat("parent/agent/atlas-data-agent/call-0123456789abcdef/", 4)
	id := generateDeterministicToolCallID(runID, runID, 3, "atlas.read.list_app_runtime_capability_change_events", 7)
	replayed := generateDeterministicToolCallID(runID, runID, 3, "atlas.read.list_app_runtime_capability_change_events", 7)
	nextAttempt := generateDeterministicToolCallID(runID, runID, 4, "atlas.read.list_app_runtime_capability_change_events", 7)
	nextIndex := generateDeterministicToolCallID(runID, runID, 3, "atlas.read.list_app_runtime_capability_change_events", 8)

	assert.LessOrEqual(t, len(id), toolregistry.MaxToolCallMetaIDLength)
	assert.Regexp(t, `^call-[0-9a-f]{64}$`, id)
	assert.Equal(t, id, replayed)
	assert.NotEqual(t, id, nextAttempt)
	assert.NotEqual(t, id, nextIndex)
}
