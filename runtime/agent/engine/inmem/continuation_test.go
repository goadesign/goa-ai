package inmem

// A continuation read crosses the same recorded-value transport as planning.
// Retry attempts must receive the original references; errors cannot become
// negative answers.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
)

func TestContinuationActivityRecordsValuesAndErrors(t *testing.T) {
	for _, available := range []bool{false, true} {
		e := New().(*eng)
		attempts := 0
		require.NoError(t, e.RegisterContinuationActivity(t.Context(), "pages", engine.ActivityOptions{
			RetryPolicy: engine.RetryPolicy{MaxAttempts: 2, InitialInterval: time.Millisecond},
		}, func(_ context.Context, input *api.ContinuationActivityInput) (bool, error) {
			attempts++
			assert.Equal(t, "original", input.ToolOutputs[0].ToolCallID)
			input.ToolOutputs[0].ToolCallID = "mutated-reference"
			if attempts == 1 {
				return false, errors.New("temporary read failure")
			}
			return available, nil
		}))
		input := &api.ContinuationActivityInput{ToolOutputs: []*api.ToolOutputRef{{ToolCallID: "original"}}}
		wf := &wfCtx{ctx: t.Context(), eng: e}
		answer, err := wf.ExecuteContinuationActivity(engine.ContinuationActivityCall{Name: "pages", Input: input})
		require.NoError(t, err)
		assert.Equal(t, available, answer)
		assert.Equal(t, "original", input.ToolOutputs[0].ToolCallID)
		assert.Equal(t, 2, attempts)
	}
	e := New().(*eng)
	sentinel := engine.MarkActivityErrorNonRetryable(errors.New("wrong owner"))
	attempts := 0
	require.NoError(t, e.RegisterContinuationActivity(t.Context(), "invalid", engine.ActivityOptions{
		RetryPolicy: engine.RetryPolicy{MaxAttempts: 2},
	}, func(context.Context, *api.ContinuationActivityInput) (bool, error) {
		attempts++
		return false, sentinel
	}))
	wf := &wfCtx{ctx: t.Context(), eng: e}
	_, err := wf.ExecuteContinuationActivity(engine.ContinuationActivityCall{Name: "invalid", Input: &api.ContinuationActivityInput{}})
	require.ErrorIs(t, err, sentinel)
	assert.Equal(t, 1, attempts)
	_, err = wf.ExecuteContinuationActivity(engine.ContinuationActivityCall{Name: "invalid"})
	assert.ErrorContains(t, err, "input is required")
}
