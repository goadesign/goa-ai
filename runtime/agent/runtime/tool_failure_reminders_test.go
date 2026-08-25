package runtime

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestToolFailureReminderCarriesCorrectionContract(t *testing.T) {
	t.Parallel()

	got := toolFailureReminder(&planner.ToolResult{
		Name: tools.Ident("svc.read.aggregate"),
		Failure: &planner.ToolFailure{
			Kind:  planner.FailureInvalidCall,
			Error: planner.NewToolError("unknown field parameters"),
			Recovery: planner.RecoveryDirective{
				Action: planner.RecoveryCorrectCall,
				Issues: []*tools.FieldIssue{{
					Field:      "parameters",
					Constraint: "unknown_field",
					Allowed:    []string{"dataset"},
				}},
				PriorInput:  rawjson.Message(`{"parameters":{}}`),
				ExampleJSON: rawjson.Message(`{"dataset":"alarms"}`),
			},
		},
	}, map[string]string{"dataset": "Data source to query."})

	assert.Contains(t, got, "failed tool remains available with correction guidance")
	assert.Contains(t, got, `"field":"parameters"`)
	assert.Contains(t, got, `Field guidance: {"dataset":"Data source to query."}`)
	assert.Contains(t, got, `Example input: {"dataset":"alarms"}`)
	assert.Contains(t, got, `Rejected input: {"parameters":{}}`)
}

func TestToolFailureReminderFinishForbidsRetryButAllowsAdvertisedContinuation(t *testing.T) {
	t.Parallel()

	got := toolFailureReminder(&planner.ToolResult{
		Name:    tools.Ident("svc.read.get_time_series"),
		Failure: testToolFailure(planner.FailureTimeout, planner.RecoveryFinish, "deadline exceeded"),
	}, nil)

	assert.Contains(t, got, "Do not retry this failed tool.")
	assert.Contains(t, got, "advertised continuation")
	assert.NotContains(t, got, "Failure type:")
	assert.NotContains(t, got, "Example input:")
	assert.NotContains(t, got, "Input issues:")
}

func TestBoundsReminderUsesDedicatedContinuationTool(t *testing.T) {
	t.Parallel()

	cursor := "OPAQUE-CURSOR"
	total := 12
	got := boundsReminder(&planner.ToolResult{
		Name: tools.Ident("tools.search"),
		Bounds: &agent.Bounds{
			Returned:   5,
			Total:      &total,
			Truncated:  true,
			NextCursor: &cursor,
		},
	}, "continue_search", "cursor")

	assert.Contains(t, got, "call continue_search")
	assert.NotContains(t, got, "OPAQUE-CURSOR")
	assert.NotContains(t, got, "cursor set")
	assert.NotContains(t, got, "call the same tool again")
}

func TestToolRemindersUseProviderVisibleToolNames(t *testing.T) {
	t.Parallel()

	failure := toolFailureReminder(&planner.ToolResult{
		Name:    tools.Ident("svc.read.get_status"),
		Failure: testToolFailure(planner.FailureInvalidCall, planner.RecoveryReplan, "not available"),
	}, nil)
	cursor := "OPAQUE-CURSOR"
	bounds := boundsReminder(&planner.ToolResult{
		Name: tools.Ident("svc.read.list_events"),
		Bounds: &agent.Bounds{
			Returned:   1,
			Truncated:  true,
			NextCursor: &cursor,
		},
	}, "svc.read.continue_events", "cursor")

	assert.Contains(t, failure, "Tool: svc_read_get_status")
	assert.Contains(t, failure, "Do not repeat this rejected request.")
	assert.NotContains(t, failure, "Change the request")
	assert.NotContains(t, failure, "svc.read.get_status")
	assert.Contains(t, bounds, "Tool: svc_read_list_events")
	assert.Contains(t, bounds, "call svc_read_continue_events")
	assert.NotContains(t, bounds, "svc.read.")
}

func TestSelectRecoveryOutputsRejectsInvalidSelectors(t *testing.T) {
	t.Parallel()

	outputs := []*planner.ToolOutput{
		{
			Name:       "svc.read.get_status",
			ToolCallID: "failed",
			Failure: testToolFailure(
				planner.FailureDomainRejection,
				planner.RecoveryReplan,
				"not available",
			),
		},
		{
			Name:       "svc.read.get_status",
			ToolCallID: "succeeded",
		},
	}
	tests := []struct {
		name    string
		callIDs []string
		want    string
	}{
		{name: "missing", callIDs: []string{"missing"}, want: "absent from planner tool outputs"},
		{name: "duplicate", callIDs: []string{"failed", "failed"}, want: "appears more than once"},
		{name: "successful", callIDs: []string{"succeeded"}, want: "has no failure"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := selectRecoveryOutputs(outputs, tt.callIDs)

			require.ErrorContains(t, err, tt.want)
		})
	}
}
