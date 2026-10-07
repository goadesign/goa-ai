package runtime

// These tests hold workflow time fixed while the recorded read chooses whether
// recovery can fetch another page. They distinguish the ordinary work deadline
// and recovery charge from the direct finalizer's hard deadline.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
)

func TestContinuationDecisionPreservesRecoveryAndFinalizer(t *testing.T) {
	for _, tc := range []struct {
		name      string
		available bool
		remaining int
		expired   bool
		readError bool
		reason    planner.TerminationReason
	}{
		{name: "no pages with capacity", remaining: 1, reason: planner.TerminationReasonToolFailure},
		{name: "no pages without capacity", reason: planner.TerminationReasonToolFailure},
		{name: "no pages after normal deadline", expired: true, reason: planner.TerminationReasonToolFailure},
		{name: "live page spends capacity", available: true, remaining: 1},
		{name: "live page exhausted capacity", available: true, reason: planner.TerminationReasonRecoveryCap},
		{name: "live page after normal deadline", available: true, remaining: 1, expired: true, reason: planner.TerminationReasonTimeBudget},
		{name: "read failure never finalizes", remaining: 1, readError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Unix(100, 0)
			budget := now.Add(time.Minute)
			if tc.expired {
				budget = now
			}
			hard := now.Add(2 * time.Minute)
			initialDeadlines := runDeadlines{Budget: budget, Hard: hard}
			plannerCalls := 0
			loop, wf := newResumeDeadlineTestLoop(t, func() time.Time { return now }, initialDeadlines,
				func(_ context.Context, input *PlanActivityInput) (*PlanActivityOutput, error) {
					plannerCalls++
					if tc.reason == "" {
						assert.Nil(t, input.Finalize)
					} else {
						require.NotNil(t, input.Finalize)
						assert.Equal(t, tc.reason, input.Finalize.Reason)
					}
					return deadlineTestFinalOutput(), nil
				})
			loop.base.HistoryEndID = "selected-end"
			loop.st.Caps.RemainingRecoveryTurns = tc.remaining
			failure := testToolFailure(planner.FailureInternal, planner.RecoveryFinish, "failed")
			loop.st.ToolOutputs = []*planner.ToolOutput{{
				Name: "tools.failed", ToolCallID: "failed-call", CallRunID: "run-1", ResultRunID: "run-1",
				Payload: rawjson.Message(`{}`), Failure: failure,
			}}
			sentinel := errors.New("history store unavailable")
			wf.continuationRead = func(input *api.ContinuationActivityInput) (bool, error) {
				assert.Equal(t, "selected-end", input.HistoryEndID)
				require.Len(t, input.ToolOutputs, 1)
				assert.Equal(t, "failed-call", input.ToolOutputs[0].ToolCallID)
				// Account for time actually spent reading. No path may move the
				// original normal or hard deadline forward.
				now = now.Add(time.Second)
				if tc.readError {
					return false, sentinel
				}
				return tc.available, nil
			}
			batch := deadlineTestResumeBatch()
			batch.recorded = 1
			batch.records = []stepToolRecord{{
				call:      ToolCall{Name: "tools.failed", ToolCallID: "failed-call"},
				callRunID: "run-1", resultRunID: "run-1",
				result: &planner.ToolResult{Name: "tools.failed", ToolCallID: "failed-call", Failure: failure},
			}}
			out, err := loop.advanceStep(batch)
			assert.Equal(t, initialDeadlines, loop.deadlines)
			assert.Equal(t, 2*time.Minute, wf.lastContinuationCall.Options.ScheduleToCloseTimeout)
			if tc.readError {
				require.ErrorIs(t, err, sentinel)
				assert.Nil(t, out)
				assert.Zero(t, plannerCalls)
				assert.Equal(t, tc.remaining, loop.st.Caps.RemainingRecoveryTurns)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, 1, plannerCalls)
			expectedCapacity := tc.remaining
			expectedDeadline := hard
			if tc.available && tc.remaining > 0 {
				expectedCapacity--
			}
			if tc.reason == "" {
				expectedDeadline = budget
			}
			assert.Equal(t, expectedCapacity, loop.st.Caps.RemainingRecoveryTurns)
			assert.Equal(t, expectedDeadline.Sub(now), wf.lastPlannerCall.Options.ScheduleToCloseTimeout)
		})
	}
}
