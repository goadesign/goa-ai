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
