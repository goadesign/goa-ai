package runtime

import (
	"testing"

	"github.com/stretchr/testify/assert"

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

	assert.Contains(t, got, "Call the same tool again with a corrected payload.")
	assert.Contains(t, got, `"field":"parameters"`)
	assert.Contains(t, got, `Generated field descriptions: {"dataset":"Data source to query."}`)
	assert.Contains(t, got, `Example input: {"dataset":"alarms"}`)
	assert.Contains(t, got, `Rejected input: {"parameters":{}}`)
}

func TestToolFailureReminderFinishForbidsMoreTools(t *testing.T) {
	t.Parallel()

	got := toolFailureReminder(&planner.ToolResult{
		Name:    tools.Ident("svc.read.get_time_series"),
		Failure: testToolFailure(planner.FailureTimeout, planner.RecoveryFinish, "deadline exceeded"),
	}, nil)

	assert.Contains(t, got, "Failure: timeout")
	assert.Contains(t, got, "Do not call more tools.")
	assert.NotContains(t, got, "Example input:")
}
