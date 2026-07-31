package toolerrors

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFromErrorPreservesOuterWrappers(t *testing.T) {
	t.Parallel()

	inner := New("inner failure")
	out := FromError(fmt.Errorf("outer context: %w", inner))

	require.NotNil(t, out)
	assert.Equal(t, "outer context: inner failure", out.Message)
	require.NotNil(t, out.Cause)
	assert.Equal(t, "inner failure", out.Cause.Message)
	assert.NotSame(t, inner, out.Cause)
}

func TestFromErrorStopsAtTerminalToolError(t *testing.T) {
	t.Parallel()

	out := FromError(NewWithCause("outer", New("terminal")))

	require.NoError(t, Validate(out))
	require.NotNil(t, out.Cause)
	assert.Equal(t, "terminal", out.Cause.Message)
	assert.Nil(t, out.Cause.Cause)
}

func TestCloneOwnsCauseChain(t *testing.T) {
	t.Parallel()

	in := NewWithCause("outer", New("inner"))
	out := Clone(in)

	require.NotNil(t, out)
	require.NotNil(t, out.Cause)
	assert.NotSame(t, in, out)
	assert.NotSame(t, in.Cause, out.Cause)

	out.Cause.Message = "changed"
	assert.Equal(t, "inner", in.Cause.Message)
}

func TestValidateRejectsInvalidCauseChains(t *testing.T) {
	t.Parallel()

	cyclic := New("cyclic")
	cyclic.Cause = cyclic
	tests := []struct {
		name string
		err  *ToolError
		want string
	}{
		{name: "missing error", want: "error is required"},
		{name: "empty root message", err: &ToolError{}, want: "cause depth 0"},
		{name: "empty cause message", err: &ToolError{Message: "outer", Cause: &ToolError{}}, want: "cause depth 1"},
		{name: "cycle", err: cyclic, want: "contains a cycle"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.ErrorContains(t, Validate(tt.err), tt.want)
		})
	}
}
