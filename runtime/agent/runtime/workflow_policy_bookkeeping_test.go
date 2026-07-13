package runtime

// workflow_policy_bookkeeping_test.go verifies that tools declared `Bookkeeping`
// are exempt from the run-level MaxToolCalls retrieval budget. Bookkeeping
// calls do not decrement `RemainingToolCalls`; each provider response is still
// admitted or rejected as one atomic batch.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/policy"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"
)

// newRuntimeWithSpecs returns a minimal runtime preloaded with the given tool
// specs. It is suitable for direct unit tests of budget classification helpers.
func newRuntimeWithSpecs(specs ...tools.ToolSpec) *Runtime {
	rt := New(WithLogger(telemetry.NoopLogger{}))
	for _, spec := range specs {
		rt.toolSpecs[spec.Name] = spec
		rt.policyToolMetadata[spec.Name] = canonicalToolMetadata(spec, nil)
	}
	return rt
}

// newBookkeepingSpec returns a bookkeeping-tagged stub tool spec.
func newBookkeepingSpec(name tools.Ident) tools.ToolSpec {
	spec := newAnyJSONSpec(name, "tests.toolset")
	spec.Bookkeeping = true
	return spec
}

// newRetrievalSpec returns a retrieval (budgeted) stub tool spec.
func newRetrievalSpec(name tools.Ident) tools.ToolSpec {
	return newAnyJSONSpec(name, "tests.toolset")
}

// newTerminalSpec returns a normalized terminal stub tool spec.
func newTerminalSpec(name tools.Ident) tools.ToolSpec {
	spec := newAnyJSONSpec(name, "tests.toolset")
	spec.TerminalRun = true
	spec.Bookkeeping = true
	return spec
}

// newInvalidTerminalSpec returns a terminal stub tool spec that violates the
// runtime contract and should be rejected at registration time.
func newInvalidTerminalSpec(name tools.Ident) tools.ToolSpec {
	spec := newAnyJSONSpec(name, "tests.toolset")
	spec.TerminalRun = true
	return spec
}

func TestAdmitToolBatchReportsBudgetCost(t *testing.T) {
	rt := newRuntimeWithSpecs(
		newRetrievalSpec("ret.a"),
		newBookkeepingSpec("book.a"),
		newTerminalSpec("term.a"),
		newRetrievalSpec("ret.b"),
		newBookkeepingSpec("book.b"),
	)

	calls := []planner.ToolRequest{
		{Name: "ret.a"},
		{Name: "book.a"},
		{Name: "term.a"},
		{Name: "ret.b"},
		{Name: "book.b"},
	}

	cost, admitted := rt.admitToolBatch(calls, policy.CapsState{})
	assert.Equal(t, 2, cost)
	assert.True(t, admitted)
	assert.True(t, rt.isBookkeeping("book.a"))
	assert.True(t, rt.isBookkeeping("term.a"))
	assert.False(t, rt.isBookkeeping("ret.a"))
	assert.False(t, rt.isBookkeeping("unknown"), "unknown tools are treated as budgeted")
}

func TestToolMetadataIncludesBudgetClass(t *testing.T) {
	rt := newRuntimeWithSpecs(
		newRetrievalSpec("ret.a"),
		newBookkeepingSpec("book.a"),
		newTerminalSpec("term.a"),
	)

	metas := rt.toolMetadata([]planner.ToolRequest{
		{Name: "ret.a"},
		{Name: "book.a"},
		{Name: "term.a"},
		{Name: "unknown"},
	})
	assert.Equal(t, []policy.ToolMetadata{
		{ID: "ret.a", Title: "A", BudgetClass: policy.ToolBudgetClassBudgeted},
		{ID: "book.a", Title: "A", BudgetClass: policy.ToolBudgetClassBookkeeping},
		{ID: "term.a", Title: "A", BudgetClass: policy.ToolBudgetClassBookkeeping},
		{ID: "unknown", Title: "Unknown", BudgetClass: policy.ToolBudgetClassBudgeted},
	}, metas)
}

func TestAdmitToolBatchBookkeepingDoesNotConsumeBudget(t *testing.T) {
	rt := newRuntimeWithSpecs(
		newRetrievalSpec("ret.a"),
		newRetrievalSpec("ret.b"),
		newRetrievalSpec("ret.c"),
		newBookkeepingSpec("book.a"),
		newBookkeepingSpec("book.b"),
	)

	cases := []struct {
		name         string
		calls        []planner.ToolRequest
		caps         policy.CapsState
		expectedCost int
		admitted     bool
	}{
		{
			name: "no budget set: passes everything through",
			calls: []planner.ToolRequest{
				{Name: "ret.a"}, {Name: "ret.b"}, {Name: "book.a"},
			},
			caps:         policy.CapsState{MaxToolCalls: 0, RemainingToolCalls: 0},
			expectedCost: 2,
			admitted:     true,
		},
		{
			name: "budget=0, mixed batch: rejects response atomically",
			calls: []planner.ToolRequest{
				{Name: "ret.a"}, {Name: "book.a"}, {Name: "ret.b"}, {Name: "book.b"},
			},
			caps:         policy.CapsState{MaxToolCalls: 10, RemainingToolCalls: 0},
			expectedCost: 2,
		},
		{
			name: "budget=1, three retrieval: rejects response atomically",
			calls: []planner.ToolRequest{
				{Name: "ret.a"}, {Name: "ret.b"}, {Name: "ret.c"},
			},
			caps:         policy.CapsState{MaxToolCalls: 10, RemainingToolCalls: 1},
			expectedCost: 3,
		},
		{
			name: "budget=1, mixed: bookkeeping does not reduce batch cost",
			calls: []planner.ToolRequest{
				{Name: "ret.a"}, {Name: "book.a"}, {Name: "ret.b"}, {Name: "book.b"}, {Name: "ret.c"},
			},
			caps:         policy.CapsState{MaxToolCalls: 10, RemainingToolCalls: 1},
			expectedCost: 3,
		},
		{
			name: "budget=0, all bookkeeping: passes all through",
			calls: []planner.ToolRequest{
				{Name: "book.a"}, {Name: "book.b"},
			},
			caps:         policy.CapsState{MaxToolCalls: 10, RemainingToolCalls: 0},
			expectedCost: 0,
			admitted:     true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cost, admitted := rt.admitToolBatch(tc.calls, tc.caps)
			assert.Equal(t, tc.expectedCost, cost)
			assert.Equal(t, tc.admitted, admitted)
		})
	}
}

func TestAdmitToolBatchTerminalRunDoesNotConsumeBudget(t *testing.T) {
	rt := newRuntimeWithSpecs(
		newTerminalSpec("term.a"),
		newRetrievalSpec("ret.a"),
	)

	cost, admitted := rt.admitToolBatch(
		[]planner.ToolRequest{
			{Name: "ret.a"},
			{Name: "term.a"},
		},
		policy.CapsState{MaxToolCalls: 10, RemainingToolCalls: 0},
	)
	assert.Equal(t, 1, cost)
	assert.False(t, admitted)

	cost, admitted = rt.admitToolBatch(
		[]planner.ToolRequest{{Name: "term.a"}},
		policy.CapsState{MaxToolCalls: 10, RemainingToolCalls: 0},
	)
	assert.Zero(t, cost)
	assert.True(t, admitted)
}

func TestRegisterToolset_RejectsTerminalSpecWithoutBookkeeping(t *testing.T) {
	rt := New(WithLogger(telemetry.NoopLogger{}))
	err := rt.RegisterToolset(ToolsetRegistration{
		Name: "tasks.progress",
		Execute: wrapExecute(func(ctx context.Context, call *planner.ToolRequest) (*planner.ToolResult, error) {
			return &planner.ToolResult{Name: call.Name}, nil
		}),
		Specs: []tools.ToolSpec{newInvalidTerminalSpec("tasks.complete")},
	})
	require.ErrorIs(t, err, ErrInvalidConfig)
	require.ErrorContains(t, err, "terminal tool \"tasks.complete\" must also declare bookkeeping")
}
