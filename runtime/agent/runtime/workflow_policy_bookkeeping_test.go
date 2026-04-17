package runtime

// workflow_policy_bookkeeping_test.go verifies that tools declared `Bookkeeping`
// are exempt from the run-level MaxToolCalls retrieval budget. Bookkeeping
// calls must never decrement `RemainingToolCalls` and must never be dropped by
// `capAllowedCalls` when the batch exceeds the remaining budget.

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

func TestCapAllowedCalls_ReportsBudgetCost(t *testing.T) {
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

	out, cost := rt.capAllowedCalls(calls, policy.CapsState{})
	got := make([]string, 0, len(out))
	for _, c := range out {
		got = append(got, string(c.Name))
	}
	assert.Equal(t, []string{"ret.a", "book.a", "term.a", "ret.b", "book.b"}, got)
	assert.Equal(t, 2, cost)
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

func TestCapAllowedCalls_BookkeepingBypassBudget(t *testing.T) {
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
		expected     []string
		expectedCost int
	}{
		{
			name: "no budget set: passes everything through",
			calls: []planner.ToolRequest{
				{Name: "ret.a"}, {Name: "ret.b"}, {Name: "book.a"},
			},
			caps:         policy.CapsState{MaxToolCalls: 0, RemainingToolCalls: 0},
			expected:     []string{"ret.a", "ret.b", "book.a"},
			expectedCost: 2,
		},
		{
			name: "budget=0, mixed batch: drops retrieval, keeps bookkeeping in original order",
			calls: []planner.ToolRequest{
				{Name: "ret.a"}, {Name: "book.a"}, {Name: "ret.b"}, {Name: "book.b"},
			},
			caps:         policy.CapsState{MaxToolCalls: 10, RemainingToolCalls: 0},
			expected:     []string{"book.a", "book.b"},
			expectedCost: 0,
		},
		{
			name: "budget=1, three retrieval: keeps first retrieval only",
			calls: []planner.ToolRequest{
				{Name: "ret.a"}, {Name: "ret.b"}, {Name: "ret.c"},
			},
			caps:         policy.CapsState{MaxToolCalls: 10, RemainingToolCalls: 1},
			expected:     []string{"ret.a"},
			expectedCost: 1,
		},
		{
			name: "budget=1, mixed: keeps first retrieval + all bookkeeping in original order",
			calls: []planner.ToolRequest{
				{Name: "ret.a"}, {Name: "book.a"}, {Name: "ret.b"}, {Name: "book.b"}, {Name: "ret.c"},
			},
			caps:         policy.CapsState{MaxToolCalls: 10, RemainingToolCalls: 1},
			expected:     []string{"ret.a", "book.a", "book.b"},
			expectedCost: 1,
		},
		{
			name: "budget=0, all bookkeeping: passes all through",
			calls: []planner.ToolRequest{
				{Name: "book.a"}, {Name: "book.b"},
			},
			caps:         policy.CapsState{MaxToolCalls: 10, RemainingToolCalls: 0},
			expected:     []string{"book.a", "book.b"},
			expectedCost: 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, cost := rt.capAllowedCalls(tc.calls, tc.caps)
			got := make([]string, 0, len(out))
			for _, c := range out {
				got = append(got, string(c.Name))
			}
			assert.Equal(t, tc.expected, got)
			assert.Equal(t, tc.expectedCost, cost)
		})
	}
}

func TestCapAllowedCalls_TerminalRunBypassesBudget(t *testing.T) {
	rt := newRuntimeWithSpecs(
		newTerminalSpec("term.a"),
		newRetrievalSpec("ret.a"),
	)

	out, cost := rt.capAllowedCalls(
		[]planner.ToolRequest{
			{Name: "ret.a"},
			{Name: "term.a"},
		},
		policy.CapsState{MaxToolCalls: 10, RemainingToolCalls: 0},
	)
	got := make([]string, 0, len(out))
	for _, c := range out {
		got = append(got, string(c.Name))
	}
	assert.Equal(t, []string{"term.a"}, got)
	assert.Zero(t, cost)
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
