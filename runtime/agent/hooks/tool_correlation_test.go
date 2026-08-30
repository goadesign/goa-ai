// These tests pin the shared schedule/result identity and placement rules used
// by runtime replay, planner hydration, and persisted-data migration.
package hooks

import (
	"testing"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/tools"
)

func TestValidateToolResultPlacement(t *testing.T) {
	t.Parallel()

	schedule := func(runID string) *ToolCallScheduledEvent {
		return NewToolCallScheduledEvent(
			runID,
			"agent",
			"session",
			tools.Ident("service.tools.lookup"),
			"call",
			nil,
			"",
			"parent",
			0,
		)
	}
	result := func(callRunID string) *ToolResultReceivedEvent {
		return NewToolResultReceivedEvent(
			"result-run",
			"agent",
			"session",
			callRunID,
			tools.Ident("service.tools.lookup"),
			"call",
			"parent",
			nil,
			nil,
			"",
			nil,
			0,
			nil,
			nil,
		)
	}
	tests := []struct {
		name          string
		resultRunID   string
		localSchedule *ToolCallScheduledEvent
		result        *ToolResultReceivedEvent
		wantErr       string
	}{
		{
			name:          "same run with matching local schedule",
			resultRunID:   "call-run",
			localSchedule: schedule("call-run"),
			result:        result("call-run"),
		},
		{
			name:        "same run without local schedule",
			resultRunID: "call-run",
			result:      result("call-run"),
			wantErr:     "tool schedule is required",
		},
		{
			name:        "cross run without local schedule",
			resultRunID: "result-run",
			result:      result("call-run"),
		},
		{
			name:          "cross run with local schedule",
			resultRunID:   "result-run",
			localSchedule: schedule("result-run"),
			result:        result("call-run"),
			wantErr:       `cross-run tool result call "call" reuses a schedule from result run "result-run"`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			err := ValidateToolResultPlacement(test.resultRunID, test.localSchedule, test.result)
			if test.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, test.wantErr)
		})
	}
}

func TestValidateToolResultCorrelation(t *testing.T) {
	t.Parallel()

	scheduled := NewToolCallScheduledEvent(
		"call-run",
		"agent",
		"session",
		tools.Ident("service.tools.lookup"),
		"call",
		nil,
		"",
		"parent",
		0,
	)
	validResult := func() *ToolResultReceivedEvent {
		return NewToolResultReceivedEvent(
			"result-run",
			"agent",
			"session",
			"call-run",
			tools.Ident("service.tools.lookup"),
			"call",
			"parent",
			nil,
			nil,
			"",
			nil,
			0,
			nil,
			nil,
		)
	}
	tests := []struct {
		name    string
		change  func(*ToolResultReceivedEvent)
		wantErr string
	}{
		{name: "valid"},
		{
			name: "call run",
			change: func(result *ToolResultReceivedEvent) {
				result.CallRunID = "other-run"
			},
			wantErr: "call run",
		},
		{
			name: "tool call",
			change: func(result *ToolResultReceivedEvent) {
				result.ToolCallID = "other-call"
			},
			wantErr: "scheduled call",
		},
		{
			name: "tool name",
			change: func(result *ToolResultReceivedEvent) {
				result.ToolName = "service.tools.other"
			},
			wantErr: "scheduled name",
		},
		{
			name: "parent",
			change: func(result *ToolResultReceivedEvent) {
				result.ParentToolCallID = "other-parent"
			},
			wantErr: "scheduled parent",
		},
		{
			name: "session",
			change: func(result *ToolResultReceivedEvent) {
				result.SetSessionID("other-session")
			},
			wantErr: "scheduled session",
		},
		{
			name: "agent",
			change: func(result *ToolResultReceivedEvent) {
				result.agentID = "other-agent"
			},
			wantErr: "scheduled agent",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			result := validResult()
			if test.change != nil {
				test.change(result)
			}
			err := ValidateToolResultCorrelation(scheduled, result)
			if test.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, test.wantErr)
		})
	}
}
