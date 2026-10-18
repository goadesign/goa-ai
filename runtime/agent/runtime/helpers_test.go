package runtime

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"goa.design/goa-ai/runtime/agent/tools"
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

func TestNestedRunIDForToolCallUsesBoundedIdentity(t *testing.T) {
	assert.Equal(t, "52840d7da8caa2ca548065899c4e1d40a14cf7761a378b43f8d0fc694ab4b138", NestedRunIDForToolCall("parent", "svc.agent", "call-1"))

	tests := []struct {
		name   string
		parent string
		tool   tools.Ident
		call   string
	}{
		{name: "static", parent: "parent", tool: "svc.agent", call: "call-1"},
		{name: "registered", parent: "parent", tool: tools.Ident("registry." + strings.Repeat("catalog.", 80) + "inspect"), call: "call-1"},
		{name: "long inputs", parent: strings.Repeat("parent/", 80), tool: "svc.agent", call: strings.Repeat("call/", 80)},
		{name: "empty call", parent: "parent", tool: "svc.agent"},
		{name: "empty parent", tool: "svc.agent", call: "call-1"},
		{name: "unicode", parent: "注文", tool: "svc.agent", call: "éclair"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			id := NestedRunIDForToolCall(test.parent, test.tool, test.call)
			assert.Regexp(t, `^[0-9a-f]{64}$`, id)
			assert.Equal(t, id, NestedRunIDForToolCall(test.parent, test.tool, test.call))
		})
	}
	assert.Equal(t,
		NestedRunIDForToolCall("unknown", "svc.agent", "call-1"),
		NestedRunIDForToolCall("", "svc.agent", "call-1"),
	)
}

func TestNestedRunIDForToolCallUsesEveryCompleteComponent(t *testing.T) {
	base := NestedRunIDForToolCall("parent", "svc.agent", "call-1")
	tests := []struct {
		name   string
		parent string
		tool   tools.Ident
		call   string
	}{
		{name: "parent", parent: "other-parent", tool: "svc.agent", call: "call-1"},
		{name: "tool", parent: "parent", tool: "svc.other", call: "call-1"},
		{name: "call", parent: "parent", tool: "svc.agent", call: "call-2"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert.NotEqual(t, base, NestedRunIDForToolCall(test.parent, test.tool, test.call))
		})
	}
	assert.NotEqual(t,
		NestedRunIDForToolCall("parent/agent/svc.a", "svc.b", "call"),
		NestedRunIDForToolCall("parent", "svc.a/agent/svc.b", "call"),
	)
	assert.NotEqual(t,
		NestedRunIDForToolCall("parent", "svc.agent", "part/call"),
		NestedRunIDForToolCall("parent", "svc.agent/part", "call"),
	)
	prefix := strings.Repeat("component", 80)
	assert.NotEqual(t, NestedRunIDForToolCall(prefix+"a", "svc.agent", "call"), NestedRunIDForToolCall(prefix+"b", "svc.agent", "call"))
	assert.NotEqual(t, NestedRunIDForToolCall("parent", tools.Ident(prefix+"a"), "call"), NestedRunIDForToolCall("parent", tools.Ident(prefix+"b"), "call"))
	assert.NotEqual(t, NestedRunIDForToolCall("parent", "svc.agent", prefix+"a"), NestedRunIDForToolCall("parent", "svc.agent", prefix+"b"))
}

func TestNestedRunIDForToolCallBoundsRepeatedNesting(t *testing.T) {
	parent := "root"
	seen := map[string]bool{parent: true}
	for range 100 {
		child := NestedRunIDForToolCall(parent, "svc.agent", "call-1")
		assert.Regexp(t, `^[0-9a-f]{64}$`, child)
		assert.False(t, seen[child])
		seen[child] = true
		parent = child
	}
}

func TestNestedRunIDKeepsHierarchicalFormat(t *testing.T) {
	assert.Equal(t, "parent/agent/svc.agent", NestedRunID("parent", "svc.agent"))
	assert.Equal(t, "unknown/agent/svc.agent", NestedRunID("", "svc.agent"))
}
