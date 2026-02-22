package temporal

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"go.temporal.io/api/serviceerror"
	"goa.design/goa-ai/runtime/agent/engine"
)

func TestMapSignalError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want error
	}{
		{
			name: "nil",
			err:  nil,
			want: nil,
		},
		{
			name: "not found maps to workflow not found",
			err:  serviceerror.NewNotFound("run not found"),
			want: engine.ErrWorkflowNotFound,
		},
		{
			name: "failed precondition maps to workflow completed",
			err:  serviceerror.NewFailedPrecondition("workflow execution already completed"),
			want: engine.ErrWorkflowCompleted,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := mapSignalError(tc.err)
			if tc.want == nil {
				require.NoError(t, got)
				return
			}
			require.ErrorIs(t, got, tc.want)
		})
	}
}

func TestMapSignalError_PassesThroughUnknownErrors(t *testing.T) {
	t.Parallel()

	want := errors.New("signal transport unavailable")
	got := mapSignalError(want)
	require.ErrorIs(t, got, want)
}
