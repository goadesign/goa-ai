package planner

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestToolFailureAllowsToolTurn(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		action RecoveryAction
		allows bool
	}{
		{name: "correct call", action: RecoveryCorrectCall, allows: true},
		{name: "replan", action: RecoveryReplan, allows: true},
		{name: "finish", action: RecoveryFinish},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			failure := &ToolFailure{Recovery: RecoveryDirective{Action: tt.action}}

			assert.Equal(t, tt.allows, failure.AllowsToolTurn())
		})
	}
}

func TestCloneToolFailureOwnsMutableState(t *testing.T) {
	t.Parallel()

	minLen := 2
	in := &ToolFailure{
		Kind:  FailureInvalidCall,
		Error: NewToolErrorWithCause("outer", NewToolError("inner")),
		Recovery: RecoveryDirective{
			Action: RecoveryCorrectCall,
			Issues: []*tools.FieldIssue{{
				Field:      "query",
				Constraint: "invalid_length",
				Allowed:    []string{"alpha"},
				MinLen:     &minLen,
			}},
			PriorInput:  rawjson.Message(`{"query":""}`),
			ExampleJSON: rawjson.Message(`{"query":"alpha"}`),
		},
	}

	out := CloneToolFailure(in)

	require.NotNil(t, out)
	require.NotNil(t, out.Error.Cause)
	require.Len(t, out.Recovery.Issues, 1)
	assert.NotSame(t, in.Error, out.Error)
	assert.NotSame(t, in.Error.Cause, out.Error.Cause)
	assert.NotSame(t, in.Recovery.Issues[0], out.Recovery.Issues[0])

	out.Error.Cause.Message = "changed"
	out.Recovery.Issues[0].Allowed[0] = "changed"
	out.Recovery.PriorInput[2] = 'X'
	assert.Equal(t, "inner", in.Error.Cause.Message)
	assert.Equal(t, "alpha", in.Recovery.Issues[0].Allowed[0])
	assert.JSONEq(t, `{"query":""}`, string(in.Recovery.PriorInput))
}

func TestValidateToolFailure(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		mutate  func(*ToolFailure)
		wantErr string
	}{
		{name: "valid provider correction without prior input"},
		{
			name: "missing error",
			mutate: func(failure *ToolFailure) {
				failure.Error = nil
			},
			wantErr: "failure error is invalid",
		},
		{
			name: "unknown kind",
			mutate: func(failure *ToolFailure) {
				failure.Kind = "unknown"
			},
			wantErr: `unknown failure kind "unknown"`,
		},
		{
			name: "unknown recovery",
			mutate: func(failure *ToolFailure) {
				failure.Recovery.Action = "unknown"
			},
			wantErr: `unknown recovery action "unknown"`,
		},
		{
			name: "issues on replan",
			mutate: func(failure *ToolFailure) {
				failure.Recovery.Action = RecoveryReplan
			},
			wantErr: `recovery "replan" cannot carry correction data`,
		},
		{
			name: "malformed issue",
			mutate: func(failure *ToolFailure) {
				failure.Recovery.Issues[0].Field = ""
			},
			wantErr: "correct-call recovery issues are invalid",
		},
		{
			name: "invalid correction kind",
			mutate: func(failure *ToolFailure) {
				failure.Kind = FailureUnavailable
			},
			wantErr: `failure kind "unavailable" cannot require same-tool correction`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			failure := &ToolFailure{
				Kind:  FailureInvalidCall,
				Error: NewToolError("invalid arguments"),
				Recovery: RecoveryDirective{
					Action: RecoveryCorrectCall,
					Issues: []*tools.FieldIssue{{
						Field:      "query",
						Constraint: "missing_field",
					}},
				},
			}
			if test.mutate != nil {
				test.mutate(failure)
			}

			err := ValidateToolFailure(failure)
			if test.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), test.wantErr)
		})
	}
}
