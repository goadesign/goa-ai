package runtime

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"goa.design/goa-ai/runtime/toolregistry"
)

func TestGenerateDeterministicToolCallIDUsesBoundedOpaqueEncoding(t *testing.T) {
	got := generateDeterministicToolCallID("run-1", "turn-1", 3, "svc.read.get_time_series", 7)

	assert.Regexp(t, `^[0-9a-f]{64}$`, got)
	assert.Len(t, got, 64)
	// Keep every digest byte and the existing identity preimage unchanged.
	assert.Equal(t, "0a1336094d85795896aa1cdc306a74fcf3159ebdf294fcac582212734dc66e6c", got)
	assert.LessOrEqual(t, len(got), toolregistry.MaxToolCallMetaIDLength)
}

func TestGenerateDeterministicToolCallIDChangesWithEveryComponent(t *testing.T) {
	base := generateDeterministicToolCallID("run-1", "turn-1", 1, "svc.read.get_time_series", 0)
	changed := []string{
		generateDeterministicToolCallID("run-2", "turn-1", 1, "svc.read.get_time_series", 0),
		generateDeterministicToolCallID("run-1", "turn-2", 1, "svc.read.get_time_series", 0),
		generateDeterministicToolCallID("run-1", "turn-1", 2, "svc.read.get_time_series", 0),
		generateDeterministicToolCallID("run-1", "turn-1", 1, "svc.read.get_time_series", 1),
		generateDeterministicToolCallID("run-1", "turn-1", 1, "svc.read.get_time_range", 0),
	}
	for _, id := range changed {
		assert.NotEqual(t, base, id)
	}
}

func TestGenerateDeterministicToolCallIDDeterministicForSameInputs(t *testing.T) {
	id1 := generateDeterministicToolCallID("run-1", "turn-1", 3, "svc.read.get_time_series", 7)
	id2 := generateDeterministicToolCallID("run-1", "turn-1", 3, "svc.read.get_time_series", 7)
	assert.Equal(t, id1, id2)
}

func TestGenerateDeterministicToolCallIDSeparatorsAndDotsAreInjective(t *testing.T) {
	assert.NotEqual(
		t,
		generateDeterministicToolCallID("run/a", "turn", 1, "svc.tool", 0),
		generateDeterministicToolCallID("run", "a/turn", 1, "svc.tool", 0),
	)
	assert.NotEqual(
		t,
		generateDeterministicToolCallID("run", "turn", 1, "svc.a-b", 0),
		generateDeterministicToolCallID("run", "turn", 1, "svc.a.b", 0),
	)
}

func TestGenerateDeterministicToolCallIDBoundsNestedIDs(t *testing.T) {
	runID := strings.Repeat("parent/agent/catalog-agent/call-0123456789abcdef/", 4)
	id := generateDeterministicToolCallID(runID, runID, 3, "catalog.lookup.list_workspace_change_events", 7)
	replayed := generateDeterministicToolCallID(runID, runID, 3, "catalog.lookup.list_workspace_change_events", 7)
	nextAttempt := generateDeterministicToolCallID(runID, runID, 4, "catalog.lookup.list_workspace_change_events", 7)
	nextIndex := generateDeterministicToolCallID(runID, runID, 3, "catalog.lookup.list_workspace_change_events", 8)

	assert.LessOrEqual(t, len(id), toolregistry.MaxToolCallMetaIDLength)
	assert.Regexp(t, `^[0-9a-f]{64}$`, id)
	assert.Len(t, id, 64)
	assert.Equal(t, id, replayed)
	assert.NotEqual(t, id, nextAttempt)
	assert.NotEqual(t, id, nextIndex)
}

func TestNestedRunIDForToolCallKeepsExactRuntimeID(t *testing.T) {
	callID := generateDeterministicToolCallID("parent", "turn", 1, "svc.agent", 0)
	otherCallID := generateDeterministicToolCallID("parent", "turn", 1, "svc.agent", 1)

	nested := NestedRunIDForToolCall("parent", "svc.agent", callID)
	assert.Equal(t, "parent/agent/svc.agent/"+callID, nested)
	assert.NotEqual(t, nested, NestedRunIDForToolCall("parent", "svc.agent", otherCallID))
}
