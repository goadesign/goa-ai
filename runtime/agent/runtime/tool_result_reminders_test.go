package runtime

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestRetryHintReminderRestrictingCarriesRetryTemplate(t *testing.T) {
	t.Parallel()

	got := retryHintReminder(&planner.ToolResult{
		Name:  tools.Ident("svc.read.aggregate"),
		Error: planner.NewToolError("invalid arguments"),
		RetryHint: &planner.RetryHint{
			Reason:         planner.RetryReasonInvalidArguments,
			Tool:           tools.Ident("svc.read.aggregate"),
			RestrictToTool: true,
			ExampleJSON:    rawjson.Message(`{"dataset":"alarms"}`),
		},
	})

	assert.Contains(t, got, "Restriction: retry the corrected call to svc.read.aggregate.")
	assert.Contains(t, got, `Example input: {"dataset":"alarms"}`)
	assert.NotContains(t, got, "terminal for this tool call")
}

func TestRetryHintReminderTimeoutReadsAsTerminalWithoutRetryTemplate(t *testing.T) {
	t.Parallel()

	// Even if a payload example leaks onto a non-restricting terminal hint, the
	// reminder must not surface it as a retry template.
	got := retryHintReminder(&planner.ToolResult{
		Name:  tools.Ident("svc.read.get_time_series"),
		Error: planner.NewToolError("deadline exceeded"),
		RetryHint: &planner.RetryHint{
			Reason:      planner.RetryReasonTimeout,
			Tool:        tools.Ident("svc.read.get_time_series"),
			ExampleJSON: rawjson.Message(`{"range":"7d"}`),
		},
	})

	assert.Contains(t, got, "Reason: timeout")
	assert.Contains(t, got, "This failure is terminal for this tool call")
	assert.NotContains(t, got, "Example input:")
	assert.NotContains(t, got, "retry the corrected call")
}
