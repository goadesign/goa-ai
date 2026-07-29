package runtime

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestValidateRecoveryPlan(t *testing.T) {
	t.Parallel()

	rt := New()
	seedTestToolSpecs(
		rt,
		newAnyJSONSpec("catalog.search", "catalog"),
		newAnyJSONSpec("catalog.list", "catalog"),
	)
	correctFailure := &planner.ToolOutput{
		Name:    tools.Ident("catalog.search"),
		Payload: rawjson.Message(`{"query":"bad"}`),
		Failure: testToolFailure(
			planner.FailureInvalidCall,
			planner.RecoveryCorrectCall,
			"query is invalid",
		),
	}
	secondCorrectFailure := &planner.ToolOutput{
		Name:    tools.Ident("catalog.search"),
		Payload: rawjson.Message(`{"query":"also-bad"}`),
		Failure: testToolFailure(
			planner.FailureInvalidCall,
			planner.RecoveryCorrectCall,
			"query is also invalid",
		),
	}
	replanFailure := &planner.ToolOutput{
		Name:    tools.Ident("catalog.search"),
		Payload: rawjson.Message(`{"query":"bad"}`),
		Failure: testToolFailure(
			planner.FailureDomainRejection,
			planner.RecoveryReplan,
			"no matching catalog",
		),
	}
	tests := []struct {
		name     string
		failures []*planner.ToolOutput
		result   *planner.PlanResult
		wantErr  string
	}{
		{
			name:   "no failure permits completion",
			result: &planner.PlanResult{},
		},
		{
			name:     "correct call rejects completion",
			failures: []*planner.ToolOutput{correctFailure},
			result:   &planner.PlanResult{},
			wantErr:  "completed without correcting",
		},
		{
			name:     "correct call accepts changed payload",
			failures: []*planner.ToolOutput{correctFailure},
			result: &planner.PlanResult{ToolCalls: []planner.ToolRequest{{
				Name:    tools.Ident("catalog.search"),
				Payload: rawjson.Message(`{"query":"good"}`),
			}}},
		},
		{
			name:     "correct call rejects unchanged payload",
			failures: []*planner.ToolOutput{correctFailure},
			result: &planner.PlanResult{ToolCalls: []planner.ToolRequest{{
				Name:    tools.Ident("catalog.search"),
				Payload: rawjson.Message(`{"query":"bad"}`),
			}}},
			wantErr: "without changing its payload",
		},
		{
			name: "correct call rejects reordered unchanged payload",
			failures: []*planner.ToolOutput{{
				Name:    tools.Ident("catalog.search"),
				Payload: rawjson.Message(`{"limit":10,"query":"bad"}`),
				Failure: testToolFailure(
					planner.FailureInvalidCall,
					planner.RecoveryCorrectCall,
					"query is invalid",
				),
			}},
			result: &planner.PlanResult{ToolCalls: []planner.ToolRequest{{
				Name:    tools.Ident("catalog.search"),
				Payload: rawjson.Message(`{"query":"bad","limit":10}`),
			}}},
			wantErr: "without changing its payload",
		},
		{
			name:     "correct call rejects another tool",
			failures: []*planner.ToolOutput{correctFailure},
			result: &planner.PlanResult{ToolCalls: []planner.ToolRequest{{
				Name:    tools.Ident("catalog.list"),
				Payload: rawjson.Message(`{}`),
			}}},
			wantErr: "recovery requires correcting",
		},
		{
			name:     "every correct call obligation must be satisfied",
			failures: []*planner.ToolOutput{correctFailure, secondCorrectFailure},
			result: &planner.PlanResult{ToolCalls: []planner.ToolRequest{{
				Name:    tools.Ident("catalog.search"),
				Payload: rawjson.Message(`{"query":"good"}`),
			}}},
			wantErr: "did not correct 1 failed call",
		},
		{
			name:     "multiple corrections satisfy multiple failures",
			failures: []*planner.ToolOutput{correctFailure, secondCorrectFailure},
			result: &planner.PlanResult{ToolCalls: []planner.ToolRequest{
				{
					Name:    tools.Ident("catalog.search"),
					Payload: rawjson.Message(`{"query":"good"}`),
				},
				{
					Name:    tools.Ident("catalog.search"),
					Payload: rawjson.Message(`{"query":"better"}`),
				},
			}},
		},
		{
			name:     "replan permits completion",
			failures: []*planner.ToolOutput{replanFailure},
			result:   &planner.PlanResult{},
		},
		{
			name:     "replan accepts another tool",
			failures: []*planner.ToolOutput{replanFailure},
			result: &planner.PlanResult{ToolCalls: []planner.ToolRequest{{
				Name:    tools.Ident("catalog.list"),
				Payload: rawjson.Message(`{}`),
			}}},
		},
		{
			name:     "replan rejects exact repetition",
			failures: []*planner.ToolOutput{replanFailure},
			result: &planner.PlanResult{ToolCalls: []planner.ToolRequest{{
				Name:    tools.Ident("catalog.search"),
				Payload: rawjson.Message(`{"query":"bad"}`),
			}}},
			wantErr: "without changing its payload",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := rt.validateRecoveryPlan(tt.failures, tt.result)
			if tt.wantErr == "" {
				assert.NoError(t, err)
			} else {
				assert.ErrorContains(t, err, tt.wantErr)
			}
		})
	}
}

func TestPendingRecoveryOutputsDropsWeakerTransitionsWhenFinishIsPresent(t *testing.T) {
	t.Parallel()

	records := []stepToolRecord{
		{
			call: planner.ToolRequest{Name: tools.Ident("catalog.search")},
			result: &planner.ToolResult{Failure: testToolFailure(
				planner.FailureDomainRejection,
				planner.RecoveryReplan,
				"search rejected",
			)},
		},
		{
			call: planner.ToolRequest{Name: tools.Ident("catalog.load")},
			result: &planner.ToolResult{Failure: testToolFailure(
				planner.FailureInternal,
				planner.RecoveryFinish,
				"load failed",
			)},
		},
	}

	assert.Empty(t, pendingRecoveryOutputs(records))
}
