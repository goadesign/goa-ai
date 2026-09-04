package runtime

// planner_publication_test.go fixes the workflow-history bound for durable
// planner output. Any number of accepted ordered records must use one record
// activity; live model text and thinking use a separate append-only stream.

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/runlog"
)

type countingRecordWorkflowContext struct {
	*testWorkflowContext
	calls   int
	command *api.StorageActivityCommand
	err     error
}

func (w *countingRecordWorkflowContext) ExecuteStorageActivity(call engine.StorageActivityCall) (*api.StorageActivityResult, error) {
	w.calls++
	w.command = call.Command
	return &api.StorageActivityResult{Append: &api.AppendRecordsResult{}}, w.err
}

func TestPlannerPublicationReturnsExhaustedActivityErrorWithoutRescheduling(t *testing.T) {
	publicationErr := errors.New("storage activity retries exhausted")
	wfCtx := &countingRecordWorkflowContext{
		testWorkflowContext: &testWorkflowContext{ctx: context.Background()},
		err:                 publicationErr,
	}

	err := publishPlannerPublicationBatch(wfCtx, []*RecordActivityInput{{EventKey: "event-1"}})

	require.ErrorIs(t, err, publicationErr)
	require.Equal(t, 1, wfCtx.calls)
}

func TestPlannerPublicationSchedulesOneActivityForThousandsOfRecords(t *testing.T) {
	const recordCount = 2_500
	records := make([]*RecordActivityInput, recordCount)
	for index := range records {
		records[index] = &RecordActivityInput{
			Type:        runlog.Type("planner_note"),
			EventKey:    fmt.Sprintf("run-1/planner-publication/batch-1/%d", index),
			RunID:       "run-1",
			AgentID:     "svc.agent",
			SessionID:   "session-1",
			TurnID:      "turn-1",
			TimestampMS: int64(index),
		}
	}
	wfCtx := &countingRecordWorkflowContext{
		testWorkflowContext: &testWorkflowContext{ctx: context.Background()},
	}

	require.NoError(t, publishPlannerPublicationBatch(wfCtx, records))
	require.Equal(t, 1, wfCtx.calls)
	require.NotNil(t, wfCtx.command)
	require.Len(t, wfCtx.command.Append.Records, recordCount)
	for index, record := range wfCtx.command.Append.Records {
		require.Same(t, records[index], record)
		require.Equal(t, records[index].EventKey, record.EventKey)
		require.Equal(t, int64(index), record.TimestampMS)
	}
}
